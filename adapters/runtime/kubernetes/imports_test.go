package kubernetes

import (
	"testing"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
)

type projectEnv struct {
	core.Env
	p *spec.Project
}

func (e projectEnv) Project() *spec.Project { return e.p }

// kind embeds Runtime and calls Init itself, so the default has to live there, not in New
func TestInitDeploysFromKubernetesImports(t *testing.T) {
	p := &spec.Project{Dir: "/p", Imports: []spec.Import{{"compose": "c.yml"}, {"kubernetes": "deploy"}}}
	r := &Runtime{Opt: Options{Context: "kind-x"}}
	r.SetEnv(projectEnv{p: p})
	if err := r.Init(); err != nil {
		t.Fatal(err)
	}
	if len(r.Opt.Manifests) != 1 || r.Opt.Manifests[0] != "/p/deploy" {
		t.Fatalf("manifests = %v", r.Opt.Manifests)
	}
	own := &Runtime{Opt: Options{Context: "kind-x", Manifests: []string{"k8s"}}}
	own.SetEnv(projectEnv{p: p})
	if err := own.Init(); err != nil {
		t.Fatal(err)
	}
	if len(own.Opt.Manifests) != 1 || own.Opt.Manifests[0] != "k8s" {
		t.Fatalf("the runtime's own manifests must win: %v", own.Opt.Manifests)
	}
}
