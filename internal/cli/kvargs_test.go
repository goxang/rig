package cli

import (
	"fmt"
	"testing"

	"github.com/goxang/rig/protocol"
)

func TestKVArgsKeepsStringsForStringArgs(t *testing.T) {
	schema := protocol.Schema{"properties": map[string]any{"sprint": map[string]any{"type": "string"}, "to": map[string]any{"type": "array"}}}
	got := kvArgs([]string{"sprint=44", "limit=5", "on=true", "to=a@x, b@x"}, schema)
	if got["sprint"] != "44" || got["limit"] != float64(5) || got["on"] != true || fmt.Sprint(got["to"]) != "[a@x b@x]" {
		t.Errorf("%#v", got)
	}
}
