// Package aistore provides normalized AI conversation persistence while keeping
// the frontend collection contract unchanged.
package aistore

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mincode/app/backend/core"
	"mincode/app/backend/storage"
)

const blobReferenceKey = "$aiAttachmentBlob"

func init() {
	core.Register("ai_store_load_workspace", loadWorkspaceHandler)
	core.Register("ai_store_list_workspaces", listWorkspacesHandler)
	core.Register("ai_store_load_conversation", loadConversationHandler)
	core.Register("ai_store_save_conversation", saveConversationHandler)
	core.Register("ai_store_save_workspace_meta", saveWorkspaceMetaHandler)
	core.Register("ai_store_delete_conversation", deleteConversationHandler)
	core.Register("ai_store_replace_conversation", replaceConversationHandler)
	core.Register("ai_store_move_conversation", moveConversationHandler)
}

type scopeArgs struct {
	ScopeID string `json:"scopeId"`
}

type conversationArgs struct {
	ScopeID              string                 `json:"scopeId"`
	ConversationID       string                 `json:"conversationId"`
	Conversation         json.RawMessage        `json:"conversation"`
	Position             int                    `json:"position"`
	ReplaceItems         bool                   `json:"replaceItems"`
	ItemPatch            *itemPatch             `json:"itemPatch"`
	ActiveConversationID string                 `json:"activeConversationId"`
	ConversationIDs      []string               `json:"conversationIds"`
	RelatedConversations []conversationLocation `json:"relatedConversations"`
}

type conversationLocation struct {
	ScopeID        string `json:"scopeId"`
	ConversationID string `json:"conversationId"`
}

type moveConversationArgs struct {
	SourceScopeID         string          `json:"sourceScopeId"`
	TargetScopeID         string          `json:"targetScopeId"`
	ConversationID        string          `json:"conversationId"`
	Conversation          json.RawMessage `json:"conversation"`
	SourceActiveID        string          `json:"sourceActiveConversationId"`
	SourceConversationIDs []string        `json:"sourceConversationIds"`
	TargetConversationIDs []string        `json:"targetConversationIds"`
}

type workspaceMetaArgs struct {
	ScopeID              string               `json:"scopeId"`
	ActiveConversationID string               `json:"activeConversationId"`
	ConversationIDs      []string             `json:"conversationIds"`
	Archive              *conversationArchive `json:"archive"`
}

type conversationArchive struct {
	Conversations []conversationLocation `json:"conversations"`
	Archived      bool                   `json:"archived"`
}

type attachmentBlob struct {
	Hash     string
	Kind     string
	MimeType string
	Encoding string
	Data     []byte
}

type itemIdentity struct {
	conversationID string
	kind           string
	position       int
}

var openAIStoreDB = storage.DB

// openAIStoreReadDB 用于纯查询，走只读连接池，不与写入排队。
var openAIStoreReadDB = storage.ReadDB

func loadWorkspaceHandler(_, argsJSON string) (string, error) {
	var args scopeArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.ScopeID) == "" {
		return "", errors.New("AI 存储 scopeId 不能为空")
	}
	db, err := openAIStoreReadDB()
	if err != nil {
		return "", err
	}
	collection, err := loadWorkspaceOverview(db, args.ScopeID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.EncodeResult(nil)
	}
	if err != nil {
		return "", err
	}
	return string(collection), nil
}

func listWorkspacesHandler(_, _ string) (string, error) {
	db, err := openAIStoreReadDB()
	if err != nil {
		return "", err
	}
	collections, err := listWorkspaceOverviews(db)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(collections)
}

func loadConversationHandler(_, argsJSON string) (string, error) {
	var args conversationArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.ScopeID) == "" || strings.TrimSpace(args.ConversationID) == "" {
		return "", errors.New("AI 会话定位参数不能为空")
	}
	db, err := openAIStoreReadDB()
	if err != nil {
		return "", err
	}
	conversation, err := loadConversation(db, args.ScopeID, args.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.EncodeResult(nil)
	}
	if err != nil {
		return "", err
	}
	return string(conversation), nil
}

