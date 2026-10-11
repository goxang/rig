package host

import (
	"strings"
	"testing"
)

func TestMissingNeeds(t *testing.T) {
	m := &Manifest{Needs: map[string]string{"rig-no-such-tool": "apt install nosuch", "sh": ""}}
	got := m.MissingText(true)
	if !strings.Contains(got, "rig-no-such-tool (apt install nosuch)") || strings.Contains(got, "sh ") {
		t.Fatalf("missing text: %q", got)
	}
	if (&Manifest{}).MissingText(false) != "" {
		t.Fatal("nothing needed, nothing missing")
	}
}
