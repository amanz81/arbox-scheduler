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
						status, cl.ResolvedCategoryName(),
						cl.Registered, cl.MaxUsers, cl.Free, cl.StandBy,
						nonEmpty(you, "-"), cl.ID,
						o.WindowOpen.Format("2006-01-02 15:04 MST"))
				default:
					names := make([]string, 0, len(matches))
					for _, m := range matches {
						names = append(names, m.ResolvedCategoryName())
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

// hhmm normalizes a clock string to "HH:MM": trims whitespace, drops :SS, and
// pads single-digit hours.
func hhmm(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, ':'); i > 0 {
		// trim seconds if present (HH:MM:SS or HH:MM:SS.mmm)
		if j := strings.IndexByte(s[i+1:], ':'); j >= 0 {
			s = s[:i+1+j]
		}
		if i == 1 { // pad single-digit hour
			s = "0" + s
		}
	}
	return s
}

// resolveOption returns the classes from `day` that match this option.
// Matching rules:
//   - class.Time must equal option.Time (HH:MM, normalized).
//   - If option.Category is set: case-insensitive substring match.
//   - Otherwise: apply global include list (substring, any).
//   - Global exclude list is always applied.
//
// A well-configured option returns at most one class.
func resolveOption(opt schedule.PlannedOption, day []arboxapi.Class, flt config.CategoryFilter) []arboxapi.Class {
	var out []arboxapi.Class
	wantCat := strings.ToLower(strings.TrimSpace(opt.Category))
	wantTime := hhmm(opt.Time)
	for _, c := range day {
		if hhmm(c.Time) != wantTime {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(c.ResolvedCategoryName()))

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
			winner.ResolvedCategoryName(), winnerOpt.Priority,
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

func sampleDistinctCategoryNames(allBy map[string][]arboxapi.Class, windowStart time.Time, days, max int) []string {
	seen := make(map[string]bool)
	var out []string
	for i := 0; i < days; i++ {
		key := windowStart.AddDate(0, 0, i).Format("2006-01-02")
		for _, cl := range allBy[key] {
			n := strings.TrimSpace(cl.ResolvedCategoryName())
			if n == "" {
				n = "(empty title from API)"
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
			if len(out) >= max {
				return out
			}
		}
	}
	return out
}

func scanScheduleStats(allBy map[string][]arboxapi.Class, flt config.CategoryFilter, loc *time.Location, windowStart time.Time, days int, now time.Time) (total, passFilter, parseOK, futureOK int) {
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		for _, cl := range allBy[key] {
			total++
			if !classPassesGlobalFilter(cl.ResolvedCategoryName(), flt) {
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
			if !classPassesGlobalFilter(cl.ResolvedCategoryName(), flt) {
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
			b.WriteString("  Hint: no class title contained your include substrings (after excludes). Add Hebrew/Latin fragments that appear in Arbox, or use a YAML list for include.\n")
			samples := sampleDistinctCategoryNames(allBy, windowStart, days, 8)
			if len(samples) > 0 {
				b.WriteString("  Sample titles from this pull (copy bits into category_filter.include):\n")
				for _, s := range samples {
					fmt.Fprintf(b, "    · %s\n", s)
				}
			}
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
	you := chosen.cl.YouStatusDetail()
	if you == "" {
		you = "-"
	}
	fmt.Fprintf(b, "· %s — %s · %s · free %d · wl %d · you %s · schedule_id %d\n",
		label,
		chosen.when.Format("Mon 02 Jan 15:04"),
		chosen.cl.ResolvedCategoryName(),
		chosen.cl.Free,
		chosen.cl.StandBy,
		you,
		chosen.cl.ID)
	if chosen.cl.Free == 0 {
		b.WriteString("  (Waitlist may open with the booking window; check Arbox.)\n")
	}
	b.WriteByte('\n')
}

// fetchScheduleWindow pulls one Arbox call per requested calendar day and
// re-buckets every returned class by its **own `class.Date`** field. That way
// any API quirk (returning more days than requested, or returning the
// neighbouring day due to TZ rounding) lands the row under the right key, so
// "Sun lookup" never accidentally shows a Sat class.
func fetchScheduleWindow(ctx context.Context, c *config.Config, client *arboxapi.Client, locID, days int) (*time.Location, time.Time, time.Time, map[string][]arboxapi.Class, error) {
	loc := c.Location()
	now := time.Now().In(loc)
	windowStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	allBy := make(map[string][]arboxapi.Class)
	seen := make(map[int]bool)
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		if _, ok := allBy[key]; !ok {
			allBy[key] = []arboxapi.Class{}
		}
		ctx2, cancel := context.WithTimeout(ctx, 20*time.Second)
		classes, err := getScheduleDayCached(ctx2, client, d, locID)
		cancel()
		if err != nil {
			return nil, time.Time{}, time.Time{}, nil, fmt.Errorf("fetch %s: %w", key, err)
		}
		for _, cl := range classes {
			if cl.ID != 0 && seen[cl.ID] {
				continue
			}
			classKey := strings.TrimSpace(cl.Date)
			if i := strings.Index(classKey, "T"); i > 0 {
				classKey = classKey[:i]
			}
			if classKey == "" {
				classKey = key
			}
			allBy[classKey] = append(allBy[classKey], cl)
			if cl.ID != 0 {
				seen[cl.ID] = true
			}
		}
	}
	return loc, now, windowStart, allBy, nil
}

// nextOccurrenceKey returns the YYYY-MM-DD key of the next calendar day
// (within `days` from windowStart) whose weekday matches `wd`. Returns "" if
// none falls inside the window.
func nextOccurrenceKey(windowStart time.Time, wd time.Weekday, days int) string {
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		if d.Weekday() == wd {
			return d.Format("2006-01-02")
		}
	}
	return ""
}

// resolvePlanOptionAvailability returns a short label for the option given the
// classes listed for that calendar day. The returned label is one of:
//   "available (id <n>, you BOOKED|WAITLIST|-)"      — found and category matches
//   "not on schedule (at HH:MM: <names…>)"           — time exists but category mismatch
//   "no class at this time (nearby times: …)"        — no class at that clock time
func resolvePlanOptionAvailability(opt config.ClassOption, classes []arboxapi.Class, flt config.CategoryFilter) string {
	wantCat := strings.ToLower(strings.TrimSpace(opt.Category))
	wantTime := hhmm(opt.Time)
	var timeMatchNames []string
	for _, c := range classes {
		if hhmm(c.Time) != wantTime {
			continue
		}
		nameRaw := c.ResolvedCategoryName()
		timeMatchNames = append(timeMatchNames, nameRaw)
		name := strings.ToLower(strings.TrimSpace(nameRaw))
		// Apply global excludes always.
		exHit := false
		for _, ex := range flt.Exclude {
			if ex != "" && strings.Contains(name, strings.ToLower(ex)) {
				exHit = true
				break
			}
		}
		if exHit {
			continue
		}
		// Per-option category if set; otherwise global include list.
		ok := false
		switch {
		case wantCat != "":
			ok = strings.Contains(name, wantCat)
		case len(flt.Include) > 0:
			for _, inc := range flt.Include {
				if inc != "" && strings.Contains(name, strings.ToLower(inc)) {
					ok = true
					break
				}
			}
		default:
			ok = true
		}
		if !ok {
			continue
		}
		you := c.YouStatusDetail()
		if you == "" {
			you = "-"
		}
		return fmt.Sprintf("available (id %d, you %s)", c.ID, you)
	}
	if len(timeMatchNames) == 0 {
		nearby := nearbyTimes(classes, wantTime, 4)
		if len(nearby) == 0 {
			return "no class at this time (no classes returned for this day)"
		}
		return fmt.Sprintf("no class at this time (nearby times: %s)", strings.Join(nearby, ", "))
	}
	// Show actual category names returned at that time so user can adjust filter.
	return fmt.Sprintf("not on schedule (titles at %s: %s)", wantTime, strings.Join(uniqueStrings(timeMatchNames, 4), ", "))
}

func uniqueStrings(in []string, max int) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}

// nearbyTimes returns up to `max` distinct HH:MM strings from `classes`,
// sorted by absolute distance (in minutes) from `target` (HH:MM). Equal
// distances keep schedule order.
func nearbyTimes(classes []arboxapi.Class, target string, max int) []string {
	tMin, ok := parseHHMMMinutes(target)
	if !ok {
		return nil
	}
	type entry struct {
		t    string
		dist int
	}
	seen := make(map[string]bool)
	var ents []entry
	for _, c := range classes {
		t := hhmm(c.Time)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		cMin, ok := parseHHMMMinutes(t)
		if !ok {
			continue
		}
		d := cMin - tMin
		if d < 0 {
			d = -d
		}
		ents = append(ents, entry{t, d})
	}
	sort.SliceStable(ents, func(i, j int) bool { return ents[i].dist < ents[j].dist })
	if len(ents) > max {
		ents = ents[:max]
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.t)
	}
	return out
}

func parseHHMMMinutes(s string) (int, bool) {
	if len(s) < 4 || s[2] != ':' {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:5])
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return h*60 + m, true
}

// appendPlanSelectionsSimple lists enabled-day targets as saved with a short
// availability tag for the *next* occurrence of each weekday.
func appendPlanSelectionsSimple(b *strings.Builder, c *config.Config, allBy map[string][]arboxapi.Class, windowStart time.Time, days int) {
	b.WriteString("What you selected this week (saved plan, with live Arbox availability):\n")
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
			fmt.Fprintf(b, "· %s: off\n", pretty)
			continue
		}
		opts := c.OptionsFor(wd)
		if len(opts) == 0 {
			fmt.Fprintf(b, "· %s: (no times)\n", pretty)
			continue
		}
		key := nextOccurrenceKey(windowStart, wd, days)
		fmt.Fprintf(b, "· %s (%s):\n", pretty, key)
		for _, o := range opts {
			label := o.Time
			if strings.TrimSpace(o.Category) != "" {
				label += " " + strings.TrimSpace(o.Category)
			}
			avail := "no schedule pulled for this day"
			if key != "" {
				avail = resolvePlanOptionAvailability(o, allBy[key], c.CategoryFilter)
			}
			fmt.Fprintf(b, "    - %s — %s\n", label, avail)
		}
	}
	if n == 0 {
		b.WriteString("· (no days block in config)\n")
	}
}

