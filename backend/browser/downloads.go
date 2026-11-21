package browser

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// Page downloads. Every download is confirmed by the user in a native save
// panel, whatever the approval mode: on macOS the overlay hands WebKit's
// download to an NSSavePanel sheet and reports its progress here. Records keep
// only the sanitized file name and the page origin; the chosen destination
// stays in Go (for "Show in Finder") and never reaches events or tool results.
// The functions below are called on the main thread from native callbacks, so
// they only take the downloads lock and emit events.

const (
	downloadUpdatedEvent = "browser:download-updated"

	maxDownloadsPerTab      = 50
	maxDownloadFilenameSize = 200

	// downloadAllowance is how long after an AI click or real user input the tab may show a save panel.
	downloadAllowance = time.Second

	downloadStatePending   = "pending"
	downloadStateSaving    = "saving"
	downloadStateCompleted = "completed"
	downloadStateFailed    = "failed"
	downloadStateCancelled = "cancelled"

	// downloadErrorNoUserAction marks a download refused because no AI click or real input started it.
	downloadErrorNoUserAction = "no_user_action"
)

type downloadRecord struct {
	ID         string `json:"id"`
	TabID      string `json:"tabId"`
	Origin     string `json:"origin"`
	Filename   string `json:"filename"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"totalBytes"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
}

var downloads = struct {
	sync.Mutex
	byTab map[string][]*downloadRecord // oldest first
	byID  map[string]*downloadRecord
	paths map[string]string // destination chosen in the save panel, Go only
	// Save-panel allowances, kept apart from the popup and chooser ones.
	ai    map[string]time.Time
	input map[string]time.Time
}{}

func init() {
	resetDownloads()
	core.Register("embedded_browser_reveal_download", handleRevealDownload)
}

func resetDownloads() {
	downloads.Lock()
	defer downloads.Unlock()
	downloads.byTab = map[string][]*downloadRecord{}
	downloads.byID = map[string]*downloadRecord{}
	downloads.paths = map[string]string{}
	downloads.ai = map[string]time.Time{}
	downloads.input = map[string]time.Time{}
}

// sanitizeDownloadFilename keeps only the last path element, drops control and
// invisible formatting characters (e.g. right-to-left overrides) and leading
// dots, falls back to "download", and cuts names over 200 bytes while keeping
// the extension.
func sanitizeDownloadFilename(suggested string) string {
	name := strings.ToValidUTF8(suggested, "")
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimLeft(name, ". \t")
	name = strings.TrimRight(name, " \t")
	if name == "" {
		return "download"
	}
	if len(name) <= maxDownloadFilenameSize {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 20 || ext == name {
		ext = ""
	}
	base := truncateUTF8(strings.TrimSuffix(name, ext), maxDownloadFilenameSize-len(ext))
	return base + ext
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// sanitizeDownloadOrigin keeps scheme://host[:port]; anything else becomes "".
func sanitizeDownloadOrigin(origin string) string {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + parsed.Host
}

// sanitizeDownloadError keeps a short code-like message: letters, digits and
// a few separators, so a native error can never carry a path or address.
func sanitizeDownloadError(message string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '_' || r == '-' || r == ':' || r == ' ':
			return r
		}
		return -1
	}, message)
	return truncateUTF8(strings.TrimSpace(cleaned), 120)
}

func newDownloadID() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "download-" + hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("download-%d", time.Now().UnixNano())
}

// downloadStarted records a new pending download and returns its id and the
// sanitized file name for the save panel.
func downloadStarted(tabID, origin, suggested string) (id, filename string) {
	record := &downloadRecord{
		ID:        newDownloadID(),
		TabID:     tabID,
		Origin:    sanitizeDownloadOrigin(origin),
		Filename:  sanitizeDownloadFilename(suggested),
		State:     downloadStatePending,
		StartedAt: now().UnixMilli(),
	}
	downloads.Lock()
	records := append(downloads.byTab[tabID], record)
	for len(records) > maxDownloadsPerTab {
		dropped := records[0]
		delete(downloads.byID, dropped.ID)
		delete(downloads.paths, dropped.ID)
		records = records[1:]
	}
	downloads.byTab[tabID] = records
	downloads.byID[record.ID] = record
	snapshot := *record
	downloads.Unlock()
	core.Emit(downloadUpdatedEvent, snapshot)
	return snapshot.ID, snapshot.Filename
}

// downloadDestination stores where the user chose to save the download. The
// path stays in Go and is only used to show the file in Finder.
func downloadDestination(id, path string) {
	downloads.Lock()
	defer downloads.Unlock()
	if _, ok := downloads.byID[id]; ok && path != "" {
		downloads.paths[id] = path
	}
}

func downloadPath(id string) (string, bool) {
	downloads.Lock()
	defer downloads.Unlock()
	path, ok := downloads.paths[id]
	return path, ok
}

func downloadFinished(state string) bool {
	return state == downloadStateCompleted || state == downloadStateFailed || state == downloadStateCancelled
}

// downloadUpdated applies a progress or state change. An empty state keeps the
// current one; unknown ids, unknown states and updates after the download
// finished are ignored. A negative total (unknown size) is stored as 0.
func downloadUpdated(id string, state string, bytes, total int64, errMsg string) {
	switch state {
	case "", downloadStatePending, downloadStateSaving, downloadStateCompleted, downloadStateFailed, downloadStateCancelled:
	default:
		return
	}
	downloads.Lock()
	record, ok := downloads.byID[id]
	if !ok || downloadFinished(record.State) {
		downloads.Unlock()
		return
	}
	if state != "" {
		record.State = state
	}
	if bytes >= 0 {
		record.Bytes = bytes
	}
	if total < 0 {
		total = 0
	}
	record.TotalBytes = total
	if message := sanitizeDownloadError(errMsg); message != "" {
		record.Error = message
	}
	if downloadFinished(record.State) {
		record.FinishedAt = now().UnixMilli()
	}
	snapshot := *record
	downloads.Unlock()
	core.Emit(downloadUpdatedEvent, snapshot)
}

// downloadsForTab returns copies of the tab's records, oldest first (never nil).
func downloadsForTab(tabID string) []downloadRecord {
	downloads.Lock()
	defer downloads.Unlock()
	out := make([]downloadRecord, 0, len(downloads.byTab[tabID]))
	for _, record := range downloads.byTab[tabID] {
		out = append(out, *record)
	}
	return out
}

// allowAIDownload lets tabID show one save panel during the next second;
// called right before an AI click.
func allowAIDownload(tabID string) {
	downloads.Lock()
	defer downloads.Unlock()
	downloads.ai[tabID] = now().Add(downloadAllowance)
}

// noteDownloadInput records a real mouse or keyboard event on tabID's web view
// (via noteUserInput). Called on the main thread.
func noteDownloadInput(tabID string) {
	downloads.Lock()
	defer downloads.Unlock()
	downloads.input[tabID] = now().Add(downloadAllowance)
}

// admitDownload decides whether tabID's page may show a save panel now and
// uses up the allowance (AI click first). Called on the main thread.
func admitDownload(tabID string) bool {
	downloads.Lock()
	defer downloads.Unlock()
	for _, allowances := range []map[string]time.Time{downloads.ai, downloads.input} {
		if allowanceOpenLocked(allowances, tabID) {
			delete(allowances, tabID)
			return true
		}
	}
	return false
}

func forgetDownloadAllowances(tabID string) {
	downloads.Lock()
	defer downloads.Unlock()
	delete(downloads.ai, tabID)
	delete(downloads.input, tabID)
}

// pageNavigationAllowed decides on a navigation a page started (links, forms,
// location changes, frames). http(s) follows the same rules as AI and user
// navigation in every frame. blob: and data: are allowed as browsers do (a
// page saving a generated file); the overlay still refuses a top-level data:
// response that is not a download, so data: cannot be used to show a page.
// Any other scheme (file:, custom app schemes, mailto:) is refused everywhere.
func pageNavigationAllowed(rawURL string, mainFrame bool) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "about":
		return parsed.Opaque == "blank" || parsed.Opaque == "srcdoc"
	case "http", "https":
		return validateNavigationURL(rawURL) == nil
	case "data", "blob":
		return true
	}
	return false
}

// downloadRedirectAllowed decides on a server redirect while a download runs:
// only addresses validateNavigationURL accepts (http/https).
func downloadRedirectAllowed(rawURL string) bool {
	return validateNavigationURL(strings.TrimSpace(rawURL)) == nil
}

// downloadURLAllowed decides on a link the page marked as a download.
func downloadURLAllowed(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return validateNavigationURL(rawURL) == nil
	case "data", "blob":
		return true
	}
	return false
}

// revealDownloadInFileManager selects the file in Finder through the shared
// local_fs_show_in_file_manager command. Tests replace it.
var revealDownloadInFileManager = func(path string) error {
	payload, err := core.EncodeResult(map[string]any{"path": path, "select": true})
	if err != nil {
		return err
	}
	_, err = core.Dispatch("local_fs_show_in_file_manager", payload)
	return err
}

var errDownloadRevealFailed = errors.New("could not show the download in Finder")

// handleRevealDownload shows a completed download in Finder. The path never
// leaves Go: neither the result nor errors carry it.
func handleRevealDownload(_, argsJSON string) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := core.DecodeArgsPayload(argsJSON, &args); err != nil {
		return "", err
	}
	id := strings.TrimSpace(args.ID)
	downloads.Lock()
	record, ok := downloads.byID[id]
	state := ""
	if ok {
		state = record.State
	}
	path, hasPath := downloads.paths[id]
	downloads.Unlock()
	if !ok || !hasPath {
		return "", errors.New("download not found")
	}
	if state != downloadStateCompleted {
		return "", errors.New("the download has not finished")
	}
	if err := revealDownloadInFileManager(path); err != nil {
		return "", errDownloadRevealFailed
	}
	return core.EncodeResult(nil)
}
