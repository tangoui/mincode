package plugin

import (
	"errors"
	"regexp"
	"strings"
)

// Native desktop drops reach a plugin view only when its manifest declares
// fileDrop. "contents" hands the page the bytes of a few small files (the page
// never learns their paths); "paths" hands it absolute paths, like the host
// path picker does, so the plugin backend can open them.
const (
	FileDropModeContents = "contents"
	FileDropModePaths    = "paths"

	fileDropMaxExtensions     = 32
	fileDropMaxContentEntries = 4
	fileDropMaxPathEntries    = 1000
	fileDropDefaultPathLimit  = 100
)

var fileDropExtensionPattern = regexp.MustCompile(`^\.[a-z0-9]{1,16}$`)

type ManifestFileDrop struct {
	Mode string `json:"mode"`
	// Extensions filters dropped files (case-insensitive, with the leading dot);
	// empty accepts every regular file.
	Extensions []string `json:"extensions,omitempty"`
	// Directories forwards dropped folders as entries; only in "paths" mode.
	Directories bool `json:"directories,omitempty"`
	// MaxEntries rejects larger drops; 0 means the mode's default.
	MaxEntries int `json:"maxEntries,omitempty"`
}

// normalizeFileDrop validates and fills defaults in place.
func normalizeFileDrop(drop *ManifestFileDrop) error {
	if drop == nil {
		return nil
	}
	drop.Mode = strings.TrimSpace(drop.Mode)
	limit := 0
	switch drop.Mode {
	case FileDropModeContents:
		if drop.Directories {
			return errors.New("fileDrop.directories requires mode \"paths\"")
		}
		limit = fileDropMaxContentEntries
		if drop.MaxEntries == 0 {
			drop.MaxEntries = fileDropMaxContentEntries
		}
	case FileDropModePaths:
		limit = fileDropMaxPathEntries
		if drop.MaxEntries == 0 {
			drop.MaxEntries = fileDropDefaultPathLimit
		}
	default:
		return errors.New("fileDrop.mode must be \"contents\" or \"paths\"")
	}
	if drop.MaxEntries < 1 || drop.MaxEntries > limit {
		return errors.New("fileDrop.maxEntries is out of range")
	}
	if len(drop.Extensions) > fileDropMaxExtensions {
		return errors.New("fileDrop.extensions has too many entries")
	}
	seen := make(map[string]struct{}, len(drop.Extensions))
	extensions := make([]string, 0, len(drop.Extensions))
	for _, extension := range drop.Extensions {
		extension = strings.ToLower(strings.TrimSpace(extension))
		if !fileDropExtensionPattern.MatchString(extension) {
			return errors.New("fileDrop.extensions entries must look like \".png\"")
		}
		if _, duplicate := seen[extension]; duplicate {
			continue
		}
		seen[extension] = struct{}{}
		extensions = append(extensions, extension)
	}
	drop.Extensions = extensions
	return nil
}

func cloneFileDrop(drop *ManifestFileDrop) *ManifestFileDrop {
	if drop == nil {
		return nil
	}
	copy := *drop
	copy.Extensions = append([]string(nil), drop.Extensions...)
	return &copy
}
