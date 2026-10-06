package scaffold

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Candidate is something rig init --deep found that could go into rig.yaml: a service to run, a
// test suite, or a folder of manifests.
type Candidate struct {
	Kind      string // go, python, node, rust, java, dotnet, ruby, dockerfile, compose, manifest, helm, goland, container, tests
	Name      string
	Where     string
	Service   *Service
	Suite     *Suite
	Manifests string
}

// deepSkip are folders no service lives in: dependencies, build output, caches, VCS.
var deepSkip = map[string]bool{"node_modules": true, "vendor": true, "venv": true, ".venv": true, "__pycache__": true,
	"dist": true, "target": true, "obj": true, ".git": true, ".hg": true, ".svn": true, ".idea": true, ".vscode": true,
	".cache": true, ".next": true, ".nuxt": true, "coverage": true, ".gradle": true, ".terraform": true, ".rig": true}

var (
	goMainRe   = regexp.MustCompile(`(?m)^package main\b`)
	goFuncMain = regexp.MustCompile(`(?m)^func main\(\)`)
	pyMainRe   = regexp.MustCompile(`if __name__ == ['"]__main__['"]`)
	composeRe  = regexp.MustCompile(`^(docker-)?compose(\.[\w-]+)?\.ya?ml$`)
)

// Deep walks every folder under root and lists what could run there, without building anything:
// Go main packages and test packages, Python/Node/Rust/Java/.NET/Ruby entry points, Dockerfiles,
// compose services, Kubernetes workloads, Helm charts, GoLand run configurations, and the
// containers running on this machine. Names are unique; the first find of a name wins.
func Deep(ctx context.Context, root string) ([]Candidate, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var (
		mu    sync.Mutex
		out   []Candidate
		goDir = map[string]*goPkg{}
		files = make(chan string, 256)
		wg    sync.WaitGroup
	)
	add := func(c Candidate) {
		mu.Lock()
		out = append(out, c)
		mu.Unlock()
	}
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range files {
				deepFile(root, path, add, &mu, goDir)
			}
		}()
	}
	modules := []string{}
	_ = filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if de.IsDir() {
			if path != root && (deepSkip[de.Name()] || strings.HasPrefix(de.Name(), ".") && de.Name() != ".run") {
				return filepath.SkipDir
			}
			return nil
		}
		if de.Name() == "go.mod" {
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			modules = append(modules, rel)
		}
		files <- path
		return nil
	})
	close(files)
	wg.Wait()

	dirs := make([]string, 0, len(goDir))
	for d := range goDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		g := goDir[d]
		rel, _ := filepath.Rel(root, d)
		if g.main {
			s := &Service{Name: cleanName(filepath.Base(d)), Role: "app", Image: cleanName(filepath.Base(d)), Go: "./" + filepath.ToSlash(rel), From: "go main package " + rel}
			if rel == "." {
				s.Name, s.Image = cleanName(filepath.Base(root)), cleanName(filepath.Base(root))
			}
			if exists(filepath.Join(d, "Dockerfile")) {
				s.Go, s.Dockerfile, s.Run = "", filepath.Join(rel, "Dockerfile"), []string{"go", "run", "./" + filepath.ToSlash(rel)}
			}
			out = append(out, Candidate{Kind: "go", Name: s.Name, Where: rel, Service: s})
		}
	}
	for _, m := range modules {
		name := "unit"
		if m != "." {
			name = "unit-" + cleanName(filepath.Base(m))
		}
		out = append(out, Candidate{Kind: "tests", Name: name, Where: m + " (go test ./...)", Suite: &Suite{Name: name, Dir: m, Packages: []string{"./..."}}})
	}
	for _, d := range dirs {
		if !goDir[d].tests {
			continue
		}
		rel, _ := filepath.Rel(root, d)
		if l := strings.ToLower(rel); strings.Contains(l, "integration") || strings.Contains(l, "e2e") {
			name := "tests-" + cleanName(strings.ReplaceAll(rel, string(filepath.Separator), "-"))
			out = append(out, Candidate{Kind: "tests", Name: name, Where: rel, Suite: &Suite{Name: name, Dir: ".", Packages: []string{"./" + filepath.ToSlash(rel)}}})
		}
	}
	out = append(out, goland(root)...)
	out = append(out, containers(ctx)...)
	return unique(out), nil
}

type goPkg struct{ main, tests bool }

