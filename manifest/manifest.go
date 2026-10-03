// Package manifest reads Kubernetes manifests from any folder layout, links the objects to each other
// (what selects, mounts, scales or routes to what), flags what is broken, and renders objects for apply.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Object struct {
	File      string
	Doc       int
	Kind      string
	Name      string
	Namespace string
	Labels    map[string]string
	Node      *yaml.Node
}

func (o *Object) ID() string { return o.Kind + "/" + o.Name }

func (o *Object) String() string {
	if o.Namespace != "" {
		return o.Namespace + "/" + o.ID()
	}
	return o.ID()
}

// Decode fills v from the object's YAML.
func (o *Object) Decode(v any) error { return o.Node.Decode(v) }

type Issue struct {
	Level  string // error, warn, info
	Object string
	File   string
	Text   string
}

type Set struct {
	Roots   []string
	Objects []*Object
	Edges   []Edge
	Issues  []Issue
	byID    map[string][]*Object
}

var workloadKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true, "Job": true, "CronJob": true, "ReplicaSet": true, "Pod": true}

func IsWorkload(kind string) bool { return workloadKinds[kind] }

// Scan reads every .yml/.yaml under roots. A directory holding a kustomization file is rendered
// with `kubectl kustomize` instead of read file by file. Files that do not parse (Helm templates,
// non-Kubernetes YAML) are reported as issues, not errors.
func Scan(roots ...string) (*Set, error) {
	s := &Set{Roots: roots, byID: map[string][]*Object{}}
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			return nil, err
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && p != root {
					return filepath.SkipDir
				}
				if k := kustomization(p); k != "" {
					out, err := exec.Command("kubectl", "kustomize", p).Output()
					if err != nil {
						s.issue("warn", "", k, "kustomize failed, reading files instead: "+err.Error())
						return nil
					}
					s.read(k, bytes.NewReader(out))
					return filepath.SkipDir
				}
				return nil
			}
			if ext := filepath.Ext(p); ext != ".yml" && ext != ".yaml" {
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			s.read(p, f)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(s.Objects, func(i, j int) bool {
		if s.Objects[i].File != s.Objects[j].File {
			return s.Objects[i].File < s.Objects[j].File
		}
		return s.Objects[i].Doc < s.Objects[j].Doc
	})
	s.link()
	s.lint()
	return s, nil
}

func kustomization(dir string) string {
	for _, n := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return filepath.Join(dir, n)
		}
	}
	return ""
}

func (s *Set) read(file string, r io.Reader) {
	dec := yaml.NewDecoder(r)
	for i := 0; ; i++ {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			s.issue("info", "", file, "not parsed: "+firstLine(err.Error()))
			return
		}
		if len(n.Content) == 0 {
			continue
		}
		s.add(file, i, n.Content[0])
	}
}

func (s *Set) add(file string, doc int, n *yaml.Node) {
	var head struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string            `yaml:"name"`
			Namespace string            `yaml:"namespace"`
			Labels    map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
		Items []yaml.Node `yaml:"items"`
	}
	if n.Kind != yaml.MappingNode || n.Decode(&head) != nil || head.Kind == "" {
		return
	}
	if head.Kind == "List" || strings.HasSuffix(head.Kind, "List") && len(head.Items) > 0 {
		for i := range head.Items {
			s.add(file, doc, &head.Items[i])
		}
		return
	}
	o := &Object{File: file, Doc: doc, Kind: head.Kind, Name: head.Metadata.Name,
		Namespace: head.Metadata.Namespace, Labels: head.Metadata.Labels, Node: n}
	s.Objects = append(s.Objects, o)
	s.byID[o.ID()] = append(s.byID[o.ID()], o)
}

// Get finds an object by kind and name.
func (s *Set) Get(kind, name string) *Object {
	if os := s.byID[kind+"/"+name]; len(os) > 0 {
		return os[0]
	}
	return nil
}

func (s *Set) issue(level, obj, file, text string) {
	s.Issues = append(s.Issues, Issue{Level: level, Object: obj, File: file, Text: text})
}

// Files lists every file that holds at least one object.
func (s *Set) Files() []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range s.Objects {
		if !seen[o.File] {
			seen[o.File] = true
			out = append(out, o.File)
		}
	}
	return out
}

// Kinds counts objects per kind.
func (s *Set) Kinds() map[string]int {
	out := map[string]int{}
	for _, o := range s.Objects {
		out[o.Kind]++
	}
	return out
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// Encode writes objects as one multi-document YAML stream.
func Encode(objs []*Object) ([]byte, error) {
	var b bytes.Buffer
	for i, o := range objs {
		if i > 0 {
			b.WriteString("---\n")
		}
		enc := yaml.NewEncoder(&b)
		enc.SetIndent(2)
		if err := enc.Encode(o.Node); err != nil {
			return nil, fmt.Errorf("%s: %w", o, err)
		}
		_ = enc.Close()
	}
	return b.Bytes(), nil
}
