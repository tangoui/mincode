// SQL 安全网：查询窗口以外的写入口（db_safety_check 的 operation 参数）。
//
//	db_safety_check {sessionId, database, operation: {kind: "destructive", action, table?}}
//	db_safety_check {sessionId, database, operation: {kind: "import", table, format, data}}       // db_import_data
//	db_safety_check {sessionId, operation: {kind: "import", planId}}                              // db_data_import_start
//	db_safety_check {sessionId, database, operation: {kind: "schemaSync", sourceSessionId,
//	                 sourceDatabase, sourceFingerprint, targetFingerprint, selectedChangeIds}}    // sessionId 为目标会话
//	db_safety_check {sessionId, operation: {kind: "redis", action, dbIndex?, keys? | pattern?}}   // 见 safety_nonsql.go
//	db_safety_check {sessionId, database, operation: {kind: "rows", table, changes, namespace?}}  // db_apply_changes，见 safety_rows.go
//
// 令牌绑定会话、数据库与 operationHash(operation)（规范化 operation JSON 的 SHA-256，导入的
// data 先取摘要；changes 里的 map 按键排序编码，与前端的字段顺序无关）。执行命令按自己的
// 参数重建同一 operation 后消费令牌：缺 checkId 为
// sqlsafety:checkRequired，错配、过期、重放为 checkInvalid，高风险 confirmText 不符为
// confirmMismatch。插件会话同样要求令牌（破坏性操作与导入只警告、不留底）。
//
// 破坏性操作：清空表 / 删除表整表留底（≤ MaxSnapshotRows，不需要主键），清空 / 删除数据库
// 为高风险、不可撤回。结构同步：每条 DDL 按结构修改只警告，DROP TABLE 按整表留底。
// 导入见 safety_import.go。
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"mincode/app/backend/db/sqlsafety"
)

// safetyOperation is a write that is not query text. Its JSON (normalized)
// is what a check's token is bound to; field order is fixed by the struct.
type safetyOperation struct {
	Kind              string      `json:"kind"`
	Action            string      `json:"action,omitempty"`
	Table             string      `json:"table,omitempty"`
	Keys              []string    `json:"keys,omitempty"`
	Pattern           string      `json:"pattern,omitempty"`
	DBIndex           *int64      `json:"dbIndex,omitempty"`
	PlanID            string      `json:"planId,omitempty"`
	Format            string      `json:"format,omitempty"`
	Data              string      `json:"data,omitempty"`
	SourceSessionID   string      `json:"sourceSessionId,omitempty"`
	SourceDatabase    string      `json:"sourceDatabase,omitempty"`
	SourceFingerprint string      `json:"sourceFingerprint,omitempty"`
	TargetFingerprint string      `json:"targetFingerprint,omitempty"`
	SelectedChangeIDs []string    `json:"selectedChangeIds,omitempty"`
	Changes           []rowChange `json:"changes,omitempty"`   // kind rows, see safety_rows.go
	Namespace         string      `json:"namespace,omitempty"` // kind rows: vector namespace, kept exact
}

// normalized trims names and defaults a Redis database index to 0, so a
// check and the command it guards describe the operation identically.
// Keys and data stay exact.
func (op safetyOperation) normalized() safetyOperation {
	for _, f := range []*string{&op.Kind, &op.Action, &op.Table, &op.PlanID, &op.Format,
		&op.SourceSessionID, &op.SourceDatabase, &op.SourceFingerprint, &op.TargetFingerprint} {
		*f = strings.TrimSpace(*f)
	}
	op.Format = strings.ToLower(op.Format)
	if op.Changes != nil {
		changes := make([]rowChange, len(op.Changes))
		for i, c := range op.Changes {
			c.Op = strings.ToLower(strings.TrimSpace(c.Op))
			changes[i] = c
		}
		op.Changes = changes
	}
	if op.Kind == "redis" && op.DBIndex == nil {
		zero := int64(0)
		op.DBIndex = &zero
	}
	return op
}

