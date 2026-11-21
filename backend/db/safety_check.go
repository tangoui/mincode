// SQL 安全网：执行前检查（db_safety_check）与影响行数估算。
//
//	db_safety_check {sessionId, query, database?, source?} -> sqlsafety.Check
//	db_safety_check {sessionId, operation, database?, source?} -> sqlsafety.Check（见 safety_operations.go）
//
// 查询文本先按方言拆成语句，逐条分类；全部只读时直接返回 risk none、不签发令牌。
// 否则对每条 UPDATE/DELETE 按元数据解析表名与列名、取主键、估算行数（COUNT →
// 执行计划 → 无法估算），再按设置判定风险并签发一次性令牌。解析后的表名、列名与
// 主键只留在本包（safetyPlans），供执行阶段生成留底语句，不返回前端。
package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/backend/db/sqlsafety"
)

const (
	safetyEstimateTimeout = 5 * time.Second
	safetyCheckTimeout    = 30 * time.Second
	safetyTextLimit       = 2000 // characters of statement text sent to the frontend
	safetyExplainMaxRows  = 1000
	maxSafetyPlans        = 256
)

// safetyTokens issues the confirmation tokens execution must present.
var safetyTokens = sqlsafety.NewTokens(5*time.Minute, time.Now)

var safetySources = map[string]bool{"query": true, "ai": true, "designer": true, "import": true, "grid": true, "e2e": true}

// ── 设置缓存 ──

var safetySettingsState struct {
	mu       sync.Mutex
	loaded   bool
	settings sqlsafety.Settings
}

// safetySettings returns the cached SQL safety settings, loading them from
// sqlsafety.Dir() on first use. Unreadable settings fall back to defaults.
func safetySettings() sqlsafety.Settings {
	safetySettingsState.mu.Lock()
	defer safetySettingsState.mu.Unlock()
	if !safetySettingsState.loaded {
		settings := sqlsafety.DefaultSettings()
		if dir := sqlsafety.Dir(); dir != "" {
			loaded, err := sqlsafety.LoadSettings(dir)
			if err != nil {
				slog.Warn("sql safety settings unreadable, using defaults", "error", err)
			}
			settings = loaded
		}
		safetySettingsState.settings = settings
		safetySettingsState.loaded = true
	}
	return safetySettingsState.settings
}

// setSafetySettings replaces the cached settings; call it after saving them.
func setSafetySettings(s sqlsafety.Settings) {
	safetySettingsState.mu.Lock()
	defer safetySettingsState.mu.Unlock()
	safetySettingsState.settings = s
	safetySettingsState.loaded = true
}

// ── 检查结果的内部计划（不返回前端）──

// safetyResolvedStatement is one statement of a check as execution needs it.
// For a resolved UPDATE/DELETE, parsed.Table and parsed.SetColumns carry the
// names exactly as the database spells them (schema filled in), ready for
// sqlsafety.CountSQL/SnapshotSQL, and primaryKey the table's key columns.
type safetyResolvedStatement struct {
	index      int
	parsed     sqlsafety.Statement
	primaryKey []string
	resolved   bool
}

type safetyPlan struct {
	expiresAt  time.Time
	statements []safetyResolvedStatement
	// forceWrite: text the classifier called read-only but the read guard
	// refused as a write (see forceWriteRequired); run it unguarded.
	forceWrite bool
}

var safetyPlans = struct {
	mu    sync.Mutex
	plans map[string]safetyPlan
}{plans: map[string]safetyPlan{}}

func storeSafetyPlan(checkID string, plan safetyPlan) {
	safetyPlans.mu.Lock()
	defer safetyPlans.mu.Unlock()
	now := time.Now()
	for id, p := range safetyPlans.plans {
		if now.After(p.expiresAt) {
			delete(safetyPlans.plans, id)
		}
	}
	for len(safetyPlans.plans) >= maxSafetyPlans {
		oldest := ""
		for id, p := range safetyPlans.plans {
			if oldest == "" || p.expiresAt.Before(safetyPlans.plans[oldest].expiresAt) {
				oldest = id
			}
		}
		delete(safetyPlans.plans, oldest)
	}
	safetyPlans.plans[checkID] = plan
}

// takeSafetyPlan returns and forgets the resolved plan of a check. Call it
// after safetyTokens.Consume has accepted the same checkID.
func takeSafetyPlan(checkID string) (safetyPlan, bool) {
	safetyPlans.mu.Lock()
	defer safetyPlans.mu.Unlock()
	p, ok := safetyPlans.plans[checkID]
	delete(safetyPlans.plans, checkID)
	if !ok || time.Now().After(p.expiresAt) {
		return safetyPlan{}, false
	}
	return p, true
}

