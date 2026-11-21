// SQL 安全网：插件驱动会话（kindPlugin）的同事务留底执行。
//
// 插件进程里的事务宿主拿不到，所以留底、写入与回读交给一次 plugin.db.snapshotWrite
// （特性 transaction.snapshotWrite）：查询窗口每条可撤回语句一次调用（快照 SELECT
// 加锁 → 原语句 → 按同一快照回读改后的值）；表格整组修改一次调用多步（结构化行编辑，
// 驱动须同时声明对应的 rows.* 特性）；撤回一次调用多步（按主键的 SQL 文本）。快照
// 行数超过 maxRows 时驱动回滚并答 exceeded，宿主与内置驱动一样返回 sqlsafety:reconfirm；
// 回复超过单行上限时驱动回滚并答 limit：读取拆小批次重试，写入转为不可撤回的重新确认。
//
// 驱动在同一次调用里提交，所以留底在提交后才能写入：写不进去时改存一条不可撤回的审计
// 记录并在结果里说明，绝不声称可撤回。撤回无法在写入前于驱动事务内核对行未被改动，
// 提交后发现预览后又变过的行会在结果中报告（新记录保存了被覆盖的值，可再撤回）。
// 未声明特性的驱动只警告（不可撤回），但非只读语句同样要求令牌。
// 日志只记录记录 ID 与行数，绝不记录语句文本或值。
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"mincode/app/backend/db/sqlsafety"
)

// ── 取消键 ──

type safetyQueryIDKey struct{}

// withSafetyQueryID carries the db_cancel_query key of a guarded plugin query.
func withSafetyQueryID(ctx context.Context, queryID string) context.Context {
	if queryID == "" {
		return ctx
	}
	return context.WithValue(ctx, safetyQueryIDKey{}, queryID)
}

func safetyQueryID(ctx context.Context) string {
	id, _ := ctx.Value(safetyQueryIDKey{}).(string)
	return id
}

// ── 执行 ──

// runPluginGuarded is runGuarded / runGuardedGroup for plugin sessions.
// Per statement: each undoable step is one snapshotWrite call, others run
// through plugin.db.execute as before. Batch: every step goes into one
// snapshotWrite call (all or nothing); merge works as in runGuardedGroup.
func runPluginGuarded(ctx context.Context, s *dbSession, check *sqlsafety.Check, steps []guardedStep, mode execMode, source string,
	merge func(r *guardedRun, pending []*pendingRecord) *pendingRecord) (*guardedResult, error) {
	if s.kind != kindPlugin || s.ext == nil {
		return nil, fmt.Errorf("该会话不支持 SQL 安全网执行")
	}
	r := &guardedRun{ctx: ctx, s: s, family: s.safetyFamily(), check: check, steps: steps, source: source,
		settings: safetySettings(), store: safetyRecords(), merge: merge, failedStep: -1}
	if source == "undo" {
		r.settings.Enabled = true
	}
	for _, st := range steps {
		undoable := st.Statement.Undoable && (st.Snapshot != "" || st.Wire != nil)
		if st.Statement.Undoable && (!undoable || !s.supportsSnapshotWrite() || st.Statement.Kind == sqlsafety.KindStructure) {
			return nil, fmt.Errorf("SQL 安全网：第 %d 条可撤回语句无法通过插件驱动留底，未执行任何语句", st.Statement.Index+1)
		}
	}
	if mode == execBatch {
		return r.runPluginBatch()
	}
	res := &guardedResult{}
	writes := 0
	for _, step := range r.steps {
		out, ids, err := r.runPluginStep(step)
		if err != nil {
			var unread *errExternalSnapshotUnread
			if errors.As(err, &unread) {
				writes++ // the driver committed it: it counts as applied
			}
			return nil, r.stopAt(writes, step, err)
		}
		res.add(out, ids)
		if step.Statement.Kind != sqlsafety.KindRead {
			writes++
		}
	}
	return res, nil
}

// pluginSnapshotted: the step is run with its before-image.
func (r *guardedRun) pluginSnapshotted(step guardedStep) bool {
	st := step.Statement
	return r.settings.Enabled && st.Kind != sqlsafety.KindRead && st.Undoable && (step.Snapshot != "" || step.Wire != nil)
}

