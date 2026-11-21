// Meilisearch 连接与只读浏览（HTTP REST，非 SQL）。
//
//   - 连接：GET /version 探活并校验 API Key
//   - 授权：Authorization: Bearer <API Key>（token；无 master key 的开发实例可留空）
//   - Schema：GET /indexes 把索引映射为表
//   - 查询：POST /indexes/{uid}/search，JSON 搜索参数（q/filter/sort/limit/offset）
//
// 写操作暂返回明确不支持提示。
package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mincode/app/backend/core"
)

// applyMeiliAuth 为请求设置 Meilisearch Bearer API Key。
func applyMeiliAuth(req *http.Request, token string) {
	if t := strings.TrimSpace(token); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
}

func buildMeilisearchSession(cfg *DbConnectionConfig) (*dbSession, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 7700
	}
	scheme := "http"
	if cfg.SSL {
		scheme = "https"
	}
	baseURL := fmt.Sprintf("%s://%s:%d", scheme, host, port)
	token := derefOr(cfg.Token, "")

	client := &http.Client{Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 探活并校验 API Key（/version 需要认证，/health 无需认证无法校验密钥）
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/version", nil)
	if err != nil {
		return nil, err
	}
	applyMeiliAuth(req, token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Meilisearch 连接失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("Meilisearch 认证失败(%d): API Key 无效或缺失", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Meilisearch 探活失败: %d", resp.StatusCode)
	}

	return &dbSession{
		kind:        kindHTTP,
		httpKind:    httpMeili,
		httpClient:  client,
		httpBaseURL: baseURL,
		httpToken:   token,
	}, nil
}

// meilisearchSchema 列出所有索引（GET /indexes），每个索引作为一张表。
func meilisearchSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.httpBaseURL+"/indexes?limit=10000", nil)
	if err != nil {
		return nil, err
	}
	applyMeiliAuth(req, s.httpToken)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Meilisearch 索引列表读取失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("Meilisearch 索引响应读取失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Meilisearch 索引列表读取失败(%d): %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Results []struct {
			UID       string  `json:"uid"`
			CreatedAt *string `json:"createdAt"`
			UpdatedAt *string `json:"updatedAt"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("Meilisearch 索引响应解析失败: %w", err)
	}

	var tables []SchemaTable
	for _, idx := range parsed.Results {
		uid := strings.TrimSpace(idx.UID)
		if uid == "" {
			continue
		}
		tables = append(tables, SchemaTable{
			Name:      uid,
			Kind:      "index",
			CreatedAt: idx.CreatedAt,
			UpdatedAt: idx.UpdatedAt,
		})
	}
	return &DbSchemaInfo{
		Databases: []SchemaDatabase{{Name: "meilisearch", Tables: tables}},
	}, nil
}

// meilisearchSearch 向 /indexes/{uid}/search 提交搜索参数并返回结果。
func meilisearchSearch(ctx context.Context, s *dbSession, index string, body []byte, startMs int64) (*QueryResult, error) {
	index = strings.TrimSpace(index)
	if index == "" {
		return nil, fmt.Errorf("Meilisearch 查询需要指定索引")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.httpBaseURL+"/indexes/"+url.PathEscape(index)+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	applyMeiliAuth(req, s.httpToken)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Meilisearch 查询失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("Meilisearch 查询响应读取失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Meilisearch 查询失败(%d): %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return parseMeiliResponse(respBody, startMs)
}

// parseMeiliResponse 解析 search 响应：hits 为原始文档，
// 列取所有文档键的并集（按出现顺序去重），嵌套结构保留 JSON 字符串。
func parseMeiliResponse(body []byte, executionMs int64) (*QueryResult, error) {
	var parsed struct {
		Hits               []json.RawMessage `json:"hits"`
		EstimatedTotalHits *int64            `json:"estimatedTotalHits"`
		TotalHits          *int64            `json:"totalHits"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("Meilisearch 响应解析失败: %w", err)
	}
	hits := parsed.Hits
	if len(hits) == 0 {
		return &QueryResult{Columns: []string{}, Rows: [][]any{}, RowCount: 0, ExecutionMs: executionMs}, nil
	}

	var columns []string
	seen := map[string]struct{}{}
	docs := make([]map[string]json.RawMessage, len(hits))
	for i, h := range hits {
		doc := map[string]json.RawMessage{}
		// 用 Decoder 顺序解析 key，保证列顺序稳定
		dec := json.NewDecoder(bytes.NewReader(h))
		if tok, err := dec.Token(); err == nil {
			if d, ok := tok.(json.Delim); ok && d == '{' {
				for dec.More() {
					kTok, err := dec.Token()
					if err != nil {
						break
					}
					key, _ := kTok.(string)
					var raw json.RawMessage
					if err := dec.Decode(&raw); err != nil {
						break
					}
					doc[key] = raw
					if _, ok := seen[key]; !ok {
						seen[key] = struct{}{}
						columns = append(columns, key)
					}
				}
			}
		}
		docs[i] = doc
	}

	rows := make([][]any, 0, len(hits))
	for i := range hits {
		row := make([]any, 0, len(columns))
		for _, col := range columns {
			raw, ok := docs[i][col]
			if !ok {
				row = append(row, nil)
				continue
			}
			var v any
			if err := json.Unmarshal(raw, &v); err == nil {
				switch v.(type) {
				case map[string]any, []any:
					row = append(row, string(raw)) // 嵌套结构保留 JSON 字符串
				default:
					row = append(row, v)
				}
			} else {
				row = append(row, string(raw))
			}
		}
		rows = append(rows, row)
	}

	result := &QueryResult{
		Columns:     columns,
		Rows:        rows,
		RowCount:    len(rows),
		ExecutionMs: executionMs,
	}
	// finite pagination 设置下响应为 totalHits，否则为 estimatedTotalHits
	total := parsed.EstimatedTotalHits
	if total == nil {
		total = parsed.TotalHits
	}
	if total != nil {
		notice := fmt.Sprintf("totalHits: %d", *total)
		result.Notice = &notice
	}
	return result, nil
}