// ── 命令 ──

type safetyCheckArgs struct {
	SessionID string  `json:"sessionId"`
	Query     string  `json:"query"`
	Database  *string `json:"database"`
	Source    string  `json:"source"`
	// Operation, when query is empty, checks a write that is not query
	// text (destructive / import / schemaSync / redis).
	Operation *safetyOperation `json:"operation"`
}

func dbSafetyCheckHandler(callCtx context.Context, _, argsJSON string) (string, error) {
	var a safetyCheckArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	source := strings.TrimSpace(a.Source)
	if source == "" {
		source = "query"
	}
	if !safetySources[source] {
		return "", fmt.Errorf("db_safety_check: 未知的来源 %q", source)
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(callCtx, safetyCheckTimeout)
	defer cancel()
	start := time.Now()
	operation := ""
	var check *sqlsafety.Check
	switch {
	case strings.TrimSpace(a.Query) == "" && a.Operation != nil:
		operation = a.Operation.Kind
		check, err = checkOperation(ctx, s, *a.Operation, safetyDatabase(a.Database))
	case nonSQLQuerySession(s):
		check = checkNonSQLQuery(s, a.Query, safetyDatabase(a.Database))
	default:
		check, err = checkStatements(ctx, s, a.Query, safetyDatabase(a.Database))
	}
	if err != nil {
		return "", err
	}
	// Never log statement text, keys or values: only counts and outcome.
	slog.Debug("sql safety check", "source", source, "operation", operation, "statements", len(check.Statements), "risk", check.Risk, "undoable", check.Undoable, "duration_ms", time.Since(start).Milliseconds())
	return core.EncodeResult(check)
}

// safetyDatabase normalizes the optional database argument the way checks
// bind it; execution must use the same value when consuming the token.
func safetyDatabase(d *string) string {
	if v := trimmedOptional(d); v != nil {
		return *v
	}
	return ""
}

// queryHash is the hex SHA-256 of the exact query text a check assessed.
func queryHash(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:])
}

// operationHash is the hash a check of op binds its token to: SHA-256 of
// the normalized operation JSON with data replaced by its own hash.
func operationHash(op safetyOperation) string {
	op = op.normalized()
	if op.Data != "" {
		op.Data = "sha256:" + queryHash(op.Data)
	}
	b, err := json.Marshal(op)
	if err != nil { // plain strings and slices always encode
		panic(err)
	}
	return queryHash("operation:" + string(b))
}

// safetyFamily is the SQL family the safety net parses this session's SQL as.
func (s *dbSession) safetyFamily() sqlsafety.Family {
	switch s.kind {
	case kindSQL:
		return sqlsafety.FamilyFor(s.dialect)
	}
	return sqlsafety.FamilyNone
}

// safetyWithoutTransactions reports sessions whose engine has no
// transactions to snapshot and write in.
func (s *dbSession) safetyWithoutTransactions() bool {
	switch s.dialect {
	case "clickhouse", "trino", "tdengine":
		return true
	}
	if s.engine == "materialize" {
		// Postgres dialect, but UPDATE / DELETE cannot run in a transaction block.
		return true
	}
	return s.safetyFamily() == sqlsafety.FamilyNone
}