// buildStatusShortReport is for Telegram /status: one short line per planned weekday.
func buildStatusShortReport(ctx context.Context, c *config.Config, client *arboxapi.Client, locID, days int) (string, error) {
	loc, now, windowStart, allBy, err := fetchScheduleWindow(ctx, c, client, locID, days)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s (%s)\n", now.Format("Mon 02 Jan 15:04 MST"), c.Timezone)
	if ps, err := readPauseState(); err == nil {
		if tag := ps.Summary(now, loc); tag != "" {
			fmt.Fprintf(&b, "%s\n", tag)
		}
	}
	b.WriteByte('\n')
	for _, dk := range setupWeekdayOrder {
		d, ok := c.Days[dk]
		if !ok {
			continue
		}
		wd, okWD := dayKeyToWeekday[dk]
		if !okWD {
			continue
		}
		pretty := strings.ToUpper(dk[:1]) + dk[1:]
		if !d.Enabled {
			fmt.Fprintf(&b, "%s — rest\n", pretty)
			continue
		}
		opts := c.OptionsFor(wd)
		if len(opts) == 0 {
			fmt.Fprintf(&b, "%s — (no options)\n", pretty)
			continue
		}
		key := nextOccurrenceKey(windowStart, wd, days)
		datePart := pretty
		if key != "" {
			if t, err := time.ParseInLocation("2006-01-02", key, loc); err == nil {
				datePart = t.Format("Mon 02 Jan")
			}
		}
		fmt.Fprintf(&b, "%s %s — %s\n", datePart, opts[0].Time, summarizePlanOptionsLive(opts, allBy[key], c.CategoryFilter))
	}

	// List the user's actual bookings + waitlists from the same day maps we
	// already fetched. Reuses the same renderer /schedule resolve uses, so
	// waitlist position (e.g. "WAITLIST 3/7") shows up here too when Arbox
	// reports it.
	b.WriteByte('\n')
	writeUserBookingsSection(&b, allBy, loc, windowStart, days,
		"Your Arbox bookings (BOOKED / WAITLIST):",
		"If you know you have bookings but this stays empty, Arbox may not mark them on this API response for your account.")

	b.WriteString("\nMore: /morning [HH-HH] [days], /evening [HH-HH] [days].\n")
	return b.String(), nil
}

