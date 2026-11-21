package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Validation of what a session source's read action returns. Session text
// is untrusted: single-line fields (id, title, detail, model) are rejected
// when they carry control (Cc) or format (Cf) characters or U+2028/U+2029;
// message text is turned into plain text (ANSI sequences, controls and format
// characters removed, newlines and tabs kept). Invalid items are dropped and
// counted, never repaired into something else. Nothing here is logged.
// Mirror: vue-app/src/domain/plugins/session-sources.ts (frontend revalidation).
var (
	sessionSourceListResultMaxBytes     = int(sessionSourceSpec.Limit("listResultMaxBytes"))
	sessionSourceMessagesResultMaxBytes = int(sessionSourceSpec.Limit("messagesResultMaxBytes"))
	sessionSourceSessionsMax            = int(sessionSourceSpec.Limit("sessionsMax"))
	sessionSourceSessionIDMaxLength     = int(sessionSourceSpec.Limit("sessionIdMaxLength"))
	sessionSourceSessionTitleMaxRunes   = int(sessionSourceSpec.Limit("sessionTitleMaxRunes"))
	sessionSourceSessionDetailMaxRunes  = int(sessionSourceSpec.Limit("sessionDetailMaxRunes"))
	sessionSourceModelMaxRunes          = int(sessionSourceSpec.Limit("modelMaxRunes"))
	sessionSourceMessageCountMax        = int64(sessionSourceSpec.Limit("messageCountMax"))
	sessionSourceMessagesMax            = int(sessionSourceSpec.Limit("messagesMax"))
	sessionSourceMessageTextMaxRunes    = int(sessionSourceSpec.Limit("messageTextMaxRunes"))
	sessionSourceTimestampMax           = int64(sessionSourceSpec.Limit("timestampMax"))
	sessionSourceRoles                  = contributionListSet(sessionSourceSpec.List("roles"))
)

// SessionSummary is one validated row of a source's session list.
type SessionSummary struct {
	ID           string `json:"id"`
	Title        string `json:"title"` // empty: the frontend shows the id
	Detail       string `json:"detail,omitempty"`
	Model        string `json:"model,omitempty"`
	LastAt       *int64 `json:"lastAt,omitempty"`
	MessageCount *int64 `json:"messageCount,omitempty"`
}

// SessionMessage is one validated transcript message (plain text only).
type SessionMessage struct {
	Role      string `json:"role"`
	Text      string `json:"text"`
	Timestamp *int64 `json:"timestamp,omitempty"`
	Model     string `json:"model,omitempty"`
	// Truncated marks text cut to messageTextMaxRunes.
	Truncated bool `json:"truncated,omitempty"`
}

type sessionListResponse struct {
	Sessions  []SessionSummary `json:"sessions"`
	Dropped   int              `json:"dropped"`
	Truncated bool             `json:"truncated"`
}

type sessionMessagesResponse struct {
	Messages  []SessionMessage `json:"messages"`
	Dropped   int              `json:"dropped"`
	Truncated bool             `json:"truncated"`
}

type sessionListRawResult struct {
	Sessions []json.RawMessage `json:"sessions"`
}

type sessionRawSummary struct {
	ID           string  `json:"id"`
	Title        *string `json:"title,omitempty"`
	Detail       *string `json:"detail,omitempty"`
	Model        *string `json:"model,omitempty"`
	LastAt       *int64  `json:"lastAt,omitempty"`
	MessageCount *int64  `json:"messageCount,omitempty"`
}

type sessionMessagesRawResult struct {
	Messages []json.RawMessage `json:"messages"`
}

type sessionRawMessage struct {
	Role      string  `json:"role"`
	Text      *string `json:"text"`
	Timestamp *int64  `json:"timestamp,omitempty"`
	Model     *string `json:"model,omitempty"`
}

// parseSessionListResult validates {"sessions": [...]}. The top level is
// strict; sessions past sessionsMax are cut (truncated), invalid or
// duplicate ones are dropped and counted, and the plugin's order is kept.
func parseSessionListResult(raw json.RawMessage) (sessionListResponse, error) {
	var result sessionListRawResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return sessionListResponse{}, fmt.Errorf("result must be {\"sessions\": [...]}: %v", err)
	}
	if result.Sessions == nil {
		return sessionListResponse{}, errors.New("result must contain sessions")
	}
	response := sessionListResponse{Sessions: []SessionSummary{}}
	rawSessions := result.Sessions
	if len(rawSessions) > sessionSourceSessionsMax {
		response.Truncated = true
		rawSessions = rawSessions[:sessionSourceSessionsMax]
	}
	seen := map[string]bool{}
	for _, rawSession := range rawSessions {
		session, err := normalizeSessionSummary(rawSession)
		if err != nil || seen[session.ID] {
			response.Dropped++
			continue
		}
		seen[session.ID] = true
		response.Sessions = append(response.Sessions, session)
	}
	return response, nil
}

