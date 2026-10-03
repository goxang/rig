package manifest

import (
	"fmt"
	"sort"
	"strings"
)

// Edge is a relation between two objects; To may name an object the set does not hold (Missing).
type Edge struct {
	From    string
	To      string
	Rel     string
	Missing bool
}

type ref struct {
	Name string `yaml:"name"`
}

type Container struct {
	Name  string `yaml:"name"`
	Image string `yaml:"image"`
	Env   []struct {
		Name      string `yaml:"name"`
		Value     string `yaml:"value"`
		ValueFrom *struct {
			ConfigMapKeyRef *ref `yaml:"configMapKeyRef"`
			SecretKeyRef    *ref `yaml:"secretKeyRef"`
		} `yaml:"valueFrom"`
	} `yaml:"env"`
	EnvFrom []struct {
		ConfigMapRef *ref `yaml:"configMapRef"`
		SecretRef    *ref `yaml:"secretRef"`
	} `yaml:"envFrom"`
	Ports []struct {
		Name          string `yaml:"name"`
		ContainerPort int    `yaml:"containerPort"`
	} `yaml:"ports"`
	Resources struct {
		Limits   map[string]string `yaml:"limits"`
		Requests map[string]string `yaml:"requests"`
	} `yaml:"resources"`
	ReadinessProbe any `yaml:"readinessProbe"`
	LivenessProbe  any `yaml:"livenessProbe"`
}

type PodSpec struct {
	ServiceAccountName string      `yaml:"serviceAccountName"`
	Containers         []Container `yaml:"containers"`
	InitContainers     []Container `yaml:"initContainers"`
	ImagePullSecrets   []ref       `yaml:"imagePullSecrets"`
	Volumes            []struct {
		Name      string `yaml:"name"`
		ConfigMap *ref   `yaml:"configMap"`
		Secret    *struct {
			SecretName string `yaml:"secretName"`
		} `yaml:"secret"`
		PersistentVolumeClaim *struct {
			ClaimName string `yaml:"claimName"`
		} `yaml:"persistentVolumeClaim"`
	} `yaml:"volumes"`
}

type PodTemplate struct {
	Labels map[string]string
	Spec   PodSpec
}

// Template returns a workload's pod template.
func Template(o *Object) (PodTemplate, bool) {
	if !IsWorkload(o.Kind) {
		return PodTemplate{}, false
	}
	type tmpl struct {
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
		Spec PodSpec `yaml:"spec"`
	}
	var w struct {
		Spec struct {
			Template    tmpl `yaml:"template"`
			JobTemplate struct {
				Spec struct {
					Template tmpl `yaml:"template"`
				} `yaml:"spec"`
			} `yaml:"jobTemplate"`
		} `yaml:"spec"`
	}
	var t tmpl
	switch o.Kind {
	case "Pod":
		if o.Decode(&t) != nil {
			return PodTemplate{}, false
		}
	case "CronJob":
		if o.Decode(&w) != nil {
			return PodTemplate{}, false
		}
		t = w.Spec.JobTemplate.Spec.Template
	default:
		if o.Decode(&w) != nil {
			return PodTemplate{}, false
		}
		t = w.Spec.Template
	}
	return PodTemplate{Labels: t.Metadata.Labels, Spec: t.Spec}, true
}

func (s *Set) edge(from *Object, kind, name, rel string) {
	if name == "" {
		return
	}
	to := kind + "/" + name
	s.Edges = append(s.Edges, Edge{From: from.ID(), To: to, Rel: rel, Missing: len(s.byID[to]) == 0})
}

