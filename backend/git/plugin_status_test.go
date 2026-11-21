package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real porcelain-v2 rename metadata must not become part of the path: doing
// so would bypass a sensitive-name filter before digesting or counting.
func TestStatusEventsRenameParser(t *testing.T) {
	st := parsePorcelainV2("2 RM N... 100644 100644 100644 a b R100 .env\x00public.txt\x00")
	if len(st.Staged) != 1 || st.Staged[0].Path != ".env" || st.Staged[0].OldPath == nil || *st.Staged[0].OldPath != "public.txt" {
		t.Fatalf("rename metadata corrupted path: %+v", st)
	}
	if len(st.Unstaged) != 1 || st.Unstaged[0].OldPath == nil || *st.Unstaged[0].OldPath != "public.txt" {
		t.Fatalf("unstaged rename lost other side: %+v", st.Unstaged)
	}
}

func TestStatusEventsFilterAndDigest(t *testing.T) {
	allow := func(p string) bool { return !testDeniedPath(p) }
	ordinary := func(xy, path string) string { return "1 " + xy + " N... 100644 100644 100644 a b " + path + "\x00" }
	rename := func(old, next string) string {
		return "2 RM N... 100644 100644 100644 a b R100 " + next + "\x00" + old + "\x00"
	}
	conflict := func(xy string) string { return "u " + xy + " N... 100644 100644 100644 100644 a b c conflict.txt\x00" }
	base := ordinary("MM", "a.txt") + "? new.txt\x00" + conflict("UU")
	snapshot := pluginStatusDigest([]byte(base), allow)
	if snapshot.Staged != 1 || snapshot.Unstaged != 1 || snapshot.Untracked != 1 || snapshot.Conflicted != 1 {
		t.Fatalf("counts: %+v", snapshot)
	}
	for _, extra := range []string{ordinary("MM", ".env"), "? .git/config\x00", rename(".env", "public.txt"), rename("public.txt", ".env"), rename(".git/secret", "public.txt")} {
		if got := pluginStatusDigest([]byte(base+extra), allow); got != snapshot {
			t.Fatalf("hidden side affected count/digest: %+v", got)
		}
	}
	if got := pluginStatusDigest([]byte(conflict("UU")+"? new.txt\x00"+ordinary("MM", "a.txt")), allow); got != snapshot {
		t.Fatal("ordering alone changed digest")
	}
	if got := pluginStatusDigest([]byte(strings.Replace(base, "u UU ", "u AA ", 1)), allow); got.Digest == snapshot.Digest {
		t.Fatal("different conflict XY status did not change digest")
	}
	if got := pluginStatusDigest([]byte(ordinary("MM", " other.txt ")), func(string) bool { return true }); got.Staged != 1 || got.Unstaged != 1 {
		t.Fatal("ordinary path with spaces lost")
	}
}

