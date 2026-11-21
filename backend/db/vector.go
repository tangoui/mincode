// 向量数据库统一连接、集合浏览与相似度查询。
//
// Qdrant、Milvus、Weaviate、Pinecone、Chroma 只使用 REST/HTTP，不引入各家 SDK；
// pgvector 复用现有 pgx database/sql 驱动。前端统一使用 JSON 查询协议，后端负责将其
// 转换成各供应商 API，避免在 Vue 层散落鉴权、网络访问和协议差异。
package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

const (
	vectorQueryMaxLimit = 1000
	pineconeAPIVersion  = "2026-04"
)

type vectorCollectionRef struct {
	ID        string
	Host      string
	Dimension int
	Metric    string
}

type vectorSession struct {
	provider string
	client   *http.Client
	baseURL  string
	token    string
	user     string
	pass     string
	database string
	tenant   string

	mu            sync.RWMutex
	collections   map[string]vectorCollectionRef
	initialSchema *DbSchemaInfo
}

type pgvectorColumnRef struct {
	Name       string
	TypeSchema string
	TypeName   string
}

type vectorQueryRequest struct {
	Collection    string          `json:"collection"`
	Vector        []float64       `json:"vector"`
	Limit         int             `json:"limit"`
	TopK          int             `json:"topK"`
	Filter        json.RawMessage `json:"filter"`
	Namespace     string          `json:"namespace"`
	IncludeVector bool            `json:"includeVector"`
	VectorColumn  string          `json:"vectorColumn"`
	OutputFields  []string        `json:"outputFields"`
	Metric        string          `json:"metric"`
	TargetVector  string          `json:"targetVector"`
}

type vectorCreateCollectionRequest struct {
	Name       string
	Dimension  int
	Metric     string
	Cloud      string
	Region     string
	VectorName string
	Options    map[string]any
}

func buildPgvectorSession(cfg *DbConnectionConfig) (*dbSession, error) {
	s, err := buildPostgresSession(cfg)
	if err != nil {
		return nil, err
	}
	var version string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.db.QueryRowContext(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&version); err != nil {
		s.close()
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("pgvector 扩展尚未安装，请先在目标数据库执行 CREATE EXTENSION vector")
		}
		return nil, fmt.Errorf("检测 pgvector 扩展失败: %w", err)
	}
	s.vector = &vectorSession{
		provider:    "pgvector",
		database:    s.defaultDatabase,
		collections: map[string]vectorCollectionRef{},
	}
	return s, nil
}

func buildVectorHTTPSession(cfg *DbConnectionConfig, provider string) (*dbSession, error) {
	baseURL, err := vectorBaseURL(cfg, provider)
	if err != nil {
		return nil, err
	}
	vs := &vectorSession{
		provider:    provider,
		client:      &http.Client{Timeout: 30 * time.Second},
		baseURL:     baseURL,
		token:       strings.TrimSpace(derefOr(cfg.Token, "")),
		user:        strings.TrimSpace(derefOr(cfg.Username, "")),
		pass:        derefOr(cfg.Password, ""),
		database:    strings.TrimSpace(derefOr(cfg.Database, "")),
		tenant:      strings.TrimSpace(derefOr(cfg.Org, "")),
		collections: map[string]vectorCollectionRef{},
	}
	if provider == "milvus" && vs.database == "" {
		vs.database = "default"
	}
	if provider == "chroma" {
		if vs.database == "" {
			vs.database = "default_database"
		}
		if vs.tenant == "" {
			vs.tenant = "default_tenant"
		}
	}

	s := &dbSession{
		kind:            kindVector,
		vector:          vs,
		defaultDatabase: vs.database,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	initialSchema, err := vectorSchema(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("%s 连接失败: %w", provider, err)
	}
	vs.cacheInitialSchema(initialSchema)
	return s, nil
}

func vectorBaseURL(cfg *DbConnectionConfig, provider string) (string, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	if strings.Contains(host, "://") {
		parsed, err := url.Parse(host)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return "", fmt.Errorf("向量数据库地址必须是有效的 HTTP 或 HTTPS 地址")
		}
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	port := cfg.Port
	if port == 0 {
		switch provider {
		case "qdrant":
			port = 6333
		case "milvus":
			port = 19530
		case "weaviate":
			port = 8080
		case "pinecone":
			port = 443
		case "chroma":
			port = 8000
		}
	}
	scheme := "http"
	if cfg.SSL || provider == "pinecone" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port), nil
}

func (v *vectorSession) cacheInitialSchema(schema *DbSchemaInfo) {
	v.mu.Lock()
	v.initialSchema = schema
	v.mu.Unlock()
}

func (v *vectorSession) takeInitialSchema() *DbSchemaInfo {
	v.mu.Lock()
	schema := v.initialSchema
	v.initialSchema = nil
	v.mu.Unlock()
	return schema
}

func (v *vectorSession) rememberCollection(name string, ref vectorCollectionRef) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	v.mu.Lock()
	v.collections[name] = ref
	v.mu.Unlock()
}

func (v *vectorSession) collection(name string) vectorCollectionRef {
	v.mu.RLock()
	ref := v.collections[name]
	v.mu.RUnlock()
	return ref
}

func (v *vectorSession) forgetCollection(name string) {
	v.mu.Lock()
	delete(v.collections, strings.TrimSpace(name))
	v.mu.Unlock()
}

func vectorApplyAuth(req *http.Request, v *vectorSession) {
	switch v.provider {
	case "qdrant":
		if v.token != "" {
			req.Header.Set("api-key", v.token)
		}
	case "pinecone":
		if v.token != "" {
			req.Header.Set("Api-Key", v.token)
		}
		req.Header.Set("X-Pinecone-API-Version", pineconeAPIVersion)
	case "chroma":
		if v.token != "" {
			req.Header.Set("Authorization", "Bearer "+v.token)
			req.Header.Set("X-Chroma-Token", v.token)
		} else if v.user != "" {
			req.SetBasicAuth(v.user, v.pass)
		}
	default:
		if v.token != "" {
			req.Header.Set("Authorization", "Bearer "+v.token)
		} else if v.provider == "milvus" && v.user != "" {
			req.Header.Set("Authorization", "Bearer "+v.user+":"+v.pass)
		} else if v.user != "" {
			req.SetBasicAuth(v.user, v.pass)
		}
	}
}

func vectorRequest(ctx context.Context, v *vectorSession, method, requestURL string, payload any) ([]byte, error) {
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return nil, fmt.Errorf("编码请求失败: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, &body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	vectorApplyAuth(req, v)
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := core.ReadAllLimited(resp.Body, dbHTTPMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 1200 {
			message = message[:1200] + "…"
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, message)
	}
	return responseBody, nil
}

func vectorSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	if s.vector == nil {
		return nil, fmt.Errorf("当前会话不是向量数据库")
	}
	switch s.vector.provider {
	case "pgvector":
		return pgvectorSchema(ctx, s)
	case "qdrant":
		return qdrantSchema(ctx, s)
	case "milvus":
		return milvusSchema(ctx, s)
	case "weaviate":
		return weaviateSchema(ctx, s)
	case "pinecone":
		return pineconeSchema(ctx, s)
	case "chroma":
		return chromaSchema(ctx, s)
	default:
		return nil, fmt.Errorf("不支持的向量数据库类型: %s", s.vector.provider)
	}
}

func pgvectorSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.nspname, c.relname, COALESCE(st.n_live_tup, 0)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		JOIN pg_type t ON t.oid = a.atttypid
		LEFT JOIN pg_stat_user_tables st ON st.relid = c.oid
		WHERE c.relkind IN ('r', 'p')
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND t.typname IN ('vector', 'halfvec', 'sparsevec')
		GROUP BY n.nspname, c.relname, st.n_live_tup
		ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, fmt.Errorf("读取 pgvector 表失败: %w", err)
	}
	defer rows.Close()
	grouped := map[string][]SchemaTable{}
	var schemas []string
	for rows.Next() {
		var schema, table string
		var count int64
		if err := rows.Scan(&schema, &table, &count); err != nil {
			return nil, err
		}
		if _, exists := grouped[schema]; !exists {
			schemas = append(schemas, schema)
		}
		rowCount := count
		grouped[schema] = append(grouped[schema], SchemaTable{
			Name: table, Kind: "vector_table", RowCount: &rowCount,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	databases := make([]SchemaDatabase, 0, len(schemas))
	for _, schema := range schemas {
		databases = append(databases, SchemaDatabase{Name: schema, Tables: grouped[schema]})
	}
	catalog := s.defaultDatabase
	return &DbSchemaInfo{Databases: databases, Catalog: trimmedOptional(&catalog)}, nil
}

func qdrantSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	body, err := vectorRequest(ctx, s.vector, http.MethodGet, s.vector.baseURL+"/collections", nil)
	if err != nil {
		return nil, fmt.Errorf("读取 Collection 列表失败: %w", err)
	}
	var response struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("解析 Collection 列表失败: %w", err)
	}
	tables := make([]SchemaTable, 0, len(response.Result.Collections))
	for _, item := range response.Result.Collections {
		if item.Name != "" {
			tables = append(tables, SchemaTable{Name: item.Name, Kind: "collection"})
		}
	}
	return singleVectorDatabase("qdrant", tables), nil
}

func validateMilvusResponse(body []byte) error {
	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("解析 Milvus 响应失败: %w", err)
	}
	if response.Code == 0 {
		return nil
	}
	message := strings.TrimSpace(response.Message)
	if message == "" {
		message = "未知错误"
	}
	return fmt.Errorf("Milvus API 错误 (%d): %s", response.Code, message)
}

func milvusSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	payload := map[string]any{}
	if s.vector.database != "" {
		payload["dbName"] = s.vector.database
	}
	body, err := vectorRequest(ctx, s.vector, http.MethodPost,
		s.vector.baseURL+"/v2/vectordb/collections/list", payload)
	if err != nil {
		return nil, fmt.Errorf("读取 Collection 列表失败: %w", err)
	}
	if err := validateMilvusResponse(body); err != nil {
		return nil, err
	}
	var response struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("解析 Collection 列表失败: %w", err)
	}
	tables := make([]SchemaTable, 0, len(response.Data))
	for _, name := range response.Data {
		if strings.TrimSpace(name) != "" {
			tables = append(tables, SchemaTable{Name: name, Kind: "collection"})
		}
	}
	return singleVectorDatabase(s.vector.database, tables), nil
}

func weaviateSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	body, err := vectorRequest(ctx, s.vector, http.MethodGet, s.vector.baseURL+"/v1/schema", nil)
	if err != nil {
		return nil, fmt.Errorf("读取 Collection 列表失败: %w", err)
	}
	var response struct {
		Classes []struct {
			Class string `json:"class"`
		} `json:"classes"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("解析 Collection 列表失败: %w", err)
	}
	tables := make([]SchemaTable, 0, len(response.Classes))
	for _, item := range response.Classes {
		if item.Class != "" {
			tables = append(tables, SchemaTable{Name: item.Class, Kind: "collection"})
		}
	}
	return singleVectorDatabase("weaviate", tables), nil
}

func pineconeSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	// 用户也可以直接填写某个 index 的 data-plane host，并在“数据库”中填写 index 名。
	if s.vector.database != "" && !strings.Contains(strings.ToLower(s.vector.baseURL), "api.pinecone.io") {
		s.vector.rememberCollection(s.vector.database, vectorCollectionRef{Host: s.vector.baseURL})
		return singleVectorDatabase("pinecone", []SchemaTable{{Name: s.vector.database, Kind: "index"}}), nil
	}
	body, err := vectorRequest(ctx, s.vector, http.MethodGet, s.vector.baseURL+"/indexes", nil)
	if err != nil {
		return nil, fmt.Errorf("读取 Index 列表失败: %w", err)
	}
	var response struct {
		Indexes []struct {
			Name      string `json:"name"`
			Dimension int    `json:"dimension"`
			Metric    string `json:"metric"`
			Host      string `json:"host"`
			Status    struct {
				Ready bool `json:"ready"`
			} `json:"status"`
		} `json:"indexes"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("解析 Index 列表失败: %w", err)
	}
	tables := make([]SchemaTable, 0, len(response.Indexes))
	for _, item := range response.Indexes {
		if item.Name == "" {
			continue
		}
		host := strings.TrimSpace(item.Host)
		if host != "" && !strings.Contains(host, "://") {
			host = "https://" + host
		}
		s.vector.rememberCollection(item.Name, vectorCollectionRef{
			Host: host, Dimension: item.Dimension, Metric: item.Metric,
		})
		comment := fmt.Sprintf("%d dimensions · %s", item.Dimension, item.Metric)
		if !item.Status.Ready {
			comment += " · not ready"
		}
		tables = append(tables, SchemaTable{Name: item.Name, Kind: "index", Comment: &comment})
	}
	return singleVectorDatabase("pinecone", tables), nil
}

func chromaSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	path := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections",
		url.PathEscape(s.vector.tenant), url.PathEscape(s.vector.database))
	body, err := vectorRequest(ctx, s.vector, http.MethodGet, s.vector.baseURL+path, nil)
	if err != nil {
		// 兼容仍使用 v1 API 的 Chroma 服务。
		body, err = vectorRequest(ctx, s.vector, http.MethodGet, s.vector.baseURL+"/api/v1/collections", nil)
		if err != nil {
			return nil, fmt.Errorf("读取 Collection 列表失败: %w", err)
		}
	}
	var items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("解析 Collection 列表失败: %w", err)
	}
	tables := make([]SchemaTable, 0, len(items))
	for _, item := range items {
		if item.Name == "" {
			continue
		}
		s.vector.rememberCollection(item.Name, vectorCollectionRef{ID: item.ID})
		tables = append(tables, SchemaTable{Name: item.Name, Kind: "collection"})
	}
	return singleVectorDatabase(s.vector.database, tables), nil
}

