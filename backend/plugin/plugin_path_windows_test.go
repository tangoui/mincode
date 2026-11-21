//go:build windows

package plugin

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// junctionOrSkip creates a directory junction (mount point reparse point),
// which unprivileged Windows users can create without developer mode.
func junctionOrSkip(t *testing.T, target, link string) {
	t.Helper()
	output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("junctions are unavailable on this host: %v: %s", err, output)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if !pluginPathIsLink(info) {
		t.Fatalf("junction %s was not detected as a reparse point (mode %s)", link, info.Mode())
	}
}

func TestPluginPathResolverWindowsJunctions(t *testing.T) {
	root := t.TempDir()
	writePluginPathFile(t, root, "assets/nested/deep.svg")
	outside := t.TempDir()
	writePluginPathFile(t, outside, "outside.svg")
	junctionOrSkip(t, filepath.Join(root, "assets", "nested"), filepath.Join(root, "assets", "inside-junction"))
	junctionOrSkip(t, outside, filepath.Join(root, "assets", "outside-junction"))
	realRoot, err := pluginRealPath(root)
	if err != nil {
		t.Fatal(err)
	}

	resolver := newPluginPathResolver(root)
	if _, _, err := resolver.resolve(filepath.FromSlash("assets/nested/deep.svg")); err != nil {
		t.Fatal(err)
	}
	// Junctions resolving inside the package are followed (filepath.EvalSymlinks
	// cannot see through them; the opened file's final path can).
	for _, relative := range []string{"assets/inside-junction/deep.svg", "Assets/INSIDE-JUNCTION/deep.svg"} {
		resolved, info, err := resolver.resolve(filepath.FromSlash(relative))
		if err != nil {
			t.Fatalf("resolve %s: a junction staying inside must be accepted: %v", relative, err)
		}
		if want := filepath.Join(realRoot, "assets", "nested", "deep.svg"); resolved != want || !info.Mode().IsRegular() {
			t.Fatalf("resolve %s = %s (%s), want %s", relative, resolved, info.Mode(), want)
		}
	}
	if _, info, err := resolver.resolve(filepath.FromSlash("assets/inside-junction")); err != nil || !info.IsDir() {
		t.Fatalf("an inside junction leaf: %v %v", info, err)
	}
	// Junctions resolving outside are rejected, including the junction leaf
	// itself, which filepath.EvalSymlinks would return unchanged.
	for _, relative := range []string{
		"assets/outside-junction/outside.svg", "assets/outside-junction",
		"ASSETS/Outside-Junction/OUTSIDE.SVG",
	} {
		if _, _, err := resolver.resolve(filepath.FromSlash(relative)); !errors.Is(err, errPluginPathLink) {
			t.Fatalf("resolve %s: expected junction escape rejection, got %v", relative, err)
		}
	}
	if _, err := resolvePluginPath(root, filepath.FromSlash("assets/outside-junction/outside.svg")); !errors.Is(err, errPluginPathLink) {
		t.Fatalf("resolvePluginPath through an outside junction: %v", err)
	}
	if _, err := resolvePluginPath(root, filepath.FromSlash("assets/outside-junction")); !errors.Is(err, errPluginPathLink) {
		t.Fatalf("resolvePluginPath of an outside junction leaf (e.g. a shell cwd): %v", err)
	}
	if got, err := resolvePluginPath(root, filepath.FromSlash("assets/inside-junction/deep.svg")); err != nil || !strings.EqualFold(got, filepath.Join(realRoot, "assets", "nested", "deep.svg")) {
		t.Fatalf("resolvePluginPath through an inside junction = %q, %v", got, err)
	}
}

// fakeReparseInfo reports an entry with FILE_ATTRIBUTE_REPARSE_POINT, as a
// OneDrive placeholder or a Dedup file would, without changing its mode.
type fakeReparseInfo struct {
	fs.FileInfo
	data *syscall.Win32FileAttributeData
}

func (info fakeReparseInfo) Sys() any { return info.data }

