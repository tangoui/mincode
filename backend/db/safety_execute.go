// SQL 安全网：同事务留底执行。
//
// 非只读的查询先由 requireCheck 消费 db_safety_check 签发的一次性令牌（绑定会话、
// 数据库与查询文本），再由 runGuarded 执行。可撤回的 UPDATE/DELETE 在同一事务里：
// 快照 SELECT（加行锁）读出要改的行 → 执行原语句 → 按快照主键回读改后的值 → 加密
// 留底 → 提交。留底写不进去就回滚，绝不在没有留底时提交可撤回的语句；实际行数越过
// 检查时的界限也回滚，并返回 sqlsafety:reconfirm（新 Check、新令牌）。
// 日志只记录记录 ID 与行数，绝不记录语句文本或行数据。
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/db/sqlsafety"
)

// Structured error codes the frontend handles (message "sqlsafety:" + JSON).
const (
	safetyCodeCheckRequired   = "checkRequired"
	safetyCodeCheckInvalid    = "checkInvalid"
	safetyCodeConfirmMismatch = "confirmMismatch"
	safetyCodeReconfirm       = "reconfirm"
	safetyCodePartial         = "partial"
)

const (
	safetyKeysPerQuery   = 500  // keys per after-image SELECT
	safetyParamsPerQuery = 2000 // bound parameters per SELECT (SQL Server allows 2100)
)

type safetyError struct {
	Code        string           `json:"code"`
	Check       *sqlsafety.Check `json:"check,omitempty"`
	Applied     *int             `json:"applied,omitempty"`
	FailedIndex *int             `json:"failedIndex,omitempty"`
	Message     string           `json:"message,omitempty"` // why the failed statement stopped (partial)
}

func (e *safetyError) Error() string {
	b, err := json.Marshal(e)
	if err != nil {
		return `sqlsafety:{"code":"` + e.Code + `"}`
	}
	return "sqlsafety:" + string(b)
}

// ── 留底存储 ──

// safetyRecordStore is the part of sqlsafety.Store execution writes to.
type safetyRecordStore interface {
	Put(rec sqlsafety.Record, rows sqlsafety.Rows) error
	Delete(id string) error
	MarkUndone(id, state string) error
}

var safetyRecordsState struct {
	mu    sync.Mutex
	store safetyRecordStore
}

// safetyRecords returns the backup store, opened under sqlsafety.Dir() on first use.
func safetyRecords() safetyRecordStore {
	safetyRecordsState.mu.Lock()
	defer safetyRecordsState.mu.Unlock()
	if safetyRecordsState.store == nil {
		safetyRecordsState.store = sqlsafety.OpenStore(sqlsafety.Dir(), time.Now)
	}
	return safetyRecordsState.store
}

// setSafetyRecords replaces the backup store and returns a function restoring the previous one.
func setSafetyRecords(store safetyRecordStore) func() {
	safetyRecordsState.mu.Lock()
	defer safetyRecordsState.mu.Unlock()
	previous := safetyRecordsState.store
	safetyRecordsState.store = store
	return func() {
		safetyRecordsState.mu.Lock()
		defer safetyRecordsState.mu.Unlock()
		safetyRecordsState.store = previous
	}
}

// ── 令牌 ──

// requireCheck consumes the check a non-read-only query must present: the
// token bound to this session, safetyDatabase(a.Database) and
// queryHash(a.Query), plus its resolved plan. A missing token is
// checkRequired; an unknown, used, expired or mismatched one — or a valid
// one whose plan is gone — is checkInvalid; a high-risk check whose
// confirmText does not name its confirmTarget is confirmMismatch. The token
// is spent in every case but checkRequired.
func requireCheck(s *dbSession, a executeQueryArgs) (*sqlsafety.Check, safetyPlan, error) {
	return consumeCheck(s, a.CheckID, a.ConfirmText, safetyDatabase(a.Database), queryHash(a.Query))
}

// consumeCheck is requireCheck for any entry point: the token must be bound
// to s, database and hash (queryHash of a query, operationHash of an operation).
func consumeCheck(s *dbSession, checkID, confirmText *string, database, hash string) (*sqlsafety.Check, safetyPlan, error) {
	id := strings.TrimSpace(optionalString(checkID))
	if id == "" {
		return nil, safetyPlan{}, &safetyError{Code: safetyCodeCheckRequired}
	}
	check, err := safetyTokens.Consume(id, s.sessionID, database, hash)
	if err != nil {
		return nil, safetyPlan{}, &safetyError{Code: safetyCodeCheckInvalid}
	}
	plan, ok := takeSafetyPlan(check.CheckID)
	if !ok || len(plan.statements) != len(check.Statements) {
		return nil, safetyPlan{}, &safetyError{Code: safetyCodeCheckInvalid}
	}
	if check.Risk == sqlsafety.RiskHigh && !sqlsafety.ConfirmTargetMatches(check.ConfirmTarget, optionalString(confirmText)) {
		return nil, safetyPlan{}, &safetyError{Code: safetyCodeConfirmMismatch}
	}
	return check, plan, nil
}

// ── 查询窗口入口 ──

// executeSQLQuery runs the query window text on a SQL session: read-only
// text inside the read guard, anything else guarded statement by statement.
func executeSQLQuery(ctx context.Context, s *dbSession, a executeQueryArgs) (*QueryResult, error) {
	if s.kind == kindPlugin && readPath(s, a) {
		// Reads go to the driver exactly as before (readOnly false): driver
		// read-only modes are narrower than the classifier (open user
		// transactions, multi-statement scripts, keyword allow-lists).
		return externalExecuteQuery(s, a.Query, a.Database, optionalString(a.QueryID), false)
	}
	if pluginReadOnlyProbe(s, a) {
		// Text the classifier does not know (PromQL, nGQL, a search DSL) on
		// a driver that enforces read-only executes: run it read-only first;
		// only a refusal (or any failure) makes it a write needing a check.
		if res, err := externalExecuteQuery(s, a.Query, a.Database, optionalString(a.QueryID), true); err == nil {
			return res, nil
		}
	}
	if readPath(s, a) {
		res, err := runReadPath(ctx, s, a)
		if isReadOnlyViolation(s.dialect, err) {
			return nil, forceWriteRequired(s, a)
		}
		return res, err
	}
	source := strings.TrimSpace(a.Source)
	if source == "" {
		source = "query"
	}
	if !safetySources[source] {
		return nil, fmt.Errorf("未知的来源 %q", source)
	}
	check, plan, err := requireCheck(s, a)
	if err != nil {
		return nil, err
	}
	steps, err := queryGuardedSteps(s, check, plan, a.Query)
	if err != nil {
		return nil, err
	}
	if s.kind == kindPlugin {
		ctx = withSafetyQueryID(ctx, optionalString(a.QueryID))
	}
	res, err := runGuarded(ctx, s, check, steps, execPerStatement, source)
	if err != nil {
		return nil, err
	}
	if res.Last == nil {
		return &QueryResult{Columns: []string{}, Rows: [][]any{}}, nil
	}
	return res.Last, nil
}

// pluginReadOnlyProbe: a plugin session whose driver enforces read-only
// executes (feature readOnly) and declares no SQL dialect ("generic": the
// only choice for PromQL, nGQL, a search DSL), no token, and text whose
// statements are all reads or unknown, at least one unknown and none of
// them SQL. A driver of a SQL dialect is never probed, and neither is SQL
// the classifier did not clear (unlisted verbs such as SET or VACUUM, reads
// demoted because they may hide a second statement): the host's write gate
// must not rest on the driver's own read-only enforcement for SQL.
func pluginReadOnlyProbe(s *dbSession, a executeQueryArgs) bool {
	if s.kind != kindPlugin || s.ext == nil || s.ext.dialect != "generic" || !s.ext.has(externalFeatureReadOnly) ||
		strings.TrimSpace(optionalString(a.CheckID)) != "" {
		return false
	}
	family := s.safetyFamily()
	unknown := false
	for _, segment := range sqlsafety.Split(family, a.Query) {
		switch sqlsafety.Classify(family, segment).Kind {
		case sqlsafety.KindRead:
		case sqlsafety.KindUnknown:
			if sqlsafety.LooksLikeSQL(family, segment) {
				return false
			}
			unknown = true
		default:
			return false
		}
	}
	return unknown
}

