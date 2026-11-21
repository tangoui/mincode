package fs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func writeRegexFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func regexSearchFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	long := strings.Repeat("é", 320) + " needle"
	writeRegexFixture(t, root, map[string]string{
		"b.txt":                  "alpha needle\nNeedle beta\n",
		"a/z.go":                 "package a\n// needle needle\n",
		"a/b/deep.go":            "func needle() {}\r\nneedle$\r\n",
		"a-b/x.txt":              "needle in a-b\n",
		".hidden/secret.txt":     "hidden needle\n",
		"node_modules/pkg/x.js":  "needle in deps\n",
		"sub/build/out.txt":      "needle in build\n",
		"build":                  "needle in a file named build\n",
		"cjk.txt":                "配置needle文件\nneedles\n",
		"invalid.txt":            "x\xffneedle\xfe tail\n",
		"long.txt":               long + "\n",
		"late-nul.txt":           strings.Repeat("a", 600) + "\x00 needle\n",
		"bin.dat":                "\x00\x01needle\n",
		"anchor.txt":             "first\nneedle\nneedle trailing\n",
		".gitignore":             "b.txt\n",
		"no-trailing-newline.md": "last needle",
	})
	big := strings.Repeat("needle\n", (regexMaxFileBytes/7)+10)
	writeRegexFixture(t, root, map[string]string{"big.txt": big})
	if err := os.Symlink(filepath.Join(root, "b.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalLocalProjectRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func withoutRipgrep(t *testing.T) {
	t.Helper()
	previous := findCodeSearchExecutable
	findCodeSearchExecutable = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { findCodeSearchExecutable = previous })
}

func regexHits(results []regexMatch) []string {
	hits := make([]string, 0, len(results))
	for _, r := range results {
		hits = append(hits, r.RelativePath+":"+itoa(r.Line)+":"+itoa(r.Col))
	}
	return hits
}

func itoa(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRegexSearchBuiltinSemantics(t *testing.T) {
	withoutRipgrep(t)
	root := regexSearchFixture(t)

	re, err := buildSearchRegex("needle", true, true)
	if err != nil {
		t.Fatal(err)
	}
	results, err := regexSearch(context.Background(), root, ".", re, "")
	if err != nil {
		t.Fatal(err)
	}
	// ReadDir 排序是字节序：".hidden" < "a" < "a-b" 之后按目录内顺序深度优先。
	want := []string{
		".hidden/secret.txt:1:8",
		"a/b/deep.go:1:6", "a/b/deep.go:2:1",
		"a/z.go:2:4", "a/z.go:2:11",
		"a-b/x.txt:1:1",
		"anchor.txt:2:1", "anchor.txt:3:1",
		"b.txt:1:7",
		"build:1:1",
		"cjk.txt:1:7",
		"invalid.txt:1:3",
		"late-nul.txt:1:603",
		"long.txt:1:642",
		"no-trailing-newline.md:1:6",
	}
	if got := regexHits(results); !reflect.DeepEqual(got, want) {
		t.Fatalf("hits mismatch\n got %v\nwant %v", got, want)
	}
	for _, r := range results {
		if r.RelativePath == "long.txt" {
			if n := len([]rune(r.LineText)); n != 300 {
				t.Fatalf("long line not truncated to 300 runes: %d", n)
			}
			if r.MatchEnd-r.MatchStart != len("needle") {
				t.Fatalf("match offsets must refer to the full line: %+v", r)
			}
		}
		if r.RelativePath == "a/b/deep.go" && !strings.HasSuffix(r.LineText, "\r") {
			t.Fatalf("CRLF line text must keep \\r like the split-based scanner: %q", r.LineText)
		}
		if r.Path != filepath.Join(root, filepath.FromSlash(r.RelativePath)) {
			t.Fatalf("unexpected absolute path %q", r.Path)
		}
	}
}

func TestRegexSearchBuiltinAnchorsCaseAndGlob(t *testing.T) {
	withoutRipgrep(t)
	root := regexSearchFixture(t)

	re, _ := buildSearchRegex("^needle$", false, false)
	results, err := regexSearch(context.Background(), root, ".", re, "*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := regexHits(results), []string{"anchor.txt:2:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("anchored search: got %v want %v", got, want)
	}

	re, _ = buildSearchRegex(`\Aneedle`, false, false)
	results, _ = regexSearch(context.Background(), root, ".", re, "anchor.???")
	if got, want := regexHits(results), []string{"anchor.txt:2:1", "anchor.txt:3:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("\\A must anchor per line: got %v want %v", got, want)
	}

	re, _ = buildSearchRegex("NEEDLE BETA", false, false)
	results, _ = regexSearch(context.Background(), root, ".", re, "")
	if got, want := regexHits(results), []string{"b.txt:2:1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case-insensitive: got %v want %v", got, want)
	}

	results, _ = regexSearch(context.Background(), root, ".", re, "src/*")
	if len(results) != 0 {
		t.Fatalf("a glob containing / never matches a file name, got %v", results)
	}
}