// checkOperation assesses op on s and issues its token.
func checkOperation(ctx context.Context, s *dbSession, op safetyOperation, database string) (*sqlsafety.Check, error) {
	op = op.normalized()
	var (
		check *sqlsafety.Check
		plan  safetyPlan
		err   error
	)
	switch op.Kind {
	case "destructive":
		check, plan, err = checkDestructive(ctx, s, op, database)
	case "import":
		check, plan, err = checkImport(ctx, s, op, database)
	case "schemaSync":
		check, plan, err = checkSchemaSync(ctx, s, op, database)
	case "redis":
		check, plan, err = checkRedis(ctx, s, op)
	case "rows":
		check, plan, err = checkRows(ctx, s, op, database)
	default:
		return nil, fmt.Errorf("db_safety_check: 未知的操作类型 %q", op.Kind)
	}
	if err != nil {
		return nil, err
	}
	issueCheck(check, plan, operationHash(op))
	return check, nil
}

// issueCheck derives the check's risk, undoability and confirm target from
// its statements (a target already set — a database name — is kept when the
// check is high risk), issues its token bound to hash and keeps its plan.
// With the safety net disabled every statement is low risk and not undoable.
func issueCheck(check *sqlsafety.Check, plan safetyPlan, hash string) {
	if !safetySettings().Enabled {
		for i := range check.Statements {
			if sc := &check.Statements[i]; sc.Kind != sqlsafety.KindRead {
				sc.Risk, sc.Estimate, sc.Undoable = sqlsafety.RiskLow, sqlsafety.Estimate{Source: "none"}, false
			}
		}
	}
	check.Risk = sqlsafety.RiskNone
	writes, undoable := 0, true
	for _, sc := range check.Statements {
		if sc.Kind != sqlsafety.KindRead {
			writes++
			undoable = undoable && sc.Undoable
		}
		if sc.Risk == sqlsafety.RiskHigh || (sc.Risk == sqlsafety.RiskLow && check.Risk == sqlsafety.RiskNone) {
			check.Risk = sc.Risk
		}
	}
	check.Undoable = writes > 0 && undoable
	switch {
	case check.Risk != sqlsafety.RiskHigh:
		check.ConfirmTarget = ""
	case check.ConfirmTarget == "":
		check.ConfirmTarget = sqlsafety.ConfirmTarget(check.Statements)
	}
	safetyTokens.Issue(check, hash)
	plan.expiresAt = check.ExpiresAt
	storeSafetyPlan(check.CheckID, plan)
}

// assessed finishes one statement of an operation check: risk and reasons
// from sqlsafety.Assess plus extra, the snapshot limit, and undoability.
func assessed(sc *sqlsafety.StatementCheck, st sqlsafety.Statement, est sqlsafety.Estimate, undoable bool, extra []sqlsafety.Reason) {
	settings := safetySettings()
	if est.Source != "none" && est.Rows > settings.MaxSnapshotRows {
		extra = append(extra, sqlsafety.ReasonOverSnapshotLimit)
		undoable = false
	}
	risk, reasons := sqlsafety.Assess(st, est, settings)
	for _, r := range extra {
		if !containsReason(reasons, r) {
			reasons = append(reasons, r)
		}
	}
	sc.Kind, sc.Estimate, sc.Risk, sc.Reasons, sc.Undoable = st.Kind, est, risk, reasons, undoable
	sc.Text = truncateRunes(st.Text, safetyTextLimit)
}

// ── 整表留底评估 ──

var (
	cascadeKeyword  = regexp.MustCompile(`(?i)\bCASCADE\b`)
	truncateOnlyRef = regexp.MustCompile(`(?i)^\s*TRUNCATE\s+(TABLE\s+)?ONLY\b`)
)

