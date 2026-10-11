package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/viz"
)

func loadRunCommand() *cobra.Command {
	var (
		rate    float64
		dur     time.Duration
		profile string
		asserts []string
		vs      string
		force   bool
	)
	c := &cobra.Command{
		Use:   "run <load run | generator>",
		Short: "run a load: entry (or a generator) with live numbers, then check its assertions; Ctrl-C ends it early",
		Example: `  rig load run smoke                          # a load: entry of rig.yaml
  rig load run api-load --rate 500 --for 2m --profile ramp --assert "p95 < 200ms" --vs last`,
		Args: cobra.ExactArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			r, err := a.LoadRunNamed(args[0])
			if err != nil {
				return err
			}
			if rate > 0 {
				r.Rate = rate
			}
			if dur > 0 {
				r.For = dur
			}
			if profile != "" {
				r.Profile = profile
			}
			r.Assert = append(r.Assert, asserts...)
			var hist []float64
			var lastSent int64
			// a terminal redraws one line; a pipe (an agent, CI, a log file) gets a line every 5s
			live := term.IsTerminal(int(os.Stdout.Fd()))
			res, err := a.RunLoad(ctx, args[0], r, force, func(st core.LoadStatus) {
				got := float64(st.Sent - lastSent)
				if lastSent == 0 {
					got = 0
				}
				lastSent = st.Sent
				hist = append(hist, got)
				line := fmt.Sprintf("%s  target %s  actual %s  failed %d/%d  p50 %s p99 %s", r.Generator, viz.Human(st.Rate, "/s"), viz.Human(got, "/s"),
					st.Failed, st.Sent, st.Latency.P50.Round(time.Microsecond), st.Latency.P99.Round(time.Microsecond))
				if !live {
					if len(hist)%5 == 0 {
						fmt.Println(line)
					}
					return
				}
				fmt.Printf("\r%s %s   ", viz.Sparkline(hist, 30, viz.Palette[0]), line)
			})
			if live {
				fmt.Println()
			}
			if err != nil {
				return err
			}
			fmt.Printf("%s %s: %d sent, %d failed (%.2f%%), %.1f/s, p50 %s p95 %s p99 %s over %s\n", bold(res.ID), res.Name, res.Sent, res.Failed,
				res.ErrorRate(), res.Throughput, res.P50.Round(time.Microsecond), res.P95.Round(time.Microsecond), res.P99.Round(time.Microsecond),
				time.Duration(res.Seconds*float64(time.Second)).Round(time.Second))
			for _, ch := range res.Checks {
				mark := green("✓")
				if !ch.OK {
					mark = red("✗")
				}
				fmt.Printf("  %s %s  (got %s)\n", mark, ch.Assert, ch.Got)
			}
			if vs != "" {
				base, err := a.LoadResultFor(vs, res.Name, res.ID)
				if err != nil {
					return err
				}
				fmt.Printf("vs %s: throughput %s  errors %s  p95 %s  p99 %s\n", base.ID, delta(res.Throughput, base.Throughput),
					delta(res.ErrorRate(), base.ErrorRate()), delta(float64(res.P95), float64(base.P95)), delta(float64(res.P99), float64(base.P99)))
			}
			if !res.Passed() {
				return fmt.Errorf("load run %s: assertions %w", res.Name, engine.ErrFailed)
			}
			return nil
		}),
	}
	f := c.Flags()
	f.Float64Var(&rate, "rate", 0, "target rate instead of the run's")
	f.DurationVarP(&dur, "for", "d", 0, "how long (open-ended runs stop on Ctrl-C)")
	f.StringVar(&profile, "profile", "", "steady, ramp, spike or soak")
	f.StringArrayVar(&asserts, "assert", nil, `a check on the result, repeatable: "p95 < 200ms", "errors < 0.5%", "throughput >= 1500/s"`)
	f.StringVar(&vs, "vs", "", "compare with a saved run: last, or its id")
	f.BoolVar(&force, "force", false, "exceed the generator's max")
	return c
}

// delta is the change from base as a signed percent.
func delta(now, base float64) string {
	if base == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%+.1f%%", 100*(now-base)/base)
}
