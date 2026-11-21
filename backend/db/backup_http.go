package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mincode/app/backend/core"
)

func backupRequest(ctx context.Context, s *dbSession, method, path string, payload any) ([]byte, error) {
	var input io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.httpBaseURL+path, input)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	switch s.httpKind {
	case httpElastic:
		applyElasticAuth(req, s.httpToken, s.httpUser, s.httpPass)
	case httpMeili:
		applyMeiliAuth(req, s.httpToken)
	case httpInflux:
		req.Header.Set("Authorization", influxAuthHeader(s.httpToken))
	default:
		if s.httpUser != "" {
			req.SetBasicAuth(s.httpUser, s.httpPass)
		}
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("backup HTTP transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("backup HTTP status %d", resp.StatusCode)
	}
	return core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
}
func backupHTTPObject(ctx context.Context, a *backupArchive, s *dbSession, database, table string) error {
	switch s.httpKind {
	case httpElastic:
		return backupElastic(ctx, a, s, database, table)
	case httpMeili:
		return backupMeili(ctx, a, s, database, table)
	case httpCouch:
		return backupCouch(ctx, a, s, database, table)
	case httpInflux:
		return backupInflux(ctx, a, s, database, table)
	default:
		return backupWarehouse(ctx, a, s, database, table)
	}
}
func backupElastic(ctx context.Context, a *backupArchive, s *dbSession, database, index string) error {
	path := "/" + url.PathEscape(index)
	meta, err := backupRequest(ctx, s, "GET", path, nil)
	if err != nil {
		return err
	}
	if err = a.metadata(database, index, json.RawMessage(meta)); err != nil {
		return err
	}
	scroll := ""
	defer func() {
		if scroll != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = backupRequest(cleanup, s, "DELETE", "/_search/scroll", map[string]any{"scroll_id": []string{scroll}})
		}
	}()
	body, err := backupRequest(ctx, s, "POST", path+"/_search?scroll=2m", map[string]any{"size": 256, "sort": []string{"_doc"}, "query": map[string]any{"match_all": map[string]any{}}})
	if err != nil {
		return err
	}
	return a.object(database, index, "elasticsearch-hit", func(out *backupArchiveStream) error {
		for {
			var page struct {
				Scroll   string `json:"_scroll_id"`
				TimedOut bool   `json:"timed_out"`
				Shards   struct {
					Failed int `json:"failed"`
				} `json:"_shards"`
				Hits struct {
					Hits []json.RawMessage `json:"hits"`
				} `json:"hits"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return err
			}
			if page.TimedOut || page.Shards.Failed > 0 {
				return fmt.Errorf("search export returned partial shards or timed out")
			}
			if page.Scroll != "" {
				scroll = page.Scroll
			}
			if len(page.Hits.Hits) == 0 {
				return nil
			}
			for _, record := range page.Hits.Hits {
				if err := out.record(record); err != nil {
					return err
				}
			}
			if scroll == "" {
				return fmt.Errorf("search export missing scroll context")
			}
			body, err = backupRequest(ctx, s, "POST", "/_search/scroll", map[string]any{"scroll": "2m", "scroll_id": scroll})
			if err != nil {
				return err
			}
		}
	})
}
func backupMeili(ctx context.Context, a *backupArchive, s *dbSession, database, index string) error {
	path := "/indexes/" + url.PathEscape(index)
	settings, err := backupRequest(ctx, s, "GET", path+"/settings", nil)
	if err != nil {
		return err
	}
	def, err := backupRequest(ctx, s, "GET", path, nil)
	if err != nil {
		return err
	}
	if err = a.metadata(database, index, map[string]json.RawMessage{"definition": def, "settings": settings}); err != nil {
		return err
	}
	return a.object(database, index, "json-document", func(out *backupArchiveStream) error {
		offset := 0
		for {
			body, err := backupRequest(ctx, s, "GET", path+"/documents?limit=256&offset="+strconv.Itoa(offset), nil)
			if err != nil {
				return err
			}
			var page struct {
				Results []json.RawMessage `json:"results"`
				Total   int               `json:"total"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				return err
			}
			for _, r := range page.Results {
				if err := out.record(r); err != nil {
					return err
				}
			}
			offset += len(page.Results)
			if offset >= page.Total {
				return nil
			}
			if len(page.Results) == 0 {
				return fmt.Errorf("documents pagination ended before total")
			}
		}
	})
}
func backupCouch(ctx context.Context, a *backupArchive, s *dbSession, database, table string) error {
	// CouchDB's schema models each database as a table below the server scope.
	name := table
	if name == "" {
		name = database
	}
	path := "/" + url.PathEscape(name)
	security, err := backupRequest(ctx, s, "GET", path+"/_security", nil)
	if err != nil {
		return err
	}
	if err = a.metadata(database, name, map[string]json.RawMessage{"security": security}); err != nil {
		return err
	}
	return a.object(database, name, "couchdb-document", func(out *backupArchiveStream) error {
		last := ""
		first := true
		for {
			values := url.Values{"include_docs": {"true"}, "attachments": {"true"}, "limit": {"256"}}
			if !first {
				encoded, _ := json.Marshal(last)
				values.Set("startkey", string(encoded))
				values.Set("skip", "1")
			}
			body, err := backupRequest(ctx, s, "GET", path+"/_all_docs?"+values.Encode(), nil)
			if err != nil {
				return err
			}
			var page struct {
				Rows []struct {
					ID    string          `json:"id"`
					Doc   json.RawMessage `json:"doc"`
					Error string          `json:"error"`
				} `json:"rows"`
			}
			if err = json.Unmarshal(body, &page); err != nil {
				return err
			}
			if len(page.Rows) == 0 {
				return nil
			}
			for _, r := range page.Rows {
				if r.Error != "" || len(r.Doc) == 0 {
					return fmt.Errorf("document disappeared during export")
				}
				if err := out.record(r.Doc); err != nil {
					return err
				}
			}
			next := page.Rows[len(page.Rows)-1].ID
			if !first && next == last {
				return fmt.Errorf("CouchDB cursor did not advance")
			}
			last = next
			first = false
		}
	})
}
func backupInflux(ctx context.Context, a *backupArchive, s *dbSession, database, measurement string) error {
	// The raw annotated CSV preserves typed values and quoted commas/newlines.
	quote := func(v string) string { b, _ := json.Marshal(v); return string(b) }
	query := "from(bucket: " + quote(database) + ") |> range(start: 1677-09-22T00:00:00Z, stop: " + time.Now().UTC().Format(time.RFC3339Nano) + ") |> filter(fn: (r) => r._measurement == " + quote(measurement) + ")"
	data, _ := json.Marshal(map[string]any{"query": query, "type": "flux", "dialect": map[string]any{"annotations": []string{"datatype", "group", "default"}}})
	req, err := http.NewRequestWithContext(ctx, "POST", s.httpBaseURL+"/api/v2/query?org="+url.QueryEscape(s.httpOrg), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", influxAuthHeader(s.httpToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/csv")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Influx export transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Influx export HTTP %d", resp.StatusCode)
	}
	// CSV is chunked as bytes: records may be arbitrarily large and are never truncated.
	return a.object(database, measurement, "annotated-csv-base64-chunks", func(out *backupArchiveStream) error {
		buf := make([]byte, 128*1024)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if e := out.record(buf[:n]); e != nil {
					return e
				}
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
		}
	})
}

