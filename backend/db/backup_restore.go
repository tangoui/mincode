package db

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
)

// Restoring writes into a database the user picks. It is a separate,
// explicit operation (never scheduled), always confirmed in the UI, and
// refuses targets that already hold the objects it would create unless the
// user explicitly allows writing into existing objects. Nothing is dropped.

// RestoreFormat is the detected kind of backup file.
type RestoreFormat string

const (
	restoreSQL     RestoreFormat = "sql"
	restoreArchive RestoreFormat = "archive"
)

// RestorePlan is the preflight answer shown before the user confirms.
type RestorePlan struct {
	PlanID    string            `json:"planId,omitempty"`
	ExpiresAt int64             `json:"expiresAt,omitempty"`
	Mode      string            `json:"mode,omitempty"`
	Format    RestoreFormat     `json:"format"`
	Engine    string            `json:"engine,omitempty"`
	Supported bool              `json:"supported"`
	Reason    string            `json:"reason,omitempty"`
	Objects   []RestorePlanItem `json:"objects"`
	// Conflicts are target objects that already exist and would receive data.
	Conflicts []string `json:"conflicts"`
	// Statements counts the SQL statements of a script backup.
	Statements int64 `json:"statements"`
	// Destructive reports a script with DROP / TRUNCATE / DELETE statements.
	Destructive bool     `json:"destructive"`
	SizeBytes   int64    `json:"sizeBytes"`
	Warnings    []string `json:"warnings"`
}

type RestorePlanItem struct {
	Database string `json:"database"`
	Object   string `json:"object"`
	Records  int64  `json:"records"`
}

// RestoreResult summarizes a finished restore.
type RestoreResult struct {
	Statements int64    `json:"statements"`
	Objects    int      `json:"objects"`
	Records    int64    `json:"records"`
	DurationMs int64    `json:"durationMs"`
	Warnings   []string `json:"warnings"`
}

// RestoreProgress is emitted as db-backup:restore while a restore runs.
type RestoreProgress struct {
	RestoreID  string `json:"restoreId"`
	Object     string `json:"object,omitempty"`
	Statements int64  `json:"statements"`
	Records    int64  `json:"records"`
	BytesRead  int64  `json:"bytesRead"`
	TotalBytes int64  `json:"totalBytes"`
}

const restoreProgressEvent = "db-backup:restore"

// restoreArchiveManifest is the part of manifest.json restore relies on.
type restoreArchiveManifest struct {
	Version   int                  `json:"version"`
	Engine    string               `json:"engine"`
	Completed bool                 `json:"completed"`
	Entries   []backupArchiveEntry `json:"entries"`
	Metadata  []backupArchiveEntry `json:"metadata"`
}

// detectRestoreFormat decides by content, not only by extension.
func detectRestoreFormat(path string) (RestoreFormat, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开备份文件: %w", err)
	}
	defer f.Close()
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	switch {
	case n >= 4 && bytes.Equal(head, []byte("PK\x03\x04")):
		return restoreArchive, nil
	case n >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return restoreSQL, nil
	}
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".sql") {
		return restoreSQL, nil
	}
	return "", fmt.Errorf("无法识别的备份文件：需要 .sql、.sql.gz 或 MinCode 归档 .zip")
}

// openSQLScript returns a reader over a plain or gzip SQL script and a
// counter of compressed bytes read (for progress).
func openSQLScript(path string) (io.Reader, *countingReader, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	counter := &countingReader{r: f}
	br := bufio.NewReader(counter)
	head, _ := br.Peek(2)
	var r io.Reader = br
	closeAll := func() { _ = f.Close() }
	if len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			_ = f.Close()
			return nil, nil, nil, fmt.Errorf("解压备份文件: %w", err)
		}
		r = gz
		closeAll = func() { _ = gz.Close(); _ = f.Close() }
	}
	return r, counter, closeAll, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// openRestoreArchive opens a MinCode archive and verifies its completion
