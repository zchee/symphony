package main

import (
	"os"

	"github.com/openai/symphony/go/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.RuntimeDependencies(), os.Stderr))
}
