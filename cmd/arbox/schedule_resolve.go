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

		// Global excludes apply to every option, even when this row has an
		// explicit category (Hall A / Hall B / Weightlifting). Example: if
		// "Weightlifting" is excluded, a class named "Weightlifting Hall A"
		// is skipped before Hall A substring matching — so you get "(no match)".
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

// classStartsAt parses class date+time in loc; empty class.Date uses dayKey.
func classStartsAt(cl arboxapi.Class, dayKey string, loc *time.Location) (time.Time, error) {
	dateStr := strings.TrimSpace(cl.Date)
	if dateStr == "" {
		dateStr = dayKey
	} else if i := strings.Index(dateStr, "T"); i > 0 {
		// API sometimes returns ISO datetime in `date`; keep calendar part only.
		dateStr = dateStr[:i]
	}
	tim := strings.TrimSpace(cl.Time)
	if tim == "" {
		return time.Time{}, fmt.Errorf("empty class time")
	}
	combo := dateStr + " " + tim
	for _, lay := range []string{"2006-01-02 15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(lay, combo, loc); err == nil {
			return t, nil
		}
	}
	// e.g. "8:30am" / "2:00pm"
	if t, err := time.ParseInLocation("2006-01-02 3:04pm", dateStr+" "+strings.ToLower(tim), loc); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unparsed time %q date %q", tim, dateStr)
}

func appendCurrentPlanSummary(b *strings.Builder, c *config.Config) {
	b.WriteString("Your saved plan (config.yaml + user_plan overlay):\n")
	n := 0
	for _, dk := range setupWeekdayOrder {
		d, ok := c.Days[dk]
		if !ok {
			continue
		}
		wd, okWD := dayKeyToWeekday[dk]
		if !okWD {
			continue
		}
		n++
		pretty := strings.ToUpper(dk[:1]) + dk[1:]
		if !d.Enabled {
			fmt.Fprintf(b, "· %s: disabled (rest day)\n", pretty)
			continue
		}
		opts := c.OptionsFor(wd)
		if len(opts) == 0 {
			fmt.Fprintf(b, "· %s: enabled but no resolvable time/options\n", pretty)
			continue
		}
		var parts []string
		for _, o := range opts {
			p := o.Time
			if strings.TrimSpace(o.Category) != "" {
				p += " · " + o.Category
			} else {
				p += " · (category from global filter)"
			}
			parts = append(parts, p)
		}
		fmt.Fprintf(b, "· %s — targets in priority order (first wins when booking): %s\n",
			pretty, strings.Join(parts, "  then  "))
	}
	if n == 0 {
		b.WriteString("· (no days: block in config — only defaults apply)\n")
	}
	if dt := strings.TrimSpace(c.DefaultTime); dt != "" {
		fmt.Fprintf(b, "· default_time (bare enabled days): %s\n", dt)
	}
	inc, ex := c.CategoryFilter.Include, c.CategoryFilter.Exclude
	if len(inc) > 0 || len(ex) > 0 {
		fmt.Fprintf(b, "· category_filter — include: %s exclude: %s\n", strings.Join(inc, ", "), strings.Join(ex, ", "))
	}
	b.WriteByte('\n')
}

func scanScheduleStats(allBy map[string][]arboxapi.Class, flt config.CategoryFilter, loc *time.Location, windowStart time.Time, days int, now time.Time) (total, passFilter, parseOK, futureOK int) {
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		for _, cl := range allBy[key] {
			total++
			if !classPassesGlobalFilter(cl.BoxCategories.Name, flt) {
				continue
			}
			passFilter++
			when, err := classStartsAt(cl, key, loc)
			if err != nil {
				continue
			}
			parseOK++
			if when.After(now) {
				futureOK++
			}
		}
	}
	return total, passFilter, parseOK, futureOK
}

