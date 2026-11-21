package lsp

import (
	"errors"
	"regexp"
	"runtime"
	"strings"
)

// maxGlobPatternBytes bounds one server-supplied watcher pattern.
const maxGlobPatternBytes = 1024

// compileGlob converts an LSP GlobPattern (`*`, `?`, `**`, `{a,b}`, `[a-z]`,
// `[!a]`) into an anchored regular expression over slash-separated paths.
// `*` and `?` never cross a path segment; `**` spans any number of segments.
func compileGlob(pattern string) (*regexp.Regexp, error) {
	if pattern == "" || len(pattern) > maxGlobPatternBytes {
		return nil, errors.New("invalid glob pattern")
	}
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	if runtime.GOOS == "windows" {
		pattern = strings.ToLower(pattern)
	}
	var out strings.Builder
	out.WriteString("^")
	if err := writeGlob(&out, pattern, 0); err != nil {
		return nil, err
	}
	out.WriteString("$")
	return regexp.Compile(out.String())
}

func writeGlob(out *strings.Builder, pattern string, depth int) error {
	if depth > 8 {
		return errors.New("glob braces are nested too deeply")
	}
	for index := 0; index < len(pattern); index++ {
		char := pattern[index]
		switch char {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				atSegmentStart := index == 0 || pattern[index-1] == '/'
				end := index + 2
				switch {
				case atSegmentStart && end < len(pattern) && pattern[end] == '/':
					// "**/" matches zero or more leading segments.
					out.WriteString("(?:[^/]*/)*")
					index = end
				case atSegmentStart && end == len(pattern):
					out.WriteString(".*")
					index = end - 1
				default:
					out.WriteString("[^/]*")
					index = end - 1
				}
				continue
			}
			out.WriteString("[^/]*")
		case '?':
			out.WriteString("[^/]")
		case '[':
			closing := strings.IndexByte(pattern[index+1:], ']')
			if closing < 0 {
				out.WriteString(`\[`)
				continue
			}
			body := pattern[index+1 : index+1+closing]
			index += closing + 1
			negate := strings.HasPrefix(body, "!") || strings.HasPrefix(body, "^")
			if negate {
				body = body[1:]
			}
			if body == "" {
				return errors.New("empty glob character class")
			}
			out.WriteString("[")
			if negate {
				out.WriteString("^/")
			}
			for _, member := range body {
				if member == '\\' || member == ']' || member == '[' || member == '^' {
					out.WriteByte('\\')
				}
				out.WriteRune(member)
			}
			out.WriteString("]")
		case '{':
			closing, alternatives, err := splitBraces(pattern, index)
			if err != nil {
				return err
			}
			out.WriteString("(?:")
			for position, alternative := range alternatives {
				if position > 0 {
					out.WriteString("|")
				}
				if err := writeGlob(out, alternative, depth+1); err != nil {
					return err
				}
			}
			out.WriteString(")")
			index = closing
		default:
			out.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	return nil
}

// splitBraces returns the index of the brace closing the group that opens at
// start and its top-level comma-separated alternatives.
func splitBraces(pattern string, start int) (int, []string, error) {
	depth := 0
	last := start + 1
	var alternatives []string
	for index := start; index < len(pattern); index++ {
		switch pattern[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index, append(alternatives, pattern[last:index]), nil
			}
		case ',':
			if depth == 1 {
				alternatives = append(alternatives, pattern[last:index])
				last = index + 1
			}
		}
	}
	return 0, nil, errors.New("unclosed glob brace")
}

// globPath normalizes a host path for glob matching.
func globPath(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
