package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/engine"
	"github.com/MohammadmahdiAhmadi/rig/internal/viz"
)

func dataCommands() []*cobra.Command {
	var comp string
	db := &cobra.Command{Use: "db", Short: "databases: list, create, drop, query, and the adapter's actions (seed, clear)"}
	db.PersistentFlags().StringVar(&comp, "component", "", "database component (default: the first)")
	getDB := func(a *engine.App) (core.Database, string, error) {
		return engine.Get[core.Database](a, core.KindDatabase, comp)
	}
	db.AddCommand(
		&cobra.Command{Use: "list", Short: "list databases", RunE: withApp(func(ctx context.Context, a *engine.App, _ []string) error {
			d, _, err := getDB(a)
			if err != nil {
				return err
			}
			names, err := d.Databases(ctx)
			for _, n := range names {
				fmt.Println(n)
			}
			return err
		})},
		&cobra.Command{Use: "create <name>", Args: cobra.ExactArgs(1), Short: "create a database", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if err := a.Guard(); err != nil {
				return err
			}
			d, _, err := getDB(a)
			if err != nil {
				return err
			}
			return d.CreateDatabase(ctx, args[0])
		})},
		&cobra.Command{Use: "drop <name>", Args: cobra.ExactArgs(1), Short: "drop a database (asks for --yes)", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if !g.yes {
				return fmt.Errorf("dropping %s deletes its data; repeat with --yes", args[0])
			}
			d, _, err := getDB(a)
			if err != nil {
				return err
			}
			return d.DropDatabase(ctx, args[0])
		})},
		&cobra.Command{Use: "query <sql>", Aliases: []string{"q"}, Args: cobra.MinimumNArgs(1), Short: "run SQL; prefix @db to pick a database", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			_, name, err := getDB(a)
			if err != nil {
				return err
			}
			return runQuery(ctx, a, name, strings.Join(args, " "))
		})},
	)

	cache := &cobra.Command{
		Use: "cache <command...>", Short: "run a cache command: rig cache GET key | INFO", Args: cobra.MinimumNArgs(1),
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			c, _, err := engine.Get[core.Cache](a, core.KindCache, comp)
			if err != nil {
				return err
			}
			res, err := c.Do(ctx, args...)
			if err != nil {
				return err
			}
			fmt.Println(res)
			return nil
		}),
	}
	cache.Flags().StringVar(&comp, "component", "", "cache component")

	queue := &cobra.Command{Use: "queue", Short: "message queues: list, purge, publish"}
	queue.PersistentFlags().StringVar(&comp, "component", "", "messaging component")
	getQ := func(a *engine.App) (core.Messaging, string, error) {
		return engine.Get[core.Messaging](a, core.KindMessaging, comp)
	}
	queue.AddCommand(
		&cobra.Command{Use: "list", Short: "queues with depth, consumers and rates", RunE: withApp(func(ctx context.Context, a *engine.App, _ []string) error {
			m, _, err := getQ(a)
			if err != nil {
				return err
			}
			qs, err := m.Queues(ctx)
			if err != nil {
				return err
			}
			var rows [][]string
			for _, q := range qs {
				depth := strconv.Itoa(q.Messages)
				if q.Messages > 0 {
					depth = amber(depth)
				}
				rows = append(rows, []string{q.Name, depth, strconv.Itoa(q.Unacked), strconv.Itoa(q.Consumers), viz.Human(q.InRate, "/s"), viz.Human(q.OutRate, "/s")})
			}
			printTable(os.Stdout, []string{"QUEUE", "MESSAGES", "UNACKED", "CONSUMERS", "IN", "OUT"}, rows)
			return nil
		})},
		&cobra.Command{Use: "purge <queue>", Args: cobra.ExactArgs(1), Short: "drop a queue's messages", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if err := a.Guard(); err != nil {
				return err
			}
			m, _, err := getQ(a)
			if err != nil {
				return err
			}
			return m.Purge(ctx, args[0])
		})},
		&cobra.Command{Use: "publish <exchange/key|queue> <body>", Args: cobra.ExactArgs(2), Short: "publish one message", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if err := a.Guard(); err != nil {
				return err
			}
			m, _, err := getQ(a)
			if err != nil {
				return err
			}
			return m.Publish(ctx, args[0], []byte(args[1]))
		})},
	)

	kv := &cobra.Command{Use: "kv", Short: "key-value store: get, put, ls, rm"}
	kv.PersistentFlags().StringVar(&comp, "component", "", "kv component")
	getKV := func(a *engine.App) (core.KV, string, error) { return engine.Get[core.KV](a, core.KindKV, comp) }
	kv.AddCommand(
		&cobra.Command{Use: "get <key>", Args: cobra.ExactArgs(1), RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			k, _, err := getKV(a)
			if err != nil {
				return err
			}
			v, ok, err := k.Get(ctx, args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%s not found", args[0])
			}
			fmt.Println(string(v))
			return nil
		})},
		&cobra.Command{Use: "put <key> <value|@file>", Args: cobra.ExactArgs(2), RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if err := a.Guard(); err != nil {
				return err
			}
			k, _, err := getKV(a)
			if err != nil {
				return err
			}
			v := []byte(args[1])
			if f, ok := strings.CutPrefix(args[1], "@"); ok {
				if v, err = os.ReadFile(f); err != nil {
					return err
				}
			}
			return k.Put(ctx, args[0], v)
		})},
		&cobra.Command{Use: "ls [prefix]", RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			k, _, err := getKV(a)
			if err != nil {
				return err
			}
			p := ""
			if len(args) > 0 {
				p = args[0]
			}
			keys, err := k.List(ctx, p)
			for _, key := range keys {
				fmt.Println(key)
			}
			return err
		})},
		&cobra.Command{Use: "rm <key>", Args: cobra.ExactArgs(1), RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if err := a.Guard(); err != nil {
				return err
			}
			k, _, err := getKV(a)
			if err != nil {
				return err
			}
			return k.Delete(ctx, args[0])
		})},
	)

	return []*cobra.Command{db, cache, queue, kv, loadCommand(), queryCommand(), doCommand()}
}