// runPluginStep runs one statement of a query: snapshotted in one
// snapshotWrite call, or through plugin.db.execute with an audit record.
func (r *guardedRun) runPluginStep(step guardedStep) (stepOutcome, []string, error) {
	if !r.pluginSnapshotted(step) {
		out, err := r.pluginExec(step)
		if err != nil {
			return stepOutcome{}, nil, err
		}
		var ids []string
		if step.Statement.Kind != sqlsafety.KindRead {
			ids, _ = r.pluginSave([]*pendingRecord{r.unrecoverable(step, out.affected)})
		}
		return out, ids, nil
	}
	wire, maxRows, err := r.pluginWire(step)
	if err != nil {
		return stepOutcome{}, nil, err
	}
	resp, err := r.sendWire(r.stepDatabase(step), wire)
	var unread *errExternalSnapshotUnread
	switch {
	case errors.As(err, &unread):
		r.saveUnread([]guardedStep{step})
		return stepOutcome{}, nil, err
	case errors.Is(err, errExternalSnapshotLimit):
		return stepOutcome{}, nil, r.reconfirm(step, nil, sqlsafety.ReasonOverSnapshotLimit)
	case err != nil:
		return stepOutcome{}, nil, err
	case resp.exceeded:
		return stepOutcome{}, nil, r.pluginExceeded(step, maxRows)
	}
	out := r.pluginOutcome(step, resp.steps)
	ids, lost := r.pluginSave([]*pendingRecord{out.record})
	if lost {
		out.last.Notice = pluginBackupLostNotice()
	}
	return out, ids, nil
}

func pluginBackupLostNotice() *string {
	notice := "语句已执行，但留底未能保存，该操作不可撤回"
	return &notice
}

// pluginExceeded is the reconfirm of a step whose snapshot held more than
// maxRows rows (the driver rolled it back): over the backup limit, or more
// rows than a low-risk check allowed.
func (r *guardedRun) pluginExceeded(step guardedStep, maxRows int64) error {
	est := &sqlsafety.Estimate{Rows: maxRows + 1, Source: "count"}
	if maxRows >= r.settings.MaxSnapshotRows {
		return r.reconfirm(step, est, sqlsafety.ReasonOverSnapshotLimit)
	}
	return r.reconfirm(step, est, "")
}

// pluginWire is the snapshotWrite steps of step and the row bound of its
// snapshot: Wire as built by the caller, else snapshot → statement →
// re-read of the snapshot (an UPDATE's after-image, keyed by primary key).
func (r *guardedRun) pluginWire(step guardedStep) ([]ExternalSnapshotStep, int64, error) {
	if step.Wire != nil {
		return step.Wire, 0, nil
	}
	maxRows := r.settings.MaxSnapshotRows
	if step.Statement.Risk == sqlsafety.RiskLow {
		maxRows = min(r.settings.HighRiskRows, maxRows)
	}
	args, err := pluginArgs(step.Args)
	if err != nil {
		return nil, 0, err
	}
	snapshot := &ExternalSnapshotStatement{Statement: step.Snapshot, Args: args}
	w := ExternalSnapshotStep{Snapshot: snapshot, MaxRows: maxRows,
		Write: &ExternalSnapshotStatement{Statement: safetyExecText(r.family, r.s.safetyDialect(), step.Parsed.Text), Args: []any{}}}
	if len(step.Parsed.SetColumns) > 0 {
		after := snapshot
		if step.After != "" {
			after = &ExternalSnapshotStatement{Statement: step.After, Args: snapshot.Args}
		}
		w.After = after
	}
	return []ExternalSnapshotStep{w}, maxRows, nil
}

// saveUnread records, without undo, steps a driver committed but whose
// snapshotWrite reply could not be read.
func (r *guardedRun) saveUnread(steps []guardedStep) {
	var pending []*pendingRecord
	for _, step := range steps {
		if r.settings.Enabled && step.Statement.Kind != sqlsafety.KindRead {
			pending = append(pending, &pendingRecord{step: step, rec: r.record(step, 0, false, sqlsafety.ReasonNotUndoableStore)})
		}
	}
	r.saveAfterCommit(pending)
}

