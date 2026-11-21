// InfluxDB 连接与只读浏览（HTTP，非 SQL）。
//
// 与 Tauri 侧对齐（src-tauri/src/db/mod.rs）：
//   - 连接：GET /health 健康检查
//   - Org 解析：优先用配置 org，否则 GET /api/v2/orgs 自动读取
//   - Token 授权头：无前缀时补 "Token "
//
// 写操作暂返回明确不支持提示。
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mincode/app/backend/core"
)

// influxAuthHeader 构造 InfluxDB Authorization 头（对齐 Tauri influxdb_authorization_header）。
func influxAuthHeader(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	lower := strings.ToLower(token)
	if strings.HasPrefix(lower, "token ") || strings.HasPrefix(lower, "bearer ") {
		return token
	}
	return "Token " + token
}

// resolveInfluxOrg 优先返回配置 org，否则调用 /api/v2/orgs 自动读取。
func resolveInfluxOrg(ctx context.Context, client *http.Client, baseURL, token, configuredOrg string) (string, error) {
	if org := strings.TrimSpace(configuredOrg); org != "" {
		return org, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v2/orgs", nil)
	if err != nil {
		return "", err
	}
	if header := influxAuthHeader(token); header != "" {
		req.Header.Set("Authorization", header)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("InfluxDB 组织自动读取失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return "", fmt.Errorf("InfluxDB 组织响应读取失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("InfluxDB 组织自动读取失败(%d): %s。请在连接配置里填写 Org，或使用具备 org 读取权限的 Token",
			resp.StatusCode, influxErrorMessage(body))
	}
	var parsed struct {
		Orgs []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"orgs"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("InfluxDB 组织响应解析失败: %w", err)
	}
	for _, o := range parsed.Orgs {
		if n := strings.TrimSpace(o.Name); n != "" {
			return n, nil
		}
		if id := strings.TrimSpace(o.ID); id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("InfluxDB 未读取到组织，请在连接配置里填写 Org")
}

// influxErrorMessage 从错误响应体提取可读消息。
func influxErrorMessage(body []byte) string {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err == nil {
		if m, ok := parsed["message"].(string); ok && m != "" {
			return m
		}
		if e, ok := parsed["error"].(string); ok && e != "" {
			return e
		}
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "无返回内容"
	}
	return text
}

// influxBaseURL 构造 InfluxDB HTTP 地址。默认 localhost 使用 IPv4 回环，避免
// macOS/Go 将其优先解析为 ::1，而本地 Docker 服务只监听 127.0.0.1 的情况。
// 显式配置的 IPv6 地址仍会按原样使用。
func influxBaseURL(cfg *DbConnectionConfig) string {
	host := strings.TrimSpace(cfg.Host)
	if host == "" || strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 8086
	}
	scheme := "http"
	if cfg.SSL {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, strconv.Itoa(port)))
}

func influxConnectionError(operation, endpoint string, err error) error {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf(
			"InfluxDB %s失败：无法连接 %s（连接被拒绝）。请确认 InfluxDB 已启动并正在监听该地址和端口；若使用 Docker，请确认已发布端口 8086:8086: %w",
			operation,
			endpoint,
			err,
		)
	}
	return fmt.Errorf("InfluxDB %s失败: %w", operation, err)
}

