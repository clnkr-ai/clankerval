package release

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type commandRunner func(ctx context.Context, dir string, name string, args ...string) (string, error)

func PrepareDebianChangelog(ctx context.Context, repoRoot, releaseTag string, log io.Writer, run commandRunner) error {
	version, err := releaseVersion(releaseTag)
	if err != nil {
		return err
	}
	targetVersion := version + "-1"

	currentVersion, err := topChangelogVersion(filepath.Join(repoRoot, "debian", "changelog"))
	if err != nil {
		return err
	}
	if currentVersion == targetVersion {
		if _, err := fmt.Fprintf(log, "reusing existing debian/changelog entry for %s\n", targetVersion); err != nil {
			return fmt.Errorf("write reuse log: %w", err)
		}
		return nil
	}

	prevTag, err := previousReleaseTag(ctx, repoRoot, releaseTag, run)
	if err != nil {
		return err
	}

	args := []string{"dch", "--new-version=" + targetVersion, "--distribution=unstable"}
	if prevTag != "" {
		if _, err := fmt.Fprintf(log, "generating debian/changelog entry for %s from %s\n", targetVersion, prevTag); err != nil {
			return fmt.Errorf("write generation log: %w", err)
		}
		args = append(args, "--since="+prevTag, "--commit")
	} else {
		if _, err := fmt.Fprintf(log, "generating debian/changelog entry for %s from initial history\n", targetVersion); err != nil {
			return fmt.Errorf("write generation log: %w", err)
		}
		args = append(args, "--auto", "--commit")
	}

	if _, err := run(ctx, repoRoot, "gbp", args...); err != nil {
		return fmt.Errorf("run gbp dch for %s: %w", targetVersion, err)
	}
	return nil
}

func RunPrepareDebianChangelog(ctx context.Context, repoRoot, releaseTag string, log io.Writer) error {
	return PrepareDebianChangelog(ctx, repoRoot, releaseTag, log, execCommand)
}

func releaseVersion(tag string) (string, error) {
	if !strings.HasPrefix(tag, "v") || len(tag) == 1 {
		return "", fmt.Errorf("release tag %q must start with v", tag)
	}
	return tag[1:], nil
}

func topChangelogVersion(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		start := strings.IndexByte(line, '(')
		end := strings.IndexByte(line, ')')
		if start == -1 || end == -1 || end <= start+1 {
			return "", fmt.Errorf("parse top changelog version from %s: malformed header %q", path, line)
		}
		return line[start+1 : end], nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return "", fmt.Errorf("parse top changelog version from %s: no changelog entries found", path)
}

func previousReleaseTag(ctx context.Context, repoRoot, releaseTag string, run commandRunner) (string, error) {
	out, err := run(ctx, repoRoot, "git", "tag", "-l", "v[0-9]*", "--sort=-v:refname")
	if err != nil {
		return "", fmt.Errorf("list release tags: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		tag := strings.TrimSpace(line)
		if tag == "" || tag == releaseTag {
			continue
		}
		return tag, nil
	}
	return "", nil
}

func execCommand(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
