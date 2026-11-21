// SQL 安全网：表格修改整组提交。
//
//	db_safety_check  {sessionId, database, operation: {kind: "rows", table, changes, namespace?}}
//	db_apply_changes {sessionId, database, table, changes, checkId?, confirmText?, namespace?, source?} -> {applied, recordId?}
//	RowChange = {op: "update"|"insert"|"delete", primaryKey?: {列: 值}, values?: {列: 值}}
//	            // update：要改的列 → 新值；insert：全部字段；delete：只要 primaryKey
//
// 检查按 changes 的顺序把相邻同类修改合成一段（前端按 删除 → 修改 → 新增 发送），每段
// 一条 StatementCheck；修改 + 新增 + 删除的行数合计超过阈值为高风险，确认词为表名。
// 令牌绑定 operationHash({kind: rows, table, changes, namespace})，map 按键排序编码。
//
// 支持事务的 SQL 会话：整组在一个事务里按给定顺序执行（runGuardedGroup）：修改与删除
// 先按主键加锁快照（每批最多 500 个主键），修改后按主键回读；带完整主键的新增先确认这
// 些主键为空、插入后回读；不带主键的新增在 Postgres / SQLite / SQL Server（无触发器）
// 用 RETURNING / OUTPUT、MySQL 用自增 ID 取得新主键，取不到的整组记录不可撤回。整组
// 存为一条记录（Kind rows），可整组撤回。任一处失败整组回滚，返回 sqlsafety:partial
// （applied 0、failedIndex 为出错修改在 changes 中的位置；某段的快照或回读失败时为该段
// 第一处修改）。同一主键被多段修改（先删除再新增同一主键除外）时整组不可撤回
// （ReasonRowChangedTwice）。
//
// MongoDB、向量库、Cassandra 以及不支持事务的 SQL 引擎按顺序逐条调用单行写入，
// 遇错即停，返回 partial（applied 为已生效条数），记录不可撤回。
//
// db_update_cell / db_insert_row / db_delete_row 是单元素整组：不需要 checkId（单行
// 不会超过阈值），同样留底，记录来源取参数 source（默认 grid，AI 工具传 ai）；返回
// sqlsafety:reconfirm 时可带其中的 checkId（及 confirmText）重新提交。
// 日志只记录记录 ID 与行数，绝不记录语句文本或值。
package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/db/sqlsafety"
)

// rowChange is one staged grid change.
type rowChange struct {
	Op         string         `json:"op"`
	PrimaryKey map[string]any `json:"primaryKey,omitempty"`
	Values     map[string]any `json:"values,omitempty"`
}

const (
	rowOpUpdate = "update"
	rowOpInsert = "insert"
	rowOpDelete = "delete"
)

// rowRun is a stretch changes[lo:hi] of adjacent changes with the same op.
type rowRun struct {
	op     string
	lo, hi int
}

// rowRuns validates changes (ops already normalized) and splits them into runs.
func rowRuns(changes []rowChange) ([]rowRun, error) {
	if len(changes) == 0 {
		return nil, errors.New("没有要提交的修改")
	}
	var runs []rowRun
	for i, c := range changes {
		switch {
		case c.Op != rowOpUpdate && c.Op != rowOpInsert && c.Op != rowOpDelete:
			return nil, fmt.Errorf("第 %d 处修改：未知的操作 %q", i+1, c.Op)
		case c.Op != rowOpInsert && len(c.PrimaryKey) == 0:
			return nil, fmt.Errorf("第 %d 处修改：缺少主键", i+1)
		case c.Op != rowOpDelete && len(c.Values) == 0:
			return nil, fmt.Errorf("第 %d 处修改：没有要写入的字段", i+1)
		}
		if n := len(runs); n > 0 && runs[n-1].op == c.Op {
			runs[n-1].hi = i + 1
		} else {
			runs = append(runs, rowRun{op: c.Op, lo: i, hi: i + 1})
		}
	}
	return runs, nil
}

// rowsOperation is the operation a rows check is bound to; the namespace
// is part of it, so a vector token cannot write another namespace.
func rowsOperation(table, namespace string, changes []rowChange) safetyOperation {
	return safetyOperation{Kind: "rows", Table: table, Changes: changes, Namespace: namespace}
}

