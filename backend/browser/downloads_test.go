package browser

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func resetDownloadsForTest(t *testing.T) {
	t.Helper()
	resetDownloads()
	t.Cleanup(resetDownloads)
}

func TestSanitizeDownloadFilename(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd": "passwd",
		"a/b\\c.txt":       "c.txt",
		"\x00\x1fbad.pdf":  "bad.pdf",
		".hidden":          "hidden",
		"":                 "download",
		"..":               "download",
		"  ":               "download",
		"report‮fdp.exe":   "reportfdp.exe",
		"dir/":             "download",
		"ok name.tar.gz":   "ok name.tar.gz",
	}
	for input, want := range cases {
		if got := sanitizeDownloadFilename(input); got != want {
			t.Errorf("sanitizeDownloadFilename(%q) = %q, want %q", input, got, want)
		}
	}
	long := strings.Repeat("x", 296) + ".zip"
	got := sanitizeDownloadFilename(long)
	if len(got) > 200 || !strings.HasSuffix(got, ".zip") {
		t.Fatalf("a 300-byte name should be cut to at most 200 bytes keeping .zip, got %d bytes %q", len(got), got)
	}
	wide := strings.Repeat("文", 100) + ".pdf"
	got = sanitizeDownloadFilename(wide)
	if len(got) > 200 || !strings.HasSuffix(got, ".pdf") || !utf8.ValidString(got) {
		t.Fatalf("multi-byte names must be cut on a rune boundary, got %d bytes %q", len(got), got)
	}
}

func TestDownloadRecordsCapAt50(t *testing.T) {
	resetDownloadsForTest(t)
	events := recordEvents(t)
	var first string
	for i := 0; i < 55; i++ {
		id, _ := downloadStarted("tab-a", "https://example.com", "file.txt")
		if i == 0 {
			first = id
		}
		downloadDestination(id, "/Users/someone/Downloads/file.txt")
	}
	downloadStarted("tab-b", "https://example.com", "other.txt")
	records := downloadsForTab("tab-a")
	if len(records) != maxDownloadsPerTab {
		t.Fatalf("a tab keeps at most %d downloads, got %d", maxDownloadsPerTab, len(records))
	}
	for _, record := range records {
		if record.ID == first {
			t.Fatal("the oldest download should have been dropped")
		}
	}
	if got := len(downloadsForTab("tab-b")); got != 1 {
		t.Fatalf("other tabs keep their own records, got %d", got)
	}
	if _, ok := downloadPath(first); ok {
		t.Fatal("a dropped download must forget its destination")
	}
	if got := len(eventsNamed(events(), downloadUpdatedEvent)); got != 56 {
		t.Fatalf("every new download emits an update, got %d", got)
	}
}

func TestDownloadRecordsNeverContainPaths(t *testing.T) {
	resetDownloadsForTest(t)
	events := recordEvents(t)
	id, filename := downloadStarted("tab-a", "https://example.com:8443/some/page?token=secret", "../../Users/me/secret/report.pdf")
	if filename != "report.pdf" {
		t.Fatalf("filename = %q", filename)
	}
	downloadDestination(id, "/Users/me/Downloads/report.pdf")
	downloadUpdated(id, downloadStateSaving, 10, 100, "")
	downloadUpdated(id, downloadStateFailed, 10, 100, "network_error /private/var/tmp/x")
	records := downloadsForTab("tab-a")
	if len(records) != 1 || records[0].Origin != "https://example.com:8443" {
		t.Fatalf("origin must be scheme+host+port only, got %+v", records)
	}
	payloads := []any{records, downloadsForTab("tab-a")[0]}
	for _, event := range events() {
		payloads = append(payloads, event.Data)
	}
	for _, payload := range payloads {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		// The origin keeps its scheme separator; nothing else may carry a slash.
		text := strings.ReplaceAll(string(encoded), "https://", "")
		if strings.Contains(text, "/") || strings.Contains(text, "\\") || strings.Contains(text, "secret") || strings.Contains(text, "Downloads") {
			t.Fatalf("download JSON must not carry paths: %s", encoded)
		}
	}
}