func (r *guardedRun) sendWire(database string, wire []ExternalSnapshotStep) (*externalSnapshotResponse, error) {
	return externalSnapshotWrite(r.ctx, r.s, externalSnapshotRequest{database: database, steps: wire, queryID: safetyQueryID(r.ctx)})
}

// stepDatabase is the database step's snapshotWrite call goes with.
func (r *guardedRun) stepDatabase(step guardedStep) string {
	if step.Database != nil {
		return *step.Database
	}
	return r.check.Database
}

// pluginTargetDatabase is the database a plugin request about table goes
// with: empty for an unqualified table, which the host resolved in the
// driver's own schema (an empty-database lookup), so the count, the
// snapshot and the statement address that same table even on drivers that
// switch to the request's database (dbdriver UseDatabase); database for a
// qualified one.
func pluginTargetDatabase(database string, table *sqlsafety.TableRef) string {
	if table != nil && table.Schema == "" {
		return ""
	}
	return database
}

// recordDatabase is the database plugin requests about rec's table go with
// (undo preview and undo): empty for a table resolved in the driver's own
// schema (Record.OwnSchema), else the record's database.
func recordDatabase(rec sqlsafety.Record) string {
	if rec.OwnSchema {
		return ""
	}
	return rec.Database
}

// runPluginBatch sends every step in one snapshotWrite call.
func (r *guardedRun) runPluginBatch() (*guardedResult, error) {
	var wire []ExternalSnapshotStep
	spans := make([][2]int, len(r.steps))
	for i, step := range r.steps {
		if step.Wire == nil {
			return nil, fmt.Errorf("SQL 安全网：第 %d 步无法通过插件驱动执行，未执行任何修改", i+1)
		}
		spans[i] = [2]int{len(wire), len(wire) + len(step.Wire)}
		wire = append(wire, step.Wire...)
	}
	if len(wire) > externalSnapshotMaxSteps {
		return nil, fmt.Errorf("SQL 安全网：这组修改需要 %d 步，超过插件驱动单次 %d 步的上限，未执行", len(wire), externalSnapshotMaxSteps)
	}
	database := r.check.Database
	if len(r.steps) > 0 {
		// Undo steps of one record share its database; grid steps the check's.
		database = r.stepDatabase(r.steps[0])
		for _, step := range r.steps[1:] {
			if r.stepDatabase(step) != database {
				return nil, errors.New("SQL 安全网：这组修改涉及不同的数据库，未执行任何修改")
			}
		}
	}
	resp, err := r.sendWire(database, wire)
	var unread *errExternalSnapshotUnread
	switch {
	case errors.As(err, &unread):
		r.saveUnread(r.steps)
		return nil, err
	case errors.Is(err, errExternalSnapshotLimit):
		if r.source == "undo" {
			return nil, errors.New("撤回涉及的数据超过插件驱动单次回复上限，未做任何修改")
		}
		return nil, r.reconfirmWithoutUndo(sqlsafety.ReasonOverSnapshotLimit)
	case err != nil:
		return nil, err
	case resp.exceeded:
		// By-key snapshots bind one key: more than one row means the key is not unique.
		for i, span := range spans {
			if resp.exceededStep >= span[0] && resp.exceededStep < span[1] {
				return nil, r.reconfirm(r.steps[i], nil, sqlsafety.ReasonNoPrimaryKey)
			}
		}
		return nil, externalProtocolError("插件返回的超限步骤无效")
	}
	res := &guardedResult{}
	var pending []*pendingRecord
	for i, step := range r.steps {
		results := resp.steps[spans[i][0]:spans[i][1]]
		out := r.pluginOutcome(step, results)
		if step.Verify != nil {
			res.Overwritten += step.Verify(results)
		}
		res.add(out, nil)
		pending = append(pending, out.record)
	}
	if r.merge != nil {
		pending = []*pendingRecord{r.merge(r, pending)}
	}
	ids, lost := r.pluginSave(pending)
	if lost && res.Last != nil {
		res.Last.Notice = pluginBackupLostNotice()
	}
	res.RecordIDs = ids
	return res, nil
}