// meilisearchTableRows 分页浏览索引文档（空 q 占位搜索 + limit/offset）。
func meilisearchTableRows(ctx context.Context, s *dbSession, index string, limit, offset uint32) (*QueryResult, error) {
	start := time.Now()
	body := fmt.Sprintf(`{"q":"","limit":%d,"offset":%d}`, limit, offset)
	res, err := meilisearchSearch(ctx, s, index, []byte(body), 0)
	if err != nil {
		return nil, err
	}
	res.ExecutionMs = time.Since(start).Milliseconds()
	return res, nil
}

// meilisearchExecuteQuery 执行用户输入的搜索请求。
// 查询必须是 JSON：index 字段指定索引（必填），其余键作为搜索参数透传
// （q / filter / sort / limit / offset / attributesToRetrieve 等）。
func meilisearchExecuteQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	start := time.Now()
	var qv map[string]json.RawMessage
	if err := json.Unmarshal([]byte(query), &qv); err != nil {
		return nil, fmt.Errorf("Meilisearch 查询需要 JSON 格式: %v", err)
	}
	index := ""
	if raw, ok := qv["index"]; ok {
		var idx string
		if json.Unmarshal(raw, &idx) == nil {
			index = strings.TrimSpace(idx)
		}
		delete(qv, "index")
	}
	if index == "" {
		return nil, fmt.Errorf("Meilisearch 查询需要 index 字段指定索引")
	}
	body, err := json.Marshal(qv)
	if err != nil {
		return nil, fmt.Errorf("Meilisearch 查询序列化失败: %w", err)
	}
	res, err := meilisearchSearch(ctx, s, index, body, 0)
	if err != nil {
		return nil, err
	}
	res.ExecutionMs = time.Since(start).Milliseconds()
	return res, nil
}
