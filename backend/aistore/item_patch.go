package aistore

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// errItemPatchConflict tells the frontend that its record of the stored items
// is out of date (another window, a move/replace/delete, an app restart or a
// failed save changed them); it must resend the full conversation instead.
var errItemPatchConflict = errors.New("ai_store_item_patch_conflict")

// itemPatch carries only the conversation items that changed since the save
// that returned BaseRevision. Groups are keyed by the conversation field name
// (messages, agentHistory, agentEvents, messageQueue); every group is required.
type itemPatch struct {
	BaseRevision uint64                    `json:"baseRevision"`
	Groups       map[string]itemGroupPatch `json:"groups"`
}

type itemGroupPatch struct {
	BaseLength int          `json:"baseLength"`
	Length     int          `json:"length"`
	Changes    []itemChange `json:"changes"`
}

// itemChange.Item is the item's JSON text exactly as the frontend serialized
// it, so its SHA-256 matches the digest a full save would compute.
type itemChange struct {
	Position int    `json:"position"`
	Item     string `json:"item"`
}

type saveConversationResult struct {
	ItemRevision uint64 `json:"itemRevision,omitempty"`
}

type revisionKey struct{ scopeID, conversationID string }

// itemRevisions numbers every committed change to a conversation's item rows.
// It lives in memory only: all windows share this Go process, and an app
// restart also clears every frontend record, whose first save is then full.
// mu also serializes item writes so a revision check and the write it guards
// cannot interleave with another writer.
var itemRevisions = struct {
	mu      sync.Mutex
	last    uint64
	current map[revisionKey]uint64
}{current: map[revisionKey]uint64{}}

// bumpItemRevision must be called with itemRevisions.mu held, after commit.
func bumpItemRevision(scopeID, conversationID string) uint64 {
	itemRevisions.last++
	itemRevisions.current[revisionKey{scopeID, conversationID}] = itemRevisions.last
	return itemRevisions.last
}

// forgetItemRevision must be called with itemRevisions.mu held, after commit.
func forgetItemRevision(scopeID, conversationID string) {
	delete(itemRevisions.current, revisionKey{scopeID, conversationID})
}

func currentItemRevision(scopeID, conversationID string) uint64 {
	return itemRevisions.current[revisionKey{scopeID, conversationID}]
}

// validate checks the patch shape before any row is touched.
func (patch *itemPatch) validate() error {
	if len(patch.Groups) != len(conversationItemGroups) {
		return fmt.Errorf("AI 会话增量保存需要 %d 个条目分组", len(conversationItemGroups))
	}
	for _, group := range conversationItemGroups {
		groupPatch, ok := patch.Groups[group.field]
		if !ok {
			return fmt.Errorf("AI 会话增量保存缺少分组 %s", group.field)
		}
		if groupPatch.BaseLength < 0 || groupPatch.Length < 0 {
			return fmt.Errorf("AI 会话增量保存分组 %s 长度无效", group.field)
		}
		seen := make(map[int]struct{}, len(groupPatch.Changes))
		for _, change := range groupPatch.Changes {
			if change.Position < 0 || change.Position >= groupPatch.Length {
				return fmt.Errorf("AI 会话增量保存分组 %s 位置 %d 越界", group.field, change.Position)
			}
			if _, duplicate := seen[change.Position]; duplicate {
				return fmt.Errorf("AI 会话增量保存分组 %s 位置 %d 重复", group.field, change.Position)
			}
			if !json.Valid([]byte(change.Item)) {
				return fmt.Errorf("AI 会话增量保存分组 %s 位置 %d 不是有效 JSON", group.field, change.Position)
			}
			seen[change.Position] = struct{}{}
		}
		// Positions the stored rows do not cover must all be sent.
		for position := groupPatch.BaseLength; position < groupPatch.Length; position++ {
			if _, ok := seen[position]; !ok {
				return fmt.Errorf("AI 会话增量保存分组 %s 缺少新增位置 %d", group.field, position)
			}
		}
	}
	return nil
}

