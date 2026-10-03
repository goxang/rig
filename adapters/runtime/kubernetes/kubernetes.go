// Package kubernetes runs services on a Kubernetes cluster through kubectl, so it uses whatever
// auth the user's kubeconfig has. It never falls back to the current context: the environment names it.
package kubernetes

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/manifest"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindRuntime, "kubernetes", "Kubernetes through kubectl: manifests or generated Deployments, port-forward, ConfigMap state", New)
}

type Options struct {
	Context         string            `yaml:"context"`
	Namespace       string            `yaml:"namespace"`
	Registry        string            `yaml:"registry"`
	Manifests       []string          `yaml:"manifests"`
	Vars            map[string]string `yaml:"vars"`
	Env             map[string]string `yaml:"env"`
	CreateNamespace bool              `yaml:"create_namespace"`
	StateConfigMap  string            `yaml:"state_configmap"`
	NodeShellImage  string            `yaml:"node_shell_image"`
}

// Section is a service's k8s: block.
type Section struct {
	Workload  string   `yaml:"workload"`
	Container string   `yaml:"container"`
	Service   string   `yaml:"service"`
	Manifests []string `yaml:"manifests"`
	Command   []string `yaml:"command"`
	Args      []string `yaml:"args"`
}

type Runtime struct {
	Opt Options
	env core.Env

	mu       sync.Mutex
	forwards map[string]*forward
	set      *manifest.Set
	summary  map[string]nodeSummary
}

type podUse struct {
	cpu float64
	mem int64
}

type nodeSummary struct {
	at   time.Time
	pods map[string]podUse
}

