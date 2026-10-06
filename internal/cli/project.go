package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/scaffold"
	"github.com/goxang/rig/internal/viz"
	"github.com/goxang/rig/manifest"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func projectCommands() []*cobra.Command {
	env := &cobra.Command{
		Use: "env", Short: "environments of the project and what each one runs on",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := open()
			if err != nil {
				return err
			}
			defer a.Close()
			cur := ""
			if a.Env != nil {
				cur = a.Env.Name
			}
			var rows [][]string
			for _, n := range a.Spec.EnvironmentNames() {
				e := a.Spec.Environments[n]
				mark := "  "
				if n == cur {
					mark = green("▸ ")
				}
				p := ""
				if e.Protected {
					p = red("protected")
				}
				rt := ""
				if e.Runtime != nil {
					rt = e.Runtime.Type
				}
				rows = append(rows, []string{mark + n, rt, p, e.Description})
			}
			printTable(os.Stdout, []string{"ENVIRONMENT", "RUNTIME", "", "DESCRIPTION"}, rows)
			if a.Env != nil {
				fmt.Println()
				var crows [][]string
				for _, n := range engine.SortedKeys(a.Spec.Components) {
					k, t, err := a.Kind(n)
					if err != nil {
						crows = append(crows, []string{n, "?", red(err.Error())})
						continue
					}
					crows = append(crows, []string{n, string(k), t})
				}
				fmt.Println(bold("components in " + cur))
				printTable(os.Stdout, []string{"NAME", "KIND", "ADAPTER"}, crows)
			}
			return nil
		},
	}

	plugins := &cobra.Command{
		Use: "plugins", Short: "adapters built into this binary, by kind",
		Run: func(*cobra.Command, []string) {
			var rows [][]string
			last := core.Kind("")
			for _, a := range plugin.List() {
				k := string(a.Kind)
				if a.Kind == last {
					k = ""
				}
				last = a.Kind
				rows = append(rows, []string{bold(k), a.Type, dim(a.Description)})
			}
			printTable(os.Stdout, []string{"KIND", "TYPE", "WHAT"}, rows)
			fmt.Println(dim("\nimports: " + strings.Join(spec.Importers(), ", ")))
		},
	}

	var graph string
	var lint, tree, folders bool
	manifests := &cobra.Command{
		Use:   "manifests [dir...]",
		Short: "scan Kubernetes manifests (any layout): objects, how they relate, what is broken",
		Long: `Reads every .yml/.yaml under the given folders (default: the project's manifests:, else the
environment's runtime manifests, else .), renders kustomizations, links the objects (Service→workload,
Ingress→Service, HPA→workload, workload→ConfigMap/Secret/PVC/ServiceAccount, ...) and lints them.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if folders {
				root := "."
				if a, err := open(); err == nil {
					root = a.Spec.Dir
					a.Close()
				}
				found := manifest.Folders(root)
				var rows [][]string
				for _, d := range engine.SortedKeys(found) {
					rows = append(rows, []string{d, fmt.Sprint(found[d])})
				}
				printTable(os.Stdout, []string{"FOLDER", "FILES"}, rows)
				return nil
			}
			dirs := args
			if len(dirs) == 0 {
				dirs = projectManifestDirs()
			}
			set, err := manifest.Scan(dirs...)
			if err != nil {
				return err
			}
			switch {
			case graph != "":
				return printRelations(set, graph)
			case lint:
				printIssues(set, true)
				return nil
			case tree:
				printTree(set)
				return nil
			}
			kinds := set.Kinds()
			var parts []string
			for _, k := range engine.SortedKeys(kinds) {
				parts = append(parts, fmt.Sprintf("%s %d", k, kinds[k]))
			}
			fmt.Printf("%s in %d files under %s\n%s\n\n", bold(fmt.Sprintf("%d objects", len(set.Objects))), len(set.Files()), strings.Join(dirs, ", "), dim(strings.Join(parts, " · ")))
			var rows [][]string
			for _, w := range set.Workloads() {
				var rel []string
				for _, e := range set.In(w.ID()) {
					rel = append(rel, "← "+e.From)
				}
				for _, e := range set.Out(w.ID()) {
					s := "→ " + e.To
					if e.Missing {
						s = red(s + "?")
					}
					rel = append(rel, s)
				}
				img := ""
				if t, ok := manifest.Template(w); ok && len(t.Spec.Containers) > 0 {
					img = t.Spec.Containers[0].Image
				}
				rows = append(rows, []string{w.ID(), shortImage(img), strings.Join(rel, "  ")})
			}
			printTable(os.Stdout, []string{"WORKLOAD", "IMAGE", "RELATIONS"}, rows)
			fmt.Println()
			printIssues(set, false)
			return nil
		},
	}
	manifests.Flags().StringVar(&graph, "graph", "", "relations of one object: Kind/name or name")
	manifests.Flags().BoolVar(&lint, "lint", false, "every issue")
	manifests.Flags().BoolVar(&tree, "tree", false, "files and the objects in them")
	manifests.Flags().BoolVar(&folders, "folders", false, "every folder of the project holding manifests")

	hosts := &cobra.Command{
		Use: "hosts [ssh <host> [command...]]", Short: "servers and nodes: usage, and a shell on any of them",
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			h, _, err := engine.Get[core.Hosts](a, core.KindHosts, "")
			if err != nil {
				return err
			}
			if len(args) >= 2 && args[0] == "ssh" {
				cmd, err := h.Shell(ctx, args[1], args[2:])
				if err != nil {
					return err
				}
				cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
				return cmd.Run()
			}
			list, err := h.Hosts(ctx)
			if err != nil {
				return err
			}
			var rows [][]string
			for _, x := range list {
				state := green("● ready")
				if !x.Ready {
					state = red("✖ down")
					if x.Reason != "" {
						state += ": " + x.Reason
					}
				}
				mem := 0.0
				if x.MemTotal > 0 {
					mem = float64(x.MemUsed) / float64(x.MemTotal)
				}
				rows = append(rows, []string{x.Name, x.Addr, strings.Join(x.Roles, ","), state,
					viz.Gauge(x.CPUUsed, 12) + fmt.Sprintf(" %3.0f%% of %d", x.CPUUsed*100, x.CPUs),
					viz.Gauge(mem, 12) + fmt.Sprintf(" %s/%s", bytesText(x.MemUsed), bytesText(x.MemTotal)), x.OS})
			}
			printTable(os.Stdout, []string{"HOST", "ADDRESS", "ROLES", "STATE", "CPU", "MEMORY", "OS"}, rows)
			return nil
		}),
	}

	var (
		force, dry bool
		with       []string
	)
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "write rig.yaml from what this directory has: compose files, Kubernetes manifests, Go/Python/Node/Java/Rust services",
		Example: `  rig init                          # look around and write rig.yaml
  rig init --dry-run                # print it instead
  rig init --with postgres,redis    # plus infrastructure (` + strings.Join(scaffold.Presets(), ", ") + `)`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return initProject(cmd.Context(), force, dry, with)
		},
	}
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite an existing rig.yaml")
	initCmd.Flags().BoolVar(&dry, "dry-run", false, "print rig.yaml instead of writing it")
	initCmd.Flags().StringSliceVar(&with, "with", nil, "infrastructure to add: "+strings.Join(scaffold.Presets(), ", "))

	task := &cobra.Command{
		Use:   "task [name [args...]]",
		Short: "run a task from rig.yaml (its shell steps, in order), or list them",
		Example: `  rig task ship core shaparak              # positional args: $1... and $RIG_ARGS
  rig task ship RIG_REF=feature-x TAG=v3   # NAME=value args: set that env var instead of pre-exporting it`,
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if len(args) == 0 {
				var rows [][]string
				for _, n := range a.TaskNames() {
					rows = append(rows, []string{n, a.TaskHelp(n)})
				}
				printTable(os.Stdout, []string{"TASK", "WHAT IT DOES"}, rows)
				return nil
			}
			return a.RunTask(ctx, args[0], args[1:], os.Stdout)
		}),
	}

	source := &cobra.Command{
		Use: "source <git-ref>", Short: "export a git ref of the project (cached per commit) and print its directory", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := open()
			if err != nil {
				return err
			}
			defer a.Close()
			dir, err := a.Source(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Println(dir)
			return nil
		},
	}

	vars := &cobra.Command{
		Use:   "vars [set K=V... | unset K...]",
		Short: "manifest variables ($MAIN_DB, ...): rig.yaml's, overridden per environment and kept in its state",
		Example: `  rig -e staging vars                                  # what manifests get, and where from
  rig -e staging vars set MAIN_DB=staging_app AUDIT_DB=staging_audit   # then rig up / deploy renders with them
  rig -e staging vars unset MAIN_DB                    # back to rig.yaml's value`,
		RunE: withApp(func(ctx context.Context, a *engine.App, args []string) error {
			if len(args) > 0 {
				if err := a.Guard(); err != nil {
					return err
				}
				kv := map[string]string{}
				for _, arg := range args[1:] {
					k, v, ok := strings.Cut(arg, "=")
					switch {
					case args[0] == "set" && ok && k != "" && v != "":
						kv["var."+k] = v
					case args[0] == "unset" && !ok:
						kv["var."+k] = ""
					default:
						return fmt.Errorf("usage: rig vars set K=V [K=V ...] | rig vars unset K [K ...]")
					}
				}
				if len(kv) == 0 {
					return fmt.Errorf("usage: rig vars set K=V [K=V ...] | rig vars unset K [K ...]")
				}
				if err := a.SetState(ctx, kv); err != nil {
					return err
				}
			}
			vars, err := a.Vars(ctx)
			if err != nil {
				return err
			}
			var rows [][]string
			for _, k := range engine.SortedKeys(vars) {
				rows = append(rows, []string{k, vars[k].Value, vars[k].From})
			}
			state, _ := a.LoadState(ctx)
			if t := state["tag"]; t != "" {
				rows = append(rows, []string{"TAG", t, "last deploy"})
			}
			printTable(os.Stdout, []string{"VAR", "VALUE", "FROM"}, rows)
			return nil
		}),
	}

	secret := &cobra.Command{
		Use:   "secret [ls | set NAME [VALUE] | rm NAME]",
		Short: "secrets rig.yaml uses as ${NAME}: kept in your config dir (0600), never in the repo; the environment wins",
		Example: `  rig secret                          # declared secrets and where each value comes from (never the value)
  rig secret set RIG_RABBITMQ_PASSWORD  # asks without echo (or reads stdin)
  rig secret rm RIG_RABBITMQ_PASSWORD`,
		RunE: func(cmd *cobra.Command, args []string) error {
			file := g.file
			if file == "" {
				f, err := spec.Find(".")
				if err != nil {
					return err
				}
				file = f
			}
			p, _, err := spec.Load(file, g.env)
			if err != nil {
				return err
			}
			if len(args) >= 2 && (args[0] == "set" || args[0] == "rm") {
				value := ""
				if args[0] == "set" {
					if len(args) > 2 {
						value = args[2]
					} else if value, err = readSecret(args[1]); err != nil {
						return err
					}
					if value == "" {
						return fmt.Errorf("empty value; rig secret rm %s removes it", args[1])
					}
				}
				if err := spec.SetSecret(p.Name, args[1], value); err != nil {
					return err
				}
				fmt.Println(green("✓"), args[1])
				return nil
			}
			if len(args) > 0 && args[0] != "ls" {
				return fmt.Errorf("usage: rig secret [ls | set NAME [VALUE] | rm NAME]")
			}
			stored, err := spec.LoadSecrets(p.Name)
			if err != nil {
				return err
			}
			names := map[string]bool{}
			for n := range p.Secrets {
				names[n] = true
			}
			for n := range stored {
				names[n] = true
			}
			var rows [][]string
			for _, n := range engine.SortedKeys(names) {
				from := red("unset")
				switch _, env := os.LookupEnv(n); {
				case env:
					from = "environment"
				case stored[n] != "":
					from = green("stored")
				case p.Secrets[n].Default != "":
					from = dim("default")
				}
				rows = append(rows, []string{n, from, p.Secrets[n].Help})
			}
			printTable(os.Stdout, []string{"SECRET", "FROM", "WHAT"}, rows)
			f, _ := spec.SecretsFile(p.Name)
			fmt.Println(dim("store: " + f))
			return nil
		},
	}

	version := &cobra.Command{Use: "version", Short: "print the version", Run: func(*cobra.Command, []string) { fmt.Println("rig", Version) }}

	return []*cobra.Command{initCmd, env, vars, secret, nsCommand(), infraCommand(), task, source, manifests, hosts, mcpCommand(), plugins, version}
}

