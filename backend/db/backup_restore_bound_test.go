package db

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"mincode/app/backend/storage"
	"mincode/app/backend/tasks"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestoreMetadataErrorNoWrites(t *testing.T) {
	file, _ := backupFixture(t, "archive")
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	target.db.Close()
	if p, err := planRestore(context.Background(), target, file); err == nil && p.Supported {
		t.Fatal("metadata errors were treated as empty target")
	}
}
func TestRestoreDialectMismatch(t *testing.T) {
	file, _ := backupFixture(t, "archive")
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	target.dialect = "mysql"
	if p, err := planRestore(context.Background(), target, file); err == nil && p.Supported {
		t.Fatal("cross dialect accepted")
	}
}

func TestRestoreRequiresPreparedPlan(t *testing.T) {
	var req RestoreSubmitRequest
	if err := strictTransferJSON([]byte(`{"connectionId":"x","path":"a.sql"}`), &req); err == nil {
		t.Fatal("path-only start accepted")
	}
}
func TestConstrainedPreparedSQLiteRoundTrip(t *testing.T) {
	sourcePath, source := backupFixture(t, "archive")
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	staged, err := stageRestoreSource(context.Background(), sourcePath, t.TempDir(), defaultTransferLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	// Exercise the prepared constrained path, not the legacy raw-DDL executor.
	p := restorePrepared{Source: *staged, Mode: "constrained", Mappings: []restoreMapping{{Database: "main", Object: "items", TargetDatabase: "main", TargetObject: "items"}}}
	_, err = runConstrainedFixture(t, target, p)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := restoreRows(t, target), restoreRows(t, source); got != want {
		t.Fatalf("roundtrip mismatch: %s / %s", got, want)
	}
}

func runConstrainedFixture(t *testing.T, target *dbSession, p restorePrepared) (tasks.Result, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	db, err := storage.DB()
	if err != nil {
		return tasks.Result{}, err
	}
	t.Cleanup(func() { _ = storage.Close() })
	m, err := tasks.NewManager(context.Background(), tasks.NewStore(db), map[tasks.Kind]tasks.Executor{tasks.KindRestore: func(ctx context.Context, _ tasks.Task, r *tasks.Reporter) (tasks.Result, error) {
		return executeConstrainedRestore(ctx, target, p, r)
	}}, nil)
	if err != nil {
		return tasks.Result{}, err
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	task, err := m.Submit(context.Background(), tasks.Request{Kind: tasks.KindRestore, IdempotencyKey: "fixture", Fingerprint: "fixture"})
	if err != nil {
		return tasks.Result{}, err
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		snapshot, err := m.Get(context.Background(), task.ID)
		if err != nil {
			return tasks.Result{}, err
		}
		if snapshot.State.Terminal() {
			if snapshot.State != tasks.StateSucceeded {
				return snapshot.Result, fmt.Errorf("task failed: %+v", snapshot)
			}
			return snapshot.Result, nil
		}
		time.Sleep(time.Millisecond)
	}
	return tasks.Result{}, fmt.Errorf("fixture timeout")
}

func TestRestorePlanBindsSourceTargetAndConnection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	transferMu.Lock()
	oldM, oldS, oldStop := transferManager, transferStore, transferStopping
	transferManager = nil
	transferStore = nil
	transferStopping = false
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
	file, _ := backupFixture(t, "archive")
	path := filepath.Join(t.TempDir(), "target.db")
	conn := backupSavedConnection{ID: "target", DbType: "sqlite", FilePath: &path}
	if err := writeAppKVJSON(savedConnectionsKey, []backupSavedConnection{conn}); err != nil {
		t.Fatal(err)
	}
	p, err := prepareRestore(context.Background(), RestorePrepareRequest{ConnectionID: "target", Path: file, Mode: "constrained", Mappings: []restoreMapping{{Database: "main", Object: "items", TargetDatabase: "main", TargetObject: "items"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Supported || p.PlanID == "" {
		t.Fatalf("plan %+v", p)
	}
	if err := os.WriteFile(file, []byte("replaced source"), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := submitRestore(context.Background(), RestoreSubmitRequest{PlanID: p.PlanID, IdempotencyKey: "click"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := submitRestore(context.Background(), RestoreSubmitRequest{PlanID: p.PlanID, IdempotencyKey: "click"})
	if err != nil || again.ID != task.ID {
		t.Fatalf("retry: %+v %v", again, err)
	}
	m, _, _ := databaseTransferManager()
	for deadline := time.Now().Add(3 * time.Second); ; {
		got, err := m.Get(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State.Terminal() {
			if got.State != tasks.StateSucceeded || got.Result.Records != 2 {
				t.Fatalf("result %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(time.Millisecond)
	}
	// A distinct newly reviewed source cannot silently run after connection edits.
	file, _ = backupFixture(t, "archive")
	p, err = prepareRestore(context.Background(), RestorePrepareRequest{ConnectionID: "target", Path: file, Mode: "advanced"})
	if err != nil {
		t.Fatal(err)
	}
	conn.SSL = true
	if err := writeAppKVJSON(savedConnectionsKey, []backupSavedConnection{conn}); err != nil {
		t.Fatal(err)
	}
	if _, err := submitRestore(context.Background(), RestoreSubmitRequest{PlanID: p.PlanID, IdempotencyKey: "changed", UnrestrictedRiskAccepted: true, AllowExisting: false}); err == nil {
		t.Fatal("changed config admitted")
	}
}

func TestAdvancedScriptPersistsAcknowledgedEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script.sql")
	if err := os.WriteFile(path, []byte("CREATE TABLE acknowledged(id INTEGER); INSERT INTO acknowledged VALUES(1); INSERT INTO missing VALUES(2);"), 0600); err != nil {
		t.Fatal(err)
	}
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	result, err := runJournaledFixture(t, func(ctx context.Context, r *tasks.Reporter) (tasks.Result, error) {
		_, err := runRestoreReported(ctx, target, path, RestoreOptions{AllowDestructive: true}, nil, r)
		return tasks.Result{}, err
	})
	if err == nil || result.Certainty != tasks.EffectsUncertain || result.Objects < 1 {
		t.Fatalf("partial script result %+v err=%v", result, err)
	}
	var n int
	if err := target.db.QueryRow("SELECT count(*) FROM acknowledged").Scan(&n); err != nil || n != 1 {
		t.Fatalf("earlier commit missing: %d %v", n, err)
	}
}
func runJournaledFixture(t *testing.T, fn func(context.Context, *tasks.Reporter) (tasks.Result, error)) (tasks.Result, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	db, err := storage.DB()
	if err != nil {
		return tasks.Result{}, err
	}
	t.Cleanup(func() { _ = storage.Close() })
	m, err := tasks.NewManager(context.Background(), tasks.NewStore(db), map[tasks.Kind]tasks.Executor{tasks.KindRestore: func(ctx context.Context, _ tasks.Task, r *tasks.Reporter) (tasks.Result, error) { return fn(ctx, r) }}, nil)
	if err != nil {
		return tasks.Result{}, err
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	task, err := m.Submit(context.Background(), tasks.Request{Kind: tasks.KindRestore, IdempotencyKey: "journal-fixture", Fingerprint: "fixture"})
	if err != nil {
		return tasks.Result{}, err
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		snapshot, err := m.Get(context.Background(), task.ID)
		if err != nil {
			return tasks.Result{}, err
		}
		if snapshot.State.Terminal() {
			if snapshot.State != tasks.StateSucceeded {
				return snapshot.Result, fmt.Errorf("execution failed")
			}
			return snapshot.Result, nil
		}
		time.Sleep(time.Millisecond)
	}
	return tasks.Result{}, fmt.Errorf("timeout")
}

func TestPreparedColumnHeaderMustMatchSchema(t *testing.T) {
	path, _ := backupFixture(t, "archive")
	p, err := stageRestoreSource(context.Background(), path, t.TempDir(), defaultTransferLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	z, m, err := readPreparedManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := archiveSchemas(z, m)
	if err != nil {
		t.Fatal(err)
	}
	schema := schemas["main\x00items"]
	schema.Columns[0].Name = "different"
	if err := validatePreparedSchemaColumns(z, m, map[string]restoreSchema{"main\x00items": schema}); err == nil {
		t.Fatal("unreviewed header accepted")
	}
}

func TestConstrainedRequiresEmptyDestinationUnlessAppend(t *testing.T) {
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	if _, err := target.db.Exec("CREATE TABLE unrelated(id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := validateRestoreDestination(context.Background(), target, []restoreMapping{{TargetDatabase: "main", TargetObject: "items"}}, false); err == nil {
		t.Fatal("nonempty namespace accepted without append policy")
	}
}

func TestLegacyRestoreSingleConnectionPoolDoesNotDeadlock(t *testing.T) {
	file, _ := backupFixture(t, "archive")
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	target.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := runRestore(ctx, target, file, RestoreOptions{}, nil); err != nil {
		t.Fatalf("single connection restore failed: %v", err)
	}
}

func TestAdvancedOpenTransactionCannotSucceed(t *testing.T) {
	for _, script := range []string{"CREATE TABLE tx_items(id INTEGER); BEGIN; INSERT INTO tx_items VALUES(1);", "CREATE TABLE tx_items(id INTEGER); BEGIN; INSERT INTO tx_items VALUES(1); ROLLBACK;"} {
		t.Run(script, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "script.sql")
			os.WriteFile(path, []byte(script), 0600)
			target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
			result, err := runJournaledFixture(t, func(ctx context.Context, r *tasks.Reporter) (tasks.Result, error) {
				_, err := runRestoreReported(ctx, target, path, RestoreOptions{}, nil, r)
				return tasks.Result{}, err
			})
			if result.Objects > 1 {
				t.Fatalf("uncommitted statements counted as effects: %+v", result)
			}
			if strings.HasSuffix(script, "VALUES(1);") && err == nil {
				t.Fatal("open transaction succeeded")
			}
		})
	}
}
func TestConstrainedTimestampBindsNativeTime(t *testing.T) {
	v, err := restoreBoundValue(backupTypedValue{Type: "timestamp", Value: "2024-01-02T03:04:05.123456Z"})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := v.(time.Time)
	if !ok || value.Nanosecond() != 123456000 {
		t.Fatalf("timestamp = %#v", v)
	}
}

func TestMongoAppendRequiresEquivalentMetadata(t *testing.T) {
	source := bson.M{"options": bson.M{"validator": bson.M{"age": bson.M{"$gt": 0}}}}
	changed := bson.M{"options": bson.M{"validator": bson.M{"age": bson.M{"$gt": 18}}}}
	if mongoRestoreMetadataEqual(source, changed) {
		t.Fatal("changed validator accepted")
	}
	if !mongoRestoreMetadataEqual(source, source) {
		t.Fatal("equal metadata refused")
	}
	if mongoAppendIndexSafe(bson.M{"name": "ttl", "expireAfterSeconds": int32(0)}) {
		t.Fatal("TTL index accepted for append")
	}
}
