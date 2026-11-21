package db

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMilvus serves describe and keyset queries over n entities whose Int64
// keys sit above 2^53, and refuses any offset like Milvus' 16384 window.
func fakeMilvus(n int, shuffle bool) *httptest.Server {
	const base = int64(1) << 60
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		_ = dec.Decode(&p)
		if strings.HasSuffix(r.URL.Path, "/collections/describe") {
			io.WriteString(w, `{"code":0,"data":{"collectionName":"docs","fields":[{"name":"pk","type":"Int64","primaryKey":true},{"name":"vec","type":"FloatVector"}]}}`)
			return
		}
		if _, ok := p["offset"]; ok {
			io.WriteString(w, `{"code":1100,"message":"offset+limit exceeds 16384"}`)
			return
		}
		from := int64(-1)
		if f, _ := p["filter"].(string); f != "" {
			var key int64
			fmt.Sscanf(strings.TrimPrefix(f, "pk > "), "%d", &key)
			from = key - base
		}
		var rows []string
		for i := from + 1; i < int64(n) && len(rows) < milvusBackupPage; i++ {
			rows = append(rows, fmt.Sprintf(`{"pk":%d,"vec":[0.1,0.2]}`, base+i))
		}
		if shuffle && len(rows) > 1 {
			rows[0], rows[1] = rows[1], rows[0]
		}
		io.WriteString(w, `{"code":0,"data":[`+strings.Join(rows, ",")+`]}`)
	}))
}

func milvusArchive(server *httptest.Server) (int64, []byte, error) {
	var data bytes.Buffer
	a := &backupArchive{z: zip.NewWriter(&data)}
	s := &dbSession{kind: kindVector, vector: &vectorSession{provider: "milvus", baseURL: server.URL, client: server.Client(), database: "default", collections: map[string]vectorCollectionRef{}}}
	err := backupVectorObject(context.Background(), a, s, "default", "docs")
	_ = a.z.Close()
	return a.stats.Rows, data.Bytes(), err
}

func TestMilvusBackupBeyondOffsetWindow(t *testing.T) {
	server := fakeMilvus(20500, false)
	defer server.Close()
	rows, data, err := milvusArchive(server)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 20500 {
		t.Fatalf("exported %d entities, want 20500 (beyond the 16384 offset window)", rows)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range reader.File {
		if !strings.HasPrefix(f.Name, "objects/") {
			continue
		}
		r, _ := f.Open()
		raw, _ := io.ReadAll(r)
		r.Close()
		// 2^60 + 20499: the last key must survive without float rounding.
		if !bytes.Contains(raw, []byte(`1152921504606867347`)) {
			t.Fatal("Int64 primary key lost precision")
		}
	}
}

func TestMilvusBackupRefusesUnorderedPages(t *testing.T) {
	server := fakeMilvus(10, true)
	defer server.Close()
	if _, _, err := milvusArchive(server); err == nil || !strings.Contains(err.Error(), "out of primary key order") {
		t.Fatalf("an unordered page must fail the export, got %v", err)
	}
}
