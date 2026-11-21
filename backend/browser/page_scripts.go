package browser

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Page scripts run in the page's main world. Each scripts/*.js file is one
// expression `(() => { ... })()` whose inputs are __ARG0__, __ARG1__, ...
// placeholders; see scripts/README.md. The vitest harness in
// vue-app/tests/browser-scripts/run-page-script.ts applies the same rules.
//
//go:embed scripts/*.js
var pageScriptFiles embed.FS

// pageScriptActions lists the element actions dispatched by scripts/actions.js
// and their argument counts. actions.js receives __ARG0__ = action name and
// __ARG1__ = the argument array.
var pageScriptActions = map[string]int{
	"click":     2, // snapshotId, ref
	"fill":      3, // snapshotId, ref, value
	"press_key": 3, // snapshotId, ref, key
	"wait_for":  4, // text, urlContains, snapshotId, ref
	// snapshotId, ref, values, labels (string arrays; exactly one non-empty)
	"select_option": 4,
	"set_checked":   3, // snapshotId, ref, checked
	"checked_state": 2, // snapshotId, ref
	"hover":         2, // snapshotId, ref
	// snapshotId, ref, direction, amount (pixels or "page"), containerRef
	"scroll": 5,
}

const pageScriptActionsFile = "actions"

var pageScriptPlaceholder = regexp.MustCompile(`__ARG(\d+)__`)

var pageScriptSources = loadPageScripts()

func loadPageScripts() map[string]string {
	entries, err := pageScriptFiles.ReadDir("scripts")
	if err != nil {
		panic(fmt.Sprintf("read embedded page scripts: %v", err))
	}
	sources := map[string]string{}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".js")
		if entry.IsDir() || name == entry.Name() {
			continue
		}
		raw, err := pageScriptFiles.ReadFile(path.Join("scripts", entry.Name()))
		if err != nil {
			panic(fmt.Sprintf("read embedded page script %s: %v", entry.Name(), err))
		}
		if _, clash := pageScriptActions[name]; clash {
			panic(fmt.Sprintf("page script file %s.js shadows the actions.js action of the same name", name))
		}
		sources[name] = strings.TrimSpace(string(raw))
	}
	if _, ok := sources[pageScriptActionsFile]; !ok {
		panic("embedded page scripts are missing actions.js")
	}
	return sources
}

// pageScriptNames returns every name accepted by pageScript.
func pageScriptNames() []string {
	names := make([]string, 0, len(pageScriptSources)+len(pageScriptActions))
	for name := range pageScriptSources {
		if name != pageScriptActionsFile {
			names = append(names, name)
		}
	}
	for name := range pageScriptActions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// pageScript returns the named page script with args JSON-encoded into its
// placeholders. Names resolve to scripts/<name>.js, or to an action of
// scripts/actions.js. A wrong name or argument count is a programming error
// and panics.
func pageScript(name string, args ...any) string {
	if arity, ok := pageScriptActions[name]; ok {
		if len(args) != arity {
			panic(fmt.Sprintf("page script %s expects %d arguments, got %d", name, arity, len(args)))
		}
		if args == nil {
			args = []any{}
		}
		return expandPageScript(name, pageScriptSources[pageScriptActionsFile], []any{name, args})
	}
	source, ok := pageScriptSources[name]
	if !ok || name == pageScriptActionsFile {
		panic(fmt.Sprintf("unknown page script %q", name))
	}
	return expandPageScript(name, source, args)
}

// expandPageScript replaces each __ARGn__ with the JSON encoding of args[n] in a
// single pass, so __ARG10__ is never read as __ARG1__ and placeholder-like text
// inside an argument is never expanded. Every argument must be used and every
// placeholder must have an argument.
func expandPageScript(name, source string, args []any) string {
	encoded := make([]string, len(args))
	for index, arg := range args {
		raw, err := json.Marshal(arg)
		if err != nil {
			panic(fmt.Sprintf("page script %s argument %d: %v", name, index, err))
		}
		encoded[index] = string(raw)
	}
	used := make([]bool, len(args))
	result := pageScriptPlaceholder.ReplaceAllStringFunc(source, func(match string) string {
		index, err := strconv.Atoi(pageScriptPlaceholder.FindStringSubmatch(match)[1])
		if err != nil || index >= len(args) {
			panic(fmt.Sprintf("page script %s placeholder %s has no argument (%d given)", name, match, len(args)))
		}
		used[index] = true
		return encoded[index]
	})
	for index, ok := range used {
		if !ok {
			panic(fmt.Sprintf("page script %s does not use argument %d (__ARG%d__)", name, index, index))
		}
	}
	return result
}
