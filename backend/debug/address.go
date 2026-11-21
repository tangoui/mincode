package debug

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/internal/textpolicy"
)

// DebuggerAddressMaxRunes bounds a debugger-provided source address.
const DebuggerAddressMaxRunes = 4096

// debuggerAddressScheme is the required URI-like prefix "<scheme>://". The
// scheme has at least two characters: a single letter followed by "://"
// (`C://Users/x`) is a Windows drive path to many adapters and would bypass
// the project-root check of SourcePathInProject.
var debuggerAddressScheme = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]+)://`)

// ValidDebuggerAddress is the one host rule for a source address a debug
// adapter itself handed out (a DAP `source.path` that is not a project file,
// for example a class URI of a library). The host never parses or resolves it:
// it only requires a "<scheme>://" prefix whose scheme has at least two
// characters and is not "file" (any case; a drive letter or a file URI would
// bypass the project-root check of SourcePathInProject),
// at most DebuggerAddressMaxRunes characters, valid UTF-8 and no control,
// line/paragraph separator, bidi or zero-width characters. The address is
// returned unchanged; errors start with "invalid_request:".
func ValidDebuggerAddress(address string) (string, error) {
	if !utf8.ValidString(address) {
		return "", fmt.Errorf("invalid_request: debugger address must be valid UTF-8")
	}
	if n := utf8.RuneCountInString(address); n > DebuggerAddressMaxRunes {
		return "", fmt.Errorf("invalid_request: debugger address must be at most %d characters, got %d", DebuggerAddressMaxRunes, n)
	}
	for _, r := range address {
		if textpolicy.UnsafeRune(r, false, false) {
			return "", fmt.Errorf("invalid_request: debugger address contains a disallowed character U+%04X", r)
		}
	}
	match := debuggerAddressScheme.FindStringSubmatch(address)
	if match == nil {
		return "", fmt.Errorf("invalid_request: debugger address must start with <scheme>:// (scheme of at least 2 characters), got %q", address)
	}
	if strings.EqualFold(match[1], "file") {
		return "", fmt.Errorf("invalid_request: debugger address must not be a file URI; send project files as paths")
	}
	return address, nil
}
