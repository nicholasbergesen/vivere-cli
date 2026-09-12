// Command vivere is the standalone wrapper binary users download: it contains
// only `run` (and `version`), not the server, so it stays small.
package main

import (
	"fmt"
	"os"

	"github.com/nicholasbergesen/vivere-cli/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println(`vivere - report a command's run to a Vivere heartbeat monitor

Usage:
  vivere run [--url URL] [--timeout 10m] [--no-start] [--quiet] -- command [args...]
  vivere version

The ping URL can also come from the VIVERE_PING_URL environment variable.
Docs: https://vivere.dev/docs/cli`)
		if len(args) == 0 {
			os.Exit(2)
		}
		return
	}
	switch args[0] {
	case "run":
		os.Exit(cli.Run(args[1:], version))
	case "version", "-v", "--version":
		fmt.Println("vivere", version)
	default:
		// Allow `vivere -- cmd` and `vivere --url ... -- cmd` as shorthand for run.
		os.Exit(cli.Run(args, version))
	}
}
