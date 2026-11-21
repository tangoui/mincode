package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/db/sqlsafety"
)

// seamDriver wraps SQLite and logs every statement with the connection it
// ran on, so tests can run dialect-specific paths (MySQL USE, Postgres LOCK
// TABLE) without a live server: hook may rewrite a statement, or answer it
// (handled) with err instead of SQLite.
type seamDriver struct {
	inner driver.Driver
	dsn   string

	mu     sync.Mutex
	nextID int
	log    []seamEvent
	closed []int
	hook   func(query string) (rewritten string, handled bool, err error)
}

type seamEvent struct {
	conn     int
	query    string
	readOnly bool // BeginTx with TxOptions.ReadOnly ("BEGIN" events)
}

func (d *seamDriver) Connect(context.Context) (driver.Conn, error) {
	c, err := d.inner.Open(d.dsn)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	return &seamConn{d: d, id: d.nextID, inner: c}, nil
}

func (d *seamDriver) Driver() driver.Driver { return d.inner }

func (d *seamDriver) record(e seamEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log = append(d.log, e)
}

func (d *seamDriver) events() []seamEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]seamEvent(nil), d.log...)
}

func (d *seamDriver) closedConns() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int(nil), d.closed...)
}

// find is the first logged event whose query starts with prefix.
func (d *seamDriver) find(prefix string) (seamEvent, bool) {
	for _, e := range d.events() {
		if strings.HasPrefix(e.query, prefix) {
			return e, true
		}
	}
	return seamEvent{}, false
}

func (d *seamDriver) apply(query string) (string, bool, error) {
	d.mu.Lock()
	hook := d.hook
	d.mu.Unlock()
	if hook == nil {
		return query, false, nil
	}
	return hook(query)
}

type seamConn struct {
	d     *seamDriver
	id    int
	inner driver.Conn
}

func (c *seamConn) Prepare(query string) (driver.Stmt, error) { return c.inner.Prepare(query) }

func (c *seamConn) Close() error {
	c.d.mu.Lock()
	c.d.closed = append(c.d.closed, c.id)
	c.d.mu.Unlock()
	return c.inner.Close()
}

func (c *seamConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *seamConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.d.record(seamEvent{conn: c.id, query: "BEGIN", readOnly: opts.ReadOnly})
	opts.ReadOnly = false // SQLite has no read-only transactions
	return c.inner.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *seamConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.d.record(seamEvent{conn: c.id, query: query})
	query, handled, err := c.d.apply(query)
	if handled {
		if err != nil {
			return nil, err
		}
		return driver.RowsAffected(0), nil
	}
	return c.inner.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *seamConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.d.record(seamEvent{conn: c.id, query: query})
	query, handled, err := c.d.apply(query)
	if handled {
		if err == nil {
			err = errors.New("seam: query answered without rows")
		}
		return nil, err
	}
	return c.inner.(driver.QueryerContext).QueryContext(ctx, query, args)
}

// newSeamSession is a dialect session over a SQLite file holding table n
// (two rows), logged through a seamDriver.
func newSeamSession(t *testing.T, dialect string) (*dbSession, *seamDriver) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seam.db")
	plain, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`CREATE TABLE n (a INTEGER)`, `INSERT INTO n VALUES (1), (2)`} {
		if _, err := plain.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	d := &seamDriver{inner: plain.Driver(), dsn: path}
	_ = plain.Close()
	db := sql.OpenDB(d)
	t.Cleanup(func() { _ = db.Close() })
	return &dbSession{kind: kindSQL, dialect: dialect, db: db, connectionID: "conn-1"}, d
}

// seamSQLStateError is a driver error carrying a SQLSTATE, like pgconn.PgError.
type seamSQLStateError struct{ code, msg string }

func (e *seamSQLStateError) Error() string {
	return fmt.Sprintf("ERROR: %s (SQLSTATE %s)", e.msg, e.code)
}
func (e *seamSQLStateError) SQLState() string { return e.code }

