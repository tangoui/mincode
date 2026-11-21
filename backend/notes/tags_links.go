package notes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// maxTagRunes 是单个标签名（去首尾空白后）的字符上限。
	maxTagRunes = 64
	// maxTagsPerNote 是单篇笔记的标签数上限（去重后）。
	maxTagsPerNote = 100
	// maxLinkIDBytes 限制关联对象的 objectId / parentId / scopeId；项目 id 是绝对路径，需留足长度。
	maxLinkIDBytes = 512
	// maxSnapshotRunes 限制关联的名称快照与工作区名称快照。
	maxSnapshotRunes = 200
	// maxCountObjectIDs 是 notes_links_counts 单次可查询的对象数上限。
	maxCountObjectIDs = 10000
)

// linkObjectTypes 是可关联的对象类型：SSH 连接、项目、数据库连接、AI 对话、HTTP 已保存请求。
var linkObjectTypes = map[string]bool{"ssh": true, "project": true, "database": true, "ai": true, "http": true}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// jsonArray 把 id 列表编码为 JSON 数组，供 SQL 中 json_each(?) 展开，避免拼接可变数量的占位符。
func jsonArray(ids []string) (string, error) {
	raw, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("notes: encode ids: %w", err)
	}
	return string(raw), nil
}

// tagsByNote 返回笔记 id → 标签名（按名称不区分大小写排序）。ids 为 nil 时查询全部笔记。
func tagsByNote(ctx context.Context, q queryer, ids []string) (map[string][]string, error) {
	query := `SELECT l.note_id, t.name FROM note_tag_links l JOIN note_tags t ON t.id = l.tag_id`
	var args []any
	if ids != nil {
		if len(ids) == 0 {
			return map[string][]string{}, nil
		}
		list, err := jsonArray(ids)
		if err != nil {
			return nil, err
		}
		query += ` WHERE l.note_id IN (SELECT value FROM json_each(?))`
		args = append(args, list)
	}
	query += ` ORDER BY t.name COLLATE NOCASE, t.name`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("notes: load tags: %w", err)
	}
	defer rows.Close()
	result := map[string][]string{}
	for rows.Next() {
		var noteID, name string
		if err := rows.Scan(&noteID, &name); err != nil {
			return nil, fmt.Errorf("notes: load tags: %w", err)
		}
		result[noteID] = append(result[noteID], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: load tags: %w", err)
	}
	return result, nil
}

// fillTags 为摘要填充标签；没有标签的笔记保持 []，不为 null。
func fillTags(ctx context.Context, q queryer, summaries []NoteSummary) error {
	ids := make([]string, len(summaries))
	for i := range summaries {
		ids[i] = summaries[i].ID
	}
	tags, err := tagsByNote(ctx, q, ids)
	if err != nil {
		return err
	}
	for i := range summaries {
		if names := tags[summaries[i].ID]; names != nil {
			summaries[i].Tags = names
		} else {
			summaries[i].Tags = []string{}
		}
	}
	return nil
}

// scanSummaries 读取 id, folder_id, title, favorite, position, updated_at 六列，并填充标签。
func scanSummaries(ctx context.Context, q queryer, query string, args ...any) ([]NoteSummary, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("notes: list notes: %w", err)
	}
	summaries := []NoteSummary{}
	for rows.Next() {
		var summary NoteSummary
		var folder sql.NullString
		if err := rows.Scan(&summary.ID, &folder, &summary.Title, &summary.Favorite, &summary.Position, &summary.UpdatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("notes: list notes: %w", err)
		}
		summary.FolderID = folder.String
		summaries = append(summaries, summary)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("notes: list notes: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: list notes: %w", err)
	}
	if err := fillTags(ctx, q, summaries); err != nil {
		return nil, err
	}
	return summaries, nil
}

func loadSummary(ctx context.Context, q queryer, id string) (NoteSummary, error) {
	summaries, err := scanSummaries(ctx, q, `SELECT id, folder_id, title, favorite, position, updated_at FROM notes WHERE id = ?`, id)
	if err != nil {
		return NoteSummary{}, err
	}
	if len(summaries) == 0 {
		return NoteSummary{}, errNoteNotFound
	}
	return summaries[0], nil
}

