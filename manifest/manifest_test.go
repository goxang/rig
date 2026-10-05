package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const app = `
apiVersion: apps/v1
kind: Deployment
metadata: { name: api, labels: { app: api } }
spec:
  replicas: $REPLICAS
  selector: { matchLabels: { app: api } }
  template:
    metadata: { labels: { app: api, tier: web } }
    spec:
      serviceAccountName: api
      containers:
        - name: api
          image: registry.local/api:$TAG
          envFrom: [{ configMapRef: { name: api-config } }]
          env:
            - { name: KEEP, value: "1" }
            - { name: LOG_LEVEL, value: info }
          readinessProbe: { httpGet: { path: /ready, port: 8080 } }
          resources: { requests: { cpu: 100m } }
      volumes:
        - { name: certs, secret: { secretName: api-tls } }
---
apiVersion: v1
kind: Service
metadata: { name: api }
spec: { selector: { app: api }, ports: [{ port: 80, targetPort: 8080 }] }
---
apiVersion: v1
kind: ConfigMap
metadata: { name: api-config }
data: { A: "1" }
`

const extra = `
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: { name: api }
spec: { scaleTargetRef: { kind: Deployment, name: api }, minReplicas: 1, maxReplicas: 5 }
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: { name: web }
spec:
  rules:
    - http: { paths: [{ path: /, backend: { service: { name: api, port: { number: 80 } } } }, { path: /old, backend: { service: { name: gone } } }] }
---
apiVersion: v1
kind: Service
metadata: { name: orphan }
spec: { selector: { app: nothing } }
`

func scanTestdata(t *testing.T) *Set {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "services", "api", "api.yaml"), []byte(app), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "extra.yml"), []byte(extra), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "chart.yaml"), []byte("{{ .Values.x }}: [\n"), 0o644)
	s, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func has(es []Edge, from, to, rel string) bool {
	for _, e := range es {
		if e.From == from && e.To == to && e.Rel == rel {
			return true
		}
	}
	return false
}

func TestGraph(t *testing.T) {
	s := scanTestdata(t)
	if len(s.Objects) != 6 {
		t.Fatalf("objects = %d", len(s.Objects))
	}
	for _, want := range [][3]string{
		{"Service/api", "Deployment/api", "selects"},
		{"HorizontalPodAutoscaler/api", "Deployment/api", "scales"},
		{"Ingress/web", "Service/api", "routes"},
		{"Deployment/api", "ConfigMap/api-config", "env from"},
		{"Deployment/api", "Secret/api-tls", "mounts"},
		{"Deployment/api", "ServiceAccount/api", "runs as"},
	} {
		if !has(s.Edges, want[0], want[1], want[2]) {
			t.Errorf("missing edge %v", want)
		}
	}
	text := ""
	for _, i := range s.Issues {
		text += i.Level + " " + i.Object + " " + i.Text + "\n"
	}
	for _, want := range []string{
		"error Ingress/web routes Service/gone",
		"warn Service/orphan selector matches no workload",
		"warn Deployment/api mounts Secret/api-tls",
		"info  " + filepath.Base("chart.yaml"),
	} {
		if !strings.Contains(text, strings.Split(want, "  ")[0]) {
			t.Errorf("issue %q not reported in:\n%s", want, text)
		}
	}
}

func TestBundleAndFind(t *testing.T) {
	s := scanTestdata(t)
	w := s.FindWorkload("api")
	if w == nil || w.ID() != "Deployment/api" {
		t.Fatalf("FindWorkload = %v", w)
	}
	var ids []string
	for _, o := range s.Bundle(w) {
		ids = append(ids, o.ID())
	}
	got := strings.Join(ids, " ")
	for _, want := range []string{"Deployment/api", "ConfigMap/api-config", "Service/api", "HorizontalPodAutoscaler/api"} {
		if !strings.Contains(got, want) {
			t.Errorf("bundle %q lacks %s", got, want)
		}
	}
	if strings.Contains(got, "Ingress") {
		t.Errorf("bundle pulled in the ingress, which points at the service, not the workload: %s", got)
	}
	if s.FindWorkload("deployment/nope") != nil {
		t.Error("found a workload that does not exist")
	}
}

func TestRender(t *testing.T) {
	s := scanTestdata(t)
	w := s.FindWorkload("api")
	n := 4
	out, err := Render(s.Bundle(w), w, RenderOptions{
		Vars: func(k string) (string, bool) {
			v, ok := map[string]string{"TAG": "v9", "REPLICAS": "2"}[k]
			return v, ok
		},
		Image:        "localhost:5001/api:new",
		Env:          map[string]string{"LOG_LEVEL": "debug", "PPROF": "true"},
		Replicas:     &n,
		Labels:       map[string]string{"app.kubernetes.io/managed-by": "rig"},
		Capabilities: []string{"SYS_PTRACE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	y := string(out)
	for _, want := range []string{"image: localhost:5001/api:new", "replicas: 4", "value: debug", "name: PPROF", "name: KEEP", "app.kubernetes.io/managed-by: rig", "kind: ConfigMap", "- SYS_PTRACE"} {
		if !strings.Contains(y, want) {
			t.Errorf("render lacks %q:\n%s", want, y)
		}
	}
	if strings.Count(y, "LOG_LEVEL") != 1 || strings.Contains(y, "value: info") {
		t.Errorf("env entry not replaced:\n%s", y)
	}

	out, _ = Render([]*Object{w}, w, RenderOptions{Vars: func(k string) (string, bool) { return map[string]string{"TAG": "v9", "REPLICAS": "2"}[k], true }})
	if !strings.Contains(string(out), "replicas: 2\n") || !strings.Contains(string(out), "api:v9") {
		t.Errorf("vars not substituted (or replicas left a string):\n%s", out)
	}
}

func TestRenderRefusesUnresolvedImageAndEnv(t *testing.T) {
	s := scanTestdata(t)
	w := s.FindWorkload("api")
	_, err := Render([]*Object{w}, w, RenderOptions{Vars: func(string) (string, bool) { return "", false }})
	var ue *UnresolvedError
	if !errors.As(err, &ue) || len(ue.Names) == 0 {
		t.Fatalf("unresolved $TAG in the image must fail the render, got %v", err)
	}
	if _, err := Render([]*Object{w}, w, RenderOptions{Vars: func(string) (string, bool) { return "", false }, Image: "x:1"}); err != nil && strings.Contains(err.Error(), "TAG") {
		t.Fatalf("an image the patch replaces is not unresolved: %v", err)
	}
}

func TestRenderExpandsEnvVars(t *testing.T) {
	s := scanTestdata(t)
	w := s.FindWorkload("api")
	out, err := Render([]*Object{w}, w, RenderOptions{
		Vars: func(k string) (string, bool) {
			v, ok := map[string]string{"TAG": "v9", "REPLICAS": "1", "DB": "lt2"}[k]
			return v, ok
		},
		Env: map[string]string{"CONN": "sqlserver://h?database=$DB"},
	})
	if err != nil || !strings.Contains(string(out), "database=lt2") {
		t.Fatalf("err %v\n%s", err, out)
	}
}

func TestFolders(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	write(".docker/k8s/test/a.yaml", "apiVersion: v1\nkind: Service\n")
	write(".docker/k8s/test/b.yml", "kind: Pod\napiVersion: v1\n")
	write(".git/x.yaml", "apiVersion: v1\nkind: Service\n")
	write("configs/app.yaml", "port: 1\n")
	got := Folders(root)
	if len(got) != 1 || got[filepath.Join(".docker", "k8s", "test")] != 2 {
		t.Fatalf("%v", got)
	}
}
