package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/engine"
)

func reportCommand() *cobra.Command {
	var since time.Duration
	var from, to, out, source string
	c := &cobra.Command{
		Use:   "report [name]",
		Short: "measure a reports: entry of rig.yaml over a time window and save it as markdown; without a name, list them",
		Example: `  rig report load --since 20m          # the last 20 minutes, saved under ~/.config/rig and printed
  rig report load --from 14:05 --to 14:35 -o load-1000tps.md`,
		Args: cobra.MaximumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if len(args) == 0 {
				var rows [][]string
				for _, n := range a.ReportNames() {
					r := a.Spec.Reports[n]
					rows = append(rows, []string{n, fmt.Sprint(len(r.Metrics)), r.Help})
				}
				if len(rows) == 0 {
					return fmt.Errorf("no reports: in %s", a.Spec.File)
				}
				printTable(os.Stdout, []string{"REPORT", "METRICS", "HELP"}, rows)
				return nil
			}
			end := time.Now()
			start := end.Add(-since)
			var err error
			if from != "" {
				if start, err = clock(from); err != nil {
					return err
				}
			}
			if to != "" {
				if end, err = clock(to); err != nil {
					return err
				}
			}
			rep, err := a.MeasureReport(ctx, args[0], source, start, end)
			if err != nil {
				return err
			}
			if out == "" {
				out = a.ReportFile(args[0], end)
			}
			if err := engine.WriteReportFile(out, rep.Markdown); err != nil {
				return err
			}
			rep.Markdown(os.Stdout)
			fmt.Println(dim("\nsaved to " + out))
			return nil
		}),
	}
	c.Flags().DurationVar(&since, "since", 15*time.Minute, "the window: this long until now")
	c.Flags().StringVar(&from, "from", "", "window start: 15:04, 15:04:05 (today) or RFC 3339")
	c.Flags().StringVar(&to, "to", "", "window end (default now)")
	c.Flags().StringVar(&source, "source", "", "metrics component instead of the report's (local-prom, say)")
	c.Flags().StringVarP(&out, "output", "o", "", "file to write (default under the project's rig data directory)")
	return c
}

// clock reads a time of today (15:04 or 15:04:05) or an RFC 3339 timestamp.
func clock(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			now := time.Now()
			return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q: want 15:04, 15:04:05 or RFC 3339", s)
}