func buildInfluxSession(cfg *DbConnectionConfig) (*dbSession, error) {
	baseURL := influxBaseURL(cfg)
	token := derefOr(cfg.Token, "")

	client := &http.Client{Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 健康检查
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	if header := influxAuthHeader(token); header != "" {
		req.Header.Set("Authorization", header)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, influxConnectionError("连接", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("InfluxDB health check 失败: %d", resp.StatusCode)
	}

	org, err := resolveInfluxOrg(ctx, client, baseURL, token, derefOr(cfg.Org, ""))
	if err != nil {
		return nil, err
	}

	bucket := ""
	if d := trimmedOptional(cfg.Database); d != nil {
		bucket = *d
	}
	return &dbSession{
		kind:            kindHTTP,
		httpKind:        httpInflux,
		httpClient:      client,
		httpBaseURL:     baseURL,
		httpToken:       token,
		httpOrg:         org,
		defaultDatabase: bucket,
	}, nil
}

// influxSchema 列出 InfluxDB 的 buckets（每个 bucket 作为一个数据库节点）。
func influxSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	u := s.httpBaseURL + "/api/v2/buckets"
	if org := strings.TrimSpace(s.httpOrg); org != "" {
		u += "?org=" + url.QueryEscape(org)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if header := influxAuthHeader(s.httpToken); header != "" {
		req.Header.Set("Authorization", header)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("InfluxDB bucket 列表读取失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("InfluxDB bucket 响应读取失败: %w", err)
	}

	names := map[string]struct{}{}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsed struct {
			Buckets []struct {
				Name string `json:"name"`
			} `json:"buckets"`
		}
		if err := json.Unmarshal(body, &parsed); err == nil {
			for _, b := range parsed.Buckets {
				if n := strings.TrimSpace(b.Name); n != "" {
					names[n] = struct{}{}
				}
			}
		}
	}
	if b := strings.TrimSpace(s.defaultDatabase); b != "" {
		names[b] = struct{}{}
	}
	if len(names) == 0 && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		return nil, fmt.Errorf("InfluxDB bucket 列表读取失败(%d): %s",
			resp.StatusCode, influxErrorMessage(body))
	}

	buckets := make([]string, 0, len(names))
	for name := range names {
		buckets = append(buckets, name)
	}
	sort.Strings(buckets)
	return &DbSchemaInfo{Databases: influxBucketSchemas(ctx, s, buckets)}, nil
}

// Limit concurrent Flux queries to avoid serial network latency without flooding the server.
func influxBucketSchemas(ctx context.Context, s *dbSession, buckets []string) []SchemaDatabase {
	databases := make([]SchemaDatabase, len(buckets))
	retryable := make([]bool, len(buckets))
	const maxConcurrentBuckets = 4
	workers := min(len(buckets), maxConcurrentBuckets)
	var wg sync.WaitGroup
	jobs := make(chan int)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				tables, retry := influxMeasurements(ctx, s, buckets[i])
				retryable[i] = retry
				if len(tables) == 0 {
					// Preserve the bucket placeholder on empty/failed measurement requests.
					tables = []SchemaTable{{Name: buckets[i], Kind: "bucket"}}
				}
				databases[i] = SchemaDatabase{Name: buckets[i], Tables: tables}
			}
		}()
	}
	for i := range buckets {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	// Some servers throttle simultaneous Flux queries. Retry transient failures
	// serially after the concurrent batch has drained before showing placeholders.
	for i, retry := range retryable {
		if !retry || ctx.Err() != nil {
			continue
		}
		if tables, _ := influxMeasurements(ctx, s, buckets[i]); len(tables) > 0 {
			databases[i].Tables = tables
		}
	}
	return databases
}

// influxMeasurements 用 Flux schema.measurements 列出某 bucket 下的 measurement，作为表。
// 失败时返回 nil（由调用方退回 bucket 占位），不阻断整体 schema。
// 临时限流或服务端错误标记为可重试；权限错误不重复请求。
func influxMeasurements(ctx context.Context, s *dbSession, bucket string) ([]SchemaTable, bool) {
	org := strings.TrimSpace(s.httpOrg)
	if org == "" {
		return nil, false
	}
	flux := fmt.Sprintf(
		`import "influxdata/influxdb/schema"`+"\n"+`schema.measurements(bucket: %q)`, bucket)
	u := s.httpBaseURL + "/api/v2/query?org=" + url.QueryEscape(org)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(flux))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/vnd.flux")
	req.Header.Set("Accept", "application/csv")
	if header := influxAuthHeader(s.httpToken); header != "" {
		req.Header.Set("Authorization", header)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil
	}
	defer resp.Body.Close()
	body, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	}
	return parseInfluxMeasurementCSV(string(body)), false
}

