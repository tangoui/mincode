package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mincode/app/backend/core"
)

type warehouseHTTPConfig struct {
	provider string
	project  string
	catalog  string
	schema   string
	location string
}

func buildWarehouseHTTPSession(cfg *DbConnectionConfig, provider string) (*dbSession, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	host := strings.TrimSpace(cfg.Host)
	if provider == "bigquery" && host == "" {
		host = "bigquery.googleapis.com"
	}
	if host == "" {
		return nil, fmt.Errorf("%s 主机不能为空", warehouseDisplayName(provider))
	}
	port := cfg.Port
	if port == 0 {
		port = warehouseDefaultPort(provider)
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("%s 端口无效", warehouseDisplayName(provider))
	}
	scheme := "http"
	if cfg.SSL || port == 443 || provider == "bigquery" || provider == "snowflake" || provider == "databricks" {
		scheme = "https"
	}
	baseURL := scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))
	baseURL = strings.TrimSuffix(baseURL, "/")

	project := strings.TrimSpace(derefOr(cfg.Database, ""))
	token := strings.TrimSpace(derefOr(cfg.Token, ""))
	if provider == "databricks" && project == "" {
		return nil, fmt.Errorf("Databricks SQL 需要填写 SQL Warehouse ID")
	}
	if provider == "bigquery" && project == "" {
		return nil, fmt.Errorf("BigQuery 需要填写 Project ID")
	}
	if provider == "kylin" && project == "" {
		return nil, fmt.Errorf("Apache Kylin 需要填写 Project")
	}
	if warehouseRequiresToken(provider) && token == "" {
		return nil, fmt.Errorf("%s 需要填写访问令牌", warehouseDisplayName(provider))
	}

	kind, err := warehouseHTTPKind(provider)
	if err != nil {
		return nil, err
	}
	return &dbSession{
		kind:            kindHTTP,
		httpKind:        kind,
		httpClient:      &http.Client{Timeout: 120 * time.Second},
		httpBaseURL:     baseURL,
		httpToken:       token,
		httpUser:        strings.TrimSpace(derefOr(cfg.Username, "")),
		httpPass:        derefOr(cfg.Password, ""),
		httpOrg:         strings.TrimSpace(derefOr(cfg.Org, "")),
		defaultDatabase: project,
		warehouse: &warehouseHTTPConfig{
			provider: provider,
			project:  project,
			catalog:  strings.TrimSpace(derefOr(cfg.Org, "")),
			schema:   strings.TrimSpace(derefOr(cfg.AuthSource, "")),
			location: strings.TrimSpace(derefOr(cfg.Org, "")),
		},
	}, nil
}

func warehouseDisplayName(provider string) string {
	switch provider {
	case "databricks":
		return "Databricks SQL"
	case "bigquery":
		return "BigQuery"
	case "dremio":
		return "Dremio"
	case "kylin":
		return "Apache Kylin"
	case "pinot":
		return "Apache Pinot"
	case "snowflake":
		return "Snowflake SQL API"
	default:
		return provider
	}
}

func warehouseDefaultPort(provider string) int {
	switch provider {
	case "dremio":
		return 9047
	case "kylin":
		return 7070
	case "pinot":
		return 8099
	default:
		return 443
	}
}

func warehouseRequiresToken(provider string) bool {
	switch provider {
	case "databricks", "bigquery", "dremio", "snowflake":
		return true
	default:
		return false
	}
}

func warehouseHTTPKind(provider string) (httpDbKind, error) {
	switch provider {
	case "databricks":
		return httpDatabricks, nil
	case "bigquery":
		return httpBigQuery, nil
	case "dremio":
		return httpDremio, nil
	case "kylin":
		return httpKylin, nil
	case "pinot":
		return httpPinot, nil
	case "snowflake":
		return httpSnowflake, nil
	default:
		return 0, fmt.Errorf("暂不支持的 HTTP 数仓类型: %s", provider)
	}
}

func warehouseExecuteQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	if s.warehouse == nil {
		return nil, fmt.Errorf("HTTP 数仓会话配置缺失")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("查询不能为空")
	}
	switch s.warehouse.provider {
	case "databricks":
		return databricksQuery(ctx, s, query)
	case "bigquery":
		return bigQueryQuery(ctx, s, query)
	case "dremio":
		return dremioQuery(ctx, s, query)
	case "kylin":
		return kylinQuery(ctx, s, query)
	case "pinot":
		return pinotQuery(ctx, s, query)
	case "snowflake":
		return snowflakeQuery(ctx, s, query)
	default:
		return nil, fmt.Errorf("暂不支持的 HTTP 数仓类型: %s", s.warehouse.provider)
	}
}

func warehouseSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	if s.warehouse == nil {
		return nil, fmt.Errorf("HTTP 数仓会话配置缺失")
	}
	if s.warehouse.provider == "bigquery" {
		return bigQuerySchema(ctx, s)
	}
	query := "SHOW TABLES"
	if s.warehouse.provider == "dremio" && s.warehouse.project != "" {
		query = "SHOW TABLES IN " + quoteWarehousePath(s.warehouse.project)
	}
	result, err := warehouseExecuteQuery(ctx, s, query)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 表列表失败: %w", warehouseDisplayName(s.warehouse.provider), err)
	}
	tables := schemaTablesFromQueryResult(result)
	name := warehouseSchemaName(s)
	return &DbSchemaInfo{Databases: []SchemaDatabase{{Name: name, Tables: tables}}}, nil
}

func warehouseSchemaName(s *dbSession) string {
	w := s.warehouse
	if w == nil {
		return "default"
	}
	switch w.provider {
	case "databricks":
		if w.schema != "" {
			return w.schema
		}
		if w.catalog != "" {
			return w.catalog
		}
		return "default"
	case "snowflake":
		if w.schema != "" {
			return w.schema
		}
		if w.project != "" {
			return w.project
		}
		return "PUBLIC"
	default:
		if w.project != "" {
			return w.project
		}
		return "default"
	}
}

func schemaTablesFromQueryResult(result *QueryResult) []SchemaTable {
	if result == nil {
		return []SchemaTable{}
	}
	idx := -1
	for i, column := range result.Columns {
		n := strings.ToLower(strings.ReplaceAll(column, "_", ""))
		if n == "tablename" || n == "name" || n == "tabname" || strings.HasSuffix(n, "tablename") {
			idx = i
			break
		}
	}
	if idx < 0 && len(result.Columns) > 0 {
		idx = len(result.Columns) - 1
	}
	seen := map[string]struct{}{}
	out := make([]SchemaTable, 0, len(result.Rows))
	for _, row := range result.Rows {
		if idx < 0 || idx >= len(row) || row[idx] == nil {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(row[idx]))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, SchemaTable{Name: name, Kind: "table"})
	}
	return out
}

func warehouseTableRows(ctx context.Context, s *dbSession, database, table string, limit, offset uint32) (*QueryResult, error) {
	if s.warehouse == nil {
		return nil, fmt.Errorf("HTTP 数仓会话配置缺失")
	}
	if limit == 0 {
		limit = 500
	}
	var target string
	switch s.warehouse.provider {
	case "bigquery":
		parts := []string{s.warehouse.project}
		if strings.TrimSpace(database) != "" {
			parts = append(parts, database)
		}
		parts = append(parts, table)
		target = "`" + strings.ReplaceAll(strings.Join(parts, "."), "`", "``") + "`"
	case "pinot", "kylin":
		target = quoteWarehouseIdent(table)
	case "databricks":
		// Spark SQL 中双引号是字符串字面量，标识符必须用反引号。
		if strings.TrimSpace(database) != "" {
			target = quoteBacktickPath(database) + "."
		}
		target += quoteBacktickIdent(table)
	default:
		if strings.TrimSpace(database) != "" {
			target = quoteWarehousePath(database) + "."
		}
		target += quoteWarehouseIdent(table)
	}
	query := fmt.Sprintf("SELECT * FROM %s LIMIT %d", target, limit)
	if offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", offset)
	}
	return warehouseExecuteQuery(ctx, s, query)
}

func quoteWarehouseIdent(value string) string {
	return `"` + strings.ReplaceAll(strings.TrimSpace(value), `"`, `""`) + `"`
}
func quoteWarehousePath(value string) string {
	parts := strings.Split(value, ".")
	for i := range parts {
		parts[i] = quoteWarehouseIdent(parts[i])
	}
	return strings.Join(parts, ".")
}

func quoteBacktickIdent(value string) string {
	return "`" + escapeBacktick(strings.TrimSpace(value)) + "`"
}

func quoteBacktickPath(value string) string {
	parts := strings.Split(value, ".")
	for i := range parts {
		parts[i] = quoteBacktickIdent(parts[i])
	}
	return strings.Join(parts, ".")
}

