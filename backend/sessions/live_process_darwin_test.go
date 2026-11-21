//go:build darwin

package sessions

import (
	"reflect"
	"testing"
)

func TestMatchingProcessIDsUsesExecutableBasename(t *testing.T) {
	output := `
 101 /Users/admin/.local/bin/claude
 202 /opt/homebrew/bin/codex
 303 /Applications/Claude Desktop.app/Contents/MacOS/2.1.237
 404 /Applications/Developer Tools.app/Contents/MacOS/zsh
`
	want := map[string]struct{}{"claude": {}, "codex": {}}
	if got := matchingProcessIDs(output, want); !reflect.DeepEqual(got, []string{"101", "202"}) {
		t.Fatalf("matchingProcessIDs() = %v, want [101 202]", got)
	}
}
