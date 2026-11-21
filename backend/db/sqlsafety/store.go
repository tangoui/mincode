package sqlsafety

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	appcrypto "mincode/app/backend/crypto"
)

// ErrRecordNotFound means no stored record has the given ID.
var ErrRecordNotFound = errors.New("sqlsafety: record not found")

// ErrOverStoreLimit means one record alone is larger than
// Settings.MaxTotalBytes; Put wrote nothing.
var ErrOverStoreLimit = errors.New("sqlsafety: record exceeds the backup store limit")

// rowsPerFile caps the rows of each list (Before/After/Inserted) per file.
const rowsPerFile = 5000

// Dir is the backup store and settings directory of this build, or "" when
// the platform user data directory is unknown.
func Dir() string {
	base := core.UserDataDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, appDataDirectory, "sql-safety")
}

// Record describes one backed-up change. Row data lives separately in Rows.
type Record struct {
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"createdAt"`
	ConnectionID   string    `json:"connectionId"`
	ConnectionName string    `json:"connectionName"`
	DbType         string    `json:"dbType"`
	Database       string    `json:"database"`
	Table          TableRef  `json:"table"`
	Statement      string    `json:"statement"`
	Kind           Kind      `json:"kind"`
	Source         string    `json:"source"` // query grid ai import destructive undo
	Affected       int64     `json:"affected"`
	Undoable       bool      `json:"undoable"`
	UndoReason     Reason    `json:"undoReason,omitempty"`
	UndoState      string    `json:"undoState"` // available undone partial none uncertain
	ParentID       string    `json:"parentId,omitempty"`
	PrimaryKey     []string  `json:"primaryKey,omitempty"`
	Columns        []string  `json:"columns,omitempty"`
	RowFiles       int       `json:"rowFiles"`
	// OwnSchema (plugin sessions): Table is unqualified and was resolved in
	// the driver's own schema, so undo sends its reads and writes with an
	// empty database instead of Database (the database the request named).
	OwnSchema bool `json:"ownSchema,omitempty"`
}

// Rows holds a record's row images, each row ordered like Record.Columns;
// Inserted rows carry only the primary key. Each value is stored with a type
// tag and Get restores it as: nil; bool; int64 (from any signed int type);
// uint64 (from any unsigned int type); float64 (from float32/float64);
// json.Number (exact decimal text, kept verbatim); string (any bytes,
// including invalid UTF-8); []byte (nil stays nil, which drivers bind as
// NULL, and empty stays empty); time.Time (instant, nanoseconds and zone:
// UTC and time.Local come back as themselves, any other zone as a fixed zone
// with the same name and offset). Put rejects any other type.
type Rows struct {
	Before   [][]any `json:"before,omitempty"`
	After    [][]any `json:"after,omitempty"`
	Inserted [][]any `json:"inserted,omitempty"`
}

// uncertain: the change's commit failed ambiguously; the backup is kept
// because the data may have been committed.
var undoStates = map[string]bool{"available": true, "undone": true, "partial": true, "none": true, "uncertain": true}

var recordIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NewRecordID returns a fresh random record ID.
func NewRecordID() string { return randomHex(16) }

// Store keeps encrypted backup records under dir/records/<id>/: meta.json.enc
// plus rows-<n>.json.enc chunks, each file holding
// crypto.EncryptForStorage(JSON). Safe for concurrent use within a process.
type Store struct {
	dir      string
	now      func() time.Time
	settings func() Settings // settings Put applies; tests override
	mu       sync.Mutex
}

func OpenStore(dir string, now func() time.Time) *Store {
	s := &Store{dir: dir, now: now}
	s.settings = func() Settings {
		settings, _ := LoadSettings(s.dir) // falls back to defaults on error
		return settings
	}
	return s
}

func (s *Store) recordsDir() string { return filepath.Join(s.dir, "records") }

func (s *Store) recordDir(id string) string { return filepath.Join(s.recordsDir(), id) }

// Put stores rec and its rows, then prunes with the settings saved in the
// store directory; that prune never removes rec. rec.ID must be set (see
// NewRecordID) and unused; a zero CreatedAt becomes now, and one already
// past retention is rejected; RowFiles is computed. Nothing is written when
// a row value has an unsupported type or the encoded record alone exceeds
// MaxTotalBytes (ErrOverStoreLimit). Rows files are written before the meta
// file, so a record without meta is incomplete and never listed.
func (s *Store) Put(rec Record, rows Rows) error {
	if s.dir == "" {
		return errors.New("sql safety store directory is unavailable")
	}
	if !recordIDPattern.MatchString(rec.ID) {
		return fmt.Errorf("invalid sql safety record id %q", rec.ID)
	}
	settings := s.settings()
	now := s.now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now.UTC().Round(0)
	} else if settings.RetentionDays > 0 && rec.CreatedAt.Before(retentionCutoff(now, settings)) {
		return errors.New("sql safety record is older than the retention period")
	}
	chunks := chunkRows(rows)
	rec.RowFiles = len(chunks)
	files, size, err := sealRecord(rec, chunks)
	if err != nil {
		return err
	}
	if settings.MaxTotalBytes > 0 && size > settings.MaxTotalBytes {
		return ErrOverStoreLimit
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.recordsDir(), 0o700); err != nil {
		return fmt.Errorf("create sql safety records directory: %w", err)
	}
	dir := s.recordDir(rec.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create sql safety record: %w", err)
	}
	for _, f := range files {
		if err := writeFileAtomic(filepath.Join(dir, f.name), f.data); err != nil {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("write sql safety record: %w", err)
		}
	}
	if _, err := s.prune(settings, rec.ID); err != nil {
		slog.Warn("sql safety backup prune failed", "error", err)
	}
	return nil
}

type sealedFile struct {
	name string
	data []byte
}

// sealRecord encodes and encrypts the rows chunks and then the meta file,
// in write order, and returns their total size.
func sealRecord(rec Record, chunks []Rows) ([]sealedFile, int64, error) {
	var files []sealedFile
	var size int64
	add := func(name string, v any) error {
		data, err := seal(v)
		if err != nil {
			return err
		}
		files = append(files, sealedFile{name: name, data: data})
		size += int64(len(data))
		return nil
	}
	for n, chunk := range chunks {
		stored, err := encodeRows(chunk)
		if err != nil {
			return nil, 0, err
		}
		if err := add(fmt.Sprintf("rows-%d.json.enc", n), stored); err != nil {
			return nil, 0, fmt.Errorf("encode sql safety rows: %w", err)
		}
	}
	if err := add("meta.json.enc", rec); err != nil {
		return nil, 0, fmt.Errorf("encode sql safety record: %w", err)
	}
	return files, size, nil
}

func retentionCutoff(now time.Time, settings Settings) time.Time {
	return now.Add(-time.Duration(settings.RetentionDays) * 24 * time.Hour)
}

// chunkRows splits rows by index so each chunk holds at most rowsPerFile
// rows of each list. No rows yield no chunks.
func chunkRows(r Rows) []Rows {
	n := max(len(r.Before), len(r.After), len(r.Inserted))
	var chunks []Rows
	for lo := 0; lo < n; lo += rowsPerFile {
		hi := lo + rowsPerFile
		chunks = append(chunks, Rows{Before: window(r.Before, lo, hi), After: window(r.After, lo, hi), Inserted: window(r.Inserted, lo, hi)})
	}
	return chunks
}

func window(rows [][]any, lo, hi int) [][]any {
	if lo >= len(rows) {
		return nil
	}
	return rows[lo:min(hi, len(rows))]
}

// List returns records newest first without row data, all connections when
// connectionID is "", at most limit when limit > 0.
func (s *Store) List(connectionID string, limit int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.scan()
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		if e.readable && (connectionID == "" || e.rec.ConnectionID == connectionID) {
			out = append(out, e.rec)
		}
	}
	slices.SortStableFunc(out, func(a, b Record) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Get returns a record with all of its rows.
func (s *Store) Get(id string) (Record, Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.readMeta(id)
	if err != nil {
		return Record{}, Rows{}, err
	}
	var rows Rows
	for n := 0; n < rec.RowFiles; n++ {
		var stored storedRows
		if err := readEncrypted(filepath.Join(s.recordDir(id), fmt.Sprintf("rows-%d.json.enc", n)), &stored); err != nil {
			return Record{}, Rows{}, fmt.Errorf("read sql safety rows: %w", err)
		}
		chunk, err := decodeRows(stored)
		if err != nil {
			return Record{}, Rows{}, fmt.Errorf("read sql safety rows: %w", err)
		}
		rows.Before = append(rows.Before, chunk.Before...)
		rows.After = append(rows.After, chunk.After...)
		rows.Inserted = append(rows.Inserted, chunk.Inserted...)
	}
	return rec, rows, nil
}

// MarkUndone sets a record's UndoState (available, undone, partial, none, uncertain).
func (s *Store) MarkUndone(id, state string) error {
	if !undoStates[state] {
		return fmt.Errorf("invalid undo state %q", state)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.readMeta(id)
	if err != nil {
		return err
	}
	rec.UndoState = state
	if err := writeEncrypted(filepath.Join(s.recordDir(id), "meta.json.enc"), rec); err != nil {
		return fmt.Errorf("write sql safety record: %w", err)
	}
	return nil
}

// Delete removes one record, such as one written for a change that was
// then rolled back.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readMeta(id); err != nil {
		return err
	}
	if err := os.RemoveAll(s.recordDir(id)); err != nil {
		return fmt.Errorf("delete sql safety record: %w", err)
	}
	return nil
}

// DeleteConnection removes every readable record of connectionID.
func (s *Store) DeleteConnection(connectionID string) error {
	if connectionID == "" {
		return errors.New("connection id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.scan()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.readable && e.rec.ConnectionID == connectionID {
			if err := os.RemoveAll(e.dir); err != nil {
				return fmt.Errorf("delete sql safety record: %w", err)
			}
		}
	}
	return nil
}

// Prune removes records older than s.RetentionDays, then the oldest records
// until the store fits s.MaxTotalBytes. A rule whose limit is not positive is
// skipped. Records whose meta is missing or unreadable age by modification
// time, so incomplete or undecryptable leftovers are eventually removed.
func (s *Store) Prune(settings Settings) (removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prune(settings, "")
}

// prune applies Prune but never removes the record with ID keep.
func (s *Store) prune(settings Settings, keep string) (int, error) {
	entries, err := s.scan()
	if err != nil {
		return 0, err
	}
	slices.SortStableFunc(entries, func(a, b storeEntry) int { return a.created.Compare(b.created) })
	removed := 0
	var firstErr error
	remove := func(e storeEntry) bool {
		if err := os.RemoveAll(e.dir); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("prune sql safety record: %w", err)
			}
			return false
		}
		removed++
		return true
	}
	var kept []storeEntry
	cutoff := retentionCutoff(s.now(), settings)
	for _, e := range entries {
		if e.id != keep && settings.RetentionDays > 0 && e.created.Before(cutoff) && remove(e) {
			continue
		}
		kept = append(kept, e)
	}
	if settings.MaxTotalBytes > 0 {
		var total int64
		for _, e := range kept {
			total += e.size
		}
		for _, e := range kept {
			if total <= settings.MaxTotalBytes {
				break
			}
			if e.id != keep && remove(e) {
				total -= e.size
			}
		}
	}
	return removed, firstErr
}

type storeEntry struct {
	id       string
	dir      string
	rec      Record
	readable bool
	created  time.Time
	size     int64
}

// scan reads every record directory. A missing records directory is empty.
func (s *Store) scan() ([]storeEntry, error) {
	if s.dir == "" {
		return nil, nil
	}
	dirents, err := os.ReadDir(s.recordsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sql safety records: %w", err)
	}
	var entries []storeEntry
	for _, d := range dirents {
		if !d.IsDir() || !recordIDPattern.MatchString(d.Name()) {
			continue
		}
		e := storeEntry{id: d.Name(), dir: filepath.Join(s.recordsDir(), d.Name())}
		if err := readEncrypted(filepath.Join(e.dir, "meta.json.enc"), &e.rec); err == nil && e.rec.ID == d.Name() {
			e.readable, e.created = true, e.rec.CreatedAt
		} else if info, err := d.Info(); err == nil {
			e.created = info.ModTime()
		}
		e.size, _ = dirSize(e.dir)
		entries = append(entries, e)
	}
	return entries, nil
}

func (s *Store) readMeta(id string) (Record, error) {
	if s.dir == "" || !recordIDPattern.MatchString(id) {
		return Record{}, ErrRecordNotFound
	}
	var rec Record
	err := readEncrypted(filepath.Join(s.recordDir(id), "meta.json.enc"), &rec)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, ErrRecordNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("read sql safety record: %w", err)
	}
	return rec, nil
}

func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func writeEncrypted(path string, v any) error {
	data, err := seal(v)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// seal returns crypto.EncryptForStorage(JSON of v).
func seal(v any) ([]byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	sealed, err := appcrypto.EncryptForStorage(string(plain))
	if err != nil {
		return nil, err
	}
	return []byte(sealed), nil
}

func readEncrypted(path string, v any) error {
	sealed, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	plain, err := appcrypto.DecryptFromStorage(string(sealed))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(plain))
	dec.UseNumber()
	return dec.Decode(v)
}

// writeFileAtomic writes data to a 0600 temp file beside path and renames it
// into place.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// storedRows is the on-disk form of a Rows chunk: every value type-tagged.
type storedRows struct {
	Before   [][]storedValue `json:"before,omitempty"`
	After    [][]storedValue `json:"after,omitempty"`
	Inserted [][]storedValue `json:"inserted,omitempty"`
}

// storedValue is one tagged row value. T is null, bool, int, uint, float,
// num (exact decimal text), str (valid UTF-8), strb (a string that is not
// valid UTF-8, base64), bytes (base64), nbytes (a nil []byte) or time. For time, V is
// "<unix seconds>.<nanoseconds>" and L is utc, local or fixed; a fixed zone
// keeps its name Z and offset O.
type storedValue struct {
	T string `json:"t"`
	V string `json:"v,omitempty"`
	L string `json:"l,omitempty"`
	Z string `json:"z,omitempty"`
	O int    `json:"o,omitempty"`
}

func encodeRows(r Rows) (storedRows, error) {
	var out storedRows
	var err error
	if out.Before, err = encodeRowList(r.Before); err != nil {
		return storedRows{}, err
	}
	if out.After, err = encodeRowList(r.After); err != nil {
		return storedRows{}, err
	}
	if out.Inserted, err = encodeRowList(r.Inserted); err != nil {
		return storedRows{}, err
	}
	return out, nil
}

func decodeRows(r storedRows) (Rows, error) {
	var out Rows
	var err error
	if out.Before, err = decodeRowList(r.Before); err != nil {
		return Rows{}, err
	}
	if out.After, err = decodeRowList(r.After); err != nil {
		return Rows{}, err
	}
	if out.Inserted, err = decodeRowList(r.Inserted); err != nil {
		return Rows{}, err
	}
	return out, nil
}

func encodeRowList(rows [][]any) ([][]storedValue, error) {
	if rows == nil {
		return nil, nil
	}
	out := make([][]storedValue, len(rows))
	for i, row := range rows {
		out[i] = make([]storedValue, len(row))
		for j, v := range row {
			sv, err := encodeValue(v)
			if err != nil {
				return nil, err
			}
			out[i][j] = sv
		}
	}
	return out, nil
}

func decodeRowList(rows [][]storedValue) ([][]any, error) {
	if rows == nil {
		return nil, nil
	}
	out := make([][]any, len(rows))
	for i, row := range rows {
		out[i] = make([]any, len(row))
		for j, sv := range row {
			v, err := decodeValue(sv)
			if err != nil {
				return nil, err
			}
			out[i][j] = v
		}
	}
	return out, nil
}

func encodeValue(v any) (storedValue, error) {
	signed := func(n int64) storedValue { return storedValue{T: "int", V: strconv.FormatInt(n, 10)} }
	unsigned := func(n uint64) storedValue { return storedValue{T: "uint", V: strconv.FormatUint(n, 10)} }
	float := func(f float64) storedValue { return storedValue{T: "float", V: strconv.FormatFloat(f, 'g', -1, 64)} }
	switch x := v.(type) {
	case nil:
		return storedValue{T: "null"}, nil
	case bool:
		return storedValue{T: "bool", V: strconv.FormatBool(x)}, nil
	case int:
		return signed(int64(x)), nil
	case int8:
		return signed(int64(x)), nil
	case int16:
		return signed(int64(x)), nil
	case int32:
		return signed(int64(x)), nil
	case int64:
		return signed(x), nil
	case uint:
		return unsigned(uint64(x)), nil
	case uint8:
		return unsigned(uint64(x)), nil
	case uint16:
		return unsigned(uint64(x)), nil
	case uint32:
		return unsigned(uint64(x)), nil
	case uint64:
		return unsigned(x), nil
	case float32:
		return float(float64(x)), nil
	case float64:
		return float(x), nil
	case json.Number:
		return storedValue{T: "num", V: string(x)}, nil
	case string:
		if utf8.ValidString(x) {
			return storedValue{T: "str", V: x}, nil
		}
		return storedValue{T: "strb", V: base64.StdEncoding.EncodeToString([]byte(x))}, nil
	case []byte:
		if x == nil {
			return storedValue{T: "nbytes"}, nil
		}
		return storedValue{T: "bytes", V: base64.StdEncoding.EncodeToString(x)}, nil
	case time.Time:
		sv := storedValue{T: "time", V: fmt.Sprintf("%d.%09d", x.Unix(), x.Nanosecond())}
		switch x.Location() {
		case time.UTC:
			sv.L = "utc"
		case time.Local:
			sv.L = "local"
		default:
			sv.L = "fixed"
			sv.Z, sv.O = x.Zone()
		}
		return sv, nil
	}
	return storedValue{}, fmt.Errorf("unsupported row value type %T", v)
}

func decodeValue(sv storedValue) (any, error) {
	switch sv.T {
	case "null":
		return nil, nil
	case "bool":
		return strconv.ParseBool(sv.V)
	case "int":
		return strconv.ParseInt(sv.V, 10, 64)
	case "uint":
		return strconv.ParseUint(sv.V, 10, 64)
	case "float":
		return strconv.ParseFloat(sv.V, 64)
	case "num":
		return json.Number(sv.V), nil
	case "str":
		return sv.V, nil
	case "strb":
		b, err := base64.StdEncoding.DecodeString(sv.V)
		return string(b), err
	case "bytes":
		return base64.StdEncoding.DecodeString(sv.V)
	case "nbytes":
		return []byte(nil), nil
	case "time":
		secText, nsecText, ok := strings.Cut(sv.V, ".")
		sec, errSec := strconv.ParseInt(secText, 10, 64)
		nsec, errNsec := strconv.ParseInt(nsecText, 10, 64)
		if !ok || errSec != nil || errNsec != nil {
			return nil, fmt.Errorf("invalid stored time %q", sv.V)
		}
		t := time.Unix(sec, nsec)
		switch sv.L {
		case "utc":
			return t.UTC(), nil
		case "local":
			return t.In(time.Local), nil
		case "fixed":
			return t.In(time.FixedZone(sv.Z, sv.O)), nil
		}
		return nil, fmt.Errorf("invalid stored time zone kind %q", sv.L)
	}
	return nil, fmt.Errorf("unknown stored value tag %q", sv.T)
}

// DecodeCell decodes one typed cell — the stored-value shape, which
// plugin.db.snapshotWrite also uses for before/after rows and returned
// values — into the Go value Put accepts. Unknown members are an error.
func DecodeCell(raw []byte) (any, error) {
	var sv storedValue
	if err := json.Unmarshal(raw, &sv); err != nil {
		return nil, fmt.Errorf("invalid typed cell: %w", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("invalid typed cell: %w", err)
	}
	for k := range probe {
		switch k {
		case "t", "v", "l", "z", "o":
		default:
			return nil, fmt.Errorf("invalid typed cell member %q", k)
		}
	}
	return decodeValue(sv)
}
