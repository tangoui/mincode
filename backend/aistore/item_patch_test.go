package aistore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func resetItemRevisions(t testing.TB) {
	t.Helper()
	itemRevisions.mu.Lock()
	itemRevisions.current = map[revisionKey]uint64{}
	itemRevisions.mu.Unlock()
}

func callSaveHandler(t testing.TB, args map[string]any) (uint64, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := saveConversationHandler("", string(raw))
	if err != nil {
		return 0, err
	}
	var decoded saveConversationResult
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("result %q: %v", result, err)
	}
	return decoded.ItemRevision, nil
}

func fullSave(t testing.TB, scopeID string, conversation map[string]any) uint64 {
	t.Helper()
	revision, err := callSaveHandler(t, map[string]any{"scopeId": scopeID, "conversation": conversation, "position": 0, "replaceItems": true})
	if err != nil {
		t.Fatal(err)
	}
	if revision == 0 {
		t.Fatal("full save returned no item revision")
	}
	return revision
}

func patchSave(t *testing.T, scopeID string, after map[string]any, patch map[string]any) (uint64, error) {
	t.Helper()
	return callSaveHandler(t, map[string]any{
		"scopeId": scopeID, "conversation": metadataOnly(after), "position": 0, "replaceItems": true, "itemPatch": patch,
	})
}