func TestDownloadUpdatesFollowTheLifecycle(t *testing.T) {
	resetDownloadsForTest(t)
	events := recordEvents(t)
	original := now
	clock := time.Unix(1_700_000_000, 0)
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = original })

	id, _ := downloadStarted("tab-a", "https://example.com", "a.bin")
	record := downloadsForTab("tab-a")[0]
	if record.State != downloadStatePending || record.StartedAt != clock.UnixMilli() || record.FinishedAt != 0 {
		t.Fatalf("a new download is pending, got %+v", record)
	}
	downloadUpdated(id, downloadStateSaving, 5, 10, "")
	downloadUpdated(id, "", 7, -1, "")
	if record := downloadsForTab("tab-a")[0]; record.State != downloadStateSaving || record.Bytes != 7 || record.TotalBytes != 0 {
		t.Fatalf("a progress update keeps the state and clamps an unknown total, got %+v", record)
	}
	clock = clock.Add(2 * time.Second)
	downloadUpdated(id, downloadStateCompleted, 10, 10, "")
	downloadUpdated(id, downloadStateFailed, 0, 0, "late")
	downloadUpdated("unknown", downloadStateFailed, 0, 0, "x")
	record = downloadsForTab("tab-a")[0]
	if record.State != downloadStateCompleted || record.FinishedAt != clock.UnixMilli() || record.Error != "" || record.Bytes != 10 {
		t.Fatalf("a finished download ignores later updates, got %+v", record)
	}
	if got := len(eventsNamed(events(), downloadUpdatedEvent)); got != 4 {
		t.Fatalf("start, two progress updates and completion emit; later ones do not, got %d", got)
	}
	downloadUpdated(id, "bogus", 0, 0, "")
	if downloadsForTab("tab-a")[0].State != downloadStateCompleted {
		t.Fatal("unknown states are ignored")
	}
}

func TestDownloadAllowanceGate(t *testing.T) {
	resetDownloadsForTest(t)
	original := now
	clock := time.Unix(1_700_000_000, 0)
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = original; resetAIPopupWindows(); resetFileChoosers() })

	if admitDownload("tab-a") {
		t.Fatal("a download without an AI click or real input is refused")
	}
	allowAIDownload("tab-a")
	if admitDownload("tab-b") {
		t.Fatal("an allowance belongs to its tab")
	}
	if !admitDownload("tab-a") || admitDownload("tab-a") {
		t.Fatal("an AI click admits exactly one save panel")
	}
	noteUserInput("tab-a")
	if !takePopupAllowance("tab-a") || !admitFileChooser("tab-a") {
		t.Fatal("real input still opens the popup and chooser allowances")
	}
	if !admitDownload("tab-a") {
		t.Fatal("real input admits a save panel independently of popups and choosers")
	}
	allowAIDownload("tab-a")
	clock = clock.Add(downloadAllowance)
	if admitDownload("tab-a") {
		t.Fatal("the allowance expires after a second")
	}
	allowAIDownload("tab-a")
	forgetDownloadAllowances("tab-a")
	if admitDownload("tab-a") {
		t.Fatal("forgotten allowances admit nothing")
	}
}

func TestPopupClosedForgetsDownloadAllowance(t *testing.T) {
	resetDownloadsForTest(t)
	recordEvents(t)
	allowAIDownload("browser:popup:1")
	popupClosed("browser:popup:1")
	if admitDownload("browser:popup:1") {
		t.Fatal("a closed popup keeps no download allowance")
	}
}