func saveConversationHandler(_, argsJSON string) (string, error) {
	var args conversationArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.ScopeID) == "" || len(args.Conversation) == 0 || !json.Valid(args.Conversation) {
		return "", errors.New("AI 会话保存参数无效")
	}
	if args.ItemPatch != nil {
		if !args.ReplaceItems {
			return "", errors.New("AI 会话增量保存必须同时保存条目")
		}
		if err := args.ItemPatch.validate(); err != nil {
			return "", err
		}
	}
	db, err := openAIStoreDB()
	if err != nil {
		return "", err
	}
	itemRevisions.mu.Lock()
	defer itemRevisions.mu.Unlock()
	conversationID, err := saveConversation(db, args.ScopeID, args.Conversation, args.Position, args.ReplaceItems, args.ItemPatch)
	if err != nil {
		return "", err
	}
	if !args.ReplaceItems {
		return core.EncodeResult(saveConversationResult{})
	}
	return core.EncodeResult(saveConversationResult{ItemRevision: bumpItemRevision(args.ScopeID, conversationID)})
}

func saveWorkspaceMetaHandler(_, argsJSON string) (string, error) {
	var args workspaceMetaArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.Archive != nil {
		return archiveConversations(*args.Archive)
	}
	if strings.TrimSpace(args.ScopeID) == "" {
		return "", errors.New("AI 存储 scopeId 不能为空")
	}
	db, err := openAIStoreDB()
	if err != nil {
		return "", err
	}
	if err := saveWorkspaceMeta(db, args.ScopeID, args.ActiveConversationID, args.ConversationIDs); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func deleteConversationHandler(_, argsJSON string) (string, error) {
	var args conversationArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.ScopeID) == "" || strings.TrimSpace(args.ConversationID) == "" {
		return "", errors.New("AI 会话定位参数不能为空")
	}
	db, err := openAIStoreDB()
	if err != nil {
		return "", err
	}
	itemRevisions.mu.Lock()
	defer itemRevisions.mu.Unlock()
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM ai_conversations WHERE scope_id=? AND conversation_id=?`, args.ScopeID, args.ConversationID); err != nil {
		return "", err
	}
	if err := deleteRelatedConversations(tx, args.ScopeID, args.RelatedConversations); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE ai_workspaces SET active_conversation_id=?, updated_at=CURRENT_TIMESTAMP WHERE scope_id=?`, args.ActiveConversationID, args.ScopeID); err != nil {
		return "", err
	}
	for position, id := range args.ConversationIDs {
		if _, err := tx.Exec(`UPDATE ai_conversations SET position=? WHERE scope_id=? AND conversation_id=?`, position, args.ScopeID, id); err != nil {
			return "", err
		}
	}
	if err := deleteUnreferencedAttachments(tx); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	forgetItemRevision(args.ScopeID, args.ConversationID)
	for _, related := range args.RelatedConversations {
		forgetItemRevision(related.ScopeID, related.ConversationID)
	}
	return core.EncodeResult(nil)
}

func replaceConversationHandler(_, argsJSON string) (string, error) {
	var args conversationArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.ScopeID) == "" || strings.TrimSpace(args.ConversationID) == "" || len(args.Conversation) == 0 || !json.Valid(args.Conversation) {
		return "", errors.New("AI 会话原子替换参数无效")
	}
	db, err := openAIStoreDB()
	if err != nil {
		return "", err
	}
	itemRevisions.mu.Lock()
	defer itemRevisions.mu.Unlock()
	savedID, err := replaceConversation(db, args.ScopeID, args.ConversationID, args.Conversation, args.RelatedConversations...)
	if err != nil {
		return "", err
	}
	forgetItemRevision(args.ScopeID, args.ConversationID)
	forgetItemRevision(args.ScopeID, savedID)
	for _, related := range args.RelatedConversations {
		forgetItemRevision(related.ScopeID, related.ConversationID)
	}
	return core.EncodeResult(nil)
}