// assessWholeTable estimates a whole-table snapshot of table (an exact
// COUNT, Postgres counting ONLY the table itself) and decides with
// wholeTableUndo whether undo can put back what text removes.
func assessWholeTable(ctx context.Context, s *dbSession, database string, table sqlsafety.TableRef, text string) (sqlsafety.Estimate, bool, []sqlsafety.Reason) {
	var partitioned, children bool
	var lookupErr error
	if s.dialect == "postgres" {
		partitioned, children, lookupErr = postgresInheritance(ctx, s, table)
	}
	undoable, reasons := wholeTableUndo(s.dialect, text, partitioned, children)
	if lookupErr != nil {
		// Unknown whether the statement also empties child tables: fail closed.
		undoable, reasons = false, []sqlsafety.Reason{sqlsafety.ReasonUnparsed}
	}
	from := s.safetyQualify(table.Schema, table.Name)
	if s.dialect == "postgres" {
		from = "ONLY " + from
	}
	est := sqlsafety.Estimate{Source: "none"}
	var n int64
	err := withEstimateTx(ctx, s, database, true, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+from).Scan(&n)
	})
	if err == nil {
		est = sqlsafety.Estimate{Rows: n, Exact: true, Source: "count"}
	}
	return est, undoable, reasons
}

// wholeTableUndo decides whether a snapshot of the table itself can undo
// text (a DROP TABLE / TRUNCATE of it). Rows it removes from other tables
// are not in the snapshot, so the statement is not undoable (ReasonCascade)
// when it says CASCADE, or on Postgres when the table is partitioned or has
// child tables the statement also empties (anything but TRUNCATE ONLY).
// Where the statement commits implicitly the snapshot is taken before it
// without a transaction: undoable, but rows written in between are not
// covered (ReasonNoTransaction); MySQL holds LOCK TABLES ... WRITE instead.
func wholeTableUndo(dialect, text string, partitioned, children bool) (bool, []sqlsafety.Reason) {
	if cascadeKeyword.MatchString(text) || partitioned || (children && !truncateOnlyRef.MatchString(text)) {
		return false, []sqlsafety.Reason{sqlsafety.ReasonCascade}
	}
	if !wholeTableInTransaction(dialect) && dialect != "mysql" {
		return true, []sqlsafety.Reason{sqlsafety.ReasonNoTransaction}
	}
	return true, nil
}

// postgresInheritance reports whether table is partitioned and whether it
// has child tables (inheritance or partitions). A failed lookup is an
// error: callers must not take it for "no children".
func postgresInheritance(ctx context.Context, s *dbSession, table sqlsafety.TableRef) (partitioned, children bool, err error) {
	schema := table.Schema
	if strings.TrimSpace(schema) == "" {
		schema = "public"
	}
	err = s.db.QueryRowContext(ctx, `SELECT c.relkind = 'p', EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhparent = c.oid)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`,
		schema, table.Name).Scan(&partitioned, &children)
	return partitioned, children, err
}

// ── 破坏性操作 ──

// destructiveTableRef names the table a destructive operation acts on the
// way records and undo address it: the database is the schema, except on
// SQLite and Firebird whose qualified names have none.
func destructiveTableRef(dialect, database, table string) sqlsafety.TableRef {
	switch dialect {
	case "sqlite", "firebird":
		return sqlsafety.TableRef{Name: table}
	}
	return sqlsafety.TableRef{Schema: database, Name: table}
}

// destructiveTableStatement is the statement drop_table / clear_table runs.
func destructiveTableStatement(dialect, action, database, table string) (string, error) {
	qualified, err := qualifiedTable(dialect, database, table)
	if err != nil {
		return "", err
	}
	if action != "clear_table" {
		return "DROP TABLE " + qualified, nil
	}
	if dialect == "sqlite" || dialect == "firebird" {
		return "DELETE FROM " + qualified, nil
	}
	return "TRUNCATE TABLE " + qualified, nil
}

