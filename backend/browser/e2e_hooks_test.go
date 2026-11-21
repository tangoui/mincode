package browser

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// goListBrowser lists this package for macOS with cgo on (where the hooks
// would apply) under the given build tags.
func goListBrowser(t *testing.T, tags string) (goFiles, cgoFiles, cflags string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	command := exec.Command("go", "list", "-tags", tags, "-f", "{{join .GoFiles \" \"}}|{{join .CgoFiles \" \"}}|{{join .CgoCFLAGS \" \"}}", ".")
	command.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=1", "GOFLAGS=")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -tags %s: %v\n%s", tags, err, output)
	}
	parts := strings.SplitN(strings.TrimSpace(string(output)), "|", 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected go list output: %q", output)
	}
	return parts[0], parts[1], parts[2]
}

func hasWord(list, word string) bool {
	for _, item := range strings.Fields(list) {
		if item == word {
			return true
		}
	}
	return false
}

// 网页不能静默写文件：the automatic save-panel answer and file pick exist
// only in `-tags e2e` builds. A production build must not compile the hook file
// nor define MINCODE_BROWSER_E2E for overlay_darwin.m.
func TestProductionBuildExcludesE2ENativePanelHooks(t *testing.T) {
	for _, tags := range []string{"production", "production,nocoredrivers", ""} {
		goFiles, cgoFiles, cflags := goListBrowser(t, tags)
		if hasWord(goFiles, "e2e_hooks_e2e.go") || hasWord(cgoFiles, "e2e_hooks_e2e.go") {
			t.Fatalf("-tags %q build includes e2e_hooks_e2e.go", tags)
		}
		if !hasWord(goFiles, "e2e_hooks_off.go") {
			t.Fatalf("-tags %q build does not include e2e_hooks_off.go: %s", tags, goFiles)
		}
		if strings.Contains(cflags, "MINCODE_BROWSER_E2E") {
			t.Fatalf("-tags %q build defines MINCODE_BROWSER_E2E: %s", tags, cflags)
		}
	}
	// The check above would pass vacuously if the file were renamed; the e2e build must see it.
	goFiles, cgoFiles, cflags := goListBrowser(t, "e2e,nocoredrivers")
	if !hasWord(cgoFiles, "e2e_hooks_e2e.go") || hasWord(goFiles, "e2e_hooks_off.go") || !strings.Contains(cflags, "-DMINCODE_BROWSER_E2E") {
		t.Fatalf("e2e build does not compile the hooks: go=%s cgo=%s cflags=%s", goFiles, cgoFiles, cflags)
	}
	if e2eNativePanelHooksCompiled() {
		t.Fatal("this test binary was built with the e2e native panel hooks")
	}
}

// Every line of overlay_darwin.m that touches the e2e hooks sits inside an
// `#ifdef MINCODE_BROWSER_E2E` block, so without the e2e-only define there is
// no code path that answers a panel automatically.
func TestOverlayE2EHooksAreCompiledOutWithoutTheE2EDefine(t *testing.T) {
	file, err := os.Open("overlay_darwin.m")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	depth, inside, hookLines := 0, 0, 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(text, "#if"):
			depth++
			if text == "#ifdef MINCODE_BROWSER_E2E" {
				if inside != 0 {
					t.Fatalf("line %d: nested MINCODE_BROWSER_E2E block", line)
				}
				inside = depth
			}
			continue
		case strings.HasPrefix(text, "#else") || strings.HasPrefix(text, "#elif"):
			if inside == depth {
				t.Fatalf("line %d: MINCODE_BROWSER_E2E block must not have an #else branch", line)
			}
			continue
		case strings.HasPrefix(text, "#endif"):
			if inside == depth {
				inside = 0
			}
			depth--
			continue
		}
		if strings.Contains(text, "e2e_") || strings.Contains(text, "E2E") {
			hookLines++
			if inside == 0 && !strings.HasPrefix(text, "//") {
				t.Fatalf("line %d is outside #ifdef MINCODE_BROWSER_E2E: %s", line, text)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if hookLines == 0 {
		t.Fatal("found no e2e hook code in overlay_darwin.m; update this test with the hooks")
	}
}
