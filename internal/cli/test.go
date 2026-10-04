package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/spec"
)

func testCommand() *cobra.Command {
	var (
		o              engine.TestOptions
		pkgs           []string
		failed, all, v bool
		junit, out     string
	)
	test := &cobra.Command{
		Use:   "test [suite | ./pkg/...]",
		Short: "run a test suite from rig.yaml, or packages without one (go test -json): --failed reruns the last run's failures; without arguments, list the suites",
		Example: `  rig test unit                 # a suite of rig.yaml
  rig test unit --failed        # only what failed last time
  rig test ./pkg/validate/...   # any packages, no suite needed
  rig test load -o report.md    # save the report (tests and the suite's metrics) to a file`,
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if len(args) > 0 && strings.HasPrefix(args[0], ".") {
				a.Spec.Tests = map[string]*spec.TestSuite{"packages": {Name: "packages", Packages: args, Help: "rig test " + strings.Join(args, " ")}}
				args = []string{"packages"}
			}
			if len(args) > 1 {
				return fmt.Errorf("one suite at a time (or package paths starting with ./)")
			}
			if len(args) == 0 {
				var rows [][]string
				for _, n := range a.SuiteNames() {
					s := a.Spec.Tests[n]
					rows = append(rows, []string{n, strings.Join(s.Packages, " "), s.Help})
				}
				if len(rows) == 0 {
					return fmt.Errorf("no tests: in %s", a.Spec.File)
				}
				printTable(os.Stdout, []string{"SUITE", "PACKAGES", "HELP"}, rows)
				return nil
			}
			s, err := a.Suite(args[0])
			if err != nil {
				return err
			}
			base := engine.OptionsFor(s)
			cmdFlags := map[string]bool{}
			for _, f := range []string{"run", "skip", "race", "cover", "short", "failfast", "shuffle", "count", "parallel", "cpu", "timeout", "bench", "benchtime", "benchmem"} {
				cmdFlags[f] = true
			}
			mergeFlags(&base, o, func(name string) bool { return cmdFlags[name] && testFlagChanged(name) })
			if len(pkgs) > 0 {
				base.Packages = pkgs
			}
			jobs := []engine.TestJob{{Options: base}}
			if failed {
				last, err := a.LoadTestRun("last", s.Name)
				if err != nil {
					return err
				}
				if jobs = engine.RerunFailed(last, base); len(jobs) == 0 {
					fmt.Println(green("nothing failed in " + last.ID))
					return nil
				}
			}
			run := a.NewTestRun(s.Name, jobs[0].Options)
			done := make(chan error, 1)
			go func() { done <- a.RunTests(ctx, s, run, jobs) }()
			printed := map[string]bool{}
			tick := time.NewTicker(300 * time.Millisecond)
			defer tick.Stop()
			var runErr error
		loop:
			for {
				select {
				case runErr = <-done:
					break loop
				case <-tick.C:
					printProgress(run, printed, all, v)
				}
			}
			printProgress(run, printed, all, v)
			fmt.Println()
			run.WriteReport(os.Stdout, true)
			fmt.Println(dim("saved as " + run.ID + ": rig test report " + run.ID))
			if junit != "" {
				if err := writeJUnit(run, junit); err != nil {
					return err
				}
			}
			if out != "" {
				if err := engine.WriteReportFile(out, func(w io.Writer) { run.WriteReport(w, false) }); err != nil {
					return err
				}
				fmt.Println(dim("report written to " + out))
			}
			if runErr != nil {
				return runErr
			}
			if n := run.Counts()[engine.TestFail]; n > 0 {
				return fmt.Errorf("%d failed (rig test %s --failed reruns them)", n, s.Name)
			}
			if n := run.FailedPackages(); n > 0 {
				return fmt.Errorf("%d packages failed (build output above)", n)
			}
			return nil
		}),
	}
	f := test.Flags()
	f.StringVar(&o.Run, "run", "", "only tests matching (go test -run)")
	f.StringVar(&o.Skip, "skip", "", "skip tests matching (go test -skip)")
	f.BoolVar(&o.Race, "race", false, "race detector")
	f.BoolVar(&o.Cover, "cover", false, "coverage per package")
	f.BoolVar(&o.Short, "short", false, "go test -short")
	f.BoolVar(&o.Failfast, "failfast", false, "stop at the first failure")
	f.BoolVar(&o.Shuffle, "shuffle", false, "random test order")
	f.IntVar(&o.Count, "count", 0, "run each test n times (1 skips the cache)")
	f.IntVar(&o.Parallel, "parallel", 0, "parallel tests per package")
	f.StringVar(&o.CPU, "cpu", "", "GOMAXPROCS list (go test -cpu)")
	f.DurationVar(&o.Timeout, "timeout", 0, "fail after this long")
	f.StringVar(&o.Bench, "bench", "", "run benchmarks matching (only benchmarks, unless --run)")
	f.StringVar(&o.Benchtime, "benchtime", "", "time or count per benchmark (1s, 100x)")
	f.BoolVar(&o.Benchmem, "benchmem", false, "allocations per benchmark")
	f.StringSliceVar(&pkgs, "pkg", nil, "packages instead of the suite's")
	f.BoolVar(&failed, "failed", false, "rerun the failures of the suite's last run")
	f.BoolVar(&all, "all", false, "print subtests as they finish too (default: top-level tests)")
	f.BoolVarP(&v, "verbose", "v", false, "print the output of failed tests as they fail")
	f.StringVar(&junit, "junit", "", "also write a JUnit XML report to this file")
	f.StringVarP(&out, "output", "o", "", "also write the full report (tests, then the suite's metrics) to this file")
	testFlagChanged = func(name string) bool { return f.Changed(name) }

	var report struct {
		suite, junit, out string
		failed, short     bool
	}
	rep := &cobra.Command{
		Use: "report [run|last]", Short: "a saved run as a report: failures with output, slowest tests, benchmarks, coverage",
		Args: cobra.MaximumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			id := "last"
			if len(args) == 1 {
				id = args[0]
			}
			run, err := a.LoadTestRun(id, report.suite)
			if err != nil {
				return err
			}
			if report.junit != "" {
				return writeJUnit(run, report.junit)
			}
			if report.out != "" {
				return engine.WriteReportFile(report.out, func(w io.Writer) { run.WriteReport(w, report.failed) })
			}
			run.WriteReport(os.Stdout, report.failed)
			return nil
		}),
	}
	rep.Flags().StringVar(&report.suite, "suite", "", "the last run of this suite")
	rep.Flags().BoolVar(&report.failed, "failed", false, "only the failures")
	rep.Flags().StringVar(&report.junit, "junit", "", "write JUnit XML to this file instead")
	rep.Flags().StringVarP(&report.out, "output", "o", "", "write the report to this file instead")
	runs := &cobra.Command{
		Use: "runs [suite]", Short: "saved test runs, newest first",
		Args: cobra.MaximumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			suite := ""
			if len(args) == 1 {
				suite = args[0]
			}
			var rows [][]string
			for _, id := range a.TestRuns(suite) {
				r, err := a.LoadTestRun(id, "")
				if err != nil {
					continue
				}
				rows = append(rows, []string{id, r.Suite, r.Env, r.Summary()})
			}
			printTable(os.Stdout, []string{"RUN", "SUITE", "ENV", "RESULT"}, rows)
			return nil
		}),
	}
	test.AddCommand(rep, runs)
	return test
}