// readPath: read-only text without a token runs on the read path. A token
// with read-only text can only be a force-write check, which runs guarded.
func readPath(s *dbSession, a executeQueryArgs) bool {
	return strings.TrimSpace(optionalString(a.CheckID)) == "" && isReadOnlyQuery(s, a.Query)
}

// safetyReadHook, when set (tests only), runs before every read-path query;
// an error it returns stands in for the database's.
var safetyReadHook func(s *dbSession, query string) error

// errReadOnlyViolation is a read-only-transaction refusal in any dialect
// (tests inject it through safetyReadHook).
var errReadOnlyViolation = errors.New("cannot execute a write in a read-only transaction")

func readHookError(s *dbSession, query string) error {
	if safetyReadHook == nil {
		return nil
	}
	return safetyReadHook(s, query)
}

func runReadPath(ctx context.Context, s *dbSession, a executeQueryArgs) (*QueryResult, error) {
	if err := readHookError(s, a.Query); err != nil {
		return nil, err
	}
	return runReadOnlyQuery(ctx, s, a.Query, a.Database)
}

type sqlStater interface{ SQLState() string }

var mysqlReadOnlyText = regexp.MustCompile(`\bError 1792\b`)

// isReadOnlyViolation reports the read guard refusing a statement the
// classifier judged read-only (a writing SELECT such as nextval(), setval(),
// a writing function or SELECT ... FOR UPDATE): Postgres SQLSTATE 25006,
// MySQL error 1792, Oracle ORA-01456 and the Oracle-family / DM
// read-only-transaction messages. SQL Server's guard rolls back instead of
// refusing, and other dialects have no guard.
func isReadOnlyViolation(dialect string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errReadOnlyViolation) {
		return true
	}
	msg := err.Error()
	switch dialect {
	case "postgres":
		var st sqlStater
		if errors.As(err, &st) && st.SQLState() == "25006" {
			return true
		}
		return strings.Contains(msg, "SQLSTATE 25006")
	case "mysql":
		return mysqlErrorNumber(err) == 1792 || mysqlReadOnlyText.MatchString(msg)
	case "oracle", "yashandb", "dm":
		lower := strings.ToLower(msg)
		return strings.Contains(msg, "ORA-01456") || strings.Contains(lower, "read only transaction") ||
			strings.Contains(lower, "read-only transaction") || strings.Contains(msg, "只读事务")
	}
	return false
}

// mysqlErrorNumber is the Number field of a go-sql-driver MySQLError in
// err's chain (read by reflection so this file needs no driver import), or 0.
func mysqlErrorNumber(err error) uint64 {
	for e := err; e != nil; e = errors.Unwrap(e) {
		v := reflect.ValueOf(e)
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			continue
		}
		if f := v.FieldByName("Number"); f.IsValid() {
			switch f.Kind() {
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				return f.Uint()
			}
		}
	}
	return 0
}

// forceWriteRequired issues the check a refused read must present to run
// as a write: one unknown, low-risk, not undoable statement bound to this
// session, database and query text, whose plan runs it outside the read
// guard (and outside a transaction, like other unknown statements).
func forceWriteRequired(s *dbSession, a executeQueryArgs) error {
	text := strings.TrimSpace(a.Query)
	check := &sqlsafety.Check{SessionID: s.sessionID, Database: safetyDatabase(a.Database), Risk: sqlsafety.RiskLow,
		Statements: []sqlsafety.StatementCheck{{Text: truncateRunes(text, safetyTextLimit), Kind: sqlsafety.KindUnknown,
			Estimate: sqlsafety.Estimate{Source: "none"}, Risk: sqlsafety.RiskLow, Reasons: []sqlsafety.Reason{sqlsafety.ReasonUnparsed}}}}
	safetyTokens.Issue(check, queryHash(a.Query))
	storeSafetyPlan(check.CheckID, safetyPlan{expiresAt: check.ExpiresAt, forceWrite: true,
		statements: []safetyResolvedStatement{{parsed: sqlsafety.Statement{Text: text, Kind: sqlsafety.KindUnknown}}}})
	slog.Info("sql safety read refused as a write; check issued")
	return &safetyError{Code: safetyCodeCheckRequired, Check: check}
}

// queryGuardedSteps turns a consumed check and its plan into one step per
// statement. On SQL Server a script (explicit transaction or DECLARE) runs
// as the original single batch, so its variables stay in scope.
func queryGuardedSteps(s *dbSession, check *sqlsafety.Check, plan safetyPlan, query string) ([]guardedStep, error) {
	if len(plan.statements) != len(check.Statements) {
		return nil, &safetyError{Code: safetyCodeCheckInvalid}
	}
	family := s.safetyFamily()
	if plan.forceWrite {
		segments := sqlsafety.Split(family, query)
		steps := make([]guardedStep, len(segments))
		for i, seg := range segments {
			sc := check.Statements[0]
			sc.Index, sc.Text = i, truncateRunes(seg, safetyTextLimit)
			steps[i] = guardedStep{Statement: sc, Parsed: sqlsafety.Statement{Index: i, Text: seg, Kind: sqlsafety.KindUnknown}}
		}
		return steps, nil
	}
	if family == sqlsafety.FamilySQLServer && len(check.Statements) > 1 && scriptedStatements(check.Statements) {
		text := strings.TrimSpace(query)
		return []guardedStep{{
			Statement: sqlsafety.StatementCheck{Text: truncateRunes(text, safetyTextLimit), Kind: sqlsafety.KindScript, Risk: check.Risk,
				Estimate: sqlsafety.Estimate{Source: "none"}, Reasons: []sqlsafety.Reason{sqlsafety.ReasonExplicitTransaction}},
			Parsed: sqlsafety.Statement{Text: text, Kind: sqlsafety.KindScript},
		}}, nil
	}
	steps := make([]guardedStep, len(check.Statements))
	for i, sc := range check.Statements {
		rs := plan.statements[i]
		step := guardedStep{Statement: sc, Parsed: rs.parsed, PrimaryKey: rs.primaryKey, Resolved: rs.resolved}
		if s.kind == kindPlugin && rs.resolved {
			database := pluginTargetDatabase(check.Database, rs.parsed.Table)
			step.Database = &database
		}
		switch {
		case sc.Undoable && sc.Kind == sqlsafety.KindData:
			q, ok := sqlsafety.SnapshotSQL(family, rs.parsed, s.safetyQualify, s.safetyQuote, rs.primaryKey, true)
			if !ok || !rs.resolved || len(rs.primaryKey) == 0 {
				return nil, fmt.Errorf("SQL 安全网无法为第 %d 条语句生成留底查询，未执行任何语句", sc.Index+1)
			}
			step.Snapshot = q
		case sc.Undoable && sc.Kind == sqlsafety.KindStructure:
			if !rs.resolved || rs.parsed.Table == nil {
				return nil, fmt.Errorf("SQL 安全网无法为第 %d 条语句生成整表留底查询，未执行任何语句", sc.Index+1)
			}
			step.Snapshot = wholeTableSnapshotSQL(s, *rs.parsed.Table)
		}
		steps[i] = step
	}
	return steps, nil
}

func scriptedStatements(statements []sqlsafety.StatementCheck) bool {
	for _, sc := range statements {
		if sc.Kind == sqlsafety.KindScript || slices.Contains(sc.Reasons, sqlsafety.ReasonExplicitTransaction) {
			return true
		}
	}
	return false
}

// emitGuardedResult streams a guarded query's final result: a result set
// as bounded chunks, an affected-rows result as the summary alone (like the
// streaming path does for statements without a result set).
func emitGuardedResult(queryID string, res *QueryResult) *QueryStreamSummary {
	summary := &QueryStreamSummary{QueryID: queryID, AffectedRows: res.AffectedRows, ExecutionMs: res.ExecutionMs, Notice: res.Notice}
	if len(res.Columns) == 0 {
		return summary
	}
	chunk := QueryResultChunk{QueryID: queryID, Columns: res.Columns, Rows: [][]any{}, EstimatedBytes: estimateColumnsBytes(res.Columns)}
	flush := func() {
		chunk.Sequence = summary.ChunkCount
		emitQueryChunk(chunk)
		summary.ChunkCount++
		chunk = QueryResultChunk{QueryID: queryID, Rows: [][]any{}, EstimatedBytes: 128}
	}
	for _, row := range res.Rows {
		rowBytes := 32
		for _, v := range row {
			rowBytes += estimateDBValueBytes(v) + 16
		}
		if len(chunk.Rows) > 0 && (len(chunk.Rows) >= dbStreamChunkRows || chunk.EstimatedBytes+rowBytes > dbStreamChunkBytes) {
			flush()
		}
		chunk.Rows = append(chunk.Rows, row)
		chunk.EstimatedBytes += rowBytes
		summary.RowCount++
	}
	if len(chunk.Rows) > 0 || summary.ChunkCount == 0 {
		flush()
	}
	return summary
}