func projectManifestDirs() []string {
	a, err := open()
	if err != nil {
		return []string{"."}
	}
	defer a.Close()
	return a.ManifestDirs()
}

func printIssues(set *manifest.Set, all bool) {
	counts := map[string]int{}
	for _, i := range set.Issues {
		counts[i.Level]++
	}
	fmt.Printf("%s  %s  %s\n", red(fmt.Sprintf("%d errors", counts["error"])), amber(fmt.Sprintf("%d warnings", counts["warn"])), dim(fmt.Sprintf("%d notes", counts["info"])))
	shown := 0
	for _, i := range set.Issues {
		if !all && (i.Level == "info" || shown >= 20) {
			continue
		}
		shown++
		lvl := map[string]string{"error": red("error"), "warn": amber("warn "), "info": dim("note ")}[i.Level]
		fmt.Printf("  %s %-40s %s %s\n", lvl, i.Object, i.Text, dim(i.File))
	}
	if !all && len(set.Issues) > shown {
		fmt.Println(dim("  --lint shows all"))
	}
}

func printRelations(set *manifest.Set, id string) error {
	var o *manifest.Object
	if strings.Contains(id, "/") {
		k, n, _ := strings.Cut(id, "/")
		for _, x := range set.Objects {
			if strings.EqualFold(x.Kind, k) && x.Name == n {
				o = x
			}
		}
	} else {
		for _, x := range set.Objects {
			if x.Name == id && (o == nil || manifest.IsWorkload(x.Kind)) {
				o = x
			}
		}
	}
	if o == nil {
		return fmt.Errorf("no object %q: give its name, or kind/name when the name is ambiguous", id)
	}
	in, outE := set.In(o.ID()), set.Out(o.ID())
	fmt.Println(viz.RelationGraph(o.ID(), toRel(in, true), toRel(outE, false)))
	fmt.Println(dim(o.File))
	return nil
}

func toRel(es []manifest.Edge, incoming bool) []viz.Rel {
	var out []viz.Rel
	for _, e := range es {
		n := e.To
		if incoming {
			n = e.From
		}
		out = append(out, viz.Rel{Node: n, Label: e.Rel, Missing: e.Missing})
	}
	return out
}

func printTree(set *manifest.Set) {
	byFile := map[string][]*manifest.Object{}
	for _, o := range set.Objects {
		byFile[o.File] = append(byFile[o.File], o)
	}
	files := engine.SortedKeys(byFile)
	sort.Strings(files)
	for _, f := range files {
		fmt.Println(bold(f))
		for i, o := range byFile[f] {
			branch := "├─"
			if i == len(byFile[f])-1 {
				branch = "└─"
			}
			fmt.Printf("  %s %s\n", dim(branch), o.ID())
		}
	}
}

func readSecret(name string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		raw, err := io.ReadAll(os.Stdin)
		return strings.TrimRight(string(raw), "\r\n"), err
	}
	fmt.Fprintf(os.Stderr, "%s: ", name)
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(raw), err
}
