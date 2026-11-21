// Package associations owns OS default-application integration. Registration
// is opt-in and never happens at startup or while reading status.
package associations

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"mincode/app/plugins/api/externalopen"
)

const BundleID = "com.mincode.desktop"

type Status struct {
	Supported   bool   `json:"supported"`
	Default     bool   `json:"default"`
	Application string `json:"application"`
	Manual      bool   `json:"manual"`
}

func Query(id string) (Status, error) {
	t, ok := externalopen.Lookup(id)
	if !ok {
		return Status{}, errors.New("unknown association type")
	}
	return query(t)
}

// Set is called only by the trusted host after confirmation. On Windows it
// opens Default Apps: UserChoice is never forged. Always query again afterwards.
func Set(id, previous string, restore bool) (Status, error) {
	t, ok := externalopen.Lookup(id)
	if !ok {
		return Status{}, errors.New("unknown association type")
	}
	current, err := query(t)
	if err != nil {
		return Status{}, err
	}
	if restore && !current.Manual {
		if !current.Default {
			return current, nil
		} // Do not overwrite a later user choice.
		if previous == "" || previous == BundleID {
			return current, errors.New("no previous application to restore; choose another application in system settings")
		}
	}
	if err = set(t, previous, restore); err != nil {
		return current, err
	}
	return query(t)
}

// ValidateFile rejects a symlink leaf and canonicalizes parent components
// (macOS /var itself is a system symlink). Callers bind identity and canonical
// path and check both again when reading.
func ValidateFile(path string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(path) || len(path) > 4096 || (!utf8.ValidString(path) || strings.IndexFunc(path, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0) {
		return "", nil, errors.New("invalid file path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, errors.New("not a regular file")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, err
	}
	current, err := os.Stat(canonical)
	if err != nil || !os.SameFile(info, current) {
		return "", nil, errors.New("file changed")
	}
	if _, ok := externalopen.FileType(canonical); !ok {
		return "", nil, errors.New("unsupported file type")
	}
	if info.Size() > 16<<20 {
		return "", nil, errors.New("external file exceeds 16 MiB")
	}
	return canonical, current, nil
}