func archiveConversations(args conversationArchive) (string, error) {
	db, err := openAIStoreDB()
	if err != nil {
		return "", err
	}
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	for _, target := range args.Conversations {
		var raw []byte
		err := tx.QueryRow(`SELECT payload FROM ai_conversations WHERE scope_id=? AND conversation_id=?`, target.ScopeID, target.ConversationID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", err
		}
		payload, err := decodeObject(raw)
		if err != nil {
			return "", err
		}
		payload["archived"] = args.Archived
		updated, hash, err := encodePayload(payload)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(`UPDATE ai_conversations SET payload=?, payload_hash=?, updated_at=CURRENT_TIMESTAMP WHERE scope_id=? AND conversation_id=?`,
			updated, hash, target.ScopeID, target.ConversationID); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type workspaceOverview struct {
	ScopeID    string          `json:"scopeId"`
	Collection json.RawMessage `json:"collection"`
}

func listWorkspaceOverviews(db *sql.DB) ([]workspaceOverview, error) {
	rows, err := db.Query(`
		SELECT w.scope_id, w.active_conversation_id, c.payload
		FROM ai_workspaces w
		LEFT JOIN ai_conversations c ON c.scope_id = w.scope_id
		ORDER BY w.updated_at DESC, w.scope_id, c.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type overviewCollection struct {
		scopeID       string
		activeID      string
		conversations []any
	}
	collections := make([]overviewCollection, 0)
	collectionIndex := make(map[string]int)
	for rows.Next() {
		var scopeID, activeID string
		var payload []byte
		if err := rows.Scan(&scopeID, &activeID, &payload); err != nil {
			return nil, err
		}
		index, ok := collectionIndex[scopeID]
		if !ok {
			index = len(collections)
			collectionIndex[scopeID] = index
			collections = append(collections, overviewCollection{
				scopeID:       scopeID,
				activeID:      activeID,
				conversations: make([]any, 0),
			})
		}
		if len(payload) == 0 {
			continue
		}
		conversation, err := decodeObject(payload)
		if err != nil {
			return nil, err
		}
		for _, field := range []string{"messages", "agentHistory", "agentEvents", "messageQueue"} {
			conversation[field] = []any{}
		}
		conversation["detailsLoaded"] = false
		collections[index].conversations = append(collections[index].conversations, conversation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	overviews := make([]workspaceOverview, 0, len(collections))
	for _, collection := range collections {
		payload, err := json.Marshal(map[string]any{
			"version":              2,
			"activeConversationId": collection.activeID,
			"conversations":        collection.conversations,
		})
		if err != nil {
			return nil, err
		}
		overviews = append(overviews, workspaceOverview{
			ScopeID:    collection.scopeID,
			Collection: payload,
		})
	}
	return overviews, nil
}

func moveConversationHandler(_, argsJSON string) (string, error) {
	var args moveConversationArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.SourceScopeID) == "" || strings.TrimSpace(args.TargetScopeID) == "" ||
		strings.TrimSpace(args.ConversationID) == "" || args.SourceScopeID == args.TargetScopeID ||
		len(args.Conversation) == 0 || !json.Valid(args.Conversation) {
		return "", errors.New("AI 会话迁移参数无效")
	}
	db, err := openAIStoreDB()
	if err != nil {
		return "", err
	}
	itemRevisions.mu.Lock()
	defer itemRevisions.mu.Unlock()
	if err := moveConversation(db, args); err != nil {
		return "", err
	}
	forgetItemRevision(args.SourceScopeID, args.ConversationID)
	forgetItemRevision(args.TargetScopeID, args.ConversationID)
	return core.EncodeResult(nil)
}

func loadWorkspaceOverview(db *sql.DB, scopeID string) ([]byte, error) {
	var activeID string
	if err := db.QueryRow("SELECT active_conversation_id FROM ai_workspaces WHERE scope_id = ?", scopeID).Scan(&activeID); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT conversation_id, payload FROM ai_conversations WHERE scope_id = ? ORDER BY position`, scopeID)
	if err != nil {
		return nil, err
	}
	type overviewRow struct {
		id           string
		conversation map[string]any
	}
	metadata := make([]overviewRow, 0)
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return nil, err
		}
		conversation, err := decodeObject(payload)
		if err != nil {
			rows.Close()
			return nil, err
		}
		for _, field := range []string{"messages", "agentHistory", "agentEvents", "messageQueue"} {
			conversation[field] = []any{}
		}
		conversation["detailsLoaded"] = false
		metadata = append(metadata, overviewRow{id: id, conversation: conversation})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	conversations := make([]any, 0, len(metadata))
	for _, row := range metadata {
		conversations = append(conversations, row.conversation)
	}
	return json.Marshal(map[string]any{"version": 2, "activeConversationId": activeID, "conversations": conversations})
}

func loadConversation(db *sql.DB, scopeID, conversationID string) ([]byte, error) {
	conversation, err := loadConversationObject(db, scopeID, conversationID)
	if err != nil {
		return nil, err
	}
	conversation["detailsLoaded"] = true
	return json.Marshal(conversation)
}

func loadConversationObject(db *sql.DB, scopeID, conversationID string) (map[string]any, error) {
	var payload []byte
	if err := db.QueryRow(`SELECT payload FROM ai_conversations WHERE scope_id = ? AND conversation_id = ?`, scopeID, conversationID).Scan(&payload); err != nil {
		return nil, err
	}
	conversation, err := decodeObject(payload)
	if err != nil {
		return nil, err
	}
	blobs, err := loadConversationAttachmentBlobs(db, scopeID, conversationID)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT kind, payload FROM ai_conversation_items WHERE scope_id = ? AND conversation_id = ? ORDER BY kind, position`, scopeID, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string][]any{}
	for rows.Next() {
		var kind string
		var itemPayload []byte
		if err := rows.Scan(&kind, &itemPayload); err != nil {
			return nil, err
		}
		item, err := decodeAny(itemPayload)
		if err != nil {
			return nil, err
		}
		groups[kind] = append(groups[kind], restoreAttachments(item, blobs))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, group := range []struct{ field, kind string }{{"messages", "message"}, {"agentHistory", "history"}, {"agentEvents", "event"}, {"messageQueue", "queue"}} {
		conversation[group.field] = groups[group.kind]
	}
	return restoreAttachments(conversation, blobs).(map[string]any), nil
}

func loadConversationAttachmentBlobs(db *sql.DB, scopeID, conversationID string) (map[string]attachmentBlob, error) {
	rows, err := db.Query(`SELECT a.hash, a.kind, a.mime_type, a.encoding, a.data FROM ai_attachments a JOIN ai_attachment_refs r ON r.attachment_hash = a.hash WHERE r.scope_id = ? AND r.conversation_id = ?`, scopeID, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blobs := map[string]attachmentBlob{}
	for rows.Next() {
		var blob attachmentBlob
		if err := rows.Scan(&blob.Hash, &blob.Kind, &blob.MimeType, &blob.Encoding, &blob.Data); err != nil {
			return nil, err
		}
		blobs[blob.Hash] = blob
	}
	return blobs, rows.Err()
}

func saveWorkspaceMeta(db *sql.DB, scopeID, activeID string, conversationIDs []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO ai_workspaces(scope_id, active_conversation_id) VALUES(?, ?) ON CONFLICT(scope_id) DO UPDATE SET active_conversation_id = excluded.active_conversation_id, updated_at = CURRENT_TIMESTAMP`, scopeID, activeID); err != nil {
		return err
	}
	for position, id := range conversationIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, err := tx.Exec(`UPDATE ai_conversations SET position = ? WHERE scope_id = ? AND conversation_id = ?`, position, scopeID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func saveConversation(db *sql.DB, scopeID string, raw json.RawMessage, position int, replaceItems bool, patch *itemPatch) (string, error) {
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	conversationID, err := saveConversationTx(tx, scopeID, raw, position, replaceItems, patch)
	if err != nil {
		return "", err
	}
	return conversationID, tx.Commit()
}

var conversationItemGroups = []struct{ field, kind string }{{"messages", "message"}, {"agentHistory", "history"}, {"agentEvents", "event"}, {"messageQueue", "queue"}}

// saveConversationTx persists one conversation. A full save sends every item,
// and items are only decoded when their raw JSON changed: an item row's
// payload_hash is the SHA-256 of the item JSON exactly as the frontend sent it
// (rows written before this scheme simply rewrite once). A patch save (non-nil
// patch, itemRevisions.mu held) sends only the changed items and is rejected
// with errItemPatchConflict unless the stored rows are the ones it was diffed
// against. Attachment refs are diffed against the stored set, so unchanged
// blobs are never re-inserted and the orphan sweep only runs for refs that
// disappeared.
func saveConversationTx(tx *sql.Tx, scopeID string, raw json.RawMessage, position int, replaceItems bool, patch *itemPatch) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	if fields == nil {
		return "", errors.New("JSON 根节点不是对象")
	}
	var conversationID string
	_ = json.Unmarshal(fields["id"], &conversationID)
	if conversationID == "" {
		return "", errors.New("AI 会话 id 不能为空")
	}
	delete(fields, "detailsLoaded")
	var previousMetadataRefs map[string]struct{}
	if patch != nil {
		if patch.BaseRevision == 0 || currentItemRevision(scopeID, conversationID) != patch.BaseRevision {
			return "", errItemPatchConflict
		}
		refs, err := storedMetadataRefs(tx, scopeID, conversationID)
		if err != nil {
			return "", err
		}
		previousMetadataRefs = refs
	}
	if _, err := tx.Exec(`INSERT INTO ai_workspaces(scope_id, active_conversation_id) VALUES(?, '') ON CONFLICT(scope_id) DO NOTHING`, scopeID); err != nil {
		return "", err
	}
	groupItems := make(map[string][]json.RawMessage, len(conversationItemGroups))
	for _, group := range conversationItemGroups {
		var items []json.RawMessage
		// A non-array group is treated as empty, matching the previous decoder.
		_ = json.Unmarshal(fields[group.field], &items)
		groupItems[group.kind] = items
		delete(fields, group.field)
	}
	metadata := make(map[string]any, len(fields))
	for key, value := range fields {
		decoded, err := decodeAny(value)
		if err != nil {
			return "", err
		}
		metadata[key] = decoded
	}
	blobs := map[string]attachmentBlob{}
	wantedRefs := map[string]struct{}{}
	if replaceItems {
		metadata = externalizeAttachments(metadata, blobs, wantedRefs).(map[string]any)
	}
	payload, hash, err := encodePayload(metadata)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO ai_conversations(scope_id, conversation_id, position, payload, payload_hash) VALUES(?, ?, ?, ?, ?) ON CONFLICT(scope_id, conversation_id) DO UPDATE SET position=excluded.position, payload=excluded.payload, payload_hash=excluded.payload_hash, updated_at=CURRENT_TIMESTAMP`, scopeID, conversationID, position, payload, hash); err != nil {
		return "", err
	}
	if !replaceItems {
		return conversationID, nil
	}

	if patch != nil {
		dropped, err := applyItemPatchTx(tx, scopeID, conversationID, patch, blobs, wantedRefs)
		if err != nil {
			return "", err
		}
		for hashValue := range previousMetadataRefs {
			dropped[hashValue] = struct{}{}
		}
		wanted, err := patchedAttachmentRefs(tx, scopeID, conversationID, dropped, wantedRefs)
		if err != nil {
			return "", err
		}
		return conversationID, syncAttachmentRefs(tx, scopeID, conversationID, wanted, blobs)
	}

	existing, err := loadItemHashes(tx, scopeID, conversationID)
	if err != nil {
		return "", err
	}
	// Refs of rows that stay untouched come from their stored (already
	// externalized) payloads; only rows that mention a blob are read.
	storedRefs, err := loadItemAttachmentRefs(tx, scopeID, conversationID)
	if err != nil {
		return "", err
	}
	for _, group := range conversationItemGroups {
		items := groupItems[group.kind]
		for itemPosition, rawItem := range items {
			identity := itemIdentity{conversationID: conversationID, kind: group.kind, position: itemPosition}
			digest := sha256.Sum256(rawItem)
			if existing[identity] == string(digest[:]) {
				for hashValue := range storedRefs[identity] {
					wantedRefs[hashValue] = struct{}{}
				}
				continue
			}
			if err := writeItemTx(tx, scopeID, conversationID, group.kind, itemPosition, rawItem, digest, blobs, wantedRefs); err != nil {
				return "", err
			}
		}
		if hasStaleItems(existing, conversationID, group.kind, len(items)) {
			if _, err := tx.Exec(`DELETE FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? AND kind=? AND position>=?`, scopeID, conversationID, group.kind, len(items)); err != nil {
				return "", err
			}
		}
	}
	if err := syncAttachmentRefs(tx, scopeID, conversationID, wantedRefs, blobs); err != nil {
		return "", err
	}
	return conversationID, nil
}

// writeItemTx stores one item row, moving its attachments into blobs and
// recording their hashes in wantedRefs.
func writeItemTx(tx *sql.Tx, scopeID, conversationID, kind string, position int, rawItem []byte, digest [sha256.Size]byte, blobs map[string]attachmentBlob, wantedRefs map[string]struct{}) error {
	item, err := decodeAny(rawItem)
	if err != nil {
		return err
	}
	itemPayload, _, err := encodePayload(externalizeAttachments(item, blobs, wantedRefs))
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO ai_conversation_items(scope_id,conversation_id,kind,position,payload,payload_hash) VALUES(?,?,?,?,?,?) ON CONFLICT(scope_id,conversation_id,kind,position) DO UPDATE SET payload=excluded.payload,payload_hash=excluded.payload_hash`, scopeID, conversationID, kind, position, itemPayload, digest[:])
	return err
}

