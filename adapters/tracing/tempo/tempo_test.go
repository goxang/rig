package tempo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/goxang/rig/core"
)

func TestSpansFromTempoJSON(t *testing.T) {
	raw := `{"batches":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},
	"scopeSpans":[{"spans":[{"traceId":"AAAAAAAAAAAAAAAAAAAAAQ==","spanId":"AAAAAAAAAAI=","name":"GET /",
	"startTimeUnixNano":"1000","endTimeUnixNano":"3000","status":{"code":2},
	"attributes":[{"key":"http.status_code","value":{"intValue":"500"}}]}]}]}]}`
	var r struct {
		Batches []resourceSpans `json:"batches"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	s := spansOf(r.Batches)[0]
	if s.Service != "api" || s.ID != "0000000000000002" || s.TraceID != "00000000000000000000000000000001" ||
		s.Duration != 2*time.Microsecond || !s.Error || s.Tags["http.status_code"] != "500" {
		t.Fatalf("%+v", s)
	}
}

func TestTraceQL(t *testing.T) {
	got := TraceQL(core.TraceQuery{Service: "api", MinDuration: 100 * time.Millisecond})
	if got != `{ resource.service.name = "api" && duration >= 100ms }` {
		t.Fatal(got)
	}
}
