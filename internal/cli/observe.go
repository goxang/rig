package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
)

func observeCommands() []*cobra.Command {
	var source string
	var since time.Duration
	metrics := &cobra.Command{
		Use: "metrics [dashboard | -- query]", Short: "dashboard panels as charts, or one query: rig metrics -- 'sum(rate(x[1m]))'",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			m, name, err := engine.Get[core.Metrics](a, core.KindMetrics, source)
			if err != nil {
				return err
			}
			var panels []panel
			switch {
			case len(args) == 1 && a.Spec.Dashboards[args[0]] != nil:
				for _, p := range a.Spec.Dashboards[args[0]] {
					panels = append(panels, panel{title: p.Title, query: p.Query, unit: p.Unit})
				}
			case len(args) > 0:
				q := strings.Join(args, " ")
				panels = []panel{{title: q, query: q}}
			default:
				for _, d := range engine.SortedKeys(a.Spec.Dashboards) {
					for _, p := range a.Spec.Dashboards[d] {
						if p.Source == "" || p.Source == name {
							panels = append(panels, panel{title: p.Title, query: p.Query, unit: p.Unit})
						}
					}
				}
			}
			if len(panels) == 0 {
				return fmt.Errorf("no dashboards in rig.yaml; give a query: rig metrics -- 'up'")
			}
			end := time.Now()
			start := end.Add(-since)
			step := max(since/60, time.Second)
			for _, p := range panels {
				ss, err := m.Range(ctx, p.query, start, end, step)
				fmt.Println(bold(p.title) + dim("  "+p.query))
				if err != nil {
					fmt.Println("  " + red(err.Error()))
					continue
				}
				if len(ss) == 0 {
					fmt.Println(dim("  no data"))
					continue
				}
				fmt.Println(viz.LineChart(viz.FromSeries(ss), 100, 10, p.unit))
			}
			return nil
		}),
	}
	metrics.Flags().StringVar(&source, "source", "", "metrics component (default: the first)")
	metrics.Flags().DurationVar(&since, "since", 15*time.Minute, "time range")

	var tq core.TraceQuery
	traces := &cobra.Command{
		Use: "traces [trace-id]", Short: "recent traces, or one trace as a waterfall",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			t, _, err := engine.Get[core.Tracing](a, core.KindTracing, source)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				spans, err := t.Trace(ctx, args[0])
				if err != nil {
					return err
				}
				fmt.Println(viz.Waterfall(spans, 110))
				return nil
			}
			ts, err := t.Search(ctx, tq)
			if err != nil {
				return err
			}
			var rows [][]string
			for _, s := range ts {
				e := ""
				if s.Error {
					e = red("error")
				}
				rows = append(rows, []string{s.ID, s.Start.Format("15:04:05"), s.Duration.Round(time.Microsecond).String(), fmt.Sprint(s.Spans), s.Root, e})
			}
			printTable(os.Stdout, []string{"TRACE", "START", "DURATION", "SPANS", "ROOT", ""}, rows)
			return nil
		}),
	}
	traces.Flags().StringVar(&source, "source", "", "tracing component")
	traces.Flags().StringVarP(&tq.Service, "service", "s", "", "service")
	traces.Flags().StringVar(&tq.Operation, "op", "", "operation / span name")
	traces.Flags().DurationVar(&tq.MinDuration, "min", 0, "minimum duration")
	traces.Flags().DurationVar(&tq.Lookback, "since", time.Hour, "look back")
	traces.Flags().IntVarP(&tq.Limit, "limit", "n", 20, "how many")

	var pr core.ProfileRequest
	var profiler string
	profile := &cobra.Command{
		Use: "profile <service>", Short: "capture a profile (cpu, heap, ... per profiler) and print its top", Args: cobra.ExactArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			p, name, err := engine.Get[core.Profiler](a, core.KindProfiler, profiler)
			if err != nil {
				return err
			}
			if pr.Service, err = a.Service(args[0]); err != nil {
				return err
			}
			fmt.Printf("capturing %s profile of %s with %s (%s)…\n", pr.Kind, args[0], name, pr.Duration)
			res, err := p.Capture(ctx, pr)
			if err != nil {
				return err
			}
			fmt.Println(green("✓ ") + res.File)
			if res.Summary != "" {
				fmt.Println(res.Summary)
			}
			if res.Format == "pprof" {
				fmt.Println(dim("explore: go tool pprof -http=: " + res.File))
			}
			return nil
		}),
	}
	profile.Flags().StringVarP(&pr.Kind, "kind", "k", "cpu", "profile kind (see the profiler)")
	profile.Flags().DurationVarP(&pr.Duration, "duration", "d", 15*time.Second, "for sampled profiles")
	profile.Flags().StringVarP(&pr.Instance, "instance", "i", "", "instance")
	profile.Flags().StringVarP(&pr.OutDir, "out", "o", "", "directory (default .rig/<env>/profiles)")
	profile.Flags().StringVar(&profiler, "profiler", "", "profiler component")

	var dinst string
	debug := &cobra.Command{
		Use: "debug <service>", Short: "attach a debugger server to a running service and keep it until Ctrl-C", Args: cobra.ExactArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			d, _, err := engine.Get[core.Debugger](a, core.KindDebugger, "")
			if err != nil {
				return err
			}
			s, err := a.Service(args[0])
			if err != nil {
				return err
			}
			sess, err := d.Attach(ctx, s, dinst)
			if err != nil {
				return err
			}
			fmt.Println(green("✓ debugger on ") + bold(sess.Addr))
			fmt.Println("  " + sess.Hint)
			fmt.Println(dim("  Ctrl-C detaches"))
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt)
			select {
			case <-sig:
			case <-ctx.Done():
			}
			if sess.Close != nil {
				return sess.Close()
			}
			return nil
		}),
	}
	debug.Flags().StringVarP(&dinst, "instance", "i", "", "instance")

	return []*cobra.Command{metrics, traces, profile, debug}
}

type panel struct{ title, query, unit string }