// checkStatements assesses every statement of query (the whole editor text)
// on s. All-read input yields risk none and no token; anything else gets a
// token bound to the session, database and queryHash(query).
func checkStatements(ctx context.Context, s *dbSession, query, database string) (*sqlsafety.Check, error) {
	family := s.safetyFamily()
	segments := sqlsafety.Split(family, query)
	parsed := make([]sqlsafety.Statement, len(segments))
	readOnly, script := true, false
	for i, segment := range segments {
		st := sqlsafety.Classify(family, segment)
		st.Index = i
		parsed[i] = st
		if st.Kind != sqlsafety.KindRead {
			readOnly = false
		}
		if safetyScriptStatement(family, len(segments), st) {
			script = true
		}
	}

	check := &sqlsafety.Check{SessionID: s.sessionID, Database: database, Risk: sqlsafety.RiskNone, Statements: make([]sqlsafety.StatementCheck, 0, len(parsed))}
	if readOnly {
		for _, st := range parsed {
			check.Statements = append(check.Statements, sqlsafety.StatementCheck{Index: st.Index, Text: truncateRunes(st.Text, safetyTextLimit), Kind: st.Kind,
				Estimate: sqlsafety.Estimate{Source: "none"}, Risk: sqlsafety.RiskNone})
		}
		return check, nil
	}
	plan := safetyPlan{statements: make([]safetyResolvedStatement, 0, len(parsed))}
	settings := safetySettings()
	writes, undoable := 0, true
	for _, st := range parsed {
		sc := sqlsafety.StatementCheck{Index: st.Index, Text: truncateRunes(st.Text, safetyTextLimit), Kind: st.Kind, Table: st.Table,
			Estimate: sqlsafety.Estimate{Source: "none"}, Risk: sqlsafety.RiskNone}
		rs := safetyResolvedStatement{index: st.Index, parsed: st}
		if st.Kind != sqlsafety.KindRead {
			writes++
			if !settings.Enabled {
				// Disabled: classification only; the frontend falls back to the generic confirm.
				sc.Risk, sc.Estimate = sqlsafety.RiskLow, sqlsafety.Estimate{Source: "none"}
				sc.Reasons = append([]sqlsafety.Reason(nil), st.Reasons...)
			} else {
				rs = assessStatement(ctx, s, family, database, settings, script, st, &sc)
			}
			undoable = undoable && sc.Undoable
		}
		check.Statements = append(check.Statements, sc)
		plan.statements = append(plan.statements, rs)
		if sc.Risk == sqlsafety.RiskHigh || (sc.Risk == sqlsafety.RiskLow && check.Risk == sqlsafety.RiskNone) {
			check.Risk = sc.Risk
		}
	}
	check.Undoable = writes > 0 && undoable
	check.ConfirmTarget = sqlsafety.ConfirmTarget(check.Statements)
	safetyTokens.Issue(check, queryHash(query))
	plan.expiresAt = check.ExpiresAt
	storeSafetyPlan(check.CheckID, plan)
	return check, nil
}

// safetyScriptStatement reports a statement that makes the whole query a
// script the user controls: explicit transaction statements, and on SQL
// Server a DECLARE among several statements (its variables live only within
// one batch, so the query must run as one batch, without per-statement
// transactions or snapshots).
func safetyScriptStatement(family sqlsafety.Family, statements int, st sqlsafety.Statement) bool {
	if st.Kind == sqlsafety.KindScript {
		return true
	}
	return family == sqlsafety.FamilySQLServer && statements > 1 && st.Kind == sqlsafety.KindProcedure &&
		strings.HasPrefix(strings.ToUpper(strings.TrimSpace(st.Text)), "DECLARE")
}

// assessStatement fills sc for one non-read statement: name resolution,
// primary key, metadata hints, row estimate, risk and undoability.
func assessStatement(ctx context.Context, s *dbSession, family sqlsafety.Family, database string, settings sqlsafety.Settings, script bool, st sqlsafety.Statement, sc *sqlsafety.StatementCheck) safetyResolvedStatement {
	rs := safetyResolvedStatement{index: st.Index, parsed: st}
	var extra []sqlsafety.Reason
	undoable := st.Kind == sqlsafety.KindData
	est := sqlsafety.Estimate{Source: "none"}
	switch {
	case s.safetyWithoutTransactions():
		extra = append(extra, sqlsafety.ReasonNoTransaction)
		undoable = false
	case s.kind != kindSQL:
		// Non-SQL sessions: no host-side snapshot.
		extra = append(extra, sqlsafety.ReasonNotUndoableStore)
		undoable = false
	case st.Kind == sqlsafety.KindData:
		if st.Rewritable && st.Table != nil {
			if resolved, pk, failure := resolveSafetyTarget(ctx, s, database, st); failure == "" {
				rs.parsed, rs.primaryKey, rs.resolved = resolved, pk, true
				if len(pk) == 0 || setsKeyColumn(resolved.SetColumns, pk) {
					// Changed keys cannot be read back by the old ones.
					extra = append(extra, sqlsafety.ReasonNoPrimaryKey)
					undoable = false
				}
				extra = append(extra, safetyMetadataHints(ctx, s, resolved)...)
			} else {
				extra = append(extra, failure)
				undoable = false
			}
		} else {
			undoable = false // Classify already added ReasonUnparsed
		}
		est = estimateRows(ctx, s, family, database, rs)
	case st.Kind == sqlsafety.KindInsert:
		// Undoability of inserts is decided at execution (RETURNING / OUTPUT /
		// LastInsertId); the check resolves the table and key it needs.
		if st.Table != nil {
			if resolved, pk, failure := resolveSafetyTarget(ctx, s, database, st); failure == "" {
				rs.parsed, rs.primaryKey, rs.resolved = resolved, pk, true
				if len(pk) == 0 {
					extra = append(extra, sqlsafety.ReasonNoPrimaryKey)
				}
				if tableHasTriggers(ctx, s, resolved.Table.Schema, resolved.Table.Name) {
					extra = append(extra, sqlsafety.ReasonTrigger)
				}
			} else if failure == sqlsafety.ReasonTableNotFound {
				extra = append(extra, failure)
			}
		}
		est = estimateInsertRows(ctx, s, database, st)
	case s.kind == kindSQL && st.Kind == sqlsafety.KindStructure && st.Destructive && st.Table != nil && !containsReason(st.Reasons, sqlsafety.ReasonUnparsed):
		// DROP TABLE / TRUNCATE of one table: back the whole table up (no key needed).
		if resolved, pk, failure := resolveSafetyTarget(ctx, s, database, st); failure == "" {
			rs.parsed, rs.primaryKey, rs.resolved = resolved, pk, true
			var hints []sqlsafety.Reason
			est, undoable, hints = assessWholeTable(ctx, s, database, *resolved.Table, st.Text)
			extra = append(extra, hints...)
		} else if failure == sqlsafety.ReasonTableNotFound {
			extra = append(extra, failure)
		}
	}
	if script {
		extra = append(extra, sqlsafety.ReasonExplicitTransaction)
		undoable = false
	}
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
	sc.Estimate, sc.Risk, sc.Reasons, sc.Undoable = est, risk, reasons, undoable
	return rs
}