func itemJSON(t testing.TB, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// patchFor diffs two versions of a conversation the way the frontend does.
func patchFor(t testing.TB, baseRevision uint64, before, after map[string]any) map[string]any {
	t.Helper()
	groups := map[string]any{}
	for _, group := range conversationItemGroups {
		previous, _ := before[group.field].([]any)
		current, _ := after[group.field].([]any)
		changes := []any{}
		for position, item := range current {
			text := itemJSON(t, item)
			if position >= len(previous) || itemJSON(t, previous[position]) != text {
				changes = append(changes, map[string]any{"position": position, "item": text})
			}
		}
		groups[group.field] = map[string]any{"baseLength": len(previous), "length": len(current), "changes": changes}
	}
	return map[string]any{"baseRevision": baseRevision, "groups": groups}
}

func metadataOnly(conversation map[string]any) map[string]any {
	metadata := map[string]any{}
	for key, value := range conversation {
		metadata[key] = value
	}
	for _, group := range conversationItemGroups {
		delete(metadata, group.field)
	}
	return metadata
}

func cloneConversation(t *testing.T, conversation map[string]any) map[string]any {
	t.Helper()
	var clone map[string]any
	if err := json.Unmarshal([]byte(itemJSON(t, conversation)), &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func loadedDetail(t *testing.T, db *sql.DB, scopeID, conversationID string) map[string]any {
	t.Helper()
	raw, err := loadConversation(db, scopeID, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func itemHash(t *testing.T, db *sql.DB, scopeID, kind string, position int) []byte {
	t.Helper()
	var hash []byte
	if err := db.QueryRow(`SELECT payload_hash FROM ai_conversation_items WHERE scope_id=? AND conversation_id='conversation-1' AND kind=? AND position=?`, scopeID, kind, position).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}

// A streaming turn: the last answer grows, an event is updated, one is added
// and the queue empties.
func streamedVersion(t *testing.T, before map[string]any) map[string]any {
	t.Helper()
	after := cloneConversation(t, before)
	after["updatedAt"] = 1777347860000
	messages := after["messages"].([]any)
	messages[1].(map[string]any)["content"] = "world, continued"
	events := after["agentEvents"].([]any)
	events[0].(map[string]any)["detail"] = "finished"
	after["agentEvents"] = append(events, map[string]any{"id": "e2", "kind": "tool", "title": "second", "detail": "running"})
	after["messageQueue"] = []any{}
	return after
}

func TestItemPatchMatchesFullSaveAndLeavesUnchangedRows(t *testing.T) {
	db := setTestDatabase(t)
	resetItemRevisions(t)
	before := sampleConversation("conversation-1")
	before["messageQueue"] = []any{map[string]any{"id": "q1", "text": "later"}}
	revision := fullSave(t, "workspace:one", before)
	unchangedHash := itemHash(t, db, "workspace:one", "message", 0)

	after := streamedVersion(t, before)
	next, err := patchSave(t, "workspace:one", after, patchFor(t, revision, before, after))
	if err != nil {
		t.Fatal(err)
	}
	if next <= revision {
		t.Fatalf("patch revision = %d, want > %d", next, revision)
	}
	if got := itemHash(t, db, "workspace:one", "message", 0); string(got) != string(unchangedHash) {
		t.Fatal("unchanged message row was rewritten")
	}
	patched := loadedDetail(t, db, "workspace:one", "conversation-1")

	// The same final state saved in full must load identically.
	reference := setTestDatabase(t)
	resetItemRevisions(t)
	fullSave(t, "workspace:one", after)
	if want := loadedDetail(t, reference, "workspace:one", "conversation-1"); !reflect.DeepEqual(patched, want) {
		t.Fatalf("patched detail differs from full save:\n got %#v\nwant %#v", patched, want)
	}
	var attachments, refs int
	if err := reference.QueryRow("SELECT COUNT(*) FROM ai_attachments").Scan(&attachments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM ai_attachment_refs").Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if attachments != 1 || refs != 1 {
		t.Fatalf("attachment rows after patch = blobs %d refs %d, want 1/1", attachments, refs)
	}
}

func TestItemPatchShrinksGroupsAndExternalizesNewAttachments(t *testing.T) {
	db := setTestDatabase(t)
	resetItemRevisions(t)
	before := sampleConversation("conversation-1")
	before["messages"] = before["messages"].([]any)[:1]
	revision := fullSave(t, "workspace:one", before)

	after := cloneConversation(t, before)
	after["messages"] = append(after["messages"].([]any), map[string]any{
		"id": "m3", "role": "user", "content": "new image",
		"attachments": []any{map[string]any{"id": "image-2", "name": "b.png", "mimeType": "image/png", "size": 3, "kind": "image", "data": "eHl6"}},
	})
	after["agentHistory"] = []any{}
	if _, err := patchSave(t, "workspace:one", after, patchFor(t, revision, before, after)); err != nil {
		t.Fatal(err)
	}
	loaded := loadedDetail(t, db, "workspace:one", "conversation-1")
	if history, _ := loaded["agentHistory"].([]any); len(history) != 0 {
		t.Fatalf("agentHistory = %#v, want empty", loaded["agentHistory"])
	}
	messages := loaded["messages"].([]any)
	attachment := messages[1].(map[string]any)["attachments"].([]any)[0].(map[string]any)
	if len(messages) != 2 || attachment["data"] != "eHl6" {
		t.Fatalf("messages = %#v", messages)
	}
	var stored []byte
	if err := db.QueryRow(`SELECT payload FROM ai_conversation_items WHERE kind='message' AND position=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "eHl6") || !strings.Contains(string(stored), blobReferenceKey) {
		t.Fatalf("new attachment was not externalized: %s", stored)
	}
	var attachments, refs int
	if err := db.QueryRow("SELECT COUNT(*) FROM ai_attachments").Scan(&attachments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM ai_attachment_refs").Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if attachments != 2 || refs != 2 {
		t.Fatalf("attachments %d refs %d, want 2/2 (retained row keeps its ref)", attachments, refs)
	}
}

func TestItemPatchRejectsOutdatedBase(t *testing.T) {
	db := setTestDatabase(t)
	resetItemRevisions(t)
	before := sampleConversation("conversation-1")
	revision := fullSave(t, "workspace:one", before)
	after := streamedVersion(t, before)

	cases := map[string]func() map[string]any{
		"zero revision":  func() map[string]any { return patchFor(t, 0, before, after) },
		"older revision": func() map[string]any { return patchFor(t, revision-1, before, after) },
		"wrong base size": func() map[string]any {
			patch := patchFor(t, revision, before, after)
			groups := patch["groups"].(map[string]any)
			groups["messages"].(map[string]any)["baseLength"] = 1
			groups["messages"].(map[string]any)["changes"] = []any{
				map[string]any{"position": 1, "item": itemJSON(t, after["messages"].([]any)[1])},
			}
			return patch
		},
	}
	for name, patch := range cases {
		if _, err := patchSave(t, "workspace:one", after, patch()); !errors.Is(err, errItemPatchConflict) {
			t.Fatalf("%s: err = %v, want conflict", name, err)
		}
	}
	if loaded := loadedDetail(t, db, "workspace:one", "conversation-1"); loaded["messages"].([]any)[1].(map[string]any)["content"] != "world" {
		t.Fatal("a rejected patch changed the stored conversation")
	}

	// Another writer (a second window's full save) moves the revision on.
	other := fullSave(t, "workspace:one", after)
	if _, err := patchSave(t, "workspace:one", after, patchFor(t, revision, before, after)); !errors.Is(err, errItemPatchConflict) {
		t.Fatalf("patch after another save: err = %v, want conflict", err)
	}
	if _, err := patchSave(t, "workspace:one", after, patchFor(t, other, after, after)); err != nil {
		t.Fatalf("patch on the current revision: %v", err)
	}
}

func TestItemPatchConflictsAfterDeleteMoveAndReplace(t *testing.T) {
	setTestDatabase(t)
	resetItemRevisions(t)
	conversation := sampleConversation("conversation-1")
	patchOn := func(scopeID string, revision uint64) error {
		_, err := patchSave(t, scopeID, conversation, patchFor(t, revision, conversation, conversation))
		return err
	}

	revision := fullSave(t, "workspace:one", conversation)
	moveArgs, _ := json.Marshal(map[string]any{
		"sourceScopeId": "workspace:one", "targetScopeId": "workspace:two", "conversationId": "conversation-1",
		"conversation": conversation, "sourceConversationIds": []string{}, "targetConversationIds": []string{},
	})
	if _, err := moveConversationHandler("", string(moveArgs)); err != nil {
		t.Fatal(err)
	}
	if err := patchOn("workspace:two", revision); !errors.Is(err, errItemPatchConflict) {
		t.Fatalf("after move: err = %v", err)
	}

	revision = fullSave(t, "workspace:two", conversation)
	replaceArgs, _ := json.Marshal(map[string]any{"scopeId": "workspace:two", "conversationId": "conversation-1", "conversation": conversation})
	if _, err := replaceConversationHandler("", string(replaceArgs)); err != nil {
		t.Fatal(err)
	}
	if err := patchOn("workspace:two", revision); !errors.Is(err, errItemPatchConflict) {
		t.Fatalf("after replace: err = %v", err)
	}

	revision = fullSave(t, "workspace:two", conversation)
	deleteArgs, _ := json.Marshal(map[string]any{"scopeId": "workspace:two", "conversationId": "conversation-1", "conversationIds": []string{}})
	if _, err := deleteConversationHandler("", string(deleteArgs)); err != nil {
		t.Fatal(err)
	}
	if err := patchOn("workspace:two", revision); !errors.Is(err, errItemPatchConflict) {
		t.Fatalf("after delete: err = %v", err)
	}
}

func TestItemPatchValidation(t *testing.T) {
	setTestDatabase(t)
	resetItemRevisions(t)
	conversation := sampleConversation("conversation-1")
	revision := fullSave(t, "workspace:one", conversation)
	valid := func() map[string]any { return patchFor(t, revision, conversation, conversation) }
	group := func(patch map[string]any, field string) map[string]any {
		return patch["groups"].(map[string]any)[field].(map[string]any)
	}

	cases := map[string]func(map[string]any){
		"missing group":   func(p map[string]any) { delete(p["groups"].(map[string]any), "agentEvents") },
		"negative length": func(p map[string]any) { group(p, "messages")["length"] = -1 },
		"out of range": func(p map[string]any) {
			group(p, "messages")["changes"] = []any{map[string]any{"position": 2, "item": "{}"}}
		},
		"duplicate": func(p map[string]any) {
			group(p, "messages")["changes"] = []any{map[string]any{"position": 0, "item": "{}"}, map[string]any{"position": 0, "item": "{}"}}
		},
		"invalid json": func(p map[string]any) {
			group(p, "messages")["changes"] = []any{map[string]any{"position": 0, "item": "{"}}
		},
		"unsent new items": func(p map[string]any) { group(p, "messages")["length"] = 3 },
	}
	for name, mutate := range cases {
		patch := valid()
		mutate(patch)
		_, err := patchSave(t, "workspace:one", conversation, patch)
		if err == nil || errors.Is(err, errItemPatchConflict) {
			t.Fatalf("%s: err = %v, want a validation error", name, err)
		}
	}
	if _, err := callSaveHandler(t, map[string]any{
		"scopeId": "workspace:one", "conversation": metadataOnly(conversation), "replaceItems": false, "itemPatch": valid(),
	}); err == nil {
		t.Fatal("a patch without replaceItems must be rejected")
	}
}

func TestMetadataSaveKeepsItemRevision(t *testing.T) {
	setTestDatabase(t)
	resetItemRevisions(t)
	conversation := sampleConversation("conversation-1")
	revision := fullSave(t, "workspace:one", conversation)
	if returned, err := callSaveHandler(t, map[string]any{"scopeId": "workspace:one", "conversation": metadataOnly(conversation), "replaceItems": false}); err != nil || returned != 0 {
		t.Fatalf("metadata save = %d, %v", returned, err)
	}
	if _, err := patchSave(t, "workspace:one", conversation, patchFor(t, revision, conversation, conversation)); err != nil {
		t.Fatalf("patch after a metadata save: %v", err)
	}
}

// largeConversation approximates a long agent run: ~10k items, ~10 MB of JSON.
func largeConversation() map[string]any {
	text := func(n, i int) string { return strings.Repeat("x", n) + strconv.Itoa(i) }
	messages := make([]any, 3300)
	for i := range messages {
		messages[i] = map[string]any{"id": "m" + strconv.Itoa(i), "role": "assistant", "content": text(330, i)}
	}
	events := make([]any, 6565)
	for i := range events {
		events[i] = map[string]any{"id": "e" + strconv.Itoa(i), "kind": "tool", "title": "workspace.read_file", "detail": text(1300, i), "status": "completed"}
	}
	return map[string]any{"id": "conversation-1", "title": "long", "messages": messages, "agentHistory": []any{}, "agentEvents": events, "messageQueue": []any{}}
}

// BenchmarkStreamingSave compares one streaming save of a long conversation
// whose last message grew: resending every item versus a one-item patch.
func BenchmarkStreamingSave(b *testing.B) {
	versions := [2]map[string]any{largeConversation(), largeConversation()}
	versions[1]["messages"].([]any)[3299].(map[string]any)["content"] = "grown"
	for _, mode := range []string{"full", "patch"} {
		b.Run(mode, func(b *testing.B) {
			setTestDatabase(b)
			resetItemRevisions(b)
			fullSave(b, "workspace:one", versions[0])
			// Diffing happens in the frontend; only the Go side is timed.
			patches := [2]map[string]any{patchFor(b, 0, versions[1], versions[0]), patchFor(b, 0, versions[0], versions[1])}
			var payloadBytes int
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				next := (i + 1) % 2
				args := map[string]any{"scopeId": "workspace:one", "conversation": versions[next], "replaceItems": true}
				if mode == "patch" {
					patches[next]["baseRevision"] = currentItemRevision("workspace:one", "conversation-1")
					args["conversation"] = metadataOnly(versions[next])
					args["itemPatch"] = patches[next]
				}
				b.StopTimer()
				raw, _ := json.Marshal(args)
				payloadBytes = len(raw)
				b.StartTimer()
				if _, err := saveConversationHandler("", string(raw)); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(payloadBytes), "payload-bytes")
		})
	}
}

func attachmentCounts(t testing.TB, db *sql.DB) (int, int) {
	t.Helper()
	var attachments, refs int
	if err := db.QueryRow("SELECT COUNT(*) FROM ai_attachments").Scan(&attachments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM ai_attachment_refs").Scan(&refs); err != nil {
		t.Fatal(err)
	}
	return attachments, refs
}

func TestItemPatchReleasesAttachmentsOnlyWhenNoRowUsesThem(t *testing.T) {
	db := setTestDatabase(t)
	resetItemRevisions(t)
	// Both sample messages carry the same image.
	before := sampleConversation("conversation-1")
	revision := fullSave(t, "workspace:one", before)

	// Dropping the image from one message keeps it: the other still uses it.
	after := cloneConversation(t, before)
	delete(after["messages"].([]any)[1].(map[string]any), "attachments")
	revision, err := patchSave(t, "workspace:one", after, patchFor(t, revision, before, after))
	if err != nil {
		t.Fatal(err)
	}
	if attachments, refs := attachmentCounts(t, db); attachments != 1 || refs != 1 {
		t.Fatalf("shared image released early: blobs %d refs %d", attachments, refs)
	}

	// Replacing the last message that used it releases it.
	trimmed := cloneConversation(t, after)
	trimmed["messages"] = []any{map[string]any{"id": "m1", "role": "user", "content": "plain"}, after["messages"].([]any)[1]}
	revision, err = patchSave(t, "workspace:one", trimmed, patchFor(t, revision, after, trimmed))
	if err != nil {
		t.Fatal(err)
	}
	if attachments, refs := attachmentCounts(t, db); attachments != 0 || refs != 0 {
		t.Fatalf("unused image kept: blobs %d refs %d", attachments, refs)
	}

	// An image only on a row that is then removed by shrinking the group.
	withImage := cloneConversation(t, trimmed)
	withImage["messages"] = append(withImage["messages"].([]any), map[string]any{
		"id": "m3", "role": "user", "content": "img",
		"attachments": []any{map[string]any{"id": "i", "name": "c.png", "mimeType": "image/png", "size": 3, "kind": "image", "data": "cXJz"}},
	})
	revision, err = patchSave(t, "workspace:one", withImage, patchFor(t, revision, trimmed, withImage))
	if err != nil {
		t.Fatal(err)
	}
	if attachments, refs := attachmentCounts(t, db); attachments != 1 || refs != 1 {
		t.Fatalf("new image not stored: blobs %d refs %d", attachments, refs)
	}
	if _, err := patchSave(t, "workspace:one", trimmed, patchFor(t, revision, withImage, trimmed)); err != nil {
		t.Fatal(err)
	}
	if attachments, refs := attachmentCounts(t, db); attachments != 0 || refs != 0 {
		t.Fatalf("trimmed row's image kept: blobs %d refs %d", attachments, refs)
	}
}
