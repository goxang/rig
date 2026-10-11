package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/internal/kubectx"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/spec"
)

type CheckStatus string

const (
	CheckOK   CheckStatus = "ok"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
)

// Check is one line of `rig doctor`.
type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
	Fix    string      `json:"fix,omitempty"`
}

// runtimeTools are the programs each runtime type drives.
var runtimeTools = map[string][]string{
	"docker":     {"docker"},
	"kubernetes": {"kubectl"},
	"kind":       {"kind", "kubectl", "docker"},
}

var toolFix = map[string]string{
	"docker":  "install Docker: https://docs.docker.com/get-docker/",
	"kubectl": "install kubectl: https://kubernetes.io/docs/tasks/tools/",
	"kind":    "install kind: go install sigs.k8s.io/kind@latest",
	"helm":    "install helm: https://helm.sh/docs/intro/install/",
	"go":      "install Go: https://go.dev/dl/",
}

// Doctor checks what this environment needs: its runtime's tools and reachability, unset
// ${VARS}, free ports for services not running, and every component through its own adapter.
func (a *App) Doctor(ctx context.Context) []Check {
	var out []Check
	add := func(c Check) { out = append(out, c) }
	add(Check{Name: "rig.yaml", Status: CheckOK, Detail: a.Spec.File})
	if len(a.Spec.Unset) > 0 {
		add(Check{Name: "variables", Status: CheckFail, Detail: "unset: " + strings.Join(a.Spec.Unset, ", "),
			Fix: "export them, rig secret set NAME, or give them a default (${NAME:-value}, secrets:, vars:)"})
	} else {
		add(Check{Name: "variables", Status: CheckOK, Detail: "every ${NAME} is set"})
	}
	out = append(out, a.PluginChecks()...)
	out = append(out, a.diskCheck())
	if a.Env == nil {
		return append(out, Check{Name: "environment", Status: CheckFail, Detail: "none defined", Fix: "add one under environments: and set default:"})
	}

	typ := a.Env.Runtime.Type
	toolsOK := true
	for _, t := range runtimeTools[typ] {
		if p, err := exec.LookPath(t); err != nil {
			toolsOK = false
			add(Check{Name: "tool " + t, Status: CheckFail, Detail: "not on PATH (the " + typ + " runtime needs it)", Fix: toolFix[t]})
		} else {
			add(Check{Name: "tool " + t, Status: CheckOK, Detail: p})
		}
	}
	if toolsOK && (typ == "kubernetes" || typ == "kind") {
		add(a.contextCheck(ctx))
	}

	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	_, err := a.runtime.Discover(c)
	cancel()
	fix := "is it running? rig -e " + a.Env.Name + " up; else check its address in rig.yaml"
	if err != nil {
		add(Check{Name: "runtime " + typ, Status: CheckFail, Detail: firstLine(err), Fix: runtimeFix(typ)})
		fix = "fix the runtime first: most components are reached through it"
	} else {
		add(Check{Name: "runtime " + typ, Status: CheckOK, Detail: "reachable"})
		out = append(out, a.portChecks(ctx)...)
	}
	return append(out, a.componentChecks(ctx, fix)...)
}

// InstallStd installs every official plugin, the runtimes and importers most rig.yaml files use.
const InstallStd = "go install github.com/goxang/rig-plugins/cmd/rig-std@latest && rig-std install"

