package db

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/db/sqlsafety"
)

// newSafetyTestSession opens a file-backed SQLite session with t(id pk, a)
// holding 1500 rows and a key-less table n(a).
func newSafetyTestSession(t *testing.T) *dbSession {
	t.Helper()
	s := openSQLiteSession(t, filepath.Join(t.TempDir(), "safety.db"))
	for _, stmt := range []string{
		`CREATE TABLE t (id INTEGER PRIMARY KEY, a INTEGER)`,
		`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM seq WHERE x < 1500) INSERT INTO t (id, a) SELECT x, 0 FROM seq`,
		`CREATE TABLE n (a INTEGER)`,
		`INSERT INTO n VALUES (1), (2)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return s
}

func useSafetySettings(t *testing.T, s sqlsafety.Settings) {
	t.Helper()
	previous, loaded := safetySettingsSnapshot()
	setSafetySettings(s)
	t.Cleanup(func() { restoreSafetySettings(previous, loaded) })
}

func safetySettingsSnapshot() (sqlsafety.Settings, bool) {
	safetySettingsState.mu.Lock()
	defer safetySettingsState.mu.Unlock()
	return safetySettingsState.settings, safetySettingsState.loaded
}

func restoreSafetySettings(s sqlsafety.Settings, loaded bool) {
	safetySettingsState.mu.Lock()
	defer safetySettingsState.mu.Unlock()
	safetySettingsState.settings, safetySettingsState.loaded = s, loaded
}

func mustCheck(t *testing.T, s *dbSession, query string) *sqlsafety.Check {
	t.Helper()
	c, err := checkStatements(context.Background(), s, query, "")
	if err != nil {
		t.Fatalf("checkStatements(%q): %v", query, err)
	}
	return c
}

func hasReason(reasons []sqlsafety.Reason, r sqlsafety.Reason) bool {
	return slices.Contains(reasons, r)
}

func TestSafetyCheckEstimatesAndRisk(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)

	c := mustCheck(t, s, "UPDATE t SET a=1 WHERE id <= 10")
	if c.Risk != sqlsafety.RiskLow || c.CheckID == "" || !c.Undoable || len(c.Statements) != 1 {
		t.Fatalf("low update check = %+v", c)
	}
	st := c.Statements[0]
	if st.Estimate != (sqlsafety.Estimate{Rows: 10, Exact: true, Source: "count"}) || !st.Undoable || st.Risk != sqlsafety.RiskLow {
		t.Fatalf("low update statement = %+v", st)
	}
	if c.ConfirmTarget != "" {
		t.Fatalf("low risk confirm target = %q", c.ConfirmTarget)
	}

	c = mustCheck(t, s, "DELETE FROM t")
	st = c.Statements[0]
	if c.Risk != sqlsafety.RiskHigh || !hasReason(st.Reasons, sqlsafety.ReasonNoWhere) || st.Estimate.Rows != 1500 || c.ConfirmTarget != "t" {
		t.Fatalf("delete all check = %+v", c)
	}
	if n := countRows(t, s, "t"); n != 1500 {
		t.Fatalf("checking DELETE changed the table: %d rows left", n)
	}

	c = mustCheck(t, s, "SELECT 1")
	if c.Risk != sqlsafety.RiskNone || c.CheckID != "" {
		t.Fatalf("select check = %+v", c)
	}

	c = mustCheck(t, s, "DELETE FROM n WHERE a=1")
	st = c.Statements[0]
	if st.Undoable || c.Undoable || !hasReason(st.Reasons, sqlsafety.ReasonNoPrimaryKey) {
		t.Fatalf("no primary key check = %+v", c)
	}

	c = mustCheck(t, s, "BEGIN; UPDATE t SET a=2 WHERE id=1; COMMIT;")
	if len(c.Statements) != 3 || c.Undoable {
		t.Fatalf("explicit transaction check = %+v", c)
	}
	for _, st := range c.Statements {
		if st.Undoable || !hasReason(st.Reasons, sqlsafety.ReasonExplicitTransaction) {
			t.Fatalf("explicit transaction statement = %+v", st)
		}
	}
}

func countRows(t *testing.T, s *dbSession, table string) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSafetyCheckInsertEstimates(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)

	c := mustCheck(t, s, "INSERT INTO t(a) VALUES (1),(2),(3)")
	st := c.Statements[0]
	if c.Risk != sqlsafety.RiskLow || st.Estimate != (sqlsafety.Estimate{Rows: 3, Exact: true, Source: "count"}) || st.Table == nil || st.Table.Name != "t" {
		t.Fatalf("insert values check = %+v", c)
	}
	if hasReason(st.Reasons, sqlsafety.ReasonUnparsed) {
		t.Fatalf("plain insert must not be unparsed: %v", st.Reasons)
	}
	plan, ok := takeSafetyPlan(c.CheckID)
	if !ok || !plan.statements[0].resolved || !slices.Equal(plan.statements[0].primaryKey, []string{"id"}) {
		t.Fatalf("insert plan = %+v", plan)
	}

	c = mustCheck(t, s, "INSERT INTO n (a) SELECT a FROM t")
	st = c.Statements[0]
	if c.Risk != sqlsafety.RiskHigh || !hasReason(st.Reasons, sqlsafety.ReasonOverThreshold) || st.Estimate.Rows != 1500 || !st.Estimate.Exact || st.Table == nil || st.Table.Name != "n" {
		t.Fatalf("insert select check = %+v", c)
	}
	if !hasReason(st.Reasons, sqlsafety.ReasonNoPrimaryKey) {
		t.Fatalf("insert into key-less table reasons = %v", st.Reasons)
	}
	if n := countRows(t, s, "n"); n != 2 {
		t.Fatalf("checking INSERT changed the table: %d rows", n)
	}
}

func TestSafetyExplainNeverSendsSecondStatement(t *testing.T) {
	s := newSafetyTestSession(t)
	var sent []string
	safetyExplainHook = func(q string) { sent = append(sent, q) }
	t.Cleanup(func() { safetyExplainHook = nil })

	for _, text := range []string{
		"DELETE FROM t WHERE a = 1; DROP TABLE t",
		"DELETE FROM t WHERE a = 1 /*!; DROP TABLE t */",
	} {
		if _, ok := explainEstimate(context.Background(), s, sqlsafety.FamilyPostgres, "", text); ok || len(sent) != 0 {
			t.Fatalf("explain of %q: ok=%v sent=%q", text, ok, sent)
		}
	}
	if est := estimateInsertRows(context.Background(), s, "", sqlsafety.Statement{InsertQuery: "SELECT 1; DROP TABLE t"}); est.Source != "none" {
		t.Fatalf("insert count of a two-statement query = %+v", est)
	}
	// Control: a single statement does reach EXPLAIN.
	explainEstimate(context.Background(), s, sqlsafety.FamilyPostgres, "", "DELETE FROM t WHERE a = 1;")
	if len(sent) != 1 || sent[0] != "EXPLAIN DELETE FROM t WHERE a = 1" {
		t.Fatalf("single statement explain = %q", sent)
	}
	if n := countRows(t, s, "t"); n != 1500 {
		t.Fatalf("explain changed the table: %d rows", n)
	}
}

func TestSafetyCheckEverySegment(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)

	c := mustCheck(t, s, "SELECT 1; SELECT a FROM t WHERE id = 1;")
	if c.Risk != sqlsafety.RiskNone || c.CheckID != "" {
		t.Fatalf("all-read check = %+v", c)
	}

	query := "SELECT 1; DELETE FROM t WHERE id = 3"
	c = mustCheck(t, s, query)
	if c.CheckID == "" || len(c.Statements) != 2 || c.Statements[0].Risk != sqlsafety.RiskNone || c.Statements[1].Estimate.Rows != 1 {
		t.Fatalf("mixed check = %+v", c)
	}
	if !c.Undoable {
		t.Fatalf("mixed check with an undoable write must be undoable: %+v", c)
	}
	if _, err := safetyTokens.Consume(c.CheckID, c.SessionID, "", queryHash(query)); err != nil {
		t.Fatalf("consume issued token: %v", err)
	}
}

func TestSafetyCheckResolvesIdentifiers(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)

	c := mustCheck(t, s, "UPDATE t SET A = 5 WHERE id = 7")
	if !c.Undoable || c.Statements[0].Estimate.Rows != 1 {
		t.Fatalf("case-folded column check = %+v", c)
	}
	plan, ok := takeSafetyPlan(c.CheckID)
	if !ok || len(plan.statements) != 1 {
		t.Fatalf("plan = %+v, %v", plan, ok)
	}
	rs := plan.statements[0]
	if !rs.resolved || rs.parsed.Table == nil || rs.parsed.Table.Name != "t" || !slices.Equal(rs.parsed.SetColumns, []string{"a"}) || !slices.Equal(rs.primaryKey, []string{"id"}) {
		t.Fatalf("resolved statement = %+v table=%+v", rs, rs.parsed.Table)
	}
	if _, ok := takeSafetyPlan(c.CheckID); ok {
		t.Fatal("plan must be taken once")
	}

	c = mustCheck(t, s, "UPDATE t SET missing = 5 WHERE id = 7")
	st := c.Statements[0]
	if st.Undoable || !hasReason(st.Reasons, sqlsafety.ReasonUnparsed) {
		t.Fatalf("unknown column check = %+v", st)
	}

	// A table the selected database does not hold is reported as such, not
	// as a statement that could not be parsed (it parsed fine).
	for _, query := range []string{"DELETE FROM nope WHERE id <= 1", "UPDATE nope SET a = 1 WHERE id = 1", "INSERT INTO nope (id) VALUES (1)", "DROP TABLE nope"} {
		c = mustCheck(t, s, query)
		st = c.Statements[0]
		if st.Undoable || !hasReason(st.Reasons, sqlsafety.ReasonTableNotFound) || hasReason(st.Reasons, sqlsafety.ReasonUnparsed) ||
			(st.Kind != sqlsafety.KindInsert && c.Risk != sqlsafety.RiskHigh) {
			t.Fatalf("%s: unknown table check = %+v", query, c)
		}
	}
	if st := mustCheck(t, s, "DELETE FROM nope WHERE id <= 1").Statements[0]; st.Estimate.Source != "none" {
		t.Fatalf("unknown table estimate = %+v", st.Estimate)
	}
}

func TestSafetyCheckMetadataHints(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	for _, stmt := range []string{
		`CREATE TABLE child (id INTEGER PRIMARY KEY, t_id INTEGER REFERENCES t(id) ON DELETE SET DEFAULT)`,
		`CREATE TRIGGER t_audit AFTER UPDATE ON t BEGIN SELECT 1; END`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	c := mustCheck(t, s, "DELETE FROM t WHERE id = 1")
	st := c.Statements[0]
	if !hasReason(st.Reasons, sqlsafety.ReasonCascade) || !hasReason(st.Reasons, sqlsafety.ReasonTrigger) || !st.Undoable {
		t.Fatalf("delete hints = %+v", st)
	}
	c = mustCheck(t, s, "UPDATE t SET a = 1 WHERE id = 1")
	st = c.Statements[0]
	if hasReason(st.Reasons, sqlsafety.ReasonCascade) || !hasReason(st.Reasons, sqlsafety.ReasonTrigger) {
		t.Fatalf("update hints (no ON UPDATE rule) = %+v", st)
	}
}

func TestSafetyCheckSettingsAndLimits(t *testing.T) {
	s := newSafetyTestSession(t)

	limited := sqlsafety.DefaultSettings()
	limited.MaxSnapshotRows = 5
	useSafetySettings(t, limited)
	c := mustCheck(t, s, "UPDATE t SET a=1 WHERE id <= 10")
	st := c.Statements[0]
	if st.Undoable || !hasReason(st.Reasons, sqlsafety.ReasonOverSnapshotLimit) {
		t.Fatalf("over snapshot limit = %+v", st)
	}

	disabled := sqlsafety.DefaultSettings()
	disabled.Enabled = false
	setSafetySettings(disabled)
	c = mustCheck(t, s, "DELETE FROM t")
	st = c.Statements[0]
	if c.CheckID == "" || c.Undoable || st.Undoable || st.Estimate.Source != "none" || c.Risk != sqlsafety.RiskLow || c.ConfirmTarget != "" {
		t.Fatalf("disabled check = %+v", c)
	}
	c = mustCheck(t, s, "SELECT 1")
	if c.CheckID != "" || c.Risk != sqlsafety.RiskNone {
		t.Fatalf("disabled read check = %+v", c)
	}
}

func TestSafetyCheckNoTransactionFamily(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	s.dialect = "clickhouse" // FamilyNone, no transactions; never queried for estimates
	c := mustCheck(t, s, "ALTER TABLE t DELETE WHERE id = 1")
	st := c.Statements[0]
	if st.Undoable || !hasReason(st.Reasons, sqlsafety.ReasonNoTransaction) || c.CheckID == "" {
		t.Fatalf("clickhouse check = %+v", c)
	}
}

// Materialize speaks the Postgres dialect but refuses UPDATE / DELETE inside
// a transaction block, so its writes run unguarded and are not undoable.
func TestSafetyCheckMaterializeHasNoTransactions(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	s.engine = "materialize"
	if !s.safetyWithoutTransactions() {
		t.Fatal("materialize session must count as without transactions")
	}
	c := mustCheck(t, s, "UPDATE t SET a = 1 WHERE id = 1")
	if st := c.Statements[0]; st.Undoable || !hasReason(st.Reasons, sqlsafety.ReasonNoTransaction) {
		t.Fatalf("materialize check = %+v", st)
	}
	if (&dbSession{kind: kindSQL, dialect: "postgres", engine: "postgres"}).safetyWithoutTransactions() {
		t.Fatal("postgres has transactions")
	}
}

func TestReadOnlyQueryKeepsCallExecution(t *testing.T) {
	if isReadOnlyQuery(&dbSession{kind: kindSQL, dialect: "mysql"}, "CALL p()") {
		t.Fatal("CALL must not be read-only")
	}
	if !isResultProducing("CALL p()") {
		t.Fatal("CALL must still run as a query returning result sets")
	}
	if !isReadOnlyQuery(&dbSession{kind: kindSQL, dialect: "mysql"}, "SELECT 1; SHOW TABLES") {
		t.Fatal("all-read input must be read-only")
	}
	if isReadOnlyQuery(&dbSession{kind: kindSQL, dialect: "mysql"}, "SELECT 1; DELETE FROM t") {
		t.Fatal("one write segment makes the input non-read-only")
	}
}

func TestMatchNameFoldAmbiguity(t *testing.T) {
	cases := []struct {
		candidates []string
		want       string
		fold       func(string) string
		match      string
		ok         bool
	}{
		{[]string{"id", "a"}, "A", nil, "a", true},
		{[]string{"a", "A"}, "A", nil, "A", true},
		{[]string{"a", "A"}, "A", strings.ToLower, "", false}, // unquoted A means a in Postgres
		{[]string{"a", "A"}, "a", strings.ToLower, "a", true},
		{[]string{"Ab", "aB"}, "ab", nil, "", false},
		{[]string{"x"}, "y", nil, "", false},
	}
	for _, c := range cases {
		got, ok := matchName(c.candidates, c.want, c.fold)
		if ok != c.ok || (ok && got != c.match) {
			t.Errorf("matchName(%v, %q) = %q, %v; want %q, %v", c.candidates, c.want, got, ok, c.match, c.ok)
		}
	}
}

func TestParseExplainRows(t *testing.T) {
	pg := [][]any{{"Delete on t  (cost=0.00..35.50 rows=0 width=6)"}, {"  ->  Seq Scan on t  (cost=0.00..35.50 rows=2550 width=6)"}}
	if n, ok := explainRows(sqlsafety.FamilyPostgres, []string{"QUERY PLAN"}, pg); !ok || n != 2550 {
		t.Fatalf("postgres explain = %d, %v", n, ok)
	}
	my := [][]any{{int64(1), "UPDATE", []byte("120")}, {int64(2), "SIMPLE", int64(40)}}
	if n, ok := explainRows(sqlsafety.FamilyMySQL, []string{"id", "select_type", "rows"}, my); !ok || n != 120 {
		t.Fatalf("mysql explain = %d, %v", n, ok)
	}
	if _, ok := explainRows(sqlsafety.FamilySQLite, []string{"id", "parent", "notused", "detail"}, [][]any{{int64(2), int64(0), int64(0), "SCAN t"}}); ok {
		t.Fatal("sqlite explain has no row count")
	}
}

// TestSafetyCheckLatency: a low-risk single-row UPDATE check on local SQLite
// adds at most 50 ms beyond its COUNT estimate (spec constraint 6). Median
// of 25 runs through db_safety_check; the COUNT is timed on its own.
func TestSafetyCheckLatency(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	const query = "UPDATE t SET a = 1 WHERE id = 7"
	median := func(run func()) time.Duration {
		var d []time.Duration
		for range 25 {
			start := time.Now()
			run()
			d = append(d, time.Since(start))
		}
		slices.Sort(d)
		return d[len(d)/2]
	}
	check := median(func() {
		c, err := checkArgs(id, query)
		if err != nil || c.Risk != sqlsafety.RiskLow || c.Statements[0].Estimate.Source != "count" {
			t.Fatalf("check = %+v, %v", c, err)
		}
	})
	count := median(func() {
		var n int64
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM "t" WHERE id = 7`).Scan(&n); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("db_safety_check median %v, COUNT alone %v, check without COUNT ≈ %v", check, count, check-count)
	if check-count > 50*time.Millisecond {
		t.Fatalf("check overhead %v > 50ms", check-count)
	}
}

