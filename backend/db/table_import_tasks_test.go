package db

import (
	"context"
	"encoding/json"
	"mincode/app/backend/storage"
	"mincode/app/backend/tasks"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportLateMalformedRowNoWrites(t *testing.T) {
	root := t.TempDir()
	_, err := prepareImportRows(context.Background(), strings.NewReader(`[{"id":1},{"id":}]`), "json", []string{"id"}, root, defaultTransferLimits())
	if err == nil {
		t.Fatal("late malformed row accepted")
	}
	files, _ := os.ReadDir(root)
	if len(files) != 0 {
		t.Fatal("invalid source left spool")
	}
}
func TestImportExactNumbers(t *testing.T) {
	path, err := prepareImportRows(context.Background(), strings.NewReader(`[{"id":9223372036854775807}]`), "json", []string{"id"}, t.TempDir(), defaultTransferLimits())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "9223372036854775807") {
		t.Fatalf("precision: %s %v", data, err)
	}
}
func TestImportRejectsDuplicateHeaders(t *testing.T) {
	if _, err := prepareImportRows(context.Background(), strings.NewReader("id,id\n1,2\n"), "csv", []string{"id"}, t.TempDir(), defaultTransferLimits()); err == nil {
		t.Fatal("duplicate CSV headers accepted")
	}
}
func TestImportRejectsUnknownColumn(t *testing.T) {
	if _, err := prepareImportRows(context.Background(), strings.NewReader(`[{"unknown":1}]`), "json", []string{"id"}, filepath.Join(t.TempDir(), "spool"), defaultTransferLimits()); err == nil {
		t.Fatal("unknown column accepted")
	}
}

func TestImportLegacyResponseAndPartialOutcome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	transferMu.Lock()
	oldM, oldS, oldStop := transferManager, transferStore, transferStopping
	transferManager, transferStore, transferStopping = nil, nil, false
	transferMu.Unlock()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		StopDatabaseTransfers(ctx)
		transferMu.Lock()
		transferManager, transferStore, transferStopping = oldM, oldS, oldStop
		transferMu.Unlock()
		_ = storage.Close()
	})
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	if _, err := target.db.Exec("CREATE TABLE items(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	registry.put("import-fixture", target)
	t.Cleanup(func() { registry.remove("import-fixture") })
	check, err := checkOperation(context.Background(), target, importOperation("items", "json", `[{"id":9223372036854775807}]`), "main")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(importDataArgs{SessionID: "import-fixture", Database: "main", Table: "items", Format: "json", Data: `[{"id":9223372036854775807}]`, CheckID: &check.CheckID})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dbImportDataHandler(context.Background(), "", string(raw))
	if err != nil || got != "1" {
		t.Fatalf("legacy response=%s err=%v", got, err)
	}
	var id int64
	if err := target.db.QueryRow("SELECT id FROM items").Scan(&id); err != nil || id != 9223372036854775807 {
		t.Fatalf("numeric precision=%d err=%v", id, err)
	}
	plan, err := prepareTableImport(context.Background(), importPrepareRequest{SessionID: "import-fixture", Database: "main", Table: "items", Format: "json", Data: `[{"id":1},{"id":1}]`})
	if err != nil {
		t.Fatal(err)
	}
	task, err := submitTableImport(context.Background(), plan, "partial")
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ := databaseTransferManager()
	for deadline := time.Now().Add(3 * time.Second); ; {
		got, err := m.Get(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State.Terminal() {
			if got.State != tasks.StateFailed || got.Result.Records != 0 {
				t.Fatalf("partial %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestImportRejectsNestedAndDuplicateJSONKeys(t *testing.T) {
	for _, raw := range []string{`[{"id":1,"id":2}]`, `[{"id":{"nested":1}}]`} {
		if _, err := prepareImportRows(context.Background(), strings.NewReader(raw), "json", []string{"id"}, t.TempDir(), defaultTransferLimits()); err == nil {
			t.Fatalf("ambiguous row accepted: %s", raw)
		}
	}
}

func TestImportApplicationStorageRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	own, err := storage.Path()
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	s := &dbSession{kind: kindSQL, dialect: "sqlite", db: db}
	if err := validateImportTarget(context.Background(), s); err == nil {
		t.Fatalf("application store %s accepted", own)
	}
	alias := filepath.Join(t.TempDir(), "alias.db")
	if err := os.Link(own, alias); err != nil {
		t.Fatal(err)
	}
	target := openSQLiteSession(t, alias)
	if err := validateImportTarget(context.Background(), target); err == nil {
		t.Fatal("application store hardlink accepted")
	}
}

func TestImportSQLBatchRollsBackOnConstraintFailure(t *testing.T) {
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	target.db.Exec("CREATE TABLE items(id INTEGER PRIMARY KEY)")
	result, err := runJournaledFixture(t, func(ctx context.Context, r *tasks.Reporter) (tasks.Result, error) {
		return tasks.Result{}, importSQLBatch(ctx, target, "main", "items", []map[string]any{{"id": "1"}, {"id": "1"}}, r, nil)
	})
	if err == nil || result.Records != 0 || result.Certainty != tasks.EffectsNone {
		t.Fatalf("rollback result %+v %v", result, err)
	}
	var n int
	target.db.QueryRow("SELECT COUNT(*) FROM items").Scan(&n)
	if n != 0 {
		t.Fatalf("partial uncommitted batch=%d", n)
	}
}

// A record limit must stop reading the record, not allocate it and reject later.
func TestImportRecordLimitStopsOversizedInput(t *testing.T) {
	for _, format := range []string{"json", "csv"} {
		t.Run(format, func(t *testing.T) {
			data := "id\n\"" + strings.Repeat("x", 100000) + "\"\n"
			if format == "json" {
				data = `[{"id":"` + strings.Repeat("x", 100000) + `"}]`
			}
			input := strings.NewReader(data)
			limits := defaultTransferLimits()
			limits.RecordBytes = 32
			_, err := prepareImportRows(context.Background(), input, format, []string{"id"}, t.TempDir(), limits)
			if err == nil {
				t.Fatal("oversized record accepted")
			}
			if consumed := len(data) - input.Len(); consumed > 8192 {
				t.Fatalf("record limit read %d bytes before rejection", consumed)
			}
		})
	}
}

func TestImportKeepsEarlierCommittedBatch(t *testing.T) {
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	if _, err := target.db.Exec("CREATE TABLE items(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	result, err := runJournaledFixture(t, func(ctx context.Context, r *tasks.Reporter) (tasks.Result, error) {
		rows := make([]map[string]any, 500)
		for i := range rows {
			rows[i] = map[string]any{"id": i + 1}
		}
		if err := importSQLBatch(ctx, target, "main", "items", rows, r, nil); err != nil {
			return tasks.Result{}, err
		}
		return tasks.Result{}, importSQLBatch(ctx, target, "main", "items", []map[string]any{{"id": 501}, {"id": 1}}, r, nil)
	})
	if err == nil || result.Records != 500 || result.Certainty != tasks.EffectsKnown {
		t.Fatalf("partial result %+v %v", result, err)
	}
	var count int
	if err := target.db.QueryRow("SELECT COUNT(*) FROM items").Scan(&count); err != nil || count != 500 {
		t.Fatalf("count=%d error=%v", count, err)
	}
}