// podUsage reads a pod's usage from its node's kubelet summary, cached a few seconds per node.
func (r *Runtime) podUsage(ctx context.Context, node, pod string) (podUse, bool) {
	if node == "" {
		return podUse{}, false
	}
	r.mu.Lock()
	sum, ok := r.summary[node]
	r.mu.Unlock()
	if !ok || time.Since(sum.at) > 4*time.Second {
		out, err := sh.New("kubectl", "--context", r.Opt.Context, "get", "--raw", "/api/v1/nodes/"+node+"/proxy/stats/summary").Output(ctx)
		if err != nil {
			return podUse{}, false
		}
		var raw struct {
			Pods []struct {
				PodRef struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"podRef"`
				CPU struct {
					UsageNanoCores float64 `json:"usageNanoCores"`
				} `json:"cpu"`
				Memory struct {
					WorkingSetBytes int64 `json:"workingSetBytes"`
				} `json:"memory"`
			} `json:"pods"`
		}
		if json.Unmarshal(out, &raw) != nil {
			return podUse{}, false
		}
		sum = nodeSummary{at: time.Now(), pods: map[string]podUse{}}
		for _, p := range raw.Pods {
			if p.PodRef.Namespace == r.Opt.Namespace {
				sum.pods[p.PodRef.Name] = podUse{cpu: p.CPU.UsageNanoCores / 1e9, mem: p.Memory.WorkingSetBytes}
			}
		}
		r.mu.Lock()
		if r.summary == nil {
			r.summary = map[string]nodeSummary{}
		}
		r.summary[node] = sum
		r.mu.Unlock()
	}
	u, ok := sum.pods[pod]
	return u, ok
}

type forward struct {
	addr string
	cmd  *exec.Cmd
	done chan struct{}
}

func New(env core.Env, c *spec.Component) (any, error) {
	r := &Runtime{env: env, forwards: map[string]*forward{}}
	if err := c.Decode(&r.Opt); err != nil {
		return nil, err
	}
	if err := r.Init(); err != nil {
		return nil, err
	}
	return r, nil
}

// Init checks options; adapters embedding Runtime call it after filling Opt.
func (r *Runtime) Init() error {
	if r.Opt.Context == "" {
		return errors.New("kubernetes runtime needs `context`: rig never uses kubectl's current context implicitly")
	}
	if r.Opt.Namespace == "" {
		r.Opt.Namespace = "default"
	}
	if r.Opt.StateConfigMap == "" {
		r.Opt.StateConfigMap = "rig-state"
	}
	if r.Opt.NodeShellImage == "" {
		r.Opt.NodeShellImage = "busybox:1.36"
	}
	if r.forwards == nil {
		r.forwards = map[string]*forward{}
	}
	return nil
}

func (r *Runtime) SetEnv(env core.Env) { r.env = env }

func (r *Runtime) kubectl(args ...string) *sh.Cmd {
	return sh.New("kubectl", append([]string{"--context", r.Opt.Context, "-n", r.Opt.Namespace}, args...)...)
}

func (r *Runtime) Registry() string { return r.Opt.Registry }

func (r *Runtime) section(s *spec.Service) Section {
	var sec Section
	_, _ = s.Section("k8s", &sec)
	if sec.Workload == "" {
		sec.Workload = "deployment/" + s.Name
	}
	if !strings.Contains(sec.Workload, "/") {
		sec.Workload = "deployment/" + sec.Workload
	}
	if sec.Service == "" {
		sec.Service = s.Name
	}
	return sec
}

// ---- status ----

type workloadJSON struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int `json:"replicas"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		Replicas               int `json:"replicas"`
		ReadyReplicas          int `json:"readyReplicas"`
		UpdatedReplicas        int `json:"updatedReplicas"`
		UnavailableReplicas    int `json:"unavailableReplicas"`
		DesiredNumberScheduled int `json:"desiredNumberScheduled"`
		NumberReady            int `json:"numberReady"`
		Conditions             []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

func (w workloadJSON) desired() int {
	if w.Kind == "DaemonSet" {
		return w.Status.DesiredNumberScheduled
	}
	if w.Spec.Replicas == nil {
		return 1
	}
	return *w.Spec.Replicas
}

func (w workloadJSON) ready() int {
	if w.Kind == "DaemonSet" {
		return w.Status.NumberReady
	}
	return w.Status.ReadyReplicas
}

func (w workloadJSON) status() core.Status {
	st := core.Status{Desired: w.desired(), Ready: w.ready()}
	if cs := w.Spec.Template.Spec.Containers; len(cs) > 0 {
		st.Image = cs[0].Image
	}
	switch {
	case st.Desired == 0:
		st.State = core.StateStopped
	case st.Ready >= st.Desired && w.Status.UpdatedReplicas >= st.Desired || w.Kind != "Deployment" && st.Ready >= st.Desired:
		st.State = core.StateRunning
	case st.Ready > 0:
		st.State = core.StateDegraded
	default:
		st.State = core.StateStarting
	}
	for _, c := range w.Status.Conditions {
		if c.Status == "False" && c.Message != "" {
			st.Message = c.Message
		}
	}
	return st
}

func (r *Runtime) workload(ctx context.Context, s *spec.Service) (workloadJSON, bool, error) {
	var w workloadJSON
	out, err := r.kubectl("get", r.section(s).Workload, "-o", "json", "--ignore-not-found").Output(ctx)
	if err != nil {
		return w, false, err
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return w, false, nil
	}
	return w, true, json.Unmarshal(out, &w)
}

func (r *Runtime) Status(ctx context.Context, s *spec.Service) (core.Status, error) {
	w, ok, err := r.workload(ctx, s)
	if err != nil {
		return core.Status{}, err
	}
	if !ok {
		return core.Status{Service: s.Name, State: core.StateAbsent}, nil
	}
	st := w.status()
	st.Service = s.Name
	st.Instances, err = r.pods(ctx, w.Spec.Selector.MatchLabels)
	if err != nil {
		return st, err
	}
	for _, in := range st.Instances {
		if !in.Ready && in.State == core.StateFailed && st.Message == "" {
			st.Message = in.ID + " failing"
		}
	}
	return st, nil
}

type podList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			NodeName string `json:"nodeName"`
		} `json:"spec"`
		Status struct {
			Phase             string    `json:"phase"`
			PodIP             string    `json:"podIP"`
			StartTime         time.Time `json:"startTime"`
			ContainerStatuses []struct {
				Ready        bool `json:"ready"`
				RestartCount int  `json:"restartCount"`
				State        struct {
					Waiting *struct {
						Reason string `json:"reason"`
					} `json:"waiting"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

func selector(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (r *Runtime) pods(ctx context.Context, sel map[string]string) ([]core.Instance, error) {
	if len(sel) == 0 {
		return nil, nil
	}
	out, err := r.kubectl("get", "pods", "-l", selector(sel), "-o", "json").Output(ctx)
	if err != nil {
		return nil, err
	}
	var pl podList
	if err := json.Unmarshal(out, &pl); err != nil {
		return nil, err
	}
	var res []core.Instance
	for _, p := range pl.Items {
		in := core.Instance{ID: p.Metadata.Name, Host: p.Spec.NodeName, IP: p.Status.PodIP, Started: p.Status.StartTime, Ready: true}
		in.State = map[string]core.State{"Running": core.StateRunning, "Pending": core.StateStarting, "Failed": core.StateFailed, "Succeeded": core.StateStopped}[p.Status.Phase]
		if len(p.Status.ContainerStatuses) == 0 {
			in.Ready = false
		}
		for _, c := range p.Status.ContainerStatuses {
			in.Ready = in.Ready && c.Ready
			in.Restarts += c.RestartCount
			if c.State.Waiting != nil && c.State.Waiting.Reason != "" && c.State.Waiting.Reason != "ContainerCreating" {
				in.State = core.StateFailed
			}
		}
		res = append(res, in)
	}
	r.usage(ctx, sel, res)
	return res, nil
}

// usage fills CPU and memory from metrics-server, or else from the kubelets' summaries.
func (r *Runtime) usage(ctx context.Context, sel map[string]string, ins []core.Instance) {
	out, err := r.kubectl("top", "pods", "-l", selector(sel), "--no-headers").Output(ctx)
	if err != nil {
		for i := range ins {
			if u, ok := r.podUsage(ctx, ins[i].Host, ins[i].ID); ok {
				ins[i].CPU, ins[i].Memory = u.cpu, u.mem
			}
		}
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		for i := range ins {
			if ins[i].ID == f[0] {
				ins[i].CPU = parseCPU(f[1])
				ins[i].Memory = parseMem(f[2])
			}
		}
	}
}

func (r *Runtime) Discover(ctx context.Context) ([]core.Workload, error) {
	out, err := r.kubectl("get", "deployments,statefulsets,daemonsets", "-o", "json").Output(ctx)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []workloadJSON `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, err
	}
	owner := map[string]string{}
	for n, s := range r.env.Project().Services {
		owner[strings.ToLower(r.section(s).Workload)] = n
	}
	var res []core.Workload
	for _, w := range list.Items {
		st := w.status()
		key := strings.ToLower(w.Kind) + "/" + w.Metadata.Name
		st.Service = owner[key]
		res = append(res, core.Workload{Name: w.Metadata.Name, Kind: w.Kind, Service: owner[key], Status: st})
	}
	return res, nil
}

// ---- lifecycle ----

func (r *Runtime) Start(ctx context.Context, s *spec.Service) error {
	w, ok, err := r.workload(ctx, s)
	if err != nil {
		return err
	}
	if !ok {
		return r.Deploy(ctx, s, core.Release{})
	}
	n := max(s.Replicas, 1)
	if w.desired() > 0 {
		return nil
	}
	return r.kubectl("scale", r.section(s).Workload, "--replicas="+strconv.Itoa(n)).Run(ctx)
}

func (r *Runtime) Stop(ctx context.Context, s *spec.Service) error {
	_, ok, err := r.workload(ctx, s)
	if err != nil || !ok {
		return err
	}
	return r.kubectl("scale", r.section(s).Workload, "--replicas=0").Run(ctx)
}

func (r *Runtime) Restart(ctx context.Context, s *spec.Service) error {
	return r.kubectl("rollout", "restart", r.section(s).Workload).Run(ctx)
}

func (r *Runtime) Scale(ctx context.Context, s *spec.Service, n int) error {
	return r.kubectl("scale", r.section(s).Workload, "--replicas="+strconv.Itoa(n)).Run(ctx)
}

func (r *Runtime) Logs(ctx context.Context, s *spec.Service, o core.LogOptions) (<-chan core.LogLine, error) {
	args := []string{"logs", "--prefix", "--timestamps", "--all-containers", "--max-log-requests=50"}
	if o.Instance != "" {
		args = append(args, o.Instance)
	} else {
		w, ok, err := r.workload(ctx, s)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s is not deployed", s.Name)
		}
		args = append(args, "-l", selector(w.Spec.Selector.MatchLabels))
	}
	if o.Follow {
		args = append(args, "-f")
	}
	if o.Tail > 0 {
		args = append(args, "--tail="+strconv.Itoa(o.Tail))
	}
	if o.Since > 0 {
		args = append(args, "--since="+o.Since.String())
	}
	lines, err := r.kubectl(args...).Lines(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan core.LogLine, 256)
	go func() {
		defer close(out)
		for l := range lines {
			out <- parseLogLine(s.Name, l)
		}
	}()
	return out, nil
}

// parseLogLine reads "[pod/name/container] 2006-01-02T15:04:05.999999999Z text".
func parseLogLine(svc, l string) core.LogLine {
	ll := core.LogLine{Service: svc, Text: l, Time: time.Now()}
	if strings.HasPrefix(l, "[") {
		if end := strings.Index(l, "] "); end > 0 {
			parts := strings.Split(l[1:end], "/")
			if len(parts) >= 2 {
				ll.Instance = parts[1]
			}
			l = l[end+2:]
			ll.Text = l
		}
	}
	if ts, rest, ok := strings.Cut(l, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ll.Time, ll.Text = t, rest
		}
	}
	return ll
}

func (r *Runtime) pickPod(ctx context.Context, s *spec.Service, instance string) (string, error) {
	if instance != "" {
		return instance, nil
	}
	st, err := r.Status(ctx, s)
	if err != nil {
		return "", err
	}
	for _, in := range st.Instances {
		if in.Ready {
			return in.ID, nil
		}
	}
	if len(st.Instances) > 0 {
		return st.Instances[0].ID, nil
	}
	return "", fmt.Errorf("%s has no pods", s.Name)
}

func (r *Runtime) Exec(ctx context.Context, s *spec.Service, o core.ExecOptions) error {
	pod, err := r.pickPod(ctx, s, o.Instance)
	if err != nil {
		return err
	}
	args := []string{"exec", "-i"}
	if o.TTY {
		args = append(args, "-t")
	}
	args = append(args, pod)
	if c := r.section(s).Container; c != "" {
		args = append(args, "-c", c)
	}
	args = append(append(args, "--"), o.Command...)
	return r.kubectl(args...).Attach(ctx, o.Stdin, o.Stdout, o.Stderr)
}

// ---- deploy ----

func (r *Runtime) manifests() (*manifest.Set, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.set != nil {
		return r.set, nil
	}
	var dirs []string
	for _, d := range r.Opt.Manifests {
		dirs = append(dirs, r.abs(d))
	}
	if len(dirs) == 0 {
		r.set = &manifest.Set{}
		return r.set, nil
	}
	set, err := manifest.Scan(dirs...)
	if err != nil {
		return nil, err
	}
	r.set = set
	return set, nil
}

func (r *Runtime) abs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(r.env.Project().Dir, p)
}

// Objects returns what a deploy of s applies: its own manifest files, or the workload found in the
// environment's manifest folders with everything bundled with it, or nil when it has neither.
func (r *Runtime) Objects(s *spec.Service) ([]*manifest.Object, *manifest.Object, error) {
	sec := r.section(s)
	if len(sec.Manifests) > 0 {
		var paths []string
		for _, m := range sec.Manifests {
			paths = append(paths, r.abs(m))
		}
		set, err := manifest.Scan(paths...)
		if err != nil {
			return nil, nil, err
		}
		w := set.FindWorkload(sec.Workload)
		if w == nil {
			return nil, nil, fmt.Errorf("%s: no %s in %v", s.Name, sec.Workload, sec.Manifests)
		}
		return set.Objects, w, nil
	}
	set, err := r.manifests()
	if err != nil {
		return nil, nil, err
	}
	if w := set.FindWorkload(sec.Workload); w != nil {
		return set.Bundle(w), w, nil
	}
	return nil, nil, nil
}

func (r *Runtime) vars(ctx context.Context) func(string) (string, bool) {
	state, _ := r.LoadState(ctx)
	return func(n string) (string, bool) {
		if v, ok := r.Opt.Vars[n]; ok {
			return v, true
		}
		if v, ok := state["var."+n]; ok {
			return v, true
		}
		if n == "TAG" && state["tag"] != "" {
			return state["tag"], true
		}
		switch n {
		case "NAMESPACE":
			return r.Opt.Namespace, true
		case "REGISTRY":
			return r.Opt.Registry, r.Opt.Registry != ""
		}
		return "", false
	}
}

// Render produces the YAML Deploy would apply.
func (r *Runtime) Render(ctx context.Context, s *spec.Service, rel core.Release) ([]byte, error) {
	objs, w, err := r.Objects(s)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for k, v := range r.Opt.Env {
		env[k] = v
	}
	for k, v := range s.Env {
		env[k] = v
	}
	for k, v := range rel.Env {
		env[k] = v
	}
	if objs == nil {
		img := rel.Image
		if img == "" {
			img = r.env.DefaultImage(ctx, s)
		}
		if img == "" {
			return nil, fmt.Errorf("%s: no image yet: run `rig build %s` (or set image, or k8s.manifests)", s.Name, s.Name)
		}
		return generate(s, r.section(s), img, env, max(rel.Replicas, s.Replicas, 1))
	}
	var rep *int
	if n := max(rel.Replicas, s.Replicas); n > 0 {
		rep = &n
	}
	return manifest.Render(objs, w, manifest.RenderOptions{
		Vars: r.vars(ctx), Image: rel.Image, Container: r.section(s).Container, Env: env, Replicas: rep,
		Labels: map[string]string{"app.kubernetes.io/managed-by": "rig"},
	})
}

func (r *Runtime) Deploy(ctx context.Context, s *spec.Service, rel core.Release) error {
	if r.Opt.CreateNamespace {
		_ = r.ensureNamespace(ctx)
	}
	y, err := r.Render(ctx, s, rel)
	if err != nil {
		return err
	}
	cmd := r.kubectl("apply", "-f", "-")
	cmd.Stdin = strings.NewReader(string(y))
	return cmd.Run(ctx)
}

func (r *Runtime) ensureNamespace(ctx context.Context) error {
	out, _ := sh.New("kubectl", "--context", r.Opt.Context, "get", "namespace", r.Opt.Namespace, "--ignore-not-found", "-o", "name").Output(ctx)
	if strings.TrimSpace(string(out)) != "" {
		return nil
	}
	return sh.New("kubectl", "--context", r.Opt.Context, "create", "namespace", r.Opt.Namespace).Run(ctx)
}

// ---- forwarding ----

func (r *Runtime) Forward(ctx context.Context, t core.Target) (string, error) {
	target := "svc/" + t.Service
	if s, ok := r.env.Project().Services[t.Service]; ok {
		target = "svc/" + r.section(s).Service
		// a port the Service does not publish (a debugger, a profiler) is reached on a pod of the workload
		if !r.exists(ctx, target) || !servesPort(s, t.Port) {
			target = r.section(s).Workload
		}
	}
	if t.Instance != "" {
		target = "pod/" + t.Instance
	}
	key := fmt.Sprintf("%s:%d", target, t.Port)
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.forwards[key]; ok {
		select {
		case <-f.done:
		default:
			return f.addr, nil
		}
	}
	// the forward outlives the request that opened it; Close ends it
	cmd := exec.Command("kubectl", "--context", r.Opt.Context, "-n", r.Opt.Namespace, "port-forward", "--address", "127.0.0.1", target, ":"+strconv.Itoa(t.Port))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	f := &forward{cmd: cmd, done: make(chan struct{})}
	addr := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			// Forwarding from 127.0.0.1:41235 -> 9090
			if rest, ok := strings.CutPrefix(sc.Text(), "Forwarding from "); ok {
				a, _, _ := strings.Cut(rest, " ")
				select {
				case addr <- a:
				default:
				}
			}
		}
	}()
	go func() { _ = cmd.Wait(); close(f.done) }()
	select {
	case f.addr = <-addr:
		r.forwards[key] = f
		return f.addr, nil
	case <-f.done:
		return "", fmt.Errorf("port-forward %s: %s", key, strings.TrimSpace(stderr.String()))
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("port-forward %s: timed out", key)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return "", ctx.Err()
	}
}