// reconfirmWithoutUndo re-issues the check with every undoable statement
// marked not undoable for reason, so it runs without snapshots once confirmed.
func (r *guardedRun) reconfirmWithoutUndo(reason sqlsafety.Reason) error {
	if r.source == "undo" {
		return errUndoUnstorable(reason)
	}
	c := *r.check
	c.Statements = append([]sqlsafety.StatementCheck(nil), r.check.Statements...)
	for i := range c.Statements {
		sc := &c.Statements[i]
		if sc.Undoable {
			sc.Undoable = false
			sc.Reasons = append(append([]sqlsafety.Reason(nil), sc.Reasons...), reason)
		}
	}
	slog.Info("sql safety reconfirm without undo", "statements", len(c.Statements))
	return r.reissue(&c)
}

// pluginOutcome turns the results of a step's wire steps into its outcome
// and record: before-images, after-images (an UPDATE), the keys a keyed
// re-insert read back or an insert's returned values. A backup the driver
// returned incomplete (no key columns, more rows written than snapshotted,
// differing columns) makes the committed change audit-only.
func (r *guardedRun) pluginOutcome(step guardedStep, results []externalSnapshotStepResult) stepOutcome {
	var (
		columns       []string
		before, after [][]any
		reason        sqlsafety.Reason
	)
	take := func(img *externalSnapshotRows, into *[][]any) {
		if img == nil || len(img.columns) == 0 {
			return
		}
		switch {
		case columns == nil:
			columns = img.columns
		case !slices.Equal(columns, img.columns):
			reason = sqlsafety.ReasonUnparsed
		}
		*into = append(*into, img.rows...)
	}
	affected := pluginAffected(results, step.WireRows)
	for _, res := range results {
		take(res.before, &before)
		take(res.after, &after)
		if step.Capture != nil { // every wire step of a capturing run is an insert
			r.pluginCapture(step, res)
		}
	}
	last := &QueryResult{Columns: []string{}, Rows: [][]any{}}
	if affected >= 0 {
		n := affected
		last.AffectedRows = &n
	}
	out := stepOutcome{affected: affected, last: last}
	if !r.pluginSnapshotted(step) {
		out.record = r.unrecoverable(step, affected)
		return out
	}
	keyAt, ok := columnPositions(columns, step.PrimaryKey)
	if !ok && reason == "" {
		reason = sqlsafety.ReasonNoPrimaryKey
	}
	rec := r.record(step, affected, true, "")
	rows := sqlsafety.Rows{Before: before, After: after}
	rec.PrimaryKey, rec.Columns = append([]string(nil), step.PrimaryKey...), columns
	switch {
	case reason != "":
	case step.Wire == nil && affected < 0:
		// A rewritten statement's completeness check needs its row count.
		reason = sqlsafety.ReasonNotUndoableStore
	case step.Statement.Kind == sqlsafety.KindInsert:
		if affected >= 0 && int64(len(after)) != affected {
			reason = sqlsafety.ReasonNotUndoableStore
			break
		}
		keys := make([][]any, len(after))
		for i, row := range after {
			keys[i] = make([]any, len(keyAt))
			for j, at := range keyAt {
				keys[i][j] = row[at]
			}
		}
		rec.Columns, rows = append([]string(nil), step.PrimaryKey...), sqlsafety.Rows{Inserted: keys}
	case affected > int64(len(before)):
		// Rows matched after the snapshot: the backup is incomplete.
		reason = sqlsafety.ReasonNotUndoableStore
	}
	if reason != "" {
		slog.Warn("sql safety plugin backup incomplete, recorded without undo", "statement", step.Statement.Index, "reason", reason)
		out.record = &pendingRecord{step: step, rec: r.record(step, affected, false, reason)}
		return out
	}
	out.record = &pendingRecord{step: step, rec: rec, rows: rows}
	return out
}

// pluginCapture adds the key an insert's returned values report to the
// step's capture, or marks it missing.
func (r *guardedRun) pluginCapture(step guardedStep, res externalSnapshotStepResult) {
	c := step.Capture
	if res.returned == nil {
		c.missing = true
		return
	}
	key := make([]any, len(step.PrimaryKey))
	for i, col := range step.PrimaryKey {
		v, ok := res.returned[col]
		if !ok || v == nil {
			c.missing = true
			return
		}
		key[i] = v
	}
	c.keys = append(c.keys, key)
}

