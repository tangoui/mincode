// Elasticsearch 连接与只读浏览（HTTP，非 SQL）。
//
// 与 Tauri 侧对齐（src-tauri/src/db/commands/session.rs）：
//   - 连接：GET / 探活
//   - 授权：优先 ApiKey（token），否则 Basic Auth（username/password）
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

// applyElasticAuth 为请求设置 Elasticsearch 授权头。
func applyElasticAuth(req *http.Request, token, user, pass string) {
	if t := strings.TrimSpace(token); t != "" {
		req.Header.Set("Authorization", "ApiKey "+t)
		return
	}
	if u := strings.TrimSpace(user); u != "" {
		req.SetBasicAuth(u, pass)
	}
}

func buildElasticsearchSession(cfg *DbConnectionConfig) (*dbSession, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 9200
	}
	scheme := "http"
	if cfg.SSL {
		scheme = "https"
	}
	baseURL := fmt.Sprintf("%s://%s:%d", scheme, host, port)
	token := derefOr(cfg.Token, "")
	user := derefOr(cfg.Username, "")
	pass := derefOr(cfg.Password, "")

	client := &http.Client{Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 探活
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return nil, err
	}
	applyElasticAuth(req, token, user, pass)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Elasticsearch 连接失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Elasticsearch 探活失败: %d", resp.StatusCode)
	}

	return &dbSession{
		kind:        kindHTTP,
		httpKind:    httpElastic,
		httpClient:  client,
		httpBaseURL: baseURL,
		httpToken:   token,
		httpUser:    user,
		httpPass:    pass,
	}, nil
}

// elasticsearchSchema 列出所有索引（GET /_cat/indices?format=json），每个索引作为一张表。
func elasticsearchSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.httpBaseURL+"/_cat/indices?format=json", nil)
	if err != nil {
		return nil, err
	}
	applyElasticAuth(req, s.httpToken, s.httpUser, s.httpPass)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Elasticsearch 索引列表读取失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("Elasticsearch 索引响应读取失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Elasticsearch 索引列表读取失败(%d): %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var items []struct {
		Index string `json:"index"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("Elasticsearch 索引响应解析失败: %w", err)
	}

	var tables []SchemaTable
	for _, it := range items {
		name := strings.TrimSpace(it.Index)
		if name == "" || strings.HasPrefix(name, ".") {
			continue // 跳过系统索引
		}
		tables = append(tables, SchemaTable{Name: name, Kind: "index"})
	}
	return &DbSchemaInfo{
		Databases: []SchemaDatabase{{Name: "elasticsearch", Tables: tables}},
	}, nil
}

// elasticsearchSearch 向 /{index}/_search 提交查询 DSL 并返回结果。
func elasticsearchSearch(ctx context.Context, s *dbSession, index string, body []byte, startMs int64) (*QueryResult, error) {
	if strings.TrimSpace(index) == "" {
		index = "_all"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.httpBaseURL+"/"+url.PathEscape(index)+"/_search", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	applyElasticAuth(req, s.httpToken, s.httpUser, s.httpPass)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Elasticsearch 查询失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("Elasticsearch 查询响应读取失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Elasticsearch 查询失败(%d): %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return parseElasticResponse(respBody, startMs)
}

// parseElasticResponse 解析 _search 响应（对齐 Tauri parse_es_response）：
// 列为 _id、_index 加上所有 _source 键的并集（按出现顺序去重）。
func parseElasticResponse(body []byte, executionMs int64) (*QueryResult, error) {
	var parsed struct {
		Hits struct {
			Hits []struct {
				ID     string          `json:"_id"`
				Index  string          `json:"_index"`
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("Elasticsearch 响应解析失败: %w", err)
	}
	hits := parsed.Hits.Hits
	if len(hits) == 0 {
		return &QueryResult{Columns: []string{}, Rows: [][]any{}, RowCount: 0, ExecutionMs: executionMs}, nil
	}

	columns := []string{"_id", "_index"}
	seen := map[string]struct{}{"_id": {}, "_index": {}}
	sources := make([]map[string]json.RawMessage, len(hits))
	sourceKeys := make([][]string, len(hits))
	for i, h := range hits {
		src := map[string]json.RawMessage{}
		var keys []string
		if len(h.Source) > 0 {
			// 用 Decoder 顺序解析 key，保证列顺序稳定
			dec := json.NewDecoder(bytes.NewReader(h.Source))
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
						src[key] = raw
						keys = append(keys, key)
					}
				}
			}
		}
		sources[i] = src
		sourceKeys[i] = keys
		for _, k := range keys {
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				columns = append(columns, k)
			}
		}
	}

	rows := make([][]any, 0, len(hits))
	for i, h := range hits {
		row := make([]any, 0, len(columns))
		for _, col := range columns {
			switch col {
			case "_id":
				row = append(row, h.ID)
			case "_index":
				row = append(row, h.Index)
			default:
				if raw, ok := sources[i][col]; ok {
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
				} else {
					row = append(row, nil)
				}
			}
		}
		rows = append(rows, row)
	}
	return &QueryResult{
		Columns:     columns,
		Rows:        rows,
		RowCount:    len(rows),
		ExecutionMs: executionMs,
	}, nil
}

// elasticsearchTableRows 分页浏览索引文档（match_all + from/size）。
func elasticsearchTableRows(ctx context.Context, s *dbSession, index string, limit, offset uint32) (*QueryResult, error) {
	start := time.Now()
	body := fmt.Sprintf(`{"query":{"match_all":{}},"from":%d,"size":%d}`, offset, limit)
	res, err := elasticsearchSearch(ctx, s, index, []byte(body), 0)
	if err != nil {
		return nil, err
	}
	res.ExecutionMs = time.Since(start).Milliseconds()
	return res, nil
}

// elasticsearchExecuteQuery 执行用户输入的查询（对齐 Tauri execute.rs Elasticsearch 分支）。
// 查询必须是 JSON：可包含 index 字段指定索引；含 "query" 键时整体作为 DSL 提交，否则默认 match_all。
func elasticsearchExecuteQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	start := time.Now()
	var qv map[string]json.RawMessage
	if err := json.Unmarshal([]byte(query), &qv); err != nil {
		return nil, fmt.Errorf("Elasticsearch 查询需要 JSON 格式: %v", err)
	}
	index := "_all"
	if raw, ok := qv["index"]; ok {
		var idx string
		if json.Unmarshal(raw, &idx) == nil && strings.TrimSpace(idx) != "" {
			index = strings.TrimSpace(idx)
		}
		delete(qv, "index")
	}
	body, err := json.Marshal(qv)
	if err != nil {
		return nil, fmt.Errorf("Elasticsearch 查询序列化失败: %w", err)
	}
	if _, ok := qv["query"]; !ok {
		body = []byte(`{"query":{"match_all":{}}}`)
	}
	res, err := elasticsearchSearch(ctx, s, index, body, 0)
	if err != nil {
		return nil, err
	}
	res.ExecutionMs = time.Since(start).Milliseconds()
	return res, nil
}
