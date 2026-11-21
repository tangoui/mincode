// SQL 安全网：操作记录、撤回与设置。
//
//	db_safety_history_list     {connectionId, limit?}              -> []sqlsafety.Record（默认 100，最多 500，不含行数据）
//	db_safety_history_get      {id}                                -> {record, sample: {columns, before, after}}（各最多 20 行）
//	db_safety_undo_preview     {sessionId, id}                                   -> {record, conflicts, conflictCount, total, previewToken}
//	db_safety_undo             {sessionId, id, mode, previewToken, confirmText?} -> {restored, skipped, recordId, recordIds}
//	db_safety_settings_get     {}                                  -> sqlsafety.Settings
//	db_safety_settings_set     {settings}                          -> sqlsafety.Settings
//	db_safety_purge_connection {connectionId}                      -> void
//
// 撤回按留底构造反向步骤：UPDATE → 按主键把列改回原值；DELETE 与整表留底 → 按原行
// 插回（整表留底的表不存在时返回 sqlsafety:tableMissing）；INSERT → 按主键删除。
// 冲突（changed / missing / exists）先在事务外按主键读当前行判定，按 mode 选出要写
// 的行；预览返回 previewToken（记录、行数与每个冲突的主键、类型、当前值的指纹），
// 撤回时重新判定，指纹不同（预览后数据又变了）返回 sqlsafety:previewStale、不做修改；反向步骤再经 runGuarded(execBatch, "undo") 在一个事务里执行：按主键加锁
// 留底 → 核对选中的行仍是判定时的状态（变了就整体回滚）→ 写回 → 生成 ParentID
// 指向原记录的新记录（可再撤回）。全部恢复后原记录为 undone，有跳过为 partial；
// partial 记录可再次撤回，已由子记录恢复过的行不再计入。提交结果未知（uncertain）
// 与已撤回（undone）的记录拒绝撤回。日志只记录记录 ID 与行数。
package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/db/sqlsafety"
)

const (
	safetyCodeTableMissing    = "tableMissing"
	safetyHistoryDefaultLimit = 100
	safetyHistoryMaxLimit     = 500
	safetySampleRows          = 20
	safetyMaxConflicts        = 1000 // conflicts listed in a preview; conflictCount has the total
)

// safetyCodePreviewStale: the data changed since the preview the user
// confirmed (or during the undo transaction); nothing was changed.
const safetyCodePreviewStale = "previewStale"

func errPreviewStale() error {
	return &safetyError{Code: safetyCodePreviewStale, Message: "数据在预览后已变化，未做任何修改，请重新预览后再撤回"}
}

// errUndoUnstorable: the undo's own backup cannot be stored (over the
// backup limits, a value type that cannot be backed up, no primary key in
// the read). Nothing was changed. A plain error rather than previewStale,
// which would send the user into a re-preview that fails the same way.
func errUndoUnstorable(reason sqlsafety.Reason) error {
	if reason == sqlsafety.ReasonOverSnapshotLimit {
		return errors.New("撤回本身的留底超过留底上限，未做任何修改。请在设置中调高留底上限后再撤回")
	}
	return errors.New("撤回本身的留底无法保存（有无法无损留底的数据类型，或读不到主键），未做任何修改")
}

// safetyHistoryStore is the part of sqlsafety.Store history and undo read.
type safetyHistoryStore interface {
	safetyRecordStore
	List(connectionID string, limit int) ([]sqlsafety.Record, error)
	Get(id string) (sqlsafety.Record, sqlsafety.Rows, error)
	DeleteConnection(connectionID string) error
	Prune(settings sqlsafety.Settings) (int, error)
}

func safetyHistory() (safetyHistoryStore, error) {
	if h, ok := safetyRecords().(safetyHistoryStore); ok {
		return h, nil
	}
	return nil, errors.New("SQL 安全网操作记录不可用")
}

// pruneSafetyRecords applies the retention settings to the backup store.
func pruneSafetyRecords() {
	store, err := safetyHistory()
	if err != nil {
		return
	}
	removed, err := store.Prune(safetySettings())
	if err != nil {
		slog.Warn("sql safety backup prune failed", "error", err)
	}
	if removed > 0 {
		slog.Info("sql safety backups pruned", "removed", removed)
	}
}

// ── 操作记录 ──

type safetyHistoryListArgs struct {
	ConnectionID string `json:"connectionId"`
	Limit        *int   `json:"limit"`
}

