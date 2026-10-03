// Package docker runs each service as labelled containers on one network, so services reach
// each other by service name. Only the first replica publishes ports, on 127.0.0.1.
package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MohammadmahdiAhmadi/rig/core"
	"github.com/MohammadmahdiAhmadi/rig/internal/localhost"
	"github.com/MohammadmahdiAhmadi/rig/internal/sh"
	"github.com/MohammadmahdiAhmadi/rig/plugin"
	"github.com/MohammadmahdiAhmadi/rig/spec"
)

func init() {
	plugin.Register(core.KindRuntime, "docker", "docker containers on one network, named <project>-<service>", New)
}

type Options struct {
	Network  string            `yaml:"network"`
	Registry string            `yaml:"registry"`
	Env      map[string]string `yaml:"env"`
	// Publish exposes the first replica's ports on 127.0.0.1 (random host ports unless docker.publish sets them);
	// off, rig reaches services by container IP.
	Publish *bool `yaml:"publish"`
}

type Section struct {
	Image     string            `yaml:"image"`
	Command   []string          `yaml:"command"`
	Args      []string          `yaml:"args"`
	Volumes   []string          `yaml:"volumes"`
	Publish   map[string]int    `yaml:"publish"` // service port name -> host port
	ExtraArgs []string          `yaml:"extra_args"`
	Labels    map[string]string `yaml:"labels"`
}

type Runtime struct {
	localhost.Host
	netMu   sync.Mutex
	opt     Options
	env     core.Env
	project string
}

func New(env core.Env, c *spec.Component) (any, error) {
	r := &Runtime{env: env, project: env.Project().Name}
	if err := c.Decode(&r.opt); err != nil {
		return nil, err
	}
	if r.project == "" {
		r.project = "rig"
	}
	if r.opt.Network == "" {
		r.opt.Network = r.project
	}
	return r, nil
}

func (r *Runtime) Registry() string { return r.opt.Registry }

// LoadImage has nothing to do: the daemon that built the image runs it.
func (r *Runtime) LoadImage(context.Context, string) error { return nil }

func (r *Runtime) name(s string, i int) string {
	if i <= 1 {
		return r.project + "-" + s
	}
	return fmt.Sprintf("%s-%s-%d", r.project, s, i)
}

func (r *Runtime) section(s *spec.Service) Section {
	var sec Section
	_, _ = s.Section("docker", &sec)
	return sec
}

type container struct {
	ID      string
	Name    string
	State   string
	Status  string
	Image   string
	Labels  map[string]string
	Created time.Time
}

func (r *Runtime) containers(ctx context.Context, filter ...string) ([]container, error) {
	args := []string{"ps", "-a", "--no-trunc", "--format", "{{json .}}", "--filter", "label=rig.project=" + r.project}
	for _, f := range filter {
		args = append(args, "--filter", f)
	}
	out, err := sh.New("docker", args...).Output(ctx)
	if err != nil {
		return nil, err
	}
	var res []container
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		var raw struct {
			ID, Names, State, Status, Image, Labels, CreatedAt string
		}
		if json.Unmarshal(sc.Bytes(), &raw) != nil {
			continue
		}
		c := container{ID: raw.ID, Name: raw.Names, State: raw.State, Status: raw.Status, Image: raw.Image, Labels: map[string]string{}}
		for _, kv := range strings.Split(raw.Labels, ",") {
			k, v, _ := strings.Cut(kv, "=")
			c.Labels[k] = v
		}
		c.Created, _ = time.Parse("2006-01-02 15:04:05 -0700 MST", raw.CreatedAt)
		res = append(res, c)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res, nil
}

func (r *Runtime) of(ctx context.Context, s *spec.Service) ([]container, error) {
	return r.containers(ctx, "label=rig.service="+s.Name)
}

