// Package inbox keeps writes waiting for another person's approval, one JSON file each, with the
// plan of what they would do.
package inbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goxang/rig/kernel/identity"
	"github.com/goxang/rig/protocol"
)

const (
	Pending  = "pending"
	Approved = "approved"
	Rejected = "rejected"
	Failed   = "failed"
)

type Item struct {
	ID      string           `json:"id"`
	Time    time.Time        `json:"time"`
	Subject identity.Subject `json:"subject"`
	Env     string           `json:"env,omitempty"`
	Action  string           `json:"action"`
	Args    protocol.Args    `json:"args,omitempty"`
	// Why is the policy rule that asked for approval; Plan what a dry run said the action would do.
	Why    string              `json:"why,omitempty"`
	Plan   *protocol.RunResult `json:"plan,omitempty"`
	Status string              `json:"status"`
	By     string              `json:"by,omitempty"`
	Result string              `json:"result,omitempty"`
}

type Inbox struct{ dir string }

func Open(dir string) *Inbox { return &Inbox{dir: dir} }

func (b *Inbox) Add(it Item) (Item, error) {
	var r [3]byte
	_, _ = rand.Read(r[:])
	it.Time = time.Now().UTC()
	it.ID = it.Time.Format("0102-150405") + "-" + hex.EncodeToString(r[:])
	it.Status = Pending
	return it, b.Put(it)
}

func (b *Inbox) Put(it Item) error {
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(it, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(b.dir, it.ID+".json"), raw, 0o600)
}

func (b *Inbox) Get(id string) (Item, error) {
	var it Item
	if strings.ContainsAny(id, `/\`) {
		return it, fmt.Errorf("no inbox item %q", id)
	}
	raw, err := os.ReadFile(filepath.Join(b.dir, id+".json"))
	if err != nil {
		return it, fmt.Errorf("no inbox item %q", id)
	}
	return it, json.Unmarshal(raw, &it)
}

// List is every item, newest first.
func (b *Inbox) List() []Item {
	files, _ := filepath.Glob(filepath.Join(b.dir, "*.json"))
	var out []Item
	for _, f := range files {
		if it, err := b.Get(strings.TrimSuffix(filepath.Base(f), ".json")); err == nil {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}
