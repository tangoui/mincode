package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"mincode/app/backend/storage"
)

// 包级注入点：写操作用单写连接，纯查询用只读连接池；测试可替换。
var (
	openNotesDB     = storage.DB
	openNotesReadDB = storage.ReadDB
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// newID 返回随机 UUID v4（crypto/rand）。
func newID() string { return uuid.NewString() }

// nextTimestamp 返回当前 Unix 毫秒，且严格大于 prev，保证每次真实修改都推进 updated_at。
func nextTimestamp(prev int64) int64 {
	now := time.Now().UnixMilli()
	if now <= prev {
		return prev + 1
	}
	return now
}

// nullableID 把空串映射为 SQL NULL（根目录）。
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func folderExists(ctx context.Context, q queryer, id string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM note_folders WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return errFolderNotFound
	}
	if err != nil {
		return fmt.Errorf("notes: check folder: %w", err)
	}
	return nil
}

func loadTree(ctx context.Context, q queryer) (Tree, error) {
	tree := Tree{Folders: []Folder{}, Notes: []NoteSummary{}}
	rows, err := q.QueryContext(ctx, `SELECT id, parent_id, name, position, created_at, updated_at
		FROM note_folders ORDER BY position, created_at, id`)
	if err != nil {
		return Tree{}, fmt.Errorf("notes: list folders: %w", err)
	}
	for rows.Next() {
		var folder Folder
		var parent sql.NullString
		if err := rows.Scan(&folder.ID, &parent, &folder.Name, &folder.Position, &folder.CreatedAt, &folder.UpdatedAt); err != nil {
			rows.Close()
			return Tree{}, fmt.Errorf("notes: list folders: %w", err)
		}
		folder.ParentID = parent.String
		tree.Folders = append(tree.Folders, folder)
	}
	if err := rows.Close(); err != nil {
		return Tree{}, fmt.Errorf("notes: list folders: %w", err)
	}
	if err := rows.Err(); err != nil {
		return Tree{}, fmt.Errorf("notes: list folders: %w", err)
	}

	rows, err = q.QueryContext(ctx, `SELECT id, folder_id, title, favorite, position, updated_at
		FROM notes ORDER BY position, created_at, id`)
	if err != nil {
		return Tree{}, fmt.Errorf("notes: list notes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		summary := NoteSummary{Tags: []string{}}
		var folder sql.NullString
		if err := rows.Scan(&summary.ID, &folder, &summary.Title, &summary.Favorite, &summary.Position, &summary.UpdatedAt); err != nil {
			return Tree{}, fmt.Errorf("notes: list notes: %w", err)
		}
		summary.FolderID = folder.String
		tree.Notes = append(tree.Notes, summary)
	}
	if err := rows.Err(); err != nil {
		return Tree{}, fmt.Errorf("notes: list notes: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Tree{}, fmt.Errorf("notes: list notes: %w", err)
	}
	tags, err := tagsByNote(ctx, q, nil)
	if err != nil {
		return Tree{}, err
	}
	for i := range tree.Notes {
		if names := tags[tree.Notes[i].ID]; names != nil {
			tree.Notes[i].Tags = names
		}
	}
	return tree, nil
}

func getNote(ctx context.Context, q queryer, id string) (Note, error) {
	note := Note{NoteSummary: NoteSummary{Tags: []string{}}, Links: []Link{}}
	var folder sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, folder_id, title, body, favorite, position, created_at, updated_at
		FROM notes WHERE id = ?`, id).
		Scan(&note.ID, &folder, &note.Title, &note.Body, &note.Favorite, &note.Position, &note.CreatedAt, &note.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, errNoteNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("notes: get note: %w", err)
	}
	note.FolderID = folder.String
	tags, err := tagsByNote(ctx, q, []string{id})
	if err != nil {
		return Note{}, err
	}
	if names := tags[id]; names != nil {
		note.Tags = names
	}
	if note.Links, err = linksForNote(ctx, q, id); err != nil {
		return Note{}, err
	}
	return note, nil
}

func createNote(ctx context.Context, db *sql.DB, folderID, title, body string) (Note, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Note{}, fmt.Errorf("notes: create note: %w", err)
	}
	defer tx.Rollback()
	note, err := insertNote(ctx, tx, folderID, title, body, false)
	if err != nil {
		return Note{}, err
	}
	if err := tx.Commit(); err != nil {
		return Note{}, fmt.Errorf("notes: create note: %w", err)
	}
	return note, nil
}

// insertNote 在调用方的事务中新建笔记，排在 folderID（空串为根目录）下最后。
func insertNote(ctx context.Context, tx *sql.Tx, folderID, title, body string, favorite bool) (Note, error) {
	if folderID != "" {
		if err := folderExists(ctx, tx, folderID); err != nil {
			return Note{}, err
		}
	}
	var position float64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM notes WHERE folder_id IS ?`,
		nullableID(folderID)).Scan(&position); err != nil {
		return Note{}, fmt.Errorf("notes: create note position: %w", err)
	}
	now := nextTimestamp(0)
	note := Note{
		NoteSummary: NoteSummary{ID: newID(), FolderID: folderID, Title: title, Favorite: favorite, Position: position, UpdatedAt: now, Tags: []string{}},
		Body:        body,
		CreatedAt:   now,
		Links:       []Link{},
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notes (id, folder_id, title, body, favorite, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		note.ID, nullableID(folderID), title, body, favorite, position, now, now); err != nil {
		return Note{}, fmt.Errorf("notes: create note: %w", err)
	}
	return note, nil
}

// updateNote 只写入传入且确有变化的字段；无变化时不推进 updated_at。
func updateNote(ctx context.Context, db *sql.DB, id string, title, body *string, favorite *bool) (NoteSummary, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return NoteSummary{}, fmt.Errorf("notes: update note: %w", err)
	}
	defer tx.Rollback()
	current, err := getNote(ctx, tx, id)
	if err != nil {
		return NoteSummary{}, err
	}
	changed := false
	if title != nil && *title != current.Title {
		current.Title, changed = *title, true
	}
	if body != nil && *body != current.Body {
		current.Body, changed = *body, true
	}
	if favorite != nil && *favorite != current.Favorite {
		current.Favorite, changed = *favorite, true
	}
	if !changed {
		return current.NoteSummary, nil
	}
	current.UpdatedAt = nextTimestamp(current.UpdatedAt)
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET title = ?, body = ?, favorite = ?, updated_at = ? WHERE id = ?`,
		current.Title, current.Body, current.Favorite, current.UpdatedAt, id); err != nil {
		return NoteSummary{}, fmt.Errorf("notes: update note: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return NoteSummary{}, fmt.Errorf("notes: update note: %w", err)
	}
	return current.NoteSummary, nil
}

// deleteNote 删除笔记；标签关联与对象关联经外键级联删除，随后清理无引用的标签。
func deleteNote(ctx context.Context, db *sql.DB, id string) error {
	return deleteRow(ctx, db, `DELETE FROM notes WHERE id = ?`, id, errNoteNotFound, "delete note")
}

// deleteRow 在事务中删除一行（找不到时返回 missing），并清理因级联删除而无引用的标签。
func deleteRow(ctx context.Context, db *sql.DB, query, id string, missing error, op string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notes: %s: %w", op, err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("notes: %s: %w", op, err)
	}
	if err := requireAffected(result, missing); err != nil {
		return err
	}
	if err := deleteOrphanTags(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("notes: %s: %w", op, err)
	}
	return nil
}

// moveNote 把笔记放到 folderID（空串为根目录）下的 position；位置或文件夹变化时推进 updated_at，
// 使移动也能参与按 updatedAt 的同步合并。
func moveNote(ctx context.Context, db *sql.DB, id, folderID string, position float64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notes: move note: %w", err)
	}
	defer tx.Rollback()
	current, err := getNote(ctx, tx, id)
	if err != nil {
		return err
	}
	if folderID != "" {
		if err := folderExists(ctx, tx, folderID); err != nil {
			return err
		}
	}
	if current.FolderID == folderID && current.Position == position {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET folder_id = ?, position = ?, updated_at = ? WHERE id = ?`,
		nullableID(folderID), position, nextTimestamp(current.UpdatedAt), id); err != nil {
		return fmt.Errorf("notes: move note: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("notes: move note: %w", err)
	}
	return nil
}

func createFolder(ctx context.Context, db *sql.DB, parentID, name string) (Folder, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Folder{}, fmt.Errorf("notes: create folder: %w", err)
	}
	defer tx.Rollback()
	if parentID != "" {
		if err := folderExists(ctx, tx, parentID); err != nil {
			return Folder{}, err
		}
	}
	var position float64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM note_folders WHERE parent_id IS ?`,
		nullableID(parentID)).Scan(&position); err != nil {
		return Folder{}, fmt.Errorf("notes: create folder position: %w", err)
	}
	now := nextTimestamp(0)
	folder := Folder{ID: newID(), ParentID: parentID, Name: name, Position: position, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO note_folders (id, parent_id, name, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		folder.ID, nullableID(parentID), name, position, now, now); err != nil {
		return Folder{}, fmt.Errorf("notes: create folder: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Folder{}, fmt.Errorf("notes: create folder: %w", err)
	}
	return folder, nil
}

func loadFolder(ctx context.Context, q queryer, id string) (Folder, error) {
	var folder Folder
	var parent sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, parent_id, name, position, created_at, updated_at FROM note_folders WHERE id = ?`, id).
		Scan(&folder.ID, &parent, &folder.Name, &folder.Position, &folder.CreatedAt, &folder.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, errFolderNotFound
	}
	if err != nil {
		return Folder{}, fmt.Errorf("notes: get folder: %w", err)
	}
	folder.ParentID = parent.String
	return folder, nil
}