func (s *Set) link() {
	s.Edges = nil
	for _, o := range s.Objects {
		if t, ok := Template(o); ok {
			s.linkPod(o, t)
		}
		switch o.Kind {
		case "Service":
			var v struct {
				Spec struct {
					Selector map[string]string `yaml:"selector"`
				} `yaml:"spec"`
			}
			_ = o.Decode(&v)
			s.selects(o, v.Spec.Selector, "selects")
		case "Ingress":
			for _, svc := range ingressBackends(o) {
				s.edge(o, "Service", svc, "routes")
			}
		case "HorizontalPodAutoscaler", "VerticalPodAutoscaler", "ScaledObject":
			var v struct {
				Spec struct {
					ScaleTargetRef struct {
						Kind string `yaml:"kind"`
						Name string `yaml:"name"`
					} `yaml:"scaleTargetRef"`
					TargetRef struct {
						Kind string `yaml:"kind"`
						Name string `yaml:"name"`
					} `yaml:"targetRef"`
				} `yaml:"spec"`
			}
			_ = o.Decode(&v)
			t := v.Spec.ScaleTargetRef
			if t.Name == "" {
				t = v.Spec.TargetRef
			}
			if t.Kind == "" {
				t.Kind = "Deployment"
			}
			s.edge(o, t.Kind, t.Name, "scales")
		case "PodDisruptionBudget", "NetworkPolicy", "ServiceMonitor", "PodMonitor":
			var v struct {
				Spec struct {
					Selector struct {
						MatchLabels map[string]string `yaml:"matchLabels"`
					} `yaml:"selector"`
					PodSelector struct {
						MatchLabels map[string]string `yaml:"matchLabels"`
					} `yaml:"podSelector"`
				} `yaml:"spec"`
			}
			_ = o.Decode(&v)
			sel := v.Spec.Selector.MatchLabels
			if o.Kind == "NetworkPolicy" {
				sel = v.Spec.PodSelector.MatchLabels
			}
			if o.Kind == "ServiceMonitor" {
				for _, x := range s.Objects {
					if x.Kind == "Service" && subset(sel, x.Labels) && len(sel) > 0 {
						s.edge(o, "Service", x.Name, "scrapes")
					}
				}
				continue
			}
			s.selects(o, sel, map[string]string{"PodDisruptionBudget": "protects", "NetworkPolicy": "governs", "PodMonitor": "scrapes"}[o.Kind])
		case "RoleBinding", "ClusterRoleBinding":
			var v struct {
				RoleRef struct {
					Kind string `yaml:"kind"`
					Name string `yaml:"name"`
				} `yaml:"roleRef"`
				Subjects []struct {
					Kind string `yaml:"kind"`
					Name string `yaml:"name"`
				} `yaml:"subjects"`
			}
			_ = o.Decode(&v)
			s.edge(o, v.RoleRef.Kind, v.RoleRef.Name, "grants")
			for _, sub := range v.Subjects {
				if sub.Kind == "ServiceAccount" {
					s.edge(o, "ServiceAccount", sub.Name, "binds")
				}
			}
		}
	}
}

func (s *Set) linkPod(o *Object, t PodTemplate) {
	if o.Kind == "StatefulSet" {
		var v struct {
			Spec struct {
				ServiceName string `yaml:"serviceName"`
			} `yaml:"spec"`
		}
		_ = o.Decode(&v)
		s.edge(o, "Service", v.Spec.ServiceName, "headless")
	}
	if t.Spec.ServiceAccountName != "" && t.Spec.ServiceAccountName != "default" {
		s.edge(o, "ServiceAccount", t.Spec.ServiceAccountName, "runs as")
	}
	for _, v := range t.Spec.Volumes {
		switch {
		case v.ConfigMap != nil:
			s.edge(o, "ConfigMap", v.ConfigMap.Name, "mounts")
		case v.Secret != nil:
			s.edge(o, "Secret", v.Secret.SecretName, "mounts")
		case v.PersistentVolumeClaim != nil:
			s.edge(o, "PersistentVolumeClaim", v.PersistentVolumeClaim.ClaimName, "mounts")
		}
	}
	for _, p := range t.Spec.ImagePullSecrets {
		s.edge(o, "Secret", p.Name, "pulls with")
	}
	seen := map[string]bool{}
	once := func(kind, name, rel string) {
		if k := kind + "/" + name; name != "" && !seen[k] {
			seen[k] = true
			s.edge(o, kind, name, rel)
		}
	}
	for _, c := range append(t.Spec.InitContainers, t.Spec.Containers...) {
		for _, e := range c.EnvFrom {
			if e.ConfigMapRef != nil {
				once("ConfigMap", e.ConfigMapRef.Name, "env from")
			}
			if e.SecretRef != nil {
				once("Secret", e.SecretRef.Name, "env from")
			}
		}
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				continue
			}
			if r := e.ValueFrom.ConfigMapKeyRef; r != nil {
				once("ConfigMap", r.Name, "env from")
			}
			if r := e.ValueFrom.SecretKeyRef; r != nil {
				once("Secret", r.Name, "env from")
			}
		}
	}
}

func (s *Set) selects(o *Object, sel map[string]string, rel string) {
	if len(sel) == 0 {
		return
	}
	for _, x := range s.Objects {
		if t, ok := Template(x); ok && x.Kind != "Job" && subset(sel, t.Labels) {
			s.Edges = append(s.Edges, Edge{From: o.ID(), To: x.ID(), Rel: rel})
		}
	}
}

func ingressBackends(o *Object) []string {
	var v struct {
		Spec struct {
			DefaultBackend struct {
				Service ref `yaml:"service"`
			} `yaml:"defaultBackend"`
			Rules []struct {
				HTTP struct {
					Paths []struct {
						Backend struct {
							Service     ref    `yaml:"service"`
							ServiceName string `yaml:"serviceName"`
						} `yaml:"backend"`
					} `yaml:"paths"`
				} `yaml:"http"`
			} `yaml:"rules"`
		} `yaml:"spec"`
	}
	_ = o.Decode(&v)
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	add(v.Spec.DefaultBackend.Service.Name)
	for _, r := range v.Spec.Rules {
		for _, p := range r.HTTP.Paths {
			add(p.Backend.Service.Name)
			add(p.Backend.ServiceName)
		}
	}
	return out
}

