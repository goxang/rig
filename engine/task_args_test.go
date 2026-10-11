package engine

import (
	"slices"
	"testing"

	"github.com/goxang/rig/spec"
)

func TestTaskArgValues(t *testing.T) {
	args := []spec.TaskArg{{Name: "what"}, {Name: "TAG"}, {Name: "RIG_REF"}}
	got := TaskArgValues(args, []string{" core  load ", "v3", ""})
	want := []string{"core", "load", "TAG=v3"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