// rowsTransactional reports whether the SQL session can apply changes atomically.
func rowsTransactional(s *dbSession) bool {
	return s.kind == kindSQL && s.db != nil && !s.safetyWithoutTransactions()
}

// ── 检查 ──

// checkRows assesses a change group: one statement per run, risk by the
// total number of changes, undoable where every row can be snapshotted by
// the table's primary key (see rowsUndo).
func checkRows(ctx context.Context, s *dbSession, op safetyOperation, database string) (*sqlsafety.Check, safetyPlan, error) {
	if op.Table == "" {
		return nil, safetyPlan{}, errors.New("数据表名称不能为空")
	}
	runs, err := rowRuns(op.Changes)
	if err != nil {
		return nil, safetyPlan{}, err
	}
	settings := safetySettings()
	ref := sqlsafety.TableRef{Name: op.Table}
	var pk []string
	var u *rowsUndo
	transactional := rowsTransactional(s)
	if transactional {
		db, table, err := resolveSQLWriteTarget(ctx, s, database, op.Table)
		if err != nil {
			return nil, safetyPlan{}, err
		}
		ref = destructiveTableRef(s.dialect, db, table)
		if info, err := getPrimaryKeys(ctx, s, db, table); err == nil && info != nil {
			pk = info.Columns
		}
		u = &rowsUndo{s: s, ref: ref, pk: pk}
		if err := u.load(ctx, runs); err != nil {
			return nil, safetyPlan{}, err
		}
	}
	risk := sqlsafety.RiskLow
	var shared []sqlsafety.Reason
	if int64(len(op.Changes)) > settings.HighRiskRows {
		risk, shared = sqlsafety.RiskHigh, []sqlsafety.Reason{sqlsafety.ReasonOverThreshold}
	}
	check := &sqlsafety.Check{SessionID: s.sessionID, Database: database, ConfirmTarget: ref.Name}
	plan := safetyPlan{}
	for i, run := range runs {
		part := op.Changes[run.lo:run.hi]
		parsed := rowsStatement(s, ref, run, part)
		parsed.Index = i
		sc := sqlsafety.StatementCheck{Index: i, Text: truncateRunes(parsed.Text, safetyTextLimit), Kind: parsed.Kind, Table: &ref,
			Estimate: sqlsafety.Estimate{Rows: int64(len(part)), Exact: true, Source: "count"}, Risk: risk,
			Reasons: append([]sqlsafety.Reason(nil), shared...)}
		switch {
		case !transactional && s.kind == kindSQL:
			sc.Reasons = append(sc.Reasons, sqlsafety.ReasonNoTransaction)
		case !transactional:
			sc.Reasons = append(sc.Reasons, sqlsafety.ReasonNotUndoableStore)
		case int64(len(op.Changes)) > settings.MaxSnapshotRows:
			sc.Reasons = append(sc.Reasons, sqlsafety.ReasonOverSnapshotLimit)
		default:
			var reasons []sqlsafety.Reason
			sc.Undoable, reasons = u.assess(run, part)
			sc.Reasons = append(sc.Reasons, reasons...)
		}
		check.Statements = append(check.Statements, sc)
		plan.statements = append(plan.statements, safetyResolvedStatement{index: i, parsed: parsed, primaryKey: pk, resolved: u != nil})
	}
	if u != nil && u.overlapping(runs, op.Changes) {
		// The record could not say how to undo that row: none of it is undoable.
		for i := range check.Statements {
			sc := &check.Statements[i]
			sc.Undoable = false
			if !slices.Contains(sc.Reasons, sqlsafety.ReasonRowChangedTwice) {
				sc.Reasons = append(sc.Reasons, sqlsafety.ReasonRowChangedTwice)
			}
		}
	}
	return check, plan, nil
}