func TestRegexSearchHonoursResultCap(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	writeRegexFixture(t, root, map[string]string{"many.txt": strings.Repeat("x x x\n", regexMaxResults)})
	re, _ := buildSearchRegex("x", true, false)
	results, err := regexSearch(context.Background(), root, ".", re, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != regexMaxResults {
		t.Fatalf("expected %d results, got %d", regexMaxResults, len(results))
	}
}

func TestRegexSearchCancelledContext(t *testing.T) {
	withoutRipgrep(t)
	root := regexSearchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	re, _ := buildSearchRegex("needle", true, false)
	if _, err := regexSearch(ctx, root, ".", re, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestRegexFilePrefilterIsLineSuperset(t *testing.T) {
	for _, pattern := range []string{`^needle$`, `(?i)NEEDLE\b`, `a.b`, `x[^y]z`} {
		re := regexp.MustCompile(pattern)
		prefilter := regexFilePrefilter(re)
		if prefilter == nil {
			t.Fatalf("%s: expected a prefilter", pattern)
		}
		for _, text := range []string{"zz\nneedle\nq", "a\xffb", "xaz\nNeedle!"} {
			lineMatch := false
			for _, line := range strings.Split(text, "\n") {
				lineMatch = lineMatch || re.MatchString(line)
			}
			if lineMatch && !prefilter.MatchString(text) {
				t.Fatalf("%s: prefilter rejected %q although a line matches", pattern, text)
			}
		}
	}
	if regexFilePrefilter(regexp.MustCompile(`\Afoo\z`)) != nil {
		t.Fatal("explicit text anchors cannot be prefiltered")
	}
}

func TestRipgrepPatternTranslation(t *testing.T) {
	for _, pattern := range []string{`\bfoo\b`, `(?i)straße`, `[^a]+`, `a.b`, `^x$`, `(a|b){2,3}c*?`, `\pL+`} {
		if _, ok := ripgrepPatternFromGo(pattern); !ok {
			t.Fatalf("%s: expected a translation", pattern)
		}
	}
	for _, pattern := range []string{`a\nb`, `[\n]`} {
		if _, ok := ripgrepPatternFromGo(pattern); ok {
			t.Fatalf("%s: expected no translation", pattern)
		}
	}
	if !ripgrepCanPrefilterGlob("*.go") || ripgrepCanPrefilterGlob("*.{go,ts}") || ripgrepCanPrefilterGlob("#x") {
		t.Fatal("unexpected glob prefilter decision")
	}
}

// 本机装有 rg 时，逐一对比两个引擎的完整输出。
func TestRegexSearchRipgrepMatchesBuiltin(t *testing.T) {
	if _, err := findCodeSearchExecutable("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	root := regexSearchFixture(t)
	cases := []struct {
		pattern         string
		caseSensitive   bool
		wholeWord       bool
		glob            string
		expectRipgrepOK bool
	}{
		{"needle", true, true, "", true},
		{"needle", false, false, "", true},
		{"NEEDLE", false, true, "*.txt", true},
		{"^needle$", true, false, "", true},
		{`\Aneedle`, true, false, "", true},
		{`needle\$\r$`, true, false, "*.go", true},
		{`x.needle`, true, false, "", true},
		{`[^a-z]needle`, true, false, "", true},
		{`配置\w+`, true, false, "", true},
		{`(?i)É{3}`, true, false, "long.???", true},
		{`needle|beta`, false, false, "*.{txt,md}", true},
		{`a\nb`, true, false, "", false},
		{`x*`, true, false, "anchor.txt", false},
	}
	for _, tc := range cases {
		re, err := buildSearchRegex(tc.pattern, tc.caseSensitive, tc.wholeWord)
		if err != nil {
			t.Fatalf("%s: %v", tc.pattern, err)
		}
		builtin, err := regexSearchBuiltin(context.Background(), root, ".", re, tc.glob)
		if err != nil {
			t.Fatal(err)
		}
		fast, ok := regexSearchWithRipgrep(context.Background(), root, ".", re, tc.glob)
		if ok != tc.expectRipgrepOK {
			t.Fatalf("%s: ripgrep used=%v, want %v", tc.pattern, ok, tc.expectRipgrepOK)
		}
		if !ok {
			continue
		}
		if !reflect.DeepEqual(fast, builtin) {
			t.Fatalf("%s (glob %q): engines differ\nripgrep %v\nbuiltin %v", tc.pattern, tc.glob, regexHits(fast), regexHits(builtin))
		}
		// 花括号在 simpleGlobMatch 中是字面量，该用例应当为空。
		if (len(builtin) == 0) != (tc.glob == "*.{txt,md}") {
			t.Fatalf("%s (glob %q): unexpected result count %d", tc.pattern, tc.glob, len(builtin))
		}
	}
}

func TestRegexSearchCommandShape(t *testing.T) {
	root := regexSearchFixture(t)
	args, _ := json.Marshal(map[string]any{"projectRoot": root, "pattern": "needle beta", "caseSensitive": false})
	raw, err := handleRegexSearch(context.Background(), "", string(args))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %s", raw)
	}
	for _, key := range []string{"relativePath", "path", "line", "col", "lineText", "matchStart", "matchEnd"} {
		if _, ok := rows[0][key]; !ok {
			t.Fatalf("missing %s in %s", key, raw)
		}
	}
}

func TestRegexSearchRipgrepHonoursResultCap(t *testing.T) {
	if _, err := findCodeSearchExecutable("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	root := t.TempDir()
	writeRegexFixture(t, root, map[string]string{
		"a.txt": strings.Repeat("x x x\n", regexMaxResults),
		"b.txt": strings.Repeat("x\n", regexMaxResults),
	})
	re, _ := buildSearchRegex("x", true, false)
	results, ok := regexSearchWithRipgrep(context.Background(), root, ".", re, "")
	if !ok || len(results) != regexMaxResults {
		t.Fatalf("ripgrep used=%v results=%d, want %d", ok, len(results), regexMaxResults)
	}
}

func TestRegexSearchScopeLimitsToSubdirectory(t *testing.T) {
	root := t.TempDir()
	writeRegexFixture(t, root, map[string]string{
		"top.txt":          "needle\n",
		"src/a.txt":        "needle a\n",
		"src/nested/b.txt": "needle b\n",
		"other/c.txt":      "needle c\n",
		"srcfoo/decoy.txt": "needle decoy\n",
	})
	canon, err := canonicalLocalProjectRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile("needle")
	scope, err := searchScopeUnderRoot(canon, " /src/ ")
	if err != nil || scope != "src" {
		t.Fatalf("scope = %q, %v", scope, err)
	}
	want := []string{"src/a.txt", "src/nested/b.txt"}
	relatives := func(results []regexMatch) []string {
		out := []string{}
		for _, r := range results {
			out = append(out, r.RelativePath)
		}
		return out
	}
	builtin, err := regexSearchBuiltin(context.Background(), canon, scope, re, "")
	if err != nil || !reflect.DeepEqual(relatives(builtin), want) {
		t.Fatalf("builtin scoped results = %v, %v; want %v", relatives(builtin), err, want)
	}
	if fast, ok := regexSearchWithRipgrep(context.Background(), canon, scope, re, ""); ok && !reflect.DeepEqual(relatives(fast), want) {
		t.Fatalf("ripgrep scoped results = %v; want %v", relatives(fast), want)
	}
	for _, bad := range []string{"../outside", "missing", "top.txt"} {
		if _, err := searchScopeUnderRoot(canon, bad); err == nil {
			t.Fatalf("scope %q accepted", bad)
		}
	}
	if scope, err := searchScopeUnderRoot(canon, ""); err != nil || scope != "." {
		t.Fatalf("empty scope = %q, %v", scope, err)
	}
}
