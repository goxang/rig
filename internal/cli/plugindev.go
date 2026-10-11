package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/host"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/protocol/rpc"
	"github.com/goxang/rig/protocol/transcript"
	"github.com/goxang/rig/scaffold"
	"github.com/goxang/rig/spec"
)

func addPluginAuthoring(pc *cobra.Command) {
	var lang string
	nc := &cobra.Command{
		Use: "new <name> [folder]", Short: "start a plugin: manifest, a source, an action, a view, a skill and a release workflow",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			if len(args) == 2 {
				dir = args[1]
			}
			files, err := scaffold.NewPlugin(dir, args[0], lang)
			if err != nil {
				return err
			}
			for _, f := range files {
				if !strings.HasPrefix(f, "rig/") {
					fmt.Printf("  %s %s\n", green("+"), filepath.Join(dir, f))
				}
			}
			if lang == "go" {
				if out, err := goCmd(dir, "mod", "tidy"); err != nil {
					lines := strings.Split(strings.TrimSpace(out), "\n")
					fmt.Println(amber("  go mod tidy failed, run it once the SDK is reachable: ") + lines[len(lines)-1])
				}
			}
			fmt.Printf("next: cd %s && rig plugin test, then rig plugin dev %s from a project\n", dir, dir)
			return nil
		},
	}
	nc.Flags().StringVar(&lang, "lang", "go", strings.Join(scaffold.Langs, ", "))
	pc.AddCommand(nc)

	var record bool
	tc := &cobra.Command{
		Use: "test [folder]", Short: "check a plugin: its manifest, build, sources and contracts, and replay testdata/*.jsonl",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return pluginTest(cmd.Context(), os.Stdout, argOr(args, "."), record)
		},
	}
	tc.Flags().BoolVar(&record, "record", false, "record testdata/<name>.jsonl from this run first")
	pc.AddCommand(tc)

	pc.AddCommand(&cobra.Command{
		Use: "dev [folder]", Short: "link a plugin folder into this project, rebuild it on change and show its RPC trace",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return pluginDev(cmd.Context(), os.Stdout, projectDir(), argOr(args, "."))
		},
	})
}

func argOr(args []string, def string) string {
	if len(args) > 0 {
		return args[0]
	}
	return def
}

func goCmd(dir string, args ...string) (string, error) {
	c := exec.Command("go", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}

func build(m *host.Manifest) error {
	if len(m.Build) == 0 {
		return nil
	}
	c := exec.Command(m.Build[0], m.Build[1:]...)
	c.Dir = m.Dir
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v\n%s", strings.Join(m.Build, " "), err, out)
	}
	return nil
}

func pluginTest(ctx context.Context, w io.Writer, dir string, record bool) error {
	dir, _ = filepath.Abs(dir)
	m, err := host.LoadManifest(dir)
	if err != nil {
		return err
	}
	bad := 0
	say := func(ok bool, what string) {
		if ok {
			fmt.Fprintf(w, "%s %s\n", green("✓"), what)
			return
		}
		fmt.Fprintf(w, "%s %s\n", red("✗"), what)
		bad++
	}
	for _, warn := range m.Warnings {
		say(false, "manifest: "+warn)
	}
	if len(m.Warnings) == 0 {
		say(true, "manifest ("+m.Kind()+")")
	}
	if err := build(m); err != nil {
		say(false, "build: "+err.Error())
		return fmt.Errorf("%d problems", bad)
	}
	if m.Kind() != "declarative" {
		// the folder under test is trusted by asking to test it
		os.Setenv("RIG_TRUST", "project")
		proj := projectDir()
		cfg := pluginConfig(proj)
		if cfg == nil {
			cfg = map[string]*spec.Plugin{}
		}
		pc := spec.Plugin{}
		if cfg[m.Name] != nil {
			pc = *cfg[m.Name]
		}
		pc.Path = dir
		cfg[m.Name] = &pc
		h := host.New(host.Options{Dir: proj}, cfg)
		defer h.Close()
		p := h.Get(m.Name)
		pr := p.Provides(ctx)
		if pr != nil && record && m.Server != nil {
			if err := recordTranscript(ctx, m, argless(pr)); err != nil {
				say(false, "record: "+err.Error())
			}
		}
		q := componentQuerier{h, componentOf(proj, m.Name)}
		unbound := false
		for _, n := range argless(pr) {
			_, err := q.Query(ctx, m.Name+"."+n, nil)
			if err != nil && q.comp == "" && strings.Contains(err.Error(), `no component ""`) {
				unbound = true
				continue
			}
			if protocol.Unmet(err) {
				fmt.Fprintln(w, dim("· source "+n+" cannot run here"+errSuffix(err)))
				continue
			}
			say(err == nil, "source "+n+errSuffix(err))
		}
		if unbound {
			fmt.Fprintln(w, dim("· sources that serve a component skipped: add a component of type "+m.Name+" to rig.yaml to test them"))
			bad += checkContracts(ctx, w, nil, p)
		} else {
			bad += checkContracts(ctx, w, q, p)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "testdata", "*.jsonl"))
	for _, f := range files {
		err := replay(ctx, m, f)
		say(err == nil, "transcript "+filepath.Base(f)+errSuffix(err))
	}
	if bad > 0 {
		return fmt.Errorf("%d problems", bad)
	}
	return nil
}

