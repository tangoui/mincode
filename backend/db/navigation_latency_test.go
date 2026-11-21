package db

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVectorConnectionReusesInitialSchemaForNavigation(t *testing.T) {
	tests := []struct {
		provider string
		path     string
		body     string
	}{
		{"qdrant", "/collections", `{"result":{"collections":[{"name":"docs"}]}}`},
		{"milvus", "/v2/vectordb/collections/list", `{"code":0,"data":["docs"]}`},
		{"weaviate", "/v1/schema", `{"classes":[{"class":"docs"}]}`},
		{"pinecone", "/indexes", `{"indexes":[{"name":"docs","dimension":2,"metric":"cosine","status":{"ready":true}}]}`},
		{"chroma", "/api/v2/tenants/default_tenant/databases/default_database/collections", `[{"id":"doc-id","name":"docs"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					http.NotFound(w, r)
					return
				}
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			s, err := buildVectorHTTPSession(&DbConnectionConfig{Host: server.URL}, tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("connect requests = %d, want 1", got)
			}
			info, err := getNavigationSchema(context.Background(), s)
			if err != nil || len(info.Databases) != 1 || len(info.Databases[0].Tables) != 1 || info.Databases[0].Tables[0].Name != "docs" {
				t.Fatalf("navigation schema = %#v, err = %v", info, err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("navigation requests = %d, want 1", got)
			}
			if _, err := getNavigationSchema(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if got := requests.Load(); got != 2 {
				t.Fatalf("refresh requests = %d, want 2", got)
			}
		})
	}
}

func TestInfluxBucketSchemaRetriesThrottledMeasurementsSerially(t *testing.T) {
	var active, throttled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/query" {
			http.NotFound(w, r)
			return
		}
		concurrent := active.Add(1)
		defer active.Add(-1)
		time.Sleep(15 * time.Millisecond)
		if concurrent > 1 {
			throttled.Add(1)
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = fmt.Fprint(w, "result,table,_value\n,,measurement\n")
	}))
	defer server.Close()

	s := &dbSession{httpClient: server.Client(), httpBaseURL: server.URL, httpOrg: "test"}
	buckets := []string{"a", "b", "c", "d"}
	databases := influxBucketSchemas(context.Background(), s, buckets)
	if throttled.Load() == 0 {
		t.Fatal("expected concurrent requests to be throttled")
	}
	for i, db := range databases {
		if db.Name != buckets[i] || len(db.Tables) != 1 || db.Tables[0].Name != "measurement" {
			t.Fatalf("bucket %q: %#v", buckets[i], db)
		}
	}
}

func TestInfluxBucketSchemaBoundsConcurrencyAndRetainsOrder(t *testing.T) {
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/query" {
			http.NotFound(w, r)
			return
		}
		current := active.Add(1)
		for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
		}
		defer active.Add(-1)
		body := make([]byte, 512)
		n, _ := r.Body.Read(body)
		time.Sleep(20 * time.Millisecond)
		if strings.Contains(string(body[:n]), `"bad"`) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		_, _ = fmt.Fprint(w, "result,table,_value\n,,measurement\n")
	}))
	defer server.Close()

	s := &dbSession{httpClient: server.Client(), httpBaseURL: server.URL, httpOrg: "test"}
	buckets := []string{"a", "b", "bad", "c", "d", "e", "f", "g"}
	databases := influxBucketSchemas(context.Background(), s, buckets)
	if got := peak.Load(); got < 2 || got > 4 {
		t.Fatalf("peak concurrent requests = %d, want between 2 and 4", got)
	}
	for i, db := range databases {
		if db.Name != buckets[i] {
			t.Fatalf("database %d = %q, want %q", i, db.Name, buckets[i])
		}
		wantName, wantKind := "measurement", "measurement"
		if db.Name == "bad" {
			wantName, wantKind = "bad", "bucket"
		}
		if len(db.Tables) != 1 || db.Tables[0].Name != wantName || db.Tables[0].Kind != wantKind {
			t.Fatalf("tables for %q = %#v", db.Name, db.Tables)
		}
	}
}
