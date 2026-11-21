package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMergeEnvOverridesAndAppendsSorted(t *testing.T) {
	got := MergeEnv([]string{"A=1", "B=2", "C=3"}, map[string]string{"Z": "9", "B": "x"})
	if strings.Join(got, ",") != "A=1,C=3,B=x,Z=9" {
		t.Fatalf("MergeEnv = %v", got)
	}
	base := []string{"A=1"}
	if got := MergeEnv(base, nil); len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("MergeEnv with no extra = %v", got)
	}
}

func TestMergeEnvWindowsNamesCaseInsensitive(t *testing.T) {
	got := MergeEnv([]string{"Path=old"}, map[string]string{"PATH": "new"})
	want := "Path=old,PATH=new"
	if runtime.GOOS == "windows" {
		want = "PATH=new"
	}
	if strings.Join(got, ",") != want {
		t.Fatalf("MergeEnv = %v, want %s", got, want)
	}
}

func TestExpandedEnvKeepsUserPathFirst(t *testing.T) {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "custom-bin"))
	want := os.Getenv("PATH")
	found := false
	for _, entry := range ExpandedEnv() {
		if strings.HasPrefix(entry, "PATH=") {
			found = true
			if !strings.HasPrefix(entry, "PATH="+want) {
				t.Fatalf("user PATH priority changed: %s", entry)
			}
		}
	}
	if !found {
		t.Fatal("ExpandedEnv has no PATH entry")
	}
}
