package manifest

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type RenderOptions struct {
	// Vars resolves $VAR and ${VAR} references; unresolved ones are left as written.
	Vars func(string) (string, bool)
	// Image replaces the image of the named container of the workload; "" names the first container.
	Image     string
	Container string
	// Env is added to every container of the workload, replacing entries of the same name.
	Env      map[string]string
	Replicas *int
	// Capabilities are added to the securityContext of the named container (Image's), e.g. SYS_PTRACE for a debugger.
	Capabilities []string
	// Labels are added to the workload and its pod template.
	Labels map[string]string
	// NodePorts keeps the node ports Services already have (service → port → node port), so a redeploy
	// does not ask for a manifest's port that something else took meanwhile.
	NodePorts map[string]map[int]int
	// FreeNodePorts drops node ports NodePorts does not keep, letting the cluster pick them.
	FreeNodePorts bool
}

// UnresolvedError names variables left in images or env values: applying them would run a
// container against a database literally called "$MAIN_DB".
type UnresolvedError struct{ Names []string }

func (e *UnresolvedError) Error() string {
	return "manifest variables without a value: " + strings.Join(e.Names, ", ") + " (set them under the runtime's vars)"
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// Render writes objs as one YAML stream, applying o to workload (which should be one of objs).
func Render(objs []*Object, workload *Object, o RenderOptions) ([]byte, error) {
	var b bytes.Buffer
	missing := map[string]bool{}
	if o.Vars != nil && len(o.Env) > 0 {
		// rig.yaml env may use manifest variables too: database=$MAIN_DB
		env := make(map[string]string, len(o.Env))
		for k, v := range o.Env {
			n := &yaml.Node{Kind: yaml.ScalarNode, Value: v}
			substitute(n, o.Vars, "value", missing)
			env[k] = n.Value
		}
		o.Env = env
	}
	for i, obj := range objs {
		n := clone(obj.Node)
		if o.Vars != nil {
			substitute(n, o.Vars, "", missing)
		}
		var m map[string]any
		if err := n.Decode(&m); err != nil {
			return nil, fmt.Errorf("%s: %w", obj, err)
		}
		if obj == workload {
			if err := patchWorkload(m, obj.Kind, o); err != nil {
				return nil, fmt.Errorf("%s: %w", obj, err)
			}
		}
		if obj.Kind == "Service" {
			keepNodePorts(m, o.NodePorts[obj.Name], o.FreeNodePorts)
		}
		if i > 0 {
			b.WriteString("---\n")
		}
		out, err := yaml.Marshal(m)
		if err != nil {
			return nil, err
		}
		b.Write(out)
	}
	var left []string
	for k := range missing {
		// a reference the workload patch replaced (the image's $TAG, say) is not missing
		if bytes.Contains(b.Bytes(), []byte("$"+k)) {
			left = append(left, k)
		}
	}
	if len(left) > 0 {
		sort.Strings(left)
		return b.Bytes(), &UnresolvedError{Names: left}
	}
	return b.Bytes(), nil
}

func clone(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, x := range n.Content {
		c.Content[i] = clone(x)
	}
	return &c
}

// substitute fills variables; key is the mapping key holding n, so references left in an image or an
// env value are reported (a shell command may use $HOME on purpose).
func substitute(n *yaml.Node, vars func(string) (string, bool), key string, missing map[string]bool) {
	if n.Kind == yaml.ScalarNode {
		v := varRef.ReplaceAllStringFunc(n.Value, func(m string) string {
			name := strings.Trim(m, "${}")
			if val, ok := vars(name); ok {
				return val
			}
			// lower case is literal text more often than a variable: a password like 'x9$d'
			if (key == "image" || key == "value") && name == strings.ToUpper(name) {
				missing[name] = true
			}
			return m
		})
		if v != n.Value {
			n.Value = v
			if n.Style == 0 {
				n.Tag = "" // let `replicas: $N` become an int again
			}
		}
		return
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			substitute(n.Content[i+1], vars, n.Content[i].Value, missing)
		}
		return
	}
	for _, c := range n.Content {
		substitute(c, vars, key, missing)
	}
}

func keepNodePorts(m map[string]any, live map[int]int, free bool) {
	if len(live) == 0 && !free {
		return
	}
	ports, _ := child(m, "spec")["ports"].([]any)
	for _, p := range ports {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		port, _ := pm["port"].(int)
		if np, ok := live[port]; ok {
			pm["nodePort"] = np
		} else if free {
			delete(pm, "nodePort")
		}
	}
}

func patchWorkload(m map[string]any, kind string, o RenderOptions) error {
	spec := child(m, "spec")
	if o.Replicas != nil && (kind == "Deployment" || kind == "StatefulSet" || kind == "ReplicaSet") {
		spec["replicas"] = *o.Replicas
	}
	addLabels(child(m, "metadata"), o.Labels)
	tmpl := m
	switch kind {
	case "Pod":
	case "CronJob":
		tmpl = child(child(child(spec, "jobTemplate"), "spec"), "template")
	default:
		tmpl = child(spec, "template")
	}
	addLabels(child(tmpl, "metadata"), o.Labels)
	podSpec := child(m, "spec")
	if kind != "Pod" {
		podSpec = child(tmpl, "spec")
	}
	containers, _ := podSpec["containers"].([]any)
	if len(containers) == 0 {
		return fmt.Errorf("no containers")
	}
	found := o.Container == ""
	for i, c := range containers {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		target := o.Container == "" && i == 0 || cm["name"] == o.Container
		if o.Image != "" && target {
			cm["image"] = o.Image
			found = true
		}
		if target && len(o.Capabilities) > 0 {
			addCapabilities(cm, o.Capabilities)
		}
		if len(o.Env) > 0 {
			cm["env"] = mergeEnv(cm["env"], o.Env)
		}
	}
	if !found {
		return fmt.Errorf("no container %q", o.Container)
	}
	return nil
}

func addCapabilities(container map[string]any, caps []string) {
	add, _ := child(child(container, "securityContext"), "capabilities")["add"].([]any)
	for _, c := range caps {
		if !slices.Contains(add, any(c)) {
			add = append(add, c)
		}
	}
	child(child(container, "securityContext"), "capabilities")["add"] = add
}

func mergeEnv(cur any, add map[string]string) []any {
	list, _ := cur.([]any)
	var out []any
	for _, e := range list {
		if em, ok := e.(map[string]any); ok {
			if _, replaced := add[fmt.Sprint(em["name"])]; replaced {
				continue
			}
		}
		out = append(out, e)
	}
	names := make([]string, 0, len(add))
	for k := range add {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		out = append(out, map[string]any{"name": k, "value": add[k]})
	}
	return out
}

func addLabels(meta map[string]any, labels map[string]string) {
	if len(labels) == 0 {
		return
	}
	l := child(meta, "labels")
	for k, v := range labels {
		l[k] = v
	}
}

func child(m map[string]any, key string) map[string]any {
	c, ok := m[key].(map[string]any)
	if !ok {
		c = map[string]any{}
		m[key] = c
	}
	return c
}
