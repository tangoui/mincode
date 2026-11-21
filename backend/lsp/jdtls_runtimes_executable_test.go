package lsp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestJavaRuntimesSkipNonExecutableBeforeDedup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX execute bits")
	}
	bad, good := fakeJDK(t, "25"), fakeJDK(t, "25")
	if err := os.Chmod(javaOf(bad), 0644); err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(good)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidates := range [][]string{{javaOf(good)}, {javaOf(bad), javaOf(good)}} {
		got := javaRuntimes(bad, candidates)
		if len(got) != 1 || got[0].Path != expected || !got[0].Default {
			t.Fatalf("bad JAVA_HOME shadowed executable JDK: %+v", got)
		}
	}
	if got := javaRuntimes(bad, nil); len(got) != 0 {
		t.Fatalf("nonexecutable runtime listed: %+v", got)
	}
}
