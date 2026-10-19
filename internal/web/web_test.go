package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutes(t *testing.T) {
	srv := httptest.NewServer((&Handler{Token: "secret-token"}).Routes())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/")
	if err != nil || res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("index: %v %v", err, res)
	}
	for _, auth := range []string{"", "Bearer nope"} {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/engine/catalog", nil)
		req.Header.Set("Authorization", auth)
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("auth %q: %v %v", auth, err, res)
		}
	}
}
