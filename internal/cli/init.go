package cli

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

type initService struct {
	Name, Main, Dockerfile string
}

type initData struct {
	Project   string
	Services  []initService
	Godev     bool
	Manifests []string
	Inline    string
}

// initProject writes rig.yaml from what the directory already has; it guesses, the user edits.
func initProject(ctx context.Context, force bool) error {
	if _, err := os.Stat("rig.yaml"); err == nil && !force {
		return fmt.Errorf("rig.yaml exists (--force to overwrite)")
	}
	wd, _ := os.Getwd()
	d := initData{Project: strings.ToLower(filepath.Base(wd))}
	if _, err := os.Stat(".godev.yaml"); err == nil {
		d.Godev = true
	}
	if _, err := os.Stat("go.mod"); err == nil && !d.Godev {
		out, _ := exec.CommandContext(ctx, "go", "list", "-f", `{{if eq .Name "main"}}{{.Dir}}{{end}}`, "./...").Output()
		for _, dir := range strings.Fields(string(out)) {
			rel, _ := filepath.Rel(wd, dir)
			if strings.Contains(rel, "test") || strings.Contains(rel, "example") {
				continue
			}
			s := initService{Name: strings.ToLower(filepath.Base(dir)), Main: "./" + rel}
			if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
				s.Dockerfile = filepath.Join(rel, "Dockerfile")
			}
			d.Services = append(d.Services, s)
		}
		if len(d.Services) > 40 {
			d.Services = d.Services[:40]
		}
	}
	d.Manifests = findManifestDirs(wd)
	d.Inline = strings.Join(d.Manifests, ", ")

	var b bytes.Buffer
	if err := initTmpl.Execute(&b, d); err != nil {
		return err
	}
	if err := os.WriteFile("rig.yaml", b.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s rig.yaml: %d services", green("✓"), len(d.Services))
	if d.Godev {
		fmt.Print(", services imported from .godev.yaml")
	}
	if len(d.Manifests) > 0 {
		fmt.Printf(", manifests in %s", strings.Join(d.Manifests, ", "))
	}
	fmt.Println("\n  next: rig env · rig status · rig up · rig")
	return nil
}

// findManifestDirs returns the top-most directories holding Kubernetes workloads, at most four levels down.
func findManifestDirs(root string) []string {
	hits := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if de.IsDir() {
			if strings.Count(rel, string(filepath.Separator)) > 3 || strings.HasPrefix(de.Name(), ".") && rel != "." && de.Name() != ".docker" || de.Name() == "node_modules" || de.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(p); ext != ".yml" && ext != ".yaml" {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err == nil && bytes.Contains(raw, []byte("kind: Deployment")) {
			hits[filepath.Dir(rel)] = true
		}
		return nil
	})
	var dirs []string
	for d := range hits {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var top []string
	for _, d := range dirs {
		if len(top) > 0 && strings.HasPrefix(d, top[len(top)-1]+string(filepath.Separator)) {
			continue
		}
		top = append(top, d)
	}
	return top
}

var initTmpl = template.Must(template.New("rig").Parse(`# rig.yaml — one file for every environment. Docs: https://github.com/MohammadmahdiAhmadi/rig
version: 1
project: {{.Project}}
default: local
{{if .Godev}}
imports:
  - godev: .godev.yaml
{{end}}{{if .Manifests}}
manifests:{{range .Manifests}}
  - {{.}}{{end}}
{{end}}
services:{{if not .Services}} {}{{end}}{{range .Services}}
  {{.Name}}:
    build: { {{if .Dockerfile}}dockerfile: {{.Dockerfile}}{{else}}go: {{.Main}}{{end}} }
    # ports: { http: 8080, metrics: 9090 }
    # health: { port: http, path: /healthz }
    # depends_on: [db]{{end}}

environments:
  local:
    description: processes on this machine
    runtime: { type: local }
  docker:
    description: containers on one docker network
    runtime: { type: docker }
  kind:
    description: a throwaway kind cluster (rig do runtime create)
    runtime:
      type: kind
      cluster: {{.Project}}
      namespace: {{.Project}}
      registry_port: 5001{{if .Manifests}}
      manifests: [{{.Inline}}]{{end}}

components: {}
  # metrics:  { type: prometheus, addr: "svc://prometheus:9090" }
  # tracing:  { type: zipkin, addr: "svc://zipkin:9411" }
  # db:       { type: sql, driver: postgres, addr: "svc://postgres:5432", user: app, password: "${DB_PASSWORD}" }
  # load:     { type: http, target: "svc://api:8080/", rate: 10, max: 500 }

dashboards: {}
`))
