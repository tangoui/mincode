package db

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type transferLimits struct {
	SourceBytes, ExpandedBytes                        int64
	RecordBytes, ManifestBytes, Entries, Preparations int
	Expiry                                            time.Duration
}

func defaultTransferLimits() transferLimits {
	return transferLimits{4 << 30, 16 << 30, 16 << 20, 8 << 20, 100000, 16, 15 * time.Minute}
}

type preparedRestoreSource struct {
	Path   string        `json:"path"`
	Digest string        `json:"digest"`
	Format RestoreFormat `json:"format"`
	Size   int64         `json:"size"`
	file   *os.File
}

func (p *preparedRestoreSource) Close() {
	if p.file != nil {
		_ = p.file.Close()
	}
	_ = os.RemoveAll(filepath.Dir(p.Path))
}
func (p *preparedRestoreSource) Verify(ctx context.Context) error {
	f := p.file
	var err error
	if f == nil {
		f, err = os.Open(p.Path)
		if err != nil {
			return fmt.Errorf("restore_prepared_unavailable")
		}
		p.file = f
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(h, &transferReader{ctx: ctx, r: f, left: p.Size + 1})
	if err != nil {
		return err
	}
	if n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.Digest {
		return fmt.Errorf("restore_prepared_changed")
	}
	_, err = f.Seek(0, io.SeekStart)
	return err
}

// transferReader counts actual bytes (including decompression), not ZIP declarations.
type transferReader struct {
	ctx  context.Context
	r    io.Reader
	left int64
}

func (r *transferReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.left < 0 {
		return 0, fmt.Errorf("transfer_size_limit")
	}
	if int64(len(p)) > r.left+1 {
		p = p[:r.left+1]
	}
	n, err := r.r.Read(p)
	r.left -= int64(n)
	if r.left < 0 {
		return n, fmt.Errorf("transfer_size_limit")
	}
	return n, err
}
func strictTransferJSON(data []byte, out any) error {
	if err := validateTransferJSONKeys(data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("restore_invalid_record")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("restore_trailing_json")
	}
	return nil
}
func stageRestoreSource(ctx context.Context, path, root string, limits transferLimits) (*preparedRestoreSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("restore_source_unavailable")
	}
	defer source.Close()
	before, err := source.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("restore_source_not_regular")
	}
	if before.Size() > limits.SourceBytes {
		return nil, fmt.Errorf("transfer_size_limit")
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	stagingMu.Lock()
	defer stagingMu.Unlock()
	used, err := transferStagingBytes(root)
	if err != nil {
		return nil, err
	}
	if before.Size() > limits.ExpandedBytes-used {
		return nil, fmt.Errorf("transfer_staging_limit")
	}
	limits.ExpandedBytes -= used

	dir, err := os.MkdirTemp(root, "restore-")
	if err != nil {
		return nil, err
	}
	p := &preparedRestoreSource{Path: filepath.Join(dir, "source.sql")}
	ok := false
	defer func() {
		if !ok {
			p.Close()
		}
	}()
	f, err := os.OpenFile(p.Path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	p.file = f
	h := sha256.New()
	p.Size, err = io.Copy(io.MultiWriter(f, h), &transferReader{ctx: ctx, r: source, left: limits.SourceBytes})
	if err != nil {
		return nil, err
	}
	after, err := source.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("restore_source_changed")
	}
	p.Digest = hex.EncodeToString(h.Sum(nil))
	if err = f.Sync(); err != nil {
		return nil, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	head := make([]byte, 4)
	n, _ := f.ReadAt(head, 0)
	if n == 4 && bytes.Equal(head, []byte("PK\x03\x04")) {
		p.Format = restoreArchive
	} else {
		p.Format = restoreSQL
	}
	if p.Format == restoreArchive {
		err = validatePreparedArchive(ctx, p, limits)
	} else {
		err = validatePreparedScript(ctx, p, limits)
	}
	if err != nil {
		return nil, err
	}
	ok = true
	return p, nil
}
func validatePreparedScript(ctx context.Context, p *preparedRestoreSource, limits transferLimits) error {
	r, _, closeFile, err := openSQLScript(p.Path)
	if err != nil {
		return err
	}
	defer closeFile()
	reader := newSQLStatementReader(&transferReader{ctx: ctx, r: r, left: limits.ExpandedBytes})
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := reader.next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("restore_invalid_sql: %w", err)
		}
	}
}
func validatePreparedArchive(ctx context.Context, p *preparedRestoreSource, limits transferLimits) error {
	z, err := zip.NewReader(p.file, p.Size)
	if err != nil {
		return fmt.Errorf("restore_invalid_archive")
	}
	if len(z.File) > limits.Entries {
		return fmt.Errorf("transfer_entry_limit")
	}
	files := map[string]*zip.File{}
	for _, f := range z.File {
		if files[f.Name] != nil {
			return fmt.Errorf("restore_duplicate_entry")
		}
		files[f.Name] = f
	}
	mf := files["manifest.json"]
	if mf == nil {
		return fmt.Errorf("restore_manifest_missing")
	}
	expanded := limits.ExpandedBytes
	read := func(f *zip.File, max int64) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		limit := max
		if expanded < limit {
			limit = expanded
		}
		data, err := io.ReadAll(&transferReader{ctx: ctx, r: r, left: limit})
		expanded -= int64(len(data))
		return data, err
	}
	data, err := read(mf, int64(limits.ManifestBytes))
	if err != nil {
		return err
	}
	// Manifest has descriptive fields outside the restore DTO; enforce EOF but allow them.
	var m restoreArchiveManifest
	d := json.NewDecoder(bytes.NewReader(data))
	if err = d.Decode(&m); err != nil {
		return fmt.Errorf("restore_invalid_manifest")
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("restore_trailing_json")
	}
	if (m.Version != 1 && m.Version != 2) || !m.Completed || m.Engine == "" {
		return fmt.Errorf("restore_invalid_manifest")
	}
	seenPaths := map[string]bool{"manifest.json": true}
	objects := map[string]bool{}
	metadata := map[string]bool{}
	for _, e := range m.Metadata {
		key := e.Database + "\x00" + e.Object
		if metadata[key] || seenPaths[e.Path] || files[e.Path] == nil {
			return fmt.Errorf("restore_duplicate_or_missing_metadata")
		}
		metadata[key] = true
		seenPaths[e.Path] = true
		raw, err := read(files[e.Path], int64(limits.ManifestBytes))
		if err != nil {
			return err
		}
		if err = checkTransferDigest(raw, e.SHA256); err != nil {
			return err
		}
		var doc struct {
			Database   string          `json:"database"`
			Object     string          `json:"object"`
			Definition json.RawMessage `json:"definition"`
		}
		if err = strictTransferJSON(raw, &doc); err != nil {
			return err
		}
		if doc.Database != e.Database || doc.Object != e.Object || len(doc.Definition) == 0 {
			return fmt.Errorf("restore_metadata_mismatch")
		}
		if err := validateTransferMetadata(doc.Definition, m.Engine); err != nil {
			return err
		}
	}
	for _, e := range m.Entries {
		key := e.Database + "\x00" + e.Object
		if objects[key] || seenPaths[e.Path] || files[e.Path] == nil || e.Object == "" || e.Records < 0 {
			return fmt.Errorf("restore_duplicate_or_missing_object")
		}
		objects[key] = true
		seenPaths[e.Path] = true
		if !metadata[key] && e.Encoding != "redis-dump-base64" {
			return fmt.Errorf("restore_metadata_missing")
		}
		r, err := files[e.Path].Open()
		if err != nil {
			return err
		}
		h := sha256.New()
		bounded := &transferReader{ctx: ctx, r: io.TeeReader(r, h), left: expanded}
		scanner := bufio.NewScanner(bounded)
		scanner.Buffer(make([]byte, 4096), limits.RecordBytes)
		records := int64(0)
		columns := 0
		header := false
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if err := ctx.Err(); err != nil {
				r.Close()
				return err
			}
			if e.Encoding == "typed-sql-row" && !header {
				var head struct {
					Columns []backupColumn `json:"columns"`
				}
				if err = strictTransferJSON(line, &head); err != nil {
					r.Close()
					return err
				}
				names := map[string]bool{}
				for _, c := range head.Columns {
					if c.Name == "" || names[c.Name] {
						r.Close()
						return fmt.Errorf("restore_invalid_columns")
					}
					names[c.Name] = true
				}
				columns = len(head.Columns)
				if columns == 0 {
					r.Close()
					return fmt.Errorf("restore_invalid_columns")
				}
				header = true
				continue
			}
			if err = validateTransferRecord(e.Encoding, line, columns); err != nil {
				r.Close()
				return err
			}
			records++
		}
		err = scanner.Err()
		expanded = bounded.left
		_ = r.Close()
		if err != nil {
			return err
		}
		if len(e.SHA256) != 64 || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
			return fmt.Errorf("restore_checksum_mismatch")
		}
		if records != e.Records || (e.Encoding == "typed-sql-row" && !header) {
			return fmt.Errorf("restore_record_count_mismatch")
		}
	}
	// Unknown archive members are rejected except the non-executable recovery text.
	for name, f := range files {
		if !seenPaths[name] {
			if name != "RECOVERY.txt" {
				return fmt.Errorf("restore_unreferenced_entry")
			}
			if _, err := read(f, int64(limits.ManifestBytes)); err != nil {
				return err
			}
		}
	}
	return nil
}
func checkTransferDigest(data []byte, want string) error {
	sum := sha256.Sum256(data)
	if len(want) != 64 || hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("restore_checksum_mismatch")
	}
	return nil
}
func validateTransferRecord(encoding string, line []byte, columns int) error {
	switch encoding {
	case "typed-sql-row":
		var row []backupTypedValue
		if err := strictTransferJSON(line, &row); err != nil {
			return err
		}
		if len(row) != columns {
			return fmt.Errorf("restore_row_width")
		}
		for _, cell := range row {
			if cell.Type == "bool" {
				if _, ok := cell.Value.(bool); !ok {
					return fmt.Errorf("restore_invalid_boolean")
				}
			}
			if cell.Type == "null" && cell.Value != nil {
				return fmt.Errorf("restore_invalid_null")
			}
			if cell.Type != "bool" && cell.Type != "null" {
				if _, ok := cell.Value.(string); !ok {
					return fmt.Errorf("restore_invalid_typed_value")
				}
			}
			value, err := restoreSQLRowValue(cell)
			if err != nil {
				return fmt.Errorf("restore_invalid_typed_value")
			}
			if n, ok := value.(float64); ok && (math.IsNaN(n) || math.IsInf(n, 0)) {
				return fmt.Errorf("restore_invalid_typed_value")
			}
			if cell.Type == "number" && !restoreExactNumberPattern.MatchString(cell.Value.(string)) {
				return fmt.Errorf("restore_invalid_typed_value")
			}
			if cell.Type == "timestamp" {
				if _, err := time.Parse(time.RFC3339Nano, cell.Value.(string)); err != nil {
					return fmt.Errorf("restore_invalid_typed_value")
				}
			}
		}
	case "bson-base64":
		var record struct {
			BSON string `json:"bson"`
		}
		if err := strictTransferJSON(line, &record); err != nil {
			return err
		}
		if _, err := restoreMongoDoc(line); err != nil {
			return fmt.Errorf("restore_invalid_bson")
		}
	case "redis-dump-base64":
		var record restoreRedisRecord
		if err := strictTransferJSON(line, &record); err != nil {
			return err
		}
		if _, err := base64.StdEncoding.DecodeString(record.Key); err != nil {
			return fmt.Errorf("restore_invalid_redis_key")
		}
		dump, err := base64.StdEncoding.DecodeString(record.Dump)
		if err != nil || len(dump) == 0 {
			return fmt.Errorf("restore_invalid_redis_dump")
		}
		if err := validateRedisDump(dump); err != nil {
			return err
		}
		ttl, err := strconv.ParseInt(record.ExpiresAtMs, 10, 64)
		if err != nil || ttl < 0 {
			return fmt.Errorf("restore_invalid_redis_expiry")
		}
	default:
		return fmt.Errorf("restore_encoding_requires_manual_recovery: %s", strings.TrimSpace(encoding))
	}
	return nil
}