func TestGetDownloadsToolReturnsTabRecords(t *testing.T) {
	resetDownloadsForTest(t)
	recordEvents(t)
	setTestSharedAIAccess(t, []string{"tab-a"}, "tab-a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	useTestHostWindow(t)
	empty, err := ExecuteAITool("browser.get_downloads", map[string]any{})
	if err != nil {
		t.Fatalf("get_downloads: %v", err)
	}
	if encoded, _ := json.Marshal(empty); string(encoded) != "[]" {
		t.Fatalf("no downloads is an empty list, got %s", encoded)
	}
	id, _ := downloadStarted("tab-a", "https://example.com", "a.txt")
	downloadDestination(id, "/Users/me/Downloads/a.txt")
	downloadStarted("tab-b", "https://example.com", "b.txt")
	result, err := ExecuteAITool("browser.get_downloads", map[string]any{})
	if err != nil {
		t.Fatalf("get_downloads: %v", err)
	}
	records, ok := result.([]downloadRecord)
	if !ok || len(records) != 1 || records[0].ID != id || records[0].Filename != "a.txt" {
		t.Fatalf("get_downloads returns only the tab's records, got %#v", result)
	}
	if encoded, _ := json.Marshal(result); strings.Contains(string(encoded), "Downloads") {
		t.Fatalf("get_downloads must not return paths: %s", encoded)
	}
}

func TestRevealDownloadUsesTheStoredPath(t *testing.T) {
	resetDownloadsForTest(t)
	recordEvents(t)
	var revealed []string
	original := revealDownloadInFileManager
	revealDownloadInFileManager = func(path string) error { revealed = append(revealed, path); return nil }
	t.Cleanup(func() { revealDownloadInFileManager = original })

	id, _ := downloadStarted("tab-a", "https://example.com", "a.txt")
	if _, err := handleRevealDownload("", `{"payload":{"id":"`+id+`"}}`); err == nil {
		t.Fatal("a download without a destination cannot be revealed")
	}
	downloadDestination(id, "/Users/me/Downloads/a.txt")
	downloadUpdated(id, downloadStateSaving, 0, 0, "")
	if _, err := handleRevealDownload("", `{"payload":{"id":"`+id+`"}}`); err == nil {
		t.Fatal("an unfinished download cannot be revealed")
	}
	downloadUpdated(id, downloadStateCompleted, 3, 3, "")
	result, err := handleRevealDownload("", `{"payload":{"id":"`+id+`"}}`)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if strings.Contains(result, "/") {
		t.Fatalf("the reveal result must not carry the path: %s", result)
	}
	if len(revealed) != 1 || revealed[0] != "/Users/me/Downloads/a.txt" {
		t.Fatalf("reveal should open the stored destination, got %v", revealed)
	}
	if _, err := handleRevealDownload("", `{"payload":{"id":"nope"}}`); err == nil {
		t.Fatal("unknown downloads cannot be revealed")
	}
	revealDownloadInFileManager = func(string) error { return errors.New("open -R /Users/me/Downloads/a.txt failed") }
	if _, err := handleRevealDownload("", `{"payload":{"id":"`+id+`"}}`); err == nil || strings.Contains(err.Error(), "/") {
		t.Fatalf("reveal errors must not carry the path, got %v", err)
	}
}

func TestPageNavigationPolicy(t *testing.T) {
	cases := []struct {
		url       string
		mainFrame bool
		allowed   bool
	}{
		{"https://example.com/a", true, true},
		{"http://127.0.0.1:8080/", true, true},
		{"about:blank", true, true},
		{"about:srcdoc", false, true},
		{"file:///etc/passwd", true, false},
		{"file:///etc/passwd", false, false},
		{"http://0.0.0.0:1/x", true, false},
		{"http://169.254.169.254/latest", false, false},
		{"data:text/html,hi", true, true},
		{"data:text/html,hi", false, true},
		{"blob:https://example.com/1234", false, true},
		{"blob:https://example.com/1234", true, true},
		{"x-custom://thing", false, false},
		{"mailto:a@example.com", true, false},
		{"javascript:alert(1)", true, false},
	}
	for _, c := range cases {
		if got := pageNavigationAllowed(c.url, c.mainFrame); got != c.allowed {
			t.Errorf("pageNavigationAllowed(%q, main=%v) = %v, want %v", c.url, c.mainFrame, got, c.allowed)
		}
	}
	for _, c := range []struct {
		url     string
		allowed bool
	}{
		{"https://example.com/file.zip", true},
		{"blob:https://example.com/1", true},
		{"data:application/octet-stream,abc", true},
		{"file:///etc/passwd", false},
		{"http://0.0.0.0/x", false},
	} {
		if got := downloadURLAllowed(c.url); got != c.allowed {
			t.Errorf("downloadURLAllowed(%q) = %v, want %v", c.url, got, c.allowed)
		}
	}
	for _, c := range []struct {
		url     string
		allowed bool
	}{
		{"https://cdn.example.com/file.zip?sig=1", true},
		{"http://169.254.169.254/latest", false},
		{"data:application/octet-stream,abc", false},
		{"blob:https://example.com/1", false},
		{"file:///etc/passwd", false},
		{"x-custom://thing", false},
	} {
		if got := downloadRedirectAllowed(c.url); got != c.allowed {
			t.Errorf("downloadRedirectAllowed(%q) = %v, want %v", c.url, got, c.allowed)
		}
	}
}