func servesPort(s *spec.Service, port int) bool {
	for _, p := range s.Ports {
		if p == port {
			return true
		}
	}
	return len(s.Ports) == 0
}

func (r *Runtime) exists(ctx context.Context, ref string) bool {
	out, err := r.kubectl("get", ref, "--ignore-not-found", "-o", "name").Output(ctx)
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, f := range r.forwards {
		_ = f.cmd.Process.Kill()
		delete(r.forwards, k)
	}
	return nil
}

// ---- state ----

func (r *Runtime) LoadState(ctx context.Context) (map[string]string, error) {
	out, err := r.kubectl("get", "configmap", r.Opt.StateConfigMap, "--ignore-not-found", "-o", "json").Output(ctx)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if strings.TrimSpace(string(out)) == "" {
		return m, nil
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &cm); err != nil {
		return nil, err
	}
	for k, v := range cm.Data {
		m[k] = v
	}
	return m, nil
}

func (r *Runtime) SaveState(ctx context.Context, m map[string]string) error {
	if r.Opt.CreateNamespace {
		_ = r.ensureNamespace(ctx)
	}
	cm := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": r.Opt.StateConfigMap, "labels": map[string]string{"app.kubernetes.io/managed-by": "rig"}},
		"data":     m,
	}
	raw, _ := json.Marshal(cm)
	cmd := r.kubectl("apply", "-f", "-")
	cmd.Stdin = strings.NewReader(string(raw))
	return cmd.Run(ctx)
}