func dbSafetyHistoryListHandler(_, argsJSON string) (string, error) {
	var a safetyHistoryListArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	connectionID := strings.TrimSpace(a.ConnectionID)
	if connectionID == "" {
		return "", errors.New("db_safety_history_list: connectionId 不能为空")
	}
	limit := safetyHistoryDefaultLimit
	if a.Limit != nil && *a.Limit > 0 {
		limit = min(*a.Limit, safetyHistoryMaxLimit)
	}
	store, err := safetyHistory()
	if err != nil {
		return "", err
	}
	recs, err := store.List(connectionID, limit)
	if err != nil {
		return "", err
	}
	if recs == nil {
		recs = []sqlsafety.Record{}
	}
	return core.EncodeResult(recs)
}

type safetyHistorySample struct {
	Columns []string `json:"columns"`
	Before  [][]any  `json:"before"`
	After   [][]any  `json:"after"`
}

type safetyHistoryDetail struct {
	Record sqlsafety.Record    `json:"record"`
	Sample safetyHistorySample `json:"sample"`
}

func dbSafetyHistoryGetHandler(_, argsJSON string) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	store, err := safetyHistory()
	if err != nil {
		return "", err
	}
	rec, rows, err := getSafetyRecord(store, strings.TrimSpace(a.ID))
	if err != nil {
		return "", err
	}
	columns := rec.Columns
	if columns == nil {
		columns = []string{}
	}
	after := rows.After
	if len(rows.After) == 0 && len(rows.Inserted) > 0 && len(rows.Inserted[0]) == len(columns) {
		after = rows.Inserted // an INSERT's rows are its new primary keys
	}
	return core.EncodeResult(safetyHistoryDetail{Record: rec, Sample: safetyHistorySample{
		Columns: columns, Before: sampleRows(rows.Before), After: sampleRows(after),
	}})
}

func getSafetyRecord(store safetyHistoryStore, id string) (sqlsafety.Record, sqlsafety.Rows, error) {
	rec, rows, err := store.Get(id)
	if errors.Is(err, sqlsafety.ErrRecordNotFound) {
		return rec, rows, errors.New("操作记录不存在或已过期清理")
	}
	return rec, rows, err
}

// sampleRows returns up to safetySampleRows rows ready for JSON.
func sampleRows(rows [][]any) [][]any {
	out := make([][]any, 0, min(len(rows), safetySampleRows))
	for _, row := range rows[:min(len(rows), safetySampleRows)] {
		view := make([]any, len(row))
		for i, v := range row {
			view[i] = sampleValue(v)
		}
		out = append(out, view)
	}
	return out
}

// sampleValue is a stored value as the result grid shows it.
func sampleValue(v any) any {
	if f, ok := v.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
		return strconv.FormatFloat(f, 'g', -1, 64) // JSON has no NaN/Inf
	}
	return normalizeValue(v)
}

// ── 撤回计划 ──

type undoKind int

const (
	undoUpdate undoKind = iota // set the changed columns back
	undoDelete                 // insert the deleted rows back
	undoInsert                 // delete the inserted rows
	undoTable                  // insert a whole-table snapshot back
	undoRows                   // a grid change group: each row undoes its own change (op)
)

type undoRow struct {
	op       undoKind // how this row is undone: the plan's kind, or per row for undoRows
	key      []any    // primary key values; nil without a primary key
	values   []any    // the stored row (plan columns order); undoInsert: the key
	expect   []any    // undoUpdate: the after-image the current row must still equal
	current  []any    // the row as markConflicts read it (plan columns; undoInsert plan: the key); nil when absent
	conflict string   // "", changed, missing, exists
}

type undoPlan struct {
	rec     sqlsafety.Record
	kind    undoKind
	pk      []string // empty only for a whole-table snapshot without primary key
	columns []string // order of undoRow.values
	keyAt   []int    // positions of pk in columns
	rows    []undoRow
}