// marker and every checksum before anything is written.
func openRestoreArchive(path string) (*zip.ReadCloser, restoreArchiveManifest, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, restoreArchiveManifest{}, fmt.Errorf("打开归档: %w", err)
	}
	fail := func(err error) (*zip.ReadCloser, restoreArchiveManifest, error) {
		_ = z.Close()
		return nil, restoreArchiveManifest{}, err
	}
	files := map[string]*zip.File{}
	for _, f := range z.File {
		files[f.Name] = f
	}
	mf := files["manifest.json"]
	if mf == nil {
		return fail(fmt.Errorf("归档缺少 manifest.json，不是 MinCode 备份"))
	}
	var m restoreArchiveManifest
	if err := readZipJSON(mf, &m); err != nil {
		return fail(fmt.Errorf("读取清单: %w", err))
	}
	if m.Version != 1 && m.Version != 2 {
		return fail(fmt.Errorf("不支持的归档版本 %d", m.Version))
	}
	if !m.Completed {
		return fail(fmt.Errorf("归档未标记为完成，不能恢复"))
	}
	for _, entry := range append(append([]backupArchiveEntry{}, m.Entries...), m.Metadata...) {
		f := files[entry.Path]
		if f == nil {
			return fail(fmt.Errorf("归档缺少 %s", entry.Path))
		}
		sum, err := zipSHA256(f)
		if err != nil {
			return fail(err)
		}
		if sum != entry.SHA256 {
			return fail(fmt.Errorf("%s 校验和不一致，归档已损坏", entry.Path))
		}
	}
	return z, m, nil
}

func readZipJSON(f *zip.File, v any) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	return json.NewDecoder(r).Decode(v)
}

func zipSHA256(f *zip.File) (string, error) {
	r, err := f.Open()
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("读取 %s: %w", f.Name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// restoreDDL returns the CREATE statements stored for one archive object.
func restoreDDL(z *zip.Reader, entry backupArchiveEntry, metadata []backupArchiveEntry) ([]string, error) {
	for _, m := range metadata {
		if m.Database != entry.Database || m.Object != entry.Object {
			continue
		}
		for _, f := range z.File {
			if f.Name != m.Path {
				continue
			}
			var doc struct {
				Definition struct {
					DDL json.RawMessage `json:"ddl"`
				} `json:"definition"`
			}
			if err := readZipJSON(f, &doc); err != nil {
				return nil, err
			}
			// The DDL is either one string or a backupTableDDL object.
			var text string
			if json.Unmarshal(doc.Definition.DDL, &text) == nil {
				return []string{text}, nil
			}
			var ddl struct {
				Create string   `json:"Create"`
				After  []string `json:"After"`
			}
			if err := json.Unmarshal(doc.Definition.DDL, &ddl); err != nil || ddl.Create == "" {
				return nil, fmt.Errorf("%s.%s 的结构定义无法读取", entry.Database, entry.Object)
			}
			return append([]string{ddl.Create}, ddl.After...), nil
		}
	}
	return nil, fmt.Errorf("%s.%s 缺少结构定义", entry.Database, entry.Object)
}

// restoreSQLRowValue converts one typed archive cell back to a driver value.
func restoreSQLRowValue(cell backupTypedValue) (any, error) {
	s, _ := cell.Value.(string)
	switch cell.Type {
	case "null":
		return nil, nil
	case "binary-base64":
		return base64.StdEncoding.DecodeString(s)
	case "string", "timestamp", "number":
		// Timestamps and exact numbers are bound as text; the server parses
		// them with the column type, which keeps every digit.
		return s, nil
	case "bool":
		b, _ := cell.Value.(bool)
		return b, nil
	case "int", "int8", "int16", "int32", "int64":
		return strconv.ParseInt(s, 10, 64)
	case "uint", "uint8", "uint16", "uint32", "uint64":
		return strconv.ParseUint(s, 10, 64)
	case "float32", "float64":
		return strconv.ParseFloat(s, 64)
	}
	return nil, fmt.Errorf("未知的值类型 %q", cell.Type)
}

// restoreRedisRecord is one archived Redis key.
type restoreRedisRecord struct {
	Key         string `json:"key"`
	Dump        string `json:"dump"`
	ExpiresAtMs string `json:"expiresAtMs"`
}

// restoreMongoDoc decodes one archived BSON document.
func restoreMongoDoc(line []byte) (bson.Raw, error) {
	var rec struct {
		BSON string `json:"bson"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(rec.BSON)
	if err != nil {
		return nil, err
	}
	if err := bson.Raw(raw).Validate(); err != nil {
		return nil, fmt.Errorf("BSON 文档损坏: %w", err)
	}
	return bson.Raw(raw), nil
}

var errRestoreCancelled = errors.New("恢复已取消")
