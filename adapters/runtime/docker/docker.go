// Package docker runs each service as labelled containers on one network, so services reach
// each other by service name. Only the first replica publishes ports, on 127.0.0.1.
package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/localhost"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
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
	// Host is the daemon (ssh://user@server, tcp://server:2376) and Context a docker context; rig's
	// docker calls use them, health checks and forwards reach an ssh daemon's containers through ssh -L.
	Host    string `yaml:"host"`
	Context string `yaml:"context"`
	// Compose hands the services to a docker compose project: rig finds its containers by compose's
	// labels and starts, stops, scales and deploys through docker compose, so the compose file stays
	// the one definition.
	Compose *Compose `yaml:"compose"`
}

type Compose struct {
	Project string `yaml:"project"`
	// Files are the compose files (from the project directory), base first.
	Files []string `yaml:"files"`
}

type Section struct {
	Image     string            `yaml:"image"`
	Command   []string          `yaml:"command"`
	Args      []string          `yaml:"args"`
	Volumes   []string          `yaml:"volumes"`
	Publish   map[string]int    `yaml:"publish"` // service port name -> host port
	ExtraArgs []string          `yaml:"extra_args"`
	Labels    map[string]string `yaml:"labels"`
	// Container adopts an existing container by name (one made by docker compose, say): rig starts
	// and stops it but never removes or recreates it.
	Container string `yaml:"container"`
	// Bind is the host address published ports listen on (default 127.0.0.1; 0.0.0.0 lets a kind
	// cluster reach a shared service).
	Bind string `yaml:"bind"`
}

type Runtime struct {
	localhost.Host
	netMu   sync.Mutex
	opt     Options
	env     core.Env
	project string

	fwdMu sync.Mutex
	fwd   map[string]cachedAddr

	epOnce   sync.Once
	endpoint string
	tunMu    sync.Mutex
	tunnels  map[string]*tunnel
}

type cachedAddr struct {
	addr string
	at   time.Time
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
	if r.opt.Compose != nil && r.opt.Compose.Project == "" {
		return nil, fmt.Errorf("%s: compose.project is required (the name docker compose ls shows)", c.Name)
	}
	return r, nil
}

func (r *Runtime) docker(args ...string) *sh.Cmd {
	switch {
	case r.opt.Context != "":
		args = append([]string{"--context", r.opt.Context}, args...)
	case r.opt.Host != "":
		args = append([]string{"--host", r.opt.Host}, args...)
	}
	return sh.New("docker", args...)
}

func (r *Runtime) compose(args ...string) *sh.Cmd {
	pre := []string{"compose", "-p", r.opt.Compose.Project}
	for _, f := range r.opt.Compose.Files {
		if !filepath.IsAbs(f) {
			f = filepath.Join(r.env.Project().Dir, f)
		}
		pre = append(pre, "-f", f)
	}
	return r.docker(append(pre, args...)...)
}

// daemon is the daemon's address: Host, the context's endpoint, or empty for the local one.
func (r *Runtime) daemon(ctx context.Context) string {
	r.epOnce.Do(func() {
		r.endpoint = r.opt.Host
		if r.opt.Context != "" {
			out, err := sh.New("docker", "context", "inspect", "-f", "{{.Endpoints.docker.Host}}", r.opt.Context).Output(ctx)
			if err == nil {
				r.endpoint = strings.TrimSpace(string(out))
			}
		}
	})
	return r.endpoint
}

func (r *Runtime) remote(ctx context.Context) bool {
	ep := r.daemon(ctx)
	return strings.HasPrefix(ep, "ssh://") || strings.HasPrefix(ep, "tcp://")
}

func (r *Runtime) Registry() string { return r.opt.Registry }

