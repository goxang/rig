package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/goxang/rig/contracts"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/spec"
)

// projectDir is where the project file is, or here when there is none.
func projectDir() string {
	if g.file != "" {
		abs, _ := filepath.Abs(g.file)
		return filepath.Dir(abs)
	}
	if f, err := spec.Find("."); err == nil {
		return filepath.Dir(f)
	}
	wd, _ := os.Getwd()
	return wd
}

func installer(dir string) *host.Installer {
	in := &host.Installer{Dir: dir, Registry: host.UserRegistry(), Yes: g.yes || os.Getenv("RIG_TRUST") == "project", Out: os.Stderr}
	if !in.Yes && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd())) {
		in.Ask = askYes
	}
	return in
}

// ensurePlugins installs what rig.lock pins and this machine lacks; true means the project must reload.
func ensurePlugins(dir string) (bool, error) {
	mine, err := installer(host.ConfigDir()).Ensure(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, amber("your plugins: ")+err.Error())
	}
	changed, err := installer(dir).Ensure(context.Background())
	changed = changed || mine
	if err != nil && !errors.Is(err, host.ErrUntrusted) {
		fmt.Fprintln(os.Stderr, amber("plugins: ")+err.Error())
		return changed, nil
	}
	return changed, err
}

func pluginCommand() *cobra.Command {
	pc := &cobra.Command{Use: "plugin", Aliases: []string{"plugins"}, Short: "list, find, add, upgrade and switch plugins"}
	var major, project, follow, user bool
	// scope is where add, rm and up pin: the project's rig.lock, or with --user the user's own
	// (~/.config/rig/rig.lock), whose plugins every project shows and no repository carries
	scope := func() string {
		if user {
			return host.ConfigDir()
		}
		return projectDir()
	}
	userFlag := func(c *cobra.Command) *cobra.Command {
		c.Flags().BoolVarP(&user, "user", "u", false, "for you in every project (~/.config/rig/rig.lock), not in this project's rig.lock")
		return c
	}

	pc.AddCommand(&cobra.Command{
		Use: "ls", Short: "the project's plugins: version, kind, where from, on or off",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := projectDir()
			cfg := pluginConfig(dir)
			all := map[string]*spec.Plugin{}
			for n, c := range cfg {
				cp := *c
				cp.Off = false
				all[n] = &cp
			}
			ms, warn := host.Discover(dir, all)
			lock, _ := host.ReadLock(dir)
			mine, _ := host.ReadLock(host.ConfigDir())
			var rows [][]string
			for _, m := range ms {
				state, from := green("on"), dim(m.Dir)
				if c := cfg[m.Name]; c != nil && c.Off {
					state = dim("off")
				}
				if l := mine.Plugins[m.Name]; l != nil {
					from = l.Source + dim(" (yours: --user)")
				}
				if l := lock.Plugins[m.Name]; l != nil {
					from = l.Source
					if l.Pack != "" {
						from += dim(" (" + l.Pack + ")")
					}
				}
				rows = append(rows, []string{m.Name, m.Version, m.Kind(), state, from})
			}
			printTable(os.Stdout, []string{"PLUGIN", "VERSION", "KIND", "STATE", "FROM"}, rows)
			for _, w := range warn {
				fmt.Fprintln(os.Stderr, amber("! ")+w)
			}
			return nil
		},
	})
	pc.AddCommand(&cobra.Command{
		Use: "search [text]", Short: "plugins in the registry index",
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := host.UserRegistry().Index(cmd.Context(), true)
			if err != nil {
				return err
			}
			q := strings.ToLower(strings.Join(args, " "))
			var rows [][]string
			for n, e := range ix.Plugins {
				if q == "" || strings.Contains(strings.ToLower(n+" "+e.Description), q) {
					rows = append(rows, []string{strings.Replace(n, "pack-", "pack/", 1), e.Latest(), e.Publisher, e.Description})
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
			printTable(os.Stdout, []string{"PLUGIN", "LATEST", "PUBLISHER", "DESCRIPTION"}, rows)
			return nil
		},
	})
	addPluginAuthoring(pc)
	pc.AddCommand(&cobra.Command{
		Use: "check <name>", Short: "check a plugin against the contracts it implements, live", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return pluginCheck(cmd.Context(), os.Stdout, projectDir(), args[0])
		},
	})
	pc.AddCommand(&cobra.Command{
		Use: "info <name>", Short: "what a plugin provides and the permissions it has", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := projectDir()
			ms, _ := host.Discover(dir, pluginConfig(dir))
			for _, m := range ms {
				if m.Name == args[0] {
					return pluginInfo(cmd.Context(), os.Stdout, dir, m)
				}
			}
			ix, err := host.UserRegistry().Index(cmd.Context(), false)
			if err != nil {
				return err
			}
			e := ix.Plugins[args[0]]
			if e == nil {
				return fmt.Errorf("no plugin %s here or in the index", args[0])
			}
			v := e.Latest()
			fmt.Printf("%s %s %s\n%s\n", bold(args[0]), v, dim("(not installed: rig plugin add "+args[0]+")"), e.Description)
			if vv := e.Versions[v]; vv != nil {
				fmt.Printf("permissions: %s\n", vv.Permissions.String())
			}
			return nil
		},
	})
	logs := func(use, short string, file func(string) string) *cobra.Command {
		c := &cobra.Command{
			Use: use + " <name>", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return tailFile(cmd.Context(), file(args[0]), follow)
			},
		}
		c.Flags().BoolVarP(&follow, "follow", "F", false, "keep printing as it grows")
		return c
	}
	pc.AddCommand(logs("logs", "the plugin's stderr and host/log lines", host.LogFile))
	pc.AddCommand(logs("trace", "the plugin's JSON-RPC exchange, recorded while RIG_TRACE names it (or is *)", host.TraceFile))
	pc.AddCommand(userFlag(&cobra.Command{
		Use: "add <source>...", Short: "install plugins and pin them in rig.lock (--user: only for you): a name, pack/<name>, host/owner/repo[/sub]@ref, a .tar.gz URL or a path",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if user {
				for i, a := range args {
					if strings.HasPrefix(a, "./") || strings.HasPrefix(a, "../") {
						args[i], _ = filepath.Abs(a)
					}
				}
			}
			return installer(scope()).Add(cmd.Context(), args)
		},
	}))
	pc.AddCommand(userFlag(&cobra.Command{
		Use: "rm <name>...", Aliases: []string{"remove"}, Short: "unpin plugins (and what a removed pack brought)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return installer(scope()).Remove(args)
		},
	}))
	up := userFlag(&cobra.Command{
		Use: "up [name...]", Short: "upgrade plugins within their major version (all when none is named)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return installer(scope()).Up(cmd.Context(), args, major)
		},
	})
	up.Flags().BoolVar(&major, "major", false, "allow a new major version")
	pc.AddCommand(up)
	for _, on := range []bool{true, false} {
		verb := map[bool]string{true: "on", false: "off"}[on]
		c := &cobra.Command{
			Use: verb + " <name>...", Short: "turn plugins " + verb + " for you (.rig/local.yaml), or for everyone with --project",
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				dir := projectDir()
				file := filepath.Join(dir, ".rig", "local.yaml")
				if project {
					f, err := spec.Find(dir)
					if err != nil {
						return err
					}
					file = f
				}
				for _, n := range args {
					if err := spec.SetYAML(file, []string{"plugins", n, "off"}, !on); err != nil {
						return err
					}
					fmt.Printf("  %s %s %s\n", green("✓"), n, verb)
				}
				return nil
			},
		}
		c.Flags().BoolVar(&project, "project", false, "write rig.yaml instead of .rig/local.yaml")
		pc.AddCommand(c)
	}
	pc.AddCommand(&cobra.Command{
		Use: "gc", Short: "delete installed plugin versions no project (and not your own) lock pins",
		RunE: func(cmd *cobra.Command, _ []string) error {
			removed, err := host.GC()
			for _, r := range removed {
				fmt.Println("  - " + r)
			}
			return err
		},
	})
	pc.AddCommand(&cobra.Command{
		Use: "vendor", Short: "copy the locked plugins into .rig/vendor for offline installs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, err := installer(projectDir()).Vendor()
			for _, f := range files {
				fmt.Println("  " + f)
			}
			return err
		},
	})
	return pc
}