func warehouseJSONRequest(ctx context.Context, s *dbSession, method, endpoint string, payload any, auth string) ([]byte, int, error) {
	headers := map[string]string{}
	if auth != "" {
		headers["Authorization"] = auth
	}
	return warehouseJSONRequestWithHeaders(ctx, s, method, endpoint, payload, headers)
}

func warehouseJSONRequestWithHeaders(ctx context.Context, s *dbSession, method, endpoint string, payload any, headers map[string]string) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(encoded)
	}
	requestURL, err := warehouseRequestURL(s.httpBaseURL, endpoint)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求 %s 失败: %w", warehouseDisplayName(s.warehouse.provider), err)
	}
	defer resp.Body.Close()
	responseBody, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseBody, resp.StatusCode, fmt.Errorf("%s 请求失败(%d): %s", warehouseDisplayName(s.warehouse.provider), resp.StatusCode, warehouseErrorMessage(responseBody))
	}
	return responseBody, resp.StatusCode, nil
}

func warehouseRequestURL(baseURL, endpoint string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("HTTP 数仓地址无效: %w", err)
	}
	ref, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", fmt.Errorf("HTTP 数仓请求路径无效: %w", err)
	}
	if ref.IsAbs() {
		if !strings.EqualFold(ref.Scheme, base.Scheme) || !strings.EqualFold(ref.Host, base.Host) {
			return "", fmt.Errorf("拒绝访问数仓主机之外的结果地址")
		}
		return ref.String(), nil
	}
	if !strings.HasPrefix(ref.Path, "/") {
		ref.Path = "/" + ref.Path
	}
	base.Path = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base.ResolveReference(ref).String(), nil
}

func warehouseErrorMessage(body []byte) string {
	var value any
	if json.Unmarshal(body, &value) == nil {
		if message := findJSONMessage(value); message != "" {
			return message
		}
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "无返回内容"
	}
	if len(text) > 1000 {
		return text[:1000] + "…"
	}
	return text
}

func findJSONMessage(value any) string {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"message", "errorMessage", "error", "detail"} {
			if raw, ok := v[key]; ok {
				if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
					return text
				}
				if nested := findJSONMessage(raw); nested != "" {
					return nested
				}
			}
		}
		for _, raw := range v {
			if nested := findJSONMessage(raw); nested != "" {
				return nested
			}
		}
	case []any:
		for _, raw := range v {
			if nested := findJSONMessage(raw); nested != "" {
				return nested
			}
		}
	}
	return ""
}

func rowsResult(columns []string, rows [][]any, started time.Time) *QueryResult {
	if columns == nil {
		columns = []string{}
	}
	if rows == nil {
		rows = [][]any{}
	}
	return &QueryResult{Columns: columns, Rows: rows, RowCount: len(rows), ExecutionMs: time.Since(started).Milliseconds()}
}

func appendWarehouseRows(rows *[][]any, usedBytes *int, page [][]any) bool {
	for _, row := range page {
		if len(*rows) >= dbMaxResultRows {
			return false
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			encoded = []byte(fmt.Sprint(row))
		}
		if *usedBytes+len(encoded) > dbMaxResultBytes {
			return false
		}
		*usedBytes += len(encoded)
		*rows = append(*rows, row)
	}
	return true
}

func warehouseRowsResult(columns []string, rows [][]any, started time.Time, truncated bool) *QueryResult {
	result := rowsResult(columns, rows, started)
	if truncated {
		notice := fmt.Sprintf("查询结果已截断：最多返回 %d 行或 %d MiB，请添加 LIMIT 或缩小查询范围", dbMaxResultRows, dbMaxResultBytes/(1024*1024))
		result.Notice = &notice
	}
	return result
}

