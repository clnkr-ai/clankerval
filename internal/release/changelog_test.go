package release

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPrepareDebianChangelogReusesTopVersion(t *testing.T) {
	repoRoot := t.TempDir()
	writeChangelog(t, repoRoot, `clankerval (0.1.0-1) unstable; urgency=medium

  * Initial release.

 -- Brian Cosgrove <bcosgrove@gmail.com>  Sun, 05 Apr 2026 00:22:34 +0000
`)

	runner := &fakeRunner{
		dir: repoRoot,
		outputs: map[string]string{
			"git tag -l v[0-9]* --sort=-v:refname": "v0.1.0\n",
		},
	}
	log := &bytes.Buffer{}

	if err := PrepareDebianChangelog(context.Background(), repoRoot, "v0.1.0", log, runner.run); err != nil {
		t.Fatalf("PrepareDebianChangelog(): %v", err)
	}

	if got := runner.commands; len(got) != 0 {
		t.Fatalf("commands = %v, want none", got)
	}
	if got, want := log.String(), "reusing existing debian/changelog entry for 0.1.0-1\n"; got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestPrepareDebianChangelogPrependsCleanReleaseEntryFromEnv(t *testing.T) {
	repoRoot := t.TempDir()
	writeChangelog(t, repoRoot, `clankerval (0.1.0-1) unstable; urgency=medium

  * Initial release.

 -- Brian Cosgrove <bcosgrove@gmail.com>  Sun, 05 Apr 2026 00:22:34 +0000
`)

	runner := &fakeRunner{dir: repoRoot}
	log := &bytes.Buffer{}
	now := func() time.Time {
		return time.Date(2026, time.April, 5, 1, 2, 3, 0, time.UTC)
	}

	if err := prepareDebianChangelog(
		context.Background(),
		repoRoot,
		"v0.1.1",
		log,
		runner.run,
		mapLookup(map[string]string{
			"DEBFULLNAME": "Release Bot",
			"DEBEMAIL":    "release@example.com",
		}),
		now,
	); err != nil {
		t.Fatalf("PrepareDebianChangelog(): %v", err)
	}

	if got := runner.commands; len(got) != 0 {
		t.Fatalf("commands = %v, want none", got)
	}
	if got, want := log.String(), "prepending clean debian/changelog entry for 0.1.1-1\n"; got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}

	got, err := os.ReadFile(filepath.Join(repoRoot, "debian", "changelog"))
	if err != nil {
		t.Fatalf("ReadFile(changelog): %v", err)
	}
	want := `clankerval (0.1.1-1) unstable; urgency=medium

  * Release 0.1.1.

 -- Release Bot <release@example.com>  Sun, 05 Apr 2026 01:02:03 +0000

clankerval (0.1.0-1) unstable; urgency=medium

  * Initial release.

 -- Brian Cosgrove <bcosgrove@gmail.com>  Sun, 05 Apr 2026 00:22:34 +0000
`
	if string(got) != want {
		t.Fatalf("changelog = %q, want %q", string(got), want)
	}
}

func TestPrepareDebianChangelogPrependsCleanReleaseEntryFromGitConfig(t *testing.T) {
	repoRoot := t.TempDir()
	writeChangelog(t, repoRoot, `clankerval (0.1.1-1) unstable; urgency=medium

  * Release 0.1.1.

 -- Brian Cosgrove <bcosgrove@gmail.com>  Sun, 05 Apr 2026 00:22:34 +0000
`)

	runner := &fakeRunner{
		dir: repoRoot,
		outputs: map[string]string{
			"git config user.name":  "Git User\n",
			"git config user.email": "git@example.com\n",
		},
	}
	log := &bytes.Buffer{}
	now := func() time.Time {
		return time.Date(2026, time.April, 5, 2, 3, 4, 0, time.UTC)
	}

	if err := prepareDebianChangelog(
		context.Background(),
		repoRoot,
		"v0.1.2",
		log,
		runner.run,
		mapLookup(nil),
		now,
	); err != nil {
		t.Fatalf("PrepareDebianChangelog(): %v", err)
	}

	wantCommands := [][]string{
		{"git", "config", "user.name"},
		{"git", "config", "user.email"},
	}
	if got := runner.commands; !reflect.DeepEqual(got, wantCommands) {
		t.Fatalf("commands = %v, want %v", got, wantCommands)
	}

	got, err := os.ReadFile(filepath.Join(repoRoot, "debian", "changelog"))
	if err != nil {
		t.Fatalf("ReadFile(changelog): %v", err)
	}
	want := `clankerval (0.1.2-1) unstable; urgency=medium

  * Release 0.1.2.

 -- Git User <git@example.com>  Sun, 05 Apr 2026 02:03:04 +0000

clankerval (0.1.1-1) unstable; urgency=medium

  * Release 0.1.1.

 -- Brian Cosgrove <bcosgrove@gmail.com>  Sun, 05 Apr 2026 00:22:34 +0000
`
	if string(got) != want {
		t.Fatalf("changelog = %q, want %q", string(got), want)
	}
}

type fakeRunner struct {
	commands [][]string
	outputs  map[string]string
	dir      string
}

func (f *fakeRunner) run(_ context.Context, dir string, name string, args ...string) (string, error) {
	tokens := append([]string{name}, args...)
	f.commands = append(f.commands, tokens)
	key := name
	if len(args) > 0 {
		key += " " + joinArgs(args)
	}
	if got, want := dir, f.dir; got != want {
		return "", &pathError{dir: dir}
	}
	return f.outputs[key], nil
}

type pathError struct {
	dir string
}

func (e *pathError) Error() string {
	return "unexpected dir: " + e.dir
}

func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	out := args[0]
	for _, arg := range args[1:] {
		out += " " + arg
	}
	return out
}

func mapLookup(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func writeChangelog(t *testing.T, repoRoot, body string) {
	t.Helper()
	path := filepath.Join(repoRoot, "debian", "changelog")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