// summarizePlanOptionsLive picks the first plan tier that resolves against the
// live day, otherwise lists tiers and a hint. Output is a single short phrase.
func summarizePlanOptionsLive(opts []config.ClassOption, classes []arboxapi.Class, flt config.CategoryFilter) string {
	// Find the first option that resolves to a class on this day.
	for _, o := range opts {
		switch lab := resolvePlanOptionAvailability(o, classes, flt); {
		case strings.HasPrefix(lab, "available"):
			cat := strings.TrimSpace(o.Category)
			if cat == "" {
				cat = "(filter)"
			}
			// available (id N, you BOOKED|WAITLIST|-)
			tail := strings.TrimPrefix(lab, "available ")
			tail = strings.TrimPrefix(tail, "(")
			tail = strings.TrimSuffix(tail, ")")
			return fmt.Sprintf("%s — %s", cat, tail)
		}
	}
	// No tier resolved — show the priority list compactly + reason from first.
	var names []string
	for _, o := range opts {
		c := strings.TrimSpace(o.Category)
		if c == "" {
			c = "(filter)"
		}
		names = append(names, c)
	}
	reason := resolvePlanOptionAvailability(opts[0], classes, flt)
	return fmt.Sprintf("%s — %s", strings.Join(names, " then "), reason)
}