// pluginAffected sums the rows the wire steps changed, a row written by
// several steps (rows[i] equal) counting once; -1 when a step cannot say.
func pluginAffected(results []externalSnapshotStepResult, rows []int) int64 {
	perRow := map[int]int64{}
	for i, res := range results {
		if res.affected < 0 {
			return -1
		}
		row := i
		if i < len(rows) {
			row = rows[i]
		}
		perRow[row] = max(perRow[row], res.affected)
	}
	var total int64
	for _, n := range perRow {
		total += n
	}
	return total
}

// pluginSave stores the records of a committed snapshotWrite. An undoable
// record the store refuses becomes an audit-only one (the change is already
// committed); lost reports that.
func (r *guardedRun) pluginSave(pending []*pendingRecord) (ids []string, lost bool) {
	for _, p := range pending {
		if p == nil {
			continue
		}
		if p.rec.Undoable {
			err := r.store.Put(p.rec, p.rows)
			if err == nil {
				slog.Info("sql safety backup saved", "record", p.rec.ID, "before", len(p.rows.Before), "after", len(p.rows.After), "inserted", len(p.rows.Inserted))
				ids = append(ids, p.rec.ID)
				continue
			}
			slog.Warn("sql safety backup of a committed plugin change not saved", "record", p.rec.ID, "error", err)
			lost = true
			reason := sqlsafety.ReasonNotUndoableStore
			if errors.Is(err, sqlsafety.ErrOverStoreLimit) {
				reason = sqlsafety.ReasonOverSnapshotLimit
			}
			p.rec.ID = sqlsafety.NewRecordID()
			p.rec.Undoable, p.rec.UndoReason, p.rec.UndoState, p.rows = false, reason, "none", sqlsafety.Rows{}
		}
		ids = append(ids, r.saveAfterCommit([]*pendingRecord{p})...)
	}
	return ids, lost
}

// pluginExec runs a step's statement through plugin.db.execute (the
// session's usual write path) in the step's database: the one its target
// was resolved and its rows counted in (stepDatabase), so a statement that
// is not snapshotted still runs on the table its risk was rated from.
func (r *guardedRun) pluginExec(step guardedStep) (stepOutcome, error) {
	var database *string
	if d := r.stepDatabase(step); d != "" {
		database = &d
	}
	res, err := runExternalQuery(r.ctx, r.s, safetyExecText(r.family, r.s.safetyDialect(), step.Parsed.Text), database, safetyQueryID(r.ctx), false)
	if err != nil {
		return stepOutcome{}, err
	}
	affected := int64(-1)
	if res.AffectedRows != nil {
		affected = *res.AffectedRows
	}
	return stepOutcome{affected: affected, last: res}, nil
}

// ── 参数 ──

// pluginArg converts a value for a snapshotWrite argument, which must be a
// JSON scalar: text read as bytes goes back as text, a time as its literal
// text. Binary data and other types cannot be sent.
func pluginArg(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, json.Number, int, int8, int16, int32, int64, uint8, uint16, uint32:
		return x, nil
	case uint:
		return pluginArg(uint64(x))
	case uint64:
		if x <= math.MaxInt64 {
			return int64(x), nil
		}
		return json.Number(strconv.FormatUint(x, 10)), nil
	case float32:
		return pluginArg(float64(x))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errPluginArg
		}
		return x, nil
	case string:
		if utf8.ValidString(x) {
			return x, nil
		}
	case []byte:
		if x == nil {
			return nil, nil
		}
		if utf8.Valid(x) {
			return string(x), nil
		}
	case time.Time:
		if x.Location() == time.UTC {
			return x.Format("2006-01-02 15:04:05.999999999"), nil
		}
		return x.Format("2006-01-02 15:04:05.999999999Z07:00"), nil
	}
	return nil, errPluginArg
}

var errPluginArg = errors.New("该值（二进制数据或不支持的类型）无法通过插件驱动写回")

func pluginArgs(values []any) ([]any, error) {
	out := make([]any, len(values))
	for i, v := range values {
		a, err := pluginArg(v)
		if err != nil {
			return nil, err
		}
		out[i] = a
	}
	return out, nil
}

// ── 按主键读取当前行（撤回的冲突判定）──

