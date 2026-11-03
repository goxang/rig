package host

import (
	"reflect"
	"testing"
)

func TestListenSandboxArgs(t *testing.T) {
	s := sandboxSpec{listen: []uint16{38765}}
	if got := s.args(); !reflect.DeepEqual(got, []string{"--listen", "38765"}) {
		t.Fatalf("args: %v", got)
	}
	s.rw = []string{"/"}
	if s.unconfined() {
		t.Fatal("bind restrictions must retain the sandbox")
	}
	if got := netPort("listen://127.0.0.1:38765"); got != 38765 {
		t.Fatalf("listen port: %d", got)
	}
}
