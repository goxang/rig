// Package kubectx picks the kubeconfig context an environment means.
package kubectx

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type kubeconfig struct {
	Contexts []struct {
		Name    string `json:"name"`
		Context struct {
			Cluster string `json:"cluster"`
		} `json:"context"`
	} `json:"contexts"`
	Clusters []struct {
		Name    string `json:"name"`
		Cluster struct {
			Server string `json:"server"`
		} `json:"cluster"`
	} `json:"clusters"`
}

// Resolve is the context to use: name when the kubeconfig has it, else one whose cluster has
// server, since kubeconfigs downloaded from Rancher and the like name one cluster differently on
// each machine. Without kubectl it returns name and leaves the error to later.
func Resolve(name, server string) (string, error) {
	raw, err := exec.Command("kubectl", "config", "view", "-o", "json").Output()
	if err != nil {
		if name == "" {
			return "", fmt.Errorf("no kubectl to find the context of %s: %w", server, err)
		}
		return name, nil
	}
	var kc kubeconfig
	if err := json.Unmarshal(raw, &kc); err != nil {
		return name, nil
	}
	return pick(kc, name, server)
}

func pick(kc kubeconfig, name, server string) (string, error) {
	servers := map[string]string{}
	for _, c := range kc.Clusters {
		servers[c.Name] = c.Cluster.Server
	}
	var have, matching []string
	for _, c := range kc.Contexts {
		if c.Name == name && name != "" {
			return name, nil
		}
		have = append(have, c.Name)
		if server != "" && sameServer(servers[c.Context.Cluster], server) {
			matching = append(matching, c.Name)
		}
	}
	if len(matching) > 0 {
		return matching[0], nil
	}
	want := fmt.Sprintf("kubectl context %q", name)
	if server != "" {
		want = fmt.Sprintf("a kubectl context for %s", server)
		if name != "" {
			want += fmt.Sprintf(" (or named %q)", name)
		}
	}
	return "", fmt.Errorf("%s is not in this machine's kubeconfig (it has: %s); fetch the cluster's kubeconfig (rig kubeconfig), or point the environment's runtime.context (or runtime.server) in rig.yaml at the right one",
		want, strings.Join(have, ", "))
}

func sameServer(a, b string) bool {
	return a != "" && strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}