// LoadImage copies a locally built image to a remote daemon; the local daemon already has it.
func (r *Runtime) LoadImage(ctx context.Context, image string) error {
	if !r.remote(ctx) {
		return nil
	}
	pr, pw := io.Pipe()
	save := sh.New("docker", "save", image).Exec(ctx)
	save.Stdout = pw
	if err := save.Start(); err != nil {
		return err
	}
	go func() { pw.CloseWithError(save.Wait()) }()
	load := r.docker("load", "-q")
	load.Stdin = pr
	err := load.Run(ctx)
	pr.Close()
	return err
}

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
	if r.opt.Compose != nil {
		return r.list(ctx, append([]string{"label=com.docker.compose.project=" + r.opt.Compose.Project}, filter...)...)
	}
	return r.list(ctx, append([]string{"label=rig.project=" + r.project}, filter...)...)
}

// serviceOf is the rig service a container belongs to, by rig's or compose's labels.
func (r *Runtime) serviceOf(c container) string {
	if r.opt.Compose != nil {
		if c.Labels["com.docker.compose.project"] != r.opt.Compose.Project {
			return ""
		}
		return c.Labels["com.docker.compose.service"]
	}
	if c.Labels["rig.project"] != r.project {
		return ""
	}
	return c.Labels["rig.service"]
}

func (r *Runtime) list(ctx context.Context, filter ...string) ([]container, error) {
	args := []string{"ps", "-a", "--no-trunc", "--format", "{{json .}}"}
	for _, f := range filter {
		args = append(args, "--filter", f)
	}
	out, err := r.docker(args...).Output(ctx)
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
		c := container{ID: raw.ID, Name: strings.Split(raw.Names, ",")[0], State: raw.State, Status: raw.Status, Image: raw.Image, Labels: map[string]string{}}
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
	if name := r.section(s).Container; name != "" {
		return r.list(ctx, "name=^/"+name+"$")
	}
	if r.opt.Compose != nil {
		return r.containers(ctx, "label=com.docker.compose.service="+s.Name)
	}
	return r.containers(ctx, "label=rig.service="+s.Name)
}

// StatusAll lists this machine's containers once and answers for every service from that list.
func (r *Runtime) StatusAll(ctx context.Context, svcs []*spec.Service) ([]core.Status, error) {
	cs, err := r.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]core.Status, len(svcs))
	var wg sync.WaitGroup
	for i, s := range svcs {
		adopt := r.section(s).Container
		var mine []container
		for _, c := range cs {
			if adopt != "" && c.Name == adopt || adopt == "" && r.serviceOf(c) == s.Name {
				mine = append(mine, c)
			}
		}
		out[i] = status(s.Name, mine)
		if out[i].State == core.StateRunning && s.Health != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if !r.healthy(ctx, s) {
					out[i].State, out[i].Ready = core.StateStarting, 0
				}
			}()
		}
	}
	wg.Wait()
	return out, nil
}