func appendNextUpcomingClass(b *strings.Builder, allBy map[string][]arboxapi.Class, flt config.CategoryFilter, loc *time.Location, windowStart time.Time, days int, now time.Time) {
	type hit struct {
		when time.Time
		cl   arboxapi.Class
	}
	var hits []hit
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		for _, cl := range allBy[key] {
			if !classPassesGlobalFilter(cl.BoxCategories.Name, flt) {
				continue
			}
			when, err := classStartsAt(cl, key, loc)
			if err != nil {
				continue
			}
			if !when.After(now) {
				continue
			}
			hits = append(hits, hit{when, cl})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].when.Before(hits[j].when) })

	b.WriteString("Next class on Arbox after right now (must pass category_filter):\n")
	if len(hits) == 0 {
		tot, passF, pOK, fut := scanScheduleStats(allBy, flt, loc, windowStart, days, now)
		b.WriteString("· none listed after now in this window.\n")
		fmt.Fprintf(b, "  Counts for this pull: %d class rows, %d pass filter, %d parseable start time, %d start strictly after now.\n", tot, passF, pOK, fut)
		if tot == 0 {
			b.WriteString("  Hint: if everything is 0, the schedule API returned no rows — often a wrong calendar day was requested before; redeploy after the latest fix.\n")
		} else if passF == 0 {
			b.WriteString("  Hint: every class name was filtered out — widen category_filter include or check exact names in Arbox.\n")
		} else if fut == 0 && pOK > 0 {
			b.WriteString("  Hint: classes are only in the past for this window, or times could not be combined with dates.\n")
		} else if pOK < passF {
			b.WriteString("  Hint: some rows had a time/date format we could not parse; set ARBOX_DEBUG=1 and check logs.\n")
		}
		b.WriteByte('\n')
		return
	}
	chosen := hits[0]
	label := "earliest in window (no open spots in this lookahead)"
	for i := range hits {
		if hits[i].cl.Free > 0 {
			chosen = hits[i]
			label = "next with open spots"
			break
		}
	}
	you := chosen.cl.YouStatus()
	if you == "" {
		you = "-"
	}
	fmt.Fprintf(b, "· %s — %s · %s · free %d · wl %d · you %s · schedule_id %d\n",
		label,
		chosen.when.Format("Mon 02 Jan 15:04"),
		chosen.cl.BoxCategories.Name,
		chosen.cl.Free,
		chosen.cl.StandBy,
		you,
		chosen.cl.ID)
	if chosen.cl.Free == 0 {
		b.WriteString("  (Waitlist may open with the booking window; check Arbox.)\n")
	}
	b.WriteByte('\n')
}