func setsKeyColumn(setColumns, pk []string) bool {
	for _, c := range setColumns {
		for _, k := range pk {
			if c == k {
				return true
			}
		}
	}
	return false
}

func containsReason(reasons []sqlsafety.Reason, r sqlsafety.Reason) bool {
	for _, have := range reasons {
		if have == r {
			return true
		}
	}
	return false
}

func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// ── 名称解析 ──

// resolveSafetyTarget resolves st.Table and st.SetColumns against metadata
// (exact name first, then a unique case-insensitive match) and returns a
// copy carrying the resolved names plus the table's primary key columns.
// On failure it returns why: ReasonTableNotFound when no candidate schema
// holds the table, ReasonUnparsed for anything else (an unsupported
// qualifier, an ambiguous name, an unknown SET column, a failed lookup).
func resolveSafetyTarget(ctx context.Context, s *dbSession, database string, st sqlsafety.Statement) (sqlsafety.Statement, []string, sqlsafety.Reason) {
	schemas, ok := safetySchemaCandidates(ctx, s, database, st.Table.Schema)
	if !ok {
		return st, nil, sqlsafety.ReasonUnparsed
	}
	if len(schemas) == 0 {
		// No current database to resolve an unqualified name in.
		return st, nil, sqlsafety.ReasonTableNotFound
	}
	// Not found: tableNotFound when some candidate's metadata answered that
	// it holds no such table, unparsed when every lookup was inconclusive.
	failure := sqlsafety.ReasonUnparsed
	for _, schema := range schemas {
		name, columns, found := resolveSafetyTable(ctx, s, schema, st.Table.Name)
		if found != tableFound {
			if found == tableMissing {
				failure = sqlsafety.ReasonTableNotFound
			}
			continue
		}
		names := make([]string, len(columns))
		for i, c := range columns {
			names[i] = c.Name
		}
		setColumns := make([]string, 0, len(st.SetColumns))
		for _, want := range st.SetColumns {
			col, ok := matchName(names, want, s.identifierFold())
			if !ok {
				return st, nil, sqlsafety.ReasonUnparsed
			}
			setColumns = append(setColumns, col)
		}
		info, err := getPrimaryKeys(ctx, s, schema, name)
		if err != nil {
			return st, nil, sqlsafety.ReasonUnparsed
		}
		resolved := st
		resolved.Table = &sqlsafety.TableRef{Schema: schema, Name: name}
		if len(st.SetColumns) > 0 {
			resolved.SetColumns = setColumns
		}
		return resolved, append([]string(nil), info.Columns...), ""
	}
	return st, nil, failure
}