func validateTransferMetadata(raw []byte, engine string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("restore_metadata_invalid")
	}
	if engine != "mongodb" && engine != "redis" {
		if _, ok := fields["ddl"]; !ok {
			return fmt.Errorf("restore_metadata_ddl_missing")
		}
	}
	if ddl, ok := fields["ddl"]; ok {
		var text string
		if json.Unmarshal(ddl, &text) != nil {
			var statements backupTableDDL
			if err := json.Unmarshal(ddl, &statements); err != nil || statements.Create == "" {
				return fmt.Errorf("restore_metadata_ddl_invalid")
			}
		} else if strings.TrimSpace(text) == "" {
			return fmt.Errorf("restore_metadata_ddl_invalid")
		}
	}
	if schema, ok := fields["schema"]; ok {
		var descriptor restoreSchema
		if err := strictTransferJSON(schema, &descriptor); err != nil {
			return err
		}
		if _, err := descriptor.Statements(engine, "main", "validated"); err != nil {
			return err
		}
	}
	if definition, ok := fields["definition"]; ok {
		var encoded string
		if err := json.Unmarshal(definition, &encoded); err != nil {
			return fmt.Errorf("restore_metadata_bson_invalid")
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("restore_metadata_bson_invalid")
		}
		if err := bson.Raw(data).Validate(); err != nil {
			return fmt.Errorf("restore_metadata_bson_invalid")
		}
		if indexes, ok := fields["indexes"]; ok {
			var entries []string
			if err := json.Unmarshal(indexes, &entries); err != nil {
				return fmt.Errorf("restore_metadata_indexes_invalid")
			}
			for _, entry := range entries {
				data, err := base64.StdEncoding.DecodeString(entry)
				if err != nil || bson.Raw(data).Validate() != nil {
					return fmt.Errorf("restore_metadata_indexes_invalid")
				}
			}
		}
	}
	return nil
}

var restoreExactNumberPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// Token-walk before decoding rejects duplicate keys rather than accepting the
// last value. Depth is bounded independently of encoding/json's broader limit.
func validateTransferJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return fmt.Errorf("transfer_json_depth")
		}
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("transfer_json_invalid")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return fmt.Errorf("transfer_json_invalid")
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("transfer_json_duplicate_key")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return fmt.Errorf("transfer_json_invalid")
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return fmt.Errorf("transfer_json_invalid")
			}
		default:
			return fmt.Errorf("transfer_json_invalid")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("transfer_json_trailing")
	}
	return nil
}

var stagingMu sync.Mutex

func transferStagingBytes(root string) (int64, error) {
	var used int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("transfer_staging_symlink")
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			used += info.Size()
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return used, err
}