// overlapping reports a primary key that more than one run changes, other
// than a delete followed by an insert of the same key (a replaced row,
// which undo supports). Changes within one run may repeat a key.
func (u *rowsUndo) overlapping(runs []rowRun, changes []rowChange) bool {
	seen := map[string][]int{} // key → runs changing it, in order
	for i, run := range runs {
		for _, c := range changes[run.lo:run.hi] {
			var key []any
			var ok bool
			if run.op == rowOpInsert {
				key, ok = u.insertKey(c.Values)
			} else {
				key, ok = rowKey(u.pk, c.PrimaryKey)
			}
			if !ok {
				continue // such a run is not undoable anyway
			}
			k := undoKeyString(key)
			if list := seen[k]; len(list) == 0 || list[len(list)-1] != i {
				seen[k] = append(list, i)
			}
		}
	}
	for _, list := range seen {
		if len(list) == 1 {
			continue
		}
		if len(list) != 2 || runs[list[0]].op != rowOpDelete || runs[list[1]].op != rowOpInsert {
			return true
		}
	}
	return false
}

// rowsStatement describes a run as the statement each of its rows runs
// (placeholders, never values): its text, kind, table and SET columns.
func rowsStatement(s *dbSession, ref sqlsafety.TableRef, run rowRun, part []rowChange) sqlsafety.Statement {
	table := ref.Name
	quote := func(name string) string { return name }
	if s.kind == kindSQL {
		if q, err := qualifiedTable(s.dialect, ref.Schema, ref.Name); err == nil {
			table, quote = q, s.safetyQuote
		}
	}
	assign := func(columns []string, sep string) string {
		parts := make([]string, len(columns))
		for i, c := range columns {
			parts[i] = quote(c) + " = ?"
		}
		return strings.Join(parts, sep)
	}
	keys := sortedKeys(part[0].PrimaryKey)
	st := sqlsafety.Statement{Kind: sqlsafety.KindData, Table: &ref}
	switch run.op {
	case rowOpUpdate:
		st.SetColumns = unionKeys(part, func(c rowChange) map[string]any { return c.Values })
		st.Text = "UPDATE " + table + " SET " + assign(st.SetColumns, ", ") + " WHERE " + assign(keys, " AND ")
	case rowOpDelete:
		st.Text = "DELETE FROM " + table + " WHERE " + assign(keys, " AND ")
	case rowOpInsert:
		columns := unionKeys(part, func(c rowChange) map[string]any { return c.Values })
		quoted := make([]string, len(columns))
		marks := make([]string, len(columns))
		for i, c := range columns {
			quoted[i], marks[i] = quote(c), "?"
		}
		st.Kind, st.InsertRows = sqlsafety.KindInsert, int64(len(part))
		st.Text = "INSERT INTO " + table + " (" + strings.Join(quoted, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
	}
	return st
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func unionKeys(part []rowChange, of func(rowChange) map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range part {
		for _, k := range sortedKeys(of(c)) {
			if !seen[k] {
				seen[k], out = true, append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// rowsUndo decides and builds the snapshots of a group on a transactional
// SQL session.
type rowsUndo struct {
	s   *dbSession
	ref sqlsafety.TableRef
	pk  []string
	// auto: DM / Firebird auto-increment columns, which an INSERT leaves out.
	auto map[string]bool
	// hints: metadata reasons per op (cascades for update / delete, triggers).
	hints map[string][]sqlsafety.Reason
}

func (u *rowsUndo) load(ctx context.Context, runs []rowRun) error {
	u.hints = map[string][]sqlsafety.Reason{}
	for _, run := range runs {
		if _, done := u.hints[run.op]; done {
			continue
		}
		var hints []sqlsafety.Reason
		if run.op != rowOpInsert && referencedWithCascade(ctx, u.s, u.ref.Schema, u.ref.Name, run.op == rowOpUpdate) {
			hints = append(hints, sqlsafety.ReasonCascade)
		}
		if tableHasTriggers(ctx, u.s, u.ref.Schema, u.ref.Name) {
			hints = append(hints, sqlsafety.ReasonTrigger)
		}
		u.hints[run.op] = hints
		if run.op == rowOpInsert && (u.s.dialect == "dm" || u.s.dialect == "firebird") {
			var columns []ColumnInfo
			var err error
			if u.s.dialect == "dm" {
				columns, err = dmColumns(ctx, u.s, u.ref.Schema, u.ref.Name)
			} else {
				columns, err = firebirdColumns(ctx, u.s, u.ref.Name)
			}
			if err != nil {
				return fmt.Errorf("读取表列信息: %w", err)
			}
			u.auto = map[string]bool{}
			for _, c := range columns {
				if c.AutoIncrement {
					u.auto[strings.ToLower(c.Name)] = true
				}
			}
		}
	}
	return nil
}

// assess decides whether a run is undoable by snapshot: updates and
// deletes whose key is exactly the primary key (and that change no key
// column), inserts that carry their whole primary key. Inserts without one
// are not undoable by snapshot; their keys are captured where the dialect
// reports them (insertCapturable) and the record is undoable once they are.
func (u *rowsUndo) assess(run rowRun, part []rowChange) (bool, []sqlsafety.Reason) {
	reasons := append([]sqlsafety.Reason(nil), u.hints[run.op]...)
	if len(u.pk) == 0 {
		return false, append(reasons, sqlsafety.ReasonNoPrimaryKey)
	}
	for _, c := range part {
		switch run.op {
		case rowOpInsert:
			if _, ok := u.insertKey(c.Values); !ok {
				if !u.insertCapturable(reasons) {
					reasons = append(reasons, sqlsafety.ReasonNotUndoableStore)
				}
				return false, reasons
			}
		default:
			_, ok := rowKey(u.pk, c.PrimaryKey)
			if !ok || run.op == rowOpUpdate && setsKey(u.pk, c.Values) {
				return false, append(reasons, sqlsafety.ReasonNoPrimaryKey)
			}
		}
	}
	return true, reasons
}

// insertCapturable: the dialect reports the keys of rows it inserts
// (RETURNING, OUTPUT without triggers, MySQL's auto-increment ID).
func (u *rowsUndo) insertCapturable(reasons []sqlsafety.Reason) bool {
	switch u.s.dialect {
	case "postgres", "sqlite":
		return true
	case "sqlserver":
		return !slices.Contains(reasons, sqlsafety.ReasonTrigger)
	case "mysql":
		return len(u.pk) == 1
	}
	return false
}

// insertKey is the primary key an insert writes, when its fields carry all
// of it (DM / Firebird drop auto-increment columns, so those never count).
func (u *rowsUndo) insertKey(values map[string]any) ([]any, bool) {
	key, ok := rowKeyIn(u.pk, values)
	if !ok {
		return nil, false
	}
	for _, c := range u.pk {
		if u.auto[strings.ToLower(c)] {
			return nil, false
		}
	}
	return key, true
}

// rowKey is the key values of m in pk order when m names exactly the
// primary key columns (exact name, else the only case-insensitive match)
// with non-null values.
func rowKey(pk []string, m map[string]any) ([]any, bool) {
	if len(m) != len(pk) {
		return nil, false
	}
	return rowKeyIn(pk, m)
}

// rowKeyIn is rowKey for a map that may hold other columns too.
func rowKeyIn(pk []string, m map[string]any) ([]any, bool) {
	if len(pk) == 0 {
		return nil, false
	}
	key := make([]any, len(pk))
	for i, c := range pk {
		v, ok := m[c]
		if !ok {
			found := 0
			for name, value := range m {
				if strings.EqualFold(name, c) {
					v, found = value, found+1
				}
			}
			if found != 1 {
				return nil, false
			}
		}
		if v == nil {
			return nil, false
		}
		key[i] = jsonToSQLValue(v)
	}
	return key, true
}

func setsKey(pk []string, values map[string]any) bool {
	for name := range values {
		for _, c := range pk {
			if strings.EqualFold(name, c) {
				return true
			}
		}
	}
	return false
}

// ── 执行 ──

type rowsRequest struct {
	database  string // as the token binds it
	table     string
	changes   []rowChange
	namespace string
	source    string
}

type rowsOutcome struct {
	Applied  int    `json:"applied"`
	RecordID string `json:"recordId,omitempty"`
	affected int64
}

// rowsFailure: changes[index] failed after applied earlier changes took effect.
type rowsFailure struct {
	applied, index int
	err            error
}

func (e *rowsFailure) Error() string { return e.err.Error() }
func (e *rowsFailure) Unwrap() error { return e.err }

// applyRowChanges runs a change group under its check: the token the
// caller presents (always required when tokenRequired), else one assessed
// and spent here for a single row.
func applyRowChanges(ctx context.Context, s *dbSession, req rowsRequest, checkID, confirmText *string, tokenRequired bool) (rowsOutcome, error) {
	for i := range req.changes {
		req.changes[i].Op = strings.ToLower(strings.TrimSpace(req.changes[i].Op))
	}
	runs, err := rowRuns(req.changes)
	if err != nil {
		return rowsOutcome{}, err
	}
	op := rowsOperation(req.table, req.namespace, req.changes)
	hash := operationHash(op)
	var check *sqlsafety.Check
	var plan safetyPlan
	switch {
	case tokenRequired || strings.TrimSpace(optionalString(checkID)) != "":
		check, plan, err = consumeCheck(s, checkID, confirmText, req.database, hash)
	case rowsTransactional(s):
		var issued *sqlsafety.Check
		if issued, err = checkOperation(ctx, s, op, req.database); err == nil {
			check, plan, err = consumeCheck(s, &issued.CheckID, nil, req.database, hash)
		}
	}
	if err != nil {
		return rowsOutcome{}, err
	}
	if !rowsTransactional(s) {
		return applyRowsInOrder(ctx, s, req, runs)
	}
	return applyRowsInTransaction(ctx, s, check, plan, req, runs)
}

// rowsStepsHook, when set (tests only), sees a group's steps before they run.
var rowsStepsHook func(steps []guardedStep)

// insertCapture collects the keys a capturing insert run reported.
type insertCapture struct {
	keys    [][]any
	missing bool  // some inserted row's key is unknown
	autoKey *bool // MySQL: the key is the AUTO_INCREMENT column (looked up once)
}

func applyRowsInTransaction(ctx context.Context, s *dbSession, check *sqlsafety.Check, plan safetyPlan, req rowsRequest, runs []rowRun) (rowsOutcome, error) {
	if len(check.Statements) != len(runs) || len(plan.statements) != len(runs) || plan.statements[0].parsed.Table == nil {
		return rowsOutcome{}, &safetyError{Code: safetyCodeCheckInvalid}
	}
	ref := *plan.statements[0].parsed.Table
	pk := plan.statements[0].primaryKey
	// destructiveTableRef keeps the database argument the row helpers take as Schema.
	database, table := ref.Schema, ref.Name
	u := &rowsUndo{s: s, ref: ref, pk: pk}
	failed := -1
	steps := make([]guardedStep, len(runs))
	captures := make([]*insertCapture, len(runs))
	for i, run := range runs {
		sc, rs := check.Statements[i], plan.statements[i]
		part := req.changes[run.lo:run.hi]
		step := guardedStep{Statement: sc, Parsed: rs.parsed, PrimaryKey: rs.primaryKey, Resolved: rs.resolved}
		if sc.Undoable {
			keys := make([][]any, len(part))
			for j, c := range part {
				var ok bool
				if run.op == rowOpInsert {
					keys[j], ok = u.insertKey(c.Values)
				} else {
					keys[j], ok = rowKey(pk, c.PrimaryKey)
				}
				if !ok {
					return rowsOutcome{}, &safetyError{Code: safetyCodeCheckInvalid}
				}
			}
			q, err := undoKeyQuery(s, ref, pk, nil, keys, true)
			if err != nil {
				return rowsOutcome{}, err
			}
			step.Snapshot, step.KeyBatches = q.sql, q.batches
			if run.op == rowOpInsert {
				step.After = q.sql // the keys are free before and hold the new rows after
			}
		}
		var capture *insertCapture
		if run.op == rowOpInsert && !sc.Undoable && len(pk) > 0 && u.insertCapturable(sc.Reasons) {
			capture = &insertCapture{}
			captures[i] = capture
		}
		lo, op := run.lo, run.op
		step.Write = func(ctx context.Context, ex sqlExecer) (int64, error) {
			var total int64
			seen := map[string]bool{}
			for j, c := range part {
				n, err := writeRowChange(ctx, ex, s, database, table, pk, op, c, capture)
				if err != nil {
					failed = lo + j
					return 0, err
				}
				// A row changed twice counts once, as in the snapshot.
				if key, ok := rowKey(pk, c.PrimaryKey); ok && op != rowOpInsert {
					if seen[undoKeyString(key)] {
						continue
					}
					seen[undoKeyString(key)] = true
				}
				total += n
			}
			return total, nil
		}
		steps[i] = step
	}
	merge := func(r *guardedRun, pending []*pendingRecord) *pendingRecord {
		return mergeRowsRecord(r, ref, pk, steps, pending, captures)
	}
	if rowsStepsHook != nil {
		rowsStepsHook(steps)
	}
	res, err := runGuardedGroup(ctx, s, check, steps, req.source, merge)
	if err != nil {
		var se *safetyError
		if errors.As(err, &se) {
			return rowsOutcome{}, se // reconfirm and the like: not a failed change
		}
		// A run's snapshot or after-image failing is reported at its first change.
		var stepErr *groupStepError
		if errors.As(err, &stepErr) {
			index := runs[stepErr.step].lo
			if failed >= 0 {
				index = failed
			}
			return rowsOutcome{}, &rowsFailure{applied: 0, index: index, err: stepErr.err}
		}
		return rowsOutcome{}, err
	}
	out := rowsOutcome{Applied: len(req.changes)}
	for _, n := range res.Affected {
		out.affected += max(n, 0)
	}
	if len(res.RecordIDs) > 0 {
		out.RecordID = res.RecordIDs[0]
	}
	return out, nil
}

// writeRowChange runs one change on ex and returns the rows it changed (an
// update's columns are set one by one; the row counts once). A capturing
// insert also collects the new row's key.
func writeRowChange(ctx context.Context, ex sqlExecer, s *dbSession, database, table string, pk []string, op string, c rowChange, capture *insertCapture) (int64, error) {
	switch op {
	case rowOpUpdate:
		var affected int64
		for _, column := range sortedKeys(c.Values) {
			n, err := updateCellOn(ctx, ex, s, database, table, c.PrimaryKey, column, c.Values[column])
			if err != nil {
				return 0, err
			}
			affected = max(affected, n)
		}
		return affected, nil
	case rowOpDelete:
		return deleteRowOn(ctx, ex, s, database, table, c.PrimaryKey)
	}
	if capture == nil {
		return insertRowOn(ctx, ex, s, database, table, c.Values)
	}
	return insertCapturing(ctx, ex, s, database, table, pk, c.Values, capture)
}

// insertCapturing inserts one row and records its primary key: RETURNING /
// OUTPUT on Postgres, SQLite and SQL Server, the auto-increment ID (or the
// key the row carries) on MySQL.
func insertCapturing(ctx context.Context, ex sqlExecer, s *dbSession, database, table string, pk []string, values map[string]any, capture *insertCapture) (int64, error) {
	query, args, err := insertRowStatement(ctx, s, database, table, values)
	if err != nil {
		return 0, err
	}
	if s.dialect != "mysql" {
		family := s.safetyFamily()
		if keyed, ok := sqlsafety.InsertKeysSQL(family, sqlsafety.Classify(family, query), s.safetyQuote, pk); ok {
			_, keys, _, err := (&guardedRun{ctx: ctx}).readRows(ex, keyed, args, 0, 0)
			if err != nil {
				return 0, err
			}
			capture.keys = append(capture.keys, keys...)
			return int64(len(keys)), nil
		}
	}
	res, err := ex.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	switch key, given := rowKeyIn(pk, values); {
	case s.dialect != "mysql" || n != 1:
		capture.missing = true
	case given:
		capture.keys = append(capture.keys, key)
	default:
		if capture.autoKey == nil {
			auto := mysqlAutoIncrement(ctx, ex, sqlsafety.TableRef{Schema: database, Name: table}, pk[0])
			capture.autoKey = &auto
		}
		id, err := res.LastInsertId()
		if err != nil || id <= 0 || !*capture.autoKey {
			capture.missing = true
		} else {
			capture.keys = append(capture.keys, []any{id})
		}
	}
	return n, nil
}

// mergeRowsRecord turns the records of a group's runs into its one record
// (Kind rows): Before / After of the updated and deleted rows, Inserted
// primary keys (PrimaryKey order) of the new ones. Any run that cannot be
// undone makes the whole record audit-only. nil while the safety net is off.
func mergeRowsRecord(r *guardedRun, ref sqlsafety.TableRef, pk []string, steps []guardedStep, pending []*pendingRecord, captures []*insertCapture) *pendingRecord {
	var (
		affected                int64
		columns                 []string
		before, after, inserted [][]any
		undoable                = true
		reason                  sqlsafety.Reason
		recorded                bool
		texts                   []string
	)
	notUndoable := func(why sqlsafety.Reason) {
		if undoable {
			undoable, reason = false, why
		}
	}
	for i, p := range pending {
		texts = append(texts, steps[i].Parsed.Text)
		if p == nil {
			continue
		}
		recorded = true
		affected += p.rec.Affected
		switch {
		case p.rec.Undoable && steps[i].Statement.Kind == sqlsafety.KindInsert:
			inserted = append(inserted, p.rows.Inserted...)
		case p.rec.Undoable:
			if columns != nil && !slices.Equal(columns, p.rec.Columns) {
				notUndoable(sqlsafety.ReasonUnparsed)
			}
			columns = p.rec.Columns
			before, after = append(before, p.rows.Before...), append(after, p.rows.After...)
		case captures[i] != nil && !captures[i].missing && int64(len(captures[i].keys)) == p.rec.Affected:
			inserted = append(inserted, captures[i].keys...)
		default:
			notUndoable(p.rec.UndoReason)
		}
	}
	if !recorded {
		return nil
	}
	if columns == nil {
		columns = pk
	}
	rec := r.record(steps[0], affected, undoable, reason)
	rec.Kind, rec.Table, rec.Statement = sqlsafety.KindRows, ref, strings.Join(texts, ";\n")
	if !undoable {
		return &pendingRecord{step: steps[0], rec: rec}
	}
	rec.PrimaryKey, rec.Columns = append([]string(nil), pk...), append([]string(nil), columns...)
	return &pendingRecord{step: steps[0], rec: rec, rows: sqlsafety.Rows{Before: before, After: after, Inserted: inserted}}
}

// applyRowsInOrder runs the changes one by one through the single-row
// writers of sessions without a guarded transaction, stopping at the first
// failure, and saves one audit record of what took effect.
func applyRowsInOrder(ctx context.Context, s *dbSession, req rowsRequest, runs []rowRun) (rowsOutcome, error) {
	out := rowsOutcome{}
	var failure error
	for i, c := range req.changes {
		n, err := writeRowDirect(ctx, s, req, c)
		if err != nil {
			failure = &rowsFailure{applied: out.Applied, index: i, err: err}
			break
		}
		out.Applied++
		out.affected += n
	}
	if out.Applied > 0 && safetySettings().Enabled {
		out.RecordID = saveRowsAudit(s, req, runs, out)
	}
	return out, failure
}

func writeRowDirect(ctx context.Context, s *dbSession, req rowsRequest, c rowChange) (int64, error) {
	vector := s.vector != nil && s.kind == kindVector
	switch c.Op {
	case rowOpUpdate:
		var affected int64
		for _, column := range sortedKeys(c.Values) {
			var n int64
			var err error
			if vector {
				n, err = vectorUpdateCell(ctx, s, req.table, c.PrimaryKey, column, c.Values[column], req.namespace)
			} else {
				n, err = updateCellByPrimaryKey(ctx, s, req.database, req.table, c.PrimaryKey, column, c.Values[column])
			}
			if err != nil {
				return 0, err
			}
			affected = max(affected, n)
		}
		return affected, nil
	case rowOpDelete:
		if vector {
			return vectorDeleteRow(ctx, s, req.table, c.PrimaryKey, req.namespace)
		}
		return deleteRowByPrimaryKey(ctx, s, req.database, req.table, c.PrimaryKey)
	}
	if vector {
		return vectorInsertRow(ctx, s, req.table, c.Values, req.namespace)
	}
	return insertRow(ctx, s, req.database, req.table, c.Values)
}

// saveRowsAudit stores the audit-only record of changes applied in order.
func saveRowsAudit(s *dbSession, req rowsRequest, runs []rowRun, out rowsOutcome) string {
	reason := sqlsafety.ReasonNotUndoableStore
	if s.kind == kindSQL {
		reason = sqlsafety.ReasonNoTransaction
	}
	ref := sqlsafety.TableRef{Name: req.table}
	texts := make([]string, len(runs))
	for i, run := range runs {
		texts[i] = rowsStatement(s, ref, run, req.changes[run.lo:run.hi]).Text
	}
	rec := sqlsafety.Record{ID: sqlsafety.NewRecordID(), ConnectionID: s.connectionID, DbType: s.dialect, Database: req.database,
		Table: ref, Statement: strings.Join(texts, ";\n"), Kind: sqlsafety.KindRows, Source: req.source, Affected: out.affected,
		Undoable: false, UndoReason: reason, UndoState: "none"}
	if err := safetyRecords().Put(rec, sqlsafety.Rows{}); err != nil {
		slog.Warn("sql safety audit record not saved", "record", rec.ID, "error", err)
		return ""
	}
	return rec.ID
}

// ── 命令 ──

var rowsSources = map[string]bool{"grid": true, "ai": true}

func rowsSource(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "grid"
	}
	if !rowsSources[source] {
		return "", fmt.Errorf("未知的来源 %q", source)
	}
	return source, nil
}

type applyChangesArgs struct {
	SessionID   string      `json:"sessionId"`
	Database    string      `json:"database"`
	Table       string      `json:"table"`
	Changes     []rowChange `json:"changes"`
	CheckID     *string     `json:"checkId"`
	ConfirmText *string     `json:"confirmText"`
	Namespace   string      `json:"namespace"`
	Source      string      `json:"source"`
}

func dbApplyChangesHandler(callCtx context.Context, _, argsJSON string) (string, error) {
	var a applyChangesArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	source, err := rowsSource(a.Source)
	if err != nil {
		return "", err
	}
	s, release, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	defer release()
	ctx, cancel := context.WithTimeout(callCtx, time.Duration(dbDMLTimeoutSecs)*time.Second)
	defer cancel()
	req := rowsRequest{database: strings.TrimSpace(a.Database), table: strings.TrimSpace(a.Table), changes: a.Changes, namespace: a.Namespace, source: source}
	res, err := applyRowChanges(ctx, s, req, a.CheckID, a.ConfirmText, true)
	var failure *rowsFailure
	if errors.As(err, &failure) {
		applied, index := failure.applied, failure.index
		return "", &safetyError{Code: safetyCodePartial, Applied: &applied, FailedIndex: &index, Message: failure.err.Error()}
	}
	if err != nil {
		return "", err
	}
	slog.Info("sql safety grid changes applied", "changes", res.Applied, "record", res.RecordID)
	return core.EncodeResult(res)
}

// applySingleRow runs one change of the single-row commands as a group of
// one and returns the rows it changed; a failure is the change's own error.
// No token is needed; one is consumed when given (resubmitting after a
// reconfirm, whose check is bound to the same rows operation).
func applySingleRow(ctx context.Context, s *dbSession, database, table, namespace, source string, checkID, confirmText *string, c rowChange) (int64, error) {
	source, err := rowsSource(source)
	if err != nil {
		return 0, err
	}
	req := rowsRequest{database: strings.TrimSpace(database), table: strings.TrimSpace(table), changes: []rowChange{c}, namespace: namespace, source: source}
	res, err := applyRowChanges(ctx, s, req, checkID, confirmText, false)
	var failure *rowsFailure
	if errors.As(err, &failure) {
		return 0, failure.err
	}
	return res.affected, err
}
