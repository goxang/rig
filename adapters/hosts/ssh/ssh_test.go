package ssh

import (
	"strings"
	"testing"
)

func TestHostArgs(t *testing.T) {
	h := Host{Addr: "10.0.0.1", User: "deploy", Port: 2222, Key: "/home/x/.ssh/id_ed25519", Jump: "bastion"}

	batch := strings.Join(h.args(false), " ")
	if !strings.Contains(batch, "BatchMode=yes") {
		t.Errorf("non-tty args missing BatchMode: %v", batch)
	}
	if strings.Contains(batch, "-t ") {
		t.Errorf("non-tty args should not request a tty: %v", batch)
	}

	tty := strings.Join(h.args(true), " ")
	if !strings.Contains(tty, "-t") {
		t.Errorf("tty args missing -t: %v", tty)
	}
	if strings.Contains(tty, "BatchMode") {
		t.Errorf("tty args should allow interactive prompts, got BatchMode: %v", tty)
	}

	for _, want := range []string{"-p 2222", "-i " + h.Key, "-J bastion", "deploy@10.0.0.1"} {
		if !strings.Contains(tty, want) {
			t.Errorf("args missing %q: %v", want, tty)
		}
	}
}

func TestHostArgsNoOverrides(t *testing.T) {
	h := Host{Addr: "10.0.0.2"}
	args := strings.Join(h.args(true), " ")
	if strings.Contains(args, "-p ") || strings.Contains(args, "-i ") || strings.Contains(args, "-J ") {
		t.Errorf("unset fields should not produce flags: %v", args)
	}
	if !strings.HasSuffix(args, "10.0.0.2") {
		t.Errorf("target should be bare address with no user: %v", args)
	}
}
