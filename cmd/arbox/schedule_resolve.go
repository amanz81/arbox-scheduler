package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/amanz81/arbox-scheduler/internal/arboxapi"
	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/envfile"
	"github.com/amanz81/arbox-scheduler/internal/schedule"
)

// newScheduleResolveCmd resolves each PlannedOption against real Arbox
// classes so we can see — before the booker runs — exactly what would be
// attempted, whether each target actually exists, and current spots.
func newScheduleResolveCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve planned options against real Arbox classes (online — needs auth)",
		Long: `Resolves each planned option to a real Arbox class. Calls the Arbox
API. Useful as a pre-flight check: every "OK" row is a class the booker
could target. "(no match)" rows mean the gym isn't offering that class on
that date; the booker will skip them.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = envfile.Load(".env")

			c, err := loadValidated()
			if err != nil {
				return err
			}
			client, _, err := newAuthedClient(cmd.Context())
			if err != nil {
				return err
			}
			locID, err := ensureLocationsBoxID(cmd.Context(), client)
			if err != nil {
				return err
			}

			loc := c.Location()
			now := time.Now().In(loc)
			opts, err := schedule.NextOptions(c, now, days)
			if err != nil {
				return err
			}
			if len(opts) == 0 {
				fmt.Println("(no planned options in the next", days, "days)")
				return nil
			}

			// Fetch each distinct date once, cache results.
			byDate := map[string][]arboxapi.Class{}
			for _, o := range opts {
				key := o.ClassStart.Format("2006-01-02")
				if _, ok := byDate[key]; ok {
					continue
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
				day := time.Date(o.ClassStart.Year(), o.ClassStart.Month(), o.ClassStart.Day(), 0, 0, 0, 0, loc)
				classes, err := client.GetScheduleDay(ctx, day, locID)
				cancel()
				if err != nil {
					return fmt.Errorf("fetch %s: %w", key, err)
				}
				byDate[key] = classes
			}

			// Render.
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "DAY\tDATE\tTIME\tPRI\tOPTION CATEGORY\tSTATUS\tRESOLVED CATEGORY\tSPOTS\tFREE\tWAITLIST\tYOU\tSCHEDULE_ID\tWINDOW OPEN")
			for _, o := range opts {
				key := o.ClassStart.Format("2006-01-02")
				matches := resolveOption(o, byDate[key], c.CategoryFilter)
				optCat := o.Category
				if optCat == "" {
					optCat = "(global filter)"
				}

				switch len(matches) {
				case 0:
					fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						o.Weekday.String()[:3], key, o.Time, o.Priority, optCat,
						"(no match)", "-", "-", "-", "-", "-", "-",
						o.WindowOpen.Format("2006-01-02 15:04 MST"))
				case 1:
					cl := matches[0]
					status := "OK"
					you := cl.YouStatus()
					if you != "" {
						status = you
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%d/%d\t%d\t%d\t%s\t%d\t%s\n",
						o.Weekday.String()[:3], key, o.Time, o.Priority, optCat,
						status, cl.BoxCategories.Name,
						cl.Registered, cl.MaxUsers, cl.Free, cl.StandBy,
						nonEmpty(you, "-"), cl.ID,
						o.WindowOpen.Format("2006-01-02 15:04 MST"))
				default:
					names := make([]string, 0, len(matches))
					for _, m := range matches {
						names = append(names, m.BoxCategories.Name)
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t-\t-\t-\t-\t-\t%s\n",
						o.Weekday.String()[:3], key, o.Time, o.Priority, optCat,
						"(ambiguous)", strings.Join(names, " | "),
						o.WindowOpen.Format("2006-01-02 15:04 MST"))
				}
			}
			if err := tw.Flush(); err != nil {
				return err
			}

			// Summary: one "actionable" line per date = first OK match per
			// ClassStart, to make the priority logic obvious.
			fmt.Println()
			fmt.Println("preferred per date (priority winner):")
			printPreferred(os.Stdout, opts, byDate, c.CategoryFilter)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "number of days to look ahead")
	return cmd
}

// resolveOption returns the classes from `day` that match this option.
// Matching rules:
//   - class.Time must equal option.Time (HH:MM).
//   - If option.Category is set: case-insensitive substring match.
//   - Otherwise: apply global include list (substring, any).
//   - Global exclude list is always applied.
//
// A well-configured option returns at most one class.
func resolveOption(opt schedule.PlannedOption, day []arboxapi.Class, flt config.CategoryFilter) []arboxapi.Class {
	var out []arboxapi.Class
	wantCat := strings.ToLower(strings.TrimSpace(opt.Category))
	for _, c := range day {
		if c.Time != opt.Time {
			continue
		}
		name := strings.ToLower(c.BoxCategories.Name)

		// Always apply global excludes.
		skip := false
		for _, ex := range flt.Exclude {
			if ex != "" && strings.Contains(name, strings.ToLower(ex)) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		if wantCat != "" {
			if !strings.Contains(name, wantCat) {
				continue
			}
		} else if len(flt.Include) > 0 {
			hit := false
			for _, inc := range flt.Include {
				if inc != "" && strings.Contains(name, strings.ToLower(inc)) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// printPreferred prints one line per distinct ClassStart: the option that
// the priority engine would actually go for first (lowest priority index
// whose option resolves). That's the "intended booking" view.
func printPreferred(w *os.File, opts []schedule.PlannedOption, byDate map[string][]arboxapi.Class, flt config.CategoryFilter) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DAY\tCLASS START\tTARGET CATEGORY\tSPOTS\tFREE\tWAITLIST\tYOU\tSCHEDULE_ID\tWINDOW OPEN")

	// Group by ClassStart and sort priorities within each group.
	type key struct{ Start time.Time }
	groups := map[time.Time][]schedule.PlannedOption{}
	var starts []time.Time
	for _, o := range opts {
		if _, ok := groups[o.ClassStart]; !ok {
			starts = append(starts, o.ClassStart)
		}
		groups[o.ClassStart] = append(groups[o.ClassStart], o)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

	for _, start := range starts {
		day := groups[start]
		sort.SliceStable(day, func(i, j int) bool { return day[i].Priority < day[j].Priority })
		var winner *arboxapi.Class
		var winnerOpt schedule.PlannedOption
		for _, o := range day {
			matches := resolveOption(o, byDate[o.ClassStart.Format("2006-01-02")], flt)
			if len(matches) >= 1 {
				w := matches[0]
				winner = &w
				winnerOpt = o
				break
			}
		}
		if winner == nil {
			fmt.Fprintf(tw, "%s\t%s\t(no option matched)\t-\t-\t-\t-\t-\t%s\n",
				start.Weekday().String()[:3],
				start.Format("2006-01-02 15:04 MST"),
				day[0].WindowOpen.Format("2006-01-02 15:04 MST"))
			continue
		}
		you := winner.YouStatus()
		if you == "" {
			you = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s (pri=%d)\t%d/%d\t%d\t%d\t%s\t%d\t%s\n",
			start.Weekday().String()[:3],
			start.Format("2006-01-02 15:04 MST"),
			winner.BoxCategories.Name, winnerOpt.Priority,
			winner.Registered, winner.MaxUsers,
			winner.Free, winner.StandBy, you, winner.ID,
			winnerOpt.WindowOpen.Format("2006-01-02 15:04 MST"))
	}
	_ = tw.Flush()
}

// buildScheduleStatusReport returns a plain-text summary suitable for
// Telegram /status (caller may wrap with MarkdownV2 escapes).
// It mirrors the resolve command's data fetch but uses compact lines.
func buildScheduleStatusReport(ctx context.Context, c *config.Config, client *arboxapi.Client, locID, days int) (string, error) {
	loc := c.Location()
	now := time.Now().In(loc)
	opts, err := schedule.NextOptions(c, now, days)
	if err != nil {
		return "", err
	}
	if len(opts) == 0 {
		return fmt.Sprintf("No planned options in the next %d days.", days), nil
	}

	byDate := map[string][]arboxapi.Class{}
	for _, o := range opts {
		key := o.ClassStart.Format("2006-01-02")
		if _, ok := byDate[key]; ok {
			continue
		}
		ctx2, cancel := context.WithTimeout(ctx, 20*time.Second)
		day := time.Date(o.ClassStart.Year(), o.ClassStart.Month(), o.ClassStart.Day(), 0, 0, 0, 0, loc)
		classes, err := client.GetScheduleDay(ctx2, day, locID)
		cancel()
		if err != nil {
			return "", fmt.Errorf("fetch %s: %w", key, err)
		}
		byDate[key] = classes
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Timezone: %s\nLookahead: %d days\n\n", c.Timezone, days)
	fmt.Fprintf(&b, "Options:\n")
	for _, o := range opts {
		key := o.ClassStart.Format("2006-01-02")
		matches := resolveOption(o, byDate[key], c.CategoryFilter)
		optCat := o.Category
		if optCat == "" {
			optCat = "(global filter)"
		}
		switch len(matches) {
		case 0:
			fmt.Fprintf(&b, "· %s %s pri%d %s → no match | window %s\n",
				o.Weekday.String()[:3], o.Time, o.Priority, optCat,
				o.WindowOpen.Format("Mon 02 Jan 15:04"))
		case 1:
			cl := matches[0]
			st := cl.YouStatus()
			if st == "" {
				st = "open"
			}
			fmt.Fprintf(&b, "· %s %s pri%d %s → %s | spots %d/%d free %d wl %d | you %s | window %s | id %d\n",
				o.Weekday.String()[:3], o.Time, o.Priority, optCat,
				cl.BoxCategories.Name, cl.Registered, cl.MaxUsers, cl.Free, cl.StandBy,
				st, o.WindowOpen.Format("Mon 02 Jan 15:04"), cl.ID)
		default:
			names := make([]string, 0, len(matches))
			for _, m := range matches {
				names = append(names, m.BoxCategories.Name)
			}
			fmt.Fprintf(&b, "· %s %s pri%d %s → ambiguous: %s | window %s\n",
				o.Weekday.String()[:3], o.Time, o.Priority, optCat,
				strings.Join(names, ", "), o.WindowOpen.Format("Mon 02 Jan 15:04"))
		}
	}

	b.WriteString("\nPreferred per class start (priority winner):\n")
	type key struct{ Start time.Time }
	groups := map[time.Time][]schedule.PlannedOption{}
	var starts []time.Time
	for _, o := range opts {
		if _, ok := groups[o.ClassStart]; !ok {
			starts = append(starts, o.ClassStart)
		}
		groups[o.ClassStart] = append(groups[o.ClassStart], o)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

	for _, start := range starts {
		day := groups[start]
		sort.SliceStable(day, func(i, j int) bool { return day[i].Priority < day[j].Priority })
		var winner *arboxapi.Class
		var winnerOpt schedule.PlannedOption
		for _, o := range day {
			matches := resolveOption(o, byDate[o.ClassStart.Format("2006-01-02")], c.CategoryFilter)
			if len(matches) >= 1 {
				w := matches[0]
				winner = &w
				winnerOpt = o
				break
			}
		}
		if winner == nil {
			fmt.Fprintf(&b, "· %s %s → no option matched | window %s\n",
				start.Weekday().String()[:3],
				start.Format("Mon 02 Jan 15:04"),
				day[0].WindowOpen.Format("Mon 02 Jan 15:04"))
			continue
		}
		you := winner.YouStatus()
		if you == "" {
			you = "-"
		}
		fmt.Fprintf(&b, "· %s %s → %s (pri %d) | free %d wl %d you %s | id %d | window %s\n",
			start.Weekday().String()[:3],
			start.Format("Mon 02 Jan 15:04"),
			winner.BoxCategories.Name, winnerOpt.Priority,
			winner.Free, winner.StandBy, you, winner.ID,
			winnerOpt.WindowOpen.Format("Mon 02 Jan 15:04"))
	}
	return b.String(), nil
}
