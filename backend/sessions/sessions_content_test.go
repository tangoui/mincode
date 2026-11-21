package sessions

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractContentTextPreservesClaudeTodoWriteItems(t *testing.T) {
	content := json.RawMessage(`[
		{"type":"tool_use","name":"TodoWrite","input":{"todos":[
			{"content":"Inspect the current editor file","status":"in_progress"},
			{"content":"Report the result","status":"pending"}
		]}}
	]`)

	got := extractContentText(content)
	for _, want := range []string{
		"[TodoWrite]",
		"- [in_progress] Inspect the current editor file",
		"- [pending] Report the result",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("extractContentText() = %q, expected %q", got, want)
		}
	}
}

func TestExtractContentTextKeepsTextAndToolNameWithoutToolArguments(t *testing.T) {
	content := json.RawMessage(`[
		{"type":"text","text":"I will inspect the repository."},
		{"type":"tool_use","name":"Bash","input":{"command":"cat .env"}}
	]`)

	got := extractContentText(content)
	if !strings.Contains(got, "I will inspect the repository.") {
		t.Fatalf("extractContentText() = %q, missing assistant text", got)
	}
	if !strings.Contains(got, "[Tool: Bash]") {
		t.Fatalf("extractContentText() = %q, missing tool marker", got)
	}
	if strings.Contains(got, "cat .env") {
		t.Fatalf("extractContentText() must not serialize arbitrary tool arguments: %q", got)
	}
}
