package fs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
)

func TestSearchCodeCommandDispatchesThroughRegistry(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "src/agent_run.go", "package agent\n\ntype AgentRun struct{}\n")
	args, err := json.Marshal(map[string]any{
		"projectRoot": root,
		"query":       "AgentRun",
		"mode":        "symbol",
		"limit":       5,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := core.Dispatch("local_fs_search_code", string(args))
	if err != nil {
		t.Fatal(err)
	}
	var result localFsCodeSearchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Kind != "symbol" {
		t.Fatalf("unexpected dispatched result: %#v", result)
	}
}

func TestSearchCodeRanksSymbolDeclarations(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "src/agent_run.go", `package agent

type AgentRun struct {
	RunID string
}

func CancelAgentRun(run AgentRun) error {
	return nil
}
`)
	writeCodeSearchFixture(t, root, "docs/agent-run.md", "AgentRun tracks one task.\n")

	result, err := searchCode(root, "", "AgentRun", "auto", 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.Query != "AgentRun" || result.Mode != "auto" || result.Engine == "" {
		t.Fatalf("unexpected metadata: %#v", result)
	}
	if result.FilesConsidered != 2 {
		t.Fatalf("expected two files, got %d", result.FilesConsidered)
	}
	var foundSymbol bool
	for _, match := range result.Results {
		if filepath.IsAbs(match.RelativePath) {
			t.Fatalf("search leaked an absolute path: %#v", match)
		}
		if match.RelativePath == "src/agent_run.go" && match.Kind == "symbol" && match.Line == 3 {
			foundSymbol = true
			if match.Score < 900 || !strings.Contains(match.LineText, "type AgentRun") {
				t.Fatalf("unexpected symbol match: %#v", match)
			}
		}
	}
	if !foundSymbol {
		t.Fatalf("expected AgentRun declaration, got %#v", result.Results)
	}
}

func TestSearchCodeFallsBackToProjectRootForMissingScope(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "vue-app/src/database.ts", "export const DatabaseBackend = true;\n")

	result, err := searchCode(root, "src", "DatabaseBackend", "text", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].RelativePath != "vue-app/src/database.ts" {
		t.Fatalf("expected root fallback result, got %#v", result.Results)
	}
}

func TestSearchCodePathModeUsesRankedMetadataOnlyResults(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "src/agent-run-store.ts", "export const unrelated = true;\n")
	writeCodeSearchFixture(t, root, "src/other.ts", "const agentRunStore = true;\n")

	result, err := searchCode(root, "", "agent-run", "path", 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Engine != "path-index" {
		t.Fatalf("expected path engine, got %q", result.Engine)
	}
	if len(result.Results) == 0 || result.Results[0].RelativePath != "src/agent-run-store.ts" {
		t.Fatalf("expected exact path candidate first, got %#v", result.Results)
	}
	for _, match := range result.Results {
		if match.Kind != "path" || match.Line != 0 || match.LineText != "" {
			t.Fatalf("expected metadata-only path match, got %#v", match)
		}
	}
}

func TestSearchCodeRespectsScopeIgnoresGeneratedAndSensitiveFiles(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "src/visible.ts", "export const SearchNeedle = true;\n")
	writeCodeSearchFixture(t, root, "tests/outside.ts", "export const SearchNeedle = true;\n")
	writeCodeSearchFixture(t, root, "node_modules/pkg/index.js", "const SearchNeedle = true;\n")
	writeCodeSearchFixture(t, root, ".env", "SearchNeedle=secret\n")
	writeCodeSearchFixture(t, root, "secrets/id_rsa", "SearchNeedle private key\n")

	result, err := searchCode(root, "src", "SearchNeedle", "auto", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) == 0 {
		t.Fatal("expected a scoped source match")
	}
	for _, match := range result.Results {
		if match.RelativePath != "src/visible.ts" {
			t.Fatalf("unexpected result outside scope: %#v", match)
		}
	}

	rootResult, err := searchCode(root, "", "SearchNeedle", "auto", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range rootResult.Results {
		if match.RelativePath == ".env" || strings.Contains(match.RelativePath, "node_modules") || strings.Contains(match.RelativePath, "id_rsa") {
			t.Fatalf("sensitive or generated result was not filtered: %#v", match)
		}
	}
}

func TestSearchCodeAcceptsRegularFileScope(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "src/agent_run.go", "package agent\n\nfunc RunScopedSearch() {}\n")
	writeCodeSearchFixture(t, root, "src/other.go", "func RunScopedSearch() {}\n")

	result, err := searchCode(root, "src/agent_run.go", "RunScopedSearch", "auto", 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesConsidered != 1 {
		t.Fatalf("expected one scoped file, got %d", result.FilesConsidered)
	}
	if len(result.Results) == 0 {
		t.Fatal("expected a result from the scoped file")
	}
	for _, match := range result.Results {
		if match.RelativePath != "src/agent_run.go" {
			t.Fatalf("file scope returned an out-of-scope result: %#v", match)
		}
	}
}

