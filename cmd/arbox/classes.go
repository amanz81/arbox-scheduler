package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/lafofo-nivo/arbox-scheduler/internal/arboxapi"
	"github.com/lafofo-nivo/arbox-scheduler/internal/envfile"
)

func newClassesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "classes",
		Short: "List classes from Arbox",
	}
	cmd.AddCommand(newClassesListCmd(), newClassesDiscoverCmd())
	return cmd
}

func newClassesDiscoverCmd() *cobra.Command {
	var envPath string
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover your gym (box) and save ARBOX_BOX_ID / ARBOX_LOCATIONS_BOX_ID to .env",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = envfile.Load(envPath)
			client, _, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			locs, err := client.GetLocations(ctx)
			if err != nil {
				return err
			}
			if len(locs) == 0 {
				return fmt.Errorf("no boxes returned for this account")
			}

			fmt.Println("boxes:")
			for _, b := range locs {
				fmt.Printf("  [%d] %s\n", b.BoxID, b.BoxName)
				for _, l := range b.LocationsBox {
					fmt.Printf("     - location %d: %s\n", l.ID, l.Name)
				}
			}

			// Persist the first box + first location by default. With one gym
			// that's what the member app does too.
			box := locs[0]
			if err := envfile.Upsert(envPath, "ARBOX_BOX_ID", strconv.Itoa(box.BoxID)); err != nil {
				return err
			}
			if len(box.LocationsBox) > 0 {
				if err := envfile.Upsert(envPath, "ARBOX_LOCATIONS_BOX_ID",
					strconv.Itoa(box.LocationsBox[0].ID)); err != nil {
					return err
				}
			}
			fmt.Printf("saved ARBOX_BOX_ID=%d and ARBOX_LOCATIONS_BOX_ID=%d to %s\n",
				box.BoxID,
				firstLocID(box.LocationsBox),
				envPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&envPath, "env-file", defaultEnvPath(), ".env file to read/write")
	return cmd
}

func firstLocID(ls []struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}) int {
	if len(ls) == 0 {
		return 0
	}
	return ls[0].ID
}

