package notes

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 200
	// maxQueryRunes 限制搜索词长度（去首尾空白后）。
	maxQueryRunes = 500
	// snippetContextRunes 是片段在命中处前后各保留的字符数；只命中标题时取正文开头 2 倍长度。
	snippetContextRunes = 40
)

// likeEscaper 转义 LIKE 通配符，配合 ESCAPE '\' 使用。
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// snippetWhitespace 把换行与制表符替换为空格（逐字符替换，不改变片段字符数）。
var snippetWhitespace = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")

// searchSQL：内层按「标题命中优先、updated_at 降序」取前 limit 条（排序时不携带正文）；外层只为这些行计算片段。
//
// 大小写不敏感与 SQLite 内置 LIKE 一致，只对 ASCII 字母生效。内置 lower() 同样只折叠 ASCII，
// 不改变字符位置，所以 instr(lower(body), lower(query)) 给出与 LIKE 一致的命中字符位置；
// instr / substr 对 TEXT 按字符（rune）计数。片段在 SQL 内截取，不必把整篇正文读到 Go 侧。
//
// 参数：?1 LIKE 模式，?2 原始搜索词，?3 limit，?4 前后字符数，?5 搜索词字符数。
const searchSQL = `SELECT n.id, n.folder_id, n.title, n.favorite, n.position, n.updated_at,
		CASE WHEN hits.hit > 0
			THEN substr(n.body, max(1, hits.hit - ?4), hits.hit - max(1, hits.hit - ?4) + ?5 + ?4)
			ELSE substr(n.body, 1, ?4 * 2)
		END
	FROM (
		SELECT id, title_hit, updated_at, (SELECT instr(lower(body), lower(?2)) FROM notes WHERE id = ranked.id) AS hit
		FROM (
			SELECT id, updated_at, title LIKE ?1 ESCAPE '\' AS title_hit
			FROM notes
			WHERE title LIKE ?1 ESCAPE '\' OR body LIKE ?1 ESCAPE '\'
			ORDER BY title_hit DESC, updated_at DESC, id
			LIMIT ?3
		) AS ranked
	) AS hits
	JOIN notes n ON n.id = hits.id
	ORDER BY hits.title_hit DESC, hits.updated_at DESC, hits.id`

// searchNotes 对标题与正文做不区分大小写的子串匹配；query 已去首尾空白，空串返回 []。
func searchNotes(ctx context.Context, q queryer, query string, limit int) ([]SearchResult, error) {
	results := []SearchResult{}
	if query == "" {
		return results, nil
	}
	pattern := "%" + likeEscaper.Replace(query) + "%"
	rows, err := q.QueryContext(ctx, searchSQL, pattern, query, limit, snippetContextRunes, utf8.RuneCountInString(query))
	if err != nil {
		return nil, fmt.Errorf("notes: search: %w", err)
	}
	summaries := []NoteSummary{}
	snippets := []string{}
	for rows.Next() {
		var summary NoteSummary
		var folder sql.NullString
		var snippet string
		if err := rows.Scan(&summary.ID, &folder, &summary.Title, &summary.Favorite, &summary.Position, &summary.UpdatedAt, &snippet); err != nil {
			rows.Close()
			return nil, fmt.Errorf("notes: search: %w", err)
		}
		summary.FolderID = folder.String
		summaries = append(summaries, summary)
		snippets = append(snippets, snippetWhitespace.Replace(snippet))
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("notes: search: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notes: search: %w", err)
	}
	if err := fillTags(ctx, q, summaries); err != nil {
		return nil, err
	}
	for i := range summaries {
		results = append(results, SearchResult{Note: summaries[i], Snippet: snippets[i]})
	}
	return results, nil
}

// searchLimit 把请求的 limit 规整到 [1, maxSearchLimit]；未给出或非正数时用默认值。
func searchLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultSearchLimit
	case limit > maxSearchLimit:
		return maxSearchLimit
	default:
		return limit
	}
}