func TestStatusEventsSampleRealRepository(t *testing.T) {
	root := stagedTestRepository(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	allow := func(p string) bool { return !testDeniedPath(p) }
	clean, err := SamplePluginStatus(context.Background(), root, allow)
	if err != nil {
		t.Fatal(err)
	}
	writeStagedTestFile(t, root, ".env", "SECRET=not observable\n")
	hidden, err := SamplePluginStatus(context.Background(), root, allow)
	if err != nil || hidden != clean {
		t.Fatalf("hidden untracked changed snapshot: %+v %v", hidden, err)
	}
	writeStagedTestFile(t, root, "README.md", "first edit\n")
	first, err := SamplePluginStatus(context.Background(), root, allow)
	if err != nil || first.Unstaged != 1 || first.Digest == clean.Digest {
		t.Fatalf("first edit: %+v %v", first, err)
	}
	writeStagedTestFile(t, root, "README.md", "second content edit\n")
	second, err := SamplePluginStatus(context.Background(), root, allow)
	if err != nil || second != first {
		t.Fatalf("status digest unexpectedly promises content edits: %+v %v", second, err)
	}
	runGitTestCommand(t, root, "add", "README.md")
	staged, err := SamplePluginStatus(context.Background(), root, allow)
	if err != nil || staged.Staged != 1 || staged.Unstaged != 0 {
		t.Fatalf("staged: %+v %v", staged, err)
	}
	missing := t.TempDir()
	missing, _ = filepath.EvalSymlinks(missing)
	if got, err := SamplePluginStatus(context.Background(), missing, allow); err == nil || got.Digest != "" {
		t.Fatal("non-repository became clean")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := SamplePluginStatus(ctx, root, allow); !errors.Is(err, context.Canceled) || got.Digest != "" {
		t.Fatalf("cancel became clean: %+v %v", got, err)
	}
}

// A real child writes exactly the bound, bound+1, or waits to be cancelled.
// This is not system sleep and never needs a user to interrupt the process.
func TestStatusEventsChild(t *testing.T) {
	mode := os.Getenv("MINCODE_STATUS_CHILD")
	if mode == "" {
		return
	}
	switch mode {
	case "pipeDescendant", "overflowDescendant", "cancelDescendant", "heartbeat":
		statusDescendantHelper(mode)
		os.Exit(0)
	case "bound":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'x'}, 4*1024*1024))
		os.Exit(0)
	case "overflow":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'x'}, 4*1024*1024+1))
	case "error":
		_, _ = os.Stderr.Write([]byte("SECRET RAW GIT OUTPUT"))
		os.Exit(2)
	}
	<-time.NewTimer(time.Minute).C
	os.Exit(0)
}
func statusChild(mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestStatusEventsChild$")
	cmd.Env = append(os.Environ(), "MINCODE_STATUS_CHILD="+mode)
	return cmd
}
func TestStatusEventsCommandHardLimits(t *testing.T) {
	t.Run("exact4MiB", func(t *testing.T) {
		raw, err := runPluginStatusCommand(context.Background(), statusChild("bound"))
		if err != nil || len(raw) != 4*1024*1024 {
			t.Fatalf("exact bound failed: %d %v", len(raw), err)
		}
	})
	t.Run("4MiBPlusOneKills", func(t *testing.T) {
		cmd := statusChild("overflow")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		start := time.Now()
		raw, err := runPluginStatusCommand(ctx, cmd)
		if !errors.Is(err, errPluginStatusOverflow) || len(raw) != 0 || time.Since(start) > 4*time.Second || cmd.ProcessState == nil || cmd.ProcessState.Success() {
			t.Fatalf("overflow was not killed/bounded: %d %v %v", len(raw), err, cmd.ProcessState)
		}
	})
	t.Run("fiveSecondDeadline", func(t *testing.T) {
		cmd := statusChild("stall")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		start := time.Now()
		raw, err := runPluginStatusCommand(ctx, cmd)
		elapsed := time.Since(start)
		if !errors.Is(err, context.DeadlineExceeded) || len(raw) != 0 || elapsed < 4400*time.Millisecond || elapsed > 5*time.Second || cmd.ProcessState == nil {
			t.Fatalf("deadline not enforced: %v %v", elapsed, err)
		}
	})
	t.Run("errorNotClean", func(t *testing.T) {
		raw, err := runPluginStatusCommand(context.Background(), statusChild("error"))
		if err == nil || len(raw) != 0 || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("error became clean or leaked: %s %v", raw, err)
		}
	})
}

