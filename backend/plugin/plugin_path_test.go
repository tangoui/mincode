package plugin

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writePluginPathFile(t *testing.T, root, relative string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(relative), 0o600); err != nil {
		t.Fatal(err)
	}
}

// symlinkOrSkip creates link -> target or skips when the host cannot create
// symlinks (unprivileged Windows without developer mode).
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable on this host: %v", err)
	}
}

func TestPluginPathResolverAcceptsPlainTreeAndCachesDirectories(t *testing.T) {
	root := t.TempDir()
	writePluginPathFile(t, root, "assets/icons/a.svg")
	writePluginPathFile(t, root, "assets/icons/b.svg")
	writePluginPathFile(t, root, "bin/run")
	resolver := newPluginPathResolver(root)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"assets/icons/a.svg", "assets/icons/b.svg", "assets/./icons/../icons/a.svg", "bin/run"} {
		resolved, info, err := resolver.resolve(filepath.FromSlash(relative))
		if err != nil {
			t.Fatalf("resolve %s: %v", relative, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("resolve %s: expected regular file info, got %s", relative, info.Mode())
		}
		if !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
			t.Fatalf("resolve %s: %s is not below %s", relative, resolved, resolvedRoot)
		}
		if data, err := os.ReadFile(resolved); err != nil || string(data) != filepath.ToSlash(filepath.Clean(relative)) {
			t.Fatalf("resolve %s: read %q, %v", relative, data, err)
		}
	}
	for _, dir := range []string{"assets", filepath.Join("assets", "icons"), "bin"} {
		if _, cached := resolver.dirs[dir]; !cached {
			t.Fatalf("expected verified directory %q in cache, got %v", dir, resolver.dirs)
		}
	}
	// The single-shot helper agrees with the shared resolver.
	single, err := resolvePluginPath(root, filepath.FromSlash("assets/icons/a.svg"))
	if err != nil || single != filepath.Join(resolvedRoot, "assets", "icons", "a.svg") {
		t.Fatalf("resolvePluginPath = %q, %v", single, err)
	}
	// Directories may be resolved too; callers enforce regular files.
	if _, info, err := resolver.resolve("assets"); err != nil || !info.IsDir() {
		t.Fatalf("resolve directory: %v %v", info, err)
	}
}

func TestPluginPathResolverRejectsTraversalAndInvalidInput(t *testing.T) {
	root := t.TempDir()
	writePluginPathFile(t, root, "ui/index.html")
	outside := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root))
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	resolver := newPluginPathResolver(root)
	cases := []string{
		".", "", "..", "../" + filepath.Base(outside), "ui/../../" + filepath.Base(outside),
		"ui/../..", "a\x00b", outside,
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, "C:ui/index.html", `\\server\share\x`)
	} else {
		cases = append(cases, "/etc/passwd")
	}
	for _, relative := range cases {
		if resolved, _, err := resolver.resolve(filepath.FromSlash(relative)); err == nil {
			t.Fatalf("resolve %q unexpectedly succeeded: %s", relative, resolved)
		}
	}
}

func TestPluginPathResolverMissingAndNonDirectoryComponents(t *testing.T) {
	root := t.TempDir()
	writePluginPathFile(t, root, "ui/index.html")
	resolver := newPluginPathResolver(root)
	if _, _, err := resolver.resolve(filepath.FromSlash("ui/missing.html")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing leaf: expected ErrNotExist, got %v", err)
	}
	if _, _, err := resolver.resolve(filepath.FromSlash("missing/index.html")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory: expected ErrNotExist, got %v", err)
	}
	if _, _, err := resolver.resolve(filepath.FromSlash("ui/index.html/child")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file used as directory: expected ErrNotExist, got %v", err)
	}
	if _, cached := resolver.dirs["missing"]; cached {
		t.Fatal("a missing directory must not be cached")
	}
}

func TestPluginPathResolverRootErrorIsReported(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	if _, _, err := newPluginPathResolver(root).resolve("plugin.json"); err == nil || !strings.Contains(err.Error(), "resolve plugin directory") {
		t.Fatalf("expected root resolution error, got %v", err)
	}
}