// safetySchemaCandidates returns the schema (or MySQL database) to look an
// UPDATE/DELETE target up in: the explicit one, else the session's current
// one. Unquoted identifiers fold (Postgres lower, Oracle/DM upper), and the
// statement does not say whether it quoted the schema, so the folded
// spelling is tried second.
func safetySchemaCandidates(ctx context.Context, s *dbSession, database, explicit string) ([]string, bool) {
	fold := s.identifierFold()
	var current string
	switch s.dialect {
	case "sqlite":
		// qualifiedTable has no schema for SQLite: only the main database is safe.
		return []string{""}, explicit == "" || strings.EqualFold(explicit, "main")
	case "firebird":
		return []string{""}, explicit == ""
	case "mysql":
		// Never ask a pooled connection (SELECT DATABASE()): an earlier
		// USE may have left it in another database. Execution pins the
		// same database (mysqlEffectiveDatabase).
		current = mysqlEffectiveDatabase(s, database)
		if explicit == "" && current == "" {
			return nil, true // no database selected
		}
	case "postgres":
		if explicit == "" {
			// Unqualified names resolve along the search path, not only
			// in its first schema.
			if path := querySearchPath(ctx, s); len(path) > 0 {
				return path, true
			}
			current = queryCurrentSchema(ctx, s, "SELECT current_schema()")
		}
	case "duckdb":
		if explicit == "" {
			current = queryCurrentSchema(ctx, s, "SELECT current_schema()")
		}
	case "sqlserver":
		if explicit == "" {
			// A user's default schema first, then dbo, as SQL Server resolves them.
			current = queryCurrentSchema(ctx, s, "SELECT SCHEMA_NAME()")
			if current != "" && !strings.EqualFold(current, "dbo") {
				return []string{current, "dbo"}, true
			}
		}
	case "oracle", "yashandb", "dm":
		if explicit == "" {
			current = queryCurrentSchema(ctx, s, "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM DUAL")
		}
	default:
		return nil, false
	}
	if explicit == "" {
		return []string{current}, current != ""
	}
	out := []string{explicit}
	if fold != nil && fold(explicit) != explicit {
		out = append(out, fold(explicit))
	}
	return out, true
}

// mysqlEffectiveDatabase is the database a MySQL statement runs in: the
// request's database, else the connection's default one ("" when neither).
func mysqlEffectiveDatabase(s *dbSession, database string) string {
	if database = strings.TrimSpace(database); database != "" {
		return database
	}
	return strings.TrimSpace(s.defaultDatabase)
}

// querySearchPath lists the existing schemas of the Postgres search path in
// order (current_schemas(false)); nil when the server cannot say.
func querySearchPath(ctx context.Context, s *dbSession) []string {
	rows, err := s.db.QueryContext(ctx, "SELECT unnest(current_schemas(false))")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return nil
		}
		if name.Valid && name.String != "" {
			out = append(out, name.String)
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

func queryCurrentSchema(ctx context.Context, s *dbSession, query string) string {
	var name sql.NullString
	if err := s.db.QueryRowContext(ctx, query).Scan(&name); err != nil {
		return ""
	}
	return name.String
}

// identifierFold is how the dialect folds unquoted identifiers when its
// metadata lookups are case-sensitive (Postgres lower, Oracle/DM upper), or
// nil. Statement names lose their quotes, so a name whose folded spelling
// names another object is ambiguous.
func (s *dbSession) identifierFold() func(string) string {
	switch s.dialect {
	case "postgres", "duckdb":
		return strings.ToLower
	case "oracle", "yashandb", "dm", "firebird":
		return strings.ToUpper
	}
	return nil
}

// tableLookup is the outcome of resolveSafetyTable.
type tableLookup int

const (
	tableFound     tableLookup = iota
	tableMissing               // metadata answered and holds no such table
	tableUncertain             // ambiguous name or failed lookup
)

// resolveSafetyTable finds table in schema: an exact column lookup first,
// then a unique case-insensitive match among the schema's tables. An exact
// match whose folded spelling is another table is ambiguous.
func resolveSafetyTable(ctx context.Context, s *dbSession, schema, table string) (string, []ColumnInfo, tableLookup) {
	columns, err := getTableColumns(ctx, s, schema, table)
	fold := s.identifierFold()
	if err == nil && len(columns) > 0 {
		if fold != nil && fold(table) != table {
			if other, err := getTableColumns(ctx, s, schema, fold(table)); err != nil || len(other) > 0 {
				return "", nil, tableUncertain
			}
		}
		return table, columns, tableFound
	}
	answered := err == nil // metadata said: no such table (exact spelling)
	if fold != nil && fold(table) != table {
		// An unquoted name as the dialect stores it (Firebird / Oracle upper case).
		folded, ferr := getTableColumns(ctx, s, schema, fold(table))
		if ferr == nil && len(folded) > 0 {
			return fold(table), folded, tableFound
		}
		answered = answered && ferr == nil
	}
	if s.dialect == "sqlite" {
		// SQLite lookups already ignore case.
		if err != nil {
			return "", nil, tableUncertain
		}
		return "", nil, tableMissing
	}
	tables, err := getDatabaseTables(ctx, s, schema)
	if err != nil {
		if answered && fold != nil {
			// No table listing (Firebird); both spellings were looked up.
			return "", nil, tableMissing
		}
		return "", nil, tableUncertain
	}
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	name, ok := matchName(names, table, nil)
	switch {
	case !ok && name == "" && !containsFold(names, table):
		return "", nil, tableMissing
	case !ok || name == table:
		return "", nil, tableUncertain
	}
	columns, err = getTableColumns(ctx, s, schema, name)
	if err != nil || len(columns) == 0 {
		return "", nil, tableUncertain
	}
	return name, columns, tableFound
}

func containsFold(names []string, want string) bool {
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return true
		}
	}
	return false
}

