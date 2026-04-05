package release

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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

func TestPrepareDebianChangelogGeneratesNextReleaseFromPreviousTag(t *testing.T) {
	repoRoot := t.TempDir()
	writeChangelog(t, repoRoot, `clankerval (0.1.0-1) unstable; urgency=medium

  * Initial release.

 -- Brian Cosgrove <bcosgrove@gmail.com>  Sun, 05 Apr 2026 00:22:34 +0000
`)

	runner := &fakeRunner{
		dir: repoRoot,
		outputs: map[string]string{
			"git tag -l v[0-9]* --sort=-v:refname": "v0.1.1\nv0.1.0\n",
		},
	}
	log := &bytes.Buffer{}

	if err := PrepareDebianChangelog(context.Background(), repoRoot, "v0.1.1", log, runner.run); err != nil {
		t.Fatalf("PrepareDebianChangelog(): %v", err)
	}

	wantCommands := [][]string{
		{"git", "tag", "-l", "v[0-9]*", "--sort=-v:refname"},
		{"gbp", "dch", "--new-version=0.1.1-1", "--distribution=unstable", "--since=v0.1.0", "--commit"},
	}
	if got := runner.commands; !reflect.DeepEqual(got, wantCommands) {
		t.Fatalf("commands = %v, want %v", got, wantCommands)
	}
	if got, want := log.String(), "generating debian/changelog entry for 0.1.1-1 from v0.1.0\n"; got != want {
		t.Fatalf("log = %q, want %q", got, want)
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
