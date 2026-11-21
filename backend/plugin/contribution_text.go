package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/internal/textpolicy"
)

// Shared text rules for contribution points that render plugin-authored text
// (names, descriptions, documentation, snippets). They reject, never strip:
// control characters, line/paragraph separators, bidi overrides and
// zero-width characters can disguise what the user sees or copies. Error
// messages are bare ("must be ..."); callers prefix the field name.

// isUnsafeContributionRune reports whether r is never allowed in contributed
// text; the rule is the host-wide textpolicy.UnsafeRune.
func isUnsafeContributionRune(r rune, allowNewline, allowTab bool) bool {
	return textpolicy.UnsafeRune(r, allowNewline, allowTab)
}

func checkContributionRunes(value string, allowNewline, allowTab bool) error {
	for _, r := range value {
		if isUnsafeContributionRune(r, allowNewline, allowTab) {
			return fmt.Errorf("contains a disallowed character U+%04X", r)
		}
	}
	return nil
}

// validateContributionLine validates single-line text and returns it trimmed.
// An empty (after trimming) value is an error only when required.
func validateContributionLine(value string, maxRunes int, required bool) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("must be valid UTF-8")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", errors.New("is required")
		}
		return "", nil
	}
	if maxRunes > 0 && utf8.RuneCountInString(value) > maxRunes {
		return "", fmt.Errorf("must be at most %d characters", maxRunes)
	}
	if err := checkContributionRunes(value, false, false); err != nil {
		return "", err
	}
	return value, nil
}

// validateContributionMultiline validates multi-line text (\n and \t allowed)
// after normalizing CRLF to LF; a lone CR is rejected. The value is not
// trimmed. maxRunes and maxBytes apply to the normalized value; 0 means
// unbounded.
func validateContributionMultiline(value string, maxRunes, maxBytes int) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("must be valid UTF-8")
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	if maxBytes > 0 && len(value) > maxBytes {
		return "", fmt.Errorf("must be at most %d bytes", maxBytes)
	}
	if maxRunes > 0 && utf8.RuneCountInString(value) > maxRunes {
		return "", fmt.Errorf("must be at most %d characters", maxRunes)
	}
	if err := checkContributionRunes(value, true, true); err != nil {
		return "", err
	}
	return value, nil
}

// validateContributionLocalized validates a locale → single-line text map.
// Locale keys follow validateLocalizedTitles (non-blank, at most 32 bytes)
// and the line rules; values follow validateContributionLine (required) and
// are returned trimmed. An empty map yields nil.
func validateContributionLocalized(values map[string]string, maxRunes, localesMax int) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if localesMax > 0 && len(values) > localesMax {
		return nil, fmt.Errorf("must have at most %d locales", localesMax)
	}
	result := make(map[string]string, len(values))
	for locale, value := range values {
		if strings.TrimSpace(locale) == "" || len(locale) > 32 || !utf8.ValidString(locale) || checkContributionRunes(locale, false, false) != nil {
			return nil, fmt.Errorf("locale %q is invalid", locale)
		}
		normalized, err := validateContributionLine(value, maxRunes, true)
		if err != nil {
			return nil, fmt.Errorf("locale %q %w", locale, err)
		}
		result[locale] = normalized
	}
	return result, nil
}

// strictJSONContainer marks object and array nodes passed to a strictJSONWalk
// visitor.
type strictJSONContainer int

const (
	jsonObjectValue strictJSONContainer = iota + 1
	jsonArrayValue
)

// errSkipJSONMember, returned by a strictJSONWalk visitor for an object
// member, omits that member (and its whole value) from the canonical output.
// Returned for the root or an array element it is an error.
var errSkipJSONMember = errors.New("skip JSON member")

// strictJSONMaxDepth caps container nesting to guard the recursion; points enforce tighter depths in
// their visitor.
const strictJSONMaxDepth = 1000

// strictJSONWalk decodes one JSON document strictly and re-encodes it
// compactly while preserving key order. A leading UTF-8 BOM is stripped;
// invalid UTF-8, duplicate object keys, comments and trailing data are
// errors. visit (optional) is called for every node before its children:
// path is the parent's path from the root (object keys and decimal array
// indices), key is the node's member name or array index ("" for the root),
// and value is a string, json.Number, bool, nil, jsonObjectValue or
// jsonArrayValue. visit must not retain path. A visit error aborts the walk,
// except errSkipJSONMember for an object member (see there). Strings are
// re-encoded without HTML escaping.
func strictJSONWalk(data []byte, visit func(path []string, key string, value any) error) ([]byte, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return nil, errors.New("JSON must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	walker := &strictJSONWalker{decoder: decoder, visit: visit}
	walker.encoder = json.NewEncoder(&walker.scratch)
	walker.encoder.SetEscapeHTML(false)
	if _, err := walker.value(nil, "", false); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("JSON has trailing data")
		}
		return nil, fmt.Errorf("JSON has trailing data: %w", err)
	}
	return walker.out.Bytes(), nil
}

