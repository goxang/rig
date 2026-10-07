package sh

import (
	"strings"
	"testing"
)

func TestHint(t *testing.T) {
	cases := map[string]string{
		`kubectl get pods: exec: "kubectl": executable file not found in $PATH`: "install kubectl",
		"sh: 1: opencode: not found":                                         "install opencode",
		"bash: kind: command not found":                                      "install kind",
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock": "not running",
		"error: current-context is not set":                                  "kind create cluster",
		`exec: "frobnicate": executable file not found in $PATH`:             "frobnicate is not installed",
	}
	for in, want := range cases {
		if got := Hint(in); !strings.Contains(got, want) {
			t.Errorf("Hint(%q) = %q, want it to say %q", in, got, want)
		}
	}
	if got := Hint("deploy api: timed out"); got != "" {
		t.Errorf("an unrelated error got %q", got)
	}
}
