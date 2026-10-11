package catalog

import (
	"strings"
	"testing"

	"github.com/goxang/rig/spec"
)

func TestBuild(t *testing.T) {
	p, _, err := spec.LoadData([]byte(`project: shop
services:
  api: {image: api, depends_on: [db]}
  db: {role: infra, image: postgres}
environments:
  local: {}
tasks:
  seed: [echo hi]
`), "/x/rig.yaml", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := Build(p)
	api, ok := c.Get("service", "api")
	if !ok || api.Ref() != "svc://api" || len(api.Links) != 1 || api.Links[0] != "svc://db" {
		t.Fatalf("api = %+v", api)
	}
	if api.Origin.Line != 3 || !strings.HasSuffix(api.Origin.File, "rig.yaml") {
		t.Errorf("origin %v", api.Origin)
	}
	if k := c.Kinds(); k["service"] != 2 || k["environment"] != 1 || k["task"] != 1 {
		t.Errorf("kinds %v", k)
	}
	c.Add(Resource{Kind: "terraform.stack", Name: "infra"})
	if len(c.List("")) != 5 {
		t.Errorf("list %v", c.List(""))
	}
}