type databricksStatementResponse struct {
	StatementID string `json:"statement_id"`
	Status      struct {
		State string `json:"state"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"status"`
	Manifest struct {
		Schema struct {
			Columns []struct {
				Name string `json:"name"`
			} `json:"columns"`
		} `json:"schema"`
	} `json:"manifest"`
	Result struct {
		Data [][]any `json:"data_array"`
		Next string  `json:"next_chunk_internal_link"`
	} `json:"result"`
}

func databricksQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	started := time.Now()
	w := s.warehouse
	payload := map[string]any{"statement": query, "warehouse_id": w.project, "wait_timeout": "30s", "disposition": "INLINE", "format": "JSON_ARRAY", "row_limit": dbMaxResultRows}
	if w.catalog != "" {
		payload["catalog"] = w.catalog
	}
	if w.schema != "" {
		payload["schema"] = w.schema
	}
	body, _, err := warehouseJSONRequest(ctx, s, http.MethodPost, "/api/2.0/sql/statements", payload, "Bearer "+s.httpToken)
	if err != nil {
		return nil, err
	}
	var response databricksStatementResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("Databricks SQL 响应解析失败: %w", err)
	}
	for response.Status.State == "PENDING" || response.Status.State == "RUNNING" {
		if response.StatementID == "" {
			return nil, fmt.Errorf("Databricks SQL 未返回 Statement ID")
		}
		if err := waitWarehousePoll(ctx); err != nil {
			return nil, err
		}
		body, _, err = warehouseJSONRequest(ctx, s, http.MethodGet, "/api/2.0/sql/statements/"+url.PathEscape(response.StatementID), nil, "Bearer "+s.httpToken)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("Databricks SQL 状态解析失败: %w", err)
		}
	}
	if response.Status.Error != nil {
		return nil, fmt.Errorf("Databricks SQL 查询失败: %s", response.Status.Error.Message)
	}
	if response.Status.State != "SUCCEEDED" {
		return nil, fmt.Errorf("Databricks SQL 查询状态: %s", response.Status.State)
	}
	columns := make([]string, len(response.Manifest.Schema.Columns))
	for i, column := range response.Manifest.Schema.Columns {
		columns[i] = column.Name
	}
	rows := make([][]any, 0, len(response.Result.Data))
	usedBytes := 0
	truncated := !appendWarehouseRows(&rows, &usedBytes, response.Result.Data)
	next := response.Result.Next
	for next != "" && !truncated {
		if len(rows) >= dbMaxResultRows {
			truncated = true
			break
		}
		chunkBody, _, chunkErr := warehouseJSONRequest(ctx, s, http.MethodGet, next, nil, "Bearer "+s.httpToken)
		if chunkErr != nil {
			return nil, chunkErr
		}
		var chunk struct {
			Data   [][]any `json:"data_array"`
			Next   string  `json:"next_chunk_internal_link"`
			Result struct {
				Data [][]any `json:"data_array"`
				Next string  `json:"next_chunk_internal_link"`
			} `json:"result"`
		}
		if err := json.Unmarshal(chunkBody, &chunk); err != nil {
			return nil, fmt.Errorf("Databricks SQL 分块结果解析失败: %w", err)
		}
		data := chunk.Data
		if data == nil {
			data = chunk.Result.Data
		}
		next = chunk.Next
		if next == "" {
			next = chunk.Result.Next
		}
		if !appendWarehouseRows(&rows, &usedBytes, data) {
			truncated = true
		}
	}
	return warehouseRowsResult(columns, rows, started, truncated), nil
}