// ---- generated manifests ----

func generate(s *spec.Service, sec Section, image string, env map[string]string, replicas int) ([]byte, error) {
	labels := map[string]string{"app.kubernetes.io/name": s.Name, "app.kubernetes.io/managed-by": "rig"}
	sel := map[string]string{"app.kubernetes.io/name": s.Name}
	var envList []map[string]string
	for _, k := range sortedKeys(env) {
		envList = append(envList, map[string]string{"name": k, "value": env[k]})
	}
	var ports []map[string]any
	var svcPorts []map[string]any
	for _, n := range sortedKeys(s.Ports) {
		p := s.Ports[n]
		ports = append(ports, map[string]any{"name": trimPortName(n), "containerPort": p})
		svcPorts = append(svcPorts, map[string]any{"name": trimPortName(n), "port": p, "targetPort": p})
	}
	// IfNotPresent keeps preloaded and :latest images working on clusters without registry access
	container := map[string]any{"name": s.Name, "image": image, "imagePullPolicy": "IfNotPresent"}
	if len(sec.Command) > 0 {
		container["command"] = sec.Command
	}
	if len(sec.Args) > 0 {
		container["args"] = sec.Args
	}
	if len(envList) > 0 {
		container["env"] = envList
	}
	if len(ports) > 0 {
		container["ports"] = ports
	}
	if s.Health != nil {
		port := s.PortNumber(s.Health.Port)
		if port == 0 {
			port = s.FirstPort()
		}
		container["readinessProbe"] = map[string]any{"httpGet": map[string]any{"path": s.Health.Path, "port": port}, "periodSeconds": 3}
	}
	docs := []any{map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": strings.TrimPrefix(sec.Workload, "deployment/"), "labels": labels},
		"spec": map[string]any{
			"replicas": replicas,
			"selector": map[string]any{"matchLabels": sel},
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec":     map[string]any{"containers": []any{container}},
			},
		},
	}}
	if len(svcPorts) > 0 {
		docs = append(docs, map[string]any{
			"apiVersion": "v1", "kind": "Service",
			"metadata": map[string]any{"name": sec.Service, "labels": labels},
			"spec":     map[string]any{"selector": sel, "ports": svcPorts},
		})
	}
	var b strings.Builder
	for i, d := range docs {
		if i > 0 {
			b.WriteString("---\n")
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}

// trimPortName keeps a port name within Kubernetes' 15-character, lowercase limit.
func trimPortName(n string) string {
	n = strings.ToLower(n)
	if len(n) > 15 {
		n = n[:15]
	}
	return n
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func parseCPU(s string) float64 {
	if v, ok := strings.CutSuffix(s, "m"); ok {
		f, _ := strconv.ParseFloat(v, 64)
		return f / 1000
	}
	if v, ok := strings.CutSuffix(s, "n"); ok {
		f, _ := strconv.ParseFloat(v, 64)
		return f / 1e9
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseMem(s string) int64 {
	units := map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "K": 1e3, "M": 1e6, "G": 1e9}
	for u, m := range units {
		if v, ok := strings.CutSuffix(s, u); ok {
			f, _ := strconv.ParseFloat(v, 64)
			return int64(f * float64(m))
		}
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int64(f)
}
