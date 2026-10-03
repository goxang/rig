// Package kind is the Kubernetes runtime on a local kind cluster, plus what only kind can do:
// create and delete the cluster with a local registry, load images, and shell into nodes with docker.
package kind

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/goxang/rig/adapters/runtime/kubernetes"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindRuntime, "kind", "local kind cluster: the kubernetes runtime plus cluster create/delete, local registry and image loading", New)
}

type Options struct {
	kubernetes.Options `yaml:",inline"`
	Cluster            string `yaml:"cluster"`
	NodeImage          string `yaml:"node_image"`
	Workers            int    `yaml:"workers"`
	// RegistryPort runs a registry container on localhost:<port> that the cluster pulls from.
	RegistryPort int `yaml:"registry_port"`
	// Preload copies these local docker images into the nodes on create, for clusters that cannot pull.
	Preload []string `yaml:"preload"`
}

type Runtime struct {
	*kubernetes.Runtime
	opt Options
}

func New(env core.Env, c *spec.Component) (any, error) {
	var o Options
	if err := c.Decode(&o); err != nil {
		return nil, err
	}
	if o.Cluster == "" {
		o.Cluster = "rig"
	}
	if o.Context == "" {
		o.Context = "kind-" + o.Cluster
	}
	if o.RegistryPort > 0 && o.Registry == "" {
		o.Registry = fmt.Sprintf("localhost:%d", o.RegistryPort)
	}
	k := &kubernetes.Runtime{Opt: o.Options, HostIP: hostIP}
	k.SetEnv(env)
	if err := k.Init(); err != nil {
		return nil, err
	}
	return &Runtime{Runtime: k, opt: o}, nil
}

func (r *Runtime) registryName() string { return r.opt.Cluster + "-registry" }

// LoadImage copies a local docker image into the cluster's nodes.
func (r *Runtime) LoadImage(ctx context.Context, image string) error {
	return sh.New("kind", "load", "docker-image", image, "--name", r.opt.Cluster).Run(ctx)
}

func (r *Runtime) Shell(ctx context.Context, host string, command []string) (*exec.Cmd, error) {
	if len(command) == 0 {
		command = []string{"bash", "-l"}
	}
	return exec.CommandContext(ctx, "docker", append([]string{"exec", "-it", host}, command...)...), nil
}

func (r *Runtime) Actions() []core.Action {
	own := []core.Action{
		{Name: "create", Mutate: true, Help: "create the kind cluster (and its registry) if missing", Run: r.create},
		{Name: "delete-cluster", Mutate: true, Help: "delete the kind cluster (needs --yes)", Run: func(ctx context.Context, args []string, out io.Writer) error {
			if !core.Confirmed(ctx) {
				return fmt.Errorf("this deletes cluster %s and everything in it; repeat with --yes", r.opt.Cluster)
			}
			return sh.New("kind", "delete", "cluster", "--name", r.opt.Cluster).Attach(ctx, nil, out, out)
		}},
		{Name: "load", Mutate: true, Help: "load local docker images into the nodes: load <image>...", Run: func(ctx context.Context, args []string, out io.Writer) error {
			for _, img := range args {
				if err := r.LoadImage(ctx, img); err != nil {
					return err
				}
				fmt.Fprintln(out, "loaded", img)
			}
			return nil
		}},
	}
	return append(own, r.Runtime.Actions()...)
}

func (r *Runtime) exists(ctx context.Context) bool {
	out, _ := sh.New("kind", "get", "clusters").Output(ctx)
	for _, c := range strings.Fields(string(out)) {
		if c == r.opt.Cluster {
			return true
		}
	}
	return false
}