type bigQueryResponse struct {
	JobComplete bool   `json:"jobComplete"`
	PageToken   string `json:"pageToken"`
	JobRef      struct {
		ProjectID string `json:"projectId"`
		JobID     string `json:"jobId"`
		Location  string `json:"location"`
	} `json:"jobReference"`
	Schema struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	} `json:"schema"`
	Rows []struct {
		F []struct {
			V any `json:"v"`
		} `json:"f"`
	} `json:"rows"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func bigQueryQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	started := time.Now()
	w := s.warehouse
	payload := map[string]any{"query": query, "useLegacySql": false, "maxResults": dbMaxResultRows, "timeoutMs": 120000}
	if w.location != "" {
		payload["location"] = w.location
	}
	endpoint := "/bigquery/v2/projects/" + url.PathEscape(w.project) + "/queries"
	body, _, err := warehouseJSONRequest(ctx, s, http.MethodPost, endpoint, payload, "Bearer "+s.httpToken)
	if err != nil {
		return nil, err
	}
	response, err := parseBigQueryResponse(body)
	if err != nil {
		return nil, err
	}
	jobID := response.JobRef.JobID
	projectID := response.JobRef.ProjectID
	if projectID == "" {
		projectID = w.project
	}
	location := response.JobRef.Location
	if location == "" {
		location = w.location
	}
	for !response.JobComplete {
		if jobID == "" {
			return nil, fmt.Errorf("BigQuery 查询尚未完成且未返回 Job ID")
		}
		if err := waitWarehousePoll(ctx); err != nil {
			return nil, err
		}
		response, err = bigQueryResultsPage(ctx, s, projectID, jobID, location, "", dbMaxResultRows)
		if err != nil {
			return nil, err
		}
	}
	if len(response.Errors) > 0 && len(response.Rows) == 0 {
		return nil, fmt.Errorf("BigQuery 查询失败: %s", response.Errors[0].Message)
	}
	columns := make([]string, len(response.Schema.Fields))
	for i, field := range response.Schema.Fields {
		columns[i] = field.Name
	}
	rows := make([][]any, 0, len(response.Rows))
	usedBytes := 0
	truncated := !appendWarehouseRows(&rows, &usedBytes, bigQueryRows(response))
	pageToken := response.PageToken
	for pageToken != "" && !truncated {
		remaining := dbMaxResultRows - len(rows)
		if remaining <= 0 {
			truncated = true
			break
		}
		page, pageErr := bigQueryResultsPage(ctx, s, projectID, jobID, location, pageToken, remaining)
		if pageErr != nil {
			return nil, pageErr
		}
		if len(columns) == 0 && len(page.Schema.Fields) > 0 {
			columns = make([]string, len(page.Schema.Fields))
			for i, field := range page.Schema.Fields {
				columns[i] = field.Name
			}
		}
		if !appendWarehouseRows(&rows, &usedBytes, bigQueryRows(page)) {
			truncated = true
		}
		pageToken = page.PageToken
	}
	return warehouseRowsResult(columns, rows, started, truncated), nil
}

func parseBigQueryResponse(body []byte) (*bigQueryResponse, error) {
	var response bigQueryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("BigQuery 响应解析失败: %w", err)
	}
	return &response, nil
}

func bigQueryResultsPage(ctx context.Context, s *dbSession, projectID, jobID, location, pageToken string, maxResults int) (*bigQueryResponse, error) {
	values := url.Values{}
	values.Set("maxResults", strconv.Itoa(maxResults))
	if location != "" {
		values.Set("location", location)
	}
	if pageToken != "" {
		values.Set("pageToken", pageToken)
	}
	endpoint := "/bigquery/v2/projects/" + url.PathEscape(projectID) + "/queries/" + url.PathEscape(jobID) + "?" + values.Encode()
	body, _, err := warehouseJSONRequest(ctx, s, http.MethodGet, endpoint, nil, "Bearer "+s.httpToken)
	if err != nil {
		return nil, err
	}
	return parseBigQueryResponse(body)
}

func bigQueryRows(response *bigQueryResponse) [][]any {
	rows := make([][]any, len(response.Rows))
	for i, source := range response.Rows {
		row := make([]any, len(source.F))
		for j, cell := range source.F {
			row[j] = cell.V
		}
		rows[i] = row
	}
	return rows
}

func waitWarehousePoll(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(350 * time.Millisecond):
		return nil
	}
}

func bigQuerySchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	w := s.warehouse
	endpoint := "/bigquery/v2/projects/" + url.PathEscape(w.project) + "/datasets?maxResults=1000"
	body, _, err := warehouseJSONRequest(ctx, s, http.MethodGet, endpoint, nil, "Bearer "+s.httpToken)
	if err != nil {
		return nil, err
	}
	var response struct {
		Datasets []struct {
			Ref struct {
				DatasetID string `json:"datasetId"`
			} `json:"datasetReference"`
		} `json:"datasets"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("BigQuery 数据集响应解析失败: %w", err)
	}
	out := make([]SchemaDatabase, 0, len(response.Datasets))
	for _, dataset := range response.Datasets {
		id := dataset.Ref.DatasetID
		if id == "" {
			continue
		}
		tableEndpoint := "/bigquery/v2/projects/" + url.PathEscape(w.project) + "/datasets/" + url.PathEscape(id) + "/tables?maxResults=1000"
		tableBody, _, tableErr := warehouseJSONRequest(ctx, s, http.MethodGet, tableEndpoint, nil, "Bearer "+s.httpToken)
		if tableErr != nil {
			return nil, tableErr
		}
		var tablesResponse struct {
			Tables []struct {
				Ref struct {
					TableID string `json:"tableId"`
				} `json:"tableReference"`
				Type string `json:"type"`
			} `json:"tables"`
		}
		if err := json.Unmarshal(tableBody, &tablesResponse); err != nil {
			return nil, err
		}
		tables := make([]SchemaTable, 0, len(tablesResponse.Tables))
		for _, table := range tablesResponse.Tables {
			kind := "table"
			if strings.Contains(strings.ToUpper(table.Type), "VIEW") {
				kind = "view"
			}
			tables = append(tables, SchemaTable{Name: table.Ref.TableID, Kind: kind})
		}
		out = append(out, SchemaDatabase{Name: id, Tables: tables})
	}
	return &DbSchemaInfo{Databases: out, Catalog: &w.project}, nil
}

func dremioQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	started := time.Now()
	auth := "Bearer " + s.httpToken
	body, _, err := warehouseJSONRequest(ctx, s, http.MethodPost, "/api/v3/sql", map[string]any{"sql": query}, auth)
	if err != nil {
		return nil, err
	}
	var submitted struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &submitted); err != nil || submitted.ID == "" {
		return nil, fmt.Errorf("Dremio 未返回 Job ID")
	}
	for {
		statusBody, _, statusErr := warehouseJSONRequest(ctx, s, http.MethodGet, "/api/v3/job/"+url.PathEscape(submitted.ID), nil, auth)
		if statusErr != nil {
			return nil, statusErr
		}
		var status struct {
			JobState           string `json:"jobState"`
			ErrorMessage       string `json:"errorMessage"`
			CancellationReason string `json:"cancellationReason"`
		}
		if err := json.Unmarshal(statusBody, &status); err != nil {
			return nil, fmt.Errorf("Dremio Job 状态解析失败: %w", err)
		}
		switch status.JobState {
		case "COMPLETED":
			goto fetch
		case "FAILED", "CANCELED", "CANCELLATION_REQUESTED":
			message := status.ErrorMessage
			if message == "" {
				message = status.CancellationReason
			}
			if message != "" {
				return nil, fmt.Errorf("Dremio 查询状态: %s: %s", status.JobState, message)
			}
			return nil, fmt.Errorf("Dremio 查询状态: %s", status.JobState)
		}
		if err := waitWarehousePoll(ctx); err != nil {
			return nil, err
		}
	}

fetch:
	const pageSize = 1000
	columns := []string{}
	rows := make([][]any, 0, pageSize)
	usedBytes := 0
	truncated := false
	for offset := 0; ; offset += pageSize {
		remaining := dbMaxResultRows - len(rows)
		if remaining <= 0 {
			truncated = true
			break
		}
		limit := pageSize
		if remaining < limit {
			limit = remaining
		}
		endpoint := "/api/v3/job/" + url.PathEscape(submitted.ID) + "/results?offset=" + strconv.Itoa(offset) + "&limit=" + strconv.Itoa(limit)
		resultBody, _, resultErr := warehouseJSONRequest(ctx, s, http.MethodGet, endpoint, nil, auth)
		if resultErr != nil {
			return nil, resultErr
		}
		var result struct {
			RowCount int `json:"rowCount"`
			Schema   []struct {
				Name string `json:"name"`
			} `json:"schema"`
			Rows []map[string]any `json:"rows"`
		}
		if err := json.Unmarshal(resultBody, &result); err != nil {
			return nil, fmt.Errorf("Dremio 结果解析失败: %w", err)
		}
		if len(columns) == 0 {
			columns = make([]string, len(result.Schema))
			for i, column := range result.Schema {
				columns[i] = column.Name
			}
		}
		pageRows := make([][]any, len(result.Rows))
		for i, item := range result.Rows {
			row := make([]any, len(columns))
			for j, column := range columns {
				row[j] = item[column]
			}
			pageRows[i] = row
		}
		if !appendWarehouseRows(&rows, &usedBytes, pageRows) {
			truncated = true
			break
		}
		if len(result.Rows) < limit || (result.RowCount > 0 && len(rows) >= result.RowCount) {
			break
		}
	}
	return warehouseRowsResult(columns, rows, started, truncated), nil
}

type kylinColumnMeta struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

type kylinQueryResponse struct {
	ColumnMetasCamel []kylinColumnMeta `json:"columnMetas"`
	ColumnMetasSnake []kylinColumnMeta `json:"column_metas"`
	Results          [][]any           `json:"results"`
	ExceptionCamel   string            `json:"exceptionMessage"`
	ExceptionSnake   string            `json:"exception_message"`
	Message          string            `json:"msg"`
	IsException      bool              `json:"isException"`
	IsExceptionSnake bool              `json:"is_exception"`
	Data             json.RawMessage   `json:"data"`
}

func kylinQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	started := time.Now()
	payload := map[string]any{"sql": query, "project": s.warehouse.project, "limit": dbMaxResultRows, "offset": 0, "acceptPartial": false}
	var body []byte
	var err error
	if s.httpToken != "" {
		body, _, err = warehouseJSONRequestWithHeaders(ctx, s, http.MethodPost, "/kylin/api/query", payload, map[string]string{
			"Authorization": "Bearer " + s.httpToken,
			"Accept":        "application/vnd.apache.kylin-v4-public+json",
		})
	} else {
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, marshalErr
		}
		requestURL, requestURLErr := warehouseRequestURL(s.httpBaseURL, "/kylin/api/query")
		if requestURLErr != nil {
			return nil, requestURLErr
		}
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(encoded))
		if requestErr != nil {
			return nil, requestErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/vnd.apache.kylin-v4-public+json")
		if s.httpUser != "" {
			req.SetBasicAuth(s.httpUser, s.httpPass)
		}
		resp, requestErr := s.httpClient.Do(req)
		if requestErr != nil {
			return nil, fmt.Errorf("Apache Kylin 请求失败: %w", requestErr)
		}
		defer resp.Body.Close()
		body, requestErr = core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
		if requestErr != nil {
			return nil, requestErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err = fmt.Errorf("Apache Kylin 请求失败(%d): %s", resp.StatusCode, warehouseErrorMessage(body))
		}
	}
	if err != nil {
		return nil, err
	}
	response, err := parseKylinQueryResponse(body)
	if err != nil {
		return nil, err
	}
	exception := response.ExceptionCamel
	if exception == "" {
		exception = response.ExceptionSnake
	}
	if exception != "" || response.IsException || response.IsExceptionSnake {
		if exception == "" {
			exception = response.Message
		}
		if exception == "" {
			exception = "服务器返回查询异常"
		}
		return nil, fmt.Errorf("Apache Kylin 查询失败: %s", exception)
	}
	columnMetas := response.ColumnMetasCamel
	if len(columnMetas) == 0 {
		columnMetas = response.ColumnMetasSnake
	}
	columns := make([]string, len(columnMetas))
	for i, column := range columnMetas {
		columns[i] = column.Label
		if columns[i] == "" {
			columns[i] = column.Name
		}
	}
	return rowsResult(columns, response.Results, started), nil
}

func parseKylinQueryResponse(body []byte) (*kylinQueryResponse, error) {
	var response kylinQueryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("Apache Kylin 响应解析失败: %w", err)
	}
	if len(response.Data) > 0 && string(response.Data) != "null" {
		var nested kylinQueryResponse
		if err := json.Unmarshal(response.Data, &nested); err != nil {
			return nil, fmt.Errorf("Apache Kylin data 响应解析失败: %w", err)
		}
		if len(nested.ColumnMetasCamel) > 0 || len(nested.ColumnMetasSnake) > 0 || nested.Results != nil || nested.ExceptionCamel != "" || nested.ExceptionSnake != "" || nested.IsException || nested.IsExceptionSnake {
			if nested.Message == "" {
				nested.Message = response.Message
			}
			return &nested, nil
		}
	}
	return &response, nil
}

func pinotQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	started := time.Now()
	auth := ""
	if s.httpToken != "" {
		auth = "Bearer " + s.httpToken
	}
	body, _, err := warehouseJSONRequest(ctx, s, http.MethodPost, "/query/sql", map[string]any{"sql": query}, auth)
	if err != nil {
		return nil, err
	}
	var response struct {
		ResultTable struct {
			DataSchema struct {
				ColumnNames []string `json:"columnNames"`
			} `json:"dataSchema"`
			Rows [][]any `json:"rows"`
		} `json:"resultTable"`
		Exceptions []struct {
			Message string `json:"message"`
		} `json:"exceptions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("Apache Pinot 响应解析失败: %w", err)
	}
	if len(response.Exceptions) > 0 {
		return nil, fmt.Errorf("Apache Pinot 查询失败: %s", response.Exceptions[0].Message)
	}
	rows := make([][]any, 0, len(response.ResultTable.Rows))
	usedBytes := 0
	truncated := !appendWarehouseRows(&rows, &usedBytes, response.ResultTable.Rows)
	return warehouseRowsResult(response.ResultTable.DataSchema.ColumnNames, rows, started, truncated), nil
}

type snowflakeSQLResponse struct {
	Data [][]any `json:"data"`
	Meta struct {
		RowType []struct {
			Name string `json:"name"`
		} `json:"rowType"`
		PartitionInfo []json.RawMessage `json:"partitionInfo"`
	} `json:"resultSetMetaData"`
	Message            string `json:"message"`
	Code               string `json:"code"`
	SQLState           string `json:"sqlState"`
	StatementHandle    string `json:"statementHandle"`
	StatementStatusURL string `json:"statementStatusUrl"`
}

func snowflakeQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	started := time.Now()
	w := s.warehouse
	payload := map[string]any{"statement": query, "timeout": 120, "parameters": map[string]string{"MULTI_STATEMENT_COUNT": "0"}}
	if w.project != "" {
		payload["database"] = w.project
	}
	if w.catalog != "" {
		payload["warehouse"] = w.catalog
	}
	if w.schema != "" {
		payload["schema"] = w.schema
	}
	headers := map[string]string{
		"Authorization": "Bearer " + s.httpToken,
		"User-Agent":    "MinCode",
	}
	body, statusCode, err := warehouseJSONRequestWithHeaders(ctx, s, http.MethodPost, "/api/v2/statements", payload, headers)
	if err != nil {
		return nil, err
	}
	response, err := parseSnowflakeSQLResponse(body)
	if err != nil {
		return nil, err
	}
	statusEndpoint := response.StatementStatusURL
	if statusEndpoint == "" && response.StatementHandle != "" {
		statusEndpoint = "/api/v2/statements/" + url.PathEscape(response.StatementHandle)
	}
	for statusCode == http.StatusAccepted {
		if statusEndpoint == "" {
			return nil, fmt.Errorf("Snowflake SQL API 查询尚未完成且未返回状态地址")
		}
		if err := waitWarehousePoll(ctx); err != nil {
			return nil, err
		}
		body, statusCode, err = warehouseJSONRequestWithHeaders(ctx, s, http.MethodGet, statusEndpoint, nil, headers)
		if err != nil {
			return nil, err
		}
		polled, parseErr := parseSnowflakeSQLResponse(body)
		if parseErr != nil {
			return nil, parseErr
		}
		if polled.StatementHandle == "" {
			polled.StatementHandle = response.StatementHandle
		}
		if polled.StatementStatusURL == "" {
			polled.StatementStatusURL = statusEndpoint
		}
		response = polled
	}
	if response.SQLState != "" && response.SQLState != "00000" {
		return nil, fmt.Errorf("Snowflake SQL API 查询失败(%s): %s", response.SQLState, response.Message)
	}
	columns := make([]string, len(response.Meta.RowType))
	for i, column := range response.Meta.RowType {
		columns[i] = column.Name
	}
	rows := make([][]any, 0, len(response.Data))
	usedBytes := 0
	truncated := !appendWarehouseRows(&rows, &usedBytes, response.Data)
	partitionCount := len(response.Meta.PartitionInfo)
	if partitionCount > 1 && statusEndpoint == "" && response.StatementHandle != "" {
		statusEndpoint = "/api/v2/statements/" + url.PathEscape(response.StatementHandle)
	}
	if partitionCount > 1 && statusEndpoint == "" {
		return nil, fmt.Errorf("Snowflake SQL API 返回分区结果但未提供结果地址")
	}
	for partition := 1; partition < partitionCount && !truncated; partition++ {
		if len(rows) >= dbMaxResultRows {
			truncated = true
			break
		}
		endpoint, endpointErr := appendWarehouseQuery(statusEndpoint, "partition", strconv.Itoa(partition))
		if endpointErr != nil {
			return nil, endpointErr
		}
		partitionBody, _, partitionErr := warehouseJSONRequestWithHeaders(ctx, s, http.MethodGet, endpoint, nil, headers)
		if partitionErr != nil {
			return nil, partitionErr
		}
		partitionResponse, parseErr := parseSnowflakeSQLResponse(partitionBody)
		if parseErr != nil {
			return nil, parseErr
		}
		if !appendWarehouseRows(&rows, &usedBytes, partitionResponse.Data) {
			truncated = true
		}
	}
	return warehouseRowsResult(columns, rows, started, truncated), nil
}

func parseSnowflakeSQLResponse(body []byte) (*snowflakeSQLResponse, error) {
	var response snowflakeSQLResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("Snowflake SQL API 响应解析失败: %w", err)
	}
	return &response, nil
}

func appendWarehouseQuery(endpoint, key, value string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("数仓结果地址无效: %w", err)
	}
	values := parsed.Query()
	values.Set(key, value)
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}