// MySQL with no database in the request and a connection default: the
// check's name lookup, the estimate and the guarded run all pin that same
// database (USE on the connection they run on), never whatever an earlier
// USE left on a pooled connection.
func TestMySQLCheckAndRunPinTheSameDatabase(t *testing.T) {
	ctx := context.Background()
	s, d := newSeamSession(t, "mysql")
	s.defaultDatabase = "a"
	d.hook = func(q string) (string, bool, error) {
		if strings.HasPrefix(q, "USE ") {
			return q, true, nil
		}
		return q, false, nil
	}

	if got, ok := safetySchemaCandidates(ctx, s, "", ""); !ok || !slices.Equal(got, []string{"a"}) {
		t.Fatalf("lookup schemas = %v %v, want [a]", got, ok)
	}

	if err := withEstimateTx(ctx, s, "", true, func(ctx context.Context, tx *sql.Tx) error {
		var n int
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM n").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	use, ok := d.find("USE ")
	if !ok || use.query != "USE `a`" {
		t.Fatalf("estimate did not pin database a: %+v", d.events())
	}
	count, _ := d.find("SELECT COUNT(*) FROM n")
	if count.conn != use.conn {
		t.Fatalf("estimate ran on connection %d, USE on %d", count.conn, use.conn)
	}

	d.log = nil
	r, err := startGuardedRun(ctx, s, &sqlsafety.Check{Database: ""}, nil, "query")
	if err != nil {
		t.Fatal(err)
	}
	defer r.conn.Close()
	if _, err := r.conn.ExecContext(ctx, "DELETE FROM n WHERE a = 1"); err != nil {
		t.Fatal(err)
	}
	use, ok = d.find("USE ")
	if !ok || use.query != "USE `a`" {
		t.Fatalf("guarded run did not pin database a: %+v", d.events())
	}
	if write, _ := d.find("DELETE FROM n"); write.conn != use.conn {
		t.Fatalf("statement ran on connection %d, USE on %d", write.conn, use.conn)
	}

	// The request's database wins over the default, in both places.
	d.log = nil
	r2, err := startGuardedRun(ctx, s, &sqlsafety.Check{Database: "b"}, nil, "query")
	if err != nil {
		t.Fatal(err)
	}
	r2.conn.Close()
	if use, ok := d.find("USE "); !ok || use.query != "USE `b`" {
		t.Fatalf("guarded run with database b: %+v", d.events())
	}
	if got, _ := safetySchemaCandidates(ctx, s, "b", ""); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("lookup schemas with database b = %v", got)
	}
}

// postgresWholeTableStep empties table n through the Postgres whole-table
// path (SQLite stands in; ONLY is dropped by the seam).
func postgresWholeTableRun(t *testing.T, s *dbSession) (*guardedResult, error) {
	t.Helper()
	table := sqlsafety.TableRef{Schema: "main", Name: "n"}
	step := guardedStep{
		Statement: sqlsafety.StatementCheck{Kind: sqlsafety.KindStructure, Text: "DELETE FROM n", Table: &table, Undoable: true, Risk: sqlsafety.RiskHigh},
		Parsed:    sqlsafety.Statement{Kind: sqlsafety.KindStructure, Text: "DELETE FROM n", Table: &table, Destructive: true},
		Resolved:  true,
		Snapshot:  wholeTableSnapshotSQL(s, table),
	}
	check := &sqlsafety.Check{SessionID: s.sessionID, Risk: sqlsafety.RiskHigh, Undoable: true, Statements: []sqlsafety.StatementCheck{step.Statement}}
	return runGuarded(context.Background(), s, check, []guardedStep{step}, execPerStatement, "destructive")
}

func postgresLockHook(lockErr, rollbackErr error) func(string) (string, bool, error) {
	return func(q string) (string, bool, error) {
		switch {
		case strings.HasPrefix(q, "LOCK TABLE "):
			return q, true, lockErr
		case strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT") && rollbackErr != nil:
			return q, true, rollbackErr
		}
		return strings.Replace(q, "SELECT * FROM ONLY ", "SELECT * FROM ", 1), false, nil
	}
}

// A Postgres-dialect engine without LOCK TABLE (CockroachDB: SQLSTATE
// 0A000) rolls the refusal back to the savepoint and backs the table up
// without the lock, noted on the record; a lock that is refused for any
// other reason (lock timeout, privilege) or a failing ROLLBACK TO stops the
// statement.
func TestPostgresWholeTableLockFallback(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	unsupported := &seamSQLStateError{code: "0A000", msg: "at or near \"lock\": syntax error: unimplemented: this syntax"}

	t.Run("lock held", func(t *testing.T) {
		store := useSafetyStore(t)
		s, d := newSeamSession(t, "postgres")
		d.hook = postgresLockHook(nil, nil)
		if _, err := postgresWholeTableRun(t, s); err != nil {
			t.Fatal(err)
		}
		rec := onlyRecord(t, store)
		if !rec.Undoable || rec.UndoReason != "" || tableCount(t, s, "n") != 0 {
			t.Fatalf("record = %+v", rec)
		}
		if _, ok := d.find("RELEASE SAVEPOINT mincode_safety_lock"); !ok {
			t.Fatalf("savepoint not released: %+v", d.events())
		}
	})

	t.Run("lock not supported", func(t *testing.T) {
		store := useSafetyStore(t)
		s, d := newSeamSession(t, "postgres")
		d.hook = postgresLockHook(unsupported, nil)
		if _, err := postgresWholeTableRun(t, s); err != nil {
			t.Fatal(err)
		}
		if _, ok := d.find("ROLLBACK TO SAVEPOINT mincode_safety_lock"); !ok {
			t.Fatalf("refused lock not rolled back to the savepoint: %+v", d.events())
		}
		rec := onlyRecord(t, store)
		if !rec.Undoable || rec.UndoReason != sqlsafety.ReasonNoTransaction || tableCount(t, s, "n") != 0 {
			t.Fatalf("record = %+v", rec)
		}
		_, rows, err := store.Get(rec.ID)
		if err != nil || len(rows.Before) != 2 {
			t.Fatalf("backup rows = %+v %v", rows, err)
		}
	})

	t.Run("lock not supported by message", func(t *testing.T) {
		useSafetyStore(t)
		s, d := newSeamSession(t, "postgres")
		d.hook = postgresLockHook(errors.New("ERROR: unimplemented: LOCK TABLE (SQLSTATE 0A000)"), nil)
		if _, err := postgresWholeTableRun(t, s); err != nil {
			t.Fatal(err)
		}
	})

	for name, lockErr := range map[string]error{
		"lock timeout":      &seamSQLStateError{code: "55P03", msg: "canceling statement due to lock timeout"},
		"permission denied": &seamSQLStateError{code: "42501", msg: "permission denied for table n"},
	} {
		t.Run(name, func(t *testing.T) {
			store := useSafetyStore(t)
			s, d := newSeamSession(t, "postgres")
			d.hook = postgresLockHook(lockErr, nil)
			if _, err := postgresWholeTableRun(t, s); err == nil || !strings.Contains(err.Error(), lockErr.Error()) {
				t.Fatalf("err = %v, want the lock error", err)
			}
			if n := tableCount(t, s, "n"); n != 2 || len(listRecords(t, store)) != 0 {
				t.Fatalf("after a failed lock: %d rows, records %+v", n, listRecords(t, store))
			}
		})
	}

	t.Run("rollback to savepoint fails", func(t *testing.T) {
		store := useSafetyStore(t)
		s, d := newSeamSession(t, "postgres")
		d.hook = postgresLockHook(unsupported, errors.New("current transaction is aborted"))
		if _, err := postgresWholeTableRun(t, s); err == nil {
			t.Fatal("ran after ROLLBACK TO SAVEPOINT failed")
		}
		if n := tableCount(t, s, "n"); n != 2 || len(listRecords(t, store)) != 0 {
			t.Fatalf("after a failed rollback: %d rows, records %+v", n, listRecords(t, store))
		}
	})
}

// Firebird estimates run read-only, and the connection is discarded
// afterwards: the driver would carry the read-only parameters into the
// pooled connection's next transaction and refuse the user's write.
func TestFirebirdEstimateIsReadOnlyAndDiscardsConnection(t *testing.T) {
	ctx := context.Background()
	s, d := newSeamSession(t, "firebird")
	var n int
	if err := withEstimateTx(ctx, s, "", true, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM n").Scan(&n)
	}); err != nil || n != 2 {
		t.Fatalf("estimate = %d, %v", n, err)
	}
	begin, ok := d.find("BEGIN")
	if !ok || !begin.readOnly {
		t.Fatalf("estimate transaction = %+v, want read-only", d.events())
	}
	if !slices.Contains(d.closedConns(), begin.conn) {
		t.Fatalf("connection %d went back to the pool (closed: %v)", begin.conn, d.closedConns())
	}
	if open := s.db.Stats().OpenConnections; open != 0 {
		t.Fatalf("open connections = %d, want the estimate's discarded", open)
	}
}