func newClassesListCmd() *cobra.Command {
	var (
		days         int
		fromFlag     string
		includeNames []string
		excludeNames []string
		showAll      bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Arbox classes for the next N days (defaults to 7)",
		Long: `Lists real classes from Arbox for a date range.

By default lists the next 7 days starting today. Use --include/--exclude to
filter on class name (matches the Arbox category name, case-insensitive
substring, comma-separated). --all disables filters.

Examples:
  arbox classes list --days 7 --include "Hall A,Hall B"
  arbox classes list --days 14 --exclude "Open Box"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = envfile.Load(".env")

			client, cfgForTZ, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}

			// Resolve locations_box_id, discovering lazily if missing.
			locID, err := ensureLocationsBoxID(cmd.Context(), client)
			if err != nil {
				return err
			}

			// Start date: --from or today in config tz.
			loc := cfgForTZ.Location()
			var start time.Time
			if fromFlag != "" {
				t, err := time.ParseInLocation("2006-01-02", fromFlag, loc)
				if err != nil {
					return fmt.Errorf("--from: %w", err)
				}
				start = t
			} else {
				now := time.Now().In(loc)
				start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
			}

			// The reference member client calls betweenDates one day at a
			// time. Do the same to be safe.
			var all []arboxapi.Class
			for i := 0; i < days; i++ {
				day := start.AddDate(0, 0, i)
				ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
				classes, err := client.GetScheduleDay(ctx, day, locID)
				cancel()
				if err != nil {
					return fmt.Errorf("fetch %s: %w", day.Format("2006-01-02"), err)
				}
				all = append(all, classes...)
			}

			// Apply filters.
			filtered := all
			if !showAll {
				filtered = filterClasses(all, includeNames, excludeNames)
			}

			// Sort chronologically by date + time.
			sort.SliceStable(filtered, func(i, j int) bool {
				if filtered[i].Date != filtered[j].Date {
					return filtered[i].Date < filtered[j].Date
				}
				return filtered[i].Time < filtered[j].Time
			})

			printClassesTable(filtered, loc)

			// Summary line so it's clear what we filtered.
			fmt.Printf("\n%d classes shown (%d fetched across %d days)",
				len(filtered), len(all), days)
			if len(includeNames) > 0 {
				fmt.Printf("; include=%v", includeNames)
			}
			if len(excludeNames) > 0 {
				fmt.Printf("; exclude=%v", excludeNames)
			}
			fmt.Println()
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "number of days to fetch starting from --from (or today)")
	cmd.Flags().StringVar(&fromFlag, "from", "", "start date YYYY-MM-DD (default: today in config tz)")
	cmd.Flags().StringSliceVar(&includeNames, "include", nil, "only show classes whose category name contains any of these (CSV)")
	cmd.Flags().StringSliceVar(&excludeNames, "exclude", nil, "hide classes whose category name contains any of these (CSV)")
	cmd.Flags().BoolVar(&showAll, "all", false, "show all classes (ignore filters)")
	return cmd
}

// filterClasses returns classes whose BoxCategories.Name contains any
// `include` substring AND does not contain any `exclude` substring.
// Both comparisons are case-insensitive. Empty `include` means "keep all".
func filterClasses(classes []arboxapi.Class, include, exclude []string) []arboxapi.Class {
	normalize := func(ss []string) []string {
		out := make([]string, 0, len(ss))
		for _, s := range ss {
			s = strings.TrimSpace(strings.ToLower(s))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	inc := normalize(include)
	exc := normalize(exclude)

	out := classes[:0]
	for _, c := range classes {
		name := strings.ToLower(c.ResolvedCategoryName())
		if len(inc) > 0 {
			match := false
			for _, s := range inc {
				if strings.Contains(name, s) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		skip := false
		for _, s := range exc {
			if strings.Contains(name, s) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		out = append(out, c)
	}
	return out
}

func printClassesTable(classes []arboxapi.Class, loc *time.Location) {
	if len(classes) == 0 {
		fmt.Println("(no classes match)")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DATE\tDAY\tTIME\tCATEGORY\tCOACH\tSPOTS\tFREE\tWAITLIST\tYOU\tSCHEDULE_ID")
	for _, c := range classes {
		day := ""
		if t, err := time.ParseInLocation("2006-01-02", c.Date, loc); err == nil {
			day = t.Weekday().String()[:3]
		}
		coach := ""
		if c.Coach != nil {
			coach = strings.TrimSpace(c.Coach.FullName)
		}
		spots := fmt.Sprintf("%d/%d", c.Registered, c.MaxUsers)
		you := c.YouStatus()
		if you == "" {
			you = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%d\n",
			c.Date, day, c.Time, c.ResolvedCategoryName(), coach,
			spots, c.Free, c.StandBy, you, c.ID)
	}
	_ = tw.Flush()
}

// ensureLocationsBoxID returns the locations_box_id to use.
//
// Resolution order:
//  1. If config.gym is set: ALWAYS re-discover and pick the box+location whose
//     name (or location name) contains that substring (case-insensitive). Saves
//     both IDs back to .env. This corrects a stale env value pointing to the
//     wrong gym.
//  2. Else if ARBOX_LOCATIONS_BOX_ID is in env: use it as-is.
//  3. Else: discover and pick first box's first location (legacy behavior).
func ensureLocationsBoxID(ctx context.Context, client *arboxapi.Client) (int, error) {
	gym := ""
	if c, err := loadValidated(); err == nil {
		gym = strings.TrimSpace(c.Gym)
	}

	if gym == "" {
		if s := os.Getenv("ARBOX_LOCATIONS_BOX_ID"); s != "" {
			n, err := strconv.Atoi(s)
			if err == nil && n > 0 {
				return n, nil
			}
		}
	}

	locs, err := client.GetLocations(ctx)
	if err != nil {
		return 0, err
	}
	if len(locs) == 0 {
		return 0, fmt.Errorf("could not discover locations_box_id from /boxes/locations")
	}
	gymLower := strings.ToLower(gym)
	if gymLower != "" {
		for _, b := range locs {
			boxMatch := strings.Contains(strings.ToLower(b.BoxName), gymLower)
			for _, l := range b.LocationsBox {
				if boxMatch || strings.Contains(strings.ToLower(l.Name), gymLower) {
					_ = envfile.Upsert(defaultEnvPath(), "ARBOX_BOX_ID", strconv.Itoa(b.BoxID))
					_ = envfile.Upsert(defaultEnvPath(), "ARBOX_LOCATIONS_BOX_ID", strconv.Itoa(l.ID))
					fmt.Fprintf(os.Stderr, "[discover] gym=%q matched box=%s (%d), location=%s (%d) — saved to %s\n",
						gym, b.BoxName, b.BoxID, l.Name, l.ID, defaultEnvPath())
					return l.ID, nil
				}
			}
		}
		var avail []string
		for _, b := range locs {
			for _, l := range b.LocationsBox {
				avail = append(avail, fmt.Sprintf("%s / %s", b.BoxName, l.Name))
			}
		}
		return 0, fmt.Errorf("config.gym %q matched none of: %s", gym, strings.Join(avail, "; "))
	}
	if len(locs[0].LocationsBox) == 0 {
		return 0, fmt.Errorf("first box %q has no locations_box", locs[0].BoxName)
	}
	box := locs[0]
	loc := box.LocationsBox[0]
	_ = envfile.Upsert(defaultEnvPath(), "ARBOX_BOX_ID", strconv.Itoa(box.BoxID))
	_ = envfile.Upsert(defaultEnvPath(), "ARBOX_LOCATIONS_BOX_ID", strconv.Itoa(loc.ID))
	fmt.Fprintf(os.Stderr, "[discover] box=%s (%d), location=%s (%d) — saved to %s (set `gym:` in config to disambiguate)\n",
		box.BoxName, box.BoxID, loc.Name, loc.ID, defaultEnvPath())
	return loc.ID, nil
}
