package kubectx

import (
	"encoding/json"
	"testing"
)

func TestPick(t *testing.T) {
	var kc kubeconfig
	_ = json.Unmarshal([]byte(`{
		"clusters": [{"name": "rancher", "cluster": {"server": "https://rancher.test/k8s/clusters/local"}},
		             {"name": "kind-x", "cluster": {"server": "https://127.0.0.1:1234"}}],
		"contexts": [{"name": "kind-x", "context": {"cluster": "kind-x"}},
		             {"name": "loadtest2", "context": {"cluster": "rancher"}}]}`), &kc)
	const srv = "https://rancher.test/k8s/clusters/local/"
	for _, c := range []struct{ name, server, want string }{
		{"local", srv, "loadtest2"},
		{"loadtest2", srv, "loadtest2"},
		{"kind-x", srv, "kind-x"},
		{"kind-x", "", "kind-x"},
		{"", srv, "loadtest2"},
	} {
		got, err := pick(kc, c.name, c.server)
		if err != nil || got != c.want {
			t.Errorf("pick(%q, %q) = %q, %v; want %q", c.name, c.server, got, err, c.want)
		}
	}
	if _, err := pick(kc, "local", ""); err == nil {
		t.Error("a missing context without a server must fail")
	}
	if _, err := pick(kc, "local", "https://other"); err == nil {
		t.Error("an unknown server must fail")
	}
}
