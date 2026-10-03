package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/internal/sh"
)

// ---- hosts: the cluster's nodes ----

func (r *Runtime) Hosts(ctx context.Context) ([]core.Host, error) {
	out, err := sh.New("kubectl", "--context", r.Opt.Context, "get", "nodes", "-o", "json").Output(ctx)
	if err != nil {
		return nil, err
	}
	var nl struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Addresses []struct {
					Type    string `json:"type"`
					Address string `json:"address"`
				} `json:"addresses"`
				Capacity   map[string]string `json:"capacity"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				NodeInfo struct {
					OSImage       string `json:"osImage"`
					KernelVersion string `json:"kernelVersion"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &nl); err != nil {
		return nil, err
	}
	hosts := make([]core.Host, len(nl.Items))
	var wg sync.WaitGroup
	for i, n := range nl.Items {
		h := core.Host{Name: n.Metadata.Name, OS: n.Status.NodeInfo.OSImage, Kernel: n.Status.NodeInfo.KernelVersion, Labels: n.Metadata.Labels}
		for _, a := range n.Status.Addresses {
			if a.Type == "InternalIP" {
				h.Addr = a.Address
			}
		}
		for k := range n.Metadata.Labels {
			if role, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok {
				h.Roles = append(h.Roles, role)
			}
		}
		if len(h.Roles) == 0 {
			h.Roles = []string{"worker"}
		}
		for _, c := range n.Status.Conditions {
			if c.Type == "Ready" {
				h.Ready = c.Status == "True"
			}
		}
		h.CPUs = int(parseCPU(n.Status.Capacity["cpu"]))
		h.MemTotal = parseMem(n.Status.Capacity["memory"])
		hosts[i] = h
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.nodeUsage(ctx, &hosts[i])
		}()
	}
	wg.Wait()
	return hosts, nil
}

// nodeUsage reads the kubelet summary through the API server, so it needs no metrics-server.
func (r *Runtime) nodeUsage(ctx context.Context, h *core.Host) {
	out, err := sh.New("kubectl", "--context", r.Opt.Context, "get", "--raw", "/api/v1/nodes/"+h.Name+"/proxy/stats/summary").Output(ctx)
	if err != nil {
		return
	}
	var s struct {
		Node struct {
			CPU struct {
				UsageNanoCores float64 `json:"usageNanoCores"`
			} `json:"cpu"`
			Memory struct {
				WorkingSetBytes int64 `json:"workingSetBytes"`
			} `json:"memory"`
		} `json:"node"`
	}
	if json.Unmarshal(out, &s) != nil {
		return
	}
	if h.CPUs > 0 {
		h.CPUUsed = s.Node.CPU.UsageNanoCores / 1e9 / float64(h.CPUs)
	}
	h.MemUsed = s.Node.Memory.WorkingSetBytes
}

// Shell opens a root shell on a node through a privileged debug pod.
func (r *Runtime) Shell(ctx context.Context, host string, command []string) (*exec.Cmd, error) {
	args := []string{"--context", r.Opt.Context, "-n", r.Opt.Namespace, "debug", "node/" + host, "-it", "--profile=sysadmin", "--image=" + r.Opt.NodeShellImage, "--", "chroot", "/host"}
	if len(command) == 0 {
		command = []string{"sh", "-c", "command -v bash >/dev/null && exec bash -l || exec sh -l"}
	}
	return exec.CommandContext(ctx, "kubectl", append(args, command...)...), nil
}

// ---- queries: read-only kubectl ----

var readOnlyVerbs = map[string]bool{"get": true, "describe": true, "top": true, "logs": true, "events": true,
	"explain": true, "api-resources": true, "api-versions": true, "version": true, "auth": true, "cluster-info": true}

func (r *Runtime) QueryLanguage() string { return "kubectl" }

func (r *Runtime) RunQuery(ctx context.Context, q string) (core.Table, error) {
	args := strings.Fields(q)
	if len(args) > 0 && args[0] == "kubectl" {
		args = args[1:]
	}
	if len(args) == 0 || !readOnlyVerbs[args[0]] {
		return core.Table{}, fmt.Errorf("only read-only kubectl verbs here: %s", strings.Join(sortedKeys(readOnlyVerbs), ", "))
	}
	out, err := r.kubectl(args...).Output(ctx)
	if err != nil {
		return core.Table{}, err
	}
	return TextTable(string(out)), nil
}

