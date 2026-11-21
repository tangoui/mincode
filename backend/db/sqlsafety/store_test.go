package sqlsafety

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// isolateUserData points the machine-key salt (and anything else under the
// user data directory) at a temp dir so tests never touch the real profile.
func isolateUserData(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("APPDATA", filepath.Join(home, "data"))
}

const (
	stmtMarker = "STMTMARK7f3a"
	rowMarker  = "ROWMARK91c2"
)

func bigRows(n int) Rows {
	var r Rows
	for i := 0; i < n; i++ {
		r.Before = append(r.Before, []any{json.Number(fmt.Sprint(9007199254740993 + i)), fmt.Sprintf("%s-before-%d", rowMarker, i), nil})
		r.After = append(r.After, []any{json.Number(fmt.Sprint(9007199254740993 + i)), fmt.Sprintf("%s-after-%d", rowMarker, i), nil})
	}
	return r
}

func TestStoreRoundTripEncrypted(t *testing.T) {
	isolateUserData(t)
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	store := OpenStore(dir, clock.now)

	rows := bigRows(12000)
	r1 := Record{
		ID: NewRecordID(), ConnectionID: "c1", ConnectionName: "prod", DbType: "mysql", Database: "app",
		Table: TableRef{Name: "orders"}, Statement: "UPDATE orders SET note = '" + stmtMarker + "'",
		Kind: KindData, Source: "query", Affected: 12000, Undoable: true, UndoState: "available",
		PrimaryKey: []string{"id"}, Columns: []string{"id", "note", "extra"},
	}
	if err := store.Put(r1, rows); err != nil {
		t.Fatalf("Put: %v", err)
	}

	recDir := filepath.Join(dir, "records", r1.ID)
	rowFiles, _ := filepath.Glob(filepath.Join(recDir, "rows-*.json.enc"))
	if len(rowFiles) != 3 {
		t.Fatalf("rows files = %d, want 3", len(rowFiles))
	}
	files := append(rowFiles, filepath.Join(recDir, "meta.json.enc"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(stmtMarker)) || bytes.Contains(data, []byte(rowMarker)) {
			t.Fatalf("%s contains plaintext", filepath.Base(f))
		}
		if runtime.GOOS != "windows" {
			if info, _ := os.Stat(f); info.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %v, want 0600", filepath.Base(f), info.Mode().Perm())
			}
		}
	}
	if runtime.GOOS != "windows" {
		for _, d := range []string{filepath.Join(dir, "records"), recDir} {
			if info, _ := os.Stat(d); info.Mode().Perm() != 0o700 {
				t.Fatalf("%s mode = %v, want 0700", d, info.Mode().Perm())
			}
		}
	}
	if left, _ := filepath.Glob(filepath.Join(recDir, ".tmp-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}

	gotRec, gotRows, err := store.Get(r1.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantRec := r1
	wantRec.CreatedAt = clock.t
	wantRec.RowFiles = 3
	if !reflect.DeepEqual(gotRec, wantRec) {
		t.Fatalf("record round trip:\n got %+v\nwant %+v", gotRec, wantRec)
	}
	if !reflect.DeepEqual(gotRows, rows) {
		t.Fatalf("rows round trip mismatch (len before %d after %d)", len(gotRows.Before), len(gotRows.After))
	}

	// Eight days later the first Put prunes r1 (default retention 7 days).
	clock.t = clock.t.Add(8 * 24 * time.Hour)
	r2 := Record{ID: NewRecordID(), ConnectionID: "c2", Statement: "DELETE FROM a WHERE id = 1", Kind: KindData, UndoState: "available"}
	if err := store.Put(r2, Rows{Before: [][]any{{json.Number("1")}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(r1.ID); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("expired record still readable: %v", err)
	}
	clock.t = clock.t.Add(time.Second)
	r3 := Record{ID: NewRecordID(), ConnectionID: "c1", Statement: "INSERT INTO a VALUES (2)", Kind: KindInsert, UndoState: "available"}
	if err := store.Put(r3, Rows{Inserted: [][]any{{json.Number("2")}}}); err != nil {
		t.Fatal(err)
	}

	all, err := store.List("", 0)
	if err != nil || len(all) != 2 || all[0].ID != r3.ID || all[1].ID != r2.ID {
		t.Fatalf("List all = %v, %v; want r3, r2", ids(all), err)
	}
	if one, _ := store.List("", 1); len(one) != 1 || one[0].ID != r3.ID {
		t.Fatalf("List limit 1 = %v", ids(one))
	}

	if err := store.MarkUndone(r2.ID, "undone"); err != nil {
		t.Fatal(err)
	}
	if rec, _, _ := store.Get(r2.ID); rec.UndoState != "undone" {
		t.Fatalf("UndoState = %q", rec.UndoState)
	}
	if err := store.MarkUndone(r2.ID, "bogus"); err == nil {
		t.Fatal("MarkUndone accepted an unknown state")
	}

	if err := store.DeleteConnection("c1"); err != nil {
		t.Fatal(err)
	}
	if c1, _ := store.List("c1", 0); len(c1) != 0 {
		t.Fatalf("c1 records left: %v", ids(c1))
	}
	if c2, _ := store.List("c2", 0); len(c2) != 1 || c2[0].ID != r2.ID {
		t.Fatalf("c2 records = %v, want r2", ids(c2))
	}

	clock.t = clock.t.Add(8 * 24 * time.Hour)
	removed, err := store.Prune(DefaultSettings())
	if err != nil || removed != 1 {
		t.Fatalf("Prune = %d, %v; want 1", removed, err)
	}
	if left, _ := store.List("", 0); len(left) != 0 {
		t.Fatalf("records after prune: %v", ids(left))
	}
}

func TestStorePruneTotalBytesDropsOldest(t *testing.T) {
	isolateUserData(t)
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	store := OpenStore(dir, clock.now)
	var recs []Record
	for i := 0; i < 3; i++ {
		rec := Record{ID: NewRecordID(), ConnectionID: "c", UndoState: "available"}
		if err := store.Put(rec, bigRows(200)); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
		clock.t = clock.t.Add(time.Minute)
	}
	size, err := dirSize(filepath.Join(dir, "records", recs[2].ID))
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	s.MaxTotalBytes = size*2 + size/2
	removed, err := store.Prune(s)
	if err != nil || removed != 1 {
		t.Fatalf("Prune = %d, %v; want 1", removed, err)
	}
	left, _ := store.List("", 0)
	if len(left) != 2 || left[0].ID != recs[2].ID || left[1].ID != recs[1].ID {
		t.Fatalf("left = %v, want newest two", ids(left))
	}
}

func TestStoreRejectsUnsafeIDs(t *testing.T) {
	isolateUserData(t)
	store := OpenStore(t.TempDir(), time.Now)
	for _, id := range []string{"", "../x", "a/b", `a\b`, "."} {
		if _, _, err := store.Get(id); !errors.Is(err, ErrRecordNotFound) {
			t.Errorf("Get(%q) err = %v, want ErrRecordNotFound", id, err)
		}
		if id != "" {
			if err := store.Put(Record{ID: id}, Rows{}); err == nil {
				t.Errorf("Put(%q) accepted", id)
			}
		}
	}
}

func TestSettingsValidate(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	for name, mutate := range map[string]func(*Settings){
		"rows 0":           func(s *Settings) { s.HighRiskRows = 0 },
		"rows 1000001":     func(s *Settings) { s.HighRiskRows = 1_000_001 },
		"retention 0":      func(s *Settings) { s.RetentionDays = 0 },
		"retention 31":     func(s *Settings) { s.RetentionDays = 31 },
		"snapshot 0":       func(s *Settings) { s.MaxSnapshotRows = 0 },
		"snapshot 100001":  func(s *Settings) { s.MaxSnapshotRows = 100_001 },
		"bytes under 64M":  func(s *Settings) { s.MaxTotalBytes = 64<<20 - 1 },
		"bytes over 20GiB": func(s *Settings) { s.MaxTotalBytes = 20<<30 + 1 },
	} {
		s := DefaultSettings()
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, s)
		}
	}
	edge := Settings{Enabled: false, HighRiskRows: 1_000_000, RetentionDays: 30, MaxSnapshotRows: 100_000, MaxTotalBytes: 20 << 30}
	if err := edge.Validate(); err != nil {
		t.Fatalf("upper bounds rejected: %v", err)
	}
}

func TestSettingsLoadSave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sql-safety")
	got, err := LoadSettings(dir)
	if err != nil || got != DefaultSettings() {
		t.Fatalf("missing file: %+v, %v; want defaults", got, err)
	}
	want := Settings{Enabled: false, HighRiskRows: 50, RetentionDays: 3, MaxSnapshotRows: 10, MaxTotalBytes: 64 << 20}
	if err := SaveSettings(dir, want); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(filepath.Join(dir, "settings.json")); info.Mode().Perm() != 0o600 {
			t.Fatalf("settings mode = %v", info.Mode().Perm())
		}
	}
	if got, err := LoadSettings(dir); err != nil || got != want {
		t.Fatalf("LoadSettings = %+v, %v; want %+v", got, err, want)
	}
	bad := want
	bad.HighRiskRows = 0
	if err := SaveSettings(dir, bad); err == nil {
		t.Fatal("SaveSettings accepted invalid settings")
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"highRiskRows":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadSettings(dir); err == nil || got != DefaultSettings() {
		t.Fatalf("invalid file: %+v, %v; want defaults and an error", got, err)
	}
}

func ids(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func TestStorePutOverLimitWritesNothing(t *testing.T) {
	isolateUserData(t)
	dir := t.TempDir()
	store := OpenStore(dir, time.Now)
	store.settings = func() Settings { s := DefaultSettings(); s.MaxTotalBytes = 1000; return s }
	if err := store.Put(Record{ID: NewRecordID(), ConnectionID: "c"}, bigRows(200)); !errors.Is(err, ErrOverStoreLimit) {
		t.Fatalf("Put err = %v, want ErrOverStoreLimit", err)
	}
	if left, _ := os.ReadDir(filepath.Join(dir, "records")); len(left) != 0 {
		t.Fatalf("records written despite the limit: %d", len(left))
	}
}

func TestStorePutRejectsCreatedAtBeyondRetention(t *testing.T) {
	isolateUserData(t)
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	store := OpenStore(dir, clock.now)
	rec := Record{ID: NewRecordID(), ConnectionID: "c", CreatedAt: clock.t.Add(-8 * 24 * time.Hour)}
	if err := store.Put(rec, Rows{Before: [][]any{{int64(1)}}}); err == nil {
		t.Fatal("Put accepted a record older than retention")
	}
	if left, _ := os.ReadDir(filepath.Join(dir, "records")); len(left) != 0 {
		t.Fatalf("expired record written: %d", len(left))
	}
}

func TestStorePutNeverPrunesNewRecord(t *testing.T) {
	isolateUserData(t)
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	store := OpenStore(dir, clock.now)
	var older []Record
	for i := 0; i < 2; i++ {
		rec := Record{ID: NewRecordID(), ConnectionID: "c"}
		if err := store.Put(rec, bigRows(200)); err != nil {
			t.Fatal(err)
		}
		older = append(older, rec)
		clock.t = clock.t.Add(time.Minute)
	}
	size, err := dirSize(filepath.Join(dir, "records", older[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	// Room for about one and a half records, and the new record claims to be
	// the oldest: the cap must still remove the older ones, never the new one.
	store.settings = func() Settings { s := DefaultSettings(); s.MaxTotalBytes = size + size/2; return s }
	fresh := Record{ID: NewRecordID(), ConnectionID: "c", CreatedAt: clock.t.Add(-time.Hour)}
	if err := store.Put(fresh, bigRows(200)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, _, err := store.Get(fresh.ID); err != nil {
		t.Fatalf("new record pruned: %v", err)
	}
	if left, _ := store.List("", 0); len(left) != 1 {
		t.Fatalf("left = %v, want only the new record", ids(left))
	}
}

func TestStoreRowValuesRoundTrip(t *testing.T) {
	isolateUserData(t)
	store := OpenStore(t.TempDir(), time.Now)
	zoned := time.Date(2026, 10, 30, 9, 8, 7, 123456789, time.FixedZone("CST", 8*3600))
	rows := Rows{
		Before: [][]any{{
			nil, true, false, int64(1) << 60, int64(-7), uint64(18446744073709551615), 3.25, json.Number("12345678901234567890.0001"),
			"plain 文本", "", string([]byte{0xff, 'a', 0xfe}), []byte{0xff, 0x00}, []byte{}, []byte(nil),
			zoned, time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC), time.Date(2026, 1, 2, 3, 4, 5, 6, time.Local),
		}},
		Inserted: [][]any{{int64(9007199254740993)}},
	}
	rec := Record{ID: NewRecordID(), ConnectionID: "c"}
	if err := store.Put(rec, rows); err != nil {
		t.Fatal(err)
	}
	_, got, err := store.Get(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := got.Before[0][12].([]byte); !ok || b == nil {
		t.Fatalf("[]byte{} came back as %#v, want a non-nil empty []byte", got.Before[0][12])
	}
	if b, ok := got.Before[0][13].([]byte); !ok || b != nil {
		t.Fatalf("[]byte(nil) came back as %#v, want a nil []byte", got.Before[0][13])
	}
	if !reflect.DeepEqual(got, rows) {
		for i := range rows.Before[0] {
			if !reflect.DeepEqual(got.Before[0][i], rows.Before[0][i]) {
				t.Errorf("value %d: got %#v (%T), want %#v (%T)", i, got.Before[0][i], got.Before[0][i], rows.Before[0][i], rows.Before[0][i])
			}
		}
		t.Fatalf("rows round trip mismatch: %#v", got.Inserted)
	}
}

func TestStorePutRejectsUnsupportedValue(t *testing.T) {
	isolateUserData(t)
	dir := t.TempDir()
	store := OpenStore(dir, time.Now)
	type custom struct{ A int }
	err := store.Put(Record{ID: NewRecordID()}, Rows{After: [][]any{{custom{1}}}})
	if err == nil || !strings.Contains(err.Error(), "sqlsafety.custom") {
		t.Fatalf("Put err = %v, want an error naming the type", err)
	}
	if left, _ := os.ReadDir(filepath.Join(dir, "records")); len(left) != 0 {
		t.Fatalf("record written despite bad value")
	}
}

func TestStoreDelete(t *testing.T) {
	isolateUserData(t)
	store := OpenStore(t.TempDir(), time.Now)
	keep, drop := NewRecordID(), NewRecordID()
	for _, id := range []string{keep, drop} {
		if err := store.Put(Record{ID: id, ConnectionID: "c"}, Rows{Before: [][]any{{int64(1)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Delete(drop); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(drop); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("Get deleted record err = %v", err)
	}
	if _, _, err := store.Get(keep); err != nil {
		t.Fatalf("Delete removed another record: %v", err)
	}
	if err := store.Delete(drop); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("second Delete err = %v, want ErrRecordNotFound", err)
	}
	if err := store.Delete("../x"); err == nil {
		t.Fatal("Delete accepted an unsafe id")
	}
}

// DecodeCell reads the typed cells plugin.db.snapshotWrite sends (the same
// shape the store keeps) back to the exact Go values Put accepts.
func TestDecodeCellMatchesStoredValues(t *testing.T) {
	zoned := time.Date(2026, 10, 30, 9, 8, 7, 123456789, time.FixedZone("CST", 8*3600))
	values := []any{
		nil, true, int64(-7), uint64(18446744073709551615), 3.25, json.Number("12345678901234567890.0001"),
		"plain 文本", string([]byte{0xff, 'a'}), []byte{0xff, 0x00}, []byte{}, []byte(nil), zoned,
		time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC),
	}
	for i, v := range values {
		sv, err := encodeValue(v)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(sv)
		got, err := DecodeCell(raw)
		if err != nil || !reflect.DeepEqual(got, v) {
			t.Errorf("value %d: DecodeCell(%s) = %#v, %v; want %#v", i, raw, got, err, v)
		}
	}
	for _, bad := range []string{`{"t":"int","v":"x"}`, `{"t":"weird"}`, `{"t":"str","v":"a","extra":1}`, `[1]`} {
		if _, err := DecodeCell([]byte(bad)); err == nil {
			t.Errorf("DecodeCell(%s) accepted", bad)
		}
	}
}
