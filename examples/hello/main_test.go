package main

import "testing"

func TestGreet(t *testing.T) {
	if got := greet("rig"); got != "hello, rig" {
		t.Fatalf("greet(rig) = %q", got)
	}
}