// pluginReadByKey reads the rows of table with the given primary keys
// (columns; all when nil) through snapshotWrite steps whose write changes
// nothing, so they come back as typed, lossless cells. A reply over the
// line limit is retried with the keys split in half.
func pluginReadByKey(ctx context.Context, s *dbSession, database string, table sqlsafety.TableRef, pk, columns []string, keys [][]any) (map[string][]any, error) {
	noop := "UPDATE " + s.safetyQualify(table.Schema, table.Name) + " SET " + s.safetyQuote(pk[0]) + " = " + s.safetyQuote(pk[0]) + " WHERE 1 = 0"
	out := map[string][]any{}
	var read func(keys [][]any) error
	read = func(keys [][]any) error {
		q, err := undoKeyQuery(s, table, pk, columns, keys, false)
		if err != nil || len(q.batches) == 0 {
			return err
		}
		var resp *externalSnapshotResponse
		if len(q.batches) <= externalSnapshotMaxSteps {
			wire := make([]ExternalSnapshotStep, len(q.batches))
			per := int64(len(q.batches[0]) / len(pk))
			for i, args := range q.batches {
				a, err := pluginArgs(args)
				if err != nil {
					return err
				}
				wire[i] = ExternalSnapshotStep{Snapshot: &ExternalSnapshotStatement{Statement: q.sql, Args: a},
					Write: &ExternalSnapshotStatement{Statement: noop, Args: []any{}}, MaxRows: per}
			}
			resp, err = externalSnapshotWrite(ctx, s, externalSnapshotRequest{database: database, steps: wire})
		}
		if (resp == nil && err == nil || errors.Is(err, errExternalSnapshotLimit)) && len(keys) > 1 {
			if err := read(keys[:len(keys)/2]); err != nil {
				return err
			}
			return read(keys[len(keys)/2:])
		}
		switch {
		case err != nil:
			return err
		case resp == nil || resp.exceeded:
			return errors.New("按主键读取当前数据失败")
		}
		for _, res := range resp.steps {
			if res.before == nil {
				return errors.New("插件未返回当前数据")
			}
			keyAt, ok := columnPositions(res.before.columns, pk)
			if !ok {
				return errors.New("查询结果缺少主键列")
			}
			for _, row := range res.before.rows {
				key := make([]any, len(keyAt))
				for i, at := range keyAt {
					key[i] = row[at]
				}
				out[undoKeyString(key)] = row
			}
		}
		return nil
	}
	if err := read(keys); err != nil {
		return nil, err
	}
	return out, nil
}

// ── 撤回步骤 ──

// pluginUndoWire builds an undo step's snapshotWrite steps: per target a
// locked by-key snapshot of columns (all when nil), text with argsOf(target),
// and, with after, the same read after the write. verifyExpect makes Verify
// count targets whose before-image is missing or differs from expect.
func (b *undoSteps) pluginUndoWire(step *guardedStep, columns []string, text string, targets []undoTarget, argsOf func(undoTarget) []any, after, verifyExpect bool) error {
	if len(b.p.pk) == 0 {
		return errors.New("操作记录缺少主键，插件驱动无法撤回")
	}
	for _, t := range targets {
		q, err := undoKeyQuery(b.s, b.p.rec.Table, b.p.pk, columns, [][]any{t.row.key}, true)
		if err != nil {
			return err
		}
		key, err := pluginArgs(q.batches[0])
		if err != nil {
			return err
		}
		args, err := pluginArgs(argsOf(t))
		if err != nil {
			return err
		}
		w := ExternalSnapshotStep{Snapshot: &ExternalSnapshotStatement{Statement: q.sql, Args: key},
			Write: &ExternalSnapshotStatement{Statement: text, Args: args}, MaxRows: 1}
		if after {
			w.After = w.Snapshot
		}
		step.Snapshot = q.sql
		step.Wire = append(step.Wire, w)
	}
	if verifyExpect {
		step.Verify = func(results []externalSnapshotStepResult) int {
			changed := 0
			for i, t := range targets {
				if i >= len(results) || results[i].before == nil || len(results[i].before.rows) == 0 ||
					t.expect != nil && !sameRow(results[i].before.rows[0], t.expect) {
					changed++
				}
			}
			return changed
		}
	}
	return nil
}

// ── 表格整组修改 ──

