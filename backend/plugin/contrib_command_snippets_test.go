package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func decodeCommandSnippetLibraryForTest(raw string) (CommandSnippetLibrary, error) {
	value, _, err := decodeCommandSnippetLibrary(&pointContext{spec: commandSnippetSpec}, 0, "", json.RawMessage(raw))
	if err != nil {
		return CommandSnippetLibrary{}, err
	}
	return value.(CommandSnippetLibrary), nil
}

func TestCommandSnippetDecodeNormalizesAndDefaults(t *testing.T) {
	library, err := decodeCommandSnippetLibraryForTest(`{"id":"docker","title":" Docker ","titleLocalized":{"zh":"容器"},
		"commands":[
		 {"id":"logs","title":"Logs","description":"Follow","command":"docker logs -f {{ container }}  \n\n","variables":[{"name":"container","title":"Container","quote":"posix"}],"tags":["logs","logs"]},
		 {"id":"up","title":"Up","command":["docker compose pull","\tdocker compose up -d  "]},
		 {"id":"ps","title":"PS","command":"docker ps --format '{{.Names}} {{json .}}'"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if library.Title != "Docker" || len(library.Targets) != 3 || len(library.Platforms) != 3 || library.TitleLocalized["zh"] != "容器" {
		t.Fatalf("unexpected library %+v", library)
	}
	logs := library.Commands[0]
	if logs.Command != "docker logs -f {{ container }}" || logs.Variables[0].Quote != "posix" || len(logs.Tags) != 1 {
		t.Fatalf("unexpected command %+v", logs)
	}
	if library.Commands[1].Command != "docker compose pull\n\tdocker compose up -d" {
		t.Fatalf("lines not joined: %q", library.Commands[1].Command)
	}
	if library.Commands[2].Variables == nil || library.Commands[2].Variables[0:0] == nil {
		t.Fatal("variables must be an empty list")
	}
	if library.Commands[1].Variables[0:0] == nil || library.Commands[1].Tags == nil {
		t.Fatal("snapshot lists must not be null")
	}
}

func TestCommandSnippetDecodeRejectsUnsafeInput(t *testing.T) {
	cases := map[string]string{
		"escape":               `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"echo \u001b[31m"}]}`,
		"carriage":             `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls\r"}]}`,
		"crlf":                 `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls\r\npwd"}]}`,
		"bidi":                 `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"echo ‮"}]}`,
		"zero width":           `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ec​ho"}]}`,
		"del":                  `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls\u007f"}]}`,
		"c1":                   `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls\u009b"}]}`,
		"undeclared":           `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ssh {{host}}"}]}`,
		"bad target":           `{"id":"a","title":"A","targets":["cloud"],"commands":[{"id":"x","title":"X","command":"ls"}]}`,
		"empty targets":        `{"id":"a","title":"A","targets":[],"commands":[{"id":"x","title":"X","command":"ls"}]}`,
		"bad platform":         `{"id":"a","title":"A","platforms":["plan9"],"commands":[{"id":"x","title":"X","command":"ls"}]}`,
		"no commands":          `{"id":"a","title":"A","commands":[]}`,
		"empty command":        `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":" \n "}]}`,
		"dup command":          `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls"},{"id":"x","title":"Y","command":"pwd"}]}`,
		"dup variable":         `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls {{a}}","variables":[{"name":"a"},{"name":"a"}]}]}`,
		"bad quote":            `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls {{a}}","variables":[{"name":"a","quote":"shell"}]}]}`,
		"multiline default":    `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls {{a}}","variables":[{"name":"a","default":"x\ny"}]}]}`,
		"bad variable":         `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls","variables":[{"name":"1a"}]}]}`,
		"unknown field":        `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":"ls","run":true}]}`,
		"bad id":               `{"id":"A","title":"A","commands":[{"id":"x","title":"X","command":"ls"}]}`,
		"number":               `{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":5}]}`,
		"too many lines":       fmt.Sprintf(`{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":%q}]}`, strings.Repeat("ls\n", 33)+"ls"),
		"too many array lines": fmt.Sprintf(`{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":[%s"ls"]}]}`, strings.Repeat(`"ls",`, 32)),
		"too long":             fmt.Sprintf(`{"id":"a","title":"A","commands":[{"id":"x","title":"X","command":%q}]}`, strings.Repeat("a", 4097)),
	}
	for name, raw := range cases {
		if _, err := decodeCommandSnippetLibraryForTest(raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestCommandSnippetFinalizeEnforcesPluginBudget(t *testing.T) {
	commands := make([]string, 0, 200)
	for index := 0; index < 200; index++ {
		commands = append(commands, fmt.Sprintf(`{"id":"c%d","title":"C","command":"echo %d"}`, index, index))
	}
	entries := []contributionEntry{}
	for index := 0; index < 3; index++ {
		library, err := decodeCommandSnippetLibraryForTest(fmt.Sprintf(`{"id":"l%d","title":"L","commands":[%s]}`, index, strings.Join(commands, ",")))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, contributionEntry{Index: index, ID: library.ID, Value: library})
	}
	kept, issues := finalizeCommandSnippetLibraries(nil, entries, nil)
	if len(kept) != 2 || len(issues) != 1 || issues[0].ID != "l2" || !strings.Contains(issues[0].Message, "500 commands") {
		t.Fatalf("kept %d issues %+v", len(kept), issues)
	}
}
