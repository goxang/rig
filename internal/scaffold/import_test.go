package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goxang/rig/spec"
)

func TestImportCompose(t *testing.T) {
	dir := t.TempDir()
	deploy := filepath.Join(dir, "deploy", "vds")
	os.MkdirAll(deploy, 0o755)
	os.WriteFile(filepath.Join(deploy, ".env"), []byte("PG_USER=app\n"), 0o644)
	os.WriteFile(filepath.Join(deploy, "web.env"), []byte("SECRET_KEY=s\n"), 0o644)
	os.WriteFile(filepath.Join(deploy, "docker-compose.yml"), []byte(`services:
  web:
    build: { context: ../.. }
    command: gunicorn app.wsgi
    ports: ["8000:8000"]
    env_file: web.env
    environment: { DB_USER: "${PG_USER}", DB_HOST: db }
    volumes: ["./media:/app/media"]
    depends_on: [db]
  db:
    image: postgres:16
`), 0o644)
	os.WriteFile(filepath.Join(dir, "rig.yaml"), []byte(`project: momentz
imports: [{compose: deploy/vds/docker-compose.yml}]
`), 0o644)
	p, _, err := spec.Load(filepath.Join(dir, "rig.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	web := p.Services["web"]
	if web == nil || p.Services["db"].Role != spec.RoleInfra {
		t.Fatalf("services = %v", p.ServiceNames())
	}
	if web.Env["DB_USER"] != "app" || web.Env["SECRET_KEY"] != "s" || web.Ports["http"] != 8000 {
		t.Fatalf("web env %v ports %v", web.Env, web.Ports)
	}
	if web.Build.Context != "." || web.Build.Dockerfile != "Dockerfile" {
		t.Fatalf("build = %+v", web.Build)
	}
	var sec struct {
		Args    []string `yaml:"args"`
		Volumes []string `yaml:"volumes"`
	}
	if _, err := web.Section("docker", &sec); err != nil || sec.Volumes[0] != "./deploy/vds/media:/app/media" || sec.Args[0] != "gunicorn" {
		t.Fatalf("docker section %+v %v", sec, err)
	}
}

func TestImportManifestsMergesRigYAML(t *testing.T) {
	dir := t.TempDir()
	k8s := filepath.Join(dir, "k8s")
	os.MkdirAll(k8s, 0o755)
	os.WriteFile(filepath.Join(k8s, "api.yml"), []byte(`apiVersion: apps/v1
kind: Deployment
metadata: { name: api }
spec:
  template:
    spec:
      containers:
        - { name: api, image: "$REGISTRY/api:$TAG", ports: [{ name: http, containerPort: 8080 }] }
---
apiVersion: apps/v1
kind: StatefulSet
metadata: { name: cache }
spec:
  template:
    spec:
      containers: [{ name: redis, image: "redis:7", ports: [{ containerPort: 6379 }] }]
`), 0o644)
	os.WriteFile(filepath.Join(dir, "rig.yaml"), []byte(`project: p
imports: [{kubernetes: k8s}]
services:
  api: { build: { go: ./cmd/api }, groups: [core] }
environments:
  k: { runtime: { type: kubernetes } }
`), 0o644)
	p, _, err := spec.Load(filepath.Join(dir, "rig.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	api, cache := p.Services["api"], p.Services["cache"]
	if api == nil || cache == nil {
		t.Fatalf("services = %v", p.ServiceNames())
	}
	if api.Image != "api" || api.Ports["http"] != 8080 || api.Build == nil || api.Build.Go != "./cmd/api" || len(api.Groups) != 1 {
		t.Fatalf("api = %+v", api)
	}
	var sec struct{ Workload string }
	if ok, _ := cache.Section("k8s", &sec); !ok || sec.Workload != "statefulset/cache" || cache.Role != spec.RoleInfra || cache.Ports["redis"] != 6379 {
		t.Fatalf("cache = %+v %+v", cache, sec)
	}
	if got := p.ImportPaths("kubernetes"); len(got) != 1 || got[0] != k8s {
		t.Fatalf("import paths = %v", got)
	}
}

func TestImportSkipsWorkloadRigYAMLNamesOtherwise(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "k8s"), 0o755)
	os.WriteFile(filepath.Join(dir, "k8s", "d.yml"), []byte(`apiVersion: apps/v1
kind: Deployment
metadata: { name: domainsvc }
spec: { template: { spec: { containers: [{ name: d, image: domain }] } } }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: hsmmock }
spec: { template: { spec: { containers: [{ name: h, image: hsm }] } } }
`), 0o644)
	os.WriteFile(filepath.Join(dir, "rig.yaml"), []byte(`project: p
imports: [{kubernetes: k8s}]
services:
  domain: { k8s: { workload: domainsvc } }
`), 0o644)
	p, _, err := spec.Load(filepath.Join(dir, "rig.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ServiceNames(); len(got) != 2 || p.Services["domainsvc"] != nil || p.Services["hsmmock"] == nil {
		t.Fatalf("services = %v", got)
	}
}
