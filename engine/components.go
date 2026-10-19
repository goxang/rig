package engine

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/kernel/actions"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/manifest"
	"github.com/goxang/rig/protocol"
)

// components registers what the Data, KV, Traces, Load, Hosts and Tests screens do as sources and
// actions over the components rig.yaml names, so every client gets them through the engine.
func (l *Local) components() {
	a, reg := l.App, l.runner.Actions
	src := func(name, help string, f sourceFunc) {
		l.about[name] = protocol.SourceSpec{Description: help}
		l.sources[name] = f
	}
	act := func(name, help string, effect string, f func(ctx context.Context, args protocol.Args) (string, error)) {
		_ = reg.Add(actions.Action{Name: name, Effect: effect, Help: help,
			Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
				msg, err := f(ctx, args)
				if err != nil {
					return nil, err
				}
				return &protocol.RunResult{Message: msg}, nil
			}})
	}

	src("components", "every component: kind, adapter and what it can do (browse, query, edit, queues, actions)", func(ctx context.Context, _ protocol.Args) (*protocol.Frame, error) {
		var rows []map[string]any
		for _, n := range SortedKeys(a.Spec.Components) {
			k, typ, err := a.Kind(n)
			if err != nil || strings.HasPrefix(n, "default:") {
				continue
			}
			r := map[string]any{"name": n, "kind": string(k), "type": typ}
			if v, err := a.Component(n); err == nil {
				var caps []string
				for c, ok := range map[string]bool{"browse": is[core.Browser](v), "query": is[core.Querier](v), "edit": is[core.Editor](v),
					"queues": is[core.Messaging](v), "inspect": is[core.QueueInspector](v), "actions": is[core.Actioner](v), "suggest": is[core.PathQuerier](v)} {
					if ok {
						caps = append(caps, c)
					}
				}
				sort.Strings(caps)
				r["caps"] = strings.Join(caps, " ")
				if q, ok := core.As[core.Querier](v); ok {
					r["language"] = q.QueryLanguage()
				}
			}
			rows = append(rows, r)
		}
		return frames.FromRows(strFields("name", "kind", "type", "caps", "language"), rows), nil
	})

	src("data.browse", "walk a database, cache or queue component as a tree: component, path (list); a leaf frame is named leaf", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		b, err := as[core.Browser](a, args)
		if err != nil {
			return nil, err
		}
		t, leaf, err := b.Browse(ctx, strs(args["path"]))
		if err != nil {
			return nil, err
		}
		f := tableFrame(t)
		if leaf {
			f.Name = "leaf"
		}
		return f, nil
	})
	src("data.query", "run a query on a component: component, query, and path where a browse stands (its database)", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		c, err := component(a, args)
		if err != nil {
			return nil, err
		}
		q, _ := args["query"].(string)
		path := strs(args["path"])
		if pq, ok := core.As[core.PathQuerier](c); ok && len(path) > 0 {
			t, err := pq.QueryAt(ctx, path, q)
			return tableFrame(t), err
		}
		t, err := a.RunQuery(ctx, fmt.Sprint(args["component"]), q)
		return tableFrame(t), err
	})
	src("data.suggest", "the query a component suggests for a path (a procedure's call, a SELECT): component, path", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		pq, err := as[core.PathQuerier](a, args)
		if err != nil {
			return nil, err
		}
		return frames.FromRows(strFields("query"), []map[string]any{{"query": pq.SuggestQuery(ctx, strs(args["path"]))}}), nil
	})
	src("data.queues", "a messaging component's queues: depth, consumers, rates", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		m, err := as[core.Messaging](a, args)
		if err != nil {
			return nil, err
		}
		qs, err := m.Queues(ctx)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		for _, q := range qs {
			rows = append(rows, map[string]any{"queue": q.Name, "messages": q.Messages, "ready": q.Ready, "unacked": q.Unacked, "consumers": q.Consumers, "in": q.InRate, "out": q.OutRate})
		}
		return frames.FromRows([]protocol.Field{{Name: "queue", Type: protocol.TypeString}, {Name: "messages", Type: protocol.TypeInt}, {Name: "ready", Type: protocol.TypeInt},
			{Name: "unacked", Type: protocol.TypeInt}, {Name: "consumers", Type: protocol.TypeInt}, {Name: "in", Type: protocol.TypeNumber}, {Name: "out", Type: protocol.TypeNumber}}, rows), nil
	})
	src("data.peek", "a queue's settings (detail: info) or its first messages, which stay queued: component, queue, n", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		qi, err := as[core.QueueInspector](a, args)
		if err != nil {
			return nil, err
		}
		queue, _ := args["queue"].(string)
		if args["detail"] == "info" {
			t, err := qi.QueueInfo(ctx, queue)
			return tableFrame(t), err
		}
		t, err := qi.Peek(ctx, queue, max(1, num(args["n"], 20)))
		return tableFrame(t), err
	})
	act("data.purge", "empty a queue: component, queue", protocol.EffectDanger, func(ctx context.Context, args protocol.Args) (string, error) {
		m, err := as[core.Messaging](a, args)
		if err != nil {
			return "", err
		}
		q, _ := args["queue"].(string)
		return "purged " + q, m.Purge(ctx, q)
	})
	act("data.delete", "delete rows of what a browse of path shows: component, path, rows (their indexes)", protocol.EffectDanger, func(ctx context.Context, args protocol.Args) (string, error) {
		e, t, err := browsed[core.Editor](ctx, a, args)
		if err != nil {
			return "", err
		}
		var rows []int
		for _, r := range strs(args["rows"]) {
			if i, err := strconv.Atoi(r); err == nil && i >= 0 && i < len(t.Rows) {
				rows = append(rows, i)
			}
		}
		if len(rows) == 0 {
			return "", fmt.Errorf("no rows to delete")
		}
		return fmt.Sprintf("deleted %d rows", len(rows)), e.Delete(ctx, strs(args["path"]), t, rows)
	})
	act("data.set", "write one cell of what a browse of path shows: component, path, row, column, value", protocol.EffectWrite, func(ctx context.Context, args protocol.Args) (string, error) {
		e, t, err := browsed[core.Editor](ctx, a, args)
		if err != nil {
			return "", err
		}
		col := -1
		for i, c := range t.Columns {
			if c == fmt.Sprint(args["column"]) {
				col = i
			}
		}
		row := num(args["row"], -1)
		if col < 0 || row < 0 || row >= len(t.Rows) {
			return "", fmt.Errorf("no cell %v of row %v", args["column"], args["row"])
		}
		value, _ := args["value"].(string)
		return "saved", e.Set(ctx, strs(args["path"]), t, row, col, value)
	})

	src("kv.list", "the keys under a prefix of a config store, with their values: component, prefix", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		kv, err := kvOf(a, args)
		if err != nil {
			return nil, err
		}
		prefix, _ := args["prefix"].(string)
		vals := map[string][]byte{}
		if r, ok := core.As[core.KVReader](kv); ok {
			if vals, err = r.All(ctx, prefix); err != nil {
				return nil, err
			}
		} else {
			keys, err := kv.List(ctx, prefix)
			if err != nil {
				return nil, err
			}
			for _, k := range keys {
				v, _, err := kv.Get(ctx, k)
				if err != nil {
					return nil, err
				}
				vals[k] = v
			}
		}
		var rows []map[string]any
		for _, k := range SortedKeys(vals) {
			rows = append(rows, map[string]any{"key": k, "value": string(vals[k]), "size": len(vals[k])})
		}
		return frames.FromRows([]protocol.Field{{Name: "key", Type: protocol.TypeString}, {Name: "value", Type: protocol.TypeString}, {Name: "size", Type: protocol.TypeInt}}, rows), nil
	})
	act("kv.put", "write a key of a config store: component, key, value", protocol.EffectWrite, func(ctx context.Context, args protocol.Args) (string, error) {
		kv, err := kvOf(a, args)
		if err != nil {
			return "", err
		}
		k, _ := args["key"].(string)
		v, _ := args["value"].(string)
		return "saved " + k, kv.Put(ctx, k, []byte(v))
	})
	act("kv.delete", "delete keys of a config store: component, keys", protocol.EffectDanger, func(ctx context.Context, args protocol.Args) (string, error) {
		kv, err := kvOf(a, args)
		if err != nil {
			return "", err
		}
		keys := strs(args["keys"])
		for _, k := range keys {
			if err := kv.Delete(ctx, k); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("deleted %d keys", len(keys)), nil
	})

	src("traces.services", "the services the tracing backend has seen", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		tr, _, err := Get[core.Tracing](a, core.KindTracing, str(args["component"]))
		if err != nil {
			return nil, err
		}
		svcs, err := tr.Services(ctx)
		if err != nil {
			return nil, err
		}
		rows := make([]map[string]any, len(svcs))
		for i, s := range svcs {
			rows[i] = map[string]any{"service": s}
		}
		return frames.FromRows(strFields("service"), rows), nil
	})
	src("traces.search", "find traces: service, operation, min_duration (200ms), lookback (1h), limit", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		tr, _, err := Get[core.Tracing](a, core.KindTracing, str(args["component"]))
		if err != nil {
			return nil, err
		}
		q := core.TraceQuery{Service: str(args["service"]), Operation: str(args["operation"]), Limit: num(args["limit"], 50)}
		q.MinDuration, _ = time.ParseDuration(str(args["min_duration"]))
		if q.Lookback, _ = time.ParseDuration(str(args["lookback"])); q.Lookback == 0 {
			q.Lookback = time.Hour
		}
		ts, err := tr.Search(ctx, q)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		for _, t := range ts {
			rows = append(rows, map[string]any{"trace": t.ID, "root": t.Root, "service": strings.Join(t.Services, ","), "start": t.Start, "duration": t.Duration.Seconds(), "spans": t.Spans, "error": t.Error})
		}
		return frames.FromRows([]protocol.Field{{Name: "trace", Type: protocol.TypeString}, {Name: "root", Type: protocol.TypeString}, {Name: "service", Type: protocol.TypeString},
			{Name: "start", Type: protocol.TypeTime}, {Name: "duration", Type: protocol.TypeDuration}, {Name: "spans", Type: protocol.TypeInt}, {Name: "error", Type: protocol.TypeBool}}, rows), nil
	})
	src("traces.get", "a trace's spans: trace", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		tr, _, err := Get[core.Tracing](a, core.KindTracing, str(args["component"]))
		if err != nil {
			return nil, err
		}
		spans, err := tr.Trace(ctx, str(args["trace"]))
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		for _, s := range spans {
			tags := make([]string, 0, len(s.Tags))
			for _, k := range SortedKeys(s.Tags) {
				tags = append(tags, k+"="+s.Tags[k])
			}
			rows = append(rows, map[string]any{"id": s.ID, "parent": s.Parent, "service": s.Service, "name": s.Name, "start": s.Start, "duration": s.Duration.Seconds(), "error": s.Error, "tags": strings.Join(tags, " ")})
		}
		return frames.FromRows([]protocol.Field{{Name: "id", Type: protocol.TypeString}, {Name: "parent", Type: protocol.TypeString}, {Name: "service", Type: protocol.TypeString},
			{Name: "name", Type: protocol.TypeString}, {Name: "start", Type: protocol.TypeTime}, {Name: "duration", Type: protocol.TypeDuration}, {Name: "error", Type: protocol.TypeBool}, {Name: "tags", Type: protocol.TypeString}}, rows), nil
	})

	src("load.status", "every load generator (or component's): running, rate, sent, failed, latency, replicas", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		names := a.Names(core.KindLoad)
		if c := str(args["component"]); c != "" {
			names = []string{c}
		}
		rows := make([]map[string]any, len(names))
		var wg sync.WaitGroup
		for i, n := range names {
			wg.Go(func() {
				r := map[string]any{"name": n}
				rows[i] = r
				g, _, err := Get[core.LoadGenerator](a, core.KindLoad, n)
				if err == nil {
					c, cancel := context.WithTimeout(ctx, 5*time.Second)
					defer cancel()
					var st core.LoadStatus
					if st, err = g.Status(c); err == nil {
						r["running"], r["rate"], r["sent"], r["failed"] = st.Running, st.Rate, st.Sent, st.Failed
						r["p50"], r["p95"], r["p99"] = st.Latency.P50.Seconds(), st.Latency.P95.Seconds(), st.Latency.P99.Seconds()
					}
					if ls, ok := core.As[core.LoadScaler](g); ok {
						if n, err := ls.Replicas(c); err == nil {
							r["replicas"] = n
						}
					}
				}
				if err != nil {
					r["error"] = err.Error()
				}
			})
		}
		wg.Wait()
		return frames.FromRows([]protocol.Field{{Name: "name", Type: protocol.TypeString}, {Name: "running", Type: protocol.TypeBool}, {Name: "rate", Type: protocol.TypeNumber},
			{Name: "sent", Type: protocol.TypeInt}, {Name: "failed", Type: protocol.TypeInt}, {Name: "p50", Type: protocol.TypeDuration}, {Name: "p95", Type: protocol.TypeDuration},
			{Name: "p99", Type: protocol.TypeDuration}, {Name: "replicas", Type: protocol.TypeInt}, {Name: "error", Type: protocol.TypeString}}, rows), nil
	})
	loadAct := func(name, help string, f func(ctx context.Context, g core.LoadGenerator, args protocol.Args) (string, error)) {
		act(name, help, protocol.EffectWrite, func(ctx context.Context, args protocol.Args) (string, error) {
			g, n, err := Get[core.LoadGenerator](a, core.KindLoad, str(args["component"]))
			if err != nil {
				return "", err
			}
			msg, err := f(ctx, g, args)
			return n + ": " + msg, err
		})
	}
	loadAct("load.start", "start a load generator: component", func(ctx context.Context, g core.LoadGenerator, _ protocol.Args) (string, error) {
		return "started", g.Start(ctx)
	})
	loadAct("load.stop", "stop a load generator: component", func(ctx context.Context, g core.LoadGenerator, _ protocol.Args) (string, error) {
		return "stopped", g.Stop(ctx)
	})
	loadAct("load.rate", "set a load generator's requests per second: component, rps", func(ctx context.Context, g core.LoadGenerator, args protocol.Args) (string, error) {
		rps, err := strconv.ParseFloat(fmt.Sprint(args["rps"]), 64)
		if err != nil {
			return "", fmt.Errorf("rps: %w", err)
		}
		return fmt.Sprintf("%g rps", rps), g.SetRate(ctx, rps)
	})
	loadAct("load.replicas", "set how many replicas a load generator runs: component, replicas", func(ctx context.Context, g core.LoadGenerator, args protocol.Args) (string, error) {
		ls, ok := core.As[core.LoadScaler](g)
		if !ok {
			return "", fmt.Errorf("this generator does not run as replicas")
		}
		n := num(args["replicas"], -1)
		if n < 0 {
			return "", fmt.Errorf("replicas is a number")
		}
		return fmt.Sprintf("%d replicas", n), ls.SetReplicas(ctx, n)
	})

	src("hosts.list", "the machines the environment runs on: readiness, CPU, memory, disk, load", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		hs, ok := core.As[core.Hosts](a.Runtime())
		if !ok || str(args["component"]) != "" {
			var err error
			if hs, _, err = Get[core.Hosts](a, core.KindHosts, str(args["component"])); err != nil {
				return nil, err
			}
		}
		list, err := hs.Hosts(ctx)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		for _, x := range list {
			mem, disk := 0.0, 0.0
			if x.MemTotal > 0 {
				mem = float64(x.MemUsed) / float64(x.MemTotal)
			}
			if x.DiskTotal > 0 {
				disk = float64(x.DiskUsed) / float64(x.DiskTotal)
			}
			rows = append(rows, map[string]any{"host": x.Name, "addr": x.Addr, "roles": strings.Join(x.Roles, ","), "ready": x.Ready, "reason": x.Reason, "os": x.OS,
				"cpus": x.CPUs, "cpu": x.CPUUsed, "mem": mem, "mem_total": x.MemTotal, "disk": disk, "load1": x.Load1, "pods": len(x.Pods)})
		}
		return frames.FromRows([]protocol.Field{{Name: "host", Type: protocol.TypeString}, {Name: "addr", Type: protocol.TypeString}, {Name: "roles", Type: protocol.TypeString},
			{Name: "ready", Type: protocol.TypeBool}, {Name: "reason", Type: protocol.TypeString}, {Name: "os", Type: protocol.TypeString}, {Name: "cpus", Type: protocol.TypeInt},
			{Name: "cpu", Type: protocol.TypeNumber}, {Name: "mem", Type: protocol.TypeNumber}, {Name: "mem_total", Type: protocol.TypeInt}, {Name: "disk", Type: protocol.TypeNumber},
			{Name: "load1", Type: protocol.TypeNumber}, {Name: "pods", Type: protocol.TypeInt}}, rows), nil
	})

	src("component.actions", "the adapter's own operations of a component (rig do): component", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		ac, err := as[core.Actioner](a, args)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		for _, x := range ac.Actions() {
			rows = append(rows, map[string]any{"name": x.Name, "help": x.Help, "mutate": x.Mutate})
		}
		return frames.FromRows([]protocol.Field{{Name: "name", Type: protocol.TypeString}, {Name: "help", Type: protocol.TypeString}, {Name: "mutate", Type: protocol.TypeBool}}, rows), nil
	})
	_ = reg.Add(actions.Action{Name: "component.do", Effect: protocol.EffectDanger, Help: "run an adapter's own operation (rig do): component, action, args (list)",
		Run: func(ctx context.Context, args protocol.Args) (*protocol.RunResult, error) {
			ac, err := as[core.Actioner](a, args)
			if err != nil {
				return nil, err
			}
			name := str(args["action"])
			for _, x := range ac.Actions() {
				if x.Name == name {
					return &protocol.RunResult{Message: name + " done"}, x.Run(ctx, strs(args["args"]), jobs.Out(ctx))
				}
			}
			return nil, fmt.Errorf("%v has no action %q", args["component"], name)
		}})

	src("manifests", "the Kubernetes objects of the manifest folders: file, kind, name, namespace", func(ctx context.Context, _ protocol.Args) (*protocol.Frame, error) {
		set, err := manifest.Scan(a.ManifestDirs()...)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		for _, o := range set.Objects {
			rel, _ := filepath.Rel(a.Spec.Dir, o.File)
			rows = append(rows, map[string]any{"file": rel, "kind": o.Kind, "name": o.Name, "namespace": o.Namespace, "doc": o.Doc})
		}
		f := frames.FromRows([]protocol.Field{{Name: "file", Type: protocol.TypeString}, {Name: "kind", Type: protocol.TypeString}, {Name: "name", Type: protocol.TypeString},
			{Name: "namespace", Type: protocol.TypeString}, {Name: "doc", Type: protocol.TypeInt}}, rows)
		if len(set.Issues) > 0 {
			f.Meta = &protocol.FrameMeta{Notice: fmt.Sprintf("%d issues: rig manifests check", len(set.Issues))}
		}
		return f, nil
	})
	src("manifests.get", "one manifest object's YAML: file, kind, name; live: true reads what runs in the cluster", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		var text []byte
		kind, name := str(args["kind"]), str(args["name"])
		if args["live"] == true {
			mf, ok := core.As[core.Manifester](a.Runtime())
			if !ok {
				return nil, fmt.Errorf("this environment's runtime has no cluster to read")
			}
			var err error
			if text, err = mf.LiveObject(ctx, kind, name); err != nil {
				return nil, err
			}
		} else {
			file := filepath.Join(a.Spec.Dir, str(args["file"]))
			if rel, err := filepath.Rel(a.Spec.Dir, file); err != nil || strings.HasPrefix(rel, "..") {
				return nil, fmt.Errorf("%s is outside the project", args["file"])
			}
			set, err := manifest.Scan(file)
			if err != nil {
				return nil, err
			}
			for _, o := range set.Objects {
				if o.Kind == kind && o.Name == name {
					text, err = o.YAML()
					if err != nil {
						return nil, err
					}
				}
			}
			if text == nil {
				return nil, fmt.Errorf("no %s/%s in %s", kind, name, args["file"])
			}
		}
		return frames.FromRows(strFields("yaml"), []map[string]any{{"yaml": string(text)}}), nil
	})

	src("tests.suites", "the test suites of rig.yaml with their last run", func(ctx context.Context, _ protocol.Args) (*protocol.Frame, error) {
		var rows []map[string]any
		for _, n := range a.SuiteNames() {
			s := a.Spec.Tests[n]
			r := map[string]any{"suite": n, "packages": strings.Join(s.Packages, " "), "help": s.Help}
			if runs := a.TestRuns(n); len(runs) > 0 {
				r["last"] = runs[0]
				if run, err := a.LoadTestRun(runs[0], n); err == nil {
					c := run.Counts()
					r["passed"], r["failed"], r["when"] = c[TestPass], c[TestFail], run.Started
				}
			}
			rows = append(rows, r)
		}
		return frames.FromRows([]protocol.Field{{Name: "suite", Type: protocol.TypeString}, {Name: "packages", Type: protocol.TypeString}, {Name: "help", Type: protocol.TypeString},
			{Name: "last", Type: protocol.TypeString}, {Name: "passed", Type: protocol.TypeInt}, {Name: "failed", Type: protocol.TypeInt}, {Name: "when", Type: protocol.TypeTime}}, rows), nil
	})
	src("tests.run", "a saved test run's cases: suite, id (the last run when empty)", func(ctx context.Context, args protocol.Args) (*protocol.Frame, error) {
		suite, id := str(args["suite"]), str(args["id"])
		if id == "" {
			if runs := a.TestRuns(suite); len(runs) > 0 {
				id = runs[0]
			}
		}
		run, err := a.LoadTestRun(id, suite)
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		for _, c := range run.Tests {
			rows = append(rows, map[string]any{"package": c.Package, "test": c.Name, "status": c.Status, "elapsed": c.Elapsed, "output": strings.Join(c.Output, "")})
		}
		f := frames.FromRows([]protocol.Field{{Name: "package", Type: protocol.TypeString}, {Name: "test", Type: protocol.TypeString}, {Name: "status", Type: protocol.TypeString},
			{Name: "elapsed", Type: protocol.TypeDuration}, {Name: "output", Type: protocol.TypeString}}, rows)
		f.Name = run.ID
		if run.Err != "" || len(run.Stderr) > 0 {
			f.Meta = &protocol.FrameMeta{Notice: strings.TrimSpace(run.Err + "\n" + strings.Join(run.Stderr, ""))}
		}
		return f, nil
	})
	act("tests.start", "run a test suite: suite, run (a -run pattern)", protocol.EffectWrite, func(ctx context.Context, args protocol.Args) (string, error) {
		name := str(args["suite"])
		s, err := a.Suite(name)
		if err != nil {
			return "", err
		}
		o := OptionsFor(s)
		if r := str(args["run"]); r != "" {
			o.Run = r
		}
		run := a.NewTestRun(name, o)
		out := jobs.Out(ctx)
		done := make(chan struct{})
		go progress(out, run, done)
		err = a.RunTests(ctx, s, run, []TestJob{{Options: o}})
		close(done)
		c := run.Counts()
		fail := c[TestFail]
		msg := fmt.Sprintf("%s: %d passed, %d failed, %d skipped", name, c[TestPass], fail, c[TestSkip])
		fmt.Fprintln(out, msg)
		if err == nil && fail > 0 {
			err = fmt.Errorf("%s", msg)
		}
		return msg, err
	})
}