// deepFile looks at one file; only the head of a source file is read.
func deepFile(root, path string, add func(Candidate), mu *sync.Mutex, goDir map[string]*goPkg) {
	name := filepath.Base(path)
	dir := filepath.Dir(path)
	rel, _ := filepath.Rel(root, dir)
	switch {
	case strings.HasSuffix(name, "_test.go"):
		mu.Lock()
		pkg(goDir, dir).tests = true
		mu.Unlock()
	case strings.HasSuffix(name, ".go"):
		head := readHead(path, 16<<10)
		if goMainRe.Match(head) && (goFuncMain.Match(head) || goFuncMain.Match(readHead(path, 1<<20))) {
			mu.Lock()
			pkg(goDir, dir).main = true
			mu.Unlock()
		}
	case strings.HasSuffix(name, ".py"):
		head := readHead(path, 64<<10)
		if pyMainRe.Match(head) || fastapiRe.Match(head) || flaskRe.Match(head) {
			f, ok := detectDir(dir)
			if !ok || f.run == nil {
				f = found{lang: "python", run: []string{"python", name}, port: 8000}
			}
			f.dir = rel
			s := foundService(root, f, strings.TrimSuffix(name, ".py"))
			add(Candidate{Kind: "python", Name: s.Name, Where: filepath.Join(rel, name), Service: s})
		}
	case name == "package.json" || name == "Cargo.toml" || name == "pom.xml" || name == "build.gradle" || name == "build.gradle.kts" || name == "Gemfile":
		if f, ok := detectDir(dir); ok && f.run != nil {
			f.dir = rel
			s := foundService(root, f, "")
			add(Candidate{Kind: f.lang, Name: s.Name, Where: rel, Service: s})
		}
	case strings.HasSuffix(name, ".csproj"):
		if bytes.Contains(readHead(path, 32<<10), []byte("<OutputType>Exe</OutputType>")) || bytes.Contains(readHead(path, 32<<10), []byte(`Sdk="Microsoft.NET.Sdk.Web"`)) {
			s := &Service{Name: cleanName(strings.TrimSuffix(name, ".csproj")), Role: "app", Run: []string{"dotnet", "run"}, RunDir: rel, From: "dotnet " + rel}
			add(Candidate{Kind: "dotnet", Name: s.Name, Where: filepath.Join(rel, name), Service: s})
		}
	case name == "Dockerfile" || strings.HasPrefix(name, "Dockerfile.") || strings.HasSuffix(name, ".Dockerfile"):
		base := filepath.Base(dir)
		if rel == "." {
			base = filepath.Base(root)
		}
		if suffix := strings.TrimPrefix(strings.TrimSuffix(name, ".Dockerfile"), "Dockerfile."); suffix != name && suffix != "Dockerfile" {
			base = suffix
		}
		s := &Service{Name: cleanName(base), Role: "app", Image: cleanName(base), Context: rel, Dockerfile: filepath.Join(rel, name), From: "Dockerfile " + filepath.Join(rel, name)}
		add(Candidate{Kind: "dockerfile", Name: s.Name, Where: filepath.Join(rel, name), Service: s})
	case composeRe.MatchString(name):
		raw, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var doc struct {
			Services map[string]composeService `yaml:"services"`
		}
		if yaml.Unmarshal(raw, &doc) != nil {
			return
		}
		file := filepath.Join(rel, name)
		for n, c := range doc.Services {
			s := fromCompose(cleanName(n), c, file)
			if s.Context != "" && rel != "." {
				s.Context = filepath.Join(rel, s.Context)
			}
			add(Candidate{Kind: "compose", Name: s.Name, Where: file, Service: s})
		}
	case name == "Chart.yaml":
		add(Candidate{Kind: "helm", Name: cleanName(filepath.Base(dir)), Where: rel, Manifests: rel})
	case strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml"):
		raw := readHead(path, 1<<20)
		if !bytes.Contains(raw, []byte("apiVersion:")) || !(bytes.Contains(raw, []byte("kind: Deployment")) || bytes.Contains(raw, []byte("kind: StatefulSet"))) {
			return
		}
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		for {
			var w workload
			if dec.Decode(&w) != nil {
				break
			}
			cs := w.Spec.Template.Spec.Containers
			if (w.Kind != "Deployment" && w.Kind != "StatefulSet") || w.Metadata.Name == "" || len(cs) == 0 {
				continue
			}
			s := &Service{Name: cleanName(w.Metadata.Name), Role: "app", Image: imageName(cs[0].Image), From: "manifest " + w.Kind}
			if kindOf(cs[0].Image) != "" {
				s.Role = "infra"
			}
			for i, pt := range cs[0].Ports {
				n := pt.Name
				if n == "" {
					n = portName(pt.ContainerPort, i)
				}
				s.Ports = append(s.Ports, Port{n, pt.ContainerPort})
			}
			add(Candidate{Kind: "manifest", Name: s.Name, Where: filepath.Join(rel, name), Service: s, Manifests: rel})
		}
	}
}

func pkg(m map[string]*goPkg, dir string) *goPkg {
	if m[dir] == nil {
		m[dir] = &goPkg{}
	}
	return m[dir]
}

func readHead(path string, n int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	k, _ := io.ReadFull(f, buf)
	return buf[:k]
}

func foundService(root string, f found, name string) *Service {
	if name == "" || name == "main" || name == "app" || name == "server" {
		name = filepath.Base(f.dir)
		if f.dir == "." {
			name = filepath.Base(root)
		}
	}
	s := &Service{Name: cleanName(name), Role: "app", Run: f.run, RunDir: f.dir, From: f.lang + " in " + f.dir}
	if f.port > 0 {
		s.Ports = []Port{{"http", f.port}}
	}
	if f.dockerfile {
		s.Context = f.dir
	}
	return s
}

