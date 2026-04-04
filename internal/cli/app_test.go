package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestTopLevelContractByInvokedName(t *testing.T) {
	cases := []struct {
		name       string
		invokedAs  string
		args       []string
		wantExit   int
		wantStdout string
		wantStderr string
	}{
		{"help canonical long", "clankerval", []string{"--help"}, 0, "clankerval <command> [flags]", ""},
		{"help canonical short", "clankerval", []string{"-h"}, 0, "clankerval <command> [flags]", ""},
		{"help compat long", "clnkeval", []string{"--help"}, 0, "clnkeval <command> [flags]", ""},
		{"help compat word", "clnkeval", []string{"help"}, 0, "clnkeval <command> [flags]", ""},
		{"version canonical long", "clankerval", []string{"--version"}, 0, "clankerval ", ""},
		{"version canonical short", "clankerval", []string{"-V"}, 0, "clankerval ", ""},
		{"version compat long", "clnkeval", []string{"--version"}, 0, "clnkeval ", ""},
		{"version compat word", "clnkeval", []string{"version"}, 0, "clnkeval ", ""},
		{"no args canonical", "clankerval", nil, 1, "", "clankerval <command> [flags]"},
		{"no args compat", "clnkeval", nil, 1, "", "clnkeval <command> [flags]"},
		{"unknown canonical", "clankerval", []string{"bogus"}, 1, "", "unknown command"},
		{"unknown compat", "clnkeval", []string{"bogus"}, 1, "", "unknown command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			exit := Run(tc.invokedAs, "dev", tc.args, ".", stdout, stderr, func(string) string { return "" })
			if exit != tc.wantExit {
				t.Fatalf("exit = %d, want %d", exit, tc.wantExit)
			}
			if tc.wantStdout != "" && !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStdout == "" && stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty stdout", stdout.String())
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tc.wantStderr)
			}
			if tc.wantStderr == "" && stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty stderr", stderr.String())
			}
		})
	}
}

func TestSubcommandHelpStreamsAndAliases(t *testing.T) {
	for _, invokedAs := range []string{"clankerval", "clnkeval"} {
		for _, args := range [][]string{{"run", "--help"}, {"run", "-h"}, {"init", "--help"}, {"init", "-h"}} {
			t.Run(invokedAs+" "+strings.Join(args, " "), func(t *testing.T) {
				stdout := &bytes.Buffer{}
				stderr := &bytes.Buffer{}
				exit := Run(invokedAs, "dev", args, ".", stdout, stderr, func(string) string { return "" })
				if exit != 0 {
					t.Fatalf("exit = %d, want 0", exit)
				}
				if stdout.Len() != 0 {
					t.Fatalf("stdout = %q, want empty stdout", stdout.String())
				}
				if !strings.Contains(stderr.String(), "Usage: "+invokedAs+" "+args[0]) {
					t.Fatalf("stderr = %q, want subcommand usage for %s", stderr.String(), invokedAs)
				}
			})
		}
	}
}
