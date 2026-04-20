package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/amanz81/arbox-scheduler/internal/config"
	"github.com/amanz81/arbox-scheduler/internal/envfile"
	"github.com/amanz81/arbox-scheduler/internal/schedule"
)

var cfgPath string

func main() {
	// Best-effort .env load so commands can read ARBOX_* without an explicit
	// step. Real env vars always win; missing file is fine.
	_ = envfile.Load(defaultEnvPath())

	root := &cobra.Command{
		Use:   "arbox",
		Short: "Auto-book Arbox CrossFit classes",
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", "config.yaml", "path to config YAML")

	scheduleCmd := &cobra.Command{
		Use:   "schedule",
		Short: "Inspect the booking schedule",
	}
	scheduleCmd.AddCommand(
		newScheduleListCmd(),
		newScheduleValidateCmd(),
		newScheduleResolveCmd(),
	)

	root.AddCommand(
		scheduleCmd,
		newBookCmd(),
		newAuthCmd(),
		newClassesCmd(),
		newMeCmd(),
		newDaemonCmd(),
		newTelegramCmd(),
		newSelfTestCmd(),
		newTgPreviewCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadValidated() (*config.Config, error) {
	c, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	if err := c.MergeDaysFromFile(userPlanOverlayPath()); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func newScheduleListCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List planned class options and window-open times (offline — no API)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadValidated()
			if err != nil {
				return err
			}
			now := time.Now().In(c.Location())
			opts, err := schedule.NextOptions(c, now, days)
			if err != nil {
				return err
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "DAY\tCLASS START\tPRI\tCATEGORY\tWINDOW OPEN (local)\tWINDOW OPEN (UTC)\tCOUNTDOWN")
			for _, o := range opts {
				cd := o.WindowOpen.Sub(now).Round(time.Minute)
				if cd < 0 {
					cd = 0
				}
				cat := o.Category
				if cat == "" {
					cat = "(global filter)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
					o.Weekday,
					o.ClassStart.Format("2006-01-02 15:04 MST"),
					o.Priority,
					cat,
					o.WindowOpen.Format("2006-01-02 15:04 MST"),
					o.WindowOpen.UTC().Format("2006-01-02 15:04 MST"),
					cd,
				)
			}
			if len(opts) == 0 {
				fmt.Fprintln(tw, "(no options planned in the next", days, "days — everything disabled?)")
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVar(&days, "days", 7, "number of days to look ahead")
	return cmd
}

func newScheduleValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate the config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			if err := c.Validate(); err != nil {
				return err
			}
			fmt.Println("config OK:", cfgPath)
			return nil
		},
	}
}

