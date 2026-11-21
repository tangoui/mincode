package browser

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// validPageScriptArgs holds one well-formed argument list per page script name.
var validPageScriptArgs = map[string][]any{
	"snapshot":        {12000, 200},
	"inspect":         {"snap-1", "e1"},
	"click":           {"snap-1", "e1"},
	"fill":            {"snap-1", "e1", "hello"},
	"press_key":       {"snap-1", "e1", "Enter"},
	"wait_for":        {"needle", "", "", ""},
	"computer_action": {json.RawMessage(`{"type":"click","x":1,"y":2}`), json.RawMessage(`{"imageWidth":10,"imageHeight":10,"viewportWidth":10,"viewportHeight":10}`)},
	"diagnostics":     {"console", 100},
	"select_option":   {"snap-1", "e1", []string{"a"}, []string{}},
	"set_checked":     {"snap-1", "e1", true},
	"checked_state":   {"snap-1", "e1"},
	"hover":           {"snap-1", "e1"},
	"scroll":          {"", "", "down", "page", ""},
	"history_state":   {"token-1", true},
	"history_go":      {-1},
	"extract":         {"markdown", 20000, "", ""},
}

func expectPanic(t *testing.T, label string, run func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s: expected panic", label)
		}
	}()
	run()
}

func TestPageScriptReplacesEveryPlaceholder(t *testing.T) {
	for name, args := range validPageScriptArgs {
		script := pageScript(name, args...)
		if strings.Contains(script, "__ARG") {
			t.Fatalf("%s: placeholder left in script: %s", name, script)
		}
		if !strings.HasPrefix(strings.TrimSpace(script), "(() =>") || !strings.HasSuffix(strings.TrimSpace(script), "})()") {
			t.Fatalf("%s: expected an IIFE expression", name)
		}
		expectPanic(t, name+" with one argument too few", func() { pageScript(name, args[:len(args)-1]...) })
		expectPanic(t, name+" with one argument too many", func() { pageScript(name, append(append([]any{}, args...), "extra")...) })
	}
	expectPanic(t, "unknown name", func() { pageScript("no_such_script") })
}

func TestPageScriptNamesAreCoveredByTest(t *testing.T) {
	for _, name := range pageScriptNames() {
		if _, ok := validPageScriptArgs[name]; !ok {
			t.Fatalf("page script %q has no placeholder test arguments", name)
		}
	}
}

func TestExpandPageScriptPlaceholderRules(t *testing.T) {
	template := "[__ARG0__,__ARG1__,__ARG2__,__ARG3__,__ARG4__,__ARG5__,__ARG6__,__ARG7__,__ARG8__,__ARG9__,__ARG10__,__ARG1__]"
	args := []any{0, "one", 2, 3, 4, 5, 6, 7, 8, 9, "ten"}
	got := expandPageScript("test", template, args)
	want := `[0,"one",2,3,4,5,6,7,8,9,"ten","one"]`
	if got != want {
		t.Fatalf("__ARG10__ must not be read as __ARG1__ followed by 0:\n got %s\nwant %s", got, want)
	}
	// Argument values are inserted once; a value that looks like a placeholder is never expanded again.
	if got := expandPageScript("test", "__ARG0__+__ARG1__", []any{"__ARG1__", "x"}); got != `"__ARG1__"+"x"` {
		t.Fatalf("placeholder text inside an argument was re-expanded: %s", got)
	}
	// Strings are JSON (and therefore JavaScript) literals.
	if got := expandPageScript("test", "__ARG0__", []any{"a'b\"c\u2028</script>"}); got != `"a'b\"c\u2028\u003c/script\u003e"` {
		t.Fatalf("unexpected string encoding: %s", got)
	}
	expectPanic(t, "gap in placeholders", func() { expandPageScript("test", "__ARG0__ __ARG2__", []any{1, 2, 3}) })
}

func TestPageScriptActionArityMatchesActionsJS(t *testing.T) {
	signature := regexp.MustCompile(`(?m)^\s+(\w+)\(([^)]*)\)\{`)
	declared := map[string]int{}
	for _, match := range signature.FindAllStringSubmatch(pageScriptSources[pageScriptActionsFile], -1) {
		params := strings.TrimSpace(match[2])
		count := 0
		if params != "" {
			count = len(strings.Split(params, ","))
		}
		declared[match[1]] = count
	}
	if len(declared) != len(pageScriptActions) {
		t.Fatalf("actions.js declares %v, Go expects %v", declared, pageScriptActions)
	}
	for name, arity := range pageScriptActions {
		if declared[name] != arity {
			t.Fatalf("action %s: Go expects %d arguments, actions.js declares %d", name, arity, declared[name])
		}
	}
}

// A placeholder written twice (or inside a comment) would let a removed real use go
// unnoticed by the unused-argument check, and duplicates argument text in the page.
func TestPageScriptPlaceholdersOccurExactlyOncePerSource(t *testing.T) {
	for file, source := range pageScriptSources {
		counts := map[string]int{}
		for _, match := range pageScriptPlaceholder.FindAllString(source, -1) {
			counts[match]++
		}
		for index := 0; index < len(counts); index++ {
			placeholder := "__ARG" + strconv.Itoa(index) + "__"
			if counts[placeholder] != 1 {
				t.Fatalf("%s.js: %s occurs %d times, want exactly once (placeholders %v)", file, placeholder, counts[placeholder], counts)
			}
		}
	}
}
