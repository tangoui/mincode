// CouchDB 连接与只读浏览（HTTP，标准库实现，非 SQL）。
//
//   - 连接：GET / 探活（Basic Auth）
//   - Schema：GET /_all_dbs，每个库作为一张“表”（跳过 _ 前缀系统库）
//   - 浏览：GET /{db}/_all_docs?include_docs=true&limit&skip，文档键并集为列
//   - 查询：JSON 格式 —— {"db":"名称", ...Mango selector} 提交到 /{db}/_find
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

const dbHTTPMaxResponseBytes = 32 * 1024 * 1024

func buildCouchDBSession(cfg *DbConnectionConfig) (*dbSession, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 5984
	}
	scheme := "http"
	if cfg.SSL {
		scheme = "https"
	}
	baseURL := fmt.Sprintf("%s://%s:%d", scheme, host, port)
	user := derefOr(cfg.Username, "")
	pass := derefOr(cfg.Password, "")

	client := &http.Client{Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return nil, err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CouchDB 连接失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("CouchDB 探活失败: %d", resp.StatusCode)
	}

	return &dbSession{
		kind:            kindHTTP,
		httpKind:        httpCouch,
		httpClient:      client,
		httpBaseURL:     baseURL,
		httpUser:        user,
		httpPass:        pass,
		defaultDatabase: derefOr(cfg.Database, ""),
	}, nil
}

// couchGet 执行带认证的 GET 请求并返回响应体。
func couchGet(ctx context.Context, s *dbSession, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.httpBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if s.httpUser != "" {
		req.SetBasicAuth(s.httpUser, s.httpPass)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CouchDB 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("CouchDB 响应读取失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("CouchDB 请求失败(%d): %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// couchSchema 列出所有数据库，每个作为一张“表”。
func couchSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	body, err := couchGet(ctx, s, "/_all_dbs")
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(body, &names); err != nil {
		return nil, fmt.Errorf("CouchDB 库列表解析失败: %w", err)
	}
	tables := []SchemaTable{}
	for _, name := range names {
		if strings.HasPrefix(name, "_") {
			continue // 系统库
		}
		tables = append(tables, SchemaTable{Name: name, Kind: "database"})
	}
	return &DbSchemaInfo{
		Databases: []SchemaDatabase{{Name: "couchdb", Tables: tables}},
	}, nil
}

// couchDocsToResult 将文档数组解析为列并集 + 行。
func couchDocsToResult(docs []json.RawMessage, executionMs int64) *QueryResult {
	columns := []string{"_id", "_rev"}
	seen := map[string]struct{}{"_id": {}, "_rev": {}}
	parsed := make([]map[string]json.RawMessage, 0, len(docs))
	for _, raw := range docs {
		m := map[string]json.RawMessage{}
		var keys []string
		dec := json.NewDecoder(bytes.NewReader(raw))
		if tok, err := dec.Token(); err == nil {
			if d, ok := tok.(json.Delim); ok && d == '{' {
				for dec.More() {
					kTok, err := dec.Token()
					if err != nil {
						break
					}
					key, _ := kTok.(string)
					var val json.RawMessage
					if err := dec.Decode(&val); err != nil {
						break
					}
					m[key] = val
					keys = append(keys, key)
				}
			}
		}
		parsed = append(parsed, m)
		for _, k := range keys {
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				columns = append(columns, k)
			}
		}
	}
	rows := make([][]any, 0, len(parsed))
	for _, m := range parsed {
		row := make([]any, 0, len(columns))
		for _, col := range columns {
			raw, ok := m[col]
			if !ok {
				row = append(row, nil)
				continue
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				row = append(row, string(raw))
				continue
			}
			switch v.(type) {
			case map[string]any, []any:
				row = append(row, string(raw))
			default:
				row = append(row, v)
			}
		}
		rows = append(rows, row)
	}
	return &QueryResult{
		Columns:     columns,
		Rows:        rows,
		RowCount:    len(rows),
		ExecutionMs: executionMs,
	}
}

// couchTableRows 分页浏览文档（_all_docs + include_docs）。
func couchTableRows(ctx context.Context, s *dbSession, dbName string, limit, offset uint32) (*QueryResult, error) {
	start := time.Now()
	path := fmt.Sprintf("/%s/_all_docs?include_docs=true&limit=%d&skip=%d",
		url.PathEscape(dbName), limit, offset)
	body, err := couchGet(ctx, s, path)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Rows []struct {
			Doc json.RawMessage `json:"doc"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("CouchDB 文档解析失败: %w", err)
	}
	docs := make([]json.RawMessage, 0, len(parsed.Rows))
	for _, r := range parsed.Rows {
		if len(r.Doc) > 0 {
			docs = append(docs, r.Doc)
		}
	}
	return couchDocsToResult(docs, time.Since(start).Milliseconds()), nil
}

// couchExecuteQuery 执行 Mango 查询（POST /{db}/_find）。
// 查询必须为 JSON：可含 db 字段指定库（否则用连接默认库）；
// 含 "selector" 键时整体作为 Mango 查询提交，否则默认 {"selector":{}}。
func couchExecuteQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	start := time.Now()
	var qv map[string]json.RawMessage
	if err := json.Unmarshal([]byte(query), &qv); err != nil {
		return nil, fmt.Errorf("CouchDB 查询需要 JSON 格式: %v", err)
	}
	dbName := s.defaultDatabase
	if raw, ok := qv["db"]; ok {
		var d string
		if json.Unmarshal(raw, &d) == nil && strings.TrimSpace(d) != "" {
			dbName = strings.TrimSpace(d)
		}
		delete(qv, "db")
	}
	if strings.TrimSpace(dbName) == "" {
		return nil, fmt.Errorf(`CouchDB 查询需要指定库：在 JSON 中加入 "db" 字段`)
	}
	if _, ok := qv["selector"]; !ok {
		qv["selector"] = json.RawMessage(`{}`)
	}
	if _, ok := qv["limit"]; !ok {
		qv["limit"] = json.RawMessage(`100`)
	}
	payload, err := json.Marshal(qv)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.httpBaseURL+"/"+url.PathEscape(dbName)+"/_find", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.httpUser != "" {
		req.SetBasicAuth(s.httpUser, s.httpPass)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CouchDB 查询失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("CouchDB 查询响应读取失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("CouchDB 查询失败(%d): %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Docs []json.RawMessage `json:"docs"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("CouchDB 查询响应解析失败: %w", err)
	}
	return couchDocsToResult(parsed.Docs, time.Since(start).Milliseconds()), nil
}
