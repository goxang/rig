package loadgen

import "testing"

func TestRateSetGet(t *testing.T) {
	var r Rate
	if got := r.Get(); got != 0 {
		t.Fatalf("zero value Get() = %v, want 0", got)
	}
	r.Set(42.5)
	if got := r.Get(); got != 42.5 {
		t.Fatalf("Get() = %v, want 42.5", got)
	}
}