// TextTable splits kubectl's column output into a table; anything else becomes one column.
func TextTable(s string) core.Table {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) == 0 {
		return core.Table{}
	}
	head := lines[0]
	cols := columnStarts(head)
	if len(cols) < 2 || !isUpperHeader(head) {
		t := core.Table{Columns: []string{"output"}}
		for _, l := range lines {
			t.Rows = append(t.Rows, []string{l})
		}
		return t
	}
	t := core.Table{}
	for _, l := range lines {
		row := make([]string, len(cols))
		for i, c := range cols {
			end := len(l)
			if i+1 < len(cols) && cols[i+1] < end {
				end = cols[i+1]
			}
			if c < len(l) {
				row[i] = strings.TrimSpace(l[c:end])
			}
		}
		if t.Columns == nil {
			t.Columns = row
			continue
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

// columnStarts finds where header words begin after a gap of two or more spaces,
// so headers such as "NOMINATED NODE" stay one column.
func columnStarts(h string) []int {
	var out []int
	for i := 0; i < len(h); i++ {
		if h[i] != ' ' && (i == 0 || i >= 2 && h[i-1] == ' ' && h[i-2] == ' ') {
			out = append(out, i)
		}
	}
	return out
}

func isUpperHeader(h string) bool { return strings.ToUpper(h) == h }

// ---- actions ----

func (r *Runtime) Actions() []core.Action {
	return []core.Action{
		{Name: "events", Help: "recent events in the namespace", Run: func(ctx context.Context, args []string, out io.Writer) error {
			return r.kubectl("get", "events", "--sort-by=.lastTimestamp").Attach(ctx, nil, out, out)
		}},
		{Name: "render", Help: "print the YAML deploy would apply: render <service>", Run: func(ctx context.Context, args []string, out io.Writer) error {
			if len(args) != 1 {
				return fmt.Errorf("render <service>")
			}
			s, ok := r.env.Project().Services[args[0]]
			if !ok {
				return fmt.Errorf("no service %q", args[0])
			}
			y, err := r.Render(ctx, s, core.Release{})
			if err != nil {
				return err
			}
			_, err = out.Write(y)
			return err
		}},
		{Name: "diff", Help: "what a deploy would change on the cluster: diff <service> [tag]", Run: func(ctx context.Context, args []string, out io.Writer) error {
			if len(args) < 1 {
				return fmt.Errorf("diff <service> [tag]")
			}
			s, ok := r.env.Project().Services[args[0]]
			if !ok {
				return fmt.Errorf("no service %q", args[0])
			}
			rel := core.Release{}
			if len(args) > 1 {
				rel.Image = core.ImageRef(s, r.Opt.Registry, args[1])
			}
			y, err := r.Render(ctx, s, rel)
			if err != nil {
				return err
			}
			// kubectl diff exits 1 when there are differences
			err = r.kubectl("diff", "-f", "-").Attach(ctx, strings.NewReader(string(y)), out, out)
			if err != nil && strings.Contains(err.Error(), "exit status 1") {
				return nil
			}
			return err
		}},
		{Name: "prune", Mutate: true, Help: "delete deployments no rig service owns (dry run without --yes)", Run: func(ctx context.Context, args []string, out io.Writer) error {
			ws, err := r.Discover(ctx)
			if err != nil {
				return err
			}
			yes := core.Confirmed(ctx)
			n := 0
			for _, w := range ws {
				if w.Service != "" || w.Kind != "Deployment" {
					continue
				}
				n++
				if !yes {
					fmt.Fprintf(out, "would delete deployment/%s\n", w.Name)
					continue
				}
				if err := r.kubectl("delete", "deployment", w.Name).Run(ctx); err != nil {
					return err
				}
				fmt.Fprintf(out, "deleted deployment/%s\n", w.Name)
			}
			if !yes && n > 0 {
				fmt.Fprintln(out, "dry run: add --yes to delete")
			}
			return nil
		}},
		{Name: "delete", Mutate: true, Help: "delete what deploy applied for a service: delete <service>", Run: func(ctx context.Context, args []string, out io.Writer) error {
			if len(args) != 1 {
				return fmt.Errorf("delete <service>")
			}
			s, ok := r.env.Project().Services[args[0]]
			if !ok {
				return fmt.Errorf("no service %q", args[0])
			}
			y, err := r.Render(ctx, s, core.Release{})
			if err != nil {
				return err
			}
			return r.kubectl("delete", "--ignore-not-found", "-f", "-").Attach(ctx, strings.NewReader(string(y)), out, out)
		}},
		{Name: "hpa", Mutate: true, Help: "set autoscaler bounds: hpa <service> <min> <max>", Run: func(ctx context.Context, args []string, out io.Writer) error {
			if len(args) != 3 {
				return fmt.Errorf("hpa <service> <min> <max>")
			}
			lo, err1 := strconv.Atoi(args[1])
			hi, err2 := strconv.Atoi(args[2])
			if err1 != nil || err2 != nil {
				return fmt.Errorf("min and max are numbers")
			}
			patch, _ := json.Marshal(map[string]any{"spec": map[string]int{"minReplicas": lo, "maxReplicas": hi}})
			return r.kubectl("patch", "hpa", args[0], "--type=merge", "-p", string(patch)).Attach(ctx, nil, out, out)
		}},
	}
}