// goland reads the project's GoLand run configurations (.idea/runConfigurations, .run): the Go
// programs someone already runs from the IDE.
func goland(root string) []Candidate {
	var files []string
	for _, g := range []string{".idea/runConfigurations/*.xml", ".run/*.run.xml"} {
		m, _ := filepath.Glob(filepath.Join(root, g))
		files = append(files, m...)
	}
	var out []Candidate
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc struct {
			Config []struct {
				Name    string `xml:"name,attr"`
				Type    string `xml:"type,attr"`
				Options []struct {
					Name  string `xml:"name,attr"`
					Value string `xml:"value,attr"`
				} `xml:"option"`
				Kind struct {
					Value string `xml:"value,attr"`
				} `xml:"kind"`
				Package struct {
					Value string `xml:"value,attr"`
				} `xml:"package"`
				Directory struct {
					Value string `xml:"value,attr"`
				} `xml:"directory"`
			} `xml:"configuration"`
		}
		if xml.Unmarshal(raw, &doc) != nil {
			continue
		}
		for _, c := range doc.Config {
			if c.Type != "GoApplicationRunConfiguration" {
				continue
			}
			target := c.Package.Value
			if d := strings.ReplaceAll(c.Directory.Value, "$PROJECT_DIR$", "."); c.Kind.Value == "DIRECTORY" && d != "" {
				target = "./" + strings.TrimPrefix(filepath.ToSlash(filepath.Clean(d)), "./")
			}
			if target == "" {
				continue
			}
			s := &Service{Name: cleanName(c.Name), Role: "app", Run: []string{"go", "run", target}, From: "GoLand run configuration " + c.Name}
			out = append(out, Candidate{Kind: "goland", Name: s.Name, Where: target, Service: s})
		}
	}
	return out
}

// containers are the ones running on this machine; picking one adopts it, rig starts and stops it.
func containers(ctx context.Context) []Candidate {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "ps", "--format", "{{json .}}").Output()
	if err != nil {
		return nil
	}
	var out []Candidate
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var c struct{ Names, Image, Ports, Labels string }
		if json.Unmarshal([]byte(line), &c) != nil || c.Names == "" {
			continue
		}
		name := c.Names
		if m := composeLabelRe.FindStringSubmatch(c.Labels); m != nil {
			name = m[1]
		}
		s := &Service{Name: cleanName(name), Role: "infra", Image: c.Image, Adopt: c.Names, From: "running container " + c.Names}
		if kindOf(c.Image) == "" {
			s.Role = "app"
		}
		for i, p := range publishedPorts(c.Ports) {
			s.Ports = append(s.Ports, Port{portName(p, i), p})
		}
		out = append(out, Candidate{Kind: "container", Name: s.Name, Where: c.Names + " (" + c.Image + ")", Service: s})
	}
	return out
}

var (
	publishedRe    = regexp.MustCompile(`->(\d+)/tcp`)
	composeLabelRe = regexp.MustCompile(`(?:^|,)com\.docker\.compose\.service=([^,]+)`)
)

func publishedPorts(s string) []int {
	var out []int
	seen := map[string]bool{}
	for _, m := range publishedRe.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			n, _ := strconv.Atoi(m[1])
			out = append(out, n)
		}
	}
	return out
}

// unique keeps the first candidate of each kind of thing per name: a service name, a suite name.
func unique(cs []Candidate) []Candidate {
	order := map[string]int{"compose": 0, "go": 1, "goland": 2, "python": 3, "node": 3, "rust": 3, "java": 3, "dotnet": 3, "ruby": 3, "dockerfile": 4, "manifest": 5, "helm": 6, "container": 7, "tests": 8}
	sort.SliceStable(cs, func(i, j int) bool {
		if order[cs[i].Kind] != order[cs[j].Kind] {
			return order[cs[i].Kind] < order[cs[j].Kind]
		}
		if cs[i].Where != cs[j].Where {
			return cs[i].Where < cs[j].Where
		}
		return cs[i].Name < cs[j].Name
	})
	seen := map[string]bool{}
	var out []Candidate
	for _, c := range cs {
		key := "svc:" + c.Name
		if c.Suite != nil {
			key = "suite:" + c.Name
		} else if c.Service == nil {
			key = "dir:" + c.Manifests
		}
		if c.Name == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// PlanOf turns picked candidates into a plan: their services, suites and manifest folders.
func PlanOf(root string, picked []Candidate) *Plan {
	abs, _ := filepath.Abs(root)
	p := &Plan{Project: cleanName(filepath.Base(abs))}
	for _, c := range picked {
		switch {
		case c.Service != nil:
			p.Services = append(p.Services, c.Service)
			if c.Manifests != "" && !contains(p.Manifests, c.Manifests) {
				p.Manifests = append(p.Manifests, c.Manifests)
			}
		case c.Suite != nil:
			p.Suites = append(p.Suites, *c.Suite)
		case c.Manifests != "" && !contains(p.Manifests, c.Manifests):
			p.Manifests = append(p.Manifests, c.Manifests)
		}
	}
	p.infraComponents()
	return p
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