func (r *Runtime) create(ctx context.Context, _ []string, out io.Writer) error {
	reg := r.registryName()
	if r.opt.RegistryPort > 0 {
		if err := sh.New("docker", "inspect", reg).Run(ctx); err != nil {
			fmt.Fprintf(out, "starting registry %s on localhost:%d\n", reg, r.opt.RegistryPort)
			if err := sh.New("docker", "run", "-d", "--restart=always", "-p", fmt.Sprintf("127.0.0.1:%d:5000", r.opt.RegistryPort), "--name", reg, "registry:2").Run(ctx); err != nil {
				return err
			}
		}
	}
	if !r.exists(ctx) {
		cfg := "kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n  - role: control-plane\n"
		for i := 0; i < r.opt.Workers; i++ {
			cfg += "  - role: worker\n"
		}
		if r.opt.RegistryPort > 0 {
			// registry.mirrors is gone in containerd 2; hosts.toml under config_path works on 1.7 and 2.x
			cfg += "containerdConfigPatches:\n  - |-\n    [plugins.\"io.containerd.grpc.v1.cri\".registry]\n      config_path = \"/etc/containerd/certs.d\"\n"
		}
		previous, _ := sh.New("kubectl", "config", "current-context").Output(ctx)
		args := []string{"create", "cluster", "--name", r.opt.Cluster, "--config", "-"}
		if r.opt.NodeImage != "" {
			args = append(args, "--image", r.opt.NodeImage)
		}
		cmd := sh.New("kind", args...)
		cmd.Stdin = strings.NewReader(cfg)
		// kind copies the host's proxy into the nodes, where a loopback proxy is unreachable
		cmd.Env = noProxyEnv()
		fmt.Fprintf(out, "creating kind cluster %s\n", r.opt.Cluster)
		if err := cmd.Attach(ctx, strings.NewReader(cfg), out, out); err != nil {
			return err
		}
		// kind switches the current context; give the user theirs back, rig always names its own
		if p := strings.TrimSpace(string(previous)); p != "" {
			_ = sh.New("kubectl", "config", "use-context", p).Run(ctx)
		}
	}
	if r.opt.RegistryPort > 0 {
		_ = sh.New("docker", "network", "connect", "kind", reg).Run(ctx)
		if err := r.useRegistry(ctx, reg); err != nil {
			return err
		}
	}
	for _, img := range r.opt.Preload {
		fmt.Fprintf(out, "loading %s\n", img)
		if err := r.LoadImage(ctx, img); err != nil {
			return err
		}
	}
	if r.Opt.CreateNamespace || r.Opt.Namespace != "default" {
		_ = sh.New("kubectl", "--context", r.Opt.Context, "create", "namespace", r.Opt.Namespace).Run(ctx)
	}
	fmt.Fprintf(out, "ready: context %s, namespace %s\n", r.Opt.Context, r.Opt.Namespace)
	return nil
}

// useRegistry points every node's containerd at the registry container for localhost:<port> images.
func (r *Runtime) useRegistry(ctx context.Context, reg string) error {
	nodes, err := sh.New("kind", "get", "nodes", "--name", r.opt.Cluster).Output(ctx)
	if err != nil {
		return err
	}
	dir := fmt.Sprintf("/etc/containerd/certs.d/localhost:%d", r.opt.RegistryPort)
	hosts := fmt.Sprintf("[host.\"http://%s:5000\"]\n", reg)
	for _, n := range strings.Fields(string(nodes)) {
		cmd := sh.New("docker", "exec", "-i", n, "sh", "-c", "mkdir -p "+dir+" && cat > "+dir+"/hosts.toml")
		cmd.Stdin = strings.NewReader(hosts)
		if err := cmd.Run(ctx); err != nil {
			return fmt.Errorf("node %s: %w", n, err)
		}
	}
	return nil
}

func noProxyEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToLower(k) {
		case "http_proxy", "https_proxy", "all_proxy":
			env = append(env, k+"=")
		}
	}
	return env
}

// hostIP is this machine as seen from kind's pods: the gateway of the docker network the nodes are on.
func hostIP(ctx context.Context) (string, error) {
	out, err := sh.New("docker", "network", "inspect", "kind", "-f", "{{range .IPAM.Config}}{{.Gateway}} {{end}}").Output(ctx)
	if err != nil {
		return "", err
	}
	for _, ip := range strings.Fields(string(out)) {
		if !strings.Contains(ip, ":") {
			return ip, nil
		}
	}
	return "", fmt.Errorf("docker network kind has no IPv4 gateway")
}