func loadItemHashes(tx *sql.Tx, scopeID, conversationID string) (map[itemIdentity]string, error) {
	rows, err := tx.Query(`SELECT kind,position,payload_hash FROM ai_conversation_items WHERE scope_id=? AND conversation_id=?`, scopeID, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	existing := map[itemIdentity]string{}
	for rows.Next() {
		identity := itemIdentity{conversationID: conversationID}
		var itemHash []byte
		if err := rows.Scan(&identity.kind, &identity.position, &itemHash); err != nil {
			return nil, err
		}
		existing[identity] = string(itemHash)
	}
	return existing, rows.Err()
}

func loadItemAttachmentRefs(tx *sql.Tx, scopeID, conversationID string) (map[itemIdentity]map[string]struct{}, error) {
	rows, err := tx.Query(`SELECT kind,position,payload FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? AND instr(payload, ?) > 0`, scopeID, conversationID, blobReferenceKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[itemIdentity]map[string]struct{}{}
	for rows.Next() {
		identity := itemIdentity{conversationID: conversationID}
		var itemPayload []byte
		if err := rows.Scan(&identity.kind, &identity.position, &itemPayload); err != nil {
			return nil, err
		}
		item, err := decodeAny(itemPayload)
		if err != nil {
			return nil, err
		}
		itemRefs := map[string]struct{}{}
		collectAttachmentRefs(item, itemRefs)
		refs[identity] = itemRefs
	}
	return refs, rows.Err()
}

func collectAttachmentRefs(value any, refs map[string]struct{}) {
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			collectAttachmentRefs(item, refs)
		}
	case map[string]any:
		if hash, ok := current[blobReferenceKey].(string); ok {
			refs[hash] = struct{}{}
			return
		}
		for _, nested := range current {
			collectAttachmentRefs(nested, refs)
		}
	}
}