// influxExecuteQuery 执行 Flux 查询（对齐 Tauri execute.rs InfluxDB 分支）。
func influxExecuteQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	start := time.Now()
	org := strings.TrimSpace(s.httpOrg)
	if org == "" {
		return nil, fmt.Errorf("InfluxDB 查询失败: 缺少 Org，请在连接配置里填写 Org")
	}
	if strings.TrimSpace(s.httpToken) == "" {
		return nil, fmt.Errorf("InfluxDB 查询失败: 缺少 Token，请在连接配置里填写 Token")
	}
	u := s.httpBaseURL + "/api/v2/query?org=" + url.QueryEscape(org)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/vnd.flux")
	req.Header.Set("Accept", "application/csv")
	if header := influxAuthHeader(s.httpToken); header != "" {
		req.Header.Set("Authorization", header)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, influxConnectionError("查询", s.httpBaseURL, err)
	}
	defer resp.Body.Close()
	body, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("InfluxDB 查询响应读取失败: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("InfluxDB 查询失败(401): Token 无效或缺少读取权限，请检查 Token")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("InfluxDB 查询失败(%d): %s", resp.StatusCode, influxErrorMessage(body))
	}
	res := parseInfluxCSV(string(body))
	res.ExecutionMs = time.Since(start).Milliseconds()
	return res, nil
}

// parseInfluxCSV 解析 Flux 注解 CSV 为表格结果（对齐 Tauri parse_influx_csv）：
// 跳过 # 注解行；首个非注解行为表头；每行前 3 个注解列（空列/result/table）丢弃。
func parseInfluxCSV(csvText string) *QueryResult {
	var columns []string
	var rows [][]any
	for _, line := range strings.Split(csvText, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) <= 3 {
			continue
		}
		if columns == nil {
			columns = append(columns, parts[3:]...)
			continue
		}
		row := make([]any, 0, len(parts)-3)
		for _, v := range parts[3:] {
			row = append(row, v)
		}
		rows = append(rows, row)
	}
	if columns == nil {
		columns = []string{}
	}
	if rows == nil {
		rows = [][]any{}
	}
	return &QueryResult{Columns: columns, Rows: rows, RowCount: len(rows)}
}

// influxTableRows 分页浏览某 bucket 下的 measurement 数据（Flux range + filter + limit）。
func influxTableRows(ctx context.Context, s *dbSession, bucket, measurement string, limit, offset uint32) (*QueryResult, error) {
	b := strings.TrimSpace(bucket)
	if b == "" {
		b = strings.TrimSpace(s.defaultDatabase)
	}
	if b == "" {
		return nil, fmt.Errorf("InfluxDB 数据浏览需要指定 bucket（database 参数）")
	}
	flux := fmt.Sprintf(
		`from(bucket: %q) |> range(start: 0) |> filter(fn: (r) => r._measurement == %q) |> limit(n: %d, offset: %d)`,
		b, measurement, limit, offset)
	return influxExecuteQuery(ctx, s, flux)
}

// parseInfluxMeasurementCSV 解析 Flux 注解 CSV，取 _value 列作为 measurement 名。
func parseInfluxMeasurementCSV(csvText string) []SchemaTable {
	var tables []SchemaTable
	seen := map[string]struct{}{}
	valueIdx := -1
	for _, line := range strings.Split(csvText, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, ",")
		if valueIdx == -1 {
			// 表头行：定位 _value 列
			for i, c := range cols {
				if strings.TrimSpace(c) == "_value" {
					valueIdx = i
					break
				}
			}
			continue
		}
		if valueIdx >= 0 && valueIdx < len(cols) {
			name := strings.TrimSpace(cols[valueIdx])
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			tables = append(tables, SchemaTable{Name: name, Kind: "measurement"})
		}
	}
	return tables
}