type strictJSONWalker struct {
	decoder *json.Decoder
	visit   func(path []string, key string, value any) error
	encoder *json.Encoder
	scratch bytes.Buffer
	out     bytes.Buffer
	depth   int
}

// value reads one value and writes it to out. It reports skipped when the
// visitor asked to omit this member (only allowed when member is true); the
// value has then been consumed without output.
func (walker *strictJSONWalker) value(path []string, key string, member bool) (skipped bool, err error) {
	token, err := walker.decoder.Token()
	if err != nil {
		if err == io.EOF {
			return false, errors.New("JSON ended unexpectedly")
		}
		return false, fmt.Errorf("invalid JSON: %w", err)
	}
	var node any = token
	delim, isDelim := token.(json.Delim)
	if isDelim {
		switch delim {
		case '{':
			node = jsonObjectValue
		case '[':
			node = jsonArrayValue
		default:
			return false, fmt.Errorf("invalid JSON: unexpected %q", delim)
		}
	}
	if walker.visit != nil {
		if err := walker.visit(path, key, node); err != nil {
			if err != errSkipJSONMember {
				return false, err
			}
			if !member {
				return false, errors.New("only object members can be skipped")
			}
			if isDelim {
				// Consume the value with the same strictness, without output.
				saved, mark := walker.visit, walker.out.Len()
				walker.visit = nil
				err := walker.container(delim, strictJSONChildPath(path, key))
				walker.visit = saved
				walker.out.Truncate(mark)
				return true, err
			}
			return true, nil
		}
	}
	if !isDelim {
		return false, walker.writeScalar(token)
	}
	return false, walker.container(delim, strictJSONChildPath(path, key))
}

// container writes the rest of an object or array whose opening delimiter was
// already read; childPath is the container's own path.
func (walker *strictJSONWalker) container(delim json.Delim, childPath []string) error {
	if walker.depth++; walker.depth > strictJSONMaxDepth {
		return errors.New("JSON is nested too deeply")
	}
	defer func() { walker.depth-- }()
	if delim == '[' {
		walker.out.WriteByte('[')
		for index := 0; walker.decoder.More(); index++ {
			if index > 0 {
				walker.out.WriteByte(',')
			}
			if _, err := walker.value(childPath, strconv.Itoa(index), false); err != nil {
				return err
			}
		}
		if _, err := walker.decoder.Token(); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		walker.out.WriteByte(']')
		return nil
	}
	walker.out.WriteByte('{')
	seen := map[string]bool{}
	written := 0
	for walker.decoder.More() {
		keyToken, err := walker.decoder.Token()
		if err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		name, ok := keyToken.(string)
		if !ok {
			return errors.New("invalid JSON: object key must be a string")
		}
		if seen[name] {
			return fmt.Errorf("JSON has duplicate key %q", name)
		}
		seen[name] = true
		mark := walker.out.Len()
		if written > 0 {
			walker.out.WriteByte(',')
		}
		if err := walker.writeScalar(name); err != nil {
			return err
		}
		walker.out.WriteByte(':')
		skipped, err := walker.value(childPath, name, true)
		if err != nil {
			return err
		}
		if skipped {
			walker.out.Truncate(mark)
			continue
		}
		written++
	}
	if _, err := walker.decoder.Token(); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	walker.out.WriteByte('}')
	return nil
}

// strictJSONChildPath is the path of the node (path, key); the root (nil
// path) has the empty path.
func strictJSONChildPath(path []string, key string) []string {
	if path == nil {
		return []string{}
	}
	return append(path[:len(path):len(path)], key)
}

func (walker *strictJSONWalker) writeScalar(token any) error {
	switch value := token.(type) {
	case nil:
		walker.out.WriteString("null")
	case bool:
		walker.out.WriteString(strconv.FormatBool(value))
	case json.Number:
		walker.out.WriteString(value.String())
	case string:
		walker.scratch.Reset()
		if err := walker.encoder.Encode(value); err != nil {
			return err
		}
		walker.out.Write(bytes.TrimSuffix(walker.scratch.Bytes(), []byte("\n")))
	default:
		return fmt.Errorf("invalid JSON token %T", token)
	}
	return nil
}
