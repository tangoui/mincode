package notes

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
)

func TestMain(m *testing.M) {
	// 测试直接调用 collectGarbage，不让 notes_tree 在后台触发回收；回收日志不输出到测试结果。
	assetCollectorEnabled = false
	assetLogf = func(string, ...any) {}
	os.Exit(m.Run())
}

var testRefPattern = regexp.MustCompile(`^/__notes-assets/[0-9a-f]{64}\.(png|jpg|gif|webp)$`)

func pngBytes(seed string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(seed)...)
}

func putViaCommand(t *testing.T, fileName string, data []byte) (string, error) {
	t.Helper()
	out, err := dispatch(t, "notes_asset_put", map[string]any{
		"fileName":   fileName,
		"dataBase64": base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return "", err
	}
	var result struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return result.Ref, nil
}

func mustPut(t *testing.T, data []byte) string {
	t.Helper()
	ref, err := putViaCommand(t, "image.png", data)
	if err != nil {
		t.Fatalf("notes_asset_put: %v", err)
	}
	if !testRefPattern.MatchString(ref) {
		t.Fatalf("ref = %q", ref)
	}
	return ref
}

func mustAssetsDir(t *testing.T) string {
	t.Helper()
	dir, err := assetsDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func assetFileNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(mustAssetsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func assetRowCount(t *testing.T) int {
	t.Helper()
	db, err := openNotesDB()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM note_assets`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// ageAsset 把附件的入库时间与文件修改时间都改到 at。
func ageAsset(t *testing.T, ref string, at time.Time) {
	t.Helper()
	name := strings.TrimPrefix(ref, assetRefPrefix)
	db, err := openNotesDB()
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.SplitN(name, ".", 2)[0]
	if _, err := db.Exec(`UPDATE note_assets SET created_at = ? WHERE hash = ?`, at.UnixMilli(), hash); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(mustAssetsDir(t), name), at, at); err != nil {
		t.Fatal(err)
	}
}

func assetExists(t *testing.T, ref string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(mustAssetsDir(t), strings.TrimPrefix(ref, assetRefPrefix)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestAssetPutDedupesByHash(t *testing.T) {
	setupNotesDB(t)
	data := pngBytes("same image")
	first := mustPut(t, data)
	second, err := putViaCommand(t, "renamed-copy.png", data)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasSuffix(first, ".png") {
		t.Fatalf("refs = %q, %q", first, second)
	}
	if names := assetFileNames(t); len(names) != 1 {
		t.Fatalf("asset files = %v, want exactly 1", names)
	}
	if count := assetRowCount(t); count != 1 {
		t.Fatalf("note_assets rows = %d, want 1", count)
	}
	stored, err := os.ReadFile(filepath.Join(mustAssetsDir(t), strings.TrimPrefix(first, assetRefPrefix)))
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("stored = %q, %v", stored, err)
	}

	// 扩展名取自检测到的类型，与 fileName 无关。
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("jfif")...)
	gif := []byte("GIF89a....")
	webp := []byte("RIFF\x10\x00\x00\x00WEBPVP8 ")
	for data, ext := range map[string]string{string(jpeg): ".jpg", string(gif): ".gif", "GIF87a..": ".gif", string(webp): ".webp"} {
		ref, err := putViaCommand(t, "photo.png", []byte(data))
		if err != nil || !strings.HasSuffix(ref, ext) || !testRefPattern.MatchString(ref) {
			t.Fatalf("put %q = %q, %v; want suffix %s", data, ref, err, ext)
		}
	}
}

func TestAssetPutRejectsSvgAndFakePng(t *testing.T) {
	setupNotesDB(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	for name, data := range map[string][]byte{
		"evil.png":   svg,
		"fake.png":   []byte("not really a png"),
		"empty.png":  {},
		"short.png":  []byte("\x89PN"),
		"riff.webp":  []byte("RIFF\x10\x00\x00\x00WAVEfmt "),
		"gif88.gif":  []byte("GIF88a...."),
		"almost.jpg": {0xFF, 0xD8},
	} {
		if _, err := putViaCommand(t, name, data); err == nil || err.Error() != ErrAssetType {
			t.Errorf("%s: err = %v, want %s", name, err, ErrAssetType)
		}
	}

	big := make([]byte, maxAssetBytes+1)
	copy(big, pngBytes(""))
	if _, err := putViaCommand(t, "big.png", big); err == nil || err.Error() != ErrAssetTooLarge {
		t.Fatalf("oversized put err = %v, want %s", err, ErrAssetTooLarge)
	}
	if _, err := putAsset("big.png", big); !errors.Is(err, errAssetTooLarge) {
		t.Fatalf("putAsset oversized err = %v", err)
	}
	// 请求体本身超过 base64 上限也报图片过大，而不是通用的请求过大。
	huge := strings.Repeat("A", maxAssetRequestBytes)
	if _, err := dispatchAssetPutRaw(`{"fileName":"x.png","dataBase64":"` + huge + `"}`); err == nil || err.Error() != ErrAssetTooLarge {
		t.Fatalf("oversized request err = %v, want %s", err, ErrAssetTooLarge)
	}
	if names, err := os.ReadDir(mustAssetsDir(t)); err == nil && len(names) != 0 {
		t.Fatalf("rejected puts left files: %v", names)
	}

	exact := make([]byte, maxAssetBytes)
	copy(exact, pngBytes(""))
	mustPut(t, exact)

	for _, raw := range []string{
		`{"fileName":"x.png","dataBase64":"***"}`,
		`{"fileName":"x.png"}`,
		`{"fileName":"x.png","dataBase64":"iVBORw0KGgo=","extra":1}`,
		`{"fileName":"a\nb.png","dataBase64":"iVBORw0KGgo="}`,
	} {
		if _, err := dispatchAssetPutRaw(raw); err == nil || err.Error() != ErrInvalidRequest {
			t.Errorf("%s: err = %v, want %s", raw, err, ErrInvalidRequest)
		}
	}
}

func TestAssetMiddlewareServesOnlyValidNames(t *testing.T) {
	setupNotesDB(t)
	data := pngBytes("served image")
	ref := mustPut(t, data)
	nextCalled := ""
	handler := AssetMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = r.URL.Path
		w.WriteHeader(http.StatusTeapot)
	}))
	serve := func(method, target string) *httptest.ResponseRecorder {
		nextCalled = ""
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
		return rec
	}

	rec := serve(http.MethodGet, ref)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("GET ref = %d %q", rec.Code, rec.Body.Bytes())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", got)
	}
	if nextCalled != "" {
		t.Errorf("next called for asset request")
	}
	if rec := serve(http.MethodHead, ref); rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("HEAD ref = %d len=%d type=%q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Type"))
	}
	if rec := serve(http.MethodPost, ref); rec.Code != http.StatusMethodNotAllowed || nextCalled != "" {
		t.Errorf("POST ref = %d next=%q", rec.Code, nextCalled)
	}

	// 写入工作目录外的文件，再在附件目录放一个指向它的合法文件名软链接。
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, pngBytes("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkName := strings.Repeat("ab", 32) + ".png"
	symlinkOK := os.Symlink(outside, filepath.Join(mustAssetsDir(t), linkName)) == nil

	notFound := []string{
		"/__notes-assets/../storage.sqlite3",
		"/__notes-assets/abc.png",
		"/__notes-assets/",
		"/__notes-assets",
		"/__notes-assets/" + strings.Repeat("0", 64) + ".png", // 格式合法但文件不存在
		"/__notes-assets/" + strings.ToUpper(strings.TrimPrefix(ref, "/__notes-assets/")), // 大写
		strings.TrimSuffix(ref, ".png") + ".svg",
		ref + "/x",
		"/__notes-assets/%2e%2e/storage.sqlite3",
	}
	if symlinkOK {
		notFound = append(notFound, "/__notes-assets/"+linkName)
	}
	for _, target := range notFound {
		if rec := serve(http.MethodGet, target); rec.Code != http.StatusNotFound || nextCalled != "" {
			t.Errorf("GET %s = %d next=%q, want 404 without next", target, rec.Code, nextCalled)
		}
	}

	for _, target := range []string{"/index.html", "/", "/assets/app.js", "/__notes-assetsX/a.png"} {
		if rec := serve(http.MethodGet, target); rec.Code != http.StatusTeapot || nextCalled != target {
			t.Errorf("GET %s = %d next=%q, want passthrough", target, rec.Code, nextCalled)
		}
	}
}

func TestGarbageCollectionKeepsReferencedAndRecent(t *testing.T) {
	setupNotesDB(t)
	now := time.Now()
	old := now.Add(-25 * time.Hour)

	referenced := mustPut(t, pngBytes("referenced"))
	recent := mustPut(t, pngBytes("recent, unreferenced"))
	stale := mustPut(t, pngBytes("stale, unreferenced"))
	ageAsset(t, referenced, old)
	ageAsset(t, stale, old)
	mustDispatch[Note](t, "notes_create", map[string]any{"title": "图", "body": "前文 ![截图](" + referenced + ") 后文"})

	dir := mustAssetsDir(t)
	// 中断写入遗留的临时文件：旧的删除，新的保留；无关文件不动。
	oldTemp := filepath.Join(dir, assetTempPrefix+"interrupted")
	newTemp := filepath.Join(dir, assetTempPrefix+"in-flight")
	unknown := filepath.Join(dir, "README.txt")
	for _, path := range []string{oldTemp, newTemp, unknown} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(oldTemp, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unknown, old, old); err != nil {
		t.Fatal(err)
	}
	// 文件已丢失的入库行被清掉。
	db, err := openNotesDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO note_assets(hash, ext, size, created_at) VALUES (?, 'png', 1, ?)`, strings.Repeat("c", 64), old.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	removed, err := collectGarbage(now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if !assetExists(t, referenced) || !assetExists(t, recent) || assetExists(t, stale) {
		t.Fatalf("after GC referenced=%v recent=%v stale=%v", assetExists(t, referenced), assetExists(t, recent), assetExists(t, stale))
	}
	if count := assetRowCount(t); count != 2 {
		t.Errorf("note_assets rows = %d, want 2", count)
	}
	for path, want := range map[string]bool{oldTemp: false, newTemp: true, unknown: true} {
		_, err := os.Lstat(path)
		if (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", filepath.Base(path), err == nil, want)
		}
	}

	// 再次运行没有可回收的内容。
	if removed, err := collectGarbage(now); err != nil || removed != 0 {
		t.Errorf("second GC = %d, %v", removed, err)
	}
}

func TestGarbageCollectionRespectsReputAndMissingDir(t *testing.T) {
	setupNotesDB(t)
	if removed, err := collectGarbage(time.Now()); err != nil || removed != 0 {
		t.Fatalf("GC without assets dir = %d, %v", removed, err)
	}
	data := pngBytes("pasted again")
	ref := mustPut(t, data)
	ageAsset(t, ref, time.Now().Add(-48*time.Hour))
	// 重新粘贴同一张旧图：宽限期重新计算，不能被立即回收。
	if again := mustPut(t, data); again != ref {
		t.Fatalf("re-put ref = %q, want %q", again, ref)
	}
	if removed, err := collectGarbage(time.Now()); err != nil || removed != 0 || !assetExists(t, ref) {
		t.Fatalf("GC after re-put = %d, %v, exists=%v", removed, err, assetExists(t, ref))
	}
	// 文件被删后再写入同一内容会重新落盘。
	if err := os.Remove(filepath.Join(mustAssetsDir(t), strings.TrimPrefix(ref, assetRefPrefix))); err != nil {
		t.Fatal(err)
	}
	if again := mustPut(t, data); again != ref || !assetExists(t, ref) {
		t.Fatalf("re-put after removal = %q exists=%v", again, assetExists(t, ref))
	}
}

func TestTreeStartsCollectorOnce(t *testing.T) {
	setupNotesDB(t)
	stale := mustPut(t, pngBytes("collected by tree"))
	ageAsset(t, stale, time.Now().Add(-25*time.Hour))

	// 包级 Once 每次测试重新开始，保证 -count>1 时可重复运行；日志改为捕获，作为后台回收完成的信号。
	prevLogf := assetLogf
	assetCollectorOnce = sync.Once{}
	logs := make(chan string, 8)
	assetLogf = func(format string, args ...any) { logs <- fmt.Sprintf(format, args...) }
	assetCollectorEnabled = true
	t.Cleanup(func() {
		assetCollectorEnabled = false
		assetLogf = prevLogf
		assetCollectorOnce = sync.Once{}
	})

	if _, err := dispatch(t, "notes_tree", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-logs:
		if msg != "notes: removed 1 unreferenced note assets" {
			t.Fatalf("collector log = %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not run")
	}
	if assetExists(t, stale) || assetRowCount(t) != 0 {
		t.Fatalf("after collector exists=%v rows=%d", assetExists(t, stale), assetRowCount(t))
	}

	// 同一进程内再次加载笔记树不会再触发回收：新的过期附件保留，也没有新的回收日志。
	second := mustPut(t, pngBytes("not collected twice"))
	ageAsset(t, second, time.Now().Add(-25*time.Hour))
	if _, err := dispatch(t, "notes_tree", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-logs:
		t.Fatalf("collector ran again: %q", msg)
	case <-time.After(300 * time.Millisecond):
	}
	if !assetExists(t, second) {
		t.Fatal("second notes_tree collected an asset")
	}
}

// dispatchAssetPutRaw 以原始 JSON 调用 notes_asset_put，便于构造非法或超大的请求体。
func dispatchAssetPutRaw(raw string) (string, error) {
	return core.Dispatch("notes_asset_put", raw)
}
