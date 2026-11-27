package terminal

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const resumeSensitiveName = `(?:password|passwd|pwd|token|secret|api[_-]?key|private[_-]?key|passphrase|credential|authorization|cookie|session[_-]?id)`

var (
	resumeSensitiveAssignment = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])` + resumeSensitiveName + `["']?\s*[=:]`)
	resumeSensitiveFlag       = regexp.MustCompile(`(?i)(?:^|[\s"'=])-{1,2}(?:[a-z0-9]+[-_])*` + resumeSensitiveName + `(?:$|[\s"'=:_-])`)
	resumeSensitiveQueryKey   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])` + resumeSensitiveName + `(?:$|[^a-z0-9])`)
	resumeCamelCase           = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	resumeAuthorization       = regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
	resumePrivateKey          = regexp.MustCompile(`(?i)-----BEGIN [^-]*PRIVATE KEY-----`)
	resumeURL                 = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	resumeUnquotedWord        = regexp.MustCompile(`^[a-zA-Z0-9_./:@+,-]+$`)
)

func sensitiveResumeArgs(args []string) bool {
	// Check the raw argv, before shell quoting changes word boundaries.
	text := strings.Join(args, " ")
	normalized := resumeCamelCase.ReplaceAllString(text, "${1}_${2}")
	if resumeSensitiveAssignment.MatchString(normalized) || resumeSensitiveFlag.MatchString(normalized) ||
		resumeAuthorization.MatchString(text) || resumePrivateKey.MatchString(text) {
		return true
	}
	for _, candidate := range resumeURL.FindAllString(text, -1) {
		if parsed, err := url.Parse(candidate); err == nil {
			if parsed.User != nil {
				return true
			}
			for key := range parsed.Query() {
				if resumeSensitiveQueryKey.MatchString(resumeCamelCase.ReplaceAllString(key, "${1}_${2}")) {
					return true
				}
			}
		} else if strings.Contains(candidate, "@") {
			return true
		}
	}
	return false
}

func terminalResumeCommand(args []string, shellPath string) string {
	if len(args) == 0 || args[0] == "" || sensitiveResumeArgs(args) {
		return ""
	}
	shell := strings.ToLower(filepath.Base(shellPath))
	powerShell := shell == "pwsh" || shell == "powershell"
	words := make([]string, 0, len(args)+1)
	if powerShell {
		words = append(words, "&")
	}
	for _, arg := range args {
		if !utf8.ValidString(arg) || strings.ContainsFunc(arg, unicode.IsControl) {
			return ""
		}
		switch {
		case powerShell:
			words = append(words, "'"+strings.ReplaceAll(arg, "'", "''")+"'")
		case resumeUnquotedWord.MatchString(arg):
			words = append(words, arg)
		default:
			// Keep backslashes and apostrophes outside single quotes for both POSIX shells and fish.
			escaped := strings.NewReplacer("\\", `'\\'`, "'", `'\''`).Replace(arg)
			words = append(words, "'"+escaped+"'")
		}
	}
	// A line sent to a canonical PTY must fit in the terminal input buffer.
	command := strings.Join(words, " ")
	if len(command) >= 1024 {
		return ""
	}
	return command
}
