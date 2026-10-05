package scaffold

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var skipDirs = map[string]bool{"node_modules": true, "vendor": true, "venv": true, ".venv": true, "env": true, "__pycache__": true,
	"dist": true, "build": true, "target": true, "bin": true, "obj": true, "testdata": true, "examples": true, "example": true, "docs": true}

// found is a runnable thing in a directory: how to run and test it, and the port it likely listens on.
type found struct {
	dir, lang  string
	run        []string
	port       int
	test       string
	dockerfile bool
}

// scan finds services by their language's marker files, at most three levels down.
func (p *Plan) scan(ctx context.Context, root string) {
	var hits []found
	_ = filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil || !de.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel != "." && (strings.HasPrefix(de.Name(), ".") || skipDirs[de.Name()] || strings.Count(rel, string(filepath.Separator)) >= 3) {
			return filepath.SkipDir
		}
		if f, ok := detectDir(path); ok {
			f.dir = rel
			hits = append(hits, f)
			if f.lang != "go" {
				return filepath.SkipDir // a project's own subfolders are not more services
			}
		}
		return nil
	})
	var modules []string
	for _, f := range hits {
		if f.lang == "go" {
			modules = append(modules, f.dir)
			continue
		}
		p.addFound(root, f)
	}
	if len(modules) == 0 && inGoModule(root) {
		modules = []string{"."}
	}
	p.goMains(ctx, root, modules)
}

func readFile(dir, name string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, name))
	return string(raw)
}

var (
	fastapiRe = regexp.MustCompile(`(\w+)\s*=\s*FastAPI\(`)
	flaskRe   = regexp.MustCompile(`(\w+)\s*=\s*Flask\(`)
)

func detectDir(dir string) (found, bool) {
	f := found{dockerfile: exists(filepath.Join(dir, "Dockerfile"))}
	py := readFile(dir, "requirements.txt") + readFile(dir, "pyproject.toml") + readFile(dir, "Pipfile")
	pytest := strings.Contains(strings.ToLower(py), "pytest")
	switch {
	case exists(filepath.Join(dir, "go.mod")):
		f.lang = "go"
	case exists(filepath.Join(dir, "manage.py")):
		f.lang, f.run, f.port = "django", []string{"python", "manage.py", "runserver", "0.0.0.0:8000"}, 8000
		f.test = "python manage.py test"
		if pytest {
			f.test = "pytest --junitxml=$RIG_JUNIT"
		}
	case py != "":
		f.lang, f.port = "python", 8000
		for _, file := range []string{"main.py", "app.py", "server.py", "src/main.py", "app/main.py"} {
			src := readFile(dir, file)
			mod := strings.TrimSuffix(strings.ReplaceAll(file, "/", "."), ".py")
			if m := fastapiRe.FindStringSubmatch(src); m != nil {
				f.run = []string{"uvicorn", mod + ":" + m[1], "--host", "0.0.0.0", "--port", "8000"}
				break
			}
			if m := flaskRe.FindStringSubmatch(src); m != nil {
				f.run, f.port = []string{"flask", "--app", mod, "run", "--host", "0.0.0.0", "--port", "5000"}, 5000
				break
			}
			if src != "" && f.run == nil {
				f.run = []string{"python", file}
			}
		}
		if pytest {
			f.test = "pytest --junitxml=$RIG_JUNIT"
		}
		if f.run == nil && !f.dockerfile {
			return f, false
		}
	case exists(filepath.Join(dir, "package.json")):
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal([]byte(readFile(dir, "package.json")), &pkg)
		f.lang, f.port = "node", 3000
		switch {
		case pkg.Scripts["start"] != "":
			f.run = []string{"npm", "start"}
		case pkg.Scripts["dev"] != "":
			f.run = []string{"npm", "run", "dev"}
		}
		if t := pkg.Scripts["test"]; t != "" && !strings.Contains(t, "no test specified") {
			f.test = "npm test"
		}
		if f.run == nil && !f.dockerfile {
			return f, false
		}
	case exists(filepath.Join(dir, "pom.xml")):
		f.lang, f.port, f.test = "java", 8080, "mvn -q test; s=$?; cat target/surefire-reports/*.xml > $RIG_JUNIT 2>/dev/null; exit $s"
		if strings.Contains(readFile(dir, "pom.xml"), "spring-boot") {
			f.run = []string{"mvn", "spring-boot:run"}
		}
	case exists(filepath.Join(dir, "build.gradle")) || exists(filepath.Join(dir, "build.gradle.kts")):
		f.lang, f.port, f.test = "java", 8080, "./gradlew test; s=$?; cat build/test-results/test/*.xml > $RIG_JUNIT 2>/dev/null; exit $s"
		if g := readFile(dir, "build.gradle") + readFile(dir, "build.gradle.kts"); strings.Contains(g, "org.springframework.boot") {
			f.run = []string{"./gradlew", "bootRun"}
		}
	case exists(filepath.Join(dir, "Cargo.toml")):
		f.lang, f.run, f.port, f.test = "rust", []string{"cargo", "run"}, 8080, "cargo test"
	case f.dockerfile:
		f.lang = "docker"
	default:
		return f, false
	}
	return f, true
}

