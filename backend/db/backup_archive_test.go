package db

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupArchiveSQLiteRoundtrip(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE sample(id INTEGER PRIMARY KEY, value BLOB, name TEXT); INSERT INTO sample VALUES(9223372036854775807, X'00FF80', 'quote''中文')`)
	if err != nil {
		t.Fatal(err)
	}
	job := BackupJob{ID: "job", FileNamePrefix: "sample", Format: "archive", Destination: t.TempDir(), Gzip: true, Targets: []BackupTarget{{Database: "main", Tables: []string{"sample"}}}}
	result, err := writeBackupArchive(context.Background(), job, &dbSession{kind: kindSQL, dialect: "sqlite", db: db}, "sqlite", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(result.File)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var object []byte
	var entries []backupArchiveEntry
	for _, f := range archive.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
		if strings.HasPrefix(f.Name, "objects/") {
			object = raw
		}
		if f.Name == "manifest.json" {
			var m struct {
				Completed bool                 `json:"completed"`
				Entries   []backupArchiveEntry `json:"entries"`
			}
			if e = json.Unmarshal(raw, &m); e != nil || !m.Completed {
				t.Fatalf("manifest %s %v", raw, e)
			}
			entries = m.Entries
		}
	}
	if len(entries) != 1 {
		t.Fatalf("entries: %v", entries)
	}
	digest := sha256.Sum256(object)
	if hex.EncodeToString(digest[:]) != entries[0].SHA256 {
		t.Fatal("checksum mismatch")
	}
	lines := bytes.Split(bytes.TrimSpace(object), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("object: %s", object)
	}
	var row []backupTypedValue
	if err = json.Unmarshal(lines[1], &row); err != nil {
		t.Fatal(err)
	}
	if row[0].Value != "9223372036854775807" || row[1].Value != "AP+A" {
		t.Fatalf("values: %+v", row)
	}
	restored, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	_, err = restored.Exec(`CREATE TABLE sample(id INTEGER PRIMARY KEY,value BLOB,name TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := base64.StdEncoding.DecodeString(row[1].Value.(string))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.Exec(`INSERT INTO sample VALUES(?,?,?)`, row[0].Value, blob, row[2].Value)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	var value []byte
	var name string
	if err = restored.QueryRow(`SELECT * FROM sample`).Scan(&id, &value, &name); err != nil {
		t.Fatal(err)
	}
	if id != 9223372036854775807 || !bytes.Equal(blob, value) || name != "quote'中文" {
		t.Fatalf("roundtrip %d %x %s", id, value, name)
	}
}
func TestArchiveFailureDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	_, err := writeBackupArchive(ctx, BackupJob{ID: "cancel", Destination: dir, FileNamePrefix: "cancel", Targets: []BackupTarget{{Database: "main"}}}, &dbSession{kind: kindSQL, dialect: "sqlite"}, "sqlite", time.Now())
	if err == nil {
		t.Fatal("accepted cancelled backup")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left files %v", entries)
	}
}
func TestQdrantBackupRawCursorAndPrecision(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, `{"result":{"config":{"params":{"vectors":{"size":3}}}}}`)
			return
		}
		calls++
		var p map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if calls == 1 {
			io.WriteString(w, `{"result":{"points":[{"id":18446744073709551615,"vector":[0.1,0.2,0.3],"payload":{"n":9223372036854775807}}],"next_page_offset":18446744073709551615}}`)
		} else {
			if string(p["offset"]) != "18446744073709551615" {
				t.Errorf("offset lost precision: %s", p["offset"])
			}
			io.WriteString(w, `{"result":{"points":[],"next_page_offset":null}}`)
		}
	}))
	defer server.Close()
	var data bytes.Buffer
	a := &backupArchive{z: zip.NewWriter(&data)}
	s := &dbSession{kind: kindVector, vector: &vectorSession{provider: "qdrant", baseURL: server.URL, client: server.Client()}}
	if err := backupVectorObject(context.Background(), a, s, "qdrant", "test"); err != nil {
		t.Fatal(err)
	}
	if err := a.z.Close(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || a.stats.Rows != 1 {
		t.Fatalf("calls %d rows %d", calls, a.stats.Rows)
	}
	reader, err := zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range reader.File {
		if strings.HasPrefix(f.Name, "objects/") {
			r, _ := f.Open()
			raw, _ := io.ReadAll(r)
			r.Close()
			if !bytes.Contains(raw, []byte("18446744073709551615")) {
				t.Fatal("numeric precision lost")
			}
		}
	}
}
func TestMeiliExportBeyondPreviewLimit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/documents") {
			io.WriteString(w, `{}`)
			return
		}
		requests++
		offset := 0
		fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
		rows := []map[string]any{}
		for n := offset; n < min(offset+256, 6200); n++ {
			rows = append(rows, map[string]any{"id": n})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": rows, "total": 6200})
	}))
	defer server.Close()
	var data bytes.Buffer
	a := &backupArchive{z: zip.NewWriter(&data)}
	s := &dbSession{kind: kindHTTP, httpKind: httpMeili, httpBaseURL: server.URL, httpClient: server.Client()}
	if err := backupMeili(context.Background(), a, s, "meilisearch", "test"); err != nil {
		t.Fatal(err)
	}
	a.z.Close()
	if a.stats.Rows != 6200 || requests != 25 {
		t.Fatalf("rows=%d calls=%d", a.stats.Rows, requests)
	}
}
func TestArchiveRetentionJobOwnership(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	names := []string{"prefix-aaaaaaaa_20200101-000000.zip", "prefix-bbbbbbbb_20200101-000000.zip", "prefix-aaaaaaaa_20200102-000000.zip", "prefix-aaaaaaaa_20200101-000000.sql"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	n, _ := applyArchiveRetention(dir, "prefix-aaaaaaaa", filepath.Join(dir, names[2]), BackupRetention{KeepLast: 1}, now)
	if n != 1 {
		t.Fatal(n)
	}
	for _, name := range names[1:] {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(name, err)
		}
	}
}
