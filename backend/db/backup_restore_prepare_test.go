package db

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparePinsReviewedSource(t *testing.T) {
	path, _ := backupFixture(t, "archive")
	prepared, err := stageRestoreSource(context.Background(), path, t.TempDir(), defaultTransferLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prepared.Path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Verify(context.Background()); err == nil {
		t.Fatal("tampered private source accepted")
	}
}
func prepareArchiveFile(t *testing.T, object []byte, duplicate bool) string {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	h := sha256.Sum256(object)
	entry := backupArchiveEntry{Path: "objects/1.jsonl", Database: "main", Object: "items", Encoding: "typed-sql-row", Records: 1, SHA256: hex.EncodeToString(h[:])}
	f, _ := z.Create(entry.Path)
	_, _ = f.Write(object)
	if duplicate {
		f, _ = z.Create(entry.Path)
		_, _ = f.Write(object)
	}
	meta := []byte(`{"database":"main","object":"items","definition":{"ddl":"CREATE TABLE items(id INTEGER)"}}`)
	mh := sha256.Sum256(meta)
	me := backupArchiveEntry{Path: "metadata/1.json", Database: "main", Object: "items", Encoding: "native-metadata-json", SHA256: hex.EncodeToString(mh[:])}
	f, _ = z.Create(me.Path)
	_, _ = f.Write(meta)
	f, _ = z.Create("manifest.json")
	_ = json.NewEncoder(f).Encode(restoreArchiveManifest{Version: 1, Engine: "sqlite", Completed: true, Entries: []backupArchiveEntry{entry}, Metadata: []backupArchiveEntry{me}})
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestPrepareRejectsLateMalformedRecord(t *testing.T) {
	path := prepareArchiveFile(t, []byte("{\"columns\":[{\"name\":\"id\"}]}\n[{\"type\":\"int64\",\"value\":\"oops\"}]\n"), false)
	if p, err := stageRestoreSource(context.Background(), path, t.TempDir(), defaultTransferLimits()); err == nil {
		p.Close()
		t.Fatal("invalid later cell accepted")
	} else if !strings.Contains(err.Error(), "typed_value") {
		t.Fatalf("wrong rejection: %v", err)
	}
}
func TestPrepareArchiveLimits(t *testing.T) {
	path, _ := backupFixture(t, "archive")
	limits := defaultTransferLimits()
	limits.ExpandedBytes = 32
	if p, err := stageRestoreSource(context.Background(), path, t.TempDir(), limits); err == nil {
		p.Close()
		t.Fatal("expanded archive limit ignored")
	}
}
func TestPrepareDuplicateObjects(t *testing.T) {
	path := prepareArchiveFile(t, []byte("{\"columns\":[{\"name\":\"id\"}]}\n[{\"type\":\"int64\",\"value\":\"1\"}]\n"), true)
	if p, err := stageRestoreSource(context.Background(), path, t.TempDir(), defaultTransferLimits()); err == nil {
		p.Close()
		t.Fatal("duplicate archive path accepted")
	}
}
func TestPrepareCancelAndExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path, _ := backupFixture(t, "archive")
	root := t.TempDir()
	if p, err := stageRestoreSource(ctx, path, root, defaultTransferLimits()); err == nil {
		p.Close()
		t.Fatal("cancelled staging succeeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancel leaked staging files: %v %v", entries, err)
	}
}

func TestPrepareValidatesLateExecutableMetadata(t *testing.T) {
	path := prepareArchiveFile(t, []byte("{\"columns\":[{\"name\":\"id\"}]}\n[{\"type\":\"int64\",\"value\":\"1\"}]\n"), false)
	// The same archive is structurally valid; unknown typed metadata must be
	// rejected even when the attacker recomputes its checksum.
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	out := zip.NewWriter(&buffer)
	for _, f := range z.File {
		in, _ := f.Open()
		data, _ := io.ReadAll(in)
		in.Close()
		if f.Name == "metadata/1.json" {
			data = []byte(`{"database":"main","object":"items","definition":{"ddl":42}}`)
		}
		if f.Name == "manifest.json" {
			var m restoreArchiveManifest
			json.Unmarshal(data, &m)
			sum := sha256.Sum256([]byte(`{"database":"main","object":"items","definition":{"ddl":42}}`))
			m.Metadata[0].SHA256 = hex.EncodeToString(sum[:])
			data, _ = json.Marshal(m)
		}
		w, _ := out.Create(f.Name)
		w.Write(data)
	}
	z.Close()
	out.Close()
	os.WriteFile(path, buffer.Bytes(), 0600)
	if p, err := stageRestoreSource(context.Background(), path, t.TempDir(), defaultTransferLimits()); err == nil {
		p.Close()
		t.Fatal("unreadable executable metadata accepted before writes")
	}
}

func TestPrepareRejectsNonFiniteAndInvalidNumberCells(t *testing.T) {
	for _, line := range []string{`[{"type":"float64","value":"NaN"}]`, `[{"type":"number","value":"not-a-number"}]`, `[{"type":"timestamp","value":"not-a-timestamp"}]`} {
		if err := validateTransferRecord("typed-sql-row", []byte(line), 1); err == nil {
			t.Fatalf("invalid value accepted: %s", line)
		}
	}
}

func TestPrepareRejectsDuplicateJSONKeys(t *testing.T) {
	var out struct {
		Key string `json:"key"`
	}
	if err := strictTransferJSON([]byte(`{"key":"reviewed","key":"replaced"}`), &out); err == nil {
		t.Fatal("duplicate security-sensitive keys accepted")
	}
}

func TestPrepareSharedStagingBudget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "source.sql")
	os.WriteFile(source, []byte("SELECT 1;"), 0600)
	if err := os.WriteFile(filepath.Join(root, "existing"), make([]byte, 64), 0600); err != nil {
		t.Fatal(err)
	}
	limits := defaultTransferLimits()
	limits.ExpandedBytes = 64
	if p, err := stageRestoreSource(context.Background(), source, root, limits); err == nil {
		p.Close()
		t.Fatal("shared staging budget ignored")
	}
}

func TestPrepareRejectsCorruptRedisDump(t *testing.T) {
	raw := []byte(`{"key":"aw==","dump":"aW52YWxpZA==","expiresAtMs":"0"}`)
	if err := validateTransferRecord("redis-dump-base64", raw, 0); err == nil {
		t.Fatal("corrupt native payload accepted")
	}
}

func TestPrepareRequiresLegacySQLDefinition(t *testing.T) {
	if err := validateTransferMetadata([]byte(`{}`), "sqlite"); err == nil {
		t.Fatal("missing legacy DDL accepted")
	}
}