func TestSearchCodeDoesNotSearchSensitiveRegularFileScope(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, ".env", "SCOPED_SECRET=SearchNeedle\n")

	result, err := searchCode(root, ".env", "SearchNeedle", "auto", 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesConsidered != 0 || len(result.Results) != 0 {
		t.Fatalf("sensitive file scope must not produce results: %#v", result)
	}
}

func TestSearchCodeRejectsSymlinkFileScope(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("SearchNeedle outside project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := searchCode(root, "linked.txt", "SearchNeedle", "text", 20); err == nil {
		t.Fatal("symlink file scope must be rejected")
	}
}

func TestScanCodeSearchFileRejectsSymlinkAfterEnumeration(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("SearchNeedle outside project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "swapped.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	matches := scanCodeSearchFile(
		context.Background(),
		root,
		"swapped.txt",
		"text",
		buildCodeSearchNeedles("SearchNeedle"),
		0,
	)
	if len(matches) != 0 {
		t.Fatalf("symlink target outside project must not be searched: %#v", matches)
	}
}

func TestSearchCodeFallsBackWhenRipgrepIsUnavailable(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "src/fallback.go", "package fallback\n\nfunc BuiltinNeedle() {}\n")
	previous := findCodeSearchExecutable
	findCodeSearchExecutable = func(string) (string, error) {
		return "", errors.New("not found")
	}
	t.Cleanup(func() { findCodeSearchExecutable = previous })

	result, err := searchCode(root, "", "BuiltinNeedle", "auto", 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Engine != "builtin-parallel" {
		t.Fatalf("expected built-in fallback, got %q", result.Engine)
	}
	if len(result.Results) == 0 || result.Results[0].RelativePath != "src/fallback.go" {
		t.Fatalf("expected fallback content result, got %#v", result.Results)
	}
}

func TestSearchCodeValidatesInputAndLimitsResults(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 8; index++ {
		writeCodeSearchFixture(t, root, filepath.Join("src", "match-"+string(rune('a'+index))+".ts"), "const SharedNeedle = true;\n")
	}

	result, err := searchCode(root, "", "SharedNeedle", "text", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected result limit to apply, got %d", len(result.Results))
	}
	if _, err := searchCode(root, "../outside", "needle", "auto", 10); err == nil {
		t.Fatal("expected scope traversal to be rejected")
	}
	if _, err := searchCode(root, "", "", "auto", 10); err == nil {
		t.Fatal("expected empty query to be rejected")
	}
	if _, err := searchCode(root, "", "needle", "semantic", 10); err == nil {
		t.Fatal("expected unsupported mode to be rejected")
	}
}

func TestSearchCodeTextModeUsesLiteralFullQuery(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "src/partial.ts", "const alpha = true;\n")
	writeCodeSearchFixture(t, root, "src/full.ts", "const alpha beta = true;\n")

	result, err := searchCode(root, "", "alpha beta", "text", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].RelativePath != "src/full.ts" {
		t.Fatalf("text mode must only return literal full-query matches: %#v", result.Results)
	}
}

func TestSearchCodeUsesMonacoUTF16ColumnsAndAlignedSnippet(t *testing.T) {
	root := t.TempDir()
	line := "  😀 needle value\n"
	writeCodeSearchFixture(t, root, "src/unicode.ts", line)
	previous := findCodeSearchExecutable
	findCodeSearchExecutable = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { findCodeSearchExecutable = previous })

	result, err := searchCode(root, "", "needle", "text", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected one match: %#v", result.Results)
	}
	match := result.Results[0]
	if match.Column != 6 { // two spaces + one astral Unicode rune (two UTF-16 units) + space, 1-based
		t.Fatalf("column=%d, want Monaco UTF-16 column 6", match.Column)
	}
	if match.LineText != strings.TrimSuffix(line, "\n") {
		t.Fatalf("snippet must preserve source positions: %q", match.LineText)
	}
}

func TestCodeSearchCancelCommandCancelsRequestContext(t *testing.T) {
	ctx, cleanup := registerCodeSearch(context.Background(), "cancel-test")
	t.Cleanup(cleanup)
	args, err := json.Marshal(map[string]string{"requestId": "cancel-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Dispatch("local_fs_cancel_code_search", string(args)); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context was not cancelled: %v", ctx.Err())
	}
}

func TestCodeSearchCancelRequiresRequestID(t *testing.T) {
	if _, err := core.Dispatch("local_fs_cancel_code_search", "{}"); err == nil {
		t.Fatal("expected missing requestId error")
	}
}

type manuallyExpiredContext struct {
	done chan struct{}
}

