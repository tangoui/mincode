package aistore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/core"
)

const (
	defaultMessageSearchLimit = 30
	maxMessageSearchLimit     = 100
	maxMessageSearchQueryLen  = 200
	messageSnippetRadius      = 60
	// Bounds the rows decoded when the query cannot be pre-filtered in SQL.
	maxMessageSearchScanRows = 20_000
)

type searchMessagesArgs struct {
	Query string `json:"query"`
	// ScopeIDs limits the search to these workspaces; empty searches all of them.
	ScopeIDs []string `json:"scopeIds"`
	Limit    int      `json:"limit"`
}

type messageSearchMatch struct {
	ScopeID        string `json:"scopeId"`
	ConversationID string `json:"conversationId"`
	Title          string `json:"title"`
	MessageID      string `json:"messageId"`
	Role           string `json:"role"`
	Snippet        string `json:"snippet"`
	CreatedAt      int64  `json:"createdAt,omitempty"`
}

func init() {
	core.Register("ai_store_search_messages", searchMessagesHandler)
}

func searchMessagesHandler(_, argsJSON string) (string, error) {
	var args searchMessagesArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" || utf8.RuneCountInString(query) > maxMessageSearchQueryLen {
		return "", errors.New("AI 消息搜索关键字为空或过长")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultMessageSearchLimit
	}
	limit = min(limit, maxMessageSearchLimit)
	db, err := openAIStoreDB()
	if err != nil {
		return "", err
	}
	matches, err := searchMessages(db, query, args.ScopeIDs, limit)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(matches)
}

// jsonEscapesQuery reports whether json.Marshal would escape part of the query,
// in which case the raw payload cannot be pre-filtered with instr().
func jsonEscapesQuery(query string) bool {
	for _, r := range query {
		if r < 0x20 || r == '"' || r == '\\' || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029 {
			return true
		}
	}
	return false
}

// searchMessages finds saved conversation messages whose text contains query
// (case-insensitive), newest conversations first.
func searchMessages(db *sql.DB, query string, scopeIDs []string, limit int) ([]messageSearchMatch, error) {
	sqlText := `SELECT i.scope_id, i.conversation_id, i.payload, c.payload
		FROM ai_conversation_items i
		JOIN ai_conversations c ON c.scope_id = i.scope_id AND c.conversation_id = i.conversation_id
		WHERE i.kind = 'message'`
	args := []any{}
	if !jsonEscapesQuery(query) {
		sqlText += ` AND instr(lower(CAST(i.payload AS TEXT)), lower(?)) > 0`
		args = append(args, query)
	}
	if len(scopeIDs) > 0 {
		sqlText += ` AND i.scope_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(scopeIDs)), ",") + `)`
		for _, scopeID := range scopeIDs {
			args = append(args, scopeID)
		}
	}
	sqlText += ` ORDER BY c.updated_at DESC, i.conversation_id, i.position LIMIT ?`
	args = append(args, maxMessageSearchScanRows)

	rows, err := db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	needle := strings.ToLower(query)
	matches := []messageSearchMatch{}
	titles := map[string]string{}
	for rows.Next() && len(matches) < limit {
		var scopeID, conversationID string
		var messagePayload, conversationPayload []byte
		if err := rows.Scan(&scopeID, &conversationID, &messagePayload, &conversationPayload); err != nil {
			return nil, err
		}
		var message struct {
			ID           string `json:"id"`
			Role         string `json:"role"`
			Content      string `json:"content"`
			CreatedAt    int64  `json:"createdAt"`
			Presentation string `json:"presentation"`
		}
		if json.Unmarshal(messagePayload, &message) != nil || message.ID == "" || message.Presentation == "error" {
			continue
		}
		snippet, ok := matchSnippet(message.Content, needle)
		if !ok {
			continue
		}
		key := scopeID + "\x00" + conversationID
		title, known := titles[key]
		if !known {
			var conversation struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(conversationPayload, &conversation)
			title = conversation.Title
			titles[key] = title
		}
		matches = append(matches, messageSearchMatch{
			ScopeID:        scopeID,
			ConversationID: conversationID,
			Title:          title,
			MessageID:      message.ID,
			Role:           message.Role,
			Snippet:        snippet,
			CreatedAt:      message.CreatedAt,
		})
	}
	return matches, rows.Err()
}

// matchSnippet returns the text around the first case-insensitive match,
// collapsed to one line, or false when content does not contain needle.
func matchSnippet(content, needle string) (string, bool) {
	lower := strings.ToLower(content)
	at := strings.Index(lower, needle)
	if at < 0 || len(lower) != len(content) {
		// ToLower can change byte lengths for some scripts; fall back to a rune scan.
		runes := []rune(content)
		needleRunes := []rune(needle)
		at = -1
		for i := 0; i+len(needleRunes) <= len(runes); i++ {
			if strings.EqualFold(string(runes[i:i+len(needleRunes)]), string(needleRunes)) {
				at = len(string(runes[:i]))
				break
			}
		}
		if at < 0 {
			return "", false
		}
	}
	start := at
	for count := 0; start > 0 && count < messageSnippetRadius; count++ {
		_, size := utf8.DecodeLastRuneInString(content[:start])
		start -= size
	}
	end := at + len(needle)
	if end > len(content) {
		end = len(content)
	}
	for count := 0; end < len(content) && count < messageSnippetRadius; count++ {
		_, size := utf8.DecodeRuneInString(content[end:])
		end += size
	}
	snippet := strings.Join(strings.Fields(content[start:end]), " ")
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(content) {
		snippet += "…"
	}
	return snippet, true
}