func singleVectorDatabase(name string, tables []SchemaTable) *DbSchemaInfo {
	if strings.TrimSpace(name) == "" {
		name = "default"
	}
	if tables == nil {
		tables = []SchemaTable{}
	}
	sort.SliceStable(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	return &DbSchemaInfo{Databases: []SchemaDatabase{{Name: name, Tables: tables}}}
}

func vectorTableRows(ctx context.Context, s *dbSession, database, table string, limit, offset uint32) (*QueryResult, error) {
	return vectorTableRowsPage(ctx, s, database, table, limit, offset, "", "")
}

func vectorTableRowsPage(ctx context.Context, s *dbSession, database, table string, limit, offset uint32, pageToken, namespace string) (*QueryResult, error) {
	start := time.Now()
	switch s.vector.provider {
	case "qdrant":
		return qdrantTableRows(ctx, s, table, limit, pageToken, start)
	case "milvus":
		return milvusTableRows(ctx, s, table, limit, offset, start)
	case "weaviate":
		return weaviateTableRows(ctx, s, table, limit, offset, start)
	case "pinecone":
		return pineconeTableRows(ctx, s, table, limit, pageToken, namespace, start)
	case "chroma":
		return chromaTableRows(ctx, s, table, limit, offset, start)
	default:
		return nil, fmt.Errorf("%s 不支持数据预览", s.vector.provider)
	}
}

func qdrantTableRows(ctx context.Context, s *dbSession, table string, limit uint32, pageToken string, start time.Time) (*QueryResult, error) {
	payload := map[string]any{"limit": limit, "with_payload": true, "with_vector": true}
	if strings.TrimSpace(pageToken) != "" {
		var cursor any
		if err := json.Unmarshal([]byte(pageToken), &cursor); err != nil {
			return nil, fmt.Errorf("Qdrant 分页游标无效: %w", err)
		}
		payload["offset"] = cursor
	}
	body, err := vectorRequest(ctx, s.vector, http.MethodPost,
		s.vector.baseURL+"/collections/"+url.PathEscape(table)+"/points/scroll", payload)
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			Points         []map[string]any `json:"points"`
			NextPageOffset any              `json:"next_page_offset"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	result := recordsResult(response.Result.Points, start)
	result.NextPageToken = encodeVectorPageToken(response.Result.NextPageOffset)
	return result, nil
}

func milvusTableRows(ctx context.Context, s *dbSession, table string, limit, offset uint32, start time.Time) (*QueryResult, error) {
	payload := map[string]any{
		"collectionName":   table,
		"filter":           "",
		"limit":            limit,
		"offset":           offset,
		"outputFields":     []string{"*"},
		"consistencyLevel": "Strong",
	}
	if s.vector.database != "" {
		payload["dbName"] = s.vector.database
	}
	body, err := vectorRequest(ctx, s.vector, http.MethodPost,
		s.vector.baseURL+"/v2/vectordb/entities/query", payload)
	if err != nil {
		return nil, err
	}
	if err := validateMilvusResponse(body); err != nil {
		return nil, err
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return recordsResult(response.Data, start), nil
}

func weaviateTableRows(ctx context.Context, s *dbSession, table string, limit, offset uint32, start time.Time) (*QueryResult, error) {
	values := url.Values{}
	values.Set("class", table)
	values.Set("limit", strconv.FormatUint(uint64(limit), 10))
	values.Set("offset", strconv.FormatUint(uint64(offset), 10))
	values.Set("include", "vector")
	body, err := vectorRequest(ctx, s.vector, http.MethodGet,
		s.vector.baseURL+"/v1/objects?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Objects []map[string]any `json:"objects"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return recordsResult(expandPropertyRecords(response.Objects), start), nil
}

func pineconeTableRows(ctx context.Context, s *dbSession, table string, limit uint32, pageToken, namespace string, start time.Time) (*QueryResult, error) {
	ref := s.vector.collection(table)
	if ref.Host == "" {
		if _, err := pineconeSchema(ctx, s); err != nil {
			return nil, err
		}
		ref = s.vector.collection(table)
	}
	if ref.Host == "" {
		return nil, fmt.Errorf("找不到 Pinecone Index %q 的 data-plane host", table)
	}
	values := url.Values{}
	values.Set("limit", strconv.FormatUint(uint64(limit), 10))
	if strings.TrimSpace(pageToken) != "" {
		values.Set("paginationToken", strings.TrimSpace(pageToken))
	}
	if strings.TrimSpace(namespace) != "" {
		values.Set("namespace", strings.TrimSpace(namespace))
	}
	body, err := vectorRequest(ctx, s.vector, http.MethodGet,
		strings.TrimRight(ref.Host, "/")+"/vectors/list?"+values.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("列出 Vector ID 失败: %w", err)
	}
	var listed struct {
		Vectors []struct {
			ID string `json:"id"`
		} `json:"vectors"`
		Pagination struct {
			Next string `json:"next"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(listed.Vectors))
	for _, item := range listed.Vectors {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		result := recordsResult(nil, start)
		result.NextPageToken = trimmedOptional(&listed.Pagination.Next)
		return result, nil
	}
	fetchValues := url.Values{}
	for _, id := range ids {
		fetchValues.Add("ids", id)
	}
	if strings.TrimSpace(namespace) != "" {
		fetchValues.Set("namespace", strings.TrimSpace(namespace))
	}
	fetchBody, err := vectorRequest(ctx, s.vector, http.MethodGet,
		strings.TrimRight(ref.Host, "/")+"/vectors/fetch?"+fetchValues.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("读取 Vector 数据失败: %w", err)
	}
	var fetched struct {
		Vectors map[string]map[string]any `json:"vectors"`
	}
	if err := json.Unmarshal(fetchBody, &fetched); err != nil {
		return nil, err
	}
	records := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		record := fetched.Vectors[id]
		if record == nil {
			record = map[string]any{}
		}
		record["id"] = id
		records = append(records, record)
	}
	result := recordsResult(records, start)
	result.NextPageToken = trimmedOptional(&listed.Pagination.Next)
	return result, nil
}

func chromaTableRows(ctx context.Context, s *dbSession, table string, limit, offset uint32, start time.Time) (*QueryResult, error) {
	ref := s.vector.collection(table)
	if ref.ID == "" {
		if _, err := chromaSchema(ctx, s); err != nil {
			return nil, err
		}
		ref = s.vector.collection(table)
	}
	if ref.ID == "" {
		return nil, fmt.Errorf("找不到 Chroma Collection %q", table)
	}
	basePath := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s",
		url.PathEscape(s.vector.tenant), url.PathEscape(s.vector.database), url.PathEscape(ref.ID))
	payload := map[string]any{
		"limit": limit, "offset": offset,
		"include": []string{"metadatas", "documents", "embeddings"},
	}
	body, err := vectorRequest(ctx, s.vector, http.MethodPost,
		s.vector.baseURL+basePath+"/get", payload)
	if err != nil {
		body, err = vectorRequest(ctx, s.vector, http.MethodPost,
			s.vector.baseURL+"/api/v1/collections/"+url.PathEscape(ref.ID)+"/get", payload)
		if err != nil {
			return nil, err
		}
	}
	var response struct {
		IDs        []string         `json:"ids"`
		Embeddings []any            `json:"embeddings"`
		Metadatas  []map[string]any `json:"metadatas"`
		Documents  []any            `json:"documents"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	records := make([]map[string]any, 0, len(response.IDs))
	for index, id := range response.IDs {
		record := map[string]any{"id": id}
		if index < len(response.Metadatas) {
			record["metadata"] = response.Metadatas[index]
		}
		if index < len(response.Documents) {
			record["document"] = response.Documents[index]
		}
		if index < len(response.Embeddings) {
			record["vector"] = response.Embeddings[index]
		}
		records = append(records, record)
	}
	return recordsResult(records, start), nil
}

func vectorExecuteQuery(ctx context.Context, s *dbSession, query string, database *string) (*QueryResult, error) {
	var request vectorQueryRequest
	if err := json.Unmarshal([]byte(query), &request); err != nil {
		return nil, fmt.Errorf("向量查询必须是有效 JSON: %w", err)
	}
	request.Collection = strings.TrimSpace(request.Collection)
	if request.Collection == "" {
		return nil, fmt.Errorf("向量查询缺少 collection")
	}
	if len(request.Vector) == 0 {
		return nil, fmt.Errorf("向量查询的 vector 不能为空")
	}
	for _, value := range request.Vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("vector 只能包含有限数字")
		}
	}
	if request.Limit <= 0 {
		request.Limit = request.TopK
	}
	if request.Limit <= 0 {
		request.Limit = 10
	}
	if request.Limit > vectorQueryMaxLimit {
		request.Limit = vectorQueryMaxLimit
	}
	start := time.Now()
	switch s.vector.provider {
	case "pgvector":
		return pgvectorSearch(ctx, s, request, database, start)
	case "qdrant":
		return qdrantSearch(ctx, s, request, start)
	case "milvus":
		return milvusSearch(ctx, s, request, start)
	case "weaviate":
		return weaviateSearch(ctx, s, request, start)
	case "pinecone":
		return pineconeSearch(ctx, s, request, start)
	case "chroma":
		return chromaSearch(ctx, s, request, start)
	default:
		return nil, fmt.Errorf("%s 不支持向量查询", s.vector.provider)
	}
}

func pgvectorSearch(ctx context.Context, s *dbSession, request vectorQueryRequest, database *string, start time.Time) (*QueryResult, error) {
	schema := "public"
	if database != nil && strings.TrimSpace(*database) != "" {
		schema = strings.TrimSpace(*database)
	}
	table := request.Collection
	if dot := strings.Index(table, "."); dot > 0 && dot < len(table)-1 {
		schema, table = table[:dot], table[dot+1:]
	}
	column, err := pgvectorColumnInfo(ctx, s, schema, table, strings.TrimSpace(request.VectorColumn))
	if err != nil {
		return nil, err
	}
	operator := "<=>"
	switch strings.ToLower(strings.TrimSpace(request.Metric)) {
	case "", "cosine":
		operator = "<=>"
	case "l2", "euclidean":
		operator = "<->"
	case "inner", "ip", "dot":
		operator = "<#>"
	case "l1", "manhattan":
		operator = "<+>"
	default:
		return nil, fmt.Errorf("pgvector metric 仅支持 cosine、l2、inner 或 l1")
	}
	literal := pgvectorLiteral(request.Vector, column.TypeName)
	castType := quotePostgresIdent(column.TypeSchema) + "." + quotePostgresIdent(column.TypeName)
	query := fmt.Sprintf(`SELECT t.*, %s %s $1::%s AS distance
		FROM %s.%s AS t
		ORDER BY %s %s $1::%s
		LIMIT %d`, quotePostgresIdent(column.Name), operator, castType, quotePostgresIdent(schema),
		quotePostgresIdent(table), quotePostgresIdent(column.Name), operator, castType, request.Limit)
	rows, err := s.db.QueryContext(ctx, query, literal)
	if err != nil {
		return nil, fmt.Errorf("pgvector 查询失败: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, start)
}

func pgvectorColumnInfo(ctx context.Context, s *dbSession, schema, table, requestedColumn string) (pgvectorColumnRef, error) {
	var column pgvectorColumnRef
	err := s.db.QueryRowContext(ctx, `
		SELECT a.attname, tn.nspname, t.typname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_type t ON t.oid = a.atttypid
		JOIN pg_namespace tn ON tn.oid = t.typnamespace
		WHERE n.nspname = $1 AND c.relname = $2
		  AND a.attnum > 0 AND NOT a.attisdropped
		  AND t.typname IN ('vector', 'halfvec', 'sparsevec')
		  AND ($3 = '' OR a.attname = $3)
		ORDER BY a.attnum LIMIT 1`, schema, table, requestedColumn).
		Scan(&column.Name, &column.TypeSchema, &column.TypeName)
	if err == sql.ErrNoRows {
		if requestedColumn != "" {
			return pgvectorColumnRef{}, fmt.Errorf("%s.%s 的字段 %q 不是 pgvector 向量列", schema, table, requestedColumn)
		}
		return pgvectorColumnRef{}, fmt.Errorf("%s.%s 中没有 pgvector 向量列", schema, table)
	}
	if err != nil {
		return pgvectorColumnRef{}, err
	}
	return column, nil
}

func qdrantSearch(ctx context.Context, s *dbSession, request vectorQueryRequest, start time.Time) (*QueryResult, error) {
	payload := map[string]any{
		"query": request.Vector, "limit": request.Limit,
		"with_payload": true, "with_vector": request.IncludeVector,
	}
	if filter := decodeRawJSON(request.Filter); filter != nil {
		payload["filter"] = filter
	}
	body, err := vectorRequest(ctx, s.vector, http.MethodPost,
		s.vector.baseURL+"/collections/"+url.PathEscape(request.Collection)+"/points/query", payload)
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			Points []map[string]any `json:"points"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return recordsResult(response.Result.Points, start), nil
}

func milvusSearch(ctx context.Context, s *dbSession, request vectorQueryRequest, start time.Time) (*QueryResult, error) {
	outputFields := request.OutputFields
	if len(outputFields) == 0 {
		outputFields = []string{"*"}
	}
	payload := map[string]any{
		"collectionName": request.Collection,
		"data":           [][]float64{request.Vector},
		"limit":          request.Limit,
		"outputFields":   outputFields,
	}
	if s.vector.database != "" {
		payload["dbName"] = s.vector.database
	}
	if filter := decodeRawJSON(request.Filter); filter != nil {
		switch value := filter.(type) {
		case string:
			payload["filter"] = value
		default:
			return nil, fmt.Errorf("Milvus filter 必须是字符串表达式")
		}
	}
	body, err := vectorRequest(ctx, s.vector, http.MethodPost,
		s.vector.baseURL+"/v2/vectordb/entities/search", payload)
	if err != nil {
		return nil, err
	}
	if err := validateMilvusResponse(body); err != nil {
		return nil, err
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return recordsResult(response.Data, start), nil
}

func weaviateSearch(ctx context.Context, s *dbSession, request vectorQueryRequest, start time.Time) (*QueryResult, error) {
	if !isGraphQLName(request.Collection) {
		return nil, fmt.Errorf("Weaviate Collection 名称不合法: %q", request.Collection)
	}
	properties, err := weaviatePropertyNames(ctx, s, request.Collection)
	if err != nil {
		return nil, err
	}
	selection := strings.Join(properties, " ")
	if selection != "" {
		selection += " "
	}
	vectorBytes, _ := json.Marshal(request.Vector)
	nearVector := fmt.Sprintf("vector: %s", string(vectorBytes))
	if target := strings.TrimSpace(request.TargetVector); target != "" {
		targetBytes, _ := json.Marshal([]string{target})
		nearVector += ", targetVectors: " + string(targetBytes)
	}
	graphql := fmt.Sprintf(`{ Get { %s(nearVector: {%s}, limit: %d) { %s_additional { id distance%s } } } }`,
		request.Collection, nearVector, request.Limit, selection,
		map[bool]string{true: " vector", false: ""}[request.IncludeVector])
	body, err := vectorRequest(ctx, s.vector, http.MethodPost,
		s.vector.baseURL+"/v1/graphql", map[string]any{"query": graphql})
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			Get map[string][]map[string]any `json:"Get"`
		} `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 {
		encoded, _ := json.Marshal(response.Errors)
		return nil, fmt.Errorf("Weaviate 查询失败: %s", encoded)
	}
	records := response.Data.Get[request.Collection]
	for _, record := range records {
		if additional, ok := record["_additional"].(map[string]any); ok {
			for key, value := range additional {
				record[key] = value
			}
			delete(record, "_additional")
		}
	}
	return recordsResult(records, start), nil
}

func weaviatePropertyNames(ctx context.Context, s *dbSession, collection string) ([]string, error) {
	body, err := vectorRequest(ctx, s.vector, http.MethodGet,
		s.vector.baseURL+"/v1/schema/"+url.PathEscape(collection), nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Properties []struct {
			Name string `json:"name"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(response.Properties))
	for _, property := range response.Properties {
		if isGraphQLName(property.Name) {
			out = append(out, property.Name)
		}
	}
	return out, nil
}

func pineconeSearch(ctx context.Context, s *dbSession, request vectorQueryRequest, start time.Time) (*QueryResult, error) {
	ref := s.vector.collection(request.Collection)
	if ref.Host == "" {
		if _, err := pineconeSchema(ctx, s); err != nil {
			return nil, err
		}
		ref = s.vector.collection(request.Collection)
	}
	if ref.Host == "" {
		return nil, fmt.Errorf("找不到 Pinecone Index %q 的 data-plane host", request.Collection)
	}
	payload := map[string]any{
		"vector": request.Vector, "topK": request.Limit,
		"includeMetadata": true, "includeValues": request.IncludeVector,
	}
	if request.Namespace != "" {
		payload["namespace"] = request.Namespace
	}
	if filter := decodeRawJSON(request.Filter); filter != nil {
		payload["filter"] = filter
	}
	body, err := vectorRequest(ctx, s.vector, http.MethodPost,
		strings.TrimRight(ref.Host, "/")+"/query", payload)
	if err != nil {
		return nil, err
	}
	var response struct {
		Matches []map[string]any `json:"matches"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return recordsResult(response.Matches, start), nil
}

func chromaSearch(ctx context.Context, s *dbSession, request vectorQueryRequest, start time.Time) (*QueryResult, error) {
	ref := s.vector.collection(request.Collection)
	if ref.ID == "" {
		if _, err := chromaSchema(ctx, s); err != nil {
			return nil, err
		}
		ref = s.vector.collection(request.Collection)
	}
	if ref.ID == "" {
		return nil, fmt.Errorf("找不到 Chroma Collection %q", request.Collection)
	}
	include := []string{"metadatas", "documents", "distances"}
	if request.IncludeVector {
		include = append(include, "embeddings")
	}
	payload := map[string]any{
		"query_embeddings": [][]float64{request.Vector},
		"n_results":        request.Limit,
		"include":          include,
	}
	if filter := decodeRawJSON(request.Filter); filter != nil {
		payload["where"] = filter
	}
	basePath := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s/query",
		url.PathEscape(s.vector.tenant), url.PathEscape(s.vector.database), url.PathEscape(ref.ID))
	body, err := vectorRequest(ctx, s.vector, http.MethodPost, s.vector.baseURL+basePath, payload)
	if err != nil {
		body, err = vectorRequest(ctx, s.vector, http.MethodPost,
			s.vector.baseURL+"/api/v1/collections/"+url.PathEscape(ref.ID)+"/query", payload)
		if err != nil {
			return nil, err
		}
	}
	var response struct {
		IDs        [][]string         `json:"ids"`
		Distances  [][]any            `json:"distances"`
		Embeddings [][]any            `json:"embeddings"`
		Metadatas  [][]map[string]any `json:"metadatas"`
		Documents  [][]any            `json:"documents"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	var records []map[string]any
	if len(response.IDs) > 0 {
		records = make([]map[string]any, 0, len(response.IDs[0]))
		for index, id := range response.IDs[0] {
			record := map[string]any{"id": id}
			if len(response.Distances) > 0 && index < len(response.Distances[0]) {
				record["distance"] = response.Distances[0][index]
			}
			if len(response.Metadatas) > 0 && index < len(response.Metadatas[0]) {
				record["metadata"] = response.Metadatas[0][index]
			}
			if len(response.Documents) > 0 && index < len(response.Documents[0]) {
				record["document"] = response.Documents[0][index]
			}
			if len(response.Embeddings) > 0 && index < len(response.Embeddings[0]) {
				record["vector"] = response.Embeddings[0][index]
			}
			records = append(records, record)
		}
	}
	return recordsResult(records, start), nil
}

func vectorTableColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	result, err := vectorTableRows(ctx, s, database, table, 1, 0)
	if err != nil {
		return nil, fmt.Errorf("推导向量字段失败: %w", err)
	}
	names := append([]string(nil), result.Columns...)
	if len(names) == 0 {
		switch s.vector.provider {
		case "qdrant":
			names = []string{"id", "vector", "payload"}
		case "pinecone":
			names = []string{"id", "values", "metadata"}
		case "chroma":
			names = []string{"id", "vector", "document", "metadata"}
		default:
			names = []string{"id", "vector"}
		}
	}
	var sample []any
	if len(result.Rows) > 0 {
		sample = result.Rows[0]
	}
	columns := make([]ColumnInfo, 0, len(names))
	for index, name := range names {
		var value any
		if index < len(sample) {
			value = sample[index]
		}
		column := ColumnInfo{
			Name:         name,
			DataType:     vectorColumnDataType(name, value),
			Nullable:     !strings.EqualFold(name, "id"),
			IsPrimaryKey: strings.EqualFold(name, "id"),
		}
		switch strings.ToLower(name) {
		case "class", "creationtimeunix", "lastupdatetimeunix", "vectorweights":
			column.AutoIncrement = true
		}
		columns = append(columns, column)
	}
	return columns, nil
}

func vectorColumnDataType(name string, value any) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "vector", "values", "embedding", "embeddings", "vectors", "vectorweights":
		return "VECTOR"
	case "payload", "metadata", "properties":
		return "JSON"
	case "document", "content", "description":
		return "TEXT"
	}
	switch value.(type) {
	case bool:
		return "BOOLEAN"
	case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return "NUMBER"
	case []any, []float64, []string, map[string]any:
		return "JSON"
	default:
		return "STRING"
	}
}

func vectorInsertRow(ctx context.Context, s *dbSession, table string, fields map[string]any, namespace string) (int64, error) {
	if err := vectorWriteRow(ctx, s, table, fields, namespace, false); err != nil {
		return 0, err
	}
	return 1, nil
}

func vectorUpdateCell(ctx context.Context, s *dbSession, table string, primaryKey map[string]any, column string, value any, namespace string) (int64, error) {
	id, err := vectorPrimaryKeyValue(primaryKey)
	if err != nil {
		return 0, err
	}
	column = strings.TrimSpace(column)
	if column == "" || strings.EqualFold(column, "id") {
		return 0, fmt.Errorf("向量记录主键 id 不支持修改")
	}
	record, err := vectorFetchRecord(ctx, s, table, id, namespace)
	if err != nil {
		return 0, err
	}
	record[column] = value
	record["id"] = id
	if err := vectorWriteRow(ctx, s, table, record, namespace, true); err != nil {
		return 0, err
	}
	return 1, nil
}

func vectorDeleteRow(ctx context.Context, s *dbSession, table string, primaryKey map[string]any, namespace string) (int64, error) {
	id, err := vectorPrimaryKeyValue(primaryKey)
	if err != nil {
		return 0, err
	}
	if err := validateVectorObjectName(strings.TrimSpace(table)); err != nil {
		return 0, err
	}
	switch s.vector.provider {
	case "qdrant":
		_, err = vectorRequest(ctx, s.vector, http.MethodPost,
			s.vector.baseURL+"/collections/"+url.PathEscape(table)+"/points/delete?wait=true",
			map[string]any{"points": []any{id}})
	case "milvus":
		payload := map[string]any{
			"collectionName": table,
			"filter":         "id in [" + vectorFilterLiteral(id) + "]",
		}
		if s.vector.database != "" {
			payload["dbName"] = s.vector.database
		}
		var body []byte
		body, err = vectorRequest(ctx, s.vector, http.MethodPost,
			s.vector.baseURL+"/v2/vectordb/entities/delete", payload)
		if err == nil {
			err = validateMilvusResponse(body)
		}
	case "weaviate":
		_, err = vectorRequest(ctx, s.vector, http.MethodDelete,
			s.vector.baseURL+"/v1/objects/"+url.PathEscape(table)+"/"+url.PathEscape(fmt.Sprint(id)), nil)
	case "pinecone":
		var host string
		host, err = pineconeDataHost(ctx, s, table)
		if err == nil {
			payload := map[string]any{"ids": []string{fmt.Sprint(id)}}
			if strings.TrimSpace(namespace) != "" {
				payload["namespace"] = strings.TrimSpace(namespace)
			}
			_, err = vectorRequest(ctx, s.vector, http.MethodPost, host+"/vectors/delete", payload)
		}
	case "chroma":
		var basePath string
		basePath, err = chromaCollectionBasePath(ctx, s, table)
		if err == nil {
			_, err = chromaCollectionRequest(ctx, s, basePath, "/delete", map[string]any{
				"ids": []string{fmt.Sprint(id)},
			})
		}
	default:
		return 0, fmt.Errorf("%s 不支持删除向量记录", s.vector.provider)
	}
	if err != nil {
		return 0, fmt.Errorf("删除向量记录 %v 失败: %w", id, err)
	}
	return 1, nil
}

func vectorWriteRow(ctx context.Context, s *dbSession, table string, fields map[string]any, namespace string, replace bool) error {
	if s.vector == nil || s.kind != kindVector {
		return fmt.Errorf("当前会话不支持向量记录写入")
	}
	table = strings.TrimSpace(table)
	if err := validateVectorObjectName(table); err != nil {
		return err
	}
	id, err := vectorRecordID(fields)
	if err != nil {
		return err
	}
	switch s.vector.provider {
	case "qdrant":
		vector, ok := fields["vector"]
		if !ok || vector == nil {
			return fmt.Errorf("Qdrant 记录缺少 vector")
		}
		payload, err := vectorObjectField(fields, "payload")
		if err != nil {
			return err
		}
		if payload == nil {
			payload = vectorExtraFields(fields, "id", "vector")
		}
		_, err = vectorRequest(ctx, s.vector, http.MethodPut,
			s.vector.baseURL+"/collections/"+url.PathEscape(table)+"/points?wait=true",
			map[string]any{"points": []map[string]any{{"id": id, "vector": vector, "payload": payload}}})
	case "milvus":
		data := vectorExtraFields(fields, "distance", "score")
		data["id"] = id
		payload := map[string]any{"collectionName": table, "data": []map[string]any{data}}
		if s.vector.database != "" {
			payload["dbName"] = s.vector.database
		}
		var body []byte
		body, err = vectorRequest(ctx, s.vector, http.MethodPost,
			s.vector.baseURL+"/v2/vectordb/entities/upsert", payload)
		if err == nil {
			err = validateMilvusResponse(body)
		}
	case "weaviate":
		properties, err := vectorObjectField(fields, "properties")
		if err != nil {
			return err
		}
		if properties == nil {
			properties = map[string]any{}
		}
		for key, value := range fields {
			switch strings.ToLower(key) {
			case "id", "class", "vector", "vectors", "properties", "creationtimeunix", "lastupdatetimeunix", "vectorweights", "distance", "score":
				continue
			default:
				properties[key] = value
			}
		}
		payload := map[string]any{"class": table, "id": fmt.Sprint(id), "properties": properties}
		if vector, ok := fields["vector"]; ok && vector != nil {
			payload["vector"] = vector
		}
		method := http.MethodPost
		endpoint := s.vector.baseURL + "/v1/objects"
		if replace {
			method = http.MethodPut
			endpoint += "/" + url.PathEscape(table) + "/" + url.PathEscape(fmt.Sprint(id))
		}
		_, err = vectorRequest(ctx, s.vector, method, endpoint, payload)
	case "pinecone":
		values, ok := fields["values"]
		if !ok {
			values = fields["vector"]
		}
		if values == nil {
			return fmt.Errorf("Pinecone 记录缺少 values")
		}
		metadata, err := vectorObjectField(fields, "metadata")
		if err != nil {
			return err
		}
		if metadata == nil {
			metadata = vectorExtraFields(fields, "id", "values", "vector")
		}
		var host string
		host, err = pineconeDataHost(ctx, s, table)
		if err == nil {
			vector := map[string]any{"id": fmt.Sprint(id), "values": values, "metadata": metadata}
			payload := map[string]any{"vectors": []map[string]any{vector}}
			if strings.TrimSpace(namespace) != "" {
				payload["namespace"] = strings.TrimSpace(namespace)
			}
			_, err = vectorRequest(ctx, s.vector, http.MethodPost, host+"/vectors/upsert", payload)
		}
	case "chroma":
		vector, ok := fields["vector"]
		if !ok {
			vector = fields["embedding"]
		}
		if vector == nil {
			return fmt.Errorf("Chroma 记录缺少 vector")
		}
		metadata, err := vectorObjectField(fields, "metadata")
		if err != nil {
			return err
		}
		if metadata == nil {
			metadata = vectorExtraFields(fields, "id", "vector", "embedding", "document")
		}
		var basePath string
		basePath, err = chromaCollectionBasePath(ctx, s, table)
		if err == nil {
			payload := map[string]any{
				"ids":        []string{fmt.Sprint(id)},
				"embeddings": []any{vector},
				"metadatas":  []map[string]any{metadata},
			}
			if document, ok := fields["document"]; ok {
				payload["documents"] = []any{document}
			}
			_, err = chromaCollectionRequest(ctx, s, basePath, "/upsert", payload)
		}
	default:
		return fmt.Errorf("%s 不支持写入向量记录", s.vector.provider)
	}
	if err != nil {
		return fmt.Errorf("写入向量记录 %v 失败: %w", id, err)
	}
	return nil
}

func vectorFetchRecord(ctx context.Context, s *dbSession, table string, id any, namespace string) (map[string]any, error) {
	var record map[string]any
	switch s.vector.provider {
	case "qdrant":
		body, err := vectorRequest(ctx, s.vector, http.MethodGet,
			s.vector.baseURL+"/collections/"+url.PathEscape(table)+"/points/"+url.PathEscape(fmt.Sprint(id)), nil)
		if err != nil {
			return nil, err
		}
		var response struct {
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		record = response.Result
	case "milvus":
		payload := map[string]any{
			"collectionName":   table,
			"filter":           "id == " + vectorFilterLiteral(id),
			"limit":            1,
			"outputFields":     []string{"*"},
			"consistencyLevel": "Strong",
		}
		if s.vector.database != "" {
			payload["dbName"] = s.vector.database
		}
		body, err := vectorRequest(ctx, s.vector, http.MethodPost,
			s.vector.baseURL+"/v2/vectordb/entities/query", payload)
		if err != nil {
			return nil, err
		}
		if err := validateMilvusResponse(body); err != nil {
			return nil, err
		}
		var response struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		if len(response.Data) > 0 {
			record = response.Data[0]
		}
	case "weaviate":
		body, err := vectorRequest(ctx, s.vector, http.MethodGet,
			s.vector.baseURL+"/v1/objects/"+url.PathEscape(table)+"/"+url.PathEscape(fmt.Sprint(id))+"?include=vector", nil)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &record); err != nil {
			return nil, err
		}
		expandPropertyRecords([]map[string]any{record})
	case "pinecone":
		host, err := pineconeDataHost(ctx, s, table)
		if err != nil {
			return nil, err
		}
		values := url.Values{}
		values.Add("ids", fmt.Sprint(id))
		if strings.TrimSpace(namespace) != "" {
			values.Set("namespace", strings.TrimSpace(namespace))
		}
		body, err := vectorRequest(ctx, s.vector, http.MethodGet, host+"/vectors/fetch?"+values.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var response struct {
			Vectors map[string]map[string]any `json:"vectors"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		record = response.Vectors[fmt.Sprint(id)]
	case "chroma":
		basePath, err := chromaCollectionBasePath(ctx, s, table)
		if err != nil {
			return nil, err
		}
		body, err := chromaCollectionRequest(ctx, s, basePath, "/get", map[string]any{
			"ids": []string{fmt.Sprint(id)}, "include": []string{"metadatas", "documents", "embeddings"},
		})
		if err != nil {
			return nil, err
		}
		var response struct {
			IDs        []string         `json:"ids"`
			Embeddings []any            `json:"embeddings"`
			Metadatas  []map[string]any `json:"metadatas"`
			Documents  []any            `json:"documents"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		if len(response.IDs) > 0 {
			record = map[string]any{"id": response.IDs[0]}
			if len(response.Embeddings) > 0 {
				record["vector"] = response.Embeddings[0]
			}
			if len(response.Metadatas) > 0 {
				record["metadata"] = response.Metadatas[0]
			}
			if len(response.Documents) > 0 {
				record["document"] = response.Documents[0]
			}
		}
	default:
		return nil, fmt.Errorf("%s 不支持读取单条向量记录", s.vector.provider)
	}
	if record == nil {
		return nil, fmt.Errorf("找不到向量记录 %v", id)
	}
	return record, nil
}

func vectorRecordID(fields map[string]any) (any, error) {
	for key, value := range fields {
		if strings.EqualFold(key, "id") && value != nil && strings.TrimSpace(fmt.Sprint(value)) != "" {
			return value, nil
		}
	}
	return nil, fmt.Errorf("向量记录缺少 id")
}

func vectorPrimaryKeyValue(primaryKey map[string]any) (any, error) {
	if len(primaryKey) != 1 {
		return nil, fmt.Errorf("向量记录操作需要且仅支持 id 主键")
	}
	for key, value := range primaryKey {
		if !strings.EqualFold(key, "id") {
			return nil, fmt.Errorf("向量记录主键必须是 id")
		}
		if value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
			return nil, fmt.Errorf("向量记录 id 不能为空")
		}
		return value, nil
	}
	return nil, fmt.Errorf("向量记录缺少 id")
}

func vectorObjectField(fields map[string]any, name string) (map[string]any, error) {
	value, ok := fields[name]
	if !ok || value == nil {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("向量记录字段 %s 必须是 JSON 对象", name)
	}
	return vectorExtraFields(object), nil
}

func vectorExtraFields(fields map[string]any, excluded ...string) map[string]any {
	skip := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		skip[strings.ToLower(name)] = struct{}{}
	}
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		if _, ok := skip[strings.ToLower(key)]; !ok {
			out[key] = value
		}
	}
	return out
}

func vectorFilterLiteral(value any) string {
	switch typed := value.(type) {
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	default:
		encoded, _ := json.Marshal(fmt.Sprint(value))
		return string(encoded)
	}
}

func pineconeDataHost(ctx context.Context, s *dbSession, table string) (string, error) {
	ref := s.vector.collection(table)
	if ref.Host == "" {
		if _, err := pineconeSchema(ctx, s); err != nil {
			return "", err
		}
		ref = s.vector.collection(table)
	}
	if ref.Host == "" {
		return "", fmt.Errorf("找不到 Pinecone Index %q 的 data-plane host", table)
	}
	return strings.TrimRight(ref.Host, "/"), nil
}

func chromaCollectionBasePath(ctx context.Context, s *dbSession, table string) (string, error) {
	ref := s.vector.collection(table)
	if ref.ID == "" {
		if _, err := chromaSchema(ctx, s); err != nil {
			return "", err
		}
		ref = s.vector.collection(table)
	}
	if ref.ID == "" {
		return "", fmt.Errorf("找不到 Chroma Collection %q", table)
	}
	return fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s",
		url.PathEscape(s.vector.tenant), url.PathEscape(s.vector.database), url.PathEscape(ref.ID)), nil
}

func chromaCollectionRequest(ctx context.Context, s *dbSession, basePath, suffix string, payload any) ([]byte, error) {
	body, err := vectorRequest(ctx, s.vector, http.MethodPost, s.vector.baseURL+basePath+suffix, payload)
	if err == nil {
		return body, nil
	}
	parts := strings.Split(strings.Trim(basePath, "/"), "/")
	if len(parts) == 0 {
		return nil, err
	}
	collectionID := parts[len(parts)-1]
	return vectorRequest(ctx, s.vector, http.MethodPost,
		s.vector.baseURL+"/api/v1/collections/"+url.PathEscape(collectionID)+suffix, payload)
}

func vectorCreateCollection(ctx context.Context, s *dbSession, request vectorCreateCollectionRequest) error {
	if s.vector == nil || s.kind != kindVector {
		return fmt.Errorf("当前会话不支持创建向量 Collection/Index")
	}
	request.Name = strings.TrimSpace(request.Name)
	if err := validateVectorObjectName(request.Name); err != nil {
		return err
	}
	if (s.vector.provider == "qdrant" || s.vector.provider == "milvus" || s.vector.provider == "pinecone") &&
		(request.Dimension <= 0 || request.Dimension > 1000000) {
		return fmt.Errorf("向量维度必须在 1 到 1000000 之间")
	}

	metric := normalizeVectorMetric(request.Metric)
	var err error
	switch s.vector.provider {
	case "qdrant":
		payload := map[string]any{"vectors": map[string]any{
			"size": request.Dimension, "distance": qdrantMetric(metric),
		}}
		_, err = vectorRequest(ctx, s.vector, http.MethodPut,
			s.vector.baseURL+"/collections/"+url.PathEscape(request.Name), payload)
	case "milvus":
		payload := map[string]any{
			"collectionName": request.Name,
			"dimension":      request.Dimension,
			"metricType":     milvusMetric(metric),
		}
		if s.vector.database != "" {
			payload["dbName"] = s.vector.database
		}
		var body []byte
		body, err = vectorRequest(ctx, s.vector, http.MethodPost,
			s.vector.baseURL+"/v2/vectordb/collections/create", payload)
		if err == nil {
			err = validateMilvusResponse(body)
		}
	case "weaviate":
		if !isGraphQLName(request.Name) {
			return fmt.Errorf("Weaviate Collection 名称必须是合法的 GraphQL 名称")
		}
		payload := map[string]any{"class": request.Name}
		vectorName := strings.TrimSpace(request.VectorName)
		if vectorName == "" {
			payload["vectorizer"] = "none"
			payload["vectorIndexConfig"] = map[string]any{"distance": weaviateMetric(metric)}
		} else {
			if !isGraphQLName(vectorName) {
				return fmt.Errorf("Weaviate named vector 名称必须是合法的 GraphQL 名称")
			}
			payload["vectorConfig"] = map[string]any{vectorName: map[string]any{
				"vectorizer":        map[string]any{"none": map[string]any{}},
				"vectorIndexType":   "hnsw",
				"vectorIndexConfig": map[string]any{"distance": weaviateMetric(metric)},
			}}
		}
		_, err = vectorRequest(ctx, s.vector, http.MethodPost, s.vector.baseURL+"/v1/schema", payload)
	case "pinecone":
		if !strings.Contains(strings.ToLower(s.vector.baseURL), "api.pinecone.io") {
			return fmt.Errorf("Pinecone 创建 Index 需要连接 control-plane 地址 api.pinecone.io")
		}
		cloud := strings.TrimSpace(request.Cloud)
		region := strings.TrimSpace(request.Region)
		if cloud == "" {
			cloud = "aws"
		}
		if region == "" {
			region = "us-east-1"
		}
		payload := map[string]any{
			"name": request.Name, "dimension": request.Dimension, "metric": pineconeMetric(metric),
			"spec": map[string]any{"serverless": map[string]any{"cloud": cloud, "region": region}},
		}
		_, err = vectorRequest(ctx, s.vector, http.MethodPost, s.vector.baseURL+"/indexes", payload)
	case "chroma":
		path := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections",
			url.PathEscape(s.vector.tenant), url.PathEscape(s.vector.database))
		payload := map[string]any{
			"name": request.Name,
			"configuration": map[string]any{
				"hnsw": map[string]any{"space": chromaMetric(metric)},
			},
			"get_or_create": false,
		}
		_, err = vectorRequest(ctx, s.vector, http.MethodPost, s.vector.baseURL+path, payload)
		if err != nil {
			legacyPayload := map[string]any{
				"name": request.Name, "metadata": map[string]any{"hnsw:space": chromaMetric(metric)},
				"get_or_create": false,
			}
			_, err = vectorRequest(ctx, s.vector, http.MethodPost,
				s.vector.baseURL+"/api/v1/collections", legacyPayload)
		}
	default:
		return fmt.Errorf("%s 不支持创建 Collection/Index", s.vector.provider)
	}
	if err != nil {
		return fmt.Errorf("创建 %s 失败: %w", request.Name, err)
	}
	return nil
}

func vectorDeleteCollection(ctx context.Context, s *dbSession, name string) error {
	if s.vector == nil || s.kind != kindVector {
		return fmt.Errorf("当前会话不支持删除向量 Collection/Index")
	}
	name = strings.TrimSpace(name)
	if err := validateVectorObjectName(name); err != nil {
		return err
	}
	var err error
	switch s.vector.provider {
	case "qdrant":
		_, err = vectorRequest(ctx, s.vector, http.MethodDelete,
			s.vector.baseURL+"/collections/"+url.PathEscape(name), nil)
	case "milvus":
		payload := map[string]any{"collectionName": name}
		if s.vector.database != "" {
			payload["dbName"] = s.vector.database
		}
		var body []byte
		body, err = vectorRequest(ctx, s.vector, http.MethodPost,
			s.vector.baseURL+"/v2/vectordb/collections/drop", payload)
		if err == nil {
			err = validateMilvusResponse(body)
		}
	case "weaviate":
		_, err = vectorRequest(ctx, s.vector, http.MethodDelete,
			s.vector.baseURL+"/v1/schema/"+url.PathEscape(name), nil)
	case "pinecone":
		if !strings.Contains(strings.ToLower(s.vector.baseURL), "api.pinecone.io") {
			return fmt.Errorf("Pinecone 删除 Index 需要连接 control-plane 地址 api.pinecone.io")
		}
		_, err = vectorRequest(ctx, s.vector, http.MethodDelete,
			s.vector.baseURL+"/indexes/"+url.PathEscape(name), nil)
	case "chroma":
		ref := s.vector.collection(name)
		if ref.ID == "" {
			if _, schemaErr := chromaSchema(ctx, s); schemaErr != nil {
				return schemaErr
			}
			ref = s.vector.collection(name)
		}
		if ref.ID == "" {
			return fmt.Errorf("找不到 Chroma Collection %q", name)
		}
		path := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s",
			url.PathEscape(s.vector.tenant), url.PathEscape(s.vector.database), url.PathEscape(ref.ID))
		_, err = vectorRequest(ctx, s.vector, http.MethodDelete, s.vector.baseURL+path, nil)
		if err != nil {
			_, err = vectorRequest(ctx, s.vector, http.MethodDelete,
				s.vector.baseURL+"/api/v1/collections/"+url.PathEscape(ref.ID), nil)
		}
	default:
		return fmt.Errorf("%s 不支持删除 Collection/Index", s.vector.provider)
	}
	if err != nil {
		return fmt.Errorf("删除 %s 失败: %w", name, err)
	}
	s.vector.forgetCollection(name)
	return nil
}

func validateVectorObjectName(name string) error {
	if name == "" {
		return fmt.Errorf("Collection/Index 名称不能为空")
	}
	if len(name) > 255 {
		return fmt.Errorf("Collection/Index 名称不能超过 255 个字符")
	}
	if strings.ContainsAny(name, "/\\\x00\r\n") {
		return fmt.Errorf("Collection/Index 名称包含非法字符")
	}
	return nil
}

func normalizeVectorMetric(metric string) string {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case "l2", "euclid", "euclidean", "l2-squared":
		return "euclidean"
	case "ip", "inner", "inner_product", "dot", "dotproduct":
		return "dotproduct"
	case "manhattan", "l1":
		return "manhattan"
	default:
		return "cosine"
	}
}

func qdrantMetric(metric string) string {
	switch metric {
	case "euclidean":
		return "Euclid"
	case "dotproduct":
		return "Dot"
	case "manhattan":
		return "Manhattan"
	default:
		return "Cosine"
	}
}

func milvusMetric(metric string) string {
	switch metric {
	case "euclidean":
		return "L2"
	case "dotproduct":
		return "IP"
	default:
		return "COSINE"
	}
}

func weaviateMetric(metric string) string {
	switch metric {
	case "euclidean":
		return "l2-squared"
	case "dotproduct":
		return "dot"
	default:
		return "cosine"
	}
}

func pineconeMetric(metric string) string {
	if metric == "euclidean" || metric == "dotproduct" {
		return metric
	}
	return "cosine"
}

func chromaMetric(metric string) string {
	switch metric {
	case "euclidean":
		return "l2"
	case "dotproduct":
		return "ip"
	default:
		return "cosine"
	}
}

func vectorCollectionDefinition(ctx context.Context, s *dbSession, database, table string) (string, error) {
	var body []byte
	var err error
	switch s.vector.provider {
	case "qdrant":
		body, err = vectorRequest(ctx, s.vector, http.MethodGet,
			s.vector.baseURL+"/collections/"+url.PathEscape(table), nil)
	case "milvus":
		payload := map[string]any{"collectionName": table}
		if s.vector.database != "" {
			payload["dbName"] = s.vector.database
		}
		body, err = vectorRequest(ctx, s.vector, http.MethodPost,
			s.vector.baseURL+"/v2/vectordb/collections/describe", payload)
		if err == nil {
			err = validateMilvusResponse(body)
		}
	case "weaviate":
		body, err = vectorRequest(ctx, s.vector, http.MethodGet,
			s.vector.baseURL+"/v1/schema/"+url.PathEscape(table), nil)
	case "pinecone":
		body, err = vectorRequest(ctx, s.vector, http.MethodGet,
			s.vector.baseURL+"/indexes/"+url.PathEscape(table), nil)
	case "chroma":
		ref := s.vector.collection(table)
		if ref.ID == "" {
			_, err = chromaSchema(ctx, s)
			ref = s.vector.collection(table)
		}
		if err == nil && ref.ID != "" {
			path := fmt.Sprintf("/api/v2/tenants/%s/databases/%s/collections/%s",
				url.PathEscape(s.vector.tenant), url.PathEscape(s.vector.database), url.PathEscape(ref.ID))
			body, err = vectorRequest(ctx, s.vector, http.MethodGet, s.vector.baseURL+path, nil)
		}
	default:
		err = fmt.Errorf("不支持读取 Collection 定义")
	}
	if err != nil {
		return "", err
	}
	var value any
	if json.Unmarshal(body, &value) == nil {
		pretty, marshalErr := json.MarshalIndent(value, "", "  ")
		if marshalErr == nil {
			return string(pretty), nil
		}
	}
	return string(body), nil
}

func encodeVectorPageToken(value any) *string {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" {
		return nil
	}
	token := string(encoded)
	return &token
}

func recordsResult(records []map[string]any, start time.Time) *QueryResult {
	if records == nil {
		records = []map[string]any{}
	}
	columnSet := map[string]struct{}{}
	for _, record := range records {
		for key := range record {
			columnSet[key] = struct{}{}
		}
	}
	preferred := []string{"id", "score", "distance", "document", "metadata", "payload", "vector", "values"}
	columns := make([]string, 0, len(columnSet))
	for _, key := range preferred {
		if _, ok := columnSet[key]; ok {
			columns = append(columns, key)
			delete(columnSet, key)
		}
	}
	remaining := make([]string, 0, len(columnSet))
	for key := range columnSet {
		remaining = append(remaining, key)
	}
	sort.Strings(remaining)
	columns = append(columns, remaining...)
	rows := make([][]any, 0, len(records))
	for _, record := range records {
		row := make([]any, len(columns))
		for index, column := range columns {
			row[index] = record[column]
		}
		rows = append(rows, row)
	}
	return &QueryResult{
		Columns: columns, Rows: rows, RowCount: len(rows), ExecutionMs: time.Since(start).Milliseconds(),
	}
}

func expandPropertyRecords(records []map[string]any) []map[string]any {
	for _, record := range records {
		properties, ok := record["properties"].(map[string]any)
		if !ok {
			continue
		}
		for key, value := range properties {
			if _, exists := record[key]; !exists {
				record[key] = value
			}
		}
		delete(record, "properties")
	}
	return records
}

func decodeRawJSON(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func pgvectorMutationFields(ctx context.Context, s *dbSession, database, table string, fields map[string]any) (map[string]any, error) {
	columns, err := postgresColumns(ctx, s, database, table)
	if err != nil {
		return nil, fmt.Errorf("读取 pgvector 字段失败: %w", err)
	}
	columnTypes := make(map[string]string, len(columns))
	for _, column := range columns {
		columnTypes[strings.ToLower(column.Name)] = column.DataType
	}
	prepared := make(map[string]any, len(fields))
	for name, value := range fields {
		prepared[name], err = pgvectorMutationValueForType(value, columnTypes[strings.ToLower(name)])
		if err != nil {
			return nil, fmt.Errorf("pgvector 字段 %s: %w", name, err)
		}
	}
	return prepared, nil
}

func pgvectorMutationValue(ctx context.Context, s *dbSession, database, table, column string, value any) (any, error) {
	columns, err := postgresColumns(ctx, s, database, table)
	if err != nil {
		return nil, fmt.Errorf("读取 pgvector 字段失败: %w", err)
	}
	for _, info := range columns {
		if strings.EqualFold(info.Name, column) {
			return pgvectorMutationValueForType(value, info.DataType)
		}
	}
	return nil, fmt.Errorf("pgvector 表 %s.%s 中找不到字段 %s", database, table, column)
}

func pgvectorMutationValueForType(value any, dataType string) (any, error) {
	typeName := strings.ToLower(strings.TrimSpace(dataType))
	if index := strings.IndexByte(typeName, '('); index >= 0 {
		typeName = typeName[:index]
	}
	if typeName != "vector" && typeName != "halfvec" && typeName != "sparsevec" {
		return value, nil
	}
	if value == nil {
		return nil, nil
	}
	if literal, ok := value.(string); ok {
		return strings.TrimSpace(literal), nil
	}
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case []float64:
		vector := append([]float64(nil), typed...)
		return pgvectorLiteral(vector, typeName), nil
	default:
		return nil, fmt.Errorf("向量值必须是 JSON 数组")
	}
	vector := make([]float64, len(raw))
	for index, item := range raw {
		number, ok := item.(float64)
		if !ok {
			switch typed := item.(type) {
			case float32:
				number = float64(typed)
			case int:
				number = float64(typed)
			case int64:
				number = float64(typed)
			case json.Number:
				parsed, err := typed.Float64()
				if err != nil {
					return nil, fmt.Errorf("第 %d 个元素不是数字", index+1)
				}
				number = parsed
			default:
				return nil, fmt.Errorf("第 %d 个元素不是数字", index+1)
			}
		}
		vector[index] = number
	}
	return pgvectorLiteral(vector, typeName), nil
}

func pgvectorLiteral(vector []float64, typeName string) string {
	if typeName == "sparsevec" {
		parts := make([]string, 0, len(vector))
		for index, value := range vector {
			if value == 0 {
				continue
			}
			parts = append(parts, strconv.Itoa(index+1)+":"+strconv.FormatFloat(value, 'g', -1, 64))
		}
		return "{" + strings.Join(parts, ",") + "}/" + strconv.Itoa(len(vector))
	}
	parts := make([]string, len(vector))
	for index, value := range vector {
		parts[index] = strconv.FormatFloat(value, 'g', -1, 64)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func quotePostgresIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func isGraphQLName(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || index > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