// testFlagChanged reports whether a flag of rig test was given; set when the command is built.
var testFlagChanged = func(string) bool { return false }

// mergeFlags puts the options given on the command line over the suite's.
func mergeFlags(base *engine.TestOptions, o engine.TestOptions, given func(string) bool) {
	if given("run") {
		base.Run = o.Run
	}
	if given("skip") {
		base.Skip = o.Skip
	}
	if given("race") {
		base.Race = o.Race
	}
	if given("cover") {
		base.Cover = o.Cover
	}
	if given("short") {
		base.Short = o.Short
	}
	if given("failfast") {
		base.Failfast = o.Failfast
	}
	if given("shuffle") {
		base.Shuffle = o.Shuffle
	}
	if given("count") {
		base.Count = o.Count
	}
	if given("parallel") {
		base.Parallel = o.Parallel
	}
	if given("cpu") {
		base.CPU = o.CPU
	}
	if given("timeout") {
		base.Timeout = o.Timeout
	}
	if given("bench") {
		base.Bench = o.Bench
	}
	if given("benchtime") {
		base.Benchtime = o.Benchtime
	}
	if given("benchmem") {
		base.Benchmem = o.Benchmem
	}
}

// printProgress prints the tests that finished since the last call: top-level ones (all: every one)
// and packages; verbose adds a failure's output.
func printProgress(run *engine.TestRun, printed map[string]bool, all, verbose bool) {
	run.Read(func(r *engine.TestRun) {
		for _, c := range r.Tests {
			k := c.Package + " " + c.Name
			if printed[k] || c.Status == engine.TestRunning || (c.Depth() > 1 && !all) {
				continue
			}
			printed[k] = true
			mark := map[string]string{engine.TestPass: green("✓"), engine.TestFail: red("✖"), engine.TestSkip: dim("○"), engine.TestStopped: amber("■")}[c.Status]
			name := c.Name
			if name == "" {
				name = dim("package ") + c.Package
				if cov, ok := r.Coverage[c.Package]; ok {
					name += dim(fmt.Sprintf("  coverage %.1f%%", cov))
				}
			}
			fmt.Printf("%s %s %s\n", mark, name, dim(fmt.Sprintf("%.2fs", c.Elapsed)))
			if verbose && c.Status == engine.TestFail && c.Name != "" {
				for _, l := range c.Output {
					fmt.Println("    " + l)
				}
			}
		}
	})
}

func writeJUnit(run *engine.TestRun, file string) error {
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := run.WriteJUnit(f); err != nil {
		return err
	}
	fmt.Println(dim("JUnit report: " + file))
	return nil
}
