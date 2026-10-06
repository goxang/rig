// Package loadgen holds pieces shared by the load generator adapters (command, http, kv).
package loadgen

import (
	"math"
	"sync/atomic"
)

// Rate is a target requests/s that SetRate may change while the generator keeps running,
// with no restart needed: callers just re-read it (e.g. on their next tick or Status call).
type Rate struct{ bits atomic.Uint64 }

func (r *Rate) Set(rps float64) { r.bits.Store(math.Float64bits(rps)) }
func (r *Rate) Get() float64    { return math.Float64frombits(r.bits.Load()) }