// PluginChecks are the plugins this project uses that need a credential nobody has given them yet.
func (a *App) PluginChecks() []Check {
	var out []Check
	for _, kind := range a.Spec.NoImporter {
		out = append(out, Check{Name: "import " + kind, Status: CheckWarn, Detail: "skipped: no installed plugin reads " + kind,
			Fix: "rig plugin search " + kind + ", then rig plugin add the one that imports it; or all the official ones: " + InstallStd})
	}
	ms, _ := host.Discover(a.Spec.Dir, a.Spec.Plugins)
	known := a.KnownSecrets()
	for _, m := range ms {
		if !m.Relevant(a.Spec.Dir, a.Spec.Plugins) {
			continue
		}
		if miss := m.Missing(false); len(miss) > 0 {
			var fixes []string
			var bins []string
			for _, l := range miss {
				bins = append(bins, l.Bin)
				if l.Fix != "" {
					fixes = append(fixes, l.Bin+": "+l.Fix)
				}
			}
			out = append(out, Check{Name: "plugin " + m.Name, Status: CheckWarn, Detail: "not installed on this machine: " + strings.Join(bins, ", "),
				Fix: strings.Join(fixes, "; ")})
		}
		secrets := m.Permissions.Secrets
		if len(secrets) == 0 || !m.Relevant(a.Spec.Dir, a.Spec.Plugins) {
			continue
		}
		if p := a.Spec.Plugins[m.Name]; p != nil && (p.Off || p.Config["token"] != nil) {
			continue
		}
		set := slices.ContainsFunc(secrets, func(n string) bool {
			_, env := os.LookupEnv(n)
			return known[n] != "" || env
		})
		if set {
			out = append(out, Check{Name: "plugin " + m.Name, Status: CheckOK, Detail: "has its credential"})
			continue
		}
		out = append(out, Check{Name: "plugin " + m.Name, Status: CheckWarn, Detail: "no " + strings.Join(secrets, " or ") + ": its screens stay empty",
			Fix: "rig secret set " + secrets[0] + " (kept in your config directory, never in the repository); or rig plugin off " + m.Name})
	}
	return out
}

func runtimeFix(typ string) string {
	switch typ {
	case "docker":
		return "start the Docker daemon (Docker Desktop, or: sudo systemctl start docker)"
	case "kind":
		return "start Docker, then create the cluster: rig do runtime create"
	case "kubernetes":
		return "check the cluster is up and your kubeconfig can reach it (kubectl --context <context> get ns); an expired one: rig kubeconfig"
	}
	return ""
}

// contextCheck is whether the kubeconfig has the context the environment names.
func (a *App) contextCheck(ctx context.Context) Check {
	var opt struct {
		Context string `yaml:"context"`
		Server  string `yaml:"server"`
		Cluster string `yaml:"cluster"`
	}
	_ = a.Env.Runtime.Decode(&opt)
	if opt.Context == "" && opt.Cluster != "" {
		opt.Context = "kind-" + opt.Cluster
	}
	if opt.Context == "" && opt.Server == "" {
		return Check{Name: "kube context", Status: CheckWarn, Detail: "the environment names none"}
	}
	if err := sh.New("kubectl", "config", "get-contexts", "-o", "name").Run(ctx); err != nil {
		return Check{Name: "kube context", Status: CheckFail, Detail: firstLine(err), Fix: "check ~/.kube/config (or $KUBECONFIG)"}
	}
	c, err := kubectx.Resolve(opt.Context, opt.Server)
	if err != nil {
		return Check{Name: "kube context", Status: CheckFail, Detail: firstLine(err),
			Fix: "rig kubeconfig fetches the cluster's kubeconfig, or rename yours: kubectl config rename-context <yours> " + opt.Context}
	}
	return Check{Name: "kube context", Status: CheckOK, Detail: c}
}

// portChecks finds the host ports of services that are not running already taken by something else.
func (a *App) portChecks(ctx context.Context) []Check {
	typ := a.Env.Runtime.Type
	if typ != "local" && typ != "docker" {
		return nil
	}
	var names []string
	for _, n := range a.Spec.ServiceNames() {
		if !a.SharedElsewhere(n) {
			names = append(names, n)
		}
	}
	running := map[string]bool{}
	for _, st := range a.StatusAll(ctx, names) {
		running[st.Service] = st.State != core.StateStopped && st.State != core.StateAbsent && st.State != core.StateUnknown
	}
	var busy []string
	for _, n := range names {
		if running[n] {
			continue
		}
		for _, p := range hostPorts(typ, a.Spec.Services[n]) {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				busy = append(busy, fmt.Sprintf("%s:%d", n, p))
				continue
			}
			l.Close()
		}
	}
	if len(busy) > 0 {
		return []Check{{Name: "ports", Status: CheckWarn, Detail: "taken by something else: " + strings.Join(busy, ", "),
			Fix: "stop what holds them (ss -ltnp | grep :<port>), or change the port in rig.yaml"}}
	}
	return []Check{{Name: "ports", Status: CheckOK, Detail: "free for every service not running"}}
}