// buildWeeklyAvailableReport — kept so any external import or future linkage
// still resolves; not wired to any Telegram command. Use /morning week and
// /evening week for the day-by-day live views.
func buildWeeklyAvailableReport(ctx context.Context, c *config.Config, client *arboxapi.Client, locID, days int) (string, error) {
	loc, now, windowStart, allBy, err := fetchScheduleWindow(ctx, c, client, locID, days)
	if err != nil {
		return "", err
	}
	opts, err := schedule.NextOptions(c, now, days)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Timezone: %s\nLookahead: %d days\n", c.Timezone, days)
	fmt.Fprintf(&b, "All class times below use %s (Israel clocks, not a separate UTC view).\n\n", c.Timezone)
	fmt.Fprintf(&b, "Quick guide:\n"+
		"· A) Your real bookings in Arbox (BOOKED / WAITLIST).\n"+
		"· B) Each line is one booking target from the plan; \"no match\" means that day has no class at that clock time with a matching name.\n"+
		"· C) One line per class slot you care about: the first plan tier that actually found a class (or no tier matched).\n\n")

	appendCurrentPlanSummary(&b, c)
	appendNextUpcomingClass(&b, allBy, c.CategoryFilter, loc, windowStart, days, now)

	writeUserBookingsSection(&b, allBy, loc, windowStart, days,
		"A) Already in Arbox (BOOKED / WAITLIST):",
		"If you know you have bookings but this stays empty, Arbox may not mark them on this API response for your account.")

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
				fmt.Fprintf(&b, "· %s %s %s → no match | window %s\n",
					o.Weekday.String()[:3], o.Time, optCat,
					o.WindowOpen.Format("Mon 02 Jan 15:04"))
			case 1:
				cl := matches[0]
				st := cl.YouStatus()
				if st == "" {
					st = "open"
				}
				fmt.Fprintf(&b, "· %s %s %s → %s | spots %d/%d free %d wl %d | you %s | window %s | id %d\n",
					o.Weekday.String()[:3], o.Time, optCat,
					cl.ResolvedCategoryName(), cl.Registered, cl.MaxUsers, cl.Free, cl.StandBy,
					st, o.WindowOpen.Format("Mon 02 Jan 15:04"), cl.ID)
			default:
				names := make([]string, 0, len(matches))
				for _, m := range matches {
					names = append(names, m.ResolvedCategoryName())
				}
				fmt.Fprintf(&b, "· %s %s %s → ambiguous: %s | window %s\n",
					o.Weekday.String()[:3], o.Time, optCat,
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
			fmt.Fprintf(&b, "· %s → %s | free %d wl %d you %s | id %d | window %s\n",
				classStart.Format("Mon 02 Jan 15:04"),
				winner.ResolvedCategoryName(),
				winner.Free, winner.StandBy, you, winner.ID,
				winnerOpt.WindowOpen.Format("Mon 02 Jan 15:04"))
		}
	}

	appendWeeklyByDayListing(&b, allBy, c.CategoryFilter, loc, windowStart, days, 12, now)

	return b.String(), nil
}