// loadUndoPlan reads record id, checks it belongs to s and can be undone,
// drops rows earlier undos of it already restored, and marks conflicts
// against the current data. Uncertain records load (preview warns); undo
// refuses them.
func loadUndoPlan(ctx context.Context, s *dbSession, store safetyHistoryStore, id string) (*undoPlan, error) {
	rec, rows, err := getSafetyRecord(store, id)
	if err != nil {
		return nil, err
	}
	switch {
	case s.kind != kindSQL || s.db == nil:
		return nil, errors.New("该会话不支持撤回")
	case rec.ConnectionID == "" || rec.ConnectionID != s.connectionID || rec.DbType != s.dialect:
		return nil, errors.New("该操作记录不属于当前连接，不能撤回")
	case !rec.Undoable:
		return nil, errors.New("该操作没有留底，不能撤回")
	case rec.UndoState == "undone":
		return nil, errors.New("该操作已撤回")
	case rec.Table.Name == "":
		return nil, errors.New("操作记录缺少表名，不能撤回")
	}
	p := &undoPlan{rec: rec, columns: rec.Columns}
	switch rec.Kind {
	case sqlsafety.KindStructure:
		p.kind = undoTable
	case sqlsafety.KindInsert:
		p.kind = undoInsert
	case sqlsafety.KindRows:
		p.kind = undoRows
	case sqlsafety.KindData:
		p.kind = undoDelete
		if len(rows.After) > 0 || len(sqlsafety.Classify(s.safetyFamily(), rec.Statement).SetColumns) > 0 {
			p.kind = undoUpdate
		}
	default:
		return nil, errors.New("不支持撤回该类操作")
	}
	if p.kind == undoTable {
		columns, err := getTableColumns(ctx, s, rec.Table.Schema, rec.Table.Name)
		if err != nil {
			return nil, fmt.Errorf("读取表结构失败: %w", err)
		}
		if len(columns) == 0 {
			return nil, &safetyError{Code: safetyCodeTableMissing, Message: "表 " + rec.Table.Name + " 不存在，请先恢复表结构再撤回"}
		}
	}
	if keyAt, ok := columnPositions(rec.Columns, rec.PrimaryKey); ok {
		p.pk, p.keyAt = rec.PrimaryKey, keyAt
	} else if p.kind != undoTable {
		return nil, errors.New("操作记录缺少主键，不能撤回")
	}
	if err := p.loadRows(rows); err != nil {
		return nil, err
	}
	if err := p.dropRestored(store); err != nil {
		return nil, err
	}
	if err := p.markConflicts(ctx, s); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *undoPlan) project(row []any) []any {
	if len(p.keyAt) == 0 {
		return nil
	}
	key := make([]any, len(p.keyAt))
	for i, at := range p.keyAt {
		key[i] = row[at]
	}
	return key
}

func (p *undoPlan) loadRows(rows sqlsafety.Rows) error {
	if p.kind == undoRows {
		return p.loadGroupRows(rows)
	}
	list := rows.Before
	if p.kind == undoInsert {
		list = rows.Inserted
	}
	after := map[string][]any{}
	if p.kind == undoUpdate {
		for _, row := range rows.After {
			if len(row) != len(p.columns) {
				return errors.New("操作记录已损坏，不能撤回")
			}
			after[undoKeyString(p.project(row))] = row
		}
	}
	for _, row := range list {
		if len(row) != len(p.columns) {
			return errors.New("操作记录已损坏，不能撤回")
		}
		r := undoRow{op: p.kind, key: p.project(row), values: row}
		if p.kind == undoUpdate {
			r.expect = after[undoKeyString(r.key)]
		}
		p.rows = append(p.rows, r)
	}
	return nil
}

// loadGroupRows loads a grid change group: Before rows with an After row
// were updated (set back), Before rows without one were deleted (inserted
// back), Inserted rows are new primary keys in PrimaryKey order (deleted).
// A key deleted and inserted again in the group is one replaced row: it is
// set back to its Before row, and since the replacement's values are not
// kept it always shows as changed.
func (p *undoPlan) loadGroupRows(rows sqlsafety.Rows) error {
	after := map[string][]any{}
	for _, row := range rows.After {
		if len(row) != len(p.columns) {
			return errors.New("操作记录已损坏，不能撤回")
		}
		after[undoKeyString(p.project(row))] = row
	}
	inserted := map[string]bool{}
	for _, key := range rows.Inserted {
		if len(key) != len(p.pk) {
			return errors.New("操作记录已损坏，不能撤回")
		}
		inserted[undoKeyString(key)] = true
	}
	replaced := map[string]bool{}
	for _, row := range rows.Before {
		if len(row) != len(p.columns) {
			return errors.New("操作记录已损坏，不能撤回")
		}
		r := undoRow{op: undoDelete, key: p.project(row), values: row}
		k := undoKeyString(r.key)
		switch expect, updated := after[k]; {
		case updated:
			r.op, r.expect = undoUpdate, expect
		case inserted[k]:
			r.op, replaced[k] = undoUpdate, true // expect nil: always a conflict
		}
		p.rows = append(p.rows, r)
	}
	for _, key := range rows.Inserted {
		if !replaced[undoKeyString(key)] {
			p.rows = append(p.rows, undoRow{op: undoInsert, key: key, values: key})
		}
	}
	return nil
}

// dropRestored removes the rows earlier undos of this record restored: the
// keys of its child records still in effect (available or partial). A child
// whose commit was uncertain does not count; if it did apply, its rows now
// show as conflicts instead.
func (p *undoPlan) dropRestored(store safetyHistoryStore) error {
	if len(p.pk) == 0 || len(p.rows) == 0 {
		return nil
	}
	recs, err := store.List(p.rec.ConnectionID, 0)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for _, child := range recs {
		if child.ParentID != p.rec.ID || (child.UndoState != "available" && child.UndoState != "partial") {
			continue
		}
		at, ok := columnPositions(child.Columns, p.pk)
		if !ok {
			continue
		}
		_, rows, err := store.Get(child.ID)
		if err != nil {
			slog.Warn("sql safety undo record unreadable", "record", child.ID, "error", err)
			continue
		}
		for _, list := range [][][]any{rows.Before, rows.Inserted} {
			for _, row := range list {
				key := make([]any, len(at))
				for i, j := range at {
					if j < len(row) {
						key[i] = row[j]
					}
				}
				done[undoKeyString(key)] = true
			}
		}
	}
	kept := p.rows[:0]
	for _, r := range p.rows {
		if !done[undoKeyString(r.key)] {
			kept = append(kept, r)
		}
	}
	p.rows = kept
	return nil
}

