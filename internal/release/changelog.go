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
	"time"
)

type commandRunner func(ctx context.Context, dir string, name string, args ...string) (string, error)
type envLookup func(string) string

func PrepareDebianChangelog(ctx context.Context, repoRoot, releaseTag string, log io.Writer, run commandRunner) error {
	return prepareDebianChangelog(ctx, repoRoot, releaseTag, log, run, os.Getenv, time.Now)
}

func prepareDebianChangelog(
	ctx context.Context,
	repoRoot, releaseTag string,
	log io.Writer,
	run commandRunner,
	getenv envLookup,
	now func() time.Time,
) error {
	version, err := releaseVersion(releaseTag)
	if err != nil {
		return err
	}
	targetVersion := version + "-1"
	changelogPath := filepath.Join(repoRoot, "debian", "changelog")

	header, err := topChangelogHeader(changelogPath)
	if err != nil {
		return err
	}
	if header.version == targetVersion {
		if _, err := fmt.Fprintf(log, "reusing existing debian/changelog entry for %s\n", targetVersion); err != nil {
			return fmt.Errorf("write reuse log: %w", err)
		}
		return nil
	}

	maintainer, err := changelogMaintainer(ctx, repoRoot, run, getenv)
	if err != nil {
		return err
	}

	existing, err := os.ReadFile(changelogPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", changelogPath, err)
	}
	if _, err := fmt.Fprintf(log, "prepending clean debian/changelog entry for %s\n", targetVersion); err != nil {
		return fmt.Errorf("write generation log: %w", err)
	}

	entry := formatDebianChangelogEntry(header.packageName, targetVersion, version, maintainer, now().UTC())
	if err := os.WriteFile(changelogPath, []byte(entry+string(existing)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", changelogPath, err)
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

type changelogHeader struct {
	packageName string
	version     string
}

func topChangelogHeader(path string) (changelogHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return changelogHeader{}, fmt.Errorf("open %s: %w", path, err)
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
			return changelogHeader{}, fmt.Errorf("parse top changelog header from %s: malformed header %q", path, line)
		}
		packageName := strings.TrimSpace(line[:start])
		if packageName == "" {
			return changelogHeader{}, fmt.Errorf("parse top changelog header from %s: empty package name in %q", path, line)
		}
		return changelogHeader{
			packageName: packageName,
			version:     line[start+1 : end],
		}, nil
	}
	if err := scanner.Err(); err != nil {
		return changelogHeader{}, fmt.Errorf("read %s: %w", path, err)
	}
	return changelogHeader{}, fmt.Errorf("parse top changelog header from %s: no changelog entries found", path)
}

type maintainer struct {
	name  string
	email string
}

func changelogMaintainer(ctx context.Context, repoRoot string, run commandRunner, getenv envLookup) (maintainer, error) {
	name := strings.TrimSpace(getenv("DEBFULLNAME"))
	if name == "" {
		value, err := run(ctx, repoRoot, "git", "config", "user.name")
		if err != nil {
			return maintainer{}, fmt.Errorf("resolve changelog maintainer name: %w", err)
		}
		name = strings.TrimSpace(value)
	}
	email := strings.TrimSpace(getenv("DEBEMAIL"))
	if email == "" {
		value, err := run(ctx, repoRoot, "git", "config", "user.email")
		if err != nil {
			return maintainer{}, fmt.Errorf("resolve changelog maintainer email: %w", err)
		}
		email = strings.TrimSpace(value)
	}
	if name == "" || email == "" {
		return maintainer{}, fmt.Errorf("resolve changelog maintainer: need DEBFULLNAME/DEBEMAIL or git user.name/user.email")
	}
	return maintainer{name: name, email: email}, nil
}

func formatDebianChangelogEntry(packageName, targetVersion, releaseVersion string, maintainer maintainer, timestamp time.Time) string {
	return fmt.Sprintf(
		"%s (%s) unstable; urgency=medium\n\n  * Release %s.\n\n -- %s <%s>  %s\n\n",
		packageName,
		targetVersion,
		releaseVersion,
		maintainer.name,
		maintainer.email,
		timestamp.Format(time.RFC1123Z),
	)
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