// storedMetadataRefs returns the attachment refs of the stored conversation row.
func storedMetadataRefs(tx *sql.Tx, scopeID, conversationID string) (map[string]struct{}, error) {
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM ai_conversations WHERE scope_id=? AND conversation_id=?`, scopeID, conversationID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errItemPatchConflict
	}
	if err != nil {
		return nil, err
	}
	refs := map[string]struct{}{}
	return refs, collectPayloadRefs(payload, refs)
}

// collectPayloadRefs adds the attachment refs of a stored (externalized)
// payload; payloads without a ref are not decoded.
func collectPayloadRefs(payload []byte, refs map[string]struct{}) error {
	if !bytes.Contains(payload, []byte(blobReferenceKey)) {
		return nil
	}
	value, err := decodeAny(payload)
	if err != nil {
		return err
	}
	collectAttachmentRefs(value, refs)
	return nil
}

// applyItemPatchTx writes the patch's changed items and removes trimmed rows,
// reading only those rows. It returns the refs of the rows it replaced or
// removed: attachments that may no longer be referenced.
func applyItemPatchTx(tx *sql.Tx, scopeID, conversationID string, patch *itemPatch, blobs map[string]attachmentBlob, wantedRefs map[string]struct{}) (map[string]struct{}, error) {
	counts := map[string]int{}
	rows, err := tx.Query(`SELECT kind, COUNT(*) FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? GROUP BY kind`, scopeID, conversationID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return nil, err
		}
		counts[kind] = count
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	dropped := map[string]struct{}{}
	for _, group := range conversationItemGroups {
		groupPatch := patch.Groups[group.field]
		if counts[group.kind] != groupPatch.BaseLength {
			return nil, errItemPatchConflict
		}
		for _, change := range groupPatch.Changes {
			rawItem := []byte(change.Item)
			digest := sha256.Sum256(rawItem)
			if change.Position < groupPatch.BaseLength {
				var previous, previousHash []byte
				err := tx.QueryRow(`SELECT payload, payload_hash FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? AND kind=? AND position=?`, scopeID, conversationID, group.kind, change.Position).Scan(&previous, &previousHash)
				if errors.Is(err, sql.ErrNoRows) {
					return nil, errItemPatchConflict
				}
				if err != nil {
					return nil, err
				}
				if bytes.Equal(previousHash, digest[:]) {
					continue
				}
				if err := collectPayloadRefs(previous, dropped); err != nil {
					return nil, err
				}
			}
			if err := writeItemTx(tx, scopeID, conversationID, group.kind, change.Position, rawItem, digest, blobs, wantedRefs); err != nil {
				return nil, err
			}
		}
		if groupPatch.Length < groupPatch.BaseLength {
			if err := collectTrimmedRefs(tx, scopeID, conversationID, group.kind, groupPatch.Length, dropped); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`DELETE FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? AND kind=? AND position>=?`, scopeID, conversationID, group.kind, groupPatch.Length); err != nil {
				return nil, err
			}
		}
	}
	return dropped, nil
}

func collectTrimmedRefs(tx *sql.Tx, scopeID, conversationID, kind string, from int, refs map[string]struct{}) error {
	rows, err := tx.Query(`SELECT payload FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? AND kind=? AND position>=? AND instr(payload, ?) > 0`, scopeID, conversationID, kind, from, blobReferenceKey)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		if err := collectPayloadRefs(payload, refs); err != nil {
			return err
		}
	}
	return rows.Err()
}

// patchedAttachmentRefs is the conversation's ref set after a patch: the
// stored refs plus the new ones, minus dropped refs that no row (after the
// patch was written) still mentions. The new metadata row's refs are part of
// added, so only item rows need checking.
func patchedAttachmentRefs(tx *sql.Tx, scopeID, conversationID string, dropped, added map[string]struct{}) (map[string]struct{}, error) {
	wanted := map[string]struct{}{}
	rows, err := tx.Query(`SELECT attachment_hash FROM ai_attachment_refs WHERE scope_id=? AND conversation_id=?`, scopeID, conversationID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var hashValue string
		if err := rows.Scan(&hashValue); err != nil {
			rows.Close()
			return nil, err
		}
		wanted[hashValue] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for hashValue := range added {
		wanted[hashValue] = struct{}{}
	}
	for hashValue := range dropped {
		if _, ok := added[hashValue]; ok {
			continue
		}
		var found int
		err := tx.QueryRow(`SELECT 1 FROM ai_conversation_items WHERE scope_id=? AND conversation_id=? AND instr(payload, ?) > 0 LIMIT 1`, scopeID, conversationID, hashValue).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			delete(wanted, hashValue)
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return wanted, nil
}