func TestPluginPathResolverFollowsLinksThatStayInside(t *testing.T) {
	root := t.TempDir()
	writePluginPathFile(t, root, "assets/real.svg")
	writePluginPathFile(t, root, "assets/nested/deep.svg")
	outsideDir := t.TempDir()
	writePluginPathFile(t, outsideDir, "outside.svg")

	symlinkOrSkip(t, filepath.Join(root, "assets", "real.svg"), filepath.Join(root, "assets", "inside-link.svg"))
	symlinkOrSkip(t, filepath.Join(outsideDir, "outside.svg"), filepath.Join(root, "assets", "outside-link.svg"))
	symlinkOrSkip(t, filepath.Join(root, "assets", "nested"), filepath.Join(root, "assets", "linked-inside"))
	symlinkOrSkip(t, outsideDir, filepath.Join(root, "linked-outside"))
	// A relative link climbing out and back in stays inside.
	symlinkOrSkip(t, filepath.Join("..", filepath.Base(root), "assets", "real.svg"), filepath.Join(root, "relative-inside.svg"))

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newPluginPathResolver(root)
	// Warm the cache with the real parent first: a cached parent must not let a
	// linked child skip the fallback.
	if _, _, err := resolver.resolve(filepath.FromSlash("assets/real.svg")); err != nil {
		t.Fatal(err)
	}
	accepted := map[string]string{
		"assets/inside-link.svg":        "assets/real.svg",
		"assets/linked-inside/deep.svg": "assets/nested/deep.svg",
		"relative-inside.svg":           "assets/real.svg",
	}
	for relative, target := range accepted {
		resolved, info, err := resolver.resolve(filepath.FromSlash(relative))
		if err != nil {
			t.Fatalf("resolve %s: a link staying inside the package must be accepted: %v", relative, err)
		}
		if want := filepath.Join(realRoot, filepath.FromSlash(target)); resolved != want {
			t.Fatalf("resolve %s = %s, want the real path %s", relative, resolved, want)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("resolve %s: expected the target's regular-file info, got %s", relative, info.Mode())
		}
	}
	if _, info, err := resolver.resolve(filepath.FromSlash("assets/linked-inside")); err != nil || !info.IsDir() {
		t.Fatalf("a directory link staying inside: %v %v", info, err)
	}
	for _, relative := range []string{
		"assets/outside-link.svg", "linked-outside/outside.svg", "linked-outside",
	} {
		_, _, err := resolver.resolve(filepath.FromSlash(relative))
		if !errors.Is(err, errPluginPathLink) || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("resolve %s: expected escape rejection, got %v", relative, err)
		}
	}
	if _, cached := resolver.dirs[filepath.Join("assets", "linked-inside")]; cached {
		t.Fatal("a linked directory must never be cached as verified")
	}
	if got, err := resolvePluginPath(root, filepath.FromSlash("assets/linked-inside/deep.svg")); err != nil || got != filepath.Join(realRoot, "assets", "nested", "deep.svg") {
		t.Fatalf("resolvePluginPath through an inside directory link = %q, %v", got, err)
	}
	if _, err := resolvePluginPath(root, filepath.FromSlash("linked-outside/outside.svg")); !errors.Is(err, errPluginPathLink) {
		t.Fatalf("resolvePluginPath through an outside directory link: %v", err)
	}
}

// fakeLinkInfo reports an otherwise regular entry as a link / reparse point.
type fakeLinkInfo struct{ fs.FileInfo }

func (info fakeLinkInfo) Mode() fs.FileMode { return info.FileInfo.Mode() | fs.ModeSymlink }

// injectPluginPathLstat makes pluginPathLstat report the entries whose path
// ends with one of suffixes as links, and counts every call.
func injectPluginPathLstat(t *testing.T, wrap func(fs.FileInfo) fs.FileInfo, suffixes ...string) *int {
	t.Helper()
	calls := 0
	previous := pluginPathLstat
	pluginPathLstat = func(path string) (fs.FileInfo, error) {
		calls++
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		for _, suffix := range suffixes {
			if strings.HasSuffix(path, filepath.FromSlash(suffix)) {
				return wrap(info), nil
			}
		}
		return info, nil
	}
	t.Cleanup(func() { pluginPathLstat = previous })
	return &calls
}