// buildScheduleStatusReport returns a plain-text summary suitable for
// Telegram /status (caller may wrap with MarkdownV2 escapes).
// It mirrors the resolve command's data fetch but uses compact lines.
//
// It fetches each calendar day in the lookahead once, prints merged plan
// summary, next upcoming class, Arbox registrations, then planned matching.
func buildScheduleStatusReport(ctx context.Context, c *config.Config, client *arboxapi.Client, locID, days int) (string, error) {
	loc := c.Location()
	now := time.Now().In(loc)
	opts, err := schedule.NextOptions(c, now, days)
	if err != nil {
		return "", err
	}

	// One pass: full calendar-day schedule for the lookahead (used both for
	// matching planned options and for "already registered" lines).
	windowStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	allBy := make(map[string][]arboxapi.Class)
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		ctx2, cancel := context.WithTimeout(ctx, 20*time.Second)
		classes, err := client.GetScheduleDay(ctx2, d, locID)
		cancel()
		if err != nil {
			return "", fmt.Errorf("fetch %s: %w", key, err)
		}
		allBy[key] = classes
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Timezone: %s\nLookahead: %d days\n\n", c.Timezone, days)
	fmt.Fprintf(&b, "Quick guide:\n"+
		"· A) Your real bookings in Arbox (BOOKED / WAITLIST).\n"+
		"· B) Each line is one booking target from the plan; \"no match\" means that day has no class at that clock time with a matching name.\n"+
		"· C) One line per class slot you care about: the first plan tier that actually found a class (or no tier matched).\n\n")

	appendCurrentPlanSummary(&b, c)
	appendNextUpcomingClass(&b, allBy, c.CategoryFilter, loc, windowStart, days, now)

	writeUserRegistrationsSection(&b, allBy, loc, windowStart, days)

	if len(opts) == 0 {
		fmt.Fprintf(&b, "\nB) Plan vs live schedule (booking targets):\n"+
			"(No planned options in the next %d days — nothing from user_plan/config to match.)\n\n", days)
	} else {
		fmt.Fprintf(&b, "\nB) Plan vs live schedule (each line = one booking target from your plan):\n")
		for _, o := range opts {
			key := o.ClassStart.Format("2006-01-02")
			matches := resolveOption(o, allBy[key], c.CategoryFilter)
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

		b.WriteString("\nC) Best matching class per slot (same slot as B, but only the winning plan tier):\n")
		groups := map[time.Time][]schedule.PlannedOption{}
		var starts []time.Time
		for _, o := range opts {
			if _, ok := groups[o.ClassStart]; !ok {
				starts = append(starts, o.ClassStart)
			}
			groups[o.ClassStart] = append(groups[o.ClassStart], o)
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

		for _, classStart := range starts {
			day := groups[classStart]
			sort.SliceStable(day, func(i, j int) bool { return day[i].Priority < day[j].Priority })
			var winner *arboxapi.Class
			var winnerOpt schedule.PlannedOption
			for _, o := range day {
				matches := resolveOption(o, allBy[o.ClassStart.Format("2006-01-02")], c.CategoryFilter)
				if len(matches) >= 1 {
					w := matches[0]
					winner = &w
					winnerOpt = o
					break
				}
			}
			if winner == nil {
				fmt.Fprintf(&b, "· %s → no plan tier matched a live class | window %s\n",
					classStart.Format("Mon 02 Jan 15:04"),
					day[0].WindowOpen.Format("Mon 02 Jan 15:04"))
				continue
			}
			you := winner.YouStatus()
			if you == "" {
				you = "-"
			}
			fmt.Fprintf(&b, "· %s → %s (plan tier pri %d) | free %d wl %d you %s | id %d | window %s\n",
				classStart.Format("Mon 02 Jan 15:04"),
				winner.BoxCategories.Name, winnerOpt.Priority,
				winner.Free, winner.StandBy, you, winner.ID,
				winnerOpt.WindowOpen.Format("Mon 02 Jan 15:04"))
		}
	}

	return b.String(), nil
}

// writeUserRegistrationsSection lists BOOKED / WAITLIST classes from the
// already-fetched day maps (same source as /status matching).
func writeUserRegistrationsSection(b *strings.Builder, allBy map[string][]arboxapi.Class, loc *time.Location, windowStart time.Time, days int) {
	type line struct {
		when time.Time
		text string
	}
	var lines []line
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		for _, cl := range allBy[key] {
			st := cl.YouStatus()
			if st == "" {
				continue
			}
			when, err := classStartsAt(cl, key, loc)
			if err != nil {
				continue
			}
			lines = append(lines, line{
				when: when,
				text: fmt.Sprintf("· %s %s · %s · %s · schedule_id %d",
					when.Weekday().String()[:3],
					when.Format("Mon 02 Jan 15:04"),
					cl.BoxCategories.Name,
					st,
					cl.ID),
			})
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].when.Before(lines[j].when) })

	b.WriteString("A) Already in Arbox (BOOKED = confirmed seat, WAITLIST = waitlist):\n")
	if len(lines) == 0 {
		b.WriteString("· none in this window.\n")
		b.WriteString("  (If you know you have bookings but this stays empty, Arbox may not mark them on this API response for your account.)\n")
		return
	}
	for _, ln := range lines {
		b.WriteString(ln.text)
		b.WriteByte('\n')
	}
}