// touchNote 推进笔记的 updated_at（严格递增），使标签与关联变化参与按 updatedAt 的同步合并。
func touchNote(ctx context.Context, tx *sql.Tx, id string) error {
	var updatedAt int64
	err := tx.QueryRowContext(ctx, `SELECT updated_at FROM notes WHERE id = ?`, id).Scan(&updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errNoteNotFound
	}
	if err != nil {
		return fmt.Errorf("notes: touch note: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET updated_at = ? WHERE id = ?`, nextTimestamp(updatedAt), id); err != nil {
		return fmt.Errorf("notes: touch note: %w", err)
	}
	return nil
}

func requireNote(ctx context.Context, q queryer, id string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM notes WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return errNoteNotFound
	}
	if err != nil {
		return fmt.Errorf("notes: check note: %w", err)
	}
	return nil
}

// deleteOrphanTags 删除不再被任何笔记引用的标签。
func deleteOrphanTags(ctx context.Context, exec execer) error {
	if _, err := exec.ExecContext(ctx, `DELETE FROM note_tags
		WHERE NOT EXISTS (SELECT 1 FROM note_tag_links l WHERE l.tag_id = note_tags.id)`); err != nil {
		return fmt.Errorf("notes: delete orphan tags: %w", err)
	}
	return nil
}