// destructiveDatabaseText describes a database-wide operation for the
// confirmation: DROP DATABASE / SCHEMA, or one emptying statement per table.
func destructiveDatabaseText(ctx context.Context, s *dbSession, action, database string) (string, error) {
	if action == "drop_database" {
		return dropDatabaseStatement(s.dialect, database)
	}
	tables, err := getDatabaseTables(ctx, s, database)
	if err != nil {
		return "", fmt.Errorf("读取数据表: %w", err)
	}
	var lines []string
	for _, t := range tables {
		if strings.EqualFold(t.Kind, "table") {
			statement, err := destructiveTableStatement(s.dialect, "clear_table", database, t.Name)
			if err != nil {
				return "", err
			}
			lines = append(lines, statement)
		}
	}
	return strings.Join(lines, ";\n"), nil
}

// checkDestructive assesses db_destructive_operation. Emptying or dropping
// a table snapshots it whole; database-wide actions are confirmed by the
// database name and never undoable.
func checkDestructive(ctx context.Context, s *dbSession, op safetyOperation, database string) (*sqlsafety.Check, safetyPlan, error) {
	tableAction := op.Action == "drop_table" || op.Action == "clear_table"
	switch {
	case !tableAction && op.Action != "drop_database" && op.Action != "clear_database":
		return nil, safetyPlan{}, fmt.Errorf("不支持的危险操作: %s", op.Action)
	case tableAction && op.Table == "":
		return nil, safetyPlan{}, fmt.Errorf("数据表名称不能为空")
	case !tableAction && op.Table != "":
		return nil, safetyPlan{}, fmt.Errorf("数据库级危险操作不能指定数据表")
	case database == "":
		return nil, safetyPlan{}, fmt.Errorf("数据库名称不能为空")
	}
	check := &sqlsafety.Check{SessionID: s.sessionID, Database: database}
	st := sqlsafety.Statement{Kind: sqlsafety.KindStructure, Destructive: true}
	rs := safetyResolvedStatement{}
	est := sqlsafety.Estimate{Source: "none"}
	undoable := false
	var extra []sqlsafety.Reason
	if tableAction {
		st.Table = &sqlsafety.TableRef{Name: op.Table}
	} else {
		extra = append(extra, sqlsafety.ReasonNoWhere) // every table of the database
		check.ConfirmTarget = database
	}
	switch {
	case s.kind == kindSQL:
		if !supportsDestructiveOperation(s.dialect, op.Action) {
			return nil, safetyPlan{}, fmt.Errorf("当前数据库类型不支持该危险操作")
		}
		if !tableAction {
			text, err := destructiveDatabaseText(ctx, s, op.Action, database)
			if err != nil {
				return nil, safetyPlan{}, err
			}
			st.Text = text
			extra = append(extra, sqlsafety.ReasonStructure)
			break
		}
		text, err := destructiveTableStatement(s.dialect, op.Action, database, op.Table)
		if err != nil {
			return nil, safetyPlan{}, err
		}
		st.Text = text
		if columns, err := getTableColumns(ctx, s, database, op.Table); err != nil || len(columns) == 0 {
			return nil, safetyPlan{}, fmt.Errorf("目标数据表不存在或不是普通数据表")
		}
		ref := destructiveTableRef(s.dialect, database, op.Table)
		var pk []string
		if info, err := getPrimaryKeys(ctx, s, database, op.Table); err == nil && info != nil {
			pk = info.Columns
		}
		if s.safetyWithoutTransactions() {
			extra = append(extra, sqlsafety.ReasonNoTransaction)
		} else {
			var hints []sqlsafety.Reason
			est, undoable, hints = assessWholeTable(ctx, s, database, ref, text)
			extra = append(extra, hints...)
		}
		resolved := st
		resolved.Table = &ref
		rs = safetyResolvedStatement{parsed: resolved, primaryKey: pk, resolved: true}
	case s.kind == kindVector && op.Action == "drop_table":
		st.Text = "DROP COLLECTION " + database + "." + op.Table
		extra = append(extra, sqlsafety.ReasonNotUndoableStore)
	default:
		return nil, safetyPlan{}, fmt.Errorf("当前数据库类型不支持该危险操作")
	}
	if !rs.resolved {
		rs.parsed = st
	}
	sc := sqlsafety.StatementCheck{Table: st.Table}
	assessed(&sc, st, est, undoable, extra)
	check.Statements = []sqlsafety.StatementCheck{sc}
	return check, safetyPlan{statements: []safetyResolvedStatement{rs}}, nil
}