// progress writes each test as it settles until done closes.
func progress(out io.Writer, run *TestRun, done chan struct{}) {
	seen := map[*TestCase]bool{}
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		run.Read(func(r *TestRun) {
			for _, c := range r.Tests {
				if !seen[c] && c.Name != "" && c.Status != TestRunning {
					seen[c] = true
					fmt.Fprintf(out, "%-5s %s %s (%.2fs)\n", c.Status, c.Package, c.Name, c.Elapsed)
				}
			}
		})
		select {
		case <-done:
			return
		case <-tick.C:
		}
	}
}

func is[T any](v any) bool {
	_, ok := core.As[T](v)
	return ok
}

func component(a *App, args protocol.Args) (any, error) {
	name := str(args["component"])
	if name == "" {
		return nil, fmt.Errorf("component is required")
	}
	return a.Component(name)
}

func as[T any](a *App, args protocol.Args) (T, error) {
	var zero T
	c, err := component(a, args)
	if err != nil {
		return zero, err
	}
	t, ok := core.As[T](c)
	if !ok {
		return zero, fmt.Errorf("%v cannot do that", args["component"])
	}
	return t, nil
}

// browsed is the component as T with what a browse of args' path shows now, which edits index into.
func browsed[T any](ctx context.Context, a *App, args protocol.Args) (T, core.Table, error) {
	var zero T
	t, err := as[T](a, args)
	if err != nil {
		return zero, core.Table{}, err
	}
	b, err := as[core.Browser](a, args)
	if err != nil {
		return zero, core.Table{}, err
	}
	tb, _, err := b.Browse(ctx, strs(args["path"]))
	return t, tb, err
}