func (r *Runtime) Discover(ctx context.Context) ([]core.Workload, error) {
	cs, err := r.containers(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string][]container{}
	for _, c := range cs {
		by[c.Labels["rig.service"]] = append(by[c.Labels["rig.service"]], c)
	}
	var out []core.Workload
	for svc, list := range by {
		st := status(svc, list)
		out = append(out, core.Workload{Name: r.name(svc, 1), Kind: "container", Service: svc, Status: st})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func status(svc string, cs []container) core.Status {
	st := core.Status{Service: svc, Desired: len(cs)}
	if len(cs) == 0 {
		st.State = core.StateAbsent
		return st
	}
	running := 0
	for _, c := range cs {
		in := core.Instance{ID: c.Name, Host: "docker", Started: c.Created}
		switch c.State {
		case "running":
			in.State = core.StateRunning
			in.Ready = !strings.Contains(c.Status, "(unhealthy)") && !strings.Contains(c.Status, "(health: starting)")
			running++
		case "restarting":
			in.State = core.StateFailed
		case "created":
			in.State = core.StateStarting
		default:
			in.State = core.StateStopped
		}
		if in.Ready {
			st.Ready++
		}
		st.Instances = append(st.Instances, in)
		st.Image = c.Image
	}
	switch {
	case running == 0:
		st.State, st.Desired = core.StateStopped, 0
	case st.Ready == len(cs):
		st.State = core.StateRunning
	case st.Ready > 0:
		st.State = core.StateDegraded
	default:
		st.State = core.StateStarting
	}
	return st
}

func (r *Runtime) Status(ctx context.Context, s *spec.Service) (core.Status, error) {
	cs, err := r.of(ctx, s)
	if err != nil {
		return core.Status{}, err
	}
	st := status(s.Name, cs)
	if st.State == core.StateRunning && s.Health != nil && !r.healthy(ctx, s) {
		st.State, st.Ready = core.StateStarting, 0
	}
	return st, nil
}

func (r *Runtime) healthy(ctx context.Context, s *spec.Service) bool {
	port := s.PortNumber(s.Health.Port)
	if port == 0 {
		port = s.FirstPort()
	}
	addr, err := r.Forward(ctx, core.Target{Service: s.Name, Port: port})
	if err != nil {
		return false
	}
	return localhost.Probe(ctx, "http://"+addr+s.Health.Path)
}

func (r *Runtime) image(ctx context.Context, s *spec.Service, rel core.Release) string {
	for _, img := range []string{rel.Image, r.section(s).Image} {
		if img != "" {
			return img
		}
	}
	return r.env.DefaultImage(ctx, s)
}

func (r *Runtime) Deploy(ctx context.Context, s *spec.Service, rel core.Release) error {
	img := r.image(ctx, s, rel)
	if img == "" {
		return fmt.Errorf("%s: no image (set image or docker.image, or build it)", s.Name)
	}
	if err := r.network(ctx); err != nil {
		return err
	}
	cs, err := r.of(ctx, s)
	if err != nil {
		return err
	}
	n := max(rel.Replicas, s.Replicas, 1)
	if r.current(cs, img, n) && len(rel.Env) == 0 {
		return nil
	}
	for _, c := range cs {
		if err := sh.New("docker", "rm", "-f", c.ID).Run(ctx); err != nil {
			return err
		}
	}
	for i := 1; i <= n; i++ {
		if err := r.run(ctx, s, img, i, rel.Env); err != nil {
			return err
		}
	}
	return nil
}

// current reports whether the service already runs n containers of img.
func (r *Runtime) current(cs []container, img string, n int) bool {
	if len(cs) != n {
		return false
	}
	for _, c := range cs {
		if c.State != "running" || c.Image != img {
			return false
		}
	}
	return true
}

func (r *Runtime) network(ctx context.Context) error {
	r.netMu.Lock()
	defer r.netMu.Unlock()
	if sh.New("docker", "network", "inspect", r.opt.Network).Run(ctx) == nil {
		return nil
	}
	err := sh.New("docker", "network", "create", "--label", "rig.project="+r.project, r.opt.Network).Run(ctx)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

func (r *Runtime) run(ctx context.Context, s *spec.Service, img string, i int, extra map[string]string) error {
	sec := r.section(s)
	args := []string{"run", "-d", "--name", r.name(s.Name, i), "--network", r.opt.Network, "--network-alias", s.Name,
		"--label", "rig.project=" + r.project, "--label", "rig.service=" + s.Name, "--restart", "unless-stopped"}
	if e := r.env.Environment(); e != nil {
		args = append(args, "--label", "rig.env="+e.Name)
	}
	for k, v := range sec.Labels {
		args = append(args, "--label", k+"="+v)
	}
	env := map[string]string{}
	for _, m := range []map[string]string{r.opt.Env, s.Env, extra} {
		for k, v := range m {
			env[k] = v
		}
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "-e", k+"="+env[k])
	}
	if i == 1 && (r.opt.Publish == nil || *r.opt.Publish) {
		for name, p := range s.Ports {
			// docker picks a free host port unless one is asked for; Forward reads it back
			host := ""
			if h, ok := sec.Publish[name]; ok {
				host = strconv.Itoa(h)
			}
			args = append(args, "-p", fmt.Sprintf("127.0.0.1:%s:%d", host, p))
		}
	}
	for _, v := range sec.Volumes {
		args = append(args, "-v", v)
	}
	args = append(args, sec.ExtraArgs...)
	if len(sec.Command) > 0 {
		args = append(args, "--entrypoint", sec.Command[0])
	}
	args = append(args, img)
	if len(sec.Command) > 1 {
		args = append(args, sec.Command[1:]...)
	}
	args = append(args, sec.Args...)
	return sh.New("docker", args...).Run(ctx)
}

func (r *Runtime) Start(ctx context.Context, s *spec.Service) error {
	cs, err := r.of(ctx, s)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		return r.Deploy(ctx, s, core.Release{})
	}
	for _, c := range cs {
		if c.State != "running" {
			if err := sh.New("docker", "start", c.ID).Run(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runtime) each(ctx context.Context, s *spec.Service, verb string) error {
	cs, err := r.of(ctx, s)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if err := sh.New("docker", verb, c.ID).Run(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) Stop(ctx context.Context, s *spec.Service) error { return r.each(ctx, s, "stop") }
func (r *Runtime) Restart(ctx context.Context, s *spec.Service) error {
	return r.each(ctx, s, "restart")
}

func (r *Runtime) Scale(ctx context.Context, s *spec.Service, n int) error {
	cs, err := r.of(ctx, s)
	if err != nil {
		return err
	}
	if n == 0 {
		return r.Stop(ctx, s)
	}
	if len(cs) == 0 {
		return r.Deploy(ctx, s, core.Release{Replicas: n})
	}
	img := cs[0].Image
	for i := len(cs) + 1; i <= n; i++ {
		if err := r.run(ctx, s, img, i, nil); err != nil {
			return err
		}
	}
	for i := len(cs) - 1; i >= n; i-- {
		if err := sh.New("docker", "rm", "-f", cs[i].ID).Run(ctx); err != nil {
			return err
		}
	}
	return r.Start(ctx, s)
}

func (r *Runtime) Logs(ctx context.Context, s *spec.Service, o core.LogOptions) (<-chan core.LogLine, error) {
	cs, err := r.of(ctx, s)
	if err != nil {
		return nil, err
	}
	out := make(chan core.LogLine, 256)
	done := make(chan struct{}, len(cs))
	for _, c := range cs {
		if o.Instance != "" && c.Name != o.Instance {
			done <- struct{}{}
			continue
		}
		args := []string{"logs", "--timestamps"}
		if o.Follow {
			args = append(args, "-f")
		}
		if o.Tail > 0 {
			args = append(args, "--tail", strconv.Itoa(o.Tail))
		}
		if o.Since > 0 {
			args = append(args, "--since", o.Since.String())
		}
		lines, err := sh.New("docker", append(args, c.ID)...).Lines(ctx)
		if err != nil {
			return nil, err
		}
		go func() {
			defer func() { done <- struct{}{} }()
			for l := range lines {
				ll := core.LogLine{Service: s.Name, Instance: c.Name, Text: l, Time: time.Now()}
				if ts, rest, ok := strings.Cut(l, " "); ok {
					if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
						ll.Time, ll.Text = t, rest
					}
				}
				out <- ll
			}
		}()
	}
	go func() {
		for range cs {
			<-done
		}
		close(out)
	}()
	return out, nil
}

func (r *Runtime) Exec(ctx context.Context, s *spec.Service, o core.ExecOptions) error {
	name := o.Instance
	if name == "" {
		name = r.name(s.Name, 1)
	}
	args := []string{"exec", "-i"}
	if o.TTY {
		args = append(args, "-t")
	}
	return sh.New("docker", append(append(args, name), o.Command...)...).Attach(ctx, o.Stdin, o.Stdout, o.Stderr)
}

// Forward returns the published port, or else the container's address on its network.
func (r *Runtime) Forward(ctx context.Context, t core.Target) (string, error) {
	name := t.Instance
	if name == "" {
		name = r.name(t.Service, 1)
		if sh.New("docker", "inspect", name).Run(ctx) != nil {
			name = t.Service
		}
	}
	out, err := sh.New("docker", "port", name, strconv.Itoa(t.Port)).Output(ctx)
	if err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(l, "127.0.0.1:") || strings.HasPrefix(l, "0.0.0.0:") {
				return "127.0.0.1:" + l[strings.LastIndex(l, ":")+1:], nil
			}
		}
	}
	ip, err := sh.New("docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", name).Output(ctx)
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(ip))
	if len(f) == 0 {
		return "", fmt.Errorf("%s has no address", name)
	}
	return fmt.Sprintf("%s:%d", f[0], t.Port), nil
}

func (r *Runtime) Actions() []core.Action {
	return []core.Action{{Name: "rm", Mutate: true, Help: "remove every container and the network of this project (needs --yes)", Run: func(ctx context.Context, args []string, out io.Writer) error {
		if !core.Confirmed(ctx) {
			return fmt.Errorf("this removes every %s container; repeat with --yes", r.project)
		}
		cs, err := r.containers(ctx)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if err := sh.New("docker", "rm", "-f", c.ID).Run(ctx); err != nil {
				return err
			}
			fmt.Fprintln(out, "removed", c.Name)
		}
		_ = sh.New("docker", "network", "rm", r.opt.Network).Run(ctx)
		return nil
	}}}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