// markConflicts reads the current rows by primary key: an UPDATE's row
// must still equal its after-image (else changed) and exist (else missing);
// a deleted or whole-table row must not exist again (else exists); an
// inserted row must still exist (else missing). Without a primary key no
// conflict can be told.
func (p *undoPlan) markConflicts(ctx context.Context, s *dbSession) error {
	if len(p.pk) == 0 || len(p.rows) == 0 {
		return nil
	}
	columns := p.columns
	if p.kind == undoInsert {
		columns = p.pk
	}
	// A grid group reads whole rows for every key, its inserted ones too.
	q, err := undoKeyQuery(s, p.rec.Table, p.pk, columns, p.keys(p.rows), false)
	if err != nil {
		return err
	}
	current, err := readRowsByKey(ctx, s, s.db, q, p.pk)
	if err != nil {
		return fmt.Errorf("读取当前数据失败: %w", err)
	}
	for i := range p.rows {
		r := &p.rows[i]
		cur, exists := current[undoKeyString(r.key)]
		r.current = cur
		switch r.op {
		case undoUpdate:
			if !exists {
				r.conflict = "missing"
			} else if r.expect == nil || !sameRow(cur, r.expect) {
				r.conflict = "changed"
			}
		case undoDelete, undoTable:
			if exists {
				r.conflict = "exists"
			}
		case undoInsert:
			if !exists {
				r.conflict = "missing"
			}
		}
	}
	return nil
}

// previewToken fingerprints what a preview showed: the record, how many rows
// the undo covers, and each conflict's key, kind and current values. An undo
// whose plan yields another token would act on data the user has not seen.
func (p *undoPlan) previewToken() string {
	entries := []string{}
	for _, r := range p.rows {
		if r.conflict != "" {
			entries = append(entries, undoKeyString(r.key)+"|"+r.conflict+"|"+undoKeyString(r.current))
		}
	}
	slices.Sort(entries)
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%d\n", p.rec.ID, len(p.rows))
	for _, e := range entries {
		fmt.Fprintf(h, "%d:%s\n", len(e), e)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (p *undoPlan) keys(rows []undoRow) [][]any {
	keys := make([][]any, len(rows))
	for i, r := range rows {
		keys[i] = r.key
	}
	return keys
}

// ── 主键查询 ──

// undoKeyQuery is one SELECT of columns (all when nil) by primary key and the
// argument batches to run it with: each batch binds the same number of keys
// (safetyKeysPerBatch, so SQL Server stays under its parameter limit), the
// last one repeating its final key. No primary key or no keys build nothing.
type undoKeyQueryPlan struct {
	sql     string
	batches [][]any
}

func undoKeyQuery(s *dbSession, table sqlsafety.TableRef, pk, columns []string, keys [][]any, lock bool) (undoKeyQueryPlan, error) {
	if len(pk) == 0 || len(keys) == 0 {
		return undoKeyQueryPlan{}, nil
	}
	per := min(safetyKeysPerBatch(len(pk)), len(keys))
	ph := s.safetyPlaceholder
	q := undoKeyQueryPlan{sql: sqlsafety.ByKeysSQL(s.safetyFamily(), s.safetyQualify, s.safetyQuote, table, pk, columns, per, ph)}
	if lock {
		locked, err := lockedByKeys(s, q.sql, table)
		if err != nil {
			return undoKeyQueryPlan{}, err
		}
		q.sql = locked
	}
	for lo := 0; lo < len(keys); lo += per {
		batch := keys[lo:min(lo+per, len(keys))]
		args := make([]any, 0, per*len(pk))
		for i := range per {
			args = append(args, batch[min(i, len(batch)-1)]...)
		}
		q.batches = append(q.batches, args)
	}
	return q, nil
}

// lockedByKeys locks the rows a by-key SELECT reads until the transaction
// ends. On SQL Server the hint goes after the table; a query it cannot be
// placed in is an error, never run unlocked.
func lockedByKeys(s *dbSession, query string, table sqlsafety.TableRef) (string, error) {
	switch s.safetyFamily() {
	case sqlsafety.FamilyMySQL, sqlsafety.FamilyPostgres, sqlsafety.FamilyOracle, sqlsafety.FamilyDM:
		return query + " FOR UPDATE", nil
	case sqlsafety.FamilySQLServer:
		from := " FROM " + s.safetyQualify(table.Schema, table.Name) + " WHERE "
		if !strings.Contains(query, from) {
			return "", errors.New("SQL 安全网无法为撤回查询加行锁，未执行任何修改")
		}
		return strings.Replace(query, from, strings.TrimSuffix(from, " WHERE ")+" WITH (UPDLOCK, HOLDLOCK) WHERE ", 1), nil
	}
	return query, nil
}

// readRowsByKey runs q and maps each row by its primary key.
func readRowsByKey(ctx context.Context, s *dbSession, ex sqlExecer, q undoKeyQueryPlan, pk []string) (map[string][]any, error) {
	out := map[string][]any{}
	r := &guardedRun{ctx: ctx, s: s} // readRows needs only the context and dialect
	for _, args := range q.batches {
		columns, rows, _, err := r.readRows(ex, q.sql, args, 0, 0)
		if err != nil {
			return nil, err
		}
		keyAt, ok := columnPositions(columns, pk)
		if !ok {
			return nil, errors.New("查询结果缺少主键列")
		}
		for _, row := range rows {
			key := make([]any, len(keyAt))
			for i, at := range keyAt {
				key[i] = row[at]
			}
			out[undoKeyString(key)] = row
		}
	}
	return out, nil
}

// undoKeyString identifies a primary key across value representations
// (a MySQL text-protocol "5" and a binary-protocol 5 are the same key).
func undoKeyString(key []any) string {
	var b strings.Builder
	for _, v := range key {
		t := canonicalValue(v)
		b.WriteString(strconv.Itoa(len(t)))
		b.WriteByte(':')
		b.WriteString(t)
		b.WriteByte(';')
	}
	return b.String()
}

// canonicalValue renders a stored or freshly read value for comparison.
func canonicalValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "\x00null"
	case []byte:
		if x == nil {
			return "\x00null"
		}
		return string(x)
	case string:
		return x
	case json.Number:
		return string(x)
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10)
	}
	return fmt.Sprint(v)
}