func listTags(ctx context.Context, q queryer) ([]TagCount, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.name, COUNT(*) FROM note_tags t
		JOIN note_tag_links l ON l.tag_id = t.id
		GROUP BY t.id ORDER BY t.name COLLATE NOCASE, t.name`)
	if err != nil {
		return nil, fmt.Errorf("notes: list tags: %w", err)
	}
	defer rows.Close()
	tags := []TagCount{}
	for rows.Next() {
		var tag TagCount
		if err := rows.Scan(&tag.Name, &tag.Count); err != nil {
			return nil, fmt.Errorf("notes: list tags: %w", err)
		}
		tags = append(tags, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: list tags: %w", err)
	}
	return tags, nil
}

// tagKey 是标签不区分大小写比较用的键。
func tagKey(name string) string { return strings.ToLower(name) }

// setTags 把笔记的标签替换为 names（已去空白、去重、校验）。已有同名标签（不区分大小写）沿用其原名；
// 标签集合未变化时不写入、不推进 updated_at；变化时清理无引用的标签。
func setTags(ctx context.Context, db *sql.DB, id string, names []string) (NoteSummary, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return NoteSummary{}, fmt.Errorf("notes: set tags: %w", err)
	}
	defer tx.Rollback()
	updated, err := setTagsTx(ctx, tx, id, names)
	if err != nil {
		return NoteSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return NoteSummary{}, fmt.Errorf("notes: set tags: %w", err)
	}
	return updated, nil
}

// setTagsTx 是 setTags 的事务内实现；names 须已规整（见 normalizeTags）。
func setTagsTx(ctx context.Context, tx *sql.Tx, id string, names []string) (NoteSummary, error) {
	current, err := loadSummary(ctx, tx, id)
	if err != nil {
		return NoteSummary{}, err
	}
	currentKeys := map[string]bool{}
	for _, name := range current.Tags {
		currentKeys[tagKey(name)] = true
	}
	same := len(currentKeys) == len(names)
	for _, name := range names {
		same = same && currentKeys[tagKey(name)]
	}
	if same {
		return current, nil
	}

	existing := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM note_tags ORDER BY name, id`)
	if err != nil {
		return NoteSummary{}, fmt.Errorf("notes: set tags: %w", err)
	}
	for rows.Next() {
		var tagID, name string
		if err := rows.Scan(&tagID, &name); err != nil {
			rows.Close()
			return NoteSummary{}, fmt.Errorf("notes: set tags: %w", err)
		}
		if _, ok := existing[tagKey(name)]; !ok {
			existing[tagKey(name)] = tagID
		}
	}
	if err := rows.Close(); err != nil {
		return NoteSummary{}, fmt.Errorf("notes: set tags: %w", err)
	}
	if err := rows.Err(); err != nil {
		return NoteSummary{}, fmt.Errorf("notes: set tags: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM note_tag_links WHERE note_id = ?`, id); err != nil {
		return NoteSummary{}, fmt.Errorf("notes: set tags: %w", err)
	}
	for _, name := range names {
		tagID, ok := existing[tagKey(name)]
		if !ok {
			tagID = newID()
			if _, err := tx.ExecContext(ctx, `INSERT INTO note_tags (id, name) VALUES (?, ?)`, tagID, name); err != nil {
				return NoteSummary{}, fmt.Errorf("notes: create tag: %w", err)
			}
			existing[tagKey(name)] = tagID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO note_tag_links (note_id, tag_id) VALUES (?, ?)`, id, tagID); err != nil {
			return NoteSummary{}, fmt.Errorf("notes: link tag: %w", err)
		}
	}
	if err := deleteOrphanTags(ctx, tx); err != nil {
		return NoteSummary{}, err
	}
	if err := touchNote(ctx, tx, id); err != nil {
		return NoteSummary{}, err
	}
	return loadSummary(ctx, tx, id)
}

const linkColumns = `object_type, object_id, parent_id, scope_id, name_snapshot, scope_name_snapshot, created_at`

func scanLink(scanner interface{ Scan(...any) error }, extra ...any) (Link, error) {
	var link Link
	dest := append([]any{&link.ObjectType, &link.ObjectID, &link.ParentID, &link.ScopeID,
		&link.NameSnapshot, &link.ScopeNameSnapshot, &link.CreatedAt}, extra...)
	err := scanner.Scan(dest...)
	return link, err
}

// linksForNote 返回笔记的全部关联，按添加时间排序；没有关联时返回 []。
func linksForNote(ctx context.Context, q queryer, id string) ([]Link, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+linkColumns+` FROM note_links WHERE note_id = ?
		ORDER BY created_at, object_type, object_id, scope_id`, id)
	if err != nil {
		return nil, fmt.Errorf("notes: load links: %w", err)
	}
	defer rows.Close()
	links := []Link{}
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("notes: load links: %w", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: load links: %w", err)
	}
	return links, nil
}

// addLink 添加关联；同一主键（笔记、类型、对象、工作区）已存在时只更新 parentId 与名称快照，保留 createdAt
// （对象可能换了父级，例如 HTTP 请求移到另一个集合）。新增或上述字段变化时推进笔记 updated_at。
func addLink(ctx context.Context, db *sql.DB, noteID string, link Link) (Link, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Link{}, fmt.Errorf("notes: add link: %w", err)
	}
	defer tx.Rollback()
	if err := requireNote(ctx, tx, noteID); err != nil {
		return Link{}, err
	}
	stored, err := scanLink(tx.QueryRowContext(ctx, `SELECT `+linkColumns+` FROM note_links
		WHERE note_id = ? AND object_type = ? AND object_id = ? AND scope_id = ?`,
		noteID, link.ObjectType, link.ObjectID, link.ScopeID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		link.CreatedAt = time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `INSERT INTO note_links (note_id, `+linkColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			noteID, link.ObjectType, link.ObjectID, link.ParentID, link.ScopeID,
			link.NameSnapshot, link.ScopeNameSnapshot, link.CreatedAt); err != nil {
			return Link{}, fmt.Errorf("notes: add link: %w", err)
		}
		stored = link
	case err != nil:
		return Link{}, fmt.Errorf("notes: add link: %w", err)
	case stored.ParentID == link.ParentID && stored.NameSnapshot == link.NameSnapshot &&
		stored.ScopeNameSnapshot == link.ScopeNameSnapshot:
		return stored, nil
	default:
		stored.ParentID, stored.NameSnapshot, stored.ScopeNameSnapshot = link.ParentID, link.NameSnapshot, link.ScopeNameSnapshot
		if _, err := tx.ExecContext(ctx, `UPDATE note_links SET parent_id = ?, name_snapshot = ?, scope_name_snapshot = ?
			WHERE note_id = ? AND object_type = ? AND object_id = ? AND scope_id = ?`,
			stored.ParentID, stored.NameSnapshot, stored.ScopeNameSnapshot,
			noteID, stored.ObjectType, stored.ObjectID, stored.ScopeID); err != nil {
			return Link{}, fmt.Errorf("notes: update link: %w", err)
		}
	}
	if err := touchNote(ctx, tx, noteID); err != nil {
		return Link{}, err
	}
	if err := tx.Commit(); err != nil {
		return Link{}, fmt.Errorf("notes: add link: %w", err)
	}
	return stored, nil
}

// removeLink 删除一条关联；关联不存在时什么都不做（幂等），笔记不存在时返回 note_not_found。
func removeLink(ctx context.Context, db *sql.DB, noteID, objectType, objectID, scopeID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notes: remove link: %w", err)
	}
	defer tx.Rollback()
	if err := requireNote(ctx, tx, noteID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM note_links
		WHERE note_id = ? AND object_type = ? AND object_id = ? AND scope_id = ?`, noteID, objectType, objectID, scopeID)
	if err != nil {
		return fmt.Errorf("notes: remove link: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("notes: remove link: %w", err)
	} else if affected == 0 {
		return nil
	}
	if err := touchNote(ctx, tx, noteID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("notes: remove link: %w", err)
	}
	return nil
}

// notesForObject 返回关联到该对象的笔记（按 updated_at 降序）。scopeID 为 nil 时匹配任意工作区。
func notesForObject(ctx context.Context, q queryer, objectType, objectID string, scopeID *string) ([]NoteSummary, error) {
	var scope any
	if scopeID != nil {
		scope = *scopeID
	}
	return scanSummaries(ctx, q, `SELECT id, folder_id, title, favorite, position, updated_at FROM notes n
		WHERE EXISTS (SELECT 1 FROM note_links l WHERE l.note_id = n.id AND l.object_type = ? AND l.object_id = ?
			AND (? IS NULL OR l.scope_id = ?))
		ORDER BY updated_at DESC, id`, objectType, objectID, scope, scope)
}

// linkCounts 返回每个对象关联的笔记数（跨工作区去重计数）；没有关联的对象不出现在结果中。
func linkCounts(ctx context.Context, q queryer, objectType string, objectIDs []string) (map[string]int, error) {
	counts := map[string]int{}
	if len(objectIDs) == 0 {
		return counts, nil
	}
	list, err := jsonArray(objectIDs)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT object_id, COUNT(DISTINCT note_id) FROM note_links
		WHERE object_type = ? AND object_id IN (SELECT value FROM json_each(?))
		GROUP BY object_id`, objectType, list)
	if err != nil {
		return nil, fmt.Errorf("notes: count links: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var objectID string
		var count int
		if err := rows.Scan(&objectID, &count); err != nil {
			return nil, fmt.Errorf("notes: count links: %w", err)
		}
		counts[objectID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: count links: %w", err)
	}
	return counts, nil
}

// allLinks 返回全部关联及所属笔记，按类型、对象名称快照、对象、工作区、笔记更新时间排序。
func allLinks(ctx context.Context, q queryer) ([]LinkEntry, error) {
	rows, err := q.QueryContext(ctx, `SELECT l.object_type, l.object_id, l.parent_id, l.scope_id, l.name_snapshot,
			l.scope_name_snapshot, l.created_at, l.note_id, n.title
		FROM note_links l JOIN notes n ON n.id = l.note_id
		ORDER BY l.object_type, l.name_snapshot COLLATE NOCASE, l.object_id, l.scope_id, n.updated_at DESC, n.id`)
	if err != nil {
		return nil, fmt.Errorf("notes: list links: %w", err)
	}
	defer rows.Close()
	entries := []LinkEntry{}
	for rows.Next() {
		var entry LinkEntry
		link, err := scanLink(rows, &entry.NoteID, &entry.NoteTitle)
		if err != nil {
			return nil, fmt.Errorf("notes: list links: %w", err)
		}
		entry.Link = link
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: list links: %w", err)
	}
	return entries, nil
}