// matchName returns the candidate equal to want, else the only candidate
// equal ignoring case. With fold, an exact match is ambiguous when
// fold(want) is a different candidate too.
func matchName(candidates []string, want string, fold func(string) string) (string, bool) {
	exact, folded := false, false
	found := ""
	matches := 0
	for _, c := range candidates {
		switch {
		case c == want:
			exact = true
		case fold != nil && c == fold(want):
			folded = true
		}
		if strings.EqualFold(c, want) {
			found = c
			matches++
		}
	}
	if exact {
		return want, !folded
	}
	return found, matches == 1
}

// ── 级联与触发器提示 ──

// safetyMetadataHints adds ReasonCascade when another table's foreign key
// cascades (or sets null/default) on this statement's action, and ReasonTrigger when
// the table has triggers. Lookup failures add nothing: these are hints.
func safetyMetadataHints(ctx context.Context, s *dbSession, st sqlsafety.Statement) []sqlsafety.Reason {
	var out []sqlsafety.Reason
	update := len(st.SetColumns) > 0 // a rewritable UPDATE always has SET columns
	if referencedWithCascade(ctx, s, st.Table.Schema, st.Table.Name, update) {
		out = append(out, sqlsafety.ReasonCascade)
	}
	if tableHasTriggers(ctx, s, st.Table.Schema, st.Table.Name) {
		out = append(out, sqlsafety.ReasonTrigger)
	}
	return out
}

func referencedWithCascade(ctx context.Context, s *dbSession, schema, table string, update bool) bool {
	var query string
	args := []any{schema, table}
	switch s.dialect {
	case "sqlite":
		column := "on_delete"
		if update {
			column = "on_update"
		}
		query = `SELECT COUNT(*) FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) f
			WHERE m.type = 'table' AND f."table" = ? COLLATE NOCASE AND f.` + column + ` IN ('CASCADE', 'SET NULL', 'SET DEFAULT')`
		args = []any{table}
	case "mysql":
		column := "DELETE_RULE"
		if update {
			column = "UPDATE_RULE"
		}
		query = `SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS
			WHERE UNIQUE_CONSTRAINT_SCHEMA = ? AND REFERENCED_TABLE_NAME = ? AND ` + column + ` IN ('CASCADE', 'SET NULL', 'SET DEFAULT')`
	case "postgres":
		column := "confdeltype"
		if update {
			column = "confupdtype"
		}
		query = `SELECT COUNT(*) FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.confrelid JOIN pg_namespace n ON n.oid = t.relnamespace
			WHERE c.contype = 'f' AND n.nspname = $1 AND t.relname = $2 AND c.` + column + ` IN ('c', 'n', 'd')`
	case "sqlserver":
		column := "delete_referential_action"
		if update {
			column = "update_referential_action"
		}
		query = `SELECT COUNT(*) FROM sys.foreign_keys fk
			JOIN sys.tables t ON t.object_id = fk.referenced_object_id JOIN sys.schemas sc ON sc.schema_id = t.schema_id
			WHERE sc.name = @p1 AND t.name = @p2 AND fk.` + column + ` IN (1, 2, 3)`
	case "oracle", "yashandb", "dm":
		if update {
			return false // no ON UPDATE actions
		}
		query = fmt.Sprintf(`SELECT COUNT(*) FROM ALL_CONSTRAINTS c
			JOIN ALL_CONSTRAINTS p ON p.OWNER = c.R_OWNER AND p.CONSTRAINT_NAME = c.R_CONSTRAINT_NAME
			WHERE c.CONSTRAINT_TYPE = 'R' AND p.OWNER = %s AND p.TABLE_NAME = %s AND c.DELETE_RULE IN ('CASCADE', 'SET NULL')`,
			placeholder(s.dialect, 1), placeholder(s.dialect, 2))
	default:
		return false
	}
	var n int64
	return s.db.QueryRowContext(ctx, query, args...).Scan(&n) == nil && n > 0
}

