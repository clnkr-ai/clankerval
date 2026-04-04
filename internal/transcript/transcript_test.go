package transcript

import (
	"strings"
	"testing"
)

func TestFormatCommandResultEscapesSections(t *testing.T) {
	got := FormatCommandResult(CommandResult{
		Command:  "printf [x] && echo <y>",
		ExitCode: 7,
		Stdout:   "hello [x]\n",
		Stderr:   "warn <y>\n",
	})

	for _, want := range []string{
		"[command]\nprintf &#91;x&#93; &amp;&amp; echo &lt;y&gt;\n[/command]",
		"[exit_code]\n7\n[/exit_code]",
		"[stdout]\nhello &#91;x&#93;\n\n[/stdout]",
		"[stderr]\nwarn &lt;y&gt;\n\n[/stderr]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("FormatCommandResult() = %q, want substring %q", got, want)
		}
	}
}

func TestFormatStateMessageAndExtractStateCwd(t *testing.T) {
	msg := FormatStateMessage("/tmp/work")
	if got, ok := ExtractStateCwd(msg); !ok || got != "/tmp/work" {
		t.Fatalf("ExtractStateCwd(%q) = (%q, %v), want (/tmp/work, true)", msg, got, ok)
	}
}

func TestExtractStateCwdRejectsForeignState(t *testing.T) {
	content := "[state]\n{\"source\":\"user\",\"kind\":\"state\",\"cwd\":\"/tmp/wrong\"}\n[/state]"
	if got, ok := ExtractStateCwd(content); ok || got != "" {
		t.Fatalf("ExtractStateCwd(%q) = (%q, %v), want empty false", content, got, ok)
	}
}

func TestExtractLatestCwd(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "plain instruction"},
		{Role: "user", Content: FormatStateMessage("/tmp/old")},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: FormatStateMessage("/tmp/latest")},
	}

	if got, ok := ExtractLatestCwd(messages); !ok || got != "/tmp/latest" {
		t.Fatalf("ExtractLatestCwd() = (%q, %v), want (/tmp/latest, true)", got, ok)
	}
}
