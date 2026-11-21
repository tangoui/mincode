package terminal

import "testing"

func TestSSHChangeDirectoryCommandPreservesTildeAndQuotes(t *testing.T) {
	cases := map[string]string{
		"~":                 `cd -- "$HOME"`,
		"~/projects/my app": `cd -- "$HOME"/'projects/my app'`,
		"/srv/a'b":          `cd -- '/srv/a'"'"'b'`,
	}
	for input, expected := range cases {
		if actual := sshChangeDirectoryCommand(input); actual != expected {
			t.Fatalf("%q: got %q want %q", input, actual, expected)
		}
	}
}

func TestNormalizeSSHInitializationRejectsMultilineCommand(t *testing.T) {
	if _, _, err := normalizeSSHInitialization("/srv", []string{"echo ok\necho bad"}); err == nil {
		t.Fatal("expected multiline command to be rejected")
	}
}

func TestNormalizeSSHInitializationTrimsAndDropsEmptyCommands(t *testing.T) {
	directory, commands, err := normalizeSSHInitialization("  ~/app  ", []string{"  pwd ", "  "})
	if err != nil {
		t.Fatal(err)
	}
	if directory != "~/app" || len(commands) != 1 || commands[0] != "pwd" {
		t.Fatalf("directory=%q commands=%#v", directory, commands)
	}
}
