package scaffold

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type workload struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Image string `yaml:"image"`
					Ports []struct {
						Name          string `yaml:"name"`
						ContainerPort int    `yaml:"containerPort"`
					} `yaml:"ports"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// imageName keeps a templated image ($REGISTRY/app:$TAG) as the name rig builds and tags (app).
func imageName(img string) string {
	if !strings.Contains(img, "$") {
		return img
	}
	img = img[strings.LastIndex(img, "/")+1:]
	name, _, _ := strings.Cut(img, ":")
	return name
}

// manifests notes the top-most folders holding Kubernetes workloads (at most four levels down) and
// adds a service for each Deployment or StatefulSet nothing else brought.
func (p *Plan) manifests(root string) {
	hits := map[string]bool{}
	var found []workload
	_ = filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if de.IsDir() {
			if rel != "." && (strings.Count(rel, string(filepath.Separator)) > 3 || strings.HasPrefix(de.Name(), ".") || skipDirs[de.Name()]) {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yml" && ext != ".yaml" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(raw, []byte("apiVersion:")) {
			return nil
		}
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		for {
			var w workload
			if dec.Decode(&w) != nil {
				break
			}
			if w.Kind == "Deployment" || w.Kind == "StatefulSet" {
				hits[filepath.Dir(rel)] = true
				found = append(found, w)
			}
		}
		return nil
	})
	dirs := make([]string, 0, len(hits))
	for d := range hits {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	// folders under one top folder (deploy/infra/x, deploy/services/y) are that folder
	tops := map[string]int{}
	for _, d := range dirs {
		tops[strings.Split(d, string(filepath.Separator))[0]]++
	}
	for _, d := range dirs {
		if top := strings.Split(d, string(filepath.Separator))[0]; tops[top] > 1 && top != "." {
			d = top
		}
		if len(p.Manifests) > 0 && (d == p.Manifests[len(p.Manifests)-1] || strings.HasPrefix(d, p.Manifests[len(p.Manifests)-1]+string(filepath.Separator))) {
			continue
		}
		p.Manifests = append(p.Manifests, d)
	}
	added := 0
	for _, w := range found {
		name := cleanName(w.Metadata.Name)
		cs := w.Spec.Template.Spec.Containers
		if name == "" || len(cs) == 0 || p.service(name) != nil {
			continue
		}
		s := &Service{Name: name, Role: "app", Image: imageName(cs[0].Image), From: "manifest " + w.Kind}
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
		p.Services = append(p.Services, s)
		added++
	}
	p.buildFromMains()
	if len(p.Manifests) > 0 {
		p.note("kubernetes manifests in %s: %d workloads, %d new services", strings.Join(p.Manifests, ", "), len(found), added)
	}
}

// buildFromMains: workloads whose image a Go main package builds (api and worker both run app)
// build it themselves, and the main package is no service of its own.
func (p *Plan) buildFromMains() {
	var keep []*Service
	for _, g := range p.Services {
		users := 0
		for _, s := range p.Services {
			if s != g && s.Go == "" && s.Image == g.Image && strings.HasPrefix(s.From, "manifest") {
				s.Go, s.Dockerfile = g.Go, g.Dockerfile
				users++
			}
		}
		if g.Go == "" || users == 0 {
			keep = append(keep, g)
		}
	}
	p.Services = keep
}
