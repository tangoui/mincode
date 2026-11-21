package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Raw JSON pages preserve exact numeric lexemes and nested provider types.
// They bypass the interactive query cap but retain a bounded page size.
func backupWarehouse(ctx context.Context, a *backupArchive, s *dbSession, database, table string) error {
	quote := quoteWarehousePath
	if s.warehouse.provider == "databricks" {
		quote = quoteBacktickPath
	}
	target := quote(table)
	if database != "" {
		target = quote(database) + "." + target
	}
	if s.warehouse.provider == "bigquery" {
		target = quoteBacktickIdent(s.warehouse.project + "." + database + "." + table)
	}
	query := "SELECT * FROM " + target
	return a.object(database, table, "warehouse-native-json-pages", func(out *backupArchiveStream) error {
		switch s.warehouse.provider {
		case "bigquery":
			return backupBigQuery(ctx, out, s, query)
		case "databricks":
			return backupDatabricks(ctx, out, s, query)
		case "dremio":
			return backupDremio(ctx, out, s, query)
		case "snowflake":
			return backupSnowflake(ctx, out, s, query)
		case "kylin", "pinot":
			return backupWarehouseOffsets(ctx, out, s, target)
		}
		return fmt.Errorf("unknown warehouse provider")
	})
}
func backupRawObject(data []byte) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("empty provider response")
	}
	return value, nil
}
func rawText(v json.RawMessage) string { var s string; _ = json.Unmarshal(v, &s); return s }
func rawInt(v json.RawMessage) int     { var n int; _ = json.Unmarshal(v, &n); return n }
func backupRawPage(out *backupArchiveStream, body []byte, key string) (map[string]json.RawMessage, int, error) {
	page, err := backupRawObject(body)
	if err != nil {
		return nil, 0, err
	}
	var rows []json.RawMessage
	if err = json.Unmarshal(page[key], &rows); err != nil {
		return nil, 0, fmt.Errorf("missing native rows: %w", err)
	}
	if err = out.record(json.RawMessage(body)); err != nil {
		return nil, 0, err
	}
	return page, len(rows), nil
}
func backupBigQuery(ctx context.Context, out *backupArchiveStream, s *dbSession, query string) error {
	w := s.warehouse
	auth := "Bearer " + s.httpToken
	payload := map[string]any{"query": query, "useLegacySql": false, "maxResults": 256, "timeoutMs": 10000}
	if w.location != "" {
		payload["location"] = w.location
	}
	body, _, err := warehouseJSONRequest(ctx, s, "POST", "/bigquery/v2/projects/"+url.PathEscape(w.project)+"/queries", payload, auth)
	if err != nil {
		return err
	}
	job, project, location := "", w.project, w.location
	token := ""
	seen := map[string]bool{}
	for {
		page, err := backupRawObject(body)
		if err != nil {
			return err
		}
		if v := page["errors"]; len(v) > 0 && string(v) != "[]" && string(v) != "null" {
			return fmt.Errorf("BigQuery returned query errors")
		}
		var ref struct {
			Job      string `json:"jobId"`
			Project  string `json:"projectId"`
			Location string `json:"location"`
		}
		_ = json.Unmarshal(page["jobReference"], &ref)
		if ref.Job != "" {
			job = ref.Job
		}
		if ref.Project != "" {
			project = ref.Project
		}
		if ref.Location != "" {
			location = ref.Location
		}
		if string(page["jobComplete"]) != "true" {
			if job == "" {
				return fmt.Errorf("BigQuery missing job ID")
			}
			if err = waitWarehousePoll(ctx); err != nil {
				return err
			}
		} else {
			if err = out.record(json.RawMessage(body)); err != nil {
				return err
			}
			token = rawText(page["pageToken"])
			if token == "" {
				return nil
			}
			if seen[token] {
				return fmt.Errorf("BigQuery page token repeated")
			}
			seen[token] = true
		}
		q := url.Values{"maxResults": {"256"}}
		if location != "" {
			q.Set("location", location)
		}
		if token != "" {
			q.Set("pageToken", token)
		}
		body, _, err = warehouseJSONRequest(ctx, s, "GET", "/bigquery/v2/projects/"+url.PathEscape(project)+"/queries/"+url.PathEscape(job)+"?"+q.Encode(), nil, auth)
		if err != nil {
			return err
		}
	}
}
func backupDatabricks(ctx context.Context, out *backupArchiveStream, s *dbSession, query string) error {
	w := s.warehouse
	payload := map[string]any{"statement": query, "warehouse_id": w.project, "wait_timeout": "10s", "disposition": "INLINE", "format": "JSON_ARRAY"}
	if w.catalog != "" {
		payload["catalog"] = w.catalog
	}
	if w.schema != "" {
		payload["schema"] = w.schema
	}
	auth := "Bearer " + s.httpToken
	body, _, err := warehouseJSONRequest(ctx, s, "POST", "/api/2.0/sql/statements", payload, auth)
	if err != nil {
		return err
	}
	id := ""
	seen := map[string]bool{}
	for {
		page, err := backupRawObject(body)
		if err != nil {
			return err
		}
		if v := rawText(page["statement_id"]); v != "" {
			id = v
		}
		status, _ := backupRawObject(page["status"])
		state := rawText(status["state"])
		if state == "PENDING" || state == "RUNNING" {
			if id == "" {
				return fmt.Errorf("Databricks missing statement ID")
			}
			if err = waitWarehousePoll(ctx); err != nil {
				return err
			}
			body, _, err = warehouseJSONRequest(ctx, s, "GET", "/api/2.0/sql/statements/"+url.PathEscape(id), nil, auth)
			if err != nil {
				return err
			}
			continue
		}
		if state != "" && state != "SUCCEEDED" {
			return fmt.Errorf("Databricks export did not succeed")
		}
		if m, e := backupRawObject(page["manifest"]); e == nil && string(m["truncated"]) == "true" {
			return fmt.Errorf("Databricks returned a truncated result; native external export required")
		}
		result := page
		if nested, e := backupRawObject(page["result"]); e == nil {
			result = nested
		}
		if err = out.record(json.RawMessage(body)); err != nil {
			return err
		}
		next := rawText(result["next_chunk_internal_link"])
		if next == "" {
			return nil
		}
		if seen[next] {
			return fmt.Errorf("Databricks chunk repeated")
		}
		seen[next] = true
		body, _, err = warehouseJSONRequest(ctx, s, "GET", next, nil, auth)
		if err != nil {
			return err
		}
	}
}
func backupDremio(ctx context.Context, out *backupArchiveStream, s *dbSession, query string) error {
	auth := "Bearer " + s.httpToken
	body, _, err := warehouseJSONRequest(ctx, s, "POST", "/api/v3/sql", map[string]any{"sql": query}, auth)
	if err != nil {
		return err
	}
	page, err := backupRawObject(body)
	if err != nil {
		return err
	}
	id := rawText(page["id"])
	if id == "" {
		return fmt.Errorf("Dremio missing job ID")
	}
	for {
		body, _, err = warehouseJSONRequest(ctx, s, "GET", "/api/v3/job/"+url.PathEscape(id), nil, auth)
		if err != nil {
			return err
		}
		page, err = backupRawObject(body)
		if err != nil {
			return err
		}
		state := rawText(page["jobState"])
		if state == "COMPLETED" {
			break
		}
		if state == "FAILED" || state == "CANCELED" {
			return fmt.Errorf("Dremio export failed")
		}
		if err = waitWarehousePoll(ctx); err != nil {
			return err
		}
	}
	for offset := 0; ; {
		body, _, err = warehouseJSONRequest(ctx, s, "GET", "/api/v3/job/"+url.PathEscape(id)+"/results?limit=256&offset="+strconv.Itoa(offset), nil, auth)
		if err != nil {
			return err
		}
		page, n, err := backupRawPage(out, body, "rows")
		if err != nil {
			return err
		}
		offset += n
		total := rawInt(page["rowCount"])
		if offset >= total {
			return nil
		}
		if n == 0 {
			return fmt.Errorf("Dremio missing rows before completion")
		}
	}
}
func backupSnowflake(ctx context.Context, out *backupArchiveStream, s *dbSession, query string) error {
	w := s.warehouse
	payload := map[string]any{"statement": query, "timeout": 120}
	if w.project != "" {
		payload["database"] = w.project
	}
	if w.catalog != "" {
		payload["warehouse"] = w.catalog
	}
	if w.schema != "" {
		payload["schema"] = w.schema
	}
	headers := map[string]string{"Authorization": "Bearer " + s.httpToken, "User-Agent": "MinCode"}
	body, status, err := warehouseJSONRequestWithHeaders(ctx, s, "POST", "/api/v2/statements", payload, headers)
	if err != nil {
		return err
	}
	endpoint := ""
	for {
		page, err := backupRawObject(body)
		if err != nil {
			return err
		}
		if next := rawText(page["statementStatusUrl"]); next != "" {
			endpoint = next
		}
		if endpoint == "" && rawText(page["statementHandle"]) != "" {
			endpoint = "/api/v2/statements/" + url.PathEscape(rawText(page["statementHandle"]))
		}
		if status != http.StatusAccepted {
			break
		}
		if endpoint == "" {
			return fmt.Errorf("Snowflake missing result URL")
		}
		if err = waitWarehousePoll(ctx); err != nil {
			return err
		}
		body, status, err = warehouseJSONRequestWithHeaders(ctx, s, "GET", endpoint, nil, headers)
		if err != nil {
			return err
		}
	}
	page, err := backupRawObject(body)
	if err != nil {
		return err
	}
	if state := rawText(page["sqlState"]); state != "" && state != "00000" {
		return fmt.Errorf("Snowflake query failed")
	}
	if err = out.record(json.RawMessage(body)); err != nil {
		return err
	}
	meta, err := backupRawObject(page["resultSetMetaData"])
	if err != nil {
		return err
	}
	var partitions []json.RawMessage
	if err = json.Unmarshal(meta["partitionInfo"], &partitions); err != nil {
		return err
	}
	for i := 1; i < len(partitions); i++ {
		if endpoint == "" {
			return fmt.Errorf("Snowflake missing partition URL")
		}
		next, err := appendWarehouseQuery(endpoint, "partition", strconv.Itoa(i))
		if err != nil {
			return err
		}
		body, _, err = warehouseJSONRequestWithHeaders(ctx, s, "GET", next, nil, headers)
		if err != nil {
			return err
		}
		if err = out.record(json.RawMessage(body)); err != nil {
			return err
		}
	}
	return nil
}
func backupWarehouseOffsets(ctx context.Context, out *backupArchiveStream, s *dbSession, target string) error {
	for offset := 0; ; {
		query := fmt.Sprintf("SELECT * FROM %s LIMIT 256 OFFSET %d", target, offset)
		var body []byte
		var err error
		rowsKey := "results"
		if s.warehouse.provider == "kylin" {
			headers := map[string]string{"Accept": "application/vnd.apache.kylin-v4-public+json"}
			if s.httpToken != "" {
				headers["Authorization"] = "Bearer " + s.httpToken
			} else if s.httpUser != "" {
				req, _ := http.NewRequest("GET", "http://localhost", nil)
				req.SetBasicAuth(s.httpUser, s.httpPass)
				headers["Authorization"] = req.Header.Get("Authorization")
			}
			body, _, err = warehouseJSONRequestWithHeaders(ctx, s, "POST", "/kylin/api/query", map[string]any{"sql": query, "project": s.warehouse.project, "limit": 256, "offset": 0, "acceptPartial": false}, headers)
		} else {
			auth := ""
			if s.httpToken != "" {
				auth = "Bearer " + s.httpToken
			}
			body, _, err = warehouseJSONRequest(ctx, s, "POST", "/query/sql", map[string]any{"sql": query}, auth)
		}
		if err != nil {
			return err
		}
		page, err := backupRawObject(body)
		if err != nil {
			return err
		}
		if raw := page["data"]; len(raw) > 0 && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
			page, err = backupRawObject(raw)
			if err != nil {
				return err
			}
		}
		if s.warehouse.provider == "pinot" {
			if v := page["exceptions"]; len(v) > 0 && string(v) != "[]" && string(v) != "null" {
				return fmt.Errorf("Pinot export returned exceptions")
			}
			page, err = backupRawObject(page["resultTable"])
			if err != nil {
				return err
			}
			rowsKey = "rows"
		} else if rawText(page["exceptionMessage"]) != "" || rawText(page["exception_message"]) != "" || string(page["isException"]) == "true" {
			return fmt.Errorf("Kylin export failed")
		}
		var rows []json.RawMessage
		if err = json.Unmarshal(page[rowsKey], &rows); err != nil {
			return err
		}
		if err = out.record(json.RawMessage(body)); err != nil {
			return err
		}
		if len(rows) < 256 {
			return nil
		}
		offset += len(rows)
		if err = ctx.Err(); err != nil {
			return err
		}
	}
}