// TestSafetyCheckReadStatementsHaveEstimateSource: every statement of a
// check names its estimate source ("none" for reads), the contract the
// frontend parser enforces; an empty source made db_safety_check of a read,
// or of a write followed by a SELECT, unusable in the desktop.
func TestSafetyCheckReadStatementsHaveEstimateSource(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	nonSQL := registerSafetySession(t, &dbSession{kind: kindHTTP, httpKind: httpDatabricks})
	for _, tc := range []struct{ session, query string }{
		{id, "SELECT a FROM t WHERE id = 1"},
		{id, "UPDATE t SET a = 1 WHERE id = 1; SELECT a FROM t WHERE id = 1"},
		{nonSQL, "DELETE FROM sales.orders WHERE id = 1; SELECT * FROM sales.orders"},
		{nonSQL, "SELECT * FROM sales.orders"},
	} {
		c, err := checkArgs(tc.session, tc.query)
		if err != nil {
			t.Fatalf("%q: %v", tc.query, err)
		}
		for _, sc := range c.Statements {
			if sc.Estimate.Source != "none" && sc.Estimate.Source != "count" && sc.Estimate.Source != "explain" {
				t.Errorf("%q statement %d: estimate source %q", tc.query, sc.Index, sc.Estimate.Source)
			}
		}
	}
}
