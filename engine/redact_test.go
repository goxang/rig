package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goxang/rig/ai"
	"github.com/goxang/rig/spec"
)

// The shop's postgres password, in a log line on its way to a model, never reaches it.
func TestSecretsNeverReachTheModel(t *testing.T) {
	p, e, err := spec.Load("../examples/shop/rig.yaml", "docker")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Spec: p, Env: e}
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, string(body))
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	r := &ai.Runner{Setup: ai.Setup{Config: ai.Config{FastURL: srv.URL}, Bin: "fake"}, Redactor: a.Redactor()}
	logs := `api  {"level":"error","msg":"connect","url":"postgres://postgres:shop@postgres:5432/shop"}
api  pgx: password=shop user=postgres host=postgres
api  POSTGRES_PASSWORD=shop
api  partner said 401 to Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJlLWhlcmU`
	if _, err := r.Complete(context.Background(), "the logs screen:\n"+logs, "why does api fail"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("backend got %d requests", len(got))
	}
	for _, leak := range []string{":shop@", "password=shop", "PASSWORD=shop", "eyJhbGciOiJIUzI1NiJ9"} {
		if strings.Contains(got[0], leak) {
			t.Errorf("%q reached the model:\n%s", leak, got[0])
		}
	}
}