func sameRow(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if canonicalValue(a[i]) != canonicalValue(b[i]) {
			return false
		}
	}
	return true
}

// ── 撤回执行 ──

type undoTarget struct {
	row    undoRow
	expect []any // when set, the locked re-read must still equal it (the row the conflict check saw)
}

type undoSteps struct {
	p        *undoPlan
	s        *dbSession
	risk     sqlsafety.Risk
	steps    []guardedStep
	restored int
}

type safetyUndoResult struct {
	Restored  int      `json:"restored"`
	Skipped   int      `json:"skipped"`
	RecordID  string   `json:"recordId"`
	RecordIDs []string `json:"recordIds"`
}

// runUndo writes the plan's chosen rows back, provided the plan still
// matches previewToken (else previewStale, nothing changed). mode
// skipConflicts writes only conflict-free rows; overwrite also writes
// changed rows (UPDATE) and replaces rows that exist again (DELETE / whole
// table). Missing rows are always skipped.
func runUndo(ctx context.Context, s *dbSession, store safetyHistoryStore, p *undoPlan, mode, confirmText, previewToken string) (safetyUndoResult, error) {
	if p.rec.UndoState == "uncertain" {
		return safetyUndoResult{}, errors.New("该操作的提交结果未知（可能未生效），不能撤回；请先核对数据")
	}
	if previewToken == "" || previewToken != p.previewToken() {
		return safetyUndoResult{}, errPreviewStale()
	}
	overwrite := mode == "overwrite"
	var updates, inserts, deletes []undoTarget
	for _, r := range p.rows {
		switch {
		case r.op == undoUpdate && (r.conflict == "" || overwrite && r.conflict == "changed"):
			updates = append(updates, undoTarget{row: r, expect: r.current})
		case (r.op == undoDelete || r.op == undoTable) && r.conflict == "":
			inserts = append(inserts, undoTarget{row: r})
		case (r.op == undoDelete || r.op == undoTable) && overwrite && r.conflict == "exists":
			updates = append(updates, undoTarget{row: r, expect: r.current})
		case r.op == undoInsert && r.conflict == "":
			deletes = append(deletes, undoTarget{row: r})
		}
	}
	acted := len(updates) + len(inserts) + len(deletes)
	res := safetyUndoResult{Skipped: len(p.rows) - acted, RecordIDs: []string{}}
	if acted == 0 {
		if res.Skipped == 0 {
			markUndoState(store, p.rec.ID, "undone")
		}
		return res, nil
	}
	settings := safetySettings()
	if int64(acted) > settings.MaxSnapshotRows {
		return safetyUndoResult{}, fmt.Errorf("撤回涉及 %d 行，超过单次留底上限 %d 行，无法撤回", acted, settings.MaxSnapshotRows)
	}
	b := &undoSteps{p: p, s: s, risk: sqlsafety.RiskLow}
	if int64(acted) > settings.HighRiskRows {
		b.risk = sqlsafety.RiskHigh
	}
	build := []func() error{
		func() error { return b.updateStep(updates) },
		func() error { return b.insertStep(inserts) },
		func() error { return b.deleteStep(deletes) },
	}
	if p.kind == undoRows {
		// Reverse of a group's delete → update → insert: remove its new rows
		// first, so re-inserted ones cannot collide with them.
		build = []func() error{build[2], build[0], build[1]}
	}
	for _, step := range build {
		if err := step(); err != nil {
			return safetyUndoResult{}, err
		}
	}

	check := &sqlsafety.Check{SessionID: s.sessionID, Database: p.rec.Database, Risk: b.risk, Undoable: len(p.pk) > 0}
	for _, st := range b.steps {
		check.Statements = append(check.Statements, st.Statement)
	}
	check.ConfirmTarget = sqlsafety.ConfirmTarget(check.Statements)
	if b.risk == sqlsafety.RiskHigh && !sqlsafety.ConfirmTargetMatches(check.ConfirmTarget, confirmText) {
		return safetyUndoResult{}, &safetyError{Code: safetyCodeConfirmMismatch, Check: check}
	}
	out, err := runGuarded(ctx, s, check, b.steps, execBatch, "undo")
	if err != nil {
		var se *safetyError
		if errors.As(err, &se) && (se.Code == safetyCodeReconfirm || se.Code == safetyCodePreviewStale) {
			return safetyUndoResult{}, errPreviewStale()
		}
		return safetyUndoResult{}, err
	}
	state := "undone"
	if res.Skipped > 0 {
		state = "partial"
	}
	markUndoState(store, p.rec.ID, state)
	res.Restored = b.restored
	if len(out.RecordIDs) > 0 {
		res.RecordID, res.RecordIDs = out.RecordIDs[0], out.RecordIDs
	}
	slog.Info("sql safety undo", "record", p.rec.ID, "restored", res.Restored, "skipped", res.Skipped, "undoRecords", len(res.RecordIDs))
	return res, nil
}