// Preparing the index must not execute the driver. A real unsafe status is a
// positive control; reset its marker before testing the background sampler.
func TestStatusEventsNoFilterExecution(t *testing.T) {
	for _, kind := range []string{"clean", "process"} {
		t.Run(kind, func(t *testing.T) {
			root := stagedTestRepository(t)
			root, _ = filepath.EvalSymlinks(root)
			writeStagedTestFile(t, root, ".gitattributes", ".env filter=marker-driver\n")
			writeStagedTestFile(t, root, ".env", "seed\n")
			runGitTestCommand(t, root, "add", ".gitattributes", ".env")
			runGitTestCommand(t, root, "commit", "-m", "prepare without filter commands")
			marker := filepath.Join(t.TempDir(), "executed")
			command := "printf marker > '" + marker + "'; cat"
			if kind == "process" {
				command = "printf marker > '" + marker + "'; exit 1"
			}
			runGitTestCommand(t, root, "config", "filter.marker-driver."+kind, command)
			writeStagedTestFile(t, root, ".env", "edit\n")
			runGitTestCommand(t, root, "status", "--porcelain=v2", "--untracked-files=all")
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("fixture did not execute the real %s filter: %v", kind, err)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			writeStagedTestFile(t, root, ".env", "next\n")
			_, _ = SamplePluginStatus(context.Background(), root, func(p string) bool { return !testDeniedPath(p) })
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("background git status executed configured %s filter; marker stat: %v", kind, err)
			}
		})
	}
}

func TestStatusEventsConfigurationBudget(t *testing.T) {
	root := stagedTestRepository(t)
	root, _ = filepath.EvalSymlinks(root)
	runner := &pluginStatusGitRunner{remaining: 32}
	raw, err := runner.run(context.Background(), root, "rev-parse", "--is-inside-work-tree")
	if err != nil || string(raw) != "true\n" || runner.remaining != 27 {
		t.Fatalf("probe did not consume the common budget: %q %d %v", raw, runner.remaining, err)
	}
	if raw, err = runner.run(context.Background(), root, "config", "--includes", "--null", "--name-only", "--list"); !errors.Is(err, errPluginStatusOverflow) || len(raw) != 0 {
		t.Fatalf("configuration bypassed remaining budget: %d %v", len(raw), err)
	}
	// A configuration probe is subject to the actual 4 MiB sample cap, too.
	config := filepath.Join(root, ".git", "config")
	file, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n[filter \"" + strings.Repeat("a", 4*1024*1024) + "\"]\n\tclean = false\n")
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	snapshot, err := SamplePluginStatus(context.Background(), root, func(string) bool { return true })
	if !errors.Is(err, errPluginStatusOverflow) || snapshot.Digest != "" {
		t.Fatalf("oversized configuration became clean or escaped the shared output limit: %+v %v", snapshot, err)
	}
}

func TestStatusEventsEnvironmentAndDriverNames(t *testing.T) {
	root := stagedTestRepository(t)
	root, _ = filepath.EvalSymlinks(root)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	t.Setenv("GIT_TRACE2_EVENT", trace+"-event")
	t.Setenv("GIT_EXTERNAL_DIFF", "must-not-run")
	if _, err := SamplePluginStatus(context.Background(), root, func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{trace, trace + "-event"} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("inherited Git environment wrote a trace: %s %v", name, err)
		}
	}
	// Git permits unusual subsection names. They are data, not shell fragments.
	injected := filepath.Join(t.TempDir(), "injected")
	driver := "odd;$(printf${IFS}owned>" + injected + ")"
	keys := []byte("filter." + driver + ".clean\x00filter." + driver + ".process\x00")
	args, err := pluginStatusFilterOverrides(keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 8 || args[0] != "-c" || args[1] != "filter."+driver+".clean=" {
		t.Fatalf("driver was split/interpreted: %#v", args)
	}
	args = append(args, "status", "--porcelain=v2", "-z", "--ignore-submodules=all")
	runner := &pluginStatusGitRunner{remaining: pluginStatusMaxBytes}
	if _, err = runner.run(context.Background(), root, args...); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(injected); !os.IsNotExist(err) {
		t.Fatalf("driver name executed shell syntax: %v", err)
	}
	if _, err := pluginStatusFilterOverrides([]byte("filter." + strings.Repeat("x", 17000) + ".clean\x00")); err == nil {
		t.Fatal("unbounded filter override command line accepted")
	}
}
