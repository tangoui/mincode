package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Terminal link rules (contributes.terminalLinks) are data only. Each rule is
// a JavaScript regular expression plus a target: a URL template whose scheme
// and host are fixed by the plugin (captures may only fill the path, query or
// fragment, percent-encoded by the frontend), or a local file position whose
// path, line and column come from captures. The frontend is authoritative for
// compiling the regex (JavaScript dialect, "u" flag) and runs the safety
// heuristics, the worker probe and a per-line time budget; the host only
// rejects constructs outside the RE2/JS common subset that make matching
// expensive or opaque (backreferences, lookaround, named groups, \p{...}).
// File targets are limited to local terminals because remote paths do not
// exist on this machine. Nothing is rendered as HTML or executed.

const terminalLinkPointKey = "terminalLinks"

var (
	terminalLinkSpec             = mustContributionSpec(terminalLinkPointKey)
	terminalLinkMaxPatternLength = int(terminalLinkSpec.Limit("patternMaxLength"))
	terminalLinkMaxURLLength     = int(terminalLinkSpec.Limit("urlMaxLength"))
	terminalLinkTerminals        = terminalLinkSpec.List("terminals")
	terminalLinkTargetTypes      = terminalLinkSpec.List("targetTypes")
	terminalLinkSchemes          = terminalLinkSpec.List("schemes")

	terminalLinkIDPattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	terminalLinkPlaceholderPattern = regexp.MustCompile(`\{([0-9])\}`)
	terminalLinkCapturePattern     = regexp.MustCompile(`^\{([0-9])\}$`)
)

// terminalLinkMaxGroups caps capturing groups; placeholders are one digit.
const terminalLinkMaxGroups = 9

// TerminalLinkContribution is one validated contributes.terminalLinks entry
// as sent to the frontend consumer.
type TerminalLinkContribution struct {
	ID         string             `json:"id"`
	Pattern    string             `json:"pattern"`
	IgnoreCase bool               `json:"ignoreCase"`
	Target     TerminalLinkTarget `json:"target"`
	Terminals  []string           `json:"terminals"`
}

// TerminalLinkTarget is a URL template (type "url") or a file position whose
// fields are capture placeholders (type "file").
type TerminalLinkTarget struct {
	Type   string `json:"type"`
	URL    string `json:"url,omitempty"`
	Path   string `json:"path,omitempty"`
	Line   string `json:"line,omitempty"`
	Column string `json:"column,omitempty"`
}

type terminalLinkEntryReference struct {
	ID         string                       `json:"id"`
	Pattern    string                       `json:"pattern"`
	IgnoreCase *bool                        `json:"ignoreCase"`
	Target     *terminalLinkTargetReference `json:"target"`
	Terminals  []string                     `json:"terminals"`
}

type terminalLinkTargetReference struct {
	Type   string  `json:"type"`
	URL    *string `json:"url"`
	Path   *string `json:"path"`
	Line   *string `json:"line"`
	Column *string `json:"column"`
}

func init() {
	registerContributionPoint(&contributionPoint{
		Key:              terminalLinkPointKey,
		Decode:           decodeTerminalLinkEntry,
		DuplicateMessage: func(id string) string { return fmt.Sprintf("duplicate terminal link id %q", id) },
		IssuePrefix:      "invalid terminalLinks contribution",
		IssueText: func(issue ContributionIssue) string {
			if issue.ID != "" {
				return fmt.Sprintf("skipped terminal link %q: %s", issue.ID, issue.Message)
			}
			return fmt.Sprintf("skipped contributes.terminalLinks[%d]: %s", issue.Entry, issue.Message)
		},
		EntryNoun: "terminal links",
	})
}

