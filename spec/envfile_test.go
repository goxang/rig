package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvFileUnderEnv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("# db\nexport DB_USER=app\nDB_PASS=\"p #1\"\nDB_NAME=shop # comment\nPORT=1\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "rig.yaml"), []byte(`project: p
services:
  api: { env_file: [.env], env: { PORT: "2" } }
`), 0o644)
	p, _, err := Load(filepath.Join(dir, "rig.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"DB_USER": "app", "DB_PASS": "p #1", "DB_NAME": "shop", "PORT": "2"}
	for k, v := range want {
		if got := p.Services["api"].Env[k]; got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