func TestPluginPathResolverReparsePointFallsBackToFullResolution(t *testing.T) {
	root := t.TempDir()
	writePluginPathFile(t, root, "assets/icons/placeholder.svg")
	writePluginPathFile(t, root, "assets/icons/plain.svg")
	injectPluginPathLstat(t, func(info fs.FileInfo) fs.FileInfo { return fakeLinkInfo{info} }, "placeholder.svg", "icons")
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newPluginPathResolver(root)
	for _, relative := range []string{"assets/icons/placeholder.svg", "assets/icons/plain.svg"} {
		resolved, info, err := resolver.resolve(filepath.FromSlash(relative))
		if err != nil {
			t.Fatalf("resolve %s: a reparse point resolving inside must be accepted: %v", relative, err)
		}
		if resolved != filepath.Join(realRoot, filepath.FromSlash(relative)) || !info.Mode().IsRegular() {
			t.Fatalf("resolve %s = %s (%s)", relative, resolved, info.Mode())
		}
	}
	if _, cached := resolver.dirs[filepath.Join("assets", "icons")]; cached {
		t.Fatal("a directory reported as a reparse point must not be cached")
	}
}

func TestPluginPathResolverPlainTreeUsesOneLstatPerSibling(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d"} {
		writePluginPathFile(t, root, "assets/icons/"+name+".svg")
	}
	calls := injectPluginPathLstat(t, func(info fs.FileInfo) fs.FileInfo { return info })
	resolver := newPluginPathResolver(root)
	for _, name := range []string{"a", "b", "c", "d"} {
		if _, _, err := resolver.resolve(filepath.FromSlash("assets/icons/" + name + ".svg")); err != nil {
			t.Fatal(err)
		}
	}
	// assets + assets/icons once, then one leaf each.
	if *calls != 2+4 {
		t.Fatalf("expected 6 Lstat calls on the cached fast path, got %d", *calls)
	}
}

func TestPluginPathResolverAllowsLinkedPackageRoot(t *testing.T) {
	real := t.TempDir()
	writePluginPathFile(t, real, "ui/index.html")
	link := filepath.Join(t.TempDir(), "linked-plugin")
	symlinkOrSkip(t, real, link)
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, err := newPluginPathResolver(link).resolve(filepath.FromSlash("ui/index.html"))
	if err != nil {
		t.Fatalf("linked package root must stay supported: %v", err)
	}
	if resolved != filepath.Join(resolvedReal, "ui", "index.html") {
		t.Fatalf("expected path below the resolved root, got %s", resolved)
	}
	// A ui/ directory that links outside the package is rejected for view
	// entries and served assets alike (it used to be trusted as a root).
	pkg := t.TempDir()
	writePluginPathFile(t, pkg, "plugin.json")
	symlinkOrSkip(t, filepath.Join(real, "ui"), filepath.Join(pkg, "ui"))
	paths := newPluginPathResolver(pkg)
	if _, _, err := paths.resolveUnder("ui", "index.html"); !errors.Is(err, errPluginPathLink) {
		t.Fatalf("a ui/ link pointing outside the package must be rejected, got %v", err)
	}
	if _, err := resolvePluginSubdirPath(pkg, "ui", "index.html"); !errors.Is(err, errPluginPathLink) {
		t.Fatalf("served ui/ assets must reject an outside ui/ link, got %v", err)
	}
	if _, _, err := paths.resolve(filepath.FromSlash("ui/index.html")); !errors.Is(err, errPluginPathLink) {
		t.Fatalf("a ui/ link pointing outside the package must be rejected by the root resolver, got %v", err)
	}
	// A ui/ link to a directory inside the package keeps working.
	inside := t.TempDir()
	writePluginPathFile(t, inside, "dist/index.html")
	symlinkOrSkip(t, filepath.Join(inside, "dist"), filepath.Join(inside, "ui"))
	if _, err := resolvePluginSubdirPath(inside, "ui", "index.html"); err != nil {
		t.Fatalf("ui/ link inside the package: %v", err)
	}
	// relative must stay lexically inside ui/.
	for _, escape := range []string{"../plugin.json", "..", ".", filepath.FromSlash("a/../../plugin.json")} {
		if _, _, err := paths.resolveUnder("ui", escape); err == nil {
			t.Fatalf("resolveUnder accepted %q", escape)
		}
	}
}