func renameFolder(ctx context.Context, db *sql.DB, id, name string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notes: rename folder: %w", err)
	}
	defer tx.Rollback()
	current, err := loadFolder(ctx, tx, id)
	if err != nil {
		return err
	}
	if current.Name == name {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE note_folders SET name = ?, updated_at = ? WHERE id = ?`,
		name, nextTimestamp(current.UpdatedAt), id); err != nil {
		return fmt.Errorf("notes: rename folder: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("notes: rename folder: %w", err)
	}
	return nil
}

// moveFolder 把文件夹放到 parentID（空串为根目录）下；目标为自身或其子孙时返回 note_folder_cycle。
func moveFolder(ctx context.Context, db *sql.DB, id, parentID string, position float64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notes: move folder: %w", err)
	}
	defer tx.Rollback()
	current, err := loadFolder(ctx, tx, id)
	if err != nil {
		return err
	}
	if parentID == id {
		return errFolderCycle
	}
	if parentID != "" {
		if err := folderExists(ctx, tx, parentID); err != nil {
			return err
		}
		// 沿目标父文件夹向上找祖先，若遇到被移动的文件夹则会成环。UNION 去重，脏数据成环时也能终止。
		var hit int
		err := tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id) AS (
				SELECT ?
				UNION
				SELECT f.parent_id FROM note_folders f JOIN ancestors a ON f.id = a.id WHERE f.parent_id IS NOT NULL
			)
			SELECT 1 FROM ancestors WHERE id = ? LIMIT 1`, parentID, id).Scan(&hit)
		if err == nil {
			return errFolderCycle
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("notes: move folder cycle check: %w", err)
		}
	}
	if current.ParentID == parentID && current.Position == position {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE note_folders SET parent_id = ?, position = ?, updated_at = ? WHERE id = ?`,
		nullableID(parentID), position, nextTimestamp(current.UpdatedAt), id); err != nil {
		return fmt.Errorf("notes: move folder: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("notes: move folder: %w", err)
	}
	return nil
}

// deleteFolder 只删除文件夹行；子文件夹与其中笔记经 ON DELETE CASCADE 一并删除，随后清理无引用的标签。
func deleteFolder(ctx context.Context, db *sql.DB, id string) error {
	return deleteRow(ctx, db, `DELETE FROM note_folders WHERE id = ?`, id, errFolderNotFound, "delete folder")
}

func requireAffected(result sql.Result, missing error) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("notes: rows affected: %w", err)
	}
	if affected == 0 {
		return missing
	}
	return nil
}
