package main

import (
	"os"

	_ "github.com/goxang/rig/adapters/all"
	"github.com/goxang/rig/internal/cli"
	"github.com/goxang/rig/internal/tui"
)

var version = "dev"

func main() {
	cli.Version = version
	cli.UI = tui.Run
	os.Exit(cli.Execute())
}