// syncAttachmentRefs makes the conversation's ref rows equal to wanted. Blob
// rows are only written for newly referenced hashes, and only the hashes that
// lost a ref are checked for orphaning.
func syncAttachmentRefs(tx *sql.Tx, scopeID, conversationID string, wanted map[string]struct{}, blobs map[string]attachmentBlob) error {
	rows, err := tx.Query(`SELECT attachment_hash FROM ai_attachment_refs WHERE scope_id=? AND conversation_id=?`, scopeID, conversationID)
	if err != nil {
		return err
	}
	current := map[string]struct{}{}
	for rows.Next() {
		var hashValue string
		if err := rows.Scan(&hashValue); err != nil {
			rows.Close()
			return err
		}
		current[hashValue] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for hashValue := range wanted {
		if _, ok := current[hashValue]; ok {
			continue
		}
		if blob, ok := blobs[hashValue]; ok {
			if _, err := tx.Exec(`INSERT INTO ai_attachments(hash,kind,mime_type,encoding,data) VALUES(?,?,?,?,?) ON CONFLICT(hash) DO NOTHING`, blob.Hash, blob.Kind, blob.MimeType, blob.Encoding, blob.Data); err != nil {
				return err
			}
		}
		// A stored reference whose blob is already gone stays unresolved, as before.
		if _, err := tx.Exec(`INSERT INTO ai_attachment_refs(scope_id,conversation_id,attachment_hash) SELECT ?,?,? WHERE EXISTS (SELECT 1 FROM ai_attachments WHERE hash=?)`, scopeID, conversationID, hashValue, hashValue); err != nil {
			return err
		}
	}
	for hashValue := range current {
		if _, ok := wanted[hashValue]; ok {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM ai_attachment_refs WHERE scope_id=? AND conversation_id=? AND attachment_hash=?`, scopeID, conversationID, hashValue); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM ai_attachments WHERE hash=? AND NOT EXISTS (SELECT 1 FROM ai_attachment_refs WHERE attachment_hash=?)`, hashValue, hashValue); err != nil {
			return fmt.Errorf("清理未引用 AI 附件: %w", err)
		}
	}
	return nil
}

func moveConversation(db *sql.DB, args moveConversationArgs) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var sourcePosition int
	if err := tx.QueryRow(`SELECT position FROM ai_conversations WHERE scope_id=? AND conversation_id=?`, args.SourceScopeID, args.ConversationID).Scan(&sourcePosition); err != nil {
		return err
	}
	var targetCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM ai_conversations WHERE scope_id=? AND conversation_id=?`, args.TargetScopeID, args.ConversationID).Scan(&targetCount); err != nil {
		return err
	}
	if targetCount > 0 {
		return errors.New("目标工作区已存在同 ID 的 AI 会话")
	}
	if _, err := saveConversationTx(tx, args.TargetScopeID, args.Conversation, len(args.TargetConversationIDs), true, nil); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ai_conversations WHERE scope_id=? AND conversation_id=?`, args.SourceScopeID, args.ConversationID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE ai_workspaces SET active_conversation_id=?, updated_at=CURRENT_TIMESTAMP WHERE scope_id=?`, args.SourceActiveID, args.SourceScopeID); err != nil {
		return err
	}
	for position, id := range args.SourceConversationIDs {
		if _, err := tx.Exec(`UPDATE ai_conversations SET position=? WHERE scope_id=? AND conversation_id=?`, position, args.SourceScopeID, id); err != nil {
			return err
		}
	}
	targetIDs := append([]string{args.ConversationID}, args.TargetConversationIDs...)
	for position, id := range targetIDs {
		if _, err := tx.Exec(`UPDATE ai_conversations SET position=? WHERE scope_id=? AND conversation_id=?`, position, args.TargetScopeID, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE ai_workspaces SET active_conversation_id=?, updated_at=CURRENT_TIMESTAMP WHERE scope_id=?`, args.ConversationID, args.TargetScopeID); err != nil {
		return err
	}
	if err := deleteUnreferencedAttachments(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteRelatedConversations(tx *sql.Tx, primaryScopeID string, related []conversationLocation) error {
	affectedScopes := make(map[string]bool)
	for _, target := range related {
		if _, err := tx.Exec(`DELETE FROM ai_conversations WHERE scope_id=? AND conversation_id=?`, target.ScopeID, target.ConversationID); err != nil {
			return err
		}
		if target.ScopeID != primaryScopeID {
			affectedScopes[target.ScopeID] = true
		}
	}
	for scopeID := range affectedScopes {
		if _, err := tx.Exec(`UPDATE ai_workspaces SET active_conversation_id=COALESCE(
			(SELECT conversation_id FROM ai_conversations WHERE scope_id=? ORDER BY position LIMIT 1), ''),
			updated_at=CURRENT_TIMESTAMP
			WHERE scope_id=? AND NOT EXISTS (
				SELECT 1 FROM ai_conversations WHERE scope_id=? AND conversation_id=ai_workspaces.active_conversation_id
			)`, scopeID, scopeID, scopeID); err != nil {
			return err
		}
	}
	return nil
}

func replaceConversation(db *sql.DB, scopeID, removedConversationID string, raw json.RawMessage, related ...conversationLocation) (string, error) {
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM ai_conversations WHERE scope_id = ? AND conversation_id = ?`, scopeID, removedConversationID); err != nil {
		return "", err
	}
	if err := deleteRelatedConversations(tx, scopeID, related); err != nil {
		return "", err
	}
	conversationID, err := saveConversationTx(tx, scopeID, raw, 0, true, nil)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE ai_workspaces SET active_conversation_id = ?, updated_at = CURRENT_TIMESTAMP WHERE scope_id = ?`, conversationID, scopeID); err != nil {
		return "", err
	}
	if err := deleteUnreferencedAttachments(tx); err != nil {
		return "", err
	}
	return conversationID, tx.Commit()
}

