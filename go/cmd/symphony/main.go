package main

import (
	"os"

	"github.com/openai/symphony/go/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.RuntimeDependencies(), os.Stderr))
}
