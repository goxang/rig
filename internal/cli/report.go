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
	var from, to, out, source, format, verbosity string
	save := func(a *engine.App, rep *engine.ReportResult) error {
		if format != "" {
			rep.Format = format
		}
		file, err := a.SaveReport(rep, out)
		if err != nil {
			return err
		}
		if err := rep.Write(os.Stdout, engine.ReportFormat(file, rep.Format)); err != nil {
			return err
		}
		fmt.Println(dim("\nsaved to " + file))
		return nil
	}
	verbose := func(a *engine.App, name string) {
		if r := a.Spec.Reports[name]; r != nil && verbosity != "" {
			r.Verbosity = verbosity
		}
	}
	c := &cobra.Command{
		Use:   "report [name]",
		Short: "measure a reports: entry of rig.yaml (metrics, traces, queries) over a time window and save it as markdown, json or xml; without a name, list them",
		Example: `  rig report load --since 20m          # the last 20 minutes, saved under ~/.config/rig and printed
  rig report load --from 14:05 --to 14:35 -o load-1000tps.json
  rig report start load; rig test e2e; rig report stop load   # the window is the tests' run`,
		Args: cobra.MaximumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if len(args) == 0 {
				var rows [][]string
				for _, n := range a.ReportNames() {
					r := a.Spec.Reports[n]
					state := ""
					if at, ok := a.ReporterStarted(n); ok {
						state = "started " + at.Format("15:04:05")
					}
					rows = append(rows, []string{n, fmt.Sprint(len(r.Metrics)), fmt.Sprint(len(r.Traces)), fmt.Sprint(len(r.Queries)), state, r.Help})
				}
				if len(rows) == 0 {
					return fmt.Errorf("no reports: in %s", a.Spec.File)
				}
				printTable(os.Stdout, []string{"REPORT", "METRICS", "TRACES", "QUERIES", "REPORTER", "HELP"}, rows)
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
			verbose(a, args[0])
			rep, err := a.MeasureReport(ctx, args[0], source, start, end)
			if err != nil {
				return err
			}
			return save(a, rep)
		}),
	}
	c.Flags().DurationVar(&since, "since", 15*time.Minute, "the window: this long until now")
	c.Flags().StringVar(&from, "from", "", "window start: 15:04, 15:04:05 (today) or RFC 3339")
	c.Flags().StringVar(&to, "to", "", "window end (default now)")
	c.PersistentFlags().StringVar(&source, "source", "", "metrics component instead of the report's (local-prom, say)")
	c.PersistentFlags().StringVarP(&out, "output", "o", "", "file to write; .json or .xml picks the format (default under the project's rig data directory)")
	c.PersistentFlags().StringVar(&format, "format", "", "md, json or xml (default the report's format:, else md)")
	c.PersistentFlags().StringVar(&verbosity, "verbosity", "", "brief, normal or full (default the report's)")
	c.AddCommand(&cobra.Command{
		Use:   "start <name>",
		Short: "start a reporter: its window opens now and closes at rig report stop",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			at, err := a.StartReporter(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("%s started at %s; rig report stop %s measures it\n", args[0], at.Format("15:04:05"), args[0])
			return nil
		}),
	}, &cobra.Command{
		Use:   "stop <name>",
		Short: "stop a reporter: measure the report from its start until now, print and save it",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			verbose(a, args[0])
			rep, err := a.StopReporter(ctx, args[0], source)
			if err != nil {
				return err
			}
			return save(a, rep)
		}),
	})
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