func externalizeAttachments(value any, blobs map[string]attachmentBlob, refs map[string]struct{}) any {
	switch current := value.(type) {
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			result[index] = externalizeAttachments(item, blobs, refs)
		}
		return result
	case map[string]any:
		result := cloneObject(current)
		kind, _ := result["kind"].(string)
		mimeType, _ := result["mimeType"].(string)
		data, _ := result["data"].(string)
		id, idOK := result["id"].(string)
		name, nameOK := result["name"].(string)
		_, sizeOK := result["size"].(json.Number)
		isAttachment := idOK && id != "" && nameOK && name != "" && sizeOK && mimeType != ""
		if isAttachment && (kind == "image" || kind == "text") && data != "" {
			raw := []byte(data)
			encoding := "utf8"
			if kind == "image" {
				if decoded, err := base64.StdEncoding.DecodeString(data); err == nil && base64.StdEncoding.EncodeToString(decoded) == data {
					raw = decoded
					encoding = "base64"
				}
			}
			digest := sha256.Sum256(bytes.Join([][]byte{[]byte(kind), []byte(mimeType), []byte(encoding), raw}, []byte{0}))
			hash := hex.EncodeToString(digest[:])
			blobs[hash] = attachmentBlob{Hash: hash, Kind: kind, MimeType: mimeType, Encoding: encoding, Data: raw}
			refs[hash] = struct{}{}
			result["data"] = map[string]any{blobReferenceKey: hash}
			delete(result, "previewUrl")
		}
		for key, nested := range result {
			result[key] = externalizeAttachments(nested, blobs, refs)
		}
		return result
	default:
		return value
	}
}