func trustCommand() *cobra.Command {
	return &cobra.Command{
		Use: "trust", Short: "let this project's plugins run and install (workspace trust)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := projectDir()
			if err := host.Trust(dir); err != nil {
				return err
			}
			fmt.Printf("  %s trusted %s\n", green("✓"), dir)
			in := installer(dir)
			_, err := in.Ensure(cmd.Context())
			return err
		},
	}
}

// pluginConfig is plugins: from the project as loaded (rig.yaml under .rig/local.yaml), without opening it.
func pluginConfig(dir string) map[string]*spec.Plugin {
	f, err := spec.Find(dir)
	if err != nil {
		return nil
	}
	p, _, err := spec.Load(f, g.env)
	if err != nil {
		return nil
	}
	return p.Plugins
}

func pluginCheck(ctx context.Context, w io.Writer, dir, name string) error {
	h := host.New(host.Options{Dir: dir}, pluginConfig(dir))
	defer h.Close()
	p := h.Get(name)
	if p == nil {
		return fmt.Errorf("no plugin %s here", name)
	}
	if len(p.Implements) == 0 {
		return fmt.Errorf("%s implements no contract", name)
	}
	if bad := checkContracts(ctx, w, h, p); bad > 0 {
		return fmt.Errorf("%d problems", bad)
	}
	return nil
}