func subset(sel, labels map[string]string) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// Out and In are the edges leaving and entering an object.
func (s *Set) Out(id string) []Edge { return s.filter(func(e Edge) bool { return e.From == id }) }
func (s *Set) In(id string) []Edge  { return s.filter(func(e Edge) bool { return e.To == id }) }

func (s *Set) filter(f func(Edge) bool) []Edge {
	var out []Edge
	for _, e := range s.Edges {
		if f(e) {
			out = append(out, e)
		}
	}
	return out
}

// Bundle is a workload plus everything in the set that exists for it: the Services selecting it,
// what scales or protects it, and the ConfigMaps/Secrets/PVCs/ServiceAccounts it uses — what a
// deploy of that one workload has to apply.
func (s *Set) Bundle(workload *Object) []*Object {
	seen := map[*Object]bool{workload: true}
	out := []*Object{workload}
	add := func(id string) {
		for _, o := range s.byID[id] {
			if !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
	}
	for _, e := range s.Out(workload.ID()) {
		add(e.To)
	}
	for _, e := range s.In(workload.ID()) {
		add(e.From)
	}
	sort.SliceStable(out[1:], func(i, j int) bool { return kindOrder(out[1+i].Kind) < kindOrder(out[1+j].Kind) })
	return out
}

func kindOrder(k string) int {
	for i, x := range []string{"Namespace", "ServiceAccount", "Secret", "ConfigMap", "PersistentVolumeClaim", "Role", "RoleBinding", "Service"} {
		if k == x {
			return i
		}
	}
	return 100
}

// Workloads lists the workload objects.
func (s *Set) Workloads() []*Object {
	var out []*Object
	for _, o := range s.Objects {
		if IsWorkload(o.Kind) {
			out = append(out, o)
		}
	}
	return out
}

// FindWorkload finds the workload for a service: by exact kind/name, by name, or by the
// app.kubernetes.io/name or app label.
func (s *Set) FindWorkload(kindName string) *Object {
	kind, name, ok := strings.Cut(kindName, "/")
	if !ok {
		kind, name = "", kindName
	}
	for _, o := range s.Workloads() {
		if o.Name == name && (kind == "" || strings.EqualFold(o.Kind, kind)) {
			return o
		}
	}
	if kind != "" {
		return nil
	}
	for _, o := range s.Workloads() {
		if o.Labels["app.kubernetes.io/name"] == name || o.Labels["app"] == name {
			return o
		}
	}
	return nil
}

func (s *Set) lint() {
	seen := map[string]string{}
	for _, o := range s.Objects {
		key := o.Namespace + "/" + o.ID()
		if prev, dup := seen[key]; dup && o.Name != "" {
			s.issue("warn", o.ID(), o.File, "also defined in "+prev)
		}
		seen[key] = o.File
		if o.Name == "" {
			s.issue("error", o.Kind, o.File, "no metadata.name")
		}
		t, ok := Template(o)
		if !ok {
			continue
		}
		for _, c := range t.Spec.Containers {
			img := c.Image
			switch {
			case img == "":
				s.issue("error", o.ID(), o.File, fmt.Sprintf("container %s has no image", c.Name))
			case strings.HasSuffix(img, ":latest") || !strings.Contains(lastSegment(img), ":") && !strings.Contains(img, "@") && !strings.Contains(img, "$"):
				s.issue("warn", o.ID(), o.File, fmt.Sprintf("container %s: image %s is not pinned", c.Name, img))
			}
			if len(c.Resources.Requests) == 0 {
				s.issue("info", o.ID(), o.File, fmt.Sprintf("container %s has no resource requests", c.Name))
			}
			if c.ReadinessProbe == nil && o.Kind != "Job" && o.Kind != "CronJob" {
				s.issue("info", o.ID(), o.File, fmt.Sprintf("container %s has no readiness probe", c.Name))
			}
		}
	}
	for _, e := range s.Edges {
		if !e.Missing {
			continue
		}
		level := "warn"
		if e.Rel == "scales" || e.Rel == "routes" {
			level = "error"
		}
		s.issue(level, e.From, s.byID[e.From][0].File, fmt.Sprintf("%s %s, which is not in these manifests", e.Rel, e.To))
	}
	for _, o := range s.Objects {
		if o.Kind != "Service" {
			continue
		}
		var v struct {
			Spec struct {
				Selector     map[string]string `yaml:"selector"`
				ExternalName string            `yaml:"externalName"`
			} `yaml:"spec"`
		}
		_ = o.Decode(&v)
		if len(v.Spec.Selector) > 0 && len(s.Out(o.ID())) == 0 {
			s.issue("warn", o.ID(), o.File, "selector matches no workload here")
		}
	}
	sort.SliceStable(s.Issues, func(i, j int) bool { return levelRank(s.Issues[i].Level) < levelRank(s.Issues[j].Level) })
}

func levelRank(l string) int {
	return map[string]int{"error": 0, "warn": 1, "info": 2}[l]
}

func lastSegment(img string) string {
	if i := strings.LastIndex(img, "/"); i >= 0 {
		return img[i+1:]
	}
	return img
}
