package docker

import (
	"slices"
	"testing"
)

func TestDaemonFlagsAndComposeLabels(t *testing.T) {
	r := &Runtime{project: "shop", opt: Options{Host: "ssh://me@vds"}}
	if got := r.docker("ps").Args; !slices.Equal(got, []string{"--host", "ssh://me@vds", "ps"}) {
		t.Fatalf("args = %v", got)
	}
	r.opt.Context = "vds"
	if got := r.docker("ps").Args; !slices.Equal(got, []string{"--context", "vds", "ps"}) {
		t.Fatalf("context args = %v", got)
	}
	c := container{Labels: map[string]string{"com.docker.compose.project": "momentz", "com.docker.compose.service": "web"}}
	if r.serviceOf(c) != "" {
		t.Fatal("rig mode claimed a compose container")
	}
	r.opt.Compose = &Compose{Project: "momentz"}
	if got := r.serviceOf(c); got != "web" {
		t.Fatalf("compose service = %q", got)
	}
}