func markUndoState(store safetyHistoryStore, id, state string) {
	if err := store.MarkUndone(id, state); err != nil {
		slog.Warn("sql safety undo state not saved", "record", id, "state", state, "error", err)
	}
}

func (b *undoSteps) quote(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = b.s.safetyQuote(n)
	}
	return out
}

func (b *undoSteps) qualified() string {
	return b.s.safetyQualify(b.p.rec.Table.Schema, b.p.rec.Table.Name)
}

// keyWhere renders "k1 = <ph n+1> AND k2 = <ph n+2>".
func (b *undoSteps) keyWhere(n int) string {
	parts := make([]string, len(b.p.pk))
	for i, c := range b.quote(b.p.pk) {
		parts[i] = c + " = " + b.s.safetyPlaceholder(n+i+1)
	}
	return strings.Join(parts, " AND ")
}

func (b *undoSteps) add(kind sqlsafety.Kind, text string, setColumns []string, targets []undoTarget, step guardedStep) {
	table := b.p.rec.Table
	step.Statement = sqlsafety.StatementCheck{Index: len(b.steps), Text: truncateRunes(text, safetyTextLimit), Kind: kind, Table: &table,
		Estimate: sqlsafety.Estimate{Rows: int64(len(targets)), Exact: true, Source: "count"}, Risk: b.risk, Undoable: step.Snapshot != ""}
	if !step.Statement.Undoable {
		step.Statement.Reasons = []sqlsafety.Reason{sqlsafety.ReasonNoPrimaryKey}
	}
	step.Parsed = sqlsafety.Statement{Index: len(b.steps), Text: text, Kind: kind, Table: &table, SetColumns: setColumns}
	step.PrimaryKey, step.Resolved, step.ParentID = b.p.pk, true, b.p.rec.ID
	b.steps = append(b.steps, step)
	b.restored += len(targets)
}

// keyedSnapshot sets the step's locked by-key snapshot of columns.
func (b *undoSteps) keyedSnapshot(step *guardedStep, columns []string, targets []undoTarget) (undoKeyQueryPlan, error) {
	keys := make([][]any, len(targets))
	for i, t := range targets {
		keys[i] = t.row.key
	}
	q, err := undoKeyQuery(b.s, b.p.rec.Table, b.p.pk, columns, keys, true)
	if err != nil {
		return undoKeyQueryPlan{}, err
	}
	step.Snapshot, step.KeyBatches = q.sql, q.batches
	return q, nil
}