func loadCommand() *cobra.Command {
	var force bool
	load := &cobra.Command{Use: "load", Short: "load generators: ls, start, stop, rate, up, down, run"}
	one := func(use, short string, f func(ctx context.Context, a *engine.App, name string, g core.LoadGenerator, args []string) error) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: cobra.MinimumNArgs(1), RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			gen, _, err := engine.Get[core.LoadGenerator](a, core.KindLoad, args[0])
			if err != nil {
				return err
			}
			return f(ctx, a, args[0], gen, args[1:])
		})}
	}
	ls := &cobra.Command{Use: "ls", Short: "generators with rate and counters", RunE: withApp(func(ctx context.Context, a *engine.App, _ []string) error {
		gens, errs := engine.All[core.LoadGenerator](a, core.KindLoad)
		var rows [][]string
		for _, n := range a.Names(core.KindLoad) {
			_, typ, _ := a.Kind(n)
			if err := errs[n]; err != nil {
				rows = append(rows, []string{n, typ, red("error"), "", "", "", err.Error()})
				continue
			}
			st, err := gens[n].Status(ctx)
			if err != nil {
				rows = append(rows, []string{n, typ, red("error"), "", "", "", err.Error()})
				continue
			}
			state := dim("○ idle")
			if st.Running {
				state = green("● running")
			}
			lim := a.LoadLimits(n)
			rows = append(rows, []string{n, typ, state, viz.Human(st.Rate, "/s"), viz.Human(lim.Max, "/s"), fmt.Sprintf("%d/%d", st.Failed, st.Sent), st.Latency.P99.String()})
		}
		printTable(os.Stdout, []string{"GENERATOR", "TYPE", "STATE", "RATE", "MAX", "FAILED/SENT", "P99"}, rows)
		return nil
	})}
	start := one("start <generator>", "start a generator", func(ctx context.Context, a *engine.App, _ string, g core.LoadGenerator, _ []string) error {
		if err := a.Guard(); err != nil {
			return err
		}
		return g.Start(ctx)
	})
	stop := one("stop <generator>", "stop a generator", func(ctx context.Context, a *engine.App, _ string, g core.LoadGenerator, _ []string) error {
		if err := a.Guard(); err != nil {
			return err
		}
		return g.Stop(ctx)
	})
	rate := one("rate <generator> <rps>", "set the rate (capped at the generator's max unless --force)", func(ctx context.Context, a *engine.App, n string, _ core.LoadGenerator, args []string) error {
		if len(args) != 1 {
			return fmt.Errorf("rate <generator> <rps>")
		}
		r, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			return err
		}
		return a.SetRate(ctx, n, r, force)
	})
	rate.Flags().BoolVar(&force, "force", false, "exceed max")
	nudge := func(dir int) func(ctx context.Context, a *engine.App, n string, _ core.LoadGenerator, _ []string) error {
		return func(ctx context.Context, a *engine.App, n string, _ core.LoadGenerator, _ []string) error {
			r, err := a.Nudge(ctx, n, dir)
			if err == nil {
				fmt.Printf("%s → %s\n", n, viz.Human(r, "/s"))
			}
			return err
		}
	}
	upCmd := one("up <generator>", "raise the rate one step", nudge(1))
	downCmd := one("down <generator>", "lower the rate one step", nudge(-1))

	var rateFlag float64
	var dur time.Duration
	run := one("run <generator>", "start, show live numbers, stop on Ctrl-C (in-process generators run only this long)", func(ctx context.Context, a *engine.App, n string, g core.LoadGenerator, _ []string) error {
		if err := a.Guard(); err != nil {
			return err
		}
		if rateFlag > 0 {
			if err := a.SetRate(ctx, n, rateFlag, force); err != nil {
				return err
			}
		}
		if err := g.Start(ctx); err != nil {
			return err
		}
		defer g.Stop(context.Background())
		if dur > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, dur)
			defer cancel()
		}
		var hist []float64
		var lastSent int64
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				fmt.Println()
				return nil
			case <-tick.C:
			}
			st, err := g.Status(ctx)
			if err != nil {
				continue
			}
			got := float64(st.Sent - lastSent)
			lastSent = st.Sent
			hist = append(hist, got)
			fmt.Printf("\r%s %s  target %s  actual %s  failed %d/%d  p50 %s p99 %s   ", bold(n), viz.Sparkline(hist, 30, viz.Palette[0]),
				viz.Human(st.Rate, "/s"), viz.Human(got, "/s"), st.Failed, st.Sent, st.Latency.P50.Round(time.Microsecond), st.Latency.P99.Round(time.Microsecond))
		}
	})
	run.Flags().Float64VarP(&rateFlag, "rate", "r", 0, "rate to run at")
	run.Flags().DurationVarP(&dur, "for", "d", 0, "stop after")
	run.Flags().BoolVar(&force, "force", false, "exceed max")

	load.AddCommand(ls, start, stop, rate, upCmd, downCmd, run)
	return load
}

func queryCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "query [component] [query...]",
		Short: "ad hoc query in a component's language: SQL, PromQL, LogQL, redis, kubectl, http, ...",
		Long:  "With no arguments, lists the components that answer queries and their languages.",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if len(args) < 2 {
				qs := a.Queriers()
				var rows [][]string
				for _, n := range engine.SortedKeys(qs) {
					rows = append(rows, []string{n, qs[n].QueryLanguage()})
				}
				printTable(os.Stdout, []string{"COMPONENT", "LANGUAGE"}, rows)
				return nil
			}
			return runQuery(ctx, a, args[0], strings.Join(args[1:], " "))
		}),
	}
}

func runQuery(ctx context.Context, a *engine.App, comp, q string) error {
	qs := a.Queriers()
	qr, ok := qs[comp]
	if !ok {
		return fmt.Errorf("%s does not answer queries; have %v", comp, engine.SortedKeys(qs))
	}
	t, err := qr.RunQuery(ctx, q)
	if err != nil {
		return err
	}
	printCoreTable(os.Stdout, t)
	return nil
}

func doCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "do [component] [action] [args...]",
		Short: "adapter-specific actions: kind create, db seed, queue purge-all, runtime render, ...",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if len(args) == 0 {
				var rows [][]string
				names := append([]string{"runtime"}, engine.SortedKeys(a.Spec.Components)...)
				for _, n := range names {
					if strings.HasPrefix(n, "default:") {
						continue
					}
					v, err := a.Component(n)
					if err != nil {
						continue
					}
					if ac, ok := v.(core.Actioner); ok {
						for _, act := range ac.Actions() {
							rows = append(rows, []string{n, act.Name, act.Help})
						}
					}
				}
				printTable(os.Stdout, []string{"COMPONENT", "ACTION", "WHAT"}, rows)
				return nil
			}
			v, err := a.Component(args[0])
			if err != nil {
				return err
			}
			ac, ok := v.(core.Actioner)
			if !ok {
				return fmt.Errorf("%s has no actions", args[0])
			}
			if len(args) == 1 {
				for _, act := range ac.Actions() {
					fmt.Printf("%-16s %s\n", act.Name, act.Help)
				}
				return nil
			}
			for _, act := range ac.Actions() {
				if act.Name == args[1] {
					if act.Mutate {
						if err := a.Guard(); err != nil {
							return err
						}
					}
					return act.Run(ctx, args[2:], os.Stdout)
				}
			}
			return fmt.Errorf("%s has no action %q", args[0], args[1])
		}),
	}
}
