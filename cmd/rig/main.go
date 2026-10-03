package main

import (
	"os"

	_ "github.com/MohammadmahdiAhmadi/rig/adapters/all"
	"github.com/MohammadmahdiAhmadi/rig/internal/cli"
	"github.com/MohammadmahdiAhmadi/rig/internal/tui"
)

var version = "dev"

func main() {
	cli.Version = version
	cli.UI = tui.Run
	os.Exit(cli.Execute())
}