func kvOf(a *App, args protocol.Args) (core.KV, error) {
	kv, _, err := Get[core.KV](a, core.KindKV, str(args["component"]))
	return kv, err
}

func strFields(names ...string) []protocol.Field {
	out := make([]protocol.Field, len(names))
	for i, n := range names {
		out[i] = protocol.Field{Name: n, Type: protocol.TypeString}
	}
	return out
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func num(v any, def int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	}
	return def
}

// Configured is whether the project sets up anything the screen n of spec.Screens shows.
func (a *App) Configured(n string) bool {
	has := func(kinds ...core.Kind) bool {
		for _, k := range kinds {
			if len(a.Names(k)) > 0 {
				return true
			}
		}
		return false
	}
	rt := a.Env.Runtime.Type
	switch n {
	case "metrics":
		if len(a.Spec.Dashboards) > 0 || has(core.KindMetrics) {
			return true
		}
		for _, s := range a.Spec.Services {
			if s.Metrics != nil {
				return true
			}
		}
		return false
	case "traces":
		return has(core.KindTracing)
	case "queries":
		return len(a.Queries()) > 0 || len(a.Queriers()) > 0
	case "kv":
		return has(core.KindKV)
	case "data":
		return has(core.KindDatabase, core.KindCache, core.KindMessaging)
	case "load":
		return has(core.KindLoad)
	case "manifests":
		return rt == "kubernetes" || rt == "kind" || len(a.Spec.Manifests) > 0
	case "hosts":
		_, ok := core.As[core.Hosts](a.Runtime())
		return ok || has(core.KindHosts)
	case "tests":
		return len(a.SuiteNames()) > 0
	}
	return true
}