func restoreAttachments(value any, blobs map[string]attachmentBlob) any {
	switch current := value.(type) {
	case []any:
		for index, item := range current {
			current[index] = restoreAttachments(item, blobs)
		}
		return current
	case map[string]any:
		if hash, ok := current[blobReferenceKey].(string); ok {
			blob, exists := blobs[hash]
			if !exists {
				return ""
			}
			if blob.Encoding == "base64" {
				return base64.StdEncoding.EncodeToString(blob.Data)
			}
			return string(blob.Data)
		}
		for key, nested := range current {
			current[key] = restoreAttachments(nested, blobs)
		}
		return current
	default:
		return value
	}
}

func encodePayload(value any) ([]byte, []byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, nil, err
	}
	hash := sha256.Sum256(payload)
	return payload, hash[:], nil
}

func decodeObject(raw []byte) (map[string]any, error) {
	value, err := decodeAny(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("JSON 根节点不是对象")
	}
	return object, nil
}

func decodeAny(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func cloneObject(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func hasStaleItems(items map[itemIdentity]string, conversationID, kind string, retainedCount int) bool {
	for identity := range items {
		if identity.conversationID == conversationID && identity.kind == kind && identity.position >= retainedCount {
			return true
		}
	}
	return false
}

func deleteUnreferencedAttachments(tx *sql.Tx) error {
	if _, err := tx.Exec(`DELETE FROM ai_attachments
		WHERE NOT EXISTS (SELECT 1 FROM ai_attachment_refs r WHERE r.attachment_hash = ai_attachments.hash)`); err != nil {
		return fmt.Errorf("清理未引用 AI 附件: %w", err)
	}
	return nil
}
