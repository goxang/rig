package engine

import (
	"slices"
	"testing"

	"github.com/goxang/rig/spec"
)

func TestPresetTaskArgs(t *testing.T) {
	args := []spec.TaskArg{{Name: "repo", Default: "r"}, {Name: "keep", Default: "2"}, {Name: "DRY_RUN"}}
	for _, c := range []struct {
		in, want []string
		named    bool
	}{
		{[]string{"keep=5"}, []string{"r", "5"}, true},
		{[]string{"DRY_RUN=1", "extra"}, []string{"r", "2", "DRY_RUN=1", "extra"}, true},
		{[]string{"a", "b"}, []string{"a", "b"}, false},
		{[]string{"OTHER=1"}, []string{"OTHER=1"}, false},
	} {
		got, named := PresetTaskArgs(args, c.in)
		if named != c.named || !slices.Equal(got, c.want) {
			t.Errorf("%v: got %v %v, want %v %v", c.in, got, named, c.want, c.named)
		}
	}
}
