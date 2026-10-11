// Command sign writes file.minisig for each file, signed with $RIG_SIGNING_KEY; the release job runs it.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goxang/rig/host"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		pub, secret, err := host.Keygen()
		if err != nil {
			fail(err)
		}
		fmt.Printf("public key (host.OfficialKey): %s\nRIG_SIGNING_KEY=%s\n", pub, secret)
		return
	}
	key := os.Getenv("RIG_SIGNING_KEY")
	if key == "" {
		fail(fmt.Errorf("RIG_SIGNING_KEY is not set"))
	}
	for _, f := range os.Args[1:] {
		data, err := os.ReadFile(f)
		if err != nil {
			fail(err)
		}
		sig, err := host.Sign(key, filepath.Base(f), data)
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(f+".minisig", []byte(sig), 0o644); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sign:", err)
	os.Exit(1)
}