func TestPluginPathResolverWindowsReparseFileUsesFallback(t *testing.T) {
	root := t.TempDir()
	writePluginPathFile(t, root, "assets/cloud.svg")
	injectPluginPathLstat(t, func(info fs.FileInfo) fs.FileInfo {
		data := *info.Sys().(*syscall.Win32FileAttributeData)
		data.FileAttributes |= syscall.FILE_ATTRIBUTE_REPARSE_POINT
		return fakeReparseInfo{FileInfo: info, data: &data}
	}, "cloud.svg")
	resolver := newPluginPathResolver(root)
	resolved, info, err := resolver.resolve(filepath.FromSlash("assets/cloud.svg"))
	if err != nil {
		t.Fatalf("a reparse-point file inside the package must be accepted through the fallback: %v", err)
	}
	realRoot, err := pluginRealPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != filepath.Join(realRoot, "assets", "cloud.svg") || !info.Mode().IsRegular() {
		t.Fatalf("fallback result %s (%s)", resolved, info.Mode())
	}
}

func TestPluginPathResolverAllowsJunctionPackageRoot(t *testing.T) {
	real := t.TempDir()
	writePluginPathFile(t, real, "ui/index.html")
	link := filepath.Join(t.TempDir(), "junction-plugin")
	junctionOrSkip(t, real, link)
	if _, _, err := newPluginPathResolver(link).resolve(filepath.FromSlash("ui/index.html")); err != nil {
		t.Fatalf("a junctioned package root must stay supported like a linked one: %v", err)
	}
}

func TestPluginPathResolverCaseInsensitivePaths(t *testing.T) {
	root := t.TempDir()
	writePluginPathFile(t, root, "Assets/Icons/File.svg")
	// Root spelled with a different case than on disk.
	upperRoot := strings.ToUpper(root)
	resolver := newPluginPathResolver(upperRoot)
	resolved, info, err := resolver.resolve(filepath.FromSlash("assets/icons/file.SVG"))
	if err != nil {
		t.Fatalf("case-insensitive resolve: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("expected a regular file, got %s", info.Mode())
	}
	if data, err := os.ReadFile(resolved); err != nil || string(data) != "Assets/Icons/File.svg" {
		t.Fatalf("read %s: %q %v", resolved, data, err)
	}
	// A differently cased sibling spelling also resolves (cache miss, one more Lstat).
	if _, _, err := resolver.resolve(filepath.FromSlash("ASSETS/ICONS/FILE.svg")); err != nil {
		t.Fatalf("second spelling: %v", err)
	}
	// resolvePluginPath keeps the historical canonical (on-disk) spelling, which
	// persisted launch fingerprints depend on.
	canonical, err := resolvePluginPath(upperRoot, filepath.FromSlash("assets/icons/file.SVG"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "Assets", "Icons", "File.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if canonical != want {
		t.Fatalf("resolvePluginPath = %s, want canonical %s", canonical, want)
	}
	// Traversal with mixed case is still lexical.
	if _, _, err := resolver.resolve(filepath.FromSlash("ASSETS/../../outside.svg")); err == nil {
		t.Fatal("expected traversal through the parent directory to be rejected")
	}
}

// EvalSymlinks does not follow junctions on Windows, so a ui/ junction used to
// be trusted as a root: view entries and served assets came from wherever it
// pointed. Both are now checked from the package root.
func TestPluginUIJunctionOutsidePackageIsRejected(t *testing.T) {
	outside := t.TempDir()
	writePluginPathFile(t, outside, "index.html")
	pkg := t.TempDir()
	writePluginPathFile(t, pkg, "plugin.json")
	junctionOrSkip(t, outside, filepath.Join(pkg, "ui"))
	if _, _, err := newPluginPathResolver(pkg).resolveUnder("ui", "index.html"); !errors.Is(err, errPluginPathLink) {
		t.Fatalf("view entry through an outside ui/ junction: %v", err)
	}
	if _, err := resolvePluginSubdirPath(pkg, "ui", "index.html"); !errors.Is(err, errPluginPathLink) {
		t.Fatalf("served asset through an outside ui/ junction: %v", err)
	}

	inside := t.TempDir()
	writePluginPathFile(t, inside, "dist/index.html")
	junctionOrSkip(t, filepath.Join(inside, "dist"), filepath.Join(inside, "ui"))
	if _, err := resolvePluginSubdirPath(inside, "ui", "index.html"); err != nil {
		t.Fatalf("ui/ junction inside the package: %v", err)
	}
}