// componentQuerier passes comp as the component argument, the way views bound to a component do.
type componentQuerier struct {
	*host.Host
	comp string
}

func (q componentQuerier) Query(ctx context.Context, name string, args protocol.Args) (*protocol.Frame, error) {
	if q.comp != "" {
		args = maps.Clone(args)
		if args == nil {
			args = protocol.Args{}
		}
		args["component"] = q.comp
	}
	return q.Host.Query(ctx, name, args)
}

// componentOf is the first of the project's components served by plugin, or "".
func componentOf(dir, plugin string) string {
	f, err := spec.Find(dir)
	if err != nil {
		return ""
	}
	p, e, err := spec.Load(f, g.env)
	if err != nil {
		return ""
	}
	all := maps.Clone(p.Components)
	if e != nil {
		maps.Copy(all, e.Components)
	}
	for _, n := range slices.Sorted(maps.Keys(all)) {
		if all[n] != nil && all[n].Type == plugin {
			return n
		}
	}
	return ""
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

// argless are the sources a test can call as is: no required arguments, not streams.
func argless(pr *protocol.Provides) []string {
	var out []string
	for n, s := range pr.Sources {
		if req := fmt.Sprint(s.Args["required"]); (req == "[]" || req == "<nil>") && !s.Stream {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func serverCmd(m *host.Manifest) *exec.Cmd {
	args := slices.Clone([]string(m.Server.Command))
	if !filepath.IsAbs(args[0]) && strings.Contains(args[0], "/") {
		args[0] = filepath.Join(m.Dir, args[0])
	}
	c := exec.Command(args[0], args[1:]...)
	c.Dir = m.Dir
	c.Env = append(os.Environ(), "RIG_PLUGIN="+m.Name, "RIG_PLUGIN_DIR="+m.Dir, "RIG_PROTOCOL="+protocol.Version)
	return c
}

func recordTranscript(ctx context.Context, m *host.Manifest, sources []string) error {
	var buf bytes.Buffer
	p, err := rpc.Start(serverCmd(m), rpc.Options{Tap: transcript.NewRecorder(&buf).Tap})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err = p.Call(ctx, protocol.MethodInitialize, protocol.InitializeParams{Host: protocol.HostInfo{Name: "rig", Version: "1.0.0"}, Protocols: []string{protocol.Version}}, nil)
	for _, n := range sources {
		if err == nil {
			err = p.Call(ctx, protocol.MethodQuery, protocol.QueryParams{Source: n}, nil)
		}
	}
	if cerr := p.Close(5 * time.Second); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Join(m.Dir, "testdata"), 0o755)
	return os.WriteFile(filepath.Join(m.Dir, "testdata", m.Name+".jsonl"), buf.Bytes(), 0o644)
}

func replay(ctx context.Context, m *host.Manifest, file string) error {
	if m.Server == nil {
		return fmt.Errorf("only a server plugin replays transcripts")
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	lines, err := transcript.Parse(f)
	if err != nil {
		return err
	}
	return transcript.Replay(ctx, serverCmd(m), lines, 10*time.Second)
}

func pluginDev(ctx context.Context, w io.Writer, project, dir string) error {
	dir, _ = filepath.Abs(dir)
	m, err := host.LoadManifest(dir)
	if err != nil {
		return err
	}
	if filepath.Clean(project) == dir {
		return fmt.Errorf("run rig plugin dev %s from the project to try it in", dir)
	}
	if err := spec.SetYAML(filepath.Join(project, ".rig", "local.yaml"), []string{"plugins", m.Name, "path"}, dir); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s %s linked into %s; delete plugins.%s in .rig/local.yaml to unlink\n", green("✓"), m.Name, project, m.Name)
	if !host.Trusted(project) {
		fmt.Fprintln(w, amber("  this project is not trusted yet: rig trust"))
	}
	fmt.Fprintln(w, dim("  a running rig reloads after each build and records the RPC trace below"))
	stamp := filepath.Join(project, ".rig", host.DevStamp)
	rebuild := func() {
		m, err := host.LoadManifest(dir)
		if err == nil {
			err = build(m)
		}
		if err != nil {
			fmt.Fprintf(w, "%s %s\n", red("✗"), err)
			return
		}
		fmt.Fprintf(w, "%s %s built %s\n", green("✓"), m.Name, time.Now().Format("15:04:05"))
		_ = os.WriteFile(stamp, []byte(m.Name), 0o644)
	}
	rebuild()
	go func() {
		// the trace starts with the plugin's first call from a running rig
		for tailFile(ctx, host.TraceFile(m.Name), true) != nil && ctx.Err() == nil {
			time.Sleep(time.Second)
		}
	}()
	// ponytail: polls mtimes every 500ms like engine/watch; fsnotify if plugin folders get large
	last := sourcesStamp(dir)
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if s := sourcesStamp(dir); s != last {
				last = s
				rebuild()
			}
		}
	}
}

// sourcesStamp sums the plugin's file times and sizes, skipping what its build writes.
func sourcesStamp(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "bin", "dist", "node_modules", ".git", "__pycache__", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", p, info.ModTime().UnixNano(), info.Size())
		}
		return nil
	})
	return b.String()
}