// appendWeeklyByDayListing adds a compact future class list per calendar day.
func appendWeeklyByDayListing(b *strings.Builder, allBy map[string][]arboxapi.Class, flt config.CategoryFilter, loc *time.Location, windowStart time.Time, days, maxPerDay int, now time.Time) {
	var sec strings.Builder
	fmt.Fprintf(&sec, "\nD) Upcoming classes by day (pass category_filter, after now, capped per day):\n")
	any := false
	for i := 0; i < days; i++ {
		d := windowStart.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		var rows []arboxapi.Class
		for _, cl := range allBy[key] {
			if !classPassesGlobalFilter(cl.ResolvedCategoryName(), flt) {
				continue
			}
			when, err := classStartsAt(cl, key, loc)
			if err != nil || !when.After(now) {
				continue
			}
			rows = append(rows, cl)
		}
		if len(rows) == 0 {
			continue
		}
		any = true
		fmt.Fprintf(&sec, "%s %s:\n", d.Weekday().String()[:3], key)
		limit := len(rows)
		if limit > maxPerDay {
			limit = maxPerDay
		}
		for j := 0; j < limit; j++ {
			cl := rows[j]
			you := cl.YouStatus()
			if you == "" {
				you = "-"
			}
			fmt.Fprintf(&sec, "  · %s %s · free %d · you %s · id %d\n",
				cl.Time, cl.ResolvedCategoryName(), cl.Free, you, cl.ID)
		}
		if len(rows) > maxPerDay {
			fmt.Fprintf(&sec, "  · … +%d more this day\n", len(rows)-maxPerDay)
		}
	}
	if any {
		b.WriteString(sec.String())
	}
}

// writeUserBookingsSection lists BOOKED / WAITLIST classes from the
// already-fetched day maps.
func writeUserBookingsSection(b *strings.Builder, allBy map[string][]arboxapi.Class, loc *time.Location, windowStart time.Time, days int, title, emptyNote string) {
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
			// Surface waitlist position when Arbox provides it
			// (e.g. "WAITLIST 3/7"); harmless for BOOKED rows.
			statusLine := cl.YouStatusDetail()
			lines = append(lines, line{
				when: when,
				text: fmt.Sprintf("· %s · %s · %s · schedule_id %d",
					when.Format("Mon 02 Jan 15:04"),
					cl.ResolvedCategoryName(),
					statusLine,
					cl.ID),
			})
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].when.Before(lines[j].when) })

	b.WriteString(title)
	b.WriteByte('\n')
	if len(lines) == 0 {
		b.WriteString("· none in this window.\n")
		if emptyNote != "" {
			b.WriteString("  ")
			b.WriteString(emptyNote)
			b.WriteByte('\n')
		}
		return
	}
	for _, ln := range lines {
		b.WriteString(ln.text)
		b.WriteByte('\n')
	}
}