// ── 执行器 ──

// sqlExecer is where a step runs: a pinned connection or a transaction.
type sqlExecer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

type execMode int

const (
	// execPerStatement (query window): each statement in its own
	// transaction; stop at the first failure, keeping what committed.
	execPerStatement execMode = iota
	// execBatch (grid changes, undo): all steps in one transaction, all or
	// nothing. Falls back to per statement when a structure statement would
	// commit implicitly (MySQL, Oracle family, DM) or the engine has no
	// transactions.
	execBatch
)

// guardedStep is one statement to run guarded. Statement is its assessment
// (Undoable, Risk, Kind), Parsed the resolved classification (Table,
// SetColumns, Text). An undoable data step carries Snapshot: the SELECT,
// with Args, reading the rows before the write. After, when set, re-reads
// the changed rows with the same Args; otherwise an UPDATE's after-image is
// read by the snapshot's primary keys. KeyBatches, when set, replaces Args:
// Snapshot and After run once per batch and their rows are concatenated
// (key lookups bounded by parameter count). An undoable INSERT with a
// Snapshot is a re-insert by known keys: the snapshot reads those keys
// (expected empty) and After reads the new rows back, recorded as Inserted.
// Write replaces running Parsed.Text. ParentID is set on the step's record.
type guardedStep struct {
	Statement  sqlsafety.StatementCheck
	Parsed     sqlsafety.Statement
	PrimaryKey []string
	Resolved   bool // Parsed.Table and PrimaryKey come from metadata
	Snapshot   string
	After      string
	Args       []any
	KeyBatches [][]any
	Write      func(ctx context.Context, ex sqlExecer) (int64, error)
	ParentID   string
	// Database, when set (plugin sessions), is the database the step's
	// snapshotWrite call is sent with instead of the check's: "" for a
	// target resolved in the driver's own schema (pluginTargetDatabase).
	Database *string

	// Plugin sessions (see safety_plugin.go). Wire, when set, is the
	// plugin.db.snapshotWrite steps this step is made of (its snapshots and
	// after-images included); without it the step is built from Snapshot,
	// After and Parsed.Text. WireRows maps each Wire step to the row it
	// changes, so a row written by several steps counts once. Capture
	// collects the keys an insert's returned values report. Verify, after
	// the commit, counts the rows whose before-image differs from what the
	// preview saw (an undo cannot check that before writing on a plugin).
	Wire     []ExternalSnapshotStep
	WireRows []int
	Capture  *insertCapture
	Verify   func(results []externalSnapshotStepResult) int
}

type guardedResult struct {
	Affected  []int64
	RecordIDs []string
	Last      *QueryResult
	// Overwritten (plugin undo): rows that changed after the preview and
	// were overwritten anyway; their new values are in the undo's record.
	Overwritten int
}

type guardedRun struct {
	ctx      context.Context
	s        *dbSession
	family   sqlsafety.Family
	check    *sqlsafety.Check
	steps    []guardedStep
	source   string
	settings sqlsafety.Settings
	store    safetyRecordStore
	conn     *sql.Conn
	// merge, when set (runGuardedGroup), turns the batch's records (one per
	// step, in step order, nil where a step saves none) into the one saved.
	merge func(r *guardedRun, pending []*pendingRecord) *pendingRecord
	// failedStep is the index of the step whose snapshot, write or
	// after-image failed in runBatch (the transaction was rolled back), or -1.
	failedStep int
}

// stepOutcome is a step that ran (not yet committed) and what to record.
type stepOutcome struct {
	affected int64 // -1 when unknown
	last     *QueryResult
	record   *pendingRecord
}

type pendingRecord struct {
	step guardedStep
	rec  sqlsafety.Record
	rows sqlsafety.Rows
}

// runGuarded runs steps on one pinned connection of s (after USE on MySQL
// when the check names a database) under check, recording each change with
// source. See execMode for transaction scope.
func runGuarded(ctx context.Context, s *dbSession, check *sqlsafety.Check, steps []guardedStep, mode execMode, source string) (*guardedResult, error) {
	if s.kind == kindPlugin {
		return runPluginGuarded(ctx, s, check, steps, mode, source, nil)
	}
	r, err := startGuardedRun(ctx, s, check, steps, source)
	if err != nil {
		return nil, err
	}
	defer r.conn.Close()
	if mode == execBatch && r.batchable() {
		return r.runBatch()
	}
	return r.runPerStatement()
}

// runGuardedGroup runs steps as one all-or-nothing transaction (like
// execBatch) and saves their records merged into the one merge returns
// (nil: none). Engines that cannot run the steps in one transaction are refused.
func runGuardedGroup(ctx context.Context, s *dbSession, check *sqlsafety.Check, steps []guardedStep, source string, merge func(r *guardedRun, pending []*pendingRecord) *pendingRecord) (*guardedResult, error) {
	if s.kind == kindPlugin {
		return runPluginGuarded(ctx, s, check, steps, execBatch, source, merge)
	}
	r, err := startGuardedRun(ctx, s, check, steps, source)
	if err != nil {
		return nil, err
	}
	defer r.conn.Close()
	if !r.batchable() {
		return nil, errors.New("SQL 安全网：该数据库无法在一个事务里提交这组修改，未执行")
	}
	r.merge = merge
	res, err := r.runBatch()
	if err != nil && r.failedStep >= 0 {
		return nil, &groupStepError{step: r.failedStep, err: err}
	}
	return res, err
}

// groupStepError: step (an index into the steps of runGuardedGroup) failed
// and the whole group was rolled back.
type groupStepError struct {
	step int
	err  error
}

func (e *groupStepError) Error() string { return e.err.Error() }
func (e *groupStepError) Unwrap() error { return e.err }