func decodeTerminalLinkEntry(_ *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
	var reference terminalLinkEntryReference
	if err := decodeStrictJSON(raw, &reference, true); err != nil {
		return nil, "", fmt.Errorf("parse terminal link entry: %w", err)
	}
	id := strings.TrimSpace(reference.ID)
	if !terminalLinkIDPattern.MatchString(id) {
		return nil, id, fmt.Errorf("terminal link id %q must match ^[a-z0-9][a-z0-9-]{0,31}$", reference.ID)
	}
	groups, err := validateTerminalLinkPattern(reference.Pattern)
	if err != nil {
		return nil, id, fmt.Errorf("pattern %w", err)
	}
	item := TerminalLinkContribution{ID: id, Pattern: reference.Pattern}
	if reference.IgnoreCase != nil {
		item.IgnoreCase = *reference.IgnoreCase
	}
	if reference.Target == nil {
		return nil, id, errors.New("target is required")
	}
	if item.Target, err = validateTerminalLinkTarget(*reference.Target, groups); err != nil {
		return nil, id, fmt.Errorf("target %w", err)
	}
	if reference.Terminals == nil {
		if item.Target.Type == "file" {
			item.Terminals = []string{"local"}
		} else {
			item.Terminals = append([]string(nil), terminalLinkTerminals...)
		}
	} else if item.Terminals, err = commandSnippetSubset("terminals", reference.Terminals, terminalLinkTerminals); err != nil {
		return nil, id, err
	}
	if item.Target.Type == "file" {
		for _, terminal := range item.Terminals {
			if terminal != "local" {
				return nil, id, fmt.Errorf("file targets are only allowed in local terminals, not %q", terminal)
			}
		}
	}
	return item, id, nil
}

// validateTerminalLinkPattern checks the regex source and returns its number
// of capturing groups. It does not compile the pattern (the frontend uses the
// JavaScript dialect) but rejects backreferences, lookaround, named groups,
// inline modifiers and \p{...} escapes.
func validateTerminalLinkPattern(pattern string) (int, error) {
	if !utf8.ValidString(pattern) {
		return 0, errors.New("must be valid UTF-8")
	}
	length := utf8.RuneCountInString(pattern)
	if length == 0 {
		return 0, errors.New("is required")
	}
	if length > terminalLinkMaxPatternLength {
		return 0, fmt.Errorf("must be at most %d characters", terminalLinkMaxPatternLength)
	}
	if err := checkContributionRunes(pattern, false, false); err != nil {
		return 0, err
	}
	groups := 0
	inClass := false
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '\\':
			if index+1 >= len(pattern) {
				return 0, errors.New("must not end with a backslash")
			}
			next := pattern[index+1]
			switch {
			case next >= '1' && next <= '9':
				return 0, errors.New("must not use backreferences")
			case next == 'k' && index+2 < len(pattern) && pattern[index+2] == '<':
				return 0, errors.New("must not use backreferences")
			case next == 'p' || next == 'P':
				return 0, errors.New(`must not use \p{...} escapes`)
			}
			index++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '(':
			if inClass {
				continue
			}
			if index+1 < len(pattern) && pattern[index+1] == '?' {
				rest := pattern[index+2:]
				switch {
				case strings.HasPrefix(rest, ":"):
				case strings.HasPrefix(rest, "="), strings.HasPrefix(rest, "!"):
					return 0, errors.New("must not use lookahead")
				case strings.HasPrefix(rest, "<="), strings.HasPrefix(rest, "<!"):
					return 0, errors.New("must not use lookbehind")
				case strings.HasPrefix(rest, "<"):
					return 0, errors.New("must not use named groups")
				default:
					return 0, errors.New("must only use (?:...) non-capturing groups")
				}
				continue
			}
			groups++
			if groups > terminalLinkMaxGroups {
				return 0, fmt.Errorf("must have at most %d capturing groups", terminalLinkMaxGroups)
			}
		}
	}
	return groups, nil
}

