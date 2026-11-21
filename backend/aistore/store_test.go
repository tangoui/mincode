package aistore

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

func setTestDatabase(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE ai_workspaces (scope_id TEXT PRIMARY KEY, active_conversation_id TEXT NOT NULL DEFAULT '', updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE ai_conversations (scope_id TEXT NOT NULL, conversation_id TEXT NOT NULL, position INTEGER NOT NULL, payload BLOB NOT NULL, payload_hash BLOB NOT NULL, updated_at TEXT DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(scope_id, conversation_id), FOREIGN KEY(scope_id) REFERENCES ai_workspaces(scope_id) ON DELETE CASCADE)`,
		`CREATE TABLE ai_conversation_items (scope_id TEXT NOT NULL, conversation_id TEXT NOT NULL, kind TEXT NOT NULL, position INTEGER NOT NULL, payload BLOB NOT NULL, payload_hash BLOB NOT NULL, PRIMARY KEY(scope_id, conversation_id, kind, position), FOREIGN KEY(scope_id, conversation_id) REFERENCES ai_conversations(scope_id, conversation_id) ON DELETE CASCADE)`,
		`CREATE TABLE ai_attachments (hash TEXT PRIMARY KEY, kind TEXT NOT NULL, mime_type TEXT NOT NULL, encoding TEXT NOT NULL, data BLOB NOT NULL, created_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE ai_attachment_refs (scope_id TEXT NOT NULL, conversation_id TEXT NOT NULL, attachment_hash TEXT NOT NULL, PRIMARY KEY(scope_id, conversation_id, attachment_hash), FOREIGN KEY(scope_id, conversation_id) REFERENCES ai_conversations(scope_id, conversation_id) ON DELETE CASCADE, FOREIGN KEY(attachment_hash) REFERENCES ai_attachments(hash) ON DELETE CASCADE)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	original, originalRead := openAIStoreDB, openAIStoreReadDB
	openAIStoreDB = func() (*sql.DB, error) { return db, nil }
	openAIStoreReadDB = openAIStoreDB
	t.Cleanup(func() {
		openAIStoreDB = original
		openAIStoreReadDB = originalRead
		db.Close()
	})
	return db
}

func sampleConversation(id string) map[string]any {
	image := map[string]any{
		"id": "image-1", "name": "pixel.png", "mimeType": "image/png",
		"size": 3, "kind": "image", "data": "YWJj",
	}
	return map[string]any{
		"id": id, "version": 1, "mode": "agentic", "title": id,
		"createdAt": 1777347858856, "updatedAt": 1777347859999,
		"archived": false, "pinned": true, "status": "completed", "running": false,
		"messages": []any{
			map[string]any{"id": "m1", "role": "user", "content": "hello", "attachments": []any{image}},
			map[string]any{"id": "m2", "role": "assistant", "content": "world", "attachments": []any{image}},
		},
		"agentHistory": []any{map[string]any{"role": "assistant", "content": "history"}},
		"agentEvents":  []any{map[string]any{"id": "e1", "kind": "tool", "title": "tool", "detail": "detail"}},
		"messageQueue": []any{}, "acpServerId": "server", "acpSessionId": "", "agentReady": false,
	}
}

func saveTestConversation(t *testing.T, db *sql.DB, scopeID string, conversation map[string]any, position int) {
	t.Helper()
	raw, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveConversation(db, scopeID, raw, position, true, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConversationFamilyArchiveAndDeleteAcrossWorkspaces(t *testing.T) {
	db := setTestDatabase(t)
	saveTestConversation(t, db, "root", sampleConversation("parent"), 0)
	saveTestConversation(t, db, "other", sampleConversation("child"), 0)
	saveTestConversation(t, db, "other", sampleConversation("keep"), 1)
	if err := saveWorkspaceMeta(db, "other", "child", []string{"child", "keep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveWorkspaceMetaHandler("", `{"archive":{"archived":true,"conversations":[{"scopeId":"root","conversationId":"parent"},{"scopeId":"other","conversationId":"child"}]}}`); err != nil {
		t.Fatal(err)
	}
	child, err := loadConversationObject(db, "other", "child")
	if err != nil || child["archived"] != true || len(child["messages"].([]any)) != 2 {
		t.Fatalf("archived detail = %v, %v", child, err)
	}
	if _, err := replaceConversationHandler("", `{"scopeId":"root","conversationId":"parent","conversation":{"id":"new"},"relatedConversations":[{"scopeId":"other","conversationId":"child"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConversationObject(db, "other", "child"); err != sql.ErrNoRows {
		t.Fatalf("loading deleted child = %v", err)
	}
	var active string
	if err := db.QueryRow("SELECT active_conversation_id FROM ai_workspaces WHERE scope_id='other'").Scan(&active); err != nil || active != "keep" {
		t.Fatalf("other workspace active ID = %q, %v", active, err)
	}
}

func TestConversationDetailRoundTripAndAttachmentDeduplication(t *testing.T) {
	db := setTestDatabase(t)
	conversation := sampleConversation("conversation-1")
	saveTestConversation(t, db, "workspace:one", conversation, 0)

	loadedRaw, err := loadConversation(db, "workspace:one", "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeObject(loadedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["detailsLoaded"] != true || len(loaded["messages"].([]any)) != 2 {
		t.Fatalf("detail not restored: %#v", loaded)
	}
	var attachments, refs int
	if err := db.QueryRow("SELECT COUNT(*) FROM ai_attachments").Scan(&attachments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM ai_attachment_refs").Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if attachments != 1 || refs != 1 {
		t.Fatalf("attachment deduplication = blobs %d refs %d, want 1/1", attachments, refs)
	}
}

func TestIncrementalDetailSaveKeepsUnchangedPayloadAndDeletesStaleRows(t *testing.T) {
	db := setTestDatabase(t)
	conversation := sampleConversation("conversation-1")
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	var firstHash []byte
	if err := db.QueryRow(`SELECT payload_hash FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? AND kind='message' AND position=0`, "workspace:one", "conversation-1").Scan(&firstHash); err != nil {
		t.Fatal(err)
	}

	messages := conversation["messages"].([]any)
	messages[1].(map[string]any)["content"] = "changed"
	conversation["messages"] = messages[:1]
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	var retainedHash []byte
	if err := db.QueryRow(`SELECT payload_hash FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? AND kind='message' AND position=0`, "workspace:one", "conversation-1").Scan(&retainedHash); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstHash, retainedHash) {
		t.Fatal("unchanged message payload hash changed")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? AND kind='message'`, "workspace:one", "conversation-1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("message rows=%d want 1", count)
	}
}

func TestWorkspaceOverviewDefersConversationDetails(t *testing.T) {
	db := setTestDatabase(t)
	saveTestConversation(t, db, "workspace:one", sampleConversation("conversation-1"), 0)
	saveTestConversation(t, db, "workspace:one", sampleConversation("conversation-2"), 1)
	if err := saveWorkspaceMeta(db, "workspace:one", "conversation-1", []string{"conversation-1", "conversation-2"}); err != nil {
		t.Fatal(err)
	}

	overviewRaw, err := loadWorkspaceOverview(db, "workspace:one")
	if err != nil {
		t.Fatal(err)
	}
	overview, err := decodeObject(overviewRaw)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range overview["conversations"].([]any) {
		conversation := value.(map[string]any)
		if conversation["detailsLoaded"] != false || len(conversation["messages"].([]any)) != 0 {
			t.Fatalf("overview unexpectedly loaded details: %#v", conversation)
		}
	}
}

func TestMetadataOnlySavePreservesItems(t *testing.T) {
	db := setTestDatabase(t)
	conversation := sampleConversation("conversation-1")
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	conversation["title"] = "renamed"
	conversation["messages"] = []any{}
	raw, _ := json.Marshal(conversation)
	if _, err := saveConversation(db, "workspace:one", raw, 0, false, nil); err != nil {
		t.Fatal(err)
	}
	loadedRaw, err := loadConversation(db, "workspace:one", "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := decodeObject(loadedRaw)
	if loaded["title"] != "renamed" || len(loaded["messages"].([]any)) != 2 {
		t.Fatalf("metadata save lost details: %#v", loaded)
	}
}

func TestWorkspaceMetaUpdatesOnlyActiveConversationAndOrder(t *testing.T) {
	db := setTestDatabase(t)
	saveTestConversation(t, db, "workspace:one", sampleConversation("conversation-1"), 0)
	saveTestConversation(t, db, "workspace:one", sampleConversation("conversation-2"), 1)
	if err := saveWorkspaceMeta(db, "workspace:one", "conversation-2", []string{"conversation-2", "conversation-1"}); err != nil {
		t.Fatal(err)
	}
	var activeID, firstID string
	if err := db.QueryRow(`SELECT active_conversation_id FROM ai_workspaces WHERE scope_id=?`, "workspace:one").Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT conversation_id FROM ai_conversations WHERE scope_id=? ORDER BY position LIMIT 1`, "workspace:one").Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	if activeID != "conversation-2" || firstID != "conversation-2" {
		t.Fatalf("active/order=%q/%q", activeID, firstID)
	}
}

func TestReplaceConversationIsAtomic(t *testing.T) {
	db := setTestDatabase(t)
	saveTestConversation(t, db, "workspace:one", sampleConversation("old"), 0)
	if err := saveWorkspaceMeta(db, "workspace:one", "old", []string{"old"}); err != nil {
		t.Fatal(err)
	}
	newRaw, _ := json.Marshal(sampleConversation("new"))
	if _, err := replaceConversation(db, "workspace:one", "old", newRaw); err != nil {
		t.Fatal(err)
	}
	var activeID string
	if err := db.QueryRow(`SELECT active_conversation_id FROM ai_workspaces WHERE scope_id=?`, "workspace:one").Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	var oldCount, newCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM ai_conversations WHERE scope_id=? AND conversation_id='old'`, "workspace:one").Scan(&oldCount)
	_ = db.QueryRow(`SELECT COUNT(*) FROM ai_conversations WHERE scope_id=? AND conversation_id='new'`, "workspace:one").Scan(&newCount)
	if activeID != "new" || oldCount != 0 || newCount != 1 {
		t.Fatalf("replace result active=%q old=%d new=%d", activeID, oldCount, newCount)
	}

	invalidRaw := json.RawMessage(`{"title":"missing id"}`)
	if _, err := replaceConversation(db, "workspace:one", "new", invalidRaw); err == nil {
		t.Fatal("invalid replacement unexpectedly succeeded")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_conversations WHERE scope_id=? AND conversation_id='new'`, "workspace:one").Scan(&newCount); err != nil {
		t.Fatal(err)
	}
	if newCount != 1 {
		t.Fatal("failed replacement did not roll back")
	}
}

func TestListWorkspaceOverviewsReturnsEveryScopeWithoutDetails(t *testing.T) {
	db := setTestDatabase(t)
	conversationOne := []byte(`{"id":"conversation-one","title":"One","messages":[{"role":"user","content":"secret-one"}],"agentHistory":[{"role":"user"}],"agentEvents":[{"id":"event-one"}],"messageQueue":[{"id":"queued-one"}]}`)
	conversationTwo := []byte(`{"id":"conversation-two","title":"Two","messages":[{"role":"user","content":"secret-two"}]}`)
	if _, err := saveConversation(db, "workspace:one", conversationOne, 0, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := saveConversation(db, "workspace:two", conversationTwo, 0, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ai_workspaces(scope_id, active_conversation_id) VALUES('workspace:empty', '')`); err != nil {
		t.Fatal(err)
	}

	overviews, err := listWorkspaceOverviews(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(overviews) != 3 {
		t.Fatalf("expected 3 workspace overviews, got %d", len(overviews))
	}

	byScope := make(map[string]map[string]any, len(overviews))
	for _, overview := range overviews {
		var collection map[string]any
		if err := json.Unmarshal(overview.Collection, &collection); err != nil {
			t.Fatal(err)
		}
		byScope[overview.ScopeID] = collection
	}
	emptyConversations, ok := byScope["workspace:empty"]["conversations"].([]any)
	if !ok || len(emptyConversations) != 0 {
		t.Fatalf("expected empty workspace overview, got %#v", byScope["workspace:empty"])
	}
	for scopeID, conversationID := range map[string]string{
		"workspace:one": "conversation-one",
		"workspace:two": "conversation-two",
	} {
		collection := byScope[scopeID]
		conversations, ok := collection["conversations"].([]any)
		if !ok || len(conversations) != 1 {
			t.Fatalf("expected one conversation for %s, got %#v", scopeID, collection["conversations"])
		}
		conversation := conversations[0].(map[string]any)
		if conversation["id"] != conversationID || conversation["detailsLoaded"] != false {
			t.Fatalf("unexpected overview for %s: %#v", scopeID, conversation)
		}
		for _, field := range []string{"messages", "agentHistory", "agentEvents", "messageQueue"} {
			items, ok := conversation[field].([]any)
			if !ok || len(items) != 0 {
				t.Fatalf("expected %s to be stripped for %s, got %#v", field, scopeID, conversation[field])
			}
		}
	}
}

func TestMoveConversationPreservesDetailsAndAttachments(t *testing.T) {
	db := setTestDatabase(t)
	moved := sampleConversation("conversation-moved")
	remaining := sampleConversation("conversation-remaining")
	target := sampleConversation("conversation-target")
	saveTestConversation(t, db, "workspace:source", moved, 0)
	saveTestConversation(t, db, "workspace:source", remaining, 1)
	saveTestConversation(t, db, "workspace:target", target, 0)
	if err := saveWorkspaceMeta(db, "workspace:source", "conversation-moved", []string{"conversation-moved", "conversation-remaining"}); err != nil {
		t.Fatal(err)
	}

	moved["workspaceId"] = "workspace:target"
	moved["workspaceName"] = "Target"
	moved["projectRoot"] = "/tmp/target"
	raw, err := json.Marshal(moved)
	if err != nil {
		t.Fatal(err)
	}
	if err := moveConversation(db, moveConversationArgs{
		SourceScopeID:         "workspace:source",
		TargetScopeID:         "workspace:target",
		ConversationID:        "conversation-moved",
		Conversation:          raw,
		SourceActiveID:        "conversation-remaining",
		SourceConversationIDs: []string{"conversation-remaining"},
		TargetConversationIDs: []string{"conversation-target"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := loadConversation(db, "workspace:source", "conversation-moved"); err == nil {
		t.Fatal("source conversation still exists")
	}
	loadedRaw, err := loadConversation(db, "workspace:target", "conversation-moved")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeObject(loadedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["workspaceId"] != "workspace:target" || loaded["projectRoot"] != "/tmp/target" {
		t.Fatalf("ownership not moved: %#v", loaded)
	}
	messages, ok := loaded["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("message details lost: %#v", loaded["messages"])
	}
	attachment := messages[0].(map[string]any)["attachments"].([]any)[0].(map[string]any)
	if attachment["data"] != "YWJj" {
		t.Fatalf("attachment data lost: %#v", attachment)
	}
	var sourceActive, targetActive, targetFirst string
	if err := db.QueryRow(`SELECT active_conversation_id FROM ai_workspaces WHERE scope_id='workspace:source'`).Scan(&sourceActive); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT active_conversation_id FROM ai_workspaces WHERE scope_id='workspace:target'`).Scan(&targetActive); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT conversation_id FROM ai_conversations WHERE scope_id='workspace:target' ORDER BY position LIMIT 1`).Scan(&targetFirst); err != nil {
		t.Fatal(err)
	}
	if sourceActive != "conversation-remaining" || targetActive != "conversation-moved" || targetFirst != "conversation-moved" {
		t.Fatalf("unexpected metadata source=%q target=%q first=%q", sourceActive, targetActive, targetFirst)
	}
}

// installWriteLog records every write the save path makes to item and
// attachment tables, so tests can assert that unchanged data is skipped.
func installWriteLog(t *testing.T, db *sql.DB) func() map[string]int {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE write_log (op TEXT NOT NULL)`,
		`CREATE TRIGGER log_item_insert AFTER INSERT ON ai_conversation_items BEGIN INSERT INTO write_log VALUES('item'); END`,
		`CREATE TRIGGER log_item_update AFTER UPDATE ON ai_conversation_items BEGIN INSERT INTO write_log VALUES('item'); END`,
		`CREATE TRIGGER log_blob_insert AFTER INSERT ON ai_attachments BEGIN INSERT INTO write_log VALUES('blob'); END`,
		`CREATE TRIGGER log_blob_delete AFTER DELETE ON ai_attachments BEGIN INSERT INTO write_log VALUES('blob-delete'); END`,
		`CREATE TRIGGER log_ref_insert AFTER INSERT ON ai_attachment_refs BEGIN INSERT INTO write_log VALUES('ref'); END`,
		`CREATE TRIGGER log_ref_delete AFTER DELETE ON ai_attachment_refs BEGIN INSERT INTO write_log VALUES('ref-delete'); END`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return func() map[string]int {
		t.Helper()
		rows, err := db.Query(`SELECT op, COUNT(*) FROM write_log GROUP BY op`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		counts := map[string]int{}
		for rows.Next() {
			var op string
			var count int
			if err := rows.Scan(&op, &count); err != nil {
				t.Fatal(err)
			}
			counts[op] = count
		}
		if _, err := db.Exec(`DELETE FROM write_log`); err != nil {
			t.Fatal(err)
		}
		return counts
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestStreamingSaveWritesOnlyChangedItemsAndKeepsAttachmentRefs(t *testing.T) {
	db := setTestDatabase(t)
	writes := installWriteLog(t, db)
	conversation := sampleConversation("conversation-1")
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	if got := writes(); got["item"] != 4 || got["blob"] != 1 || got["ref"] != 1 {
		t.Fatalf("initial save writes = %#v", got)
	}

	saveTestConversation(t, db, "workspace:one", conversation, 0)
	if got := writes(); len(got) != 0 {
		t.Fatalf("identical save wrote %#v, want nothing", got)
	}

	// A streaming tick appends a growing assistant message.
	messages := conversation["messages"].([]any)
	streaming := map[string]any{"id": "m3", "role": "assistant", "content": "par"}
	conversation["messages"] = append(messages, streaming)
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	streaming["content"] = "partial answer"
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	if got := writes(); got["item"] != 2 || len(got) != 1 {
		t.Fatalf("streaming saves wrote %#v, want only the streaming item twice", got)
	}

	loadedRaw, err := loadConversation(db, "workspace:one", "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := decodeObject(loadedRaw)
	loadedMessages := loaded["messages"].([]any)
	if len(loadedMessages) != 3 || loadedMessages[2].(map[string]any)["content"] != "partial answer" {
		t.Fatalf("streamed message not saved: %#v", loadedMessages)
	}
	attachment := loadedMessages[1].(map[string]any)["attachments"].([]any)[0].(map[string]any)
	if attachment["data"] != "YWJj" {
		t.Fatalf("attachment of unchanged message lost: %#v", attachment)
	}
	if refs := countRows(t, db, `SELECT COUNT(*) FROM ai_attachment_refs`); refs != 1 {
		t.Fatalf("refs=%d want 1", refs)
	}
}

func TestSaveRemovesOnlyOrphanedAttachments(t *testing.T) {
	db := setTestDatabase(t)
	shared := sampleConversation("conversation-shared")
	saveTestConversation(t, db, "workspace:one", shared, 0)
	conversation := sampleConversation("conversation-1")
	saveTestConversation(t, db, "workspace:one", conversation, 1)

	text := map[string]any{"id": "text-1", "name": "note.txt", "mimeType": "text/plain", "size": 4, "kind": "text", "data": "note"}
	messages := conversation["messages"].([]any)
	messages[0].(map[string]any)["attachments"] = []any{text}
	saveTestConversation(t, db, "workspace:one", conversation, 1)
	if blobs := countRows(t, db, `SELECT COUNT(*) FROM ai_attachments`); blobs != 2 {
		t.Fatalf("blobs=%d want 2", blobs)
	}

	writes := installWriteLog(t, db)
	// Dropping every attachment from conversation-1: the text blob becomes
	// orphaned, the image blob is still used by conversation-shared.
	for _, message := range messages {
		delete(message.(map[string]any), "attachments")
	}
	saveTestConversation(t, db, "workspace:one", conversation, 1)
	if got := writes(); got["ref-delete"] != 2 || got["blob-delete"] != 1 || got["blob"] != 0 || got["ref"] != 0 {
		t.Fatalf("attachment removal writes = %#v", got)
	}
	if refs := countRows(t, db, `SELECT COUNT(*) FROM ai_attachment_refs WHERE conversation_id='conversation-1'`); refs != 0 {
		t.Fatalf("conversation-1 refs=%d want 0", refs)
	}
	if blobs := countRows(t, db, `SELECT COUNT(*) FROM ai_attachments`); blobs != 1 {
		t.Fatalf("blobs=%d want the shared image only", blobs)
	}
	loadedRaw, err := loadConversation(db, "workspace:one", "conversation-shared")
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := decodeObject(loadedRaw)
	attachment := loaded["messages"].([]any)[0].(map[string]any)["attachments"].([]any)[0].(map[string]any)
	if attachment["data"] != "YWJj" {
		t.Fatalf("shared attachment lost: %#v", attachment)
	}
}

func TestSaveRewritesRowsWithLegacyPayloadHash(t *testing.T) {
	db := setTestDatabase(t)
	conversation := sampleConversation("conversation-1")
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	// Rows written before raw-JSON hashing carry a different digest.
	if _, err := db.Exec(`UPDATE ai_conversation_items SET payload_hash = X'00'`); err != nil {
		t.Fatal(err)
	}
	writes := installWriteLog(t, db)
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	if got := writes(); got["item"] != 4 || got["blob"] != 0 || got["ref"] != 0 || got["ref-delete"] != 0 {
		t.Fatalf("legacy rewrite writes = %#v", got)
	}
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	if got := writes(); len(got) != 0 {
		t.Fatalf("second save after rewrite wrote %#v", got)
	}
}

// The AI assistant records which ACP agent started a conversation
// (acpAdapterId) next to acpSessionId; a restart must get it back so a plugin
// agent's session is never handed to another agent.
func TestConversationKeepsAcpAdapterIDAcrossSavesAndLoads(t *testing.T) {
	db := setTestDatabase(t)
	conversation := sampleConversation("conversation-1")
	conversation["acpSessionId"] = "session-1"
	conversation["acpAdapterId"] = "plugin:acme.agents/helper"
	saveTestConversation(t, db, "workspace:one", conversation, 0)
	if err := saveWorkspaceMeta(db, "workspace:one", "conversation-1", []string{"conversation-1"}); err != nil {
		t.Fatal(err)
	}

	check := func(stage string) {
		t.Helper()
		loadedRaw, err := loadConversation(db, "workspace:one", "conversation-1")
		if err != nil {
			t.Fatal(err)
		}
		loaded, _ := decodeObject(loadedRaw)
		if loaded["acpAdapterId"] != conversation["acpAdapterId"] || loaded["acpSessionId"] != "session-1" {
			t.Fatalf("%s: detail lost the agent record: %#v", stage, loaded)
		}
		overviewRaw, err := loadWorkspaceOverview(db, "workspace:one")
		if err != nil {
			t.Fatal(err)
		}
		overview, _ := decodeObject(overviewRaw)
		row := overview["conversations"].([]any)[0].(map[string]any)
		if row["acpAdapterId"] != conversation["acpAdapterId"] {
			t.Fatalf("%s: overview lost the agent record: %#v", stage, row)
		}
	}
	check("detail save")

	// The frontend's metadata-only save (replaceItems=false) keeps it too.
	conversation["title"] = "renamed"
	conversation["acpAdapterId"] = "claude-code"
	raw, _ := json.Marshal(conversation)
	if _, err := saveConversation(db, "workspace:one", raw, 0, false, nil); err != nil {
		t.Fatal(err)
	}
	check("metadata save")
}