func (ctx *manuallyExpiredContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *manuallyExpiredContext) Done() <-chan struct{}       { return ctx.done }
func (ctx *manuallyExpiredContext) Err() error {
	select {
	case <-ctx.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (ctx *manuallyExpiredContext) Value(key any) any { return nil }

func TestSearchCodeReturnsPartialResultsWhenTheBudgetExpires(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "src/slow.ts", "export const SearchNeedle = true;\n")

	// Reports one match, then writes more than a normal OS pipe can buffer before
	// signalling readiness. Seeing the marker therefore proves the scanner consumed
	// the first event; the test can expire the budget deterministically without a
	// wall-clock race against process startup on a loaded CI worker.
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "rg-stub")
	ready := filepath.Join(stubDir, "ready")
	shellReady := "'" + strings.ReplaceAll(ready, "'", "'\"'\"'") + "'"
	script := "#!/bin/sh\nprintf '%s\\n' '" +
		`{"type":"match","data":{"path":{"text":"src/slow.ts"},` +
		`"lines":{"text":"export const SearchNeedle = true;"},` +
		`"line_number":1,"submatches":[{"start":13,"end":25}]}}` +
		"'\ndd if=/dev/zero bs=1572864 count=1 2>/dev/null\n: > " + shellReady + "\nexec sleep 120\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := findCodeSearchExecutable
	findCodeSearchExecutable = func(string) (string, error) { return stub, nil }
	t.Cleanup(func() { findCodeSearchExecutable = previous })
	// The test expires the budget itself through ctx. Lift the built-in 5 s
	// budget so a slow stub start under a parallel go test ./... cannot win.
	previousTimeout := codeSearchTimeout
	codeSearchTimeout = 2 * time.Minute
	t.Cleanup(func() { codeSearchTimeout = previousTimeout })

	ctx := &manuallyExpiredContext{done: make(chan struct{})}
	type searchOutcome struct {
		result localFsCodeSearchResult
		err    error
	}
	outcome := make(chan searchOutcome, 1)
	go func() {
		result, err := searchCodeContext(ctx, root, "", "SearchNeedle", "text", 20)
		outcome <- searchOutcome{result: result, err: err}
	}()

	// Generous: only bounds a broken stub. A loaded machine (go test ./... runs
	// packages in parallel) can take several seconds to start the shell script.
	readyDeadline := time.NewTimer(60 * time.Second)
	defer readyDeadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
waitForReady:
	for {
		select {
		case early := <-outcome:
			t.Fatalf("search returned before the deadline was expired: %#v, %v", early.result, early.err)
		case <-poll.C:
			if _, err := os.Stat(ready); err == nil {
				break waitForReady
			}
		case <-readyDeadline.C:
			close(ctx.done)
			t.Fatal("stub did not publish its pre-deadline match")
		}
	}
	close(ctx.done)

	var completed searchOutcome
	select {
	case completed = <-outcome:
	case <-time.After(15 * time.Second):
		t.Fatal("search did not stop after its budget expired")
	}
	if completed.err != nil {
		t.Fatalf("an exhausted budget must not fail the search: %v", completed.err)
	}
	if !completed.result.Truncated {
		t.Fatal("a partial result must be reported as truncated")
	}
	if len(completed.result.Results) != 1 || completed.result.Results[0].RelativePath != "src/slow.ts" {
		t.Fatalf("expected the match found before the deadline: %#v", completed.result.Results)
	}

	// A cancel is different: nobody is waiting for the answer, so it still fails.
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := searchCodeContext(cancelled, root, "", "SearchNeedle", "text", 20); err == nil {
		t.Fatal("a cancelled request must fail rather than return a partial result")
	}
}

func TestSearchCodeSkipsPackageManagerCaches(t *testing.T) {
	root := t.TempDir()
	writeCodeSearchFixture(t, root, "src/visible.ts", "export const SearchNeedle = true;\n")
	// A pnpm store holds a copy of every package the machine has ever downloaded, so
	// it out-matches the project itself by orders of magnitude and buries the real
	// results. ripgrep is told to skip it; the walk and the per-match filter must too.
	writeCodeSearchFixture(t, root, ".pnpm-store/v10/files/00/abcdef", "const SearchNeedle = true;\n")
	writeCodeSearchFixture(t, root, ".turbo/cache/build.log", "SearchNeedle\n")

	for _, engine := range []string{"ripgrep", "builtin"} {
		t.Run(engine, func(t *testing.T) {
			if engine == "builtin" {
				previous := findCodeSearchExecutable
				findCodeSearchExecutable = func(string) (string, error) {
					return "", errors.New("not found")
				}
				t.Cleanup(func() { findCodeSearchExecutable = previous })
			}
			result, err := searchCode(root, "", "SearchNeedle", "text", 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Results) == 0 {
				t.Fatal("expected the project source match")
			}
			for _, match := range result.Results {
				if match.RelativePath != "src/visible.ts" {
					t.Fatalf("a tool cache blob reached the results: %#v", match)
				}
			}
		})
	}
}

func writeCodeSearchFixture(t *testing.T, root, relativePath, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