func tableHasTriggers(ctx context.Context, s *dbSession, schema, table string) bool {
	var query string
	switch s.dialect {
	case "sqlserver":
		query = `SELECT COUNT(*) FROM sys.triggers tr
			JOIN sys.tables t ON t.object_id = tr.parent_id JOIN sys.schemas sc ON sc.schema_id = t.schema_id
			WHERE sc.name = @p1 AND t.name = @p2`
	case "oracle", "yashandb", "dm":
		query = fmt.Sprintf(`SELECT COUNT(*) FROM ALL_TRIGGERS WHERE TABLE_OWNER = %s AND TABLE_NAME = %s`,
			placeholder(s.dialect, 1), placeholder(s.dialect, 2))
	default:
		triggers, err := listTriggers(ctx, s, schema, table)
		return err == nil && len(triggers) > 0
	}
	var n int64
	return s.db.QueryRowContext(ctx, query, schema, table).Scan(&n) == nil && n > 0
}

// ── 行数估算 ──

// estimateRows estimates the rows an UPDATE/DELETE touches: an exact COUNT
// for a resolved statement (see withEstimateTx), else the planner's
// estimate, else Source "none".
func estimateRows(ctx context.Context, s *dbSession, family sqlsafety.Family, database string, rs safetyResolvedStatement) sqlsafety.Estimate {
	if rs.resolved {
		if query, ok := sqlsafety.CountSQL(family, rs.parsed, s.safetyQualify); ok {
			var n int64
			err := withEstimateTx(ctx, s, database, true, func(ctx context.Context, tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, query).Scan(&n)
			})
			if err == nil {
				return sqlsafety.Estimate{Rows: n, Exact: true, Source: "count"}
			}
		}
	}
	if n, ok := explainEstimate(ctx, s, family, database, rs.parsed.Text); ok {
		return sqlsafety.Estimate{Rows: n, Source: "explain"}
	}
	return sqlsafety.Estimate{Source: "none"}
}

// estimateInsertRows is the VALUES tuple count of a plain INSERT (exact, no
// query), or a COUNT over the query of INSERT ... SELECT run like any other
// estimate; otherwise Source "none".
func estimateInsertRows(ctx context.Context, s *dbSession, database string, st sqlsafety.Statement) sqlsafety.Estimate {
	if st.InsertRows > 0 {
		return sqlsafety.Estimate{Rows: st.InsertRows, Exact: true, Source: "count"}
	}
	query := strings.TrimSpace(st.InsertQuery)
	if query == "" || s.kind != kindSQL || s.db == nil || mayHoldSecondStatement(query) {
		return sqlsafety.Estimate{Source: "none"}
	}
	var n int64
	err := withEstimateTx(ctx, s, database, true, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+query+") x").Scan(&n)
	})
	if err != nil {
		return sqlsafety.Estimate{Source: "none"}
	}
	return sqlsafety.Estimate{Rows: n, Exact: true, Source: "count"}
}

// mayHoldSecondStatement reports text the estimator must never send: an
// inner semicolon or a MySQL executable comment.
func mayHoldSecondStatement(text string) bool {
	body := strings.TrimRight(strings.TrimSpace(text), "; \t\r\n")
	return strings.Contains(body, ";") || strings.Contains(body, "/*!")
}