// addFound adds a service for f, or gives the compose service built from f's directory a way to run
// as a local process.
func (p *Plan) addFound(root string, f found) {
	name := cleanName(filepath.Base(f.dir))
	if f.dir == "." {
		name = p.Project
	}
	for _, s := range p.Services {
		if s.Context != "" && filepath.Clean(s.Context) == filepath.Clean(f.dir) {
			s.Run, s.RunDir = f.run, f.dir
			p.suite(name, f)
			return
		}
	}
	if p.service(name) != nil {
		return
	}
	s := &Service{Name: name, Role: "app", Run: f.run, RunDir: f.dir, From: f.lang + " in " + f.dir}
	if f.port > 0 {
		s.Ports = []Port{{"http", f.port}}
	}
	if f.dockerfile {
		s.Context = f.dir
	}
	p.Services = append(p.Services, s)
	p.note("%s: %s service %s", f.dir, f.lang, name)
	p.suite(name, f)
}

func (p *Plan) suite(name string, f found) {
	if f.test != "" {
		p.Suites = append(p.Suites, Suite{Name: name, Dir: f.dir, Command: f.test})
	}
}

// goMains adds a service per main package of each Go module (go list), unless .godev.yaml brings them.
func (p *Plan) goMains(ctx context.Context, root string, modules []string) {
	if p.Godev || len(modules) == 0 {
		return
	}
	var mains []string
	for _, m := range modules {
		cmd := exec.CommandContext(ctx, "go", "list", "-e", "-f", `{{if eq .Name "main"}}{{.Dir}}{{end}}`, "./...")
		cmd.Dir = filepath.Join(root, m)
		out, _ := cmd.Output()
		mains = append(mains, strings.Fields(string(bytes.TrimSpace(out)))...)
	}
	n := 0
	for _, dir := range mains {
		rel, _ := filepath.Rel(root, dir)
		if strings.Contains(rel, "test") || strings.Contains(rel, "example") || strings.Contains(rel, "tools") {
			continue
		}
		name := cleanName(filepath.Base(dir))
		if rel == "." {
			name = p.Project
		}
		if p.service(name) != nil || n >= 40 {
			continue
		}
		s := &Service{Name: name, Role: "app", Image: name, Go: "./" + filepath.ToSlash(rel), From: "go main package " + rel}
		if exists(filepath.Join(dir, "Dockerfile")) {
			s.Go, s.Dockerfile = "", filepath.Join(rel, "Dockerfile")
			s.Run = []string{"go", "run", "./" + filepath.ToSlash(rel)}
		}
		p.Services = append(p.Services, s)
		n++
	}
	if n > 0 {
		p.note("go: %d main packages", n)
		for _, m := range modules {
			name := "unit"
			if len(modules) > 1 {
				name = "unit-" + cleanName(filepath.Base(m))
			}
			p.Suites = append(p.Suites, Suite{Name: name, Dir: m, Packages: []string{"./..."}})
		}
	}
}

// inGoModule says a go.mod above dir puts it in a module (an example folder of a bigger repo).
func inGoModule(dir string) bool {
	for d := filepath.Dir(dir); d != filepath.Dir(d); d = filepath.Dir(d) {
		if exists(filepath.Join(d, "go.mod")) {
			return true
		}
	}
	return false
}
