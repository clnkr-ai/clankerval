package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/clnkr-ai/clankerval/internal/release"
)

func main() {
	var repoRoot string
	var releaseTag string

	flag.StringVar(&repoRoot, "repo-root", ".", "repository root")
	flag.StringVar(&releaseTag, "release-tag", "", "release tag like v0.1.1")
	flag.Parse()

	if releaseTag == "" {
		fmt.Fprintln(os.Stderr, "release-tag is required")
		os.Exit(2)
	}

	if err := release.RunPrepareDebianChangelog(context.Background(), repoRoot, releaseTag, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
