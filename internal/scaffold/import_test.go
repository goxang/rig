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
