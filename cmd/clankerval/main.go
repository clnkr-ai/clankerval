package main

import (
	"os"

	"github.com/clnkr-ai/clankerval/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Run("clankerval", version, os.Args[1:], mustGetwd(), os.Stdout, os.Stderr, os.Getenv))
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
