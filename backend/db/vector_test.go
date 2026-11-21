package db

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func vectorTestSession(provider, baseURL string, client *http.Client) *dbSession {
	return &dbSession{
		kind: kindVector,
		vector: &vectorSession{
			provider: provider, client: client, baseURL: baseURL,
			database: "default_database", tenant: "default_tenant",
			collections: map[string]vectorCollectionRef{},
		},
	}
}

func TestQdrantVectorSchemaPreviewAndSearch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/collections":
			_, _ = w.Write([]byte(`{"result":{"collections":[{"name":"docs"}]}}`))
		case "/collections/docs/points/scroll":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if cursor, ok := payload["offset"]; ok && cursor != "cursor-2" {
				t.Fatalf("unexpected qdrant cursor: %#v", cursor)
			}
			_, _ = w.Write([]byte(`{"result":{"points":[{"id":1,"payload":{"title":"A"},"vector":[0.1,0.2]}],"next_page_offset":"cursor-2"}}`))
		case "/collections/docs/points/query":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if _, ok := payload["query"]; !ok {
				t.Fatalf("missing qdrant query payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"result":{"points":[{"id":1,"score":0.98,"payload":{"title":"A"}}]}}`))
		case "/collections/docs":
			_, _ = w.Write([]byte(`{"result":{"status":"green","points_count":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := vectorTestSession("qdrant", server.URL, server.Client())
	schema, err := vectorSchema(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got := schema.Databases[0].Tables[0].Name; got != "docs" {
		t.Fatalf("unexpected collection %q", got)
	}
	preview, err := vectorTableRows(context.Background(), s, "qdrant", "docs", 20, 0)
	if err != nil || preview.RowCount != 1 || preview.NextPageToken == nil || *preview.NextPageToken != `"cursor-2"` {
		t.Fatalf("preview failed: result=%#v err=%v", preview, err)
	}
	if _, err := vectorTableRowsPage(context.Background(), s, "qdrant", "docs", 20, 0, *preview.NextPageToken, ""); err != nil {
		t.Fatalf("cursor preview failed: %v", err)
	}
	result, err := vectorExecuteQuery(context.Background(), s,
		`{"collection":"docs","vector":[0.1,0.2],"limit":5}`, nil)
	if err != nil || result.RowCount != 1 {
		t.Fatalf("query failed: result=%#v err=%v", result, err)
	}
	definition, err := vectorCollectionDefinition(context.Background(), s, "qdrant", "docs")
	if err != nil || !strings.Contains(definition, "points_count") {
		t.Fatalf("definition failed: %q err=%v", definition, err)
	}
}

func TestMilvusVectorSchemaAndSearch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/vectordb/collections/list":
			_, _ = w.Write([]byte(`{"code":0,"data":["articles"]}`))
		case "/v2/vectordb/entities/search":
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":7,"distance":0.12,"title":"hello"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := vectorTestSession("milvus", server.URL, server.Client())
	s.vector.database = "default"
	schema, err := vectorSchema(context.Background(), s)
	if err != nil || schema.Databases[0].Tables[0].Name != "articles" {
		t.Fatalf("schema=%#v err=%v", schema, err)
	}
	result, err := vectorExecuteQuery(context.Background(), s,
		`{"collection":"articles","vector":[1,2],"filter":"id > 0"}`, nil)
	if err != nil || result.RowCount != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestWeaviateVectorSchemaPreviewAndSearch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/schema":
			_, _ = w.Write([]byte(`{"classes":[{"class":"Article"}]}`))
		case "/v1/schema/Article":
			_, _ = w.Write([]byte(`{"class":"Article","properties":[{"name":"title"}]}`))
		case "/v1/objects":
			_, _ = w.Write([]byte(`{"objects":[{"id":"a","properties":{"title":"Hello"},"vector":[0.1]}]}`))
		case "/v1/graphql":
			var payload struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(payload.Query, `targetVectors: ["content"]`) {
				t.Fatalf("missing named vector target: %s", payload.Query)
			}
			_, _ = w.Write([]byte(`{"data":{"Get":{"Article":[{"title":"Hello","_additional":{"id":"a","distance":0.1}}]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := vectorTestSession("weaviate", server.URL, server.Client())
	schema, err := vectorSchema(context.Background(), s)
	if err != nil || schema.Databases[0].Tables[0].Name != "Article" {
		t.Fatalf("schema=%#v err=%v", schema, err)
	}
	preview, err := vectorTableRows(context.Background(), s, "weaviate", "Article", 10, 0)
	if err != nil || preview.RowCount != 1 {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	result, err := vectorExecuteQuery(context.Background(), s,
		`{"collection":"Article","vector":[0.1],"targetVector":"content"}`, nil)
	if err != nil || result.RowCount != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestPineconeVectorSchemaAndSearch(t *testing.T) {
	t.Parallel()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/indexes":
			_, _ = w.Write([]byte(`{"indexes":[{"name":"docs","dimension":2,"metric":"cosine","host":"` + server.URL + `","status":{"ready":true}}]}`))
		case "/vectors/list":
			if r.Method != http.MethodGet {
				t.Fatalf("expected Pinecone list GET, got %s", r.Method)
			}
			if namespace := r.URL.Query().Get("namespace"); namespace != "tenant-a" {
				t.Fatalf("unexpected Pinecone namespace: %q", namespace)
			}
			if token := r.URL.Query().Get("paginationToken"); token != "" && token != "page-2" {
				t.Fatalf("unexpected Pinecone page token: %q", token)
			}
			_, _ = w.Write([]byte(`{"vectors":[{"id":"a"}],"pagination":{"next":"page-2"}}`))
		case "/vectors/fetch":
			if r.Method != http.MethodGet {
				t.Fatalf("expected Pinecone fetch GET, got %s", r.Method)
			}
			if ids := r.URL.Query()["ids"]; len(ids) != 1 || ids[0] != "a" {
				t.Fatalf("unexpected Pinecone fetch ids: %#v", ids)
			}
			if namespace := r.URL.Query().Get("namespace"); namespace != "tenant-a" {
				t.Fatalf("unexpected Pinecone fetch namespace: %q", namespace)
			}
			_, _ = w.Write([]byte(`{"vectors":{"a":{"id":"a","values":[0.1,0.2],"metadata":{"title":"Hello"}}}}`))
		case "/query":
			_, _ = w.Write([]byte(`{"matches":[{"id":"a","score":0.99,"metadata":{"title":"Hello"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := vectorTestSession("pinecone", server.URL, server.Client())
	s.vector.database = ""
	schema, err := vectorSchema(context.Background(), s)
	if err != nil || schema.Databases[0].Tables[0].Name != "docs" {
		t.Fatalf("schema=%#v err=%v", schema, err)
	}
	preview, err := vectorTableRowsPage(context.Background(), s, "pinecone", "docs", 10, 0, "", "tenant-a")
	if err != nil || preview.RowCount != 1 || preview.NextPageToken == nil || *preview.NextPageToken != "page-2" {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	if _, err := vectorTableRowsPage(context.Background(), s, "pinecone", "docs", 10, 0, *preview.NextPageToken, "tenant-a"); err != nil {
		t.Fatalf("pagination preview failed: %v", err)
	}
	result, err := vectorExecuteQuery(context.Background(), s,
		`{"collection":"docs","vector":[0.1,0.2]}`, nil)
	if err != nil || result.RowCount != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestChromaVectorSchemaPreviewAndSearch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/tenants/default_tenant/databases/default_database/collections":
			_, _ = w.Write([]byte(`[{"id":"cid","name":"docs"}]`))
		case "/api/v2/tenants/default_tenant/databases/default_database/collections/cid/get":
			_, _ = w.Write([]byte(`{"ids":["a"],"embeddings":[[0.1]],"metadatas":[{"title":"Hello"}],"documents":["body"]}`))
		case "/api/v2/tenants/default_tenant/databases/default_database/collections/cid/query":
			_, _ = w.Write([]byte(`{"ids":[["a"]],"distances":[[0.1]],"metadatas":[[{"title":"Hello"}]],"documents":[["body"]]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s := vectorTestSession("chroma", server.URL, server.Client())
	schema, err := vectorSchema(context.Background(), s)
	if err != nil || schema.Databases[0].Tables[0].Name != "docs" {
		t.Fatalf("schema=%#v err=%v", schema, err)
	}
	preview, err := vectorTableRows(context.Background(), s, "default_database", "docs", 10, 0)
	if err != nil || preview.RowCount != 1 {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	result, err := vectorExecuteQuery(context.Background(), s,
		`{"collection":"docs","vector":[0.1]}`, nil)
	if err != nil || result.RowCount != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestMilvusResponseErrorIsRejected(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1800,"message":"collection not found"}`))
	}))
	defer server.Close()

	s := vectorTestSession("milvus", server.URL, server.Client())
	_, err := vectorSchema(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "collection not found") {
		t.Fatalf("expected Milvus application error, got %v", err)
	}
}

func TestWeaviateSearchRejectsInvalidCollectionName(t *testing.T) {
	t.Parallel()
	s := vectorTestSession("weaviate", "http://127.0.0.1:1", http.DefaultClient)
	_, err := vectorExecuteQuery(context.Background(), s,
		`{"collection":"Article) { injected }","vector":[0.1]}`, nil)
	if err == nil || !strings.Contains(err.Error(), "名称不合法") {
		t.Fatalf("expected invalid collection error, got %v", err)
	}
}

func TestPgvectorMutationValueFormatsJSONArrays(t *testing.T) {
	t.Parallel()
	value, err := pgvectorMutationValueForType([]any{0.1, 0.2, -3.0}, "vector(3)")
	if err != nil || value != "[0.1,0.2,-3]" {
		t.Fatalf("value=%#v err=%v", value, err)
	}
	if _, err := pgvectorMutationValueForType([]any{0.1, "bad"}, "vector(2)"); err == nil {
		t.Fatal("expected invalid vector element error")
	}
	plain, err := pgvectorMutationValueForType("unchanged", "text")
	if err != nil || plain != "unchanged" {
		t.Fatalf("plain=%#v err=%v", plain, err)
	}
}

func TestPgvectorLiteralUsesColumnStorageType(t *testing.T) {
	t.Parallel()
	if got := pgvectorLiteral([]float64{0.1, 0, -2}, "vector"); got != "[0.1,0,-2]" {
		t.Fatalf("unexpected vector literal %q", got)
	}
	if got := pgvectorLiteral([]float64{0.1, 0, -2}, "halfvec"); got != "[0.1,0,-2]" {
		t.Fatalf("unexpected halfvec literal %q", got)
	}
	if got := pgvectorLiteral([]float64{0.1, 0, -2}, "sparsevec"); got != "{1:0.1,3:-2}/3" {
		t.Fatalf("unexpected sparsevec literal %q", got)
	}
}

func TestVectorCollectionCreateDeleteEndpoints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider   string
		createPath string
		createVerb string
		deletePath string
		deleteVerb string
	}{
		{provider: "qdrant", createPath: "/collections/docs", createVerb: http.MethodPut, deletePath: "/collections/docs", deleteVerb: http.MethodDelete},
		{provider: "milvus", createPath: "/v2/vectordb/collections/create", createVerb: http.MethodPost, deletePath: "/v2/vectordb/collections/drop", deleteVerb: http.MethodPost},
		{provider: "weaviate", createPath: "/v1/schema", createVerb: http.MethodPost, deletePath: "/v1/schema/docs", deleteVerb: http.MethodDelete},
		{provider: "chroma", createPath: "/api/v2/tenants/default_tenant/databases/default_database/collections", createVerb: http.MethodPost, deletePath: "/api/v2/tenants/default_tenant/databases/default_database/collections/cid", deleteVerb: http.MethodDelete},
	}
	for _, test := range tests {
		test := test
		t.Run(test.provider, func(t *testing.T) {
			t.Parallel()
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				if test.provider == "milvus" {
					_, _ = w.Write([]byte(`{"code":0}`))
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			s := vectorTestSession(test.provider, server.URL, server.Client())
			if test.provider == "chroma" {
				s.vector.rememberCollection("docs", vectorCollectionRef{ID: "cid"})
			}
			if err := vectorCreateCollection(context.Background(), s, vectorCreateCollectionRequest{
				Name: "docs", Dimension: 1536, Metric: "cosine", VectorName: "content",
			}); err != nil {
				t.Fatalf("create failed: %v", err)
			}
			if err := vectorDeleteCollection(context.Background(), s, "docs"); err != nil {
				t.Fatalf("delete failed: %v", err)
			}
			want := []string{test.createVerb + " " + test.createPath, test.deleteVerb + " " + test.deletePath}
			if len(requests) != len(want) || requests[0] != want[0] || requests[1] != want[1] {
				t.Fatalf("requests=%#v want=%#v", requests, want)
			}
		})
	}
}

func TestPineconeIndexCreateDeleteEndpoints(t *testing.T) {
	t.Parallel()
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("X-Pinecone-API-Version"); got != pineconeAPIVersion {
			t.Errorf("unexpected Pinecone API version %q", got)
		}
		if got := r.Header.Get("Api-Key"); got != "test-key" {
			t.Errorf("unexpected Pinecone API key %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var payload struct {
				Name      string `json:"name"`
				Dimension int    `json:"dimension"`
				Metric    string `json:"metric"`
				Spec      struct {
					Serverless struct {
						Cloud  string `json:"cloud"`
						Region string `json:"region"`
					} `json:"serverless"`
				} `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode create payload: %v", err)
			} else if payload.Name != "docs" || payload.Dimension != 1536 || payload.Metric != "cosine" ||
				payload.Spec.Serverless.Cloud != "aws" || payload.Spec.Serverless.Region != "us-east-1" {
				t.Errorf("unexpected create payload: %#v", payload)
			}
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return server.Client().Transport.RoundTrip(clone)
	})}
	s := vectorTestSession("pinecone", "https://api.pinecone.io", client)
	s.vector.token = "test-key"

	if err := vectorCreateCollection(context.Background(), s, vectorCreateCollectionRequest{
		Name: "docs", Dimension: 1536, Metric: "cosine", Cloud: "aws", Region: "us-east-1",
	}); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if err := vectorDeleteCollection(context.Background(), s, "docs"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	want := []string{"POST /indexes", "DELETE /indexes/docs"}
	if len(requests) != len(want) || requests[0] != want[0] || requests[1] != want[1] {
		t.Fatalf("requests=%#v want=%#v", requests, want)
	}
}

func TestQdrantVectorRecordCRUD(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections/docs/points/42":
			_, _ = w.Write([]byte(`{"result":{"id":42,"vector":[0.1,0.2],"payload":{"title":"before"}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/collections/docs/points":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			points, _ := payload["points"].([]any)
			if len(points) != 1 {
				t.Fatalf("unexpected Qdrant upsert payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/collections/docs/points/delete":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := vectorTestSession("qdrant", server.URL, server.Client())
	ctx := context.Background()
	if _, err := vectorInsertRow(ctx, s, "docs", map[string]any{
		"id": 42, "vector": []any{0.1, 0.2}, "payload": map[string]any{"title": "created"},
	}, ""); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if _, err := vectorUpdateCell(ctx, s, "docs", map[string]any{"id": 42}, "payload", map[string]any{"title": "after"}, ""); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if _, err := vectorDeleteRow(ctx, s, "docs", map[string]any{"id": 42}, ""); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	want := []string{
		"PUT /collections/docs/points?wait=true",
		"GET /collections/docs/points/42",
		"PUT /collections/docs/points?wait=true",
		"POST /collections/docs/points/delete?wait=true",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests=%#v want=%#v", requests, want)
	}
}

func TestMilvusVectorRecordCRUD(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/vectordb/entities/query":
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":42,"vector":[0.1,0.2],"title":"before"}]}`))
		case "/v2/vectordb/entities/upsert", "/v2/vectordb/entities/delete":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["collectionName"] != "docs" || payload["dbName"] != "default_database" {
				t.Fatalf("unexpected Milvus payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := vectorTestSession("milvus", server.URL, server.Client())
	ctx := context.Background()
	if _, err := vectorInsertRow(ctx, s, "docs", map[string]any{"id": 42, "vector": []any{0.1, 0.2}, "title": "created"}, ""); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if _, err := vectorUpdateCell(ctx, s, "docs", map[string]any{"id": 42}, "title", "after", ""); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if _, err := vectorDeleteRow(ctx, s, "docs", map[string]any{"id": 42}, ""); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	want := []string{
		"POST /v2/vectordb/entities/upsert",
		"POST /v2/vectordb/entities/query",
		"POST /v2/vectordb/entities/upsert",
		"POST /v2/vectordb/entities/delete",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests=%#v want=%#v", requests, want)
	}
}

func TestWeaviateVectorRecordCRUD(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000009001"
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/objects/docs/"+id:
			_, _ = w.Write([]byte(`{"class":"docs","id":"` + id + `","properties":{"title":"before"},"vector":[0.1,0.2]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/objects",
			r.Method == http.MethodPut && r.URL.Path == "/v1/objects/docs/"+id:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["class"] != "docs" || payload["id"] != id {
				t.Fatalf("unexpected Weaviate payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/objects/docs/"+id:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := vectorTestSession("weaviate", server.URL, server.Client())
	ctx := context.Background()
	fields := map[string]any{"id": id, "vector": []any{0.1, 0.2}, "title": "created"}
	if _, err := vectorInsertRow(ctx, s, "docs", fields, ""); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if _, err := vectorUpdateCell(ctx, s, "docs", map[string]any{"id": id}, "title", "after", ""); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if _, err := vectorDeleteRow(ctx, s, "docs", map[string]any{"id": id}, ""); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	want := []string{
		"POST /v1/objects",
		"GET /v1/objects/docs/" + id + "?include=vector",
		"PUT /v1/objects/docs/" + id,
		"DELETE /v1/objects/docs/" + id,
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests=%#v want=%#v", requests, want)
	}
}

func TestPineconeVectorRecordCRUDPreservesNamespace(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/vectors/fetch":
			if r.URL.Query().Get("namespace") != "tenant-a" {
				t.Fatalf("missing Pinecone fetch namespace: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"vectors":{"42":{"id":"42","values":[0.1,0.2],"metadata":{"title":"before"}}}}`))
		case r.Method == http.MethodPost && (r.URL.Path == "/vectors/upsert" || r.URL.Path == "/vectors/delete"):
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["namespace"] != "tenant-a" {
				t.Fatalf("missing Pinecone mutation namespace: %#v", payload)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := vectorTestSession("pinecone", server.URL, server.Client())
	s.vector.rememberCollection("docs", vectorCollectionRef{Host: server.URL})
	ctx := context.Background()
	if _, err := vectorInsertRow(ctx, s, "docs", map[string]any{"id": "42", "values": []any{0.1, 0.2}, "metadata": map[string]any{"title": "created"}}, "tenant-a"); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if _, err := vectorUpdateCell(ctx, s, "docs", map[string]any{"id": "42"}, "metadata", map[string]any{"title": "after"}, "tenant-a"); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if _, err := vectorDeleteRow(ctx, s, "docs", map[string]any{"id": "42"}, "tenant-a"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	want := []string{
		"POST /vectors/upsert",
		"GET /vectors/fetch?ids=42&namespace=tenant-a",
		"POST /vectors/upsert",
		"POST /vectors/delete",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests=%#v want=%#v", requests, want)
	}
}

func TestChromaVectorRecordCRUDUsesV2CollectionPath(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/get"):
			_, _ = w.Write([]byte(`{"ids":["42"],"embeddings":[[0.1,0.2]],"metadatas":[{"title":"before"}],"documents":["body"]}`))
		case strings.HasSuffix(r.URL.Path, "/upsert"), strings.HasSuffix(r.URL.Path, "/delete"):
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := vectorTestSession("chroma", server.URL, server.Client())
	s.vector.rememberCollection("docs", vectorCollectionRef{ID: "cid"})
	ctx := context.Background()
	if _, err := vectorInsertRow(ctx, s, "docs", map[string]any{"id": "42", "vector": []any{0.1, 0.2}, "document": "created", "metadata": map[string]any{"title": "created"}}, ""); err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if _, err := vectorUpdateCell(ctx, s, "docs", map[string]any{"id": "42"}, "document", "after", ""); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if _, err := vectorDeleteRow(ctx, s, "docs", map[string]any{"id": "42"}, ""); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	base := "/api/v2/tenants/default_tenant/databases/default_database/collections/cid"
	want := []string{
		"POST " + base + "/upsert",
		"POST " + base + "/get",
		"POST " + base + "/upsert",
		"POST " + base + "/delete",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests=%#v want=%#v", requests, want)
	}
}