// hostPorts are the ports a service takes on this machine: its own for a local process, the
// published ones (docker.publish) for a container.
func hostPorts(typ string, s *spec.Service) []int {
	if typ == "local" {
		var out []int
		for _, p := range s.Ports {
			out = append(out, p)
		}
		slices.Sort(out)
		return out
	}
	var sec struct {
		Publish map[string]int `yaml:"publish"`
	}
	_, _ = s.Section("docker", &sec)
	var out []int
	for _, p := range sec.Publish {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// componentChecks reaches every configured component through its adapter's own reads, in parallel.
func (a *App) componentChecks(ctx context.Context, fix string) []Check {
	var names []string
	for _, n := range a.componentNames() {
		if !strings.HasPrefix(n, "default:") {
			names = append(names, n)
		}
	}
	out := make([]Check, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			kind, typ, _ := a.Kind(n)
			name := fmt.Sprintf("%s (%s %s)", n, kind, typ)
			err := a.Reach(c, n)
			switch {
			case errors.Is(err, core.ErrUnsupported):
				out[i] = Check{Name: name, Status: CheckOK, Detail: "configured (nothing to reach)"}
			case err != nil:
				out[i] = Check{Name: name, Status: CheckFail, Detail: firstLine(err), Fix: fix}
			default:
				out[i] = Check{Name: name, Status: CheckOK, Detail: "reachable"}
			}
		}()
	}
	wg.Wait()
	return out
}

// Reach builds a component and does the cheapest read its kind has: Ping when the adapter has one.
// core.ErrUnsupported when the kind has nothing to reach (builders, profilers).
func (a *App) Reach(ctx context.Context, name string) error {
	v, err := a.Component(name)
	if err != nil {
		return err
	}
	if p, ok := core.As[core.Pinger](v); ok {
		return p.Ping(ctx)
	}
	switch c := v.(type) {
	case core.Database:
		_, err = c.Databases(ctx)
	case core.Cache:
		_, err = c.Info(ctx)
	case core.Messaging:
		_, err = c.Queues(ctx)
	case core.KV:
		_, _, err = c.Get(ctx, "rig-doctor")
	case core.Tracing:
		_, err = c.Services(ctx)
	case core.Metrics:
		_, err = c.Instant(ctx, "up")
	case core.Hosts:
		_, err = c.Hosts(ctx)
	case core.LoadGenerator:
		_, err = c.Status(ctx)
	default:
		err = core.ErrUnsupported
	}
	return err
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	return s
}

// diskCheck warns before the disk fills: builds and logs of local services need room.
func (a *App) diskCheck() Check {
	free, err := sh.FreeBytes(a.Spec.Dir)
	if err != nil {
		return Check{Name: "disk", Status: CheckOK, Detail: "not measured"}
	}
	detail := fmt.Sprintf("%d MB free where the project lives", free>>20)
	fix := "rig purge --days 0 empties logs and drops built binaries; rig purge --dry-run lists what goes"
	switch {
	case free < 512<<20:
		return Check{Name: "disk", Status: CheckFail, Detail: detail, Fix: fix}
	case free < 2<<30:
		return Check{Name: "disk", Status: CheckWarn, Detail: detail, Fix: fix}
	}
	return Check{Name: "disk", Status: CheckOK, Detail: detail}
}