// destructiveGuard is the consumed check a destructive operation runs under
// (nil for direct calls).
type destructiveGuard struct {
	check *sqlsafety.Check
	plan  safetyPlan
}

// runTable runs a drop_table / clear_table statement guarded: snapshot the
// whole table, back it up, then run it (see runWholeTable).
func (g *destructiveGuard) runTable(ctx context.Context, s *dbSession, statement string) error {
	sc, rs := g.check.Statements[0], g.plan.statements[0]
	if rs.parsed.Text != strings.TrimSpace(statement) {
		return &safetyError{Code: safetyCodeCheckInvalid}
	}
	step := guardedStep{Statement: sc, Parsed: rs.parsed, PrimaryKey: rs.primaryKey, Resolved: rs.resolved}
	if sc.Undoable {
		if !rs.resolved || rs.parsed.Table == nil {
			return errors.New("SQL 安全网无法生成整表留底查询，未执行")
		}
		step.Snapshot = wholeTableSnapshotSQL(s, *rs.parsed.Table)
	}
	_, err := runGuarded(ctx, s, g.check, []guardedStep{step}, execPerStatement, "destructive")
	return err
}

// recordDone saves the audit record (no undo) of a database-wide operation that took effect.
func (g *destructiveGuard) recordDone(s *dbSession) {
	if g == nil || !safetySettings().Enabled || s.kind != kindSQL {
		return
	}
	sc, rs := g.check.Statements[0], g.plan.statements[0]
	var table sqlsafety.TableRef
	if rs.parsed.Table != nil {
		table = *rs.parsed.Table
	}
	rec := sqlsafety.Record{ID: sqlsafety.NewRecordID(), ConnectionID: s.connectionID, DbType: s.dialect, Database: g.check.Database,
		Table: table, Statement: rs.parsed.Text, Kind: sc.Kind, Source: "destructive", Undoable: false,
		UndoReason: undoReason(guardedStep{Statement: sc}), UndoState: "none"}
	if err := safetyRecords().Put(rec, sqlsafety.Rows{}); err != nil {
		slog.Warn("sql safety audit record not saved", "record", rec.ID, "error", err)
	}
}

// ── 结构同步 ──

// schemaSyncOperation is the operation db_execute_schema_sync performs.
func schemaSyncOperation(a schemaSyncArgs) safetyOperation {
	return safetyOperation{Kind: "schemaSync", SourceSessionID: a.SourceSessionID, SourceDatabase: a.SourceDatabase,
		SourceFingerprint: a.SourceFingerprint, TargetFingerprint: a.TargetFingerprint, SelectedChangeIDs: a.SelectedChangeIDs}
}