// verify re-reads the targets under the snapshot's locks: each must exist
// and, where expect is set, still be the row the conflict check read.
func (b *undoSteps) verify(ctx context.Context, ex sqlExecer, q undoKeyQueryPlan, targets []undoTarget) error {
	current, err := readRowsByKey(ctx, b.s, ex, q, b.p.pk)
	if err != nil {
		return err
	}
	for _, t := range targets {
		cur, ok := current[undoKeyString(t.row.key)]
		if !ok || t.expect != nil && !sameRow(cur, t.expect) {
			return errPreviewStale()
		}
	}
	return nil
}

// updateStep sets the non-key columns back to the stored values.
func (b *undoSteps) updateStep(targets []undoTarget) error {
	if len(targets) == 0 {
		return nil
	}
	var setAt []int
	var setColumns []string
	for i, c := range b.p.columns {
		if !containsInt(b.p.keyAt, i) {
			setAt, setColumns = append(setAt, i), append(setColumns, c)
		}
	}
	if len(setColumns) == 0 { // every column is the key: an existing row already is the stored row
		b.restored += len(targets)
		return nil
	}
	sets := make([]string, len(setColumns))
	for i, c := range b.quote(setColumns) {
		sets[i] = c + " = " + b.s.safetyPlaceholder(i+1)
	}
	text := "UPDATE " + b.qualified() + " SET " + strings.Join(sets, ", ") + " WHERE " + b.keyWhere(len(sets))
	var step guardedStep
	q, err := b.keyedSnapshot(&step, b.p.columns, targets)
	if err != nil {
		return err
	}
	step.Write = func(ctx context.Context, ex sqlExecer) (int64, error) {
		if err := b.verify(ctx, ex, q, targets); err != nil {
			return 0, err
		}
		for _, t := range targets {
			args := make([]any, 0, len(setAt)+len(t.row.key))
			for _, at := range setAt {
				args = append(args, t.row.values[at])
			}
			if _, err := ex.ExecContext(ctx, text, append(args, t.row.key...)...); err != nil {
				return 0, err
			}
		}
		return int64(len(targets)), nil
	}
	b.add(sqlsafety.KindData, text, setColumns, targets, step)
	return nil
}