func normalizeSessionSummary(raw json.RawMessage) (SessionSummary, error) {
	var item sessionRawSummary
	if err := decodeStrictJSON(raw, &item, true); err != nil {
		return SessionSummary{}, err
	}
	if !validSessionID(item.ID) {
		return SessionSummary{}, errors.New("session id is invalid")
	}
	out := SessionSummary{ID: item.ID}
	var err error
	if out.Title, err = sessionLine(item.Title, sessionSourceSessionTitleMaxRunes); err != nil {
		return SessionSummary{}, fmt.Errorf("title %w", err)
	}
	if out.Detail, err = sessionLine(item.Detail, sessionSourceSessionDetailMaxRunes); err != nil {
		return SessionSummary{}, fmt.Errorf("detail %w", err)
	}
	if out.Model, err = sessionLine(item.Model, sessionSourceModelMaxRunes); err != nil {
		return SessionSummary{}, fmt.Errorf("model %w", err)
	}
	if item.LastAt != nil && (*item.LastAt < 0 || *item.LastAt > sessionSourceTimestampMax) {
		return SessionSummary{}, fmt.Errorf("lastAt must be 0-%d", sessionSourceTimestampMax)
	}
	if item.MessageCount != nil && (*item.MessageCount < 0 || *item.MessageCount > sessionSourceMessageCountMax) {
		return SessionSummary{}, fmt.Errorf("messageCount must be 0-%d", sessionSourceMessageCountMax)
	}
	out.LastAt, out.MessageCount = item.LastAt, item.MessageCount
	return out, nil
}

// parseSessionMessagesResult validates {"messages": [...]}. The top level is
// strict; only the last messagesMax messages are kept (truncated), invalid
// ones are dropped and counted, and the plugin's order is kept.
func parseSessionMessagesResult(raw json.RawMessage) (sessionMessagesResponse, error) {
	var result sessionMessagesRawResult
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return sessionMessagesResponse{}, fmt.Errorf("result must be {\"messages\": [...]}: %v", err)
	}
	if result.Messages == nil {
		return sessionMessagesResponse{}, errors.New("result must contain messages")
	}
	response := sessionMessagesResponse{Messages: []SessionMessage{}}
	rawMessages := result.Messages
	if len(rawMessages) > sessionSourceMessagesMax {
		response.Truncated = true
		rawMessages = rawMessages[len(rawMessages)-sessionSourceMessagesMax:]
	}
	for _, rawMessage := range rawMessages {
		message, err := normalizeSessionMessage(rawMessage)
		if err != nil {
			response.Dropped++
			continue
		}
		response.Messages = append(response.Messages, message)
	}
	return response, nil
}

func normalizeSessionMessage(raw json.RawMessage) (SessionMessage, error) {
	var item sessionRawMessage
	if err := decodeStrictJSON(raw, &item, true); err != nil {
		return SessionMessage{}, err
	}
	if !sessionSourceRoles[item.Role] {
		return SessionMessage{}, fmt.Errorf("role %q is not a known role", item.Role)
	}
	if item.Text == nil {
		return SessionMessage{}, errors.New("text is required")
	}
	if item.Timestamp != nil && (*item.Timestamp < 0 || *item.Timestamp > sessionSourceTimestampMax) {
		return SessionMessage{}, fmt.Errorf("timestamp must be 0-%d", sessionSourceTimestampMax)
	}
	model, err := sessionLine(item.Model, sessionSourceModelMaxRunes)
	if err != nil {
		return SessionMessage{}, fmt.Errorf("model %w", err)
	}
	text, truncated := sessionMessageText(*item.Text)
	return SessionMessage{Role: item.Role, Text: text, Timestamp: item.Timestamp, Model: model, Truncated: truncated}, nil
}

// validSessionID accepts a plugin-chosen session id: single-line text of at
// most sessionIdMaxLength bytes without surrounding spaces, control or
// format characters.
func validSessionID(value string) bool {
	return validateContributionItemID(value, sessionSourceSessionIDMaxLength) && !strings.ContainsFunc(value, contextRuneRefused)
}

// sessionLine validates an optional single-line field; absent and null are
// empty. Control and format characters and U+2028/U+2029 are rejected.
func sessionLine(value *string, maxRunes int) (string, error) {
	if value == nil {
		return "", nil
	}
	line, err := validateContributionLine(*value, maxRunes, false)
	if err != nil {
		return "", err
	}
	if strings.ContainsFunc(line, contextRuneRefused) {
		return "", errors.New("contains a control or format character")
	}
	return line, nil
}

// sessionMessageText is message text as plain text: format (Cf) characters
// are removed, then sanitizeContributionText removes ANSI sequences, the
// other controls and the separators, normalizes line ends to LF and cuts the
// text to messageTextMaxRunes. Markup and links stay literal characters.
func sessionMessageText(value string) (string, bool) {
	value = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
	return sanitizeContributionText(value, sessionSourceMessageTextMaxRunes)
}
