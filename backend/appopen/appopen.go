package appopen

import (
	"path/filepath"
	"strings"
	"sync"

	"mincode/app/backend/core"
)

var pending = struct {
	sync.Mutex
	files []string
}{}

func init() {
	core.Register("app_consume_opened_files", consumeOpenedFilesHandler)
}

// Enqueue records files delivered by the operating system before or after the
// frontend is ready. The frontend consumes this queue after hydration, so a
// cold-start double-click cannot lose the initial file-open event.
func Enqueue(files ...string) []string {
	pending.Lock()
	defer pending.Unlock()
	added := make([]string, 0, len(files))
	for _, file := range files {
		path := strings.TrimSpace(file)
		if path == "" {
			continue
		}
		if absolute, err := filepath.Abs(path); err == nil {
			path = filepath.Clean(absolute)
		}
		duplicate := false
		for _, existing := range pending.files {
			if existing == path {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		pending.files = append(pending.files, path)
		added = append(added, path)
	}
	return added
}

func consumeOpenedFilesHandler(_, _ string) (string, error) {
	pending.Lock()
	// Non-nil so an empty queue encodes as [] rather than null.
	files := append([]string{}, pending.files...)
	pending.files = nil
	pending.Unlock()
	return core.EncodeResult(files)
}