// adopted starts an adopted container and puts it on the project network under the service's name.
func (r *Runtime) adopted(ctx context.Context, s *spec.Service, cs []container) error {
	if err := r.network(ctx); err != nil {
		return err
	}
	for _, c := range cs {
		if c.State != "running" {
			if err := r.docker("start", c.ID).Run(ctx); err != nil {
				return err
			}
		}
		err := r.docker("network", "connect", "--alias", s.Name, r.opt.Network, c.ID).Run(ctx)
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	return nil
}

func (r *Runtime) Discover(ctx context.Context) ([]core.Workload, error) {
	cs, err := r.containers(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string][]container{}
	for _, c := range cs {
		by[r.serviceOf(c)] = append(by[r.serviceOf(c)], c)
	}
	var out []core.Workload
	for svc, list := range by {
		st := status(svc, list)
		out = append(out, core.Workload{Name: list[0].Name, Kind: "container", Service: svc, Status: st})
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
	if r.opt.Compose != nil {
		args := []string{"up", "-d", "--no-deps"}
		if rel.Replicas > 0 {
			args = append(args, "--scale", fmt.Sprintf("%s=%d", s.Name, rel.Replicas))
		}
		c := r.compose(append(args, s.Name)...)
		if rel.Image != "" {
			c.Env = []string{"RIG_IMAGE=" + rel.Image} // image: ${RIG_IMAGE:-...} in the compose file takes it
		}
		return c.Run(ctx)
	}
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
	if r.section(s).Container != "" && len(cs) > 0 {
		return r.adopted(ctx, s, cs)
	}
	n := s.CountOr(1)
	if rel.Replicas > 0 {
		n = rel.Replicas
	}
	if r.current(cs, img, n) && len(rel.Env) == 0 {
		return nil
	}
	for _, c := range cs {
		if err := r.docker("rm", "-f", c.ID).Run(ctx); err != nil {
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
	if r.docker("network", "inspect", r.opt.Network).Run(ctx) == nil {
		return nil
	}
	err := r.docker("network", "create", "--label", "rig.project="+r.project, r.opt.Network).Run(ctx)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

func (r *Runtime) run(ctx context.Context, s *spec.Service, img string, i int, extra map[string]string) error {
	sec := r.section(s)
	cname := r.name(s.Name, i)
	if sec.Container != "" {
		cname = sec.Container
	}
	args := []string{"run", "-d", "--name", cname, "--network", r.opt.Network, "--network-alias", s.Name,
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
			bind := sec.Bind
			if bind == "" {
				bind = "127.0.0.1"
			}
			args = append(args, "-p", fmt.Sprintf("%s:%s:%d", bind, host, p))
		}
	}
	for _, v := range sec.Volumes {
		// ./x:/y binds a file of the project
		if strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") {
			v = filepath.Join(r.env.Project().Dir, v)
		}
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
	return r.docker(args...).Run(ctx)
}

func (r *Runtime) Start(ctx context.Context, s *spec.Service) error {
	if r.opt.Compose != nil {
		return r.compose("up", "-d", "--no-deps", s.Name).Run(ctx)
	}
	cs, err := r.of(ctx, s)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		return r.Deploy(ctx, s, core.Release{})
	}
	if r.section(s).Container != "" {
		return r.adopted(ctx, s, cs)
	}
	for _, c := range cs {
		if c.State != "running" {
			if err := r.docker("start", c.ID).Run(ctx); err != nil {
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
		if err := r.docker(verb, c.ID).Run(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) Stop(ctx context.Context, s *spec.Service) error {
	if r.opt.Compose != nil {
		return r.compose("stop", s.Name).Run(ctx)
	}
	return r.each(ctx, s, "stop")
}

func (r *Runtime) Restart(ctx context.Context, s *spec.Service) error {
	if r.opt.Compose != nil {
		return r.compose("restart", s.Name).Run(ctx)
	}
	return r.each(ctx, s, "restart")
}

func (r *Runtime) Scale(ctx context.Context, s *spec.Service, n int) error {
	if r.opt.Compose != nil {
		if n == 0 {
			return r.Stop(ctx, s)
		}
		return r.Deploy(ctx, s, core.Release{Replicas: n})
	}
	cs, err := r.of(ctx, s)
	if err != nil {
		return err
	}
	if n == 0 {
		return r.Stop(ctx, s)
	}
	if r.section(s).Container != "" {
		if n > 1 {
			return fmt.Errorf("%s is an adopted container: %w", s.Name, core.ErrUnsupported)
		}
		return r.Start(ctx, s)
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
		if err := r.docker("rm", "-f", cs[i].ID).Run(ctx); err != nil {
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
		lines, err := r.docker(append(args, c.ID)...).Lines(ctx)
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
		name = r.first(ctx, s.Name)
	}
	args := []string{"exec", "-i"}
	if o.TTY {
		args = append(args, "-t")
	}
	return r.docker(append(append(args, name), o.Command...)...).Attach(ctx, o.Stdin, o.Stdout, o.Stderr)
}

// Forward returns the published port, or else the container's address on its network; answers are
// cached briefly since health checks ask on every status.
func (r *Runtime) Forward(ctx context.Context, t core.Target) (string, error) {
	key := fmt.Sprintf("%s/%s:%d", t.Service, t.Instance, t.Port)
	r.fwdMu.Lock()
	if c, ok := r.fwd[key]; ok && time.Since(c.at) < 20*time.Second {
		r.fwdMu.Unlock()
		return c.addr, nil
	}
	r.fwdMu.Unlock()
	addr, err := r.forward(ctx, t)
	if err == nil {
		r.fwdMu.Lock()
		if r.fwd == nil {
			r.fwd = map[string]cachedAddr{}
		}
		r.fwd[key] = cachedAddr{addr: addr, at: time.Now()}
		r.fwdMu.Unlock()
	}
	return addr, err
}

// first is the service's first container: adopted, compose's, rig's, or one named like the service.
func (r *Runtime) first(ctx context.Context, svc string) string {
	if s, ok := r.env.Project().Services[svc]; ok {
		if c := r.section(s).Container; c != "" {
			return c
		}
		if r.opt.Compose != nil {
			if cs, err := r.of(ctx, s); err == nil && len(cs) > 0 {
				return cs[0].Name
			}
		}
	}
	if name := r.name(svc, 1); r.docker("inspect", name).Run(ctx) == nil {
		return name
	}
	return svc
}

func (r *Runtime) forward(ctx context.Context, t core.Target) (string, error) {
	name := t.Instance
	if name == "" {
		name = r.first(ctx, t.Service)
	}
	addr, err := r.daemonAddr(ctx, name, t.Port)
	if err != nil {
		return "", err
	}
	ep := r.daemon(ctx)
	switch {
	case strings.HasPrefix(ep, "ssh://"):
		return r.tunnel(ctx, ep, addr)
	case strings.HasPrefix(ep, "tcp://"):
		// reachable when the port is published on 0.0.0.0 (docker.bind)
		if u, err := url.Parse(ep); err == nil && strings.HasPrefix(addr, "127.0.0.1:") {
			return net.JoinHostPort(u.Hostname(), strings.TrimPrefix(addr, "127.0.0.1:")), nil
		}
	}
	return addr, nil
}

// daemonAddr is where the daemon's machine reaches the container's port: published, the container's
// IP, or its own loopback for host networking.
func (r *Runtime) daemonAddr(ctx context.Context, name string, port int) (string, error) {
	out, err := r.docker("port", name, strconv.Itoa(port)).Output(ctx)
	if err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(l, "127.0.0.1:") || strings.HasPrefix(l, "0.0.0.0:") {
				return "127.0.0.1:" + l[strings.LastIndex(l, ":")+1:], nil
			}
		}
	}
	ip, err := r.docker("inspect", "-f", "{{.HostConfig.NetworkMode}} {{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", name).Output(ctx)
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(ip))
	switch {
	case len(f) > 1:
		return fmt.Sprintf("%s:%d", f[1], port), nil
	case len(f) == 1 && f[0] == "host":
		return fmt.Sprintf("127.0.0.1:%d", port), nil
	}
	return "", fmt.Errorf("%s has no address", name)
}

func (r *Runtime) Actions() []core.Action {
	return []core.Action{{Name: "rm", Mutate: true, Help: "remove every container and the network of this project (needs --yes)", Run: func(ctx context.Context, args []string, out io.Writer) error {
		if !core.Confirmed(ctx) {
			return fmt.Errorf("this removes every %s container; repeat with --yes", r.project)
		}
		if r.opt.Compose != nil {
			return r.compose("down").Attach(ctx, nil, out, out)
		}
		cs, err := r.containers(ctx)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if err := r.docker("rm", "-f", c.ID).Run(ctx); err != nil {
				return err
			}
			fmt.Fprintln(out, "removed", c.Name)
		}
		_ = r.docker("network", "rm", r.opt.Network).Run(ctx)
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
