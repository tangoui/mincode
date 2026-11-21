package sessions

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/storage"
)

type claudeTitleRepository struct {
	openDB func() (*sql.DB, error)
}

var claudeTitles = claudeTitleRepository{
	openDB: storage.DB,
}

func (r claudeTitleRepository) db() (*sql.DB, error) {
	db, err := r.openDB()
	if err != nil {
		return nil, fmt.Errorf("打开 Claude 会话标题存储: %w", err)
	}
	return db, nil
}

func (r claudeTitleRepository) list(encodedDir string) (map[string]string, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT session_uuid, title FROM claude_session_titles WHERE encoded_dir = ?", encodedDir)
	if err != nil {
		return nil, fmt.Errorf("查询 Claude 会话标题: %w", err)
	}
	defer rows.Close()
	titles := make(map[string]string)
	for rows.Next() {
		var sessionUUID, title string
		if err := rows.Scan(&sessionUUID, &title); err != nil {
			return nil, fmt.Errorf("读取 Claude 会话标题: %w", err)
		}
		titles[sessionUUID] = title
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Claude 会话标题: %w", err)
	}
	return titles, nil
}

func (r claudeTitleRepository) upsert(encodedDir, sessionUUID, title string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO claude_session_titles(encoded_dir, session_uuid, title)
		VALUES(?, ?, ?)
		ON CONFLICT(encoded_dir, session_uuid) DO UPDATE SET
			title = excluded.title,
			updated_at = CURRENT_TIMESTAMP`, encodedDir, sessionUUID, title); err != nil {
		return fmt.Errorf("保存 Claude 会话标题: %w", err)
	}
	return nil
}

func (r claudeTitleRepository) delete(encodedDir, sessionUUID string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	if _, err := db.Exec("DELETE FROM claude_session_titles WHERE encoded_dir = ? AND session_uuid = ?", encodedDir, sessionUUID); err != nil {
		return fmt.Errorf("删除 Claude 会话标题: %w", err)
	}
	return nil
}

func validateClaudeSessionTitle(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", errors.New("标题不是有效 UTF-8")
	}
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", errors.New("会话名称不能为空")
	}
	if len([]rune(title)) > 140 {
		return "", errors.New("会话名称不能超过 140 个字符")
	}
	return title, nil
}
