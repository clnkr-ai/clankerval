package main

import (
	"os"
	"path/filepath"

	"github.com/clnkr-ai/clankerval/internal/cli"
)

var version = "dev"

func main() {
	name := "clankerval"
	if filepath.Base(os.Args[0]) == "clnkeval" {
		name = "clnkeval"
	}
	os.Exit(cli.Run(name, version, os.Args[1:], mustGetwd(), os.Stdout, os.Stderr, os.Getenv))
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