// pluginRowsSupported: the driver declares the rows.* feature of every op.
func pluginRowsSupported(s *dbSession, changes []rowChange) bool {
	for _, c := range changes {
		feature := map[string]string{rowOpUpdate: externalFeatureRowsUpdate, rowOpInsert: externalFeatureRowsInsert, rowOpDelete: externalFeatureRowsDelete}[c.Op]
		if feature == "" || !s.ext.has(feature) {
			return false
		}
	}
	return true
}

// pluginRowsSteps is how many snapshotWrite steps a group needs: one per
// updated cell, inserted or deleted row.
func pluginRowsSteps(changes []rowChange) int {
	n := 0
	for _, c := range changes {
		if c.Op == rowOpUpdate {
			n += max(len(c.Values), 1)
		} else {
			n++
		}
	}
	return n
}

// pluginRowsWire builds a run's snapshotWrite steps: one structured
// mutation per updated cell, inserted or deleted row. An undoable run
// snapshots each key once (its first change) by primary key and, for
// updates and keyed inserts, reads it back after its last change. An
// insert without its key asks for the key columns back (capture).
func pluginRowsWire(s *dbSession, ref sqlsafety.TableRef, pk []string, op string, part []rowChange, undoable bool, capture *insertCapture) ([]ExternalSnapshotStep, []int, error) {
	type keyInfo struct{ first, last, row int }
	keys := map[string]*keyInfo{}
	ids := make([]string, len(part))
	for j, c := range part {
		var key []any
		var ok bool
		if op == rowOpInsert {
			key, ok = rowKeyIn(pk, c.Values)
		} else {
			key, ok = rowKey(pk, c.PrimaryKey)
		}
		id := strconv.Itoa(j) + "#"
		if ok {
			id = undoKeyString(key)
		}
		ids[j] = id
		if k := keys[id]; k != nil {
			k.last = j
		} else {
			keys[id] = &keyInfo{first: j, last: j, row: len(keys)}
		}
	}
	byKey := func(c rowChange) (*ExternalSnapshotStatement, error) {
		var key []any
		var ok bool
		if op == rowOpInsert {
			key, ok = rowKeyIn(pk, c.Values)
		} else {
			key, ok = rowKey(pk, c.PrimaryKey)
		}
		if !ok {
			return nil, &safetyError{Code: safetyCodeCheckInvalid}
		}
		q, err := undoKeyQuery(s, ref, pk, nil, [][]any{key}, true)
		if err != nil {
			return nil, err
		}
		args, err := pluginArgs(q.batches[0])
		if err != nil {
			return nil, err
		}
		return &ExternalSnapshotStatement{Statement: q.sql, Args: args}, nil
	}
	var wire []ExternalSnapshotStep
	var rows []int
	for j, c := range part {
		k := keys[ids[j]]
		var steps []ExternalSnapshotStep
		switch op {
		case rowOpUpdate:
			for _, column := range sortedKeys(c.Values) {
				steps = append(steps, ExternalSnapshotStep{Mutation: &ExternalSnapshotMutation{Op: ExternalMutationUpdateCell, Table: ref.Name,
					PrimaryKey: c.PrimaryKey, Column: column, Value: c.Values[column]}})
			}
		case rowOpDelete:
			steps = []ExternalSnapshotStep{{Mutation: &ExternalSnapshotMutation{Op: ExternalMutationDeleteRow, Table: ref.Name, PrimaryKey: c.PrimaryKey}}}
		default:
			w := ExternalSnapshotStep{Mutation: &ExternalSnapshotMutation{Op: ExternalMutationInsertRow, Table: ref.Name, Fields: c.Values}}
			if capture != nil {
				w.Returning = append([]string(nil), pk...)
			}
			steps = []ExternalSnapshotStep{w}
		}
		if undoable && k.first == j {
			q, err := byKey(c)
			if err != nil {
				return nil, nil, err
			}
			steps[0].Snapshot, steps[0].MaxRows = q, 1
		}
		if undoable && k.last == j && op != rowOpDelete {
			q, err := byKey(c)
			if err != nil {
				return nil, nil, err
			}
			steps[len(steps)-1].After = q
		}
		for range steps {
			rows = append(rows, k.row)
		}
		wire = append(wire, steps...)
	}
	return wire, rows, nil
}