// insertStep inserts stored rows back. With a primary key the snapshot
// proves the keys free and the new rows are recorded by key; without one
// (whole-table snapshot) the rows go in as they are, recorded without undo.
func (b *undoSteps) insertStep(targets []undoTarget) error {
	if len(targets) == 0 {
		return nil
	}
	marks := make([]string, len(b.p.columns))
	for i := range marks {
		marks[i] = b.s.safetyPlaceholder(i + 1)
	}
	table := b.qualified()
	text := "INSERT INTO " + table + " (" + strings.Join(b.quote(b.p.columns), ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
	var step guardedStep
	if len(b.p.pk) > 0 {
		if _, err := b.keyedSnapshot(&step, nil, targets); err != nil {
			return err
		}
		step.After = step.Snapshot
	}
	dialect := b.s.dialect
	step.Write = func(ctx context.Context, ex sqlExecer) (int64, error) {
		if dialect == "sqlserver" {
			off, err := sqlServerIdentityInsert(ctx, ex, table)
			if err != nil {
				return 0, err
			}
			defer off()
		}
		for _, t := range targets {
			if _, err := ex.ExecContext(ctx, text, t.row.values...); err != nil {
				return 0, err
			}
		}
		return int64(len(targets)), nil
	}
	b.add(sqlsafety.KindInsert, text, nil, targets, step)
	return nil
}

// deleteStep deletes inserted rows by key, backing up their full rows.
func (b *undoSteps) deleteStep(targets []undoTarget) error {
	if len(targets) == 0 {
		return nil
	}
	text := "DELETE FROM " + b.qualified() + " WHERE " + b.keyWhere(0)
	var step guardedStep
	q, err := b.keyedSnapshot(&step, nil, targets)
	if err != nil {
		return err
	}
	step.Write = func(ctx context.Context, ex sqlExecer) (int64, error) {
		if err := b.verify(ctx, ex, q, targets); err != nil {
			return 0, err
		}
		for _, t := range targets {
			if _, err := ex.ExecContext(ctx, text, t.row.key...); err != nil {
				return 0, err
			}
		}
		return int64(len(targets)), nil
	}
	b.add(sqlsafety.KindData, text, nil, targets, step)
	return nil
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// sqlServerIdentityInsert turns IDENTITY_INSERT on for a table with an
// identity column, so stored key values can be inserted back; the returned
// function turns it off again.
func sqlServerIdentityInsert(ctx context.Context, ex sqlExecer, table string) (func(), error) {
	rows, err := ex.QueryContext(ctx, "SELECT OBJECTPROPERTY(OBJECT_ID(@p1), 'TableHasIdentity')", table)
	if err != nil {
		return nil, err
	}
	var has sql.NullInt64
	if rows.Next() {
		err = rows.Scan(&has)
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if has.Int64 != 1 {
		return func() {}, nil
	}
	if _, err := ex.ExecContext(ctx, "SET IDENTITY_INSERT "+table+" ON"); err != nil {
		return nil, err
	}
	return func() { _, _ = ex.ExecContext(ctx, "SET IDENTITY_INSERT "+table+" OFF") }, nil
}

// ── 撤回命令 ──

type safetyUndoArgs struct {
	SessionID    string  `json:"sessionId"`
	ID           string  `json:"id"`
	Mode         string  `json:"mode"`
	ConfirmText  *string `json:"confirmText"`
	PreviewToken string  `json:"previewToken"`
}

type safetyUndoConflict struct {
	PrimaryKey map[string]any `json:"primaryKey"`
	Kind       string         `json:"kind"`
}

type safetyUndoPreview struct {
	Record        sqlsafety.Record     `json:"record"`
	Conflicts     []safetyUndoConflict `json:"conflicts"`
	ConflictCount int                  `json:"conflictCount"`
	Total         int                  `json:"total"`
	PreviewToken  string               `json:"previewToken"` // db_safety_undo must present it
}

// withUndoPlan runs fn on the plan of the requested record, holding the session.
func withUndoPlan(callCtx context.Context, argsJSON string, fn func(ctx context.Context, s *dbSession, store safetyHistoryStore, p *undoPlan, a safetyUndoArgs) (any, error)) (string, error) {
	var a safetyUndoArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	store, err := safetyHistory()
	if err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(callCtx, time.Duration(dbDMLTimeoutSecs)*time.Second)
	defer cancel()
	p, err := loadUndoPlan(ctx, s, store, strings.TrimSpace(a.ID))
	if err != nil {
		return "", err
	}
	out, err := fn(ctx, s, store, p, a)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(out)
}

func dbSafetyUndoPreviewHandler(callCtx context.Context, _, argsJSON string) (string, error) {
	return withUndoPlan(callCtx, argsJSON, func(_ context.Context, _ *dbSession, _ safetyHistoryStore, p *undoPlan, _ safetyUndoArgs) (any, error) {
		out := safetyUndoPreview{Record: p.rec, Conflicts: []safetyUndoConflict{}, Total: len(p.rows), PreviewToken: p.previewToken()}
		for _, r := range p.rows {
			if r.conflict == "" {
				continue
			}
			out.ConflictCount++
			if len(out.Conflicts) < safetyMaxConflicts {
				key := make(map[string]any, len(p.pk))
				for i, c := range p.pk {
					key[c] = sampleValue(r.key[i])
				}
				out.Conflicts = append(out.Conflicts, safetyUndoConflict{PrimaryKey: key, Kind: r.conflict})
			}
		}
		return out, nil
	})
}

func dbSafetyUndoHandler(callCtx context.Context, _, argsJSON string) (string, error) {
	return withUndoPlan(callCtx, argsJSON, func(ctx context.Context, s *dbSession, store safetyHistoryStore, p *undoPlan, a safetyUndoArgs) (any, error) {
		if a.Mode != "skipConflicts" && a.Mode != "overwrite" {
			return nil, fmt.Errorf("db_safety_undo: 未知的撤回方式 %q", a.Mode)
		}
		return runUndo(ctx, s, store, p, a.Mode, optionalString(a.ConfirmText), strings.TrimSpace(a.PreviewToken))
	})
}

// ── 设置与清理 ──

func dbSafetySettingsGetHandler(_, _ string) (string, error) {
	return core.EncodeResult(safetySettings())
}

func dbSafetySettingsSetHandler(_, argsJSON string) (string, error) {
	var a struct {
		Settings *sqlsafety.Settings `json:"settings"`
	}
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.Settings == nil {
		return "", errors.New("db_safety_settings_set: settings 不能为空")
	}
	dir := sqlsafety.Dir()
	if dir == "" {
		return "", errors.New("无法确定 SQL 安全网数据目录")
	}
	if err := sqlsafety.SaveSettings(dir, *a.Settings); err != nil {
		return "", err
	}
	setSafetySettings(*a.Settings)
	pruneSafetyRecords() // shorter retention or a smaller limit applies now
	return core.EncodeResult(*a.Settings)
}

func dbSafetyPurgeConnectionHandler(_, argsJSON string) (string, error) {
	var a struct {
		ConnectionID string `json:"connectionId"`
	}
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	connectionID := strings.TrimSpace(a.ConnectionID)
	if connectionID == "" {
		return "", errors.New("db_safety_purge_connection: connectionId 不能为空")
	}
	store, err := safetyHistory()
	if err != nil {
		return "", err
	}
	if err := store.DeleteConnection(connectionID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}
