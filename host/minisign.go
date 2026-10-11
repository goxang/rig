package host

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/blake2b"
)

// OfficialKey is the minisign public key goxang's artifacts are signed with.
var OfficialKey = "RWSO4FXV354ZdKG69JB39vUGx5l3XooSJoWp0c8T/bWN548qByjgecLr"

// verifyMinisign checks a minisign signature (legacy Ed or prehashed ED) of data by pubkey.
func verifyMinisign(pubkey, sig string, data []byte) error {
	pk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubkey))
	if err != nil || len(pk) != 42 || string(pk[:2]) != "Ed" {
		return errors.New("bad minisign public key")
	}
	lines := strings.Split(strings.TrimSpace(sig), "\n")
	if len(lines) < 4 {
		return errors.New("bad minisign signature")
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(s) != 74 {
		return errors.New("bad minisign signature")
	}
	if !bytes.Equal(s[2:10], pk[2:10]) {
		return errors.New("signed by a different key")
	}
	msg := data
	switch string(s[:2]) {
	case "ED":
		h := blake2b.Sum512(data)
		msg = h[:]
	case "Ed":
	default:
		return errors.New("unknown minisign algorithm")
	}
	key := ed25519.PublicKey(pk[10:])
	if !ed25519.Verify(key, msg, s[10:]) {
		return errors.New("signature does not match")
	}
	trusted, ok := strings.CutPrefix(strings.TrimSpace(lines[2]), "trusted comment: ")
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if !ok || err != nil || !ed25519.Verify(key, append(s[10:74:74], trusted...), global) {
		return errors.New("trusted comment signature does not match")
	}
	return nil
}

// publisherKey is the key artifacts of publisher must be signed with: goxang's built in, any other
// trusted on first use and pinned in ~/.config/rig/keys.json.
func publisherKey(publisher, offered string) (string, error) {
	if publisher == "goxang" {
		return OfficialKey, nil
	}
	keys := map[string]string{}
	file := filepath.Join(ConfigDir(), "keys.json")
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &keys)
	}
	if k, ok := keys[publisher]; ok {
		if offered != "" && offered != k {
			return "", fmt.Errorf("publisher %s now signs with a different key; if that is expected, remove it from %s", publisher, file)
		}
		return k, nil
	}
	if offered == "" {
		return "", nil
	}
	keys[publisher] = offered
	b, _ := json.MarshalIndent(keys, "", "  ")
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		return "", err
	}
	return offered, writeAtomic(file, b, 0o600)
}

// Keygen makes a minisign key pair: the public key as minisign writes it, the secret as a base64 key id
// and ed25519 seed (not minisign's encrypted format), for Sign.
func Keygen() (pub, secret string, err error) {
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return "", "", err
	}
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	pub = base64.StdEncoding.EncodeToString(slices.Concat([]byte("Ed"), id, pk))
	return pub, base64.StdEncoding.EncodeToString(slices.Concat(id, sk.Seed())), nil
}

// Sign makes a prehashed minisign signature file of data, named file in its trusted comment.
func Sign(secret, file string, data []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secret))
	if err != nil || len(raw) != 8+ed25519.SeedSize {
		return "", errors.New("not a Keygen secret")
	}
	id, sk := raw[:8], ed25519.NewKeyFromSeed(raw[8:])
	h := blake2b.Sum512(data)
	sig := ed25519.Sign(sk, h[:])
	trusted := fmt.Sprintf("timestamp:%d\tfile:%s\thashed", time.Now().Unix(), file)
	global := ed25519.Sign(sk, slices.Concat(sig, []byte(trusted)))
	return "untrusted comment: signature from rig secret key\n" +
		base64.StdEncoding.EncodeToString(slices.Concat([]byte("ED"), id, sig)) + "\n" +
		"trusted comment: " + trusted + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n", nil
}