// checkContracts checks p against each contract it implements, live, and returns the problems found.
// checkContracts checks p against its contracts, and its frames live unless q is nil.
func checkContracts(ctx context.Context, w io.Writer, q contracts.Querier, p *host.Plugin) int {
	name, pr, bad := p.Name, p.Provides(ctx), 0
	for _, id := range p.Implements {
		c := contracts.Get(id)
		if c == nil {
			fmt.Fprintf(w, "%s %s\n", red("✗"), id+": no such contract")
			bad++
			continue
		}
		ps := c.Check(pr)
		if len(ps) == 0 && q != nil {
			ps = c.CheckFrames(ctx, q, name, pr)
		}
		if len(ps) == 0 {
			fmt.Fprintf(w, "%s %s\n", green("✓"), id)
		}
		for _, pb := range ps {
			fmt.Fprintf(w, "%s %s %s\n", red("✗"), id, pb)
		}
		bad += len(ps)
	}
	return bad
}

func pluginInfo(ctx context.Context, w io.Writer, dir string, m *host.Manifest) error {
	fmt.Fprintf(w, "%s %s  %s\n", bold(m.Name), m.Version, dim(m.Kind()+" · "+m.Dir))
	if m.Description != "" {
		fmt.Fprintln(w, m.Description)
	}
	if m.Publisher != "" {
		fmt.Fprintln(w, "publisher:   "+m.Publisher)
	}
	fmt.Fprintln(w, "permissions: "+m.Permissions.String())
	if !host.Sandboxed() {
		fmt.Fprintln(w, dim("             advisory on this OS: no sandbox, calls are audited"))
	}
	h := host.New(host.Options{Dir: dir}, map[string]*spec.Plugin{})
	defer h.Close()
	if p := h.Get(m.Name); p != nil {
		pr := p.Provides(ctx)
		var rows [][]string
		for n, s := range pr.Sources {
			rows = append(rows, []string{"source", m.Name + "." + n, "read", s.Description})
		}
		for n, a := range pr.Actions {
			rows = append(rows, []string{"action", m.Name + "." + n, a.Effect, a.Description})
		}
		for n, im := range pr.Importers {
			rows = append(rows, []string{"importer", m.Name + "." + n, "", im.Description})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i][0]+rows[i][1] < rows[j][0]+rows[j][1] })
		if len(rows) > 0 {
			fmt.Fprintln(w)
			printTable(w, []string{"KIND", "NAME", "EFFECT", "HELP"}, rows)
		}
	}
	return nil
}

func tailFile(ctx context.Context, file string, follow bool) error {
	f, err := os.Open(file)
	if os.IsNotExist(err) {
		return fmt.Errorf("nothing at %s yet", file)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 64<<10 {
		_, _ = f.Seek(-64<<10, io.SeekEnd)
	}
	for {
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return err
		}
		if !follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// setYAML sets one nested key in a YAML file, keeping its comments and order.