// checkSchemaSync generates the sync plan on the target session s exactly
// as execution will, and assesses each DDL statement: structure changes
// warn, DROP TABLE snapshots the table whole.
func checkSchemaSync(ctx context.Context, s *dbSession, op safetyOperation, database string) (*sqlsafety.Check, safetyPlan, error) {
	source, release, err := requireSession(op.SourceSessionID)
	if err != nil {
		return nil, safetyPlan{}, fmt.Errorf("源数据库会话: %w", err)
	}
	defer release()
	if source == s {
		return nil, safetyPlan{}, fmt.Errorf("源连接和目标连接不能相同")
	}
	comparison, err := compareSchemaSessions(ctx, source, op.SourceDatabase, s, database)
	if err != nil {
		return nil, safetyPlan{}, err
	}
	if comparison.Source.Fingerprint != op.SourceFingerprint || comparison.Target.Fingerprint != op.TargetFingerprint {
		return nil, safetyPlan{}, fmt.Errorf("结构已变化，请重新对比后再同步")
	}
	sync, err := generateSchemaSyncPlan(comparison.Target.Dialect, database, comparison.Changes, op.SelectedChangeIDs, op.SourceFingerprint, op.TargetFingerprint)
	if err != nil {
		return nil, safetyPlan{}, err
	}
	family := s.safetyFamily()
	check := &sqlsafety.Check{SessionID: s.sessionID, Database: database}
	plan := safetyPlan{}
	for i, statement := range sync.Statements {
		st := sqlsafety.Classify(family, statement.SQL)
		st.Index = i
		rs := safetyResolvedStatement{index: i, parsed: st}
		est := sqlsafety.Estimate{Source: "none"}
		undoable := false
		var extra []sqlsafety.Reason
		switch {
		case s.kind != kindSQL:
			extra = append(extra, sqlsafety.ReasonNotUndoableStore)
		case s.safetyWithoutTransactions():
			extra = append(extra, sqlsafety.ReasonNoTransaction)
		case st.Destructive && st.Table != nil && !containsReason(st.Reasons, sqlsafety.ReasonUnparsed):
			if resolved, pk, failure := resolveSafetyTarget(ctx, s, database, st); failure == "" {
				rs.parsed, rs.primaryKey, rs.resolved = resolved, pk, true
				var hints []sqlsafety.Reason
				est, undoable, hints = assessWholeTable(ctx, s, database, *resolved.Table, st.Text)
				extra = append(extra, hints...)
			} else if failure == sqlsafety.ReasonTableNotFound {
				extra = append(extra, failure)
			}
		}
		sc := sqlsafety.StatementCheck{Index: i, Table: st.Table}
		assessed(&sc, st, est, undoable, extra)
		check.Statements = append(check.Statements, sc)
		plan.statements = append(plan.statements, rs)
	}
	return check, plan, nil
}

// schemaSyncGuard runs a checked schema sync: on a host SQL target every
// statement through runGuarded (an audit record each; DROP TABLE snapshots
// the table first).
type schemaSyncGuard struct {
	target *dbSession
	check  *sqlsafety.Check
	plan   safetyPlan
}

// verify refuses a plan that is not the one the check assessed.
func (g *schemaSyncGuard) verify(statements []SchemaSyncStatement) error {
	if len(statements) != len(g.plan.statements) {
		return fmt.Errorf("同步语句与检查时不一致，请重新检查后再执行")
	}
	for i, statement := range statements {
		if strings.TrimSpace(statement.SQL) != g.plan.statements[i].parsed.Text {
			return fmt.Errorf("同步语句与检查时不一致，请重新检查后再执行")
		}
	}
	return nil
}

func (g *schemaSyncGuard) run(ctx context.Context, index int) error {
	sc, rs := g.check.Statements[index], g.plan.statements[index]
	step := guardedStep{Statement: sc, Parsed: rs.parsed, PrimaryKey: rs.primaryKey, Resolved: rs.resolved}
	if sc.Undoable {
		if !rs.resolved || rs.parsed.Table == nil {
			return errors.New("SQL 安全网无法生成整表留底查询，未执行")
		}
		step.Snapshot = wholeTableSnapshotSQL(g.target, *rs.parsed.Table)
	}
	_, err := runGuarded(ctx, g.target, g.check, []guardedStep{step}, execPerStatement, "designer")
	var se *safetyError
	if errors.As(err, &se) && se.Code == safetyCodeReconfirm {
		// The re-issued token is bound to fingerprints this sync just changed.
		return errors.New("该表数据超出留底上限或无法留底，未执行；请重新对比后再同步")
	}
	return err
}
