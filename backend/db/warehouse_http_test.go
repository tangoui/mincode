package db

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func warehouseTestSession(provider, baseURL string, client *http.Client) *dbSession {
	kind, err := warehouseHTTPKind(provider)
	if err != nil {
		panic(err)
	}
	return &dbSession{
		kind:        kindHTTP,
		httpKind:    kind,
		httpClient:  client,
		httpBaseURL: baseURL,
		httpToken:   "test-token",
		warehouse: &warehouseHTTPConfig{
			provider: provider,
			project:  "test-project",
			catalog:  "test-catalog",
			schema:   "test-schema",
			location: "us-central1",
		},
	}
}

func TestDatabricksQueryPollsAndFetchesChunks(t *testing.T) {
	t.Parallel()
	var statusCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/sql/statements":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["warehouse_id"] != "test-project" || payload["row_limit"] != float64(dbMaxResultRows) {
				t.Fatalf("unexpected payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"statement_id":"statement-1","status":{"state":"PENDING"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/sql/statements/statement-1":
			statusCalls++
			_, _ = w.Write([]byte(`{"statement_id":"statement-1","status":{"state":"SUCCEEDED"},"manifest":{"schema":{"columns":[{"name":"id"},{"name":"name"}]}},"result":{"data_array":[[1,"first"]],"next_chunk_internal_link":"/api/2.0/sql/statements/statement-1/result/chunks/1"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/sql/statements/statement-1/result/chunks/1":
			_, _ = w.Write([]byte(`{"data_array":[[2,"second"]]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := warehouseTestSession("databricks", server.URL, server.Client())
	result, err := databricksQuery(context.Background(), s, "select * from demo")
	if err != nil {
		t.Fatal(err)
	}
	if statusCalls != 1 || result.RowCount != 2 || result.Columns[1] != "name" || result.Rows[1][1] != "second" {
		t.Fatalf("unexpected result: %#v statusCalls=%d", result, statusCalls)
	}
}

func TestBigQueryQueryPollingPaginationAndSchema(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/bigquery/v2/projects/test-project/queries":
			_, _ = w.Write([]byte(`{"jobComplete":false,"jobReference":{"projectId":"test-project","jobId":"job-1","location":"us-central1"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/bigquery/v2/projects/test-project/queries/job-1" && r.URL.Query().Get("pageToken") == "":
			if r.URL.Query().Get("location") != "us-central1" {
				t.Fatalf("missing location: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"jobComplete":true,"jobReference":{"projectId":"test-project","jobId":"job-1","location":"us-central1"},"schema":{"fields":[{"name":"id"}]},"rows":[{"f":[{"v":"1"}]}],"pageToken":"next-page"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/bigquery/v2/projects/test-project/queries/job-1" && r.URL.Query().Get("pageToken") == "next-page":
			_, _ = w.Write([]byte(`{"jobComplete":true,"rows":[{"f":[{"v":"2"}]}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/bigquery/v2/projects/test-project/datasets":
			if r.URL.Query().Get("filter") != "" {
				t.Fatalf("dataset listing must not use a fake location label filter: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"datasets":[{"datasetReference":{"datasetId":"analytics"}}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/bigquery/v2/projects/test-project/datasets/analytics/tables":
			_, _ = w.Write([]byte(`{"tables":[{"tableReference":{"tableId":"events"},"type":"TABLE"},{"tableReference":{"tableId":"daily"},"type":"VIEW"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := warehouseTestSession("bigquery", server.URL, server.Client())
	result, err := bigQueryQuery(context.Background(), s, "select 1")
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || result.Rows[1][0] != "2" {
		t.Fatalf("unexpected query result: %#v", result)
	}
	schema, err := bigQuerySchema(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Databases) != 1 || len(schema.Databases[0].Tables) != 2 || schema.Databases[0].Tables[1].Kind != "view" {
		t.Fatalf("unexpected schema: %#v", schema)
	}
}

func TestDremioQueryUsesBearerAndParsesRows(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v3/sql":
			_, _ = w.Write([]byte(`{"id":"job-1"}`))
		case "/api/v3/job/job-1":
			_, _ = w.Write([]byte(`{"jobState":"COMPLETED"}`))
		case "/api/v3/job/job-1/results":
			if r.URL.Query().Get("offset") != "0" || r.URL.Query().Get("limit") != "1000" {
				t.Fatalf("unexpected pagination: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"rowCount":2,"schema":[{"name":"id"},{"name":"name"}],"rows":[{"id":1,"name":"first"},{"id":2,"name":"second"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := warehouseTestSession("dremio", server.URL, server.Client())
	result, err := dremioQuery(context.Background(), s, "select * from demo")
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || result.Rows[1][1] != "second" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestKylinQuerySupportsBasicAuthAndSnakeCaseEnvelope(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "admin" || password != "secret" {
			t.Fatalf("unexpected basic auth: %q %q %v", username, password, ok)
		}
		if !strings.Contains(r.Header.Get("Accept"), "apache.kylin-v4") {
			t.Fatalf("unexpected accept header: %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"000","data":{"column_metas":[{"name":"ID","label":"id"}],"results":[[1]]}}`))
	}))
	defer server.Close()

	s := warehouseTestSession("kylin", server.URL, server.Client())
	s.httpToken = ""
	s.httpUser = "admin"
	s.httpPass = "secret"
	result, err := kylinQuery(context.Background(), s, "select 1")
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || result.Columns[0] != "id" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestPinotQueryParsesResultTable(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resultTable":{"dataSchema":{"columnNames":["count"]},"rows":[[3]]},"exceptions":[]}`))
	}))
	defer server.Close()

	s := warehouseTestSession("pinot", server.URL, server.Client())
	result, err := pinotQuery(context.Background(), s, "select count(*) from demo")
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || result.Columns[0] != "count" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSnowflakeQueryPollsWithoutForcingOAuthAndFetchesPartitions(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Snowflake-Authorization-Token-Type") != "" {
			t.Fatalf("token type must be auto-detected, got %q", r.Header.Get("X-Snowflake-Authorization-Token-Type"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Fatal("missing User-Agent")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"statementHandle":"handle-1","statementStatusUrl":"/api/v2/statements/handle-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/handle-1" && r.URL.Query().Get("partition") == "":
			_, _ = w.Write([]byte(`{"statementHandle":"handle-1","statementStatusUrl":"/api/v2/statements/handle-1","resultSetMetaData":{"rowType":[{"name":"ID"}],"partitionInfo":[{},{}]},"data":[["1"]],"sqlState":"00000"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/handle-1" && r.URL.Query().Get("partition") == "1":
			_, _ = w.Write([]byte(`{"data":[["2"]],"sqlState":"00000"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := warehouseTestSession("snowflake", server.URL, server.Client())
	result, err := snowflakeQuery(context.Background(), s, "select 1")
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || result.Rows[1][0] != "2" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSnowflakeQueryRejectsPartitionedResultWithoutResultAddress(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resultSetMetaData":{"rowType":[{"name":"ID"}],"partitionInfo":[{},{}]},"data":[["1"]],"sqlState":"00000"}`))
	}))
	defer server.Close()

	s := warehouseTestSession("snowflake", server.URL, server.Client())
	_, err := snowflakeQuery(context.Background(), s, "select 1")
	if err == nil || !strings.Contains(err.Error(), "未提供结果地址") {
		t.Fatalf("expected missing result address error, got %v", err)
	}
}

func TestWarehouseRequestURLRejectsCrossOriginResultLink(t *testing.T) {
	t.Parallel()
	_, err := warehouseRequestURL("https://example.snowflakecomputing.com", "https://evil.example/result")
	if err == nil {
		t.Fatal("expected cross-origin result URL to be rejected")
	}

	resolved, err := warehouseRequestURL("https://example.snowflakecomputing.com", "/api/v2/statements/1?partition=1")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(resolved)
	if err != nil || parsed.Host != "example.snowflakecomputing.com" || parsed.Query().Get("partition") != "1" {
		t.Fatalf("unexpected resolved URL: %q err=%v", resolved, err)
	}
}

func TestDatabricksTableRowsUsesBacktickIdentifiers(t *testing.T) {
	t.Parallel()
	statements := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/sql/statements" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		statement, _ := payload["statement"].(string)
		statements <- statement
		_, _ = w.Write([]byte(`{"statement_id":"s","status":{"state":"SUCCEEDED"},"manifest":{"schema":{"columns":[]}},"result":{"data_array":[]}}`))
	}))
	defer server.Close()

	s := warehouseTestSession("databricks", server.URL, server.Client())
	if _, err := warehouseTableRows(context.Background(), s, "main.sales", "ord`ers", 10, 5); err != nil {
		t.Fatal(err)
	}
	want := "SELECT * FROM `main`.`sales`.`ord``ers` LIMIT 10 OFFSET 5"
	if got := <-statements; got != want {
		t.Fatalf("statement = %q, want %q", got, want)
	}
}

func TestWarehouseIdentifierQuotingByDialect(t *testing.T) {
	t.Parallel()
	if got := quoteBacktickPath(" cat .sch"); got != "`cat`.`sch`" {
		t.Fatalf("backtick path = %q", got)
	}
	if got := quoteBacktickIdent("a`b\"c"); got != "`a``b\"c`" {
		t.Fatalf("backtick ident = %q", got)
	}
	// Dremio / Snowflake 等 ANSI 方言仍使用双引号。
	if got := quoteWarehousePath(`space.fol"der`); got != `"space"."fol""der"` {
		t.Fatalf("double-quote path = %q", got)
	}
}
