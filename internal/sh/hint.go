package sh

import (
	"regexp"
	"strings"
)

// installs says how to get each tool rig drives, for an error that says it is missing.
var installs = map[string]string{
	"docker":         "install Docker: https://docs.docker.com/get-docker/",
	"kubectl":        "install kubectl: https://kubernetes.io/docs/tasks/tools/",
	"kind":           "install kind: `go install sigs.k8s.io/kind@latest` (https://kind.sigs.k8s.io), then `kind create cluster`",
	"helm":           "install Helm: https://helm.sh/docs/intro/install/",
	"go":             "install Go: https://go.dev/dl/",
	"git":            "install Git: https://git-scm.com/downloads",
	"opencode":       "install opencode: `curl -fsSL https://opencode.ai/install | bash`, then `rig ai config`",
	"ssh":            "install an OpenSSH client",
	"psql":           "install the PostgreSQL client (psql)",
	"mysql":          "install the MySQL client",
	"sqlcmd":         "install sqlcmd: https://learn.microsoft.com/sql/tools/sqlcmd/sqlcmd-utility",
	"redis-cli":      "install redis-tools (redis-cli)",
	"python3":        "install Python 3: https://www.python.org/downloads/",
	"node":           "install Node.js: https://nodejs.org",
	"npm":            "install Node.js (npm comes with it): https://nodejs.org",
	"dlv":            "install Delve: `go install github.com/go-delve/delve/cmd/dlv@latest`",
	"crane":          "install crane: `go install github.com/google/go-containerregistry/cmd/crane@latest`",
	"xsel":           "install xsel or xclip so copies reach the clipboard",
	"terraform":      "install Terraform: https://developer.hashicorp.com/terraform/install",
	"jq":             "install jq: https://jqlang.org/download/",
	"make":           "install make (build-essential, Xcode command line tools)",
	"curl":           "install curl",
	"docker-compose": "install Docker Compose: https://docs.docker.com/compose/install/",
}

var (
	missingExec = regexp.MustCompile(`exec: "([^"]+)": executable file not found`)
	missingSh   = regexp.MustCompile(`(?:^|[\s:])([A-Za-z0-9._-]+): (?:command )?not found`)
)

// common are failures every new setup meets, with what to do about them.
var common = []struct{ match, hint string }{
	{"Cannot connect to the Docker daemon", "Docker is installed but not running: start Docker Desktop, or `sudo systemctl start docker`"},
	{"permission denied while trying to connect to the Docker daemon", "your user may not use Docker: `sudo usermod -aG docker $USER`, then log in again"},
	{"current-context is not set", "no Kubernetes cluster is set up: `kind create cluster` for a local one, or ctrl+k / `rig kubeconfig` to fetch one"},
	{"no configuration has been provided", "no Kubernetes cluster is set up: `kind create cluster` for a local one, or ctrl+k / `rig kubeconfig` to fetch one"},
	{"context was not found for specified context", "that Kubernetes context is not in your kubeconfig: `kubectl config get-contexts` lists them, ctrl+k / `rig kubeconfig` fetches one"},
	{"The connection to the server", "the Kubernetes cluster does not answer: is it running (`docker start <cluster>-control-plane` for kind) and is the VPN up?"},
	{"must be logged in to the server", "the cluster refused your credentials: they may have expired, ctrl+k / `rig kubeconfig` fetches new ones"},
	{"no rig.yaml found", "run `rig init` in the project's directory to write one"},
}

// Hint is what to do about an error a new setup typically hits (a tool not installed, Docker not
// running, no Kubernetes cluster), "" when it is none of those.
func Hint(text string) string {
	for _, re := range []*regexp.Regexp{missingExec, missingSh} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if h, ok := installs[m[1]]; ok {
				return m[1] + " is not installed: " + h
			}
		}
	}
	if m := missingExec.FindStringSubmatch(text); m != nil {
		return m[1] + " is not installed or not on PATH"
	}
	for _, c := range common {
		if strings.Contains(text, c.match) {
			return c.hint
		}
	}
	return ""
}

// WithHint is text with Hint's advice after it, when there is any.
func WithHint(text string) string {
	if h := Hint(text); h != "" && !strings.Contains(text, h) {
		return text + "\n→ " + h
	}
	return text
}
