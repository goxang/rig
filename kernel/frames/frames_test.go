package frames

import (
	"testing"

	"github.com/goxang/rig/protocol"
)

func TestRowsRoundTrip(t *testing.T) {
	fields := []protocol.Field{{Name: "service", Type: protocol.TypeString, Ref: "svc://{service}"}, {Name: "p95", Type: protocol.TypeNumber, Unit: "ms"}}
	f := FromRows(fields, []map[string]any{{"service": "api", "p95": 12.5}, {"service": "db"}})
	if Len(f) != 2 || Rows(f)[1]["service"] != "db" || Column(f, "p95")[0] != 12.5 {
		t.Fatalf("got %+v", f)
	}
	if RowRef(f, 0) != "svc://api" {
		t.Errorf("ref %q", RowRef(f, 0))
	}
	if got := Format(fields[1], 12.5); got != "12.5 ms" {
		t.Errorf("format %q", got)
	}
	if got := Format(protocol.Field{Type: protocol.TypeBytes}, 1536.0); got != "1.5 KiB" {
		t.Errorf("bytes %q", got)
	}
	if got := Format(protocol.Field{Type: protocol.TypeDuration}, 1.5); got != "1.5s" {
		t.Errorf("duration %q", got)
	}
}
