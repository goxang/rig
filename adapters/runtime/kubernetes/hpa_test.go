package kubernetes

import (
	"strings"
	"testing"

	"github.com/goxang/rig/core"
)

func TestHPAManifest(t *testing.T) {
	y, err := hpaManifest("deployment/parser", core.Bounds{Min: 1, Max: 4, Memory: 65})
	if err != nil {
		t.Fatal(err)
	}
	got := string(y)
	for _, want := range []string{`"kind":"Deployment"`, `"name":"parser"`, `"maxReplicas":4`, `"name":"memory"`, `"averageUtilization":65`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, `"name":"cpu"`) {
		t.Errorf("cpu target without asking: %s", got)
	}
	if _, err := hpaManifest("daemonset/x", core.Bounds{Min: 1, Max: 2}); err == nil {
		t.Error("a daemonset was given an autoscaler")
	}
}
