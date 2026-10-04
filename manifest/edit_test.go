package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoDocs = `# head comment
apiVersion: v1
kind: Service
metadata:
  name: web
spec:
  ports:
    - port: 80
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: web
          image: ${REGISTRY}/web:${TAG}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: web
data:
  a: "1"
`

func TestReplaceDoc(t *testing.T) {
	f := filepath.Join(t.TempDir(), "web.yml")
	if err := os.WriteFile(f, []byte(twoDocs), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Scan(f)
	if err != nil {
		t.Fatal(err)
	}
	d := set.Get("Deployment", "web")
	edit := strings.Replace(twoDocs[strings.Index(twoDocs, "apiVersion: apps/v1"):strings.Index(twoDocs, "---\napiVersion: v1\nkind: ConfigMap")], "replicas: 1", "replicas: 3", 1)
	if err := ReplaceDoc(d, []byte(edit)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(f)
	got := string(raw)
	if !strings.Contains(got, "replicas: 3") || !strings.HasPrefix(got, "# head comment") || !strings.Contains(got, "kind: ConfigMap") {
		t.Fatalf("file after edit:\n%s", got)
	}
	if strings.Count(got, "---") != 2 {
		t.Fatalf("separators changed:\n%s", got)
	}
	if err := ReplaceDoc(d, []byte("kind: Deployment\nmetadata: {name: other}\n")); err == nil {
		t.Fatal("a renamed object was accepted")
	}
}

func TestFromLive(t *testing.T) {
	f := filepath.Join(t.TempDir(), "web.yml")
	_ = os.WriteFile(f, []byte(twoDocs), 0o644)
	set, _ := Scan(f)
	live := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"ns","uid":"u","resourceVersion":"9",
"labels":{"app.kubernetes.io/managed-by":"rig"},
"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}"},
"managedFields":[{"manager":"kubectl","operation":"Apply","fieldsV1":{"f:metadata":{"f:labels":{"f:app.kubernetes.io/managed-by":{}},"f:annotations":{".":{},"f:kubectl.kubernetes.io/last-applied-configuration":{}}},
"f:spec":{"f:replicas":{},"f:template":{"f:spec":{"f:containers":{"k:{\"name\":\"web\"}":{".":{},"f:name":{},"f:image":{},"f:env":{"k:{\"name\":\"INJECTED\"}":{".":{},"f:name":{},"f:value":{}}},"f:resources":{"f:limits":{"f:cpu":{}}}}}}}}}},
{"manager":"kube-controller-manager","subresource":"status","fieldsV1":{"f:status":{"f:replicas":{}}}}]},
"spec":{"replicas":4,"progressDeadlineSeconds":600,"template":{"spec":{"dnsPolicy":"ClusterFirst","containers":[{"name":"web","image":"reg/web:abc","imagePullPolicy":"Always","env":[{"name":"INJECTED","value":"x"}],"resources":{"limits":{"cpu":"2"}}}]}}},
"status":{"replicas":4}}`
	out, err := FromLive(set.Get("Deployment", "web"), []byte(live), map[string]bool{"INJECTED": true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"replicas: 4", "image: ${REGISTRY}/web:${TAG}", "cpu: \"2\""} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, bad := range []string{"status", "uid", "managedFields", "dnsPolicy", "imagePullPolicy", "progressDeadline", "last-applied", "INJECTED", "managed-by", "namespace"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q should be gone:\n%s", bad, got)
		}
	}
	if !strings.HasPrefix(got, "apiVersion: apps/v1\nkind: Deployment\nmetadata:") {
		t.Errorf("key order:\n%s", got)
	}
}