// withEstimateTx runs fn in a transaction that is always rolled back, on one
// connection (after USE database on MySQL), bounded by
// safetyEstimateTimeout. readOnly asks for a read-only transaction and falls
// back to a plain one where the driver refuses it. Oracle-family sessions
// start with SET TRANSACTION READ ONLY (read-only TxOptions are not
// portable across their drivers); a dialect that rejects it continues in
// the plain, rolled-back transaction.
func withEstimateTx(parent context.Context, s *dbSession, database string, readOnly bool, fn func(context.Context, *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(parent, safetyEstimateTimeout)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if database := mysqlEffectiveDatabase(s, database); s.dialect == "mysql" && database != "" {
		if _, err := conn.ExecContext(ctx, "USE `"+escapeBacktick(database)+"`"); err != nil {
			return err
		}
	}
	var tx *sql.Tx
	if readOnly {
		tx, err = conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if readOnly && s.dialect == "firebird" {
		// The Firebird driver keeps a read-only transaction's parameters for
		// the connection's next (autocommit) transaction, so a pooled
		// connection would refuse the user's write afterwards: discard it
		// (after the rollback below) instead of returning it to the pool.
		defer func() { _ = conn.Raw(func(any) error { return driver.ErrBadConn }) }()
	}
	if tx == nil {
		tx, err = conn.BeginTx(ctx, nil)
	}
	if err != nil {
		return err
	}
	if readOnly && isOracleFamily(s.dialect) {
		_, _ = tx.ExecContext(ctx, "SET TRANSACTION READ ONLY")
		if err := fn(ctx, tx); !readOnlySnapshotTooOld(err) {
			_ = tx.Rollback()
			return err
		}
		// Table definition changed seconds ago: estimate in a plain transaction.
		_ = tx.Rollback()
		if tx, err = conn.BeginTx(ctx, nil); err != nil {
			return err
		}
	}
	defer func() { _ = tx.Rollback() }()
	return fn(ctx, tx)
}

// explainEstimate asks the planner how many rows text touches. Only MySQL
// and Postgres plans carry a usable count here, plus Oracle/DM PLAN_TABLE.
// Text that may hold a second statement is never sent.
func explainEstimate(ctx context.Context, s *dbSession, family sqlsafety.Family, database, text string) (int64, bool) {
	if s.kind != kindSQL || s.db == nil {
		return 0, false
	}
	if mayHoldSecondStatement(text) {
		return 0, false
	}
	query, ok := sqlsafety.ExplainSQL(family, text)
	if !ok {
		return 0, false
	}
	if safetyExplainHook != nil {
		safetyExplainHook(query)
	}
	var n int64
	switch family {
	case sqlsafety.FamilyMySQL, sqlsafety.FamilyPostgres:
		err := withEstimateTx(ctx, s, database, true, func(ctx context.Context, tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, query)
			if err != nil {
				return err
			}
			defer rows.Close()
			columns, err := rows.Columns()
			if err != nil {
				return err
			}
			scanner := newSQLRowScanner(len(columns))
			var plan [][]any
			for rows.Next() && len(plan) < safetyExplainMaxRows {
				row, _, err := scanner.scan(rows)
				if err != nil {
					return err
				}
				plan = append(plan, row)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if n, ok = explainRows(family, columns, plan); !ok {
				return fmt.Errorf("plan has no row estimate")
			}
			return nil
		})
		return n, err == nil
	case sqlsafety.FamilyOracle, sqlsafety.FamilyDM:
		// EXPLAIN PLAN inserts into PLAN_TABLE, so not read-only; rolled back.
		var card sql.NullInt64
		err := withEstimateTx(ctx, s, database, false, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return err
			}
			return tx.QueryRowContext(ctx, `SELECT MAX(CARDINALITY) FROM PLAN_TABLE WHERE PLAN_ID = (SELECT MAX(PLAN_ID) FROM PLAN_TABLE)`).Scan(&card)
		})
		return card.Int64, err == nil && card.Valid
	}
	return 0, false
}

// safetyExplainHook, when set (tests only), sees every EXPLAIN statement
// right before it is sent.
var safetyExplainHook func(query string)

func isOracleFamily(dialect string) bool {
	return dialect == "oracle" || dialect == "yashandb" || dialect == "dm"
}

var postgresPlanRows = regexp.MustCompile(`\brows=(\d+)`)

// explainRows extracts the largest row estimate from a plan: MySQL's rows
// column, Postgres' rows=N annotations. Other plans have none.
func explainRows(family sqlsafety.Family, columns []string, plan [][]any) (int64, bool) {
	var best int64
	found := false
	take := func(n int64) {
		if !found || n > best {
			best, found = n, true
		}
	}
	switch family {
	case sqlsafety.FamilyMySQL:
		col := -1
		for i, c := range columns {
			if strings.EqualFold(c, "rows") {
				col = i
			}
		}
		if col < 0 {
			return 0, false
		}
		for _, row := range plan {
			if col < len(row) {
				if n, ok := planInt(row[col]); ok {
					take(n)
				}
			}
		}
	case sqlsafety.FamilyPostgres:
		for _, row := range plan {
			for _, cell := range row {
				text, _ := cell.(string)
				for _, m := range postgresPlanRows.FindAllStringSubmatch(text, -1) {
					if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
						take(n)
					}
				}
			}
		}
	}
	return best, found
}

func planInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case int:
		return int64(x), true
	case uint64:
		return int64(x), x <= 1<<63-1
	case float64:
		return int64(x), true
	case []byte:
		n, err := strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64)
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	}
	return 0, false
}
