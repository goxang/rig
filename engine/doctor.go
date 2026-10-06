package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/core"
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

func runtimeFix(typ string) string {
	switch typ {
	case "docker":
		return "start the Docker daemon (Docker Desktop, or: sudo systemctl start docker)"
	case "kind":
		return "start Docker, then create the cluster: rig do runtime create"
	case "kubernetes":
		return "check the cluster is up and your kubeconfig can reach it: kubectl --context <context> get ns"
	}
	return ""
}

// contextCheck is whether the kubeconfig has the context the environment names.
func (a *App) contextCheck(ctx context.Context) Check {
	var opt struct {
		Context string `yaml:"context"`
		Cluster string `yaml:"cluster"`
	}
	_ = a.Env.Runtime.Decode(&opt)
	if opt.Context == "" && opt.Cluster != "" {
		opt.Context = "kind-" + opt.Cluster
	}
	if opt.Context == "" {
		return Check{Name: "kube context", Status: CheckWarn, Detail: "the environment names none"}
	}
	raw, err := sh.New("kubectl", "config", "get-contexts", "-o", "name").Output(ctx)
	if err != nil {
		return Check{Name: "kube context", Status: CheckFail, Detail: firstLine(err), Fix: "check ~/.kube/config (or $KUBECONFIG)"}
	}
	have := strings.Fields(string(raw))
	if !slices.Contains(have, opt.Context) {
		return Check{Name: "kube context", Status: CheckFail, Detail: fmt.Sprintf("%q is not in your kubeconfig (have %s)", opt.Context, strings.Join(have, ", ")),
			Fix: "add the cluster's kubeconfig, or rename yours: kubectl config rename-context <yours> " + opt.Context}
	}
	return Check{Name: "kube context", Status: CheckOK, Detail: opt.Context}
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
	if p, ok := v.(core.Pinger); ok {
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
