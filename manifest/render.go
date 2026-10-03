package manifest

import (
	"bytes"
	"fmt"
	"regexp"
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
	// Labels are added to the workload and its pod template.
	Labels map[string]string
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// Render writes objs as one YAML stream, applying o to workload (which should be one of objs).
func Render(objs []*Object, workload *Object, o RenderOptions) ([]byte, error) {
	var b bytes.Buffer
	for i, obj := range objs {
		n := clone(obj.Node)
		if o.Vars != nil {
			substitute(n, o.Vars)
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
		if i > 0 {
			b.WriteString("---\n")
		}
		out, err := yaml.Marshal(m)
		if err != nil {
			return nil, err
		}
		b.Write(out)
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

func substitute(n *yaml.Node, vars func(string) (string, bool)) {
	if n.Kind == yaml.ScalarNode {
		v := varRef.ReplaceAllStringFunc(n.Value, func(m string) string {
			name := strings.Trim(m, "${}")
			if val, ok := vars(name); ok {
				return val
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
	for _, c := range n.Content {
		substitute(c, vars)
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
	podSpec := tmpl
	if kind != "Pod" {
		podSpec = child(tmpl, "spec")
	} else {
		podSpec = child(m, "spec")
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
		if o.Image != "" && (o.Container == "" && i == 0 || cm["name"] == o.Container) {
			cm["image"] = o.Image
			found = true
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
