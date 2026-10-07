package scaffold

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/spec"
)

func write(t *testing.T, dir string, files map[string]string) {
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInitFromComposeSourcesAndManifests(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shop")
	write(t, dir, map[string]string{
		"docker-compose.yml": `services:
  api: { build: ./api, ports: ["8000:8000"], depends_on: [db] }
  db: { image: "postgres:16", environment: [POSTGRES_PASSWORD=pw] }
`,
		"api/requirements.txt": "fastapi\npytest\n",
		"api/main.py":          "app = FastAPI()\n",
		"site/manage.py":       "",
		"deploy/worker.yaml":   "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: worker}\nspec: {template: {spec: {containers: [{image: acme/worker}]}}}\n",
	})
	p, err := Detect(context.Background(), dir, Options{With: []string{"redis"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rig.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	proj, _, err := spec.Load(filepath.Join(dir, "rig.yaml"), "")
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	api := proj.Services["api"]
	if api == nil || api.Run == nil || strings.Join(api.Run.Command, " ") != "uvicorn main:app --host 0.0.0.0 --port 8000" || api.Build.Context != "api" {
		t.Fatalf("api: %+v\n%s", api, raw)
	}
	if db := proj.Services["db"]; db.Role != "infra" || db.Ports["pg"] != 5432 || !db.Shared {
		t.Fatalf("db: %+v", db)
	}
	for _, n := range []string{"site", "worker", "redis"} {
		if proj.Services[n] == nil {
			t.Errorf("no service %s\n%s", n, raw)
		}
	}
	for _, c := range []string{"db", "cache"} {
		if proj.Components[c] == nil {
			t.Errorf("no component %s", c)
		}
	}
	if proj.Tests["api"] == nil || proj.Tests["site"] == nil {
		t.Errorf("tests: %v", proj.Tests)
	}
	if proj.Environments["local"].Infra != "docker" || len(proj.Manifests) != 1 {
		t.Errorf("local env or manifests wrong\n%s", raw)
	}
}

func TestComposeDuplicateEnvLastWins(t *testing.T) {
	var c composeService
	if err := yaml.Unmarshal([]byte("environment: [A=1, B=2, A=3]\n"), &c); err != nil {
		t.Fatal(err)
	}
	s := fromCompose("x", c, "compose.yml")
	if len(s.Env) != 2 || s.Env[0] != [2]string{"A", "3"} {
		t.Fatalf("env = %v", s.Env)
	}
}