func backupVectorObject(ctx context.Context, a *backupArchive, s *dbSession, database, table string) error {
	v := s.vector
	definition, err := vectorCollectionDefinition(ctx, s, database, table)
	if err != nil {
		return err
	}
	if err = a.metadata(database, table, json.RawMessage(definition)); err != nil {
		return err
	}
	switch v.provider {
	case "qdrant":
		return a.object(database, table, "qdrant-point", func(out *backupArchiveStream) error {
			var offset json.RawMessage
			seen := map[string]bool{}
			for {
				p := map[string]any{"limit": 256, "with_payload": true, "with_vector": true}
				if len(offset) > 0 {
					p["offset"] = offset
				}
				body, err := vectorRequest(ctx, v, "POST", v.baseURL+"/collections/"+url.PathEscape(table)+"/points/scroll", p)
				if err != nil {
					return err
				}
				var page struct {
					Result struct {
						Points []json.RawMessage `json:"points"`
						Next   json.RawMessage   `json:"next_page_offset"`
					} `json:"result"`
				}
				if err = json.Unmarshal(body, &page); err != nil {
					return err
				}
				for _, r := range page.Result.Points {
					if err = out.record(r); err != nil {
						return err
					}
				}
				offset = page.Result.Next
				if len(offset) == 0 || string(offset) == "null" {
					return nil
				}
				key := string(offset)
				if seen[key] {
					return fmt.Errorf("Qdrant cursor repeated")
				}
				seen[key] = true
			}
		})
	case "weaviate":
		var config struct {
			Multi struct {
				Enabled bool `json:"enabled"`
			} `json:"multiTenancyConfig"`
		}
		if err = json.Unmarshal([]byte(definition), &config); err != nil {
			return err
		}
		tenants := []string{""}
		if config.Multi.Enabled {
			body, err := vectorRequest(ctx, v, "GET", v.baseURL+"/v1/schema/"+url.PathEscape(table)+"/tenants", nil)
			if err != nil {
				return err
			}
			var list []struct {
				Name string `json:"name"`
			}
			if err = json.Unmarshal(body, &list); err != nil {
				return err
			}
			tenants = nil
			for _, t := range list {
				tenants = append(tenants, t.Name)
			}
		}
		return a.object(database, table, "weaviate-object", func(out *backupArchiveStream) error {
			for _, tenant := range tenants {
				after := ""
				for {
					values := url.Values{"class": {table}, "limit": {"256"}, "include": {"vector"}}
					if tenant != "" {
						values.Set("tenant", tenant)
					}
					if after != "" {
						values.Set("after", after)
					}
					body, err := vectorRequest(ctx, v, "GET", v.baseURL+"/v1/objects?"+values.Encode(), nil)
					if err != nil {
						return err
					}
					var page struct {
						Objects []json.RawMessage `json:"objects"`
					}
					if err = json.Unmarshal(body, &page); err != nil {
						return err
					}
					if len(page.Objects) == 0 {
						break
					}
					for _, r := range page.Objects {
						if err = out.record(map[string]any{"tenant": tenant, "object": r}); err != nil {
							return err
						}
					}
					var last struct {
						ID string `json:"id"`
					}
					if err = json.Unmarshal(page.Objects[len(page.Objects)-1], &last); err != nil {
						return err
					}
					if last.ID == "" || last.ID == after {
						return fmt.Errorf("Weaviate cursor failed to advance")
					}
					after = last.ID
				}
			}
			return nil
		})
	case "chroma":
		ref := v.collection(table)
		if ref.ID == "" {
			return fmt.Errorf("Chroma collection ID unavailable")
		}
		path := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s", url.PathEscape(v.tenant), url.PathEscape(v.database), url.PathEscape(ref.ID))
		return a.object(database, table, "chroma-record-batch", func(out *backupArchiveStream) error {
			for offset := 0; ; {
				body, err := vectorRequest(ctx, v, "POST", v.baseURL+path+"/get", map[string]any{"limit": 256, "offset": offset, "include": []string{"embeddings", "documents", "metadatas", "uris"}})
				if err != nil {
					return err
				}
				var page struct {
					IDs []string `json:"ids"`
				}
				if err = json.Unmarshal(body, &page); err != nil {
					return err
				}
				if len(page.IDs) == 0 {
					return nil
				}
				if err = out.record(json.RawMessage(body)); err != nil {
					return err
				}
				offset += len(page.IDs)
			}
		})
	case "pinecone":
		return backupPinecone(ctx, a, s, database, table)
	case "milvus":
		return backupMilvus(ctx, a, s, database, table, definition)
	}
	return fmt.Errorf("vector export provider unavailable")
}
func backupPinecone(ctx context.Context, a *backupArchive, s *dbSession, database, table string) error {
	v := s.vector
	ref := v.collection(table)
	if ref.Host == "" {
		return fmt.Errorf("Pinecone data-plane host unavailable")
	}
	base := strings.TrimRight(ref.Host, "/")
	body, err := vectorRequest(ctx, v, "POST", base+"/describe_index_stats", map[string]any{})
	if err != nil {
		return err
	}
	var stats struct {
		Namespaces map[string]json.RawMessage `json:"namespaces"`
	}
	if err = json.Unmarshal(body, &stats); err != nil {
		return err
	}
	return a.object(database, table, "pinecone-vector", func(out *backupArchiveStream) error {
		for ns := range stats.Namespaces {
			token := ""
			seen := map[string]bool{}
			for {
				q := url.Values{"limit": {"256"}, "namespace": {ns}}
				if token != "" {
					q.Set("paginationToken", token)
				}
				body, err := vectorRequest(ctx, v, "GET", base+"/vectors/list?"+q.Encode(), nil)
				if err != nil {
					return err
				}
				var page struct {
					Vectors []struct {
						ID string `json:"id"`
					} `json:"vectors"`
					Pagination struct {
						Next string `json:"next"`
					} `json:"pagination"`
				}
				if err = json.Unmarshal(body, &page); err != nil {
					return err
				}
				q = url.Values{"namespace": {ns}}
				for _, r := range page.Vectors {
					q.Add("ids", r.ID)
				}
				if len(page.Vectors) > 0 {
					fetched, err := vectorRequest(ctx, v, "GET", base+"/vectors/fetch?"+q.Encode(), nil)
					if err != nil {
						return err
					}
					var rows struct {
						Vectors map[string]json.RawMessage `json:"vectors"`
					}
					if err = json.Unmarshal(fetched, &rows); err != nil {
						return err
					}
					for _, r := range page.Vectors {
						value, ok := rows.Vectors[r.ID]
						if !ok {
							return fmt.Errorf("Pinecone vector disappeared")
						}
						if err = out.record(map[string]any{"namespace": ns, "vector": value}); err != nil {
							return err
						}
					}
				}
				token = page.Pagination.Next
				if token == "" {
					break
				}
				if seen[token] {
					return fmt.Errorf("Pinecone pagination token repeated")
				}
				seen[token] = true
			}
		}
		return nil
	})
}