func validateTerminalLinkTarget(reference terminalLinkTargetReference, groups int) (TerminalLinkTarget, error) {
	if !slices.Contains(terminalLinkTargetTypes, reference.Type) {
		return TerminalLinkTarget{}, fmt.Errorf("type must be one of %s", strings.Join(terminalLinkTargetTypes, ", "))
	}
	target := TerminalLinkTarget{Type: reference.Type}
	if reference.Type == "url" {
		if reference.Path != nil || reference.Line != nil || reference.Column != nil {
			return TerminalLinkTarget{}, errors.New(`url targets only accept "url"`)
		}
		if reference.URL == nil {
			return TerminalLinkTarget{}, errors.New("url is required")
		}
		if err := validateTerminalLinkURL(*reference.URL, groups); err != nil {
			return TerminalLinkTarget{}, fmt.Errorf("url %w", err)
		}
		target.URL = *reference.URL
		return target, nil
	}
	if reference.URL != nil {
		return TerminalLinkTarget{}, errors.New(`file targets do not accept "url"`)
	}
	if reference.Path == nil {
		return TerminalLinkTarget{}, errors.New("path is required")
	}
	var err error
	if target.Path, err = terminalLinkCapture("path", *reference.Path, groups); err != nil {
		return TerminalLinkTarget{}, err
	}
	if reference.Line != nil {
		if target.Line, err = terminalLinkCapture("line", *reference.Line, groups); err != nil {
			return TerminalLinkTarget{}, err
		}
	}
	if reference.Column != nil {
		if target.Column, err = terminalLinkCapture("column", *reference.Column, groups); err != nil {
			return TerminalLinkTarget{}, err
		}
	}
	return target, nil
}

func terminalLinkCapture(field, value string, groups int) (string, error) {
	match := terminalLinkCapturePattern.FindStringSubmatch(value)
	if match == nil {
		return "", fmt.Errorf(`%s must be a capture placeholder such as "{1}"`, field)
	}
	if number, _ := strconv.Atoi(match[1]); number > groups {
		return "", fmt.Errorf("%s refers to capture {%d} but the pattern has %d groups", field, number, groups)
	}
	return value, nil
}

// validateTerminalLinkURL checks a URL template: an absolute http(s) URL with
// a fixed host and no credentials. Placeholders {n} (n ≤ capture count, {0}
// is the whole match) may appear only after the first "/" following the host.
func validateTerminalLinkURL(template string, groups int) error {
	if !utf8.ValidString(template) || template == "" {
		return errors.New("is required")
	}
	if len(template) > terminalLinkMaxURLLength {
		return fmt.Errorf("must be at most %d bytes", terminalLinkMaxURLLength)
	}
	if err := checkContributionRunes(template, false, false); err != nil {
		return err
	}
	if strings.ContainsAny(template, " \\") {
		return errors.New("must not contain spaces or backslashes")
	}
	scheme := ""
	for _, allowed := range terminalLinkSchemes {
		if strings.HasPrefix(template, allowed+"://") {
			scheme = allowed
			break
		}
	}
	if scheme == "" {
		return fmt.Errorf("must start with %s://", strings.Join(terminalLinkSchemes, ":// or "))
	}
	authorityStart := len(scheme) + len("://")
	pathStart := strings.IndexAny(template[authorityStart:], "/?#")
	if pathStart < 0 || template[authorityStart+pathStart] != '/' {
		if terminalLinkPlaceholderPattern.MatchString(template) {
			return errors.New("placeholders are only allowed after the host (in the path, query or fragment)")
		}
	} else if terminalLinkPlaceholderPattern.MatchString(template[:authorityStart+pathStart]) {
		return errors.New("placeholders are only allowed after the host (in the path, query or fragment)")
	}
	for _, match := range terminalLinkPlaceholderPattern.FindAllStringSubmatch(template, -1) {
		if number, _ := strconv.Atoi(match[1]); number > groups {
			return fmt.Errorf("refers to capture {%d} but the pattern has %d groups", number, groups)
		}
	}
	filled := terminalLinkPlaceholderPattern.ReplaceAllString(template, "x")
	if strings.ContainsAny(filled, "{}") {
		return errors.New(`must only use "{n}" placeholders`)
	}
	parsed, err := url.Parse(filled)
	if err != nil {
		return errors.New("must be a valid absolute URL")
	}
	if parsed.Scheme != scheme || parsed.User != nil || parsed.Hostname() == "" || parsed.Opaque != "" {
		return errors.New("must be an absolute URL with a host and no credentials")
	}
	return nil
}