// startGuardedRun validates steps (fail closed: an undoable step must have
// its snapshot and a transaction) and pins the connection they run on; the
// caller closes r.conn.
func startGuardedRun(ctx context.Context, s *dbSession, check *sqlsafety.Check, steps []guardedStep, source string) (*guardedRun, error) {
	if s.kind != kindSQL || s.db == nil {
		return nil, fmt.Errorf("该会话不支持 SQL 安全网执行")
	}
	r := &guardedRun{ctx: ctx, s: s, family: s.safetyFamily(), check: check, steps: steps, source: source,
		settings: safetySettings(), store: safetyRecords(), failedStep: -1}
	if source == "undo" {
		// An undo always backs up what it overwrites, even with the safety net off.
		r.settings.Enabled = true
	}
	for _, st := range steps {
		if st.Statement.Undoable && (st.Snapshot == "" || !r.transactional()) {
			return nil, fmt.Errorf("SQL 安全网：第 %d 条可撤回语句缺少留底查询或事务，未执行任何语句", st.Statement.Index+1)
		}
	}
	var err error
	if database := mysqlEffectiveDatabase(s, check.Database); s.dialect == "mysql" && database != "" {
		// The database the check resolved names in, never whatever an
		// earlier USE left on a pooled connection.
		r.conn, err = mysqlConnUsing(ctx, s, database)
	} else if r.conn, err = s.db.Conn(ctx); err != nil {
		err = fmt.Errorf("获取连接失败: %w", err)
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// transactional: the engine has transactions and the user did not write
// their own transaction control into the query.
func (r *guardedRun) transactional() bool {
	return !r.s.safetyWithoutTransactions() && !scriptedSteps(r.steps)
}

func scriptedSteps(steps []guardedStep) bool {
	for _, st := range steps {
		if st.Statement.Kind == sqlsafety.KindScript || slices.Contains(st.Statement.Reasons, sqlsafety.ReasonExplicitTransaction) {
			return true
		}
	}
	return false
}

func (r *guardedRun) batchable() bool {
	if !r.transactional() {
		return false
	}
	switch r.s.dialect {
	case "mysql", "oracle", "yashandb", "dm":
		for _, st := range r.steps {
			if st.Statement.Kind == sqlsafety.KindStructure {
				return false
			}
		}
	}
	return true
}

func (res *guardedResult) add(out stepOutcome, ids []string) {
	res.Affected = append(res.Affected, out.affected)
	res.RecordIDs = append(res.RecordIDs, ids...)
	res.Last = out.last
}

func (r *guardedRun) runPerStatement() (*guardedResult, error) {
	res := &guardedResult{}
	writes := 0
	for _, step := range r.steps {
		out, ids, err := r.runStep(step)
		if err != nil {
			return nil, r.stopAt(writes, step, err)
		}
		res.add(out, ids)
		if step.Statement.Kind != sqlsafety.KindRead {
			writes++
		}
	}
	return res, nil
}

// stopAt reports a failed step after writes earlier writes took effect.
// None: the error itself (a reconfirm stays a reconfirm). Some: partial
// with applied = writes, since re-running the whole query would repeat them.
func (r *guardedRun) stopAt(writes int, step guardedStep, err error) error {
	if writes == 0 {
		return err
	}
	var se *safetyError
	if errors.As(err, &se) && se.Code == safetyCodeReconfirm {
		err = errors.New("该语句实际影响的行数超出确认时的范围或无法留底，已回滚未执行")
	}
	applied, failed := writes, step.Statement.Index
	return &safetyError{Code: safetyCodePartial, Applied: &applied, FailedIndex: &failed, Message: err.Error()}
}

// runStep runs one statement in per-statement mode and returns the IDs of
// the records it saved.
func (r *guardedRun) runStep(step guardedStep) (stepOutcome, []string, error) {
	switch kind := step.Statement.Kind; {
	case kind == sqlsafety.KindRead && r.transactional():
		mode := readGuardFor(r.s.dialect)
		ex, finish, err := beginReadGuard(r.ctx, r.conn, mode)
		if err != nil {
			return stepOutcome{}, nil, err
		}
		out, err := r.exec(ex, step)
		finish()
		if mode == readGuardOracle && readOnlySnapshotTooOld(err) {
			if ex, finish, err = beginReadGuard(r.ctx, r.conn, readGuardRollback); err != nil {
				return stepOutcome{}, nil, err
			}
			defer finish()
			out, err = r.exec(ex, step)
		}
		return out, nil, err
	case kind == sqlsafety.KindRead:
		out, err := r.exec(r.conn, step)
		return out, nil, err
	case kind == sqlsafety.KindStructure && step.Statement.Undoable && r.settings.Enabled:
		return r.runWholeTable(step)
	case !r.transactional() || (kind != sqlsafety.KindData && kind != sqlsafety.KindInsert):
		// Structure, procedure and unknown statements run as before, outside
		// a transaction (some engines refuse them inside one); they are
		// never undoable, so the transaction would buy nothing.
		out, err := r.exec(r.conn, step)
		if err != nil {
			return stepOutcome{}, nil, err
		}
		ids := r.saveAfterCommit([]*pendingRecord{r.unrecoverable(step, out.affected)})
		return out, ids, nil
	}
	tx, err := r.conn.BeginTx(r.ctx, nil)
	if err != nil {
		return stepOutcome{}, nil, err
	}
	out, err := r.write(tx, step)
	if err != nil {
		_ = tx.Rollback()
		return stepOutcome{}, nil, err
	}
	ids, err := r.commit(tx, []*pendingRecord{out.record})
	return out, ids, err
}

func (r *guardedRun) runBatch() (*guardedResult, error) {
	tx, err := r.conn.BeginTx(r.ctx, nil)
	if err != nil {
		return nil, err
	}
	res := &guardedResult{}
	var pending []*pendingRecord
	for i, step := range r.steps {
		out, err := r.write(tx, step)
		if err != nil {
			_ = tx.Rollback()
			r.failedStep = i
			return nil, err
		}
		res.add(out, nil)
		pending = append(pending, out.record)
	}
	if r.merge != nil {
		pending = []*pendingRecord{r.merge(r, pending)}
	}
	ids, err := r.commit(tx, pending)
	if err != nil {
		return nil, err
	}
	res.RecordIDs = ids
	return res, nil
}

// commit saves the undoable records, commits tx, then saves the audit-only
// ones. Any undoable record that cannot be saved rolls tx back (an over
// limit one as a reconfirm without undo; an insert's merely loses undo);
// records saved before a failed commit are deleted again.
func (r *guardedRun) commit(tx *sql.Tx, pending []*pendingRecord) ([]string, error) {
	var saved []string
	fail := func(err error) error {
		_ = tx.Rollback()
		r.forget(saved)
		return err
	}
	for _, p := range pending {
		if p == nil || !p.rec.Undoable {
			continue
		}
		if err := r.store.Put(p.rec, p.rows); err != nil {
			if !errors.Is(err, sqlsafety.ErrOverStoreLimit) {
				return nil, fail(fmt.Errorf("SQL 安全网留底写入失败，语句已回滚: %w", err))
			}
			if p.step.Statement.Kind != sqlsafety.KindInsert {
				return nil, fail(r.reconfirm(p.step, nil, sqlsafety.ReasonOverSnapshotLimit))
			}
			p.rec.Undoable, p.rec.UndoReason, p.rec.UndoState, p.rows = false, sqlsafety.ReasonOverSnapshotLimit, "none", sqlsafety.Rows{}
			continue
		}
		saved = append(saved, p.rec.ID)
		slog.Info("sql safety backup saved", "record", p.rec.ID, "before", len(p.rows.Before), "after", len(p.rows.After), "inserted", len(p.rows.Inserted))
	}
	if err := r.commitTx(tx); err != nil {
		if errors.Is(err, sql.ErrTxDone) || errors.Is(err, errCommitNotSent) {
			r.forget(saved) // definitely rolled back
			return nil, err
		}
		// The commit may have reached the database: never drop its backup.
		for _, id := range saved {
			if markErr := r.store.MarkUndone(id, "uncertain"); markErr != nil {
				slog.Warn("sql safety backup not marked uncertain", "record", id, "error", markErr)
			}
		}
		if len(saved) > 0 {
			return nil, fmt.Errorf("提交结果未知，已保留留底记录（状态待确认）: %w", err)
		}
		return nil, err
	}
	var ids []string
	for _, p := range pending {
		if p == nil {
			continue
		}
		if p.rec.Undoable {
			ids = append(ids, p.rec.ID)
		} else {
			ids = append(ids, r.saveAfterCommit([]*pendingRecord{p})...)
		}
	}
	return ids, nil
}

// safetyCommitHook, when set (tests only), commits tx in place of tx.Commit.
var safetyCommitHook func(tx *sql.Tx) error

// errCommitNotSent: the context ended before COMMIT was sent; the
// transaction was rolled back.
var errCommitNotSent = errors.New("SQL 安全网：提交前操作已取消，已回滚")

func (r *guardedRun) commitTx(tx *sql.Tx) error {
	if err := r.ctx.Err(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%w: %w", errCommitNotSent, err)
	}
	if safetyCommitHook != nil {
		return safetyCommitHook(tx)
	}
	return tx.Commit()
}

// saveAfterCommit stores audit-only records of statements that already
// took effect; failures are logged, not returned.
func (r *guardedRun) saveAfterCommit(pending []*pendingRecord) []string {
	var ids []string
	for _, p := range pending {
		if p == nil {
			continue
		}
		if err := r.store.Put(p.rec, p.rows); err != nil {
			slog.Warn("sql safety audit record not saved", "record", p.rec.ID, "error", err)
			continue
		}
		ids = append(ids, p.rec.ID)
	}
	return ids
}

func (r *guardedRun) forget(ids []string) {
	for _, id := range ids {
		if err := r.store.Delete(id); err != nil {
			slog.Warn("sql safety backup of a rolled back change not deleted", "record", id, "error", err)
		}
	}
}

// write runs a data, insert or read step inside a transaction and prepares
// its record. Low-risk statements that turn out to touch more rows than
// the threshold are rolled back as reconfirm.
func (r *guardedRun) write(ex sqlExecer, step guardedStep) (stepOutcome, error) {
	st := step.Statement
	if !r.settings.Enabled || st.Kind == sqlsafety.KindRead {
		return r.exec(ex, step)
	}
	if st.Undoable && step.Snapshot != "" {
		return r.snapshotWrite(ex, step)
	}
	if st.Kind == sqlsafety.KindInsert {
		return r.insertWrite(ex, step)
	}
	out, err := r.exec(ex, step)
	if err != nil {
		return stepOutcome{}, err
	}
	if st.Kind == sqlsafety.KindData && st.Risk == sqlsafety.RiskLow && out.affected > r.settings.HighRiskRows {
		return stepOutcome{}, r.reconfirm(step, &sqlsafety.Estimate{Rows: out.affected, Exact: true, Source: "count"}, "")
	}
	out.record = r.unrecoverable(step, out.affected)
	return out, nil
}

// exec runs the step's statement (or its Write) and returns its result.
func (r *guardedRun) exec(ex sqlExecer, step guardedStep) (stepOutcome, error) {
	start := time.Now()
	if step.Write != nil {
		n, err := step.Write(r.ctx, ex)
		if err != nil {
			return stepOutcome{}, err
		}
		return stepOutcome{affected: n, last: &QueryResult{Columns: []string{}, Rows: [][]any{}, AffectedRows: &n, ExecutionMs: time.Since(start).Milliseconds()}}, nil
	}
	res, err := runOn(r.ctx, ex, safetyExecText(r.family, r.s.dialect, step.Parsed.Text), start)
	if err != nil {
		return stepOutcome{}, err
	}
	affected := int64(-1)
	if res.AffectedRows != nil {
		affected = *res.AffectedRows
	}
	return stepOutcome{affected: affected, last: res}, nil
}

// snapshotWrite: snapshot (locked) → limits → write → after-image → record.
func (r *guardedRun) snapshotWrite(ex sqlExecer, step guardedStep) (stepOutcome, error) {
	st := step.Statement
	maxRows := r.settings.MaxSnapshotRows
	keep := maxRows
	if st.Risk == sqlsafety.RiskLow {
		keep = min(r.settings.HighRiskRows, maxRows)
	}
	columns, before, total, err := r.readStep(ex, step.Snapshot, step, keep, maxRows+1)
	if errors.Is(err, errSnapshotValue) {
		return stepOutcome{}, r.reconfirm(step, nil, sqlsafety.ReasonNotUndoableStore)
	}
	if err != nil {
		return stepOutcome{}, err
	}
	switch {
	case total > maxRows:
		return stepOutcome{}, r.reconfirm(step, &sqlsafety.Estimate{Rows: total, Source: "count"}, sqlsafety.ReasonOverSnapshotLimit)
	case st.Risk == sqlsafety.RiskLow && total > r.settings.HighRiskRows:
		return stepOutcome{}, r.reconfirm(step, &sqlsafety.Estimate{Rows: total, Exact: true, Source: "count"}, "")
	}
	keyAt, ok := columnPositions(columns, step.PrimaryKey)
	if !ok {
		return stepOutcome{}, r.reconfirm(step, nil, sqlsafety.ReasonNoPrimaryKey)
	}
	out, err := r.exec(ex, step)
	if err != nil {
		return stepOutcome{}, err
	}
	if st.Kind == sqlsafety.KindInsert {
		return r.reinserted(ex, step, out, keyAt)
	}
	// More rows changed than the snapshot holds (rows matched after the
	// snapshot, where locks do not cover them): the backup is incomplete.
	// SQL Server counts trigger rows too, so it is not compared there.
	if out.affected > total && !(r.family == sqlsafety.FamilySQLServer && slices.Contains(st.Reasons, sqlsafety.ReasonTrigger)) {
		return stepOutcome{}, r.reconfirm(step, &sqlsafety.Estimate{Rows: out.affected, Exact: true, Source: "count"}, "")
	}
	var after [][]any
	if len(step.Parsed.SetColumns) > 0 && len(before) > 0 { // UPDATE; DELETE has no after-image
		if step.After != "" {
			_, after, _, err = r.readStep(ex, step.After, step, 0, 0)
		} else {
			after, err = r.readByKeys(ex, step, columns, keyAt, before)
		}
		if errors.Is(err, errSnapshotValue) {
			return stepOutcome{}, r.reconfirm(step, nil, sqlsafety.ReasonNotUndoableStore)
		}
		if err != nil {
			return stepOutcome{}, err
		}
	}
	rec := r.record(step, out.affected, true, "")
	rec.PrimaryKey, rec.Columns = append([]string(nil), step.PrimaryKey...), columns
	out.record = &pendingRecord{step: step, rec: rec, rows: sqlsafety.Rows{Before: before, After: after}}
	return out, nil
}

// reinserted records a keyed re-insert: the rows After reads back by the
// step's keys, kept as Inserted primary keys so undoing it deletes them.
func (r *guardedRun) reinserted(ex sqlExecer, step guardedStep, out stepOutcome, keyAt []int) (stepOutcome, error) {
	if step.After == "" {
		return stepOutcome{}, errors.New("SQL 安全网：按主键插入缺少回读查询，已回滚")
	}
	_, rows, _, err := r.readStep(ex, step.After, step, 0, 0)
	if errors.Is(err, errSnapshotValue) {
		return stepOutcome{}, r.reconfirm(step, nil, sqlsafety.ReasonNotUndoableStore)
	}
	if err != nil {
		return stepOutcome{}, err
	}
	if out.affected >= 0 && int64(len(rows)) != out.affected {
		return stepOutcome{}, fmt.Errorf("SQL 安全网：回读到 %d 行新增数据，与写入的 %d 行不一致，已回滚", len(rows), out.affected)
	}
	keys := make([][]any, len(rows))
	for i, row := range rows {
		keys[i] = make([]any, len(keyAt))
		for j, at := range keyAt {
			keys[i][j] = row[at]
		}
	}
	rec := r.record(step, out.affected, true, "")
	rec.PrimaryKey, rec.Columns = append([]string(nil), step.PrimaryKey...), append([]string(nil), step.PrimaryKey...)
	out.record = &pendingRecord{step: step, rec: rec, rows: sqlsafety.Rows{Inserted: keys}}
	return out, nil
}

// ── 整表留底（DROP TABLE / TRUNCATE / 清空表）──

// wholeTableInTransaction: dialects that run DROP TABLE / TRUNCATE inside a
// transaction, so snapshot, backup and statement commit together. Elsewhere
// the statement commits implicitly, so the backup is saved (as uncertain)
// before it runs and becomes available once it succeeded; MySQL holds
// LOCK TABLES ... WRITE on the pinned connection from snapshot to statement.
// A variable so tests can run the implicit-commit path on SQLite.
var wholeTableInTransaction = func(dialect string) bool {
	switch dialect {
	case "postgres", "sqlite", "sqlserver":
		return true
	}
	return false
}

// runWholeTable backs up every row of the step's table (Snapshot), then runs
// the statement that drops or empties it. The record is a whole-table
// snapshot (Kind structure, Before = all rows) that undo inserts back.
func (r *guardedRun) runWholeTable(step guardedStep) (stepOutcome, []string, error) {
	if wholeTableInTransaction(r.s.dialect) {
		tx, err := r.conn.BeginTx(r.ctx, nil)
		if err != nil {
			return stepOutcome{}, nil, err
		}
		out, err := r.wholeTableWrite(tx, step)
		if err != nil {
			_ = tx.Rollback()
			return stepOutcome{}, nil, err
		}
		ids, err := r.commit(tx, []*pendingRecord{out.record})
		return out, ids, err
	}
	unlocked := false
	table := step.Parsed.Table
	if lock, unlock := wholeTableLock(r.s.dialect, r.s.safetyQualify(table.Schema, table.Name)); lock != "" {
		// Hold off writers between the snapshot and the statement. Engines
		// sharing the dialect may refuse the lock (or the account may lack
		// the privilege): go on without it and say so on the record.
		if _, err := r.conn.ExecContext(r.ctx, lock); err != nil {
			slog.Warn("sql safety table lock unavailable, backing up without it", "error", err)
			unlocked = true
		} else {
			defer r.unlockTables(unlock)
		}
	}
	p, err := r.snapshotTable(r.conn, step)
	if err != nil {
		return stepOutcome{}, nil, err
	}
	if unlocked {
		p.rec.UndoReason = sqlsafety.ReasonNoTransaction // rows written meanwhile are not covered
	}
	// Saved as uncertain until the statement is known to have run.
	p.rec.UndoState = "uncertain"
	if err := r.store.Put(p.rec, p.rows); err != nil {
		if errors.Is(err, sqlsafety.ErrOverStoreLimit) {
			return stepOutcome{}, nil, r.reconfirm(step, nil, sqlsafety.ReasonOverSnapshotLimit)
		}
		return stepOutcome{}, nil, fmt.Errorf("SQL 安全网留底写入失败，语句未执行: %w", err)
	}
	slog.Info("sql safety backup saved", "record", p.rec.ID, "before", len(p.rows.Before))
	out, err := r.exec(r.conn, step)
	if err != nil {
		if r.ctx.Err() == nil && !errors.Is(err, driver.ErrBadConn) {
			r.forget([]string{p.rec.ID}) // definitely not run
		} // else the statement may have reached the database: the backup stays uncertain

		return stepOutcome{}, nil, err
	}
	if err := r.store.MarkUndone(p.rec.ID, "available"); err != nil {
		slog.Warn("sql safety backup left uncertain", "record", p.rec.ID, "error", err)
	} else {
		p.rec.UndoState = "available"
	}
	out.record = p
	return out, []string{p.rec.ID}, nil
}

// wholeTableLock is the statement pair that locks table against writers
// on the implicit-commit path (MySQL family), or "" (a variable for tests).
var wholeTableLock = func(dialect, table string) (lock, unlock string) {
	if dialect == "mysql" {
		return "LOCK TABLES " + table + " WRITE", "UNLOCK TABLES"
	}
	return "", ""
}

// unlockTables releases the lock; a connection that may still hold it is
// discarded instead of going back to the pool.
func (r *guardedRun) unlockTables(unlock string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), 5*time.Second)
	defer cancel()
	if _, err := r.conn.ExecContext(ctx, unlock); err != nil {
		slog.Warn("sql safety unlock failed, discarding the connection", "error", err)
		_ = r.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}

// wholeTableWrite snapshots and runs a whole-table step inside tx. Postgres
// first locks the table against writers, so no row lands between the
// snapshot and the statement. Engines speaking the Postgres dialect without
// LOCK TABLE (CockroachDB) refuse it as not supported; only that refusal is
// rolled back to a savepoint and the step goes on without the lock, noted
// on the record like the MySQL path. Any other lock failure stops the step.
func (r *guardedRun) wholeTableWrite(tx *sql.Tx, step guardedStep) (stepOutcome, error) {
	unlocked := false
	if r.s.dialect == "postgres" {
		table := step.Parsed.Table
		if _, err := tx.ExecContext(r.ctx, "SAVEPOINT mincode_safety_lock"); err != nil {
			return stepOutcome{}, err
		}
		if _, err := tx.ExecContext(r.ctx, "LOCK TABLE "+r.s.safetyQualify(table.Schema, table.Name)+" IN EXCLUSIVE MODE"); err != nil {
			if !featureNotSupported(err) {
				// Lock timeout, missing privilege …: the lock exists but was
				// not granted, so the backup could miss rows; fail as before.
				return stepOutcome{}, err
			}
			if _, rbErr := tx.ExecContext(r.ctx, "ROLLBACK TO SAVEPOINT mincode_safety_lock"); rbErr != nil {
				return stepOutcome{}, err
			}
			slog.Warn("sql safety table lock unavailable, backing up without it", "error", err)
			unlocked = true
		} else if _, err := tx.ExecContext(r.ctx, "RELEASE SAVEPOINT mincode_safety_lock"); err != nil {
			return stepOutcome{}, err
		}
	}
	p, err := r.snapshotTable(tx, step)
	if err != nil {
		return stepOutcome{}, err
	}
	if unlocked {
		p.rec.UndoReason = sqlsafety.ReasonNoTransaction // rows written meanwhile may not be covered
	}
	out, err := r.exec(tx, step)
	if err != nil {
		return stepOutcome{}, err
	}
	out.record = p
	return out, nil
}

// featureNotSupported reports a Postgres-dialect error that the statement
// is not supported at all (SQLSTATE 0A000 feature_not_supported, which
// CockroachDB answers for LOCK TABLE), by SQLSTATE or by its message.
func featureNotSupported(err error) bool {
	var st sqlStater
	if errors.As(err, &st) {
		return st.SQLState() == "0A000"
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlstate 0a000") || strings.Contains(msg, "feature not supported") || strings.Contains(msg, "unimplemented")
}

// snapshotTable reads the whole table (at most MaxSnapshotRows rows, else a
// reconfirm without undo) into a pending record. The primary key is kept
// when the snapshot has its columns; without one undo inserts rows as they are.
func (r *guardedRun) snapshotTable(ex sqlExecer, step guardedStep) (*pendingRecord, error) {
	maxRows := r.settings.MaxSnapshotRows
	columns, rows, total, err := r.readRows(ex, step.Snapshot, nil, maxRows, maxRows+1)
	switch {
	case errors.Is(err, errSnapshotValue):
		return nil, r.reconfirm(step, nil, sqlsafety.ReasonNotUndoableStore)
	case err != nil:
		return nil, err
	case total > maxRows:
		return nil, r.reconfirm(step, &sqlsafety.Estimate{Rows: total, Source: "count"}, sqlsafety.ReasonOverSnapshotLimit)
	}
	rec := r.record(step, total, true, "")
	rec.Columns = columns
	if _, ok := columnPositions(columns, step.PrimaryKey); ok {
		rec.PrimaryKey = append([]string(nil), step.PrimaryKey...)
	}
	return &pendingRecord{step: step, rec: rec, rows: sqlsafety.Rows{Before: rows}}, nil
}

// safetyKeysPerBatch is how many primary keys of pkColumns columns one key
// lookup binds: at most safetyKeysPerQuery keys and safetyParamsPerQuery
// parameters (SQL Server allows 2100), at least one key.
func safetyKeysPerBatch(pkColumns int) int {
	return max(1, min(safetyKeysPerQuery, safetyParamsPerQuery/max(pkColumns, 1)))
}

// readByKeys reads columns of the rows whose primary keys (at keyAt in
// before) the snapshot captured, in batches bounded by key and parameter count.
func (r *guardedRun) readByKeys(ex sqlExecer, step guardedStep, columns []string, keyAt []int, before [][]any) ([][]any, error) {
	pk := step.PrimaryKey
	per := safetyKeysPerBatch(len(pk))
	ph := r.s.safetyPlaceholder
	var after [][]any
	for lo := 0; lo < len(before); lo += per {
		batch := before[lo:min(lo+per, len(before))]
		query := sqlsafety.ByKeysSQL(r.family, r.s.safetyQualify, r.s.safetyQuote, *step.Parsed.Table, pk, columns, len(batch), ph)
		args := make([]any, 0, len(batch)*len(pk))
		for _, row := range batch {
			for _, at := range keyAt {
				args = append(args, row[at])
			}
		}
		_, rows, _, err := r.readRows(ex, query, args, 0, 0)
		if err != nil {
			return nil, err
		}
		after = append(after, rows...)
	}
	return after, nil
}

// insertWrite runs an INSERT so its new primary keys are known where the
// dialect can report them: RETURNING (Postgres, SQLite), OUTPUT (SQL Server
// without triggers), LastInsertId (MySQL, one row, auto-increment key).
// Anything else runs as is and is recorded without undo.
func (r *guardedRun) insertWrite(ex sqlExecer, step guardedStep) (stepOutcome, error) {
	pk := step.PrimaryKey
	keyed := step.Write == nil && step.Resolved && step.Parsed.Table != nil && len(pk) > 0 &&
		!slices.Contains(step.Statement.Reasons, sqlsafety.ReasonNotUndoableStore) // a reconfirm dropped key capture
	switch {
	case keyed && (r.s.dialect == "postgres" || r.s.dialect == "sqlite" ||
		r.s.dialect == "sqlserver" && !slices.Contains(step.Statement.Reasons, sqlsafety.ReasonTrigger)):
		if query, ok := sqlsafety.InsertKeysSQL(r.family, step.Parsed, r.s.safetyQuote, pk); ok {
			return r.insertReturning(ex, step, query)
		}
	case keyed && r.s.dialect == "mysql" && step.Parsed.InsertRows == 1 && len(pk) == 1 && mysqlAutoIncrement(r.ctx, ex, *step.Parsed.Table, pk[0]):
		return r.insertLastID(ex, step)
	}
	out, err := r.exec(ex, step)
	if err != nil {
		return stepOutcome{}, err
	}
	out.record = r.unrecoverable(step, out.affected)
	return out, nil
}

func (r *guardedRun) insertReturning(ex sqlExecer, step guardedStep, query string) (stepOutcome, error) {
	start := time.Now()
	_, keys, total, err := r.readRows(ex, query, nil, r.settings.MaxSnapshotRows, -1)
	switch {
	case errors.Is(err, errSnapshotValue):
		// Roll back rather than leave the statement half-read; run again
		// without key capture once confirmed.
		return stepOutcome{}, r.reconfirm(step, nil, sqlsafety.ReasonNotUndoableStore)
	case err != nil:
		return stepOutcome{}, err
	}
	affected := total
	last := &QueryResult{Columns: []string{}, Rows: [][]any{}, AffectedRows: &affected, ExecutionMs: time.Since(start).Milliseconds()}
	out := stepOutcome{affected: affected, last: last}
	if total > r.settings.MaxSnapshotRows {
		p := r.unrecoverable(step, affected)
		p.rec.UndoReason = sqlsafety.ReasonOverSnapshotLimit
		out.record = p
		return out, nil
	}
	rec := r.record(step, affected, true, "")
	rec.PrimaryKey, rec.Columns = append([]string(nil), step.PrimaryKey...), append([]string(nil), step.PrimaryKey...)
	out.record = &pendingRecord{step: step, rec: rec, rows: sqlsafety.Rows{Inserted: keys}}
	return out, nil
}

func (r *guardedRun) insertLastID(ex sqlExecer, step guardedStep) (stepOutcome, error) {
	start := time.Now()
	res, err := ex.ExecContext(r.ctx, safetyExecText(r.family, r.s.dialect, step.Parsed.Text))
	if err != nil {
		return stepOutcome{}, err
	}
	last := execResult(res, start)
	affected := int64(-1)
	if last.AffectedRows != nil {
		affected = *last.AffectedRows
	}
	out := stepOutcome{affected: affected, last: last}
	id, idErr := res.LastInsertId()
	if affected != 1 || idErr != nil || id <= 0 {
		out.record = r.unrecoverable(step, affected)
		return out, nil
	}
	rec := r.record(step, affected, true, "")
	rec.PrimaryKey, rec.Columns = append([]string(nil), step.PrimaryKey...), append([]string(nil), step.PrimaryKey...)
	out.record = &pendingRecord{step: step, rec: rec, rows: sqlsafety.Rows{Inserted: [][]any{{id}}}}
	return out, nil
}

// mysqlAutoIncrement reports whether column is the table's AUTO_INCREMENT
// column, the only one LastInsertId describes.
func mysqlAutoIncrement(ctx context.Context, ex sqlExecer, table sqlsafety.TableRef, column string) bool {
	rows, err := ex.QueryContext(ctx, `SELECT EXTRA FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table.Schema, table.Name, column)
	if err != nil {
		return false
	}
	defer rows.Close()
	var extra sql.NullString
	if !rows.Next() || rows.Scan(&extra) != nil {
		return false
	}
	return strings.Contains(strings.ToLower(extra.String), "auto_increment")
}

// ── 记录 ──

func (r *guardedRun) record(step guardedStep, affected int64, undoable bool, reason sqlsafety.Reason) sqlsafety.Record {
	var table sqlsafety.TableRef
	switch {
	case step.Parsed.Table != nil:
		table = *step.Parsed.Table
	case step.Statement.Table != nil:
		table = *step.Statement.Table
	}
	state := "none"
	if undoable {
		state = "available"
	}
	return sqlsafety.Record{
		ID: sqlsafety.NewRecordID(), ConnectionID: r.s.connectionID, DbType: r.s.dialect, Database: r.check.Database,
		Table: table, Statement: step.Parsed.Text, Kind: step.Statement.Kind, Source: r.source, Affected: max(affected, 0),
		Undoable: undoable, UndoReason: reason, UndoState: state, ParentID: step.ParentID,
		OwnSchema: r.s.kind == kindPlugin && step.Database != nil && *step.Database == "",
	}
}

// unrecoverable is the audit-only record of a change that cannot be
// undone; nil for reads and while the safety net is disabled.
func (r *guardedRun) unrecoverable(step guardedStep, affected int64) *pendingRecord {
	if !r.settings.Enabled || step.Statement.Kind == sqlsafety.KindRead {
		return nil
	}
	return &pendingRecord{step: step, rec: r.record(step, affected, false, undoReason(step))}
}

// undoReason is the main reason a step cannot be undone.
func undoReason(step guardedStep) sqlsafety.Reason {
	for _, reason := range []sqlsafety.Reason{
		sqlsafety.ReasonOverSnapshotLimit, sqlsafety.ReasonNoTransaction, sqlsafety.ReasonNotUndoableStore,
		sqlsafety.ReasonExplicitTransaction, sqlsafety.ReasonNoPrimaryKey, sqlsafety.ReasonUnparsed,
		sqlsafety.ReasonStructure, sqlsafety.ReasonProcedure,
	} {
		if slices.Contains(step.Statement.Reasons, reason) {
			return reason
		}
	}
	switch step.Statement.Kind {
	case sqlsafety.KindStructure:
		return sqlsafety.ReasonStructure
	case sqlsafety.KindProcedure:
		return sqlsafety.ReasonProcedure
	case sqlsafety.KindInsert:
		return sqlsafety.ReasonNotUndoableStore
	case sqlsafety.KindScript:
		return sqlsafety.ReasonExplicitTransaction
	}
	return sqlsafety.ReasonUnparsed
}

// reconfirm rolls the step's statement into a new assessment — the actual
// row count (est) and/or a reason it cannot be undone — and re-issues the
// check under a fresh token for the same query, keeping the plan.
func (r *guardedRun) reconfirm(step guardedStep, est *sqlsafety.Estimate, notUndoable sqlsafety.Reason) error {
	if r.source == "undo" {
		// An undo has no check to re-issue. Its own backup cannot be stored:
		// say why, as re-previewing would fail the same way. Otherwise its
		// rows no longer match the preview.
		if notUndoable != "" {
			return errUndoUnstorable(notUndoable)
		}
		return errPreviewStale()
	}
	c := *r.check
	c.Statements = append([]sqlsafety.StatementCheck(nil), r.check.Statements...)
	j := slices.IndexFunc(c.Statements, func(sc sqlsafety.StatementCheck) bool { return sc.Index == step.Statement.Index })
	if j < 0 {
		c.Statements = append(c.Statements, step.Statement)
		j = len(c.Statements) - 1
	}
	sc := &c.Statements[j]
	reasons := append([]sqlsafety.Reason(nil), sc.Reasons...)
	if est != nil {
		risk, assessed := sqlsafety.Assess(step.Parsed, *est, r.settings)
		for _, old := range reasons {
			switch old {
			case sqlsafety.ReasonNoEstimate, sqlsafety.ReasonEstimated, sqlsafety.ReasonOverThreshold:
				continue // re-derived from the new estimate
			}
			if !slices.Contains(assessed, old) {
				assessed = append(assessed, old)
			}
		}
		sc.Estimate, sc.Risk, reasons = *est, risk, assessed
	}
	if notUndoable != "" {
		sc.Undoable = false
		if !slices.Contains(reasons, notUndoable) {
			reasons = append(reasons, notUndoable)
		}
	}
	sc.Reasons = reasons
	slog.Info("sql safety reconfirm", "statement", step.Statement.Index, "rows", sc.Estimate.Rows, "undoable", sc.Undoable)
	return r.reissue(&c)
}

// reissue re-derives c's risk, undoability and confirm target from its
// statements, re-issues it under a fresh token for the same query with the
// run's plan, and returns it as a reconfirm.
func (r *guardedRun) reissue(c *sqlsafety.Check) error {
	c.Risk = sqlsafety.RiskNone
	writes, undoable := 0, true
	for _, s := range c.Statements {
		if s.Kind != sqlsafety.KindRead {
			writes++
			undoable = undoable && s.Undoable
		}
		if s.Risk == sqlsafety.RiskHigh || (s.Risk == sqlsafety.RiskLow && c.Risk == sqlsafety.RiskNone) {
			c.Risk = s.Risk
		}
	}
	c.Undoable = writes > 0 && undoable
	c.ConfirmTarget = sqlsafety.ConfirmTarget(c.Statements)
	if err := safetyTokens.Reissue(c); err != nil {
		return fmt.Errorf("SQL 安全网无法重新签发确认: %w", err)
	}
	plan := safetyPlan{expiresAt: c.ExpiresAt, statements: make([]safetyResolvedStatement, len(r.steps))}
	for i, st := range r.steps {
		plan.statements[i] = safetyResolvedStatement{index: st.Statement.Index, parsed: st.Parsed, primaryKey: st.PrimaryKey, resolved: st.Resolved}
	}
	storeSafetyPlan(c.CheckID, plan)
	return &safetyError{Code: safetyCodeReconfirm, Check: c}
}

// ── 留底读取 ──

var errSnapshotValue = errors.New("sql safety: column value type cannot be backed up")

// readStep runs one of the step's queries with its Args, or once per
// KeyBatches entry concatenating the rows (each batch is bounded by its
// keys, so keep and countCap apply to Args only).
func (r *guardedRun) readStep(ex sqlExecer, query string, step guardedStep, keep, countCap int64) ([]string, [][]any, int64, error) {
	if step.KeyBatches == nil {
		return r.readRows(ex, query, step.Args, keep, countCap)
	}
	var columns []string
	var out [][]any
	for _, args := range step.KeyBatches {
		cols, rows, _, err := r.readRows(ex, query, args, 0, 0)
		if err != nil {
			return nil, nil, 0, err
		}
		columns = cols
		out = append(out, rows...)
	}
	if columns == nil {
		return nil, nil, 0, errors.New("SQL 安全网：按主键查询没有任何主键")
	}
	return columns, out, int64(len(out)), nil
}

// readRows runs query and returns its columns and up to keep rows (all
// when keep <= 0) converted for the store, plus how many rows it counted:
// past keep, rows are only counted, up to countCap (all when countCap <= 0).
func (r *guardedRun) readRows(ex sqlExecer, query string, args []any, keep, countCap int64) ([]string, [][]any, int64, error) {
	rows, err := ex.QueryContext(r.ctx, query, args...)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, 0, err
	}
	holders := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range holders {
		pointers[i] = &holders[i]
	}
	text := textColumns(r.s, rows)
	var out [][]any
	var total int64
	for rows.Next() {
		total++
		if keep > 0 && total > keep {
			if countCap > 0 && total >= countCap {
				break
			}
			continue
		}
		// Scanning into *any keeps driver values raw ([]byte stays bytes, copied).
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, 0, err
		}
		row := make([]any, len(holders))
		for i, v := range holders {
			if row[i], err = snapshotValue(v); err != nil {
				return nil, nil, 0, err
			}
			if b, ok := row[i].([]byte); ok && b != nil && text != nil && text[i] && utf8.Valid(b) {
				row[i] = string(b)
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}
	return columns, out, total, nil
}

// textColumns marks the result columns whose driver bytes are text and
// must be kept (and written back) as strings, or nil when there are none
// to convert.
//
// MySQL: the text protocol returns every value as bytes in the connection
// character set (utf8mb4); kept as bytes they would be written back as
// _binary literals, which MySQL stores into a latin1 / gbk column without
// conversion (mojibake) and StarRocks / Doris reject. As strings the server
// converts them to the column's character set.
//
// SQL Server: go-mssqldb returns DECIMAL / NUMERIC / MONEY as their decimal
// text in bytes; bound back as bytes they are varbinary, which SQL Server
// refuses to convert to numeric.
func textColumns(s *dbSession, rows *sql.Rows) []bool {
	if s == nil || (s.dialect != "mysql" && s.dialect != "sqlserver") {
		return nil
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil
	}
	text := make([]bool, len(types))
	for i, ct := range types {
		if s.dialect == "mysql" {
			text[i] = mysqlTextType(ct.DatabaseTypeName())
		} else {
			text[i] = sqlServerDecimalType(ct.DatabaseTypeName())
		}
	}
	return text
}

// mysqlTextType reports a go-sql-driver column type name whose bytes are
// characters; binary strings, BIT, spatial and unknown types keep bytes.
func mysqlTextType(name string) bool {
	switch strings.ToUpper(name) {
	case "", "BIT", "BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB", "GEOMETRY":
		return false
	}
	return true
}

// sqlServerDecimalType reports a go-mssqldb column type whose value comes
// back as decimal text in bytes.
func sqlServerDecimalType(name string) bool {
	switch strings.ToUpper(name) {
	case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
		return true
	}
	return false
}

// snapshotValue converts a raw driver value to a type the store keeps:
// nil, bool, signed/unsigned ints, float64, json.Number, string, []byte,
// time.Time. Driver-specific named types fall back to their kind.
func snapshotValue(v any) (any, error) {
	for range 4 { // driver.Valuer chains are short
		switch x := v.(type) {
		case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64,
			float64, json.Number, string, []byte, time.Time:
			return x, nil
		case float32:
			return float64(x), nil
		case driver.Valuer:
			next, err := x.Value()
			if err != nil {
				return nil, errSnapshotValue
			}
			v = next
			continue
		}
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.String:
			return rv.String(), nil
		case reflect.Bool:
			return rv.Bool(), nil
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return rv.Int(), nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return rv.Uint(), nil
		case reflect.Float32, reflect.Float64:
			return rv.Float(), nil
		case reflect.Slice:
			if rv.Type().Elem().Kind() == reflect.Uint8 {
				return append([]byte(nil), rv.Bytes()...), nil
			}
		}
		return nil, errSnapshotValue
	}
	return nil, errSnapshotValue
}

// columnPositions finds each primary key column among columns: exact name
// first, else the only case-insensitive match.
func columnPositions(columns, pk []string) ([]int, bool) {
	at := make([]int, len(pk))
	for i, key := range pk {
		at[i] = slices.Index(columns, key)
		if at[i] >= 0 {
			continue
		}
		for j, c := range columns {
			if strings.EqualFold(c, key) {
				if at[i] >= 0 {
					return nil, false
				}
				at[i] = j
			}
		}
		if at[i] < 0 {
			return nil, false
		}
	}
	return at, len(pk) > 0
}

// ── 方言细节 ──

func (s *dbSession) safetyQualify(schema, table string) string {
	if s.kind == kindPlugin {
		// Plugin drivers: the family's quoting; the schema is the request's database.
		if schema == "" {
			return s.safetyQuote(table)
		}
		return s.safetyQuote(schema) + "." + s.safetyQuote(table)
	}
	q, _ := qualifiedTable(s.dialect, schema, table)
	return q
}

func (s *dbSession) safetyQuote(name string) string { return quoteColumn(s.safetyDialect(), name) }

// safetyPlaceholder is the n-th (1-based) bind parameter of SQL the safety
// net generates for this session.
func (s *dbSession) safetyPlaceholder(n int) string { return placeholder(s.safetyDialect(), n) }

// safetyDialect is the built-in dialect whose quoting and placeholders the
// safety net writes this session's SQL with: a plugin driver's by its
// sqlDialect family (MySQL backticks, SQL Server brackets, double quotes
// elsewhere), a built-in session's own.
func (s *dbSession) safetyDialect() string {
	if s.kind != kindPlugin {
		return s.dialect
	}
	switch s.safetyFamily() {
	case sqlsafety.FamilyMySQL:
		return "mysql"
	case sqlsafety.FamilyPostgres:
		return "postgres"
	case sqlsafety.FamilySQLServer:
		return "sqlserver"
	case sqlsafety.FamilyOracle, sqlsafety.FamilyDM:
		return "oracle"
	}
	return "generic"
}

// wholeTableSnapshotSQL reads every row of table. Postgres reads ONLY the
// table itself (rows of child tables and partitions are not its rows); SQL
// Server holds a shared table lock to the end of the transaction.
func wholeTableSnapshotSQL(s *dbSession, table sqlsafety.TableRef) string {
	q := s.safetyQualify(table.Schema, table.Name)
	switch s.dialect {
	case "postgres":
		return "SELECT * FROM ONLY " + q
	case "sqlserver":
		return "SELECT * FROM " + q + " WITH (TABLOCK, HOLDLOCK)"
	}
	return "SELECT * FROM " + q
}

// plsqlBlock matches statements Oracle/DM parse as PL/SQL, whose final
// "END;" semicolon is part of the statement (Split drops it).
var plsqlBlock = regexp.MustCompile(`(?is)^(\s*(--[^\n]*(\n|$)|/\*.*?\*/))*\s*(BEGIN|DECLARE|CREATE\s+(OR\s+REPLACE\s+)?((NON)?EDITIONABLE\s+)?(PROCEDURE|FUNCTION|TRIGGER|PACKAGE|TYPE))\b`)

// safetyExecText is the text sent for one split statement.
func safetyExecText(family sqlsafety.Family, dialect, text string) string {
	if dialect == "trino" {
		return normalizeTrinoQuery(text)
	}
	if (family == sqlsafety.FamilyOracle || family == sqlsafety.FamilyDM) && plsqlBlock.MatchString(text) &&
		!strings.HasSuffix(strings.TrimSpace(text), ";") {
		return text + ";"
	}
	return text
}
