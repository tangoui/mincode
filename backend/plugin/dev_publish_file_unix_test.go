//go:build !windows

package plugin

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPluginDevPublishRejectsFIFO(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	path := in.Packages[0].Path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	id := devCreate(t, b, in)
	if status := devStatus(t, b, id); status.Status != "failed" || p.plans.Load() != 0 {
		t.Fatalf("%+v plans=%d", status, p.plans.Load())
	}
}
func TestPluginDevPublishRejectsAncestorSymlinkEscape(t *testing.T) {
	p := &devTestPublisher{}
	b := devPublishBridge(t, p)
	in := devPublishInput(t)
	outside := t.TempDir()
	link := filepath.Join(in.ReleaseDir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	in.Packages[0].Path = filepath.Join(link, "secret.min")
	id := devCreate(t, b, in)
	if status := devStatus(t, b, id); status.Status != "failed" || p.plans.Load() != 0 {
		t.Fatalf("%+v plans=%d", status, p.plans.Load())
	}
}

func TestPluginDevPublishRejectsRootDirectorySubstitution(t *testing.T) {
	project := t.TempDir()
	root := filepath.Join(project, "release")
	outside := t.TempDir()
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(outside, "secret.min"), []byte("outside bytes"), 0600)
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inspectPublishFile(root, filepath.Join(root, "secret.min"), 1024, ""); err == nil {
		t.Fatal("substituted release directory allowed outside read")
	}
}

func TestPluginDevPublishRejectsProjectAncestorReplacementBeforeConfirm(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_hash", true: "different_hash"}[different], func(t *testing.T) {
			in := devPublishInput(t)
			holder := t.TempDir()
			ancestor := filepath.Join(holder, "work")
			os.Mkdir(ancestor, 0700)
			inside := filepath.Join(ancestor, "project")
			if err := os.Rename(in.ProjectRoot, inside); err != nil {
				t.Fatal(err)
			}
			in.ProjectRoot = inside
			in.ReleaseDir = filepath.Join(inside, "release")
			external := t.TempDir()
			outsideRelease := filepath.Join(external, "project", "release")
			os.MkdirAll(outsideRelease, 0700)
			for i := range in.Packages {
				in.Packages[i].Path = filepath.Join(in.ReleaseDir, filepath.Base(in.Packages[i].Path))
				data, err := os.ReadFile(in.Packages[i].Path)
				if err != nil {
					t.Fatal(err)
				}
				if different {
					data = []byte("unapproved external bytes")
				}
				os.WriteFile(filepath.Join(outsideRelease, filepath.Base(in.Packages[i].Path)), data, 0600)
			}
			p := &devTestPublisher{}
			b := devPublishBridge(t, p)
			id := devCreate(t, b, in)
			b.mu.Lock()
			roots := b.publishes[id].roots
			b.mu.Unlock()
			approved, err := roots.releaseTree.Stat(".")
			if err != nil {
				t.Fatal(err)
			}
			os.Rename(ancestor, ancestor+"-original")
			os.Symlink(external, ancestor)
			current, err := os.Stat(in.ReleaseDir)
			if err != nil || os.SameFile(approved, current) {
				t.Fatal("fixture did not replace original root")
			}
			pinned, err := roots.releaseTree.Stat(".")
			if err != nil || !os.SameFile(approved, pinned) {
				t.Fatal("original capability not retained across Plan")
			}
			// This guard runs before opening even the first package/cover; both hash cases must fail here.
			if roots.verify() == nil {
				t.Fatal("root identity guard accepted an external replacement")
			}
			if b.ConfirmPublish(id) == nil {
				t.Fatal("confirmed substituted ancestor")
			}
			if p.runs.Load() != 0 {
				t.Fatal("publisher received outside tree")
			}
		})
	}
}
