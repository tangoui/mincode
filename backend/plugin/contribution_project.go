package plugin

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// contributionProject is the project block sent to tree view and test
// provider actions: the folder name always, the canonical root only when the
// plugin declares workspace.read (testProviders require it).
type contributionProject struct {
	Name string `json:"name"`
	Root string `json:"root,omitempty"`
}

// contributionProjectFor canonicalizes a WebView-supplied project root with
// the task rules (no filesystem root, home or application data folder) and
// returns the payload block.
func contributionProjectFor(projectRoot string, includeRoot bool) (*contributionProject, error) {
	root, err := canonicalTaskProjectRoot(projectRoot)
	if err != nil {
		code := contributionErrForbidden
		if processLaunchErrorCode(err) == processLaunchErrNotFound {
			code = contributionErrNotFound
		}
		_, detail, _ := strings.Cut(err.Error(), ": ")
		return nil, newContributionError(code, "%s", detail)
	}
	project := &contributionProject{Name: filepath.Base(root)}
	if includeRoot {
		project.Root = root
	}
	return project, nil
}

// validateContributionItemID accepts a plugin-chosen item id: single-line
// text of at most maxBytes bytes without surrounding spaces.
func validateContributionItemID(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && checkContributionRunes(value, false, false) == nil
}

var contributionANSIPattern = regexp.MustCompile("\x1b(\\[[0-9;?]*[ -/]*[@-~]|\\][^\x07\x1b]*(\x07|\x1b\\\\)|[@-Z\\\\-_])")

// sanitizeContributionText turns plugin output into displayable plain text:
// CRLF/CR become LF, ANSI escape sequences and other unsafe characters are
// removed, and the text is cut to maxRunes characters (truncated reports a
// cut). Invalid UTF-8 is replaced.
func sanitizeContributionText(value string, maxRunes int) (string, bool) {
	value = strings.ToValidUTF8(value, "\uFFFD")
	value = contributionANSIPattern.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	var builder strings.Builder
	count := 0
	for _, r := range value {
		if isUnsafeContributionRune(r, true, true) {
			continue
		}
		if count == maxRunes {
			return builder.String(), true
		}
		builder.WriteRune(r)
		count++
	}
	return builder.String(), false
}
