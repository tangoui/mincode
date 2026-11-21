package db

// kindPlugin sessions: the host-side proxy for plugin-provided database
// drivers (contributes.databaseDrivers, Phase E).
//
// The host owns the session: the WebView and every db_* command address it
// only by the host sessionId; plugin handles and cursors stay inside the
// ExternalConnection implementation (backend/plugin). This file routes
// db_connect through the registered ExternalDriverProvider (never falling back
// to a built-in driver), runs every plugin call under a per-session call set
// so that disconnect/CloseExternalSessions can cancel it, maps cancellation and
// timeouts to "<code>: detail" errors and reports QueryResult.executionMs as
// host wall-clock time up to the last validated page, like built-in drivers.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"mincode/app/backend/core"
	"mincode/app/backend/db/sqlsafety"
)

const (
	externalConnectTimeout    = 30 * time.Second
	externalCloseTimeout      = 5 * time.Second
	externalCancelGrace       = 5 * time.Second
	externalHostDeadlineSlack = 5 * time.Second
	externalMetadataTimeout   = 120 * time.Second
	externalRefMaxLength      = 128
	externalStatementMaxBytes = 768 * 1024
	externalSchemaFillMaxDBs  = 64

	// ExternalSessionsClosedEvent is emitted when plugin sessions are closed by
	// the plugin lifecycle: {sessionIds, pluginId, reason}.
	ExternalSessionsClosedEvent = "db:sessions-closed"
)

// Driver features (contribution contract list "features").
const (
	externalFeatureCancel           = "cancel"
	externalFeatureReadOnly         = "readOnly"
	externalFeatureTableRows        = "tableRows"
	externalFeatureRowsUpdate       = "rows.update"
	externalFeatureRowsInsert       = "rows.insert"
	externalFeatureRowsDelete       = "rows.delete"
	externalFeatureDropTable        = "destructive.dropTable"
	externalFeatureClearTable       = "destructive.clearTable"
	externalFeatureDropDatabase     = "destructive.dropDatabase"
	externalFeatureClearDatabase    = "destructive.clearDatabase"
	externalFeatureMetadataTables   = "metadata.tables"
	externalFeatureMetadataColumns  = "metadata.columns"
	externalFeatureMetadataPKs      = "metadata.primaryKeys"
	externalFeatureMetadataIndexes  = "metadata.indexes"
	externalFeatureMetadataFKs      = "metadata.foreignKeys"
	externalFeatureMetadataTriggers = "metadata.triggers"
	externalFeatureMetadataDDL      = "metadata.ddl"
	externalFeatureSnapshotWrite    = "transaction.snapshotWrite"
)

var externalKnownFeatures = map[string]bool{
	externalFeatureCancel: true, externalFeatureReadOnly: true, "multiDatabase": true,
	externalFeatureMetadataTables: true, externalFeatureMetadataColumns: true, externalFeatureMetadataPKs: true,
	externalFeatureMetadataIndexes: true, externalFeatureMetadataFKs: true, externalFeatureMetadataTriggers: true,
	externalFeatureMetadataDDL: true, externalFeatureTableRows: true,
	externalFeatureRowsUpdate: true, externalFeatureRowsInsert: true, externalFeatureRowsDelete: true,
	externalFeatureDropTable: true, externalFeatureClearTable: true,
	externalFeatureDropDatabase: true, externalFeatureClearDatabase: true,
	externalFeatureSnapshotWrite: true,
}

var externalKnownDialects = map[string]bool{
	"generic": true, "mysql": true, "postgres": true, "sqlite": true, "sqlserver": true,
	"oracle": true, "clickhouse": true, "duckdb": true, "trino": true,
}

var externalMetadataFeature = map[ExternalMetadataKind]string{
	ExternalMetadataNavigation:  "",
	ExternalMetadataTables:      externalFeatureMetadataTables,
	ExternalMetadataColumns:     externalFeatureMetadataColumns,
	ExternalMetadataPrimaryKeys: externalFeatureMetadataPKs,
	ExternalMetadataIndexes:     externalFeatureMetadataIndexes,
	ExternalMetadataForeignKeys: externalFeatureMetadataFKs,
	ExternalMetadataTriggers:    externalFeatureMetadataTriggers,
	ExternalMetadataDDL:         externalFeatureMetadataDDL,
}

var errExternalSessionClosed = errors.New("unavailable: 插件驱动会话已关闭")

// externalSession is the kindPlugin part of a dbSession.
type externalSession struct {
	conn     ExternalConnection
	info     ExternalConnectionInfo
	features map[string]bool
	dialect  string

	mu        sync.Mutex
	closed    bool
	calls     map[*externalCall]struct{}
	closeOnce sync.Once
}

// externalCall is one in-flight plugin call.
type externalCall struct {
	session     *externalSession
	requestID   string
	cancellable bool // requestID was sent to the plugin (plugin.db.cancel applies)
	ctx         context.Context
	cancel      context.CancelFunc

	userCancelled atomic.Bool
	sessionClosed atomic.Bool

	cancelOnce  sync.Once
	cancelDone  chan struct{}
	cancelState CancelState
	cancelErr   error
}

func newExternalSession(conn ExternalConnection, info ExternalConnectionInfo) *externalSession {
	features := map[string]bool{}
	for _, feature := range info.Features {
		if externalKnownFeatures[feature] {
			features[feature] = true
		}
	}
	dialect := strings.TrimSpace(info.Dialect)
	if !externalKnownDialects[dialect] {
		dialect = "generic"
	}
	return &externalSession{
		conn:     conn,
		info:     info,
		features: features,
		dialect:  dialect,
		calls:    map[*externalCall]struct{}{},
	}
}

func (e *externalSession) has(feature string) bool { return e.features[feature] }

func (e *externalSession) require(feature string) error {
	if feature == "" || e.has(feature) {
		return nil
	}
	return fmt.Errorf("unsupported: 插件驱动未声明功能 %s", feature)
}

// begin registers a call so that closing the session cancels it.
func (e *externalSession) begin(parent context.Context, cancellable bool) (*externalCall, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errExternalSessionClosed
	}
	ctx, cancel := context.WithCancel(parent)
	call := &externalCall{
		session:     e,
		requestID:   uuid.NewString(),
		cancellable: cancellable,
		ctx:         ctx,
		cancel:      cancel,
		cancelDone:  make(chan struct{}),
	}
	e.calls[call] = struct{}{}
	return call, nil
}

func (e *externalSession) end(call *externalCall) {
	e.mu.Lock()
	delete(e.calls, call)
	e.mu.Unlock()
	call.cancel()
}

// sendPluginCancel sends plugin.db.cancel once, bounded by the cancel grace.
func (call *externalCall) sendPluginCancel() {
	call.cancelOnce.Do(func() {
		if !call.cancellable {
			call.cancelState = CancelRequested
			close(call.cancelDone)
			return
		}
		go func() {
			defer close(call.cancelDone)
			ctx, cancel := context.WithTimeout(context.Background(), externalCancelGrace)
			defer cancel()
			state, err := call.session.conn.Cancel(ctx, call.requestID)
			call.cancelState, call.cancelErr = state, err
		}()
	})
}

// requestUserCancel is what db_cancel_query triggers for a plugin query.
func (call *externalCall) requestUserCancel() {
	call.userCancelled.Store(true)
	call.cancel()
	call.sendPluginCancel()
}

func (call *externalCall) waitCancelOutcome() (CancelState, error) {
	select {
	case <-call.cancelDone:
		return call.cancelState, call.cancelErr
	case <-time.After(externalCancelGrace + time.Second):
		return CancelRequested, errors.New("cancel outcome not received")
	}
}

func externalCancelledError(state CancelState) error {
	if state == CancelConfirmed {
		return errors.New("cancelled: 查询已取消（驱动已确认）")
	}
	return errors.New("cancelled: 已请求取消，但驱动未确认结果；语句可能已执行完成，MinCode 不会回滚")
}

func externalTimeoutError(timeout time.Duration, mutation bool) error {
	if mutation {
		return fmt.Errorf("timeout: 操作超过 %d 秒未完成；语句可能已执行完成，MinCode 不会回滚", int(timeout/time.Second))
	}
	return fmt.Errorf("timeout: 查询超过 %d 秒未完成，已请求驱动取消", int(timeout/time.Second))
}

var externalErrorCodes = []string{
	"not_found", "unavailable", "forbidden", "untrusted", "protocol", "timeout",
	"cancelled", "driver_error", "too_large", "busy", "unsupported", "invalid",
	"limit", // plugin.db.snapshotWrite: rolled back, the reply would not fit; retry smaller
}

func hasExternalErrorCode(message string) bool {
	for _, code := range externalErrorCodes {
		if strings.HasPrefix(message, code+":") {
			return true
		}
	}
	return false
}

// externalProviderError keeps "<code>: detail" provider errors and prefixes
// anything else with driver_error.
func externalProviderError(err error) error {
	if err == nil {
		return nil
	}
	if hasExternalErrorCode(err.Error()) {
		return err
	}
	return fmt.Errorf("driver_error: %w", err)
}

// callError maps the outcome of a failed plugin call. timeout is the budget
// reported to the user when the host deadline fired.
func (call *externalCall) callError(err error, timeout time.Duration, mutation bool) error {
	switch {
	case call.sessionClosed.Load():
		return errExternalSessionClosed
	case call.userCancelled.Load():
		state, _ := call.waitCancelOutcome()
		return externalCancelledError(state)
	case errors.Is(call.ctx.Err(), context.DeadlineExceeded):
		call.sendPluginCancel() // best effort, not awaited
		return externalTimeoutError(timeout, mutation)
	}
	return externalProviderError(err)
}

// shutdown marks the session closed and cancels every in-flight call. It
// reports whether this was the first shutdown.
func (e *externalSession) shutdown() bool {
	first := false
	e.closeOnce.Do(func() {
		first = true
		e.mu.Lock()
		e.closed = true
		calls := make([]*externalCall, 0, len(e.calls))
		for call := range e.calls {
			calls = append(calls, call)
		}
		e.mu.Unlock()
		for _, call := range calls {
			call.sessionClosed.Store(true)
			call.cancel()
		}
	})
	return first
}

func (e *externalSession) closeConn() {
	ctx, cancel := context.WithTimeout(context.Background(), externalCloseTimeout)
	defer cancel()
	_ = e.conn.Close(ctx)
}

// close cancels in-flight calls and closes the plugin handle (best effort, 5 s).
func (e *externalSession) close() {
	if e == nil || !e.shutdown() {
		return
	}
	e.closeConn()
}

// closeAsync is close without waiting for plugin.db.close; used from the
// plugin lifecycle hook so the caller never blocks on the plugin.
func (e *externalSession) closeAsync() {
	if e == nil || !e.shutdown() {
		return
	}
	go e.closeConn()
}

// ── connect ──

func validateExternalRef(cfg *DbConnectionConfig) (ExternalDriverRef, error) {
	ref := ExternalDriverRef{PluginID: strings.TrimSpace(cfg.Driver.PluginID), DriverID: strings.TrimSpace(cfg.Driver.DriverID)}
	if ref.PluginID == "" || ref.DriverID == "" || len(ref.PluginID) > externalRefMaxLength || len(ref.DriverID) > externalRefMaxLength {
		return ExternalDriverRef{}, errors.New("invalid: 插件驱动引用无效")
	}
	connectionID := strings.TrimSpace(cfg.ConnectionID)
	if connectionID == "" || len(connectionID) > externalRefMaxLength {
		return ExternalDriverRef{}, errors.New("invalid: 插件驱动连接缺少有效的 connectionId")
	}
	cfg.ConnectionID = connectionID
	return ref, nil
}

// connectExternalSession builds a kindPlugin session through the registered
// provider. It never falls back to a built-in driver or another plugin.
// cfg is already decrypted and must not be logged.
func connectExternalSession(parent context.Context, cfg *DbConnectionConfig) (*dbSession, error) {
	ref, err := validateExternalRef(cfg)
	if err != nil {
		return nil, err
	}
	provider := registeredExternalDriverProvider()
	if provider == nil {
		return nil, errExternalDriverUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, externalConnectTimeout)
	defer cancel()
	conn, err := provider.Connect(ctx, ExternalConnectRequest{
		Ref:          ref,
		ConnectionID: cfg.ConnectionID,
		Config:       *cfg,
		Options:      cfg.Options,
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !hasExternalErrorCode(err.Error()) {
			return nil, fmt.Errorf("timeout: 插件驱动连接超过 %d 秒未完成", int(externalConnectTimeout/time.Second))
		}
		return nil, externalProviderError(err)
	}
	if conn == nil {
		return nil, externalProtocolError("插件驱动未返回连接")
	}
	info := conn.Info()
	if info.PluginID != ref.PluginID || info.DriverID != ref.DriverID {
		go func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), externalCloseTimeout)
			defer closeCancel()
			_ = conn.Close(closeCtx)
		}()
		return nil, externalProtocolError("插件驱动连接与请求的驱动不一致")
	}
	info.DefaultDatabase = sanitizeExternalText(info.DefaultDatabase, externalNameMaxBytes)
	info.ServerVersion = sanitizeExternalText(info.ServerVersion, 128)
	ext := newExternalSession(conn, info)
	return &dbSession{kind: kindPlugin, dialect: "plugin", ext: ext, defaultDatabase: info.DefaultDatabase}, nil
}

// ── lifecycle ──

type externalSessionsClosedPayload struct {
	SessionIDs []string `json:"sessionIds"`
	PluginID   string   `json:"pluginId"`
	Reason     string   `json:"reason"`
}

// CloseExternalSessions closes every session backed by pluginID and returns
// their session ids. The plugin host calls it from its lifecycle hook (stop,
// crash, disable, reload, uninstall, dev generation change). It removes the
// sessions from the registry, cancels their in-flight calls (queries fail with
// "unavailable: 插件驱动会话已关闭"), closes the plugin handles in the
// background (never blocking the caller on the plugin) and emits
// db:sessions-closed {sessionIds, pluginId, reason}.
func CloseExternalSessions(pluginID string, reason string) []string {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return nil
	}
	removed := registry.removeWhere(func(s *dbSession) bool {
		return s.kind == kindPlugin && s.ext != nil && s.ext.info.PluginID == pluginID
	})
	if len(removed) == 0 {
		return nil
	}
	ids := make([]string, 0, len(removed))
	for id, s := range removed {
		ids = append(ids, id)
		s.ext.closeAsync()
	}
	sort.Strings(ids)
	reason = sanitizeExternalText(reason, 64)
	slog.Info("database plugin sessions closed", "plugin", pluginID, "count", len(ids), "reason", reason)
	core.Emit(ExternalSessionsClosedEvent, externalSessionsClosedPayload{SessionIDs: ids, PluginID: pluginID, Reason: reason})
	return ids
}

// ── execute ──

func validateExternalStatement(statement string) error {
	if strings.TrimSpace(statement) == "" {
		return errors.New("invalid: 查询语句为空")
	}
	if len(statement) > externalStatementMaxBytes {
		return fmt.Errorf("too_large: 查询语句超过 %d KiB", externalStatementMaxBytes/1024)
	}
	return nil
}

// runExternalExecute runs one user statement (SQL editor: readOnly false,
// mode auto) and feeds validated pages to pager. queryID, when set, is the
// frontend cancel key for db_cancel_query; the plugin only sees a host
// requestId.
func runExternalExecute(parent context.Context, s *dbSession, statement string, database *string, queryID string, timeout time.Duration, readOnly bool, pager *externalPager) (*ExternalExecuteSummary, error) {
	if err := validateExternalStatement(statement); err != nil {
		return nil, err
	}
	ext := s.ext
	call, err := ext.begin(parent, true)
	if err != nil {
		return nil, err
	}
	defer ext.end(call)
	registerCancel(queryID, call.requestUserCancel)
	defer unregisterCancel(queryID)

	req := ExternalExecute{
		RequestID: call.requestID,
		Statement: statement,
		Mode:      ExternalExecuteAuto,
		ReadOnly:  readOnly,
		MaxRows:   pager.maxRows,
		MaxBytes:  int64(pager.maxBytes),
		Timeout:   timeout,
	}
	if d := trimmedOptional(database); d != nil {
		req.Database = *d
	}
	summary, err := ext.conn.Execute(call.ctx, req, pager.page)
	if errors.Is(err, errExternalStopPaging) && pager.stopped {
		err, summary = nil, nil
	}
	if err != nil {
		return nil, call.callError(err, timeout, isDMLQuery(statement))
	}
	if summary != nil && summary.Cancelled != "" {
		return nil, externalCancelledError(summary.Cancelled)
	}
	if err := pager.finish(summary); err != nil {
		return nil, err
	}
	return summary, nil
}

// externalExecStatement runs one host-generated statement that returns no rows
// (schema sync DDL) through plugin.db.execute in exec mode. Rows the driver
// returns anyway are discarded.
func externalExecStatement(parent context.Context, s *dbSession, statement string, timeout time.Duration) error {
	if err := validateExternalStatement(statement); err != nil {
		return err
	}
	if s.ext.info.ReadOnly {
		return errors.New("forbidden: 插件驱动连接为只读")
	}
	ctx, cancel := context.WithTimeout(parent, timeout+externalHostDeadlineSlack)
	defer cancel()
	ext := s.ext
	call, err := ext.begin(ctx, true)
	if err != nil {
		return err
	}
	defer ext.end(call)
	pager := newExternalPager()
	summary, err := ext.conn.Execute(call.ctx, ExternalExecute{
		RequestID: call.requestID,
		Statement: statement,
		Mode:      ExternalExecuteExec,
		MaxRows:   pager.maxRows,
		MaxBytes:  int64(pager.maxBytes),
		Timeout:   timeout,
	}, pager.page)
	if errors.Is(err, errExternalStopPaging) && pager.stopped {
		err, summary = nil, nil
	}
	if err != nil {
		return call.callError(err, timeout, true)
	}
	if summary != nil && summary.Cancelled != "" {
		return externalCancelledError(summary.Cancelled)
	}
	return pager.finish(summary)
}

func externalExecuteQuery(s *dbSession, statement string, database *string, queryID string, readOnly bool) (*QueryResult, error) {
	return runExternalQuery(context.Background(), s, statement, database, queryID, readOnly)
}

// runExternalQuery runs one statement like the SQL editor does (mode auto)
// within parent and collects its result.
func runExternalQuery(parent context.Context, s *dbSession, statement string, database *string, queryID string, readOnly bool) (*QueryResult, error) {
	start := time.Now()
	timeout := timeoutForQuery(statement)
	ctx, cancel := context.WithTimeout(parent, timeout+externalHostDeadlineSlack)
	defer cancel()
	result := &QueryResult{Rows: [][]any{}}
	pager := newExternalPager()
	pager.onRow = func(row []any, _ int) error {
		result.Rows = append(result.Rows, row)
		return nil
	}
	summary, err := runExternalExecute(ctx, s, statement, database, queryID, timeout, readOnly, pager)
	if err != nil {
		return nil, err
	}
	affected, err := externalAffectedRows(summary)
	if err != nil {
		return nil, err
	}
	result.Columns = pager.resultColumns()
	result.RowCount = len(result.Rows)
	result.AffectedRows = affected
	result.Notice = pager.notice()
	result.ExecutionMs = time.Since(start).Milliseconds()
	return result, nil
}

// streamExternalQuery is db_execute_query_stream for plugin sessions: the host
// pulls plugin pages and re-chunks them exactly like streamSQLQuery.
func streamExternalQuery(parent context.Context, s *dbSession, statement string, database *string, queryID string, timeout time.Duration, readOnly bool, maxChunkRows, maxChunkBytes int, emit queryChunkEmitter) (*QueryStreamSummary, error) {
	start := time.Now()
	if maxChunkRows <= 0 {
		maxChunkRows = dbStreamChunkRows
	}
	if maxChunkBytes <= 0 {
		maxChunkBytes = dbStreamChunkBytes
	}
	summary := &QueryStreamSummary{QueryID: queryID}
	chunk := QueryResultChunk{QueryID: queryID, Rows: [][]any{}, EstimatedBytes: 128}
	rowTooLarge := false
	flush := func() error {
		if len(chunk.Rows) == 0 {
			return nil
		}
		chunk.Sequence = summary.ChunkCount
		if err := emit(chunk); err != nil {
			return err
		}
		summary.ChunkCount++
		chunk = QueryResultChunk{QueryID: queryID, Rows: [][]any{}, EstimatedBytes: 128}
		return nil
	}
	pager := newExternalPager()
	pager.onColumns = func(columns []string) error {
		chunk.Columns = columns
		chunk.EstimatedBytes = estimateColumnsBytes(columns)
		return nil
	}
	pager.onRow = func(row []any, rowBytes int) error {
		if len(chunk.Rows) > 0 && (len(chunk.Rows) >= maxChunkRows || chunk.EstimatedBytes+rowBytes > maxChunkBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		if rowBytes+chunk.EstimatedBytes > maxChunkBytes && len(chunk.Rows) == 0 {
			rowTooLarge = true
			return errExternalStopPaging
		}
		chunk.Rows = append(chunk.Rows, row)
		chunk.EstimatedBytes += rowBytes
		return nil
	}
	result, err := runExternalExecute(parent, s, statement, database, queryID, timeout, readOnly, pager)
	if err != nil {
		return nil, err
	}
	affected, err := externalAffectedRows(result)
	if err != nil {
		return nil, err
	}
	summary.RowCount = pager.rowCount
	summary.AffectedRows = affected
	extra := ""
	if rowTooLarge {
		extra = "查询结果已截断：单行数据超过流式分块内存边界，请缩小返回列"
	}
	summary.Notice = pager.notice(extra)
	if summary.RowCount == 0 {
		if pager.haveColumns {
			chunk.Sequence = 0
			if err := emit(chunk); err != nil {
				return nil, err
			}
			summary.ChunkCount = 1
		}
	} else if err := flush(); err != nil {
		return nil, err
	}
	summary.ExecutionMs = time.Since(start).Milliseconds()
	return summary, nil
}

// externalStreamQuery wires streamExternalQuery to the db-query-chunks stream.
// externalStreamQuery wires streamExternalQuery to the db-query-chunks stream
// (readOnly false, as plugin query-window reads have always been sent).
func externalStreamQuery(s *dbSession, statement string, database *string, queryID string) (*QueryStreamSummary, error) {
	timeout := timeoutForQuery(statement)
	ctx, cancel := context.WithTimeout(context.Background(), timeout+externalHostDeadlineSlack)
	defer cancel()
	return streamExternalQuery(ctx, s, statement, database, queryID, timeout, false, dbStreamChunkRows, dbStreamChunkBytes, func(chunk QueryResultChunk) error {
		emitQueryChunk(chunk)
		return ctx.Err()
	})
}

// ── table rows (read-only preview) ──

func externalTableRowsPage(ctx context.Context, s *dbSession, database, table string, limit, offset uint32, pageToken string) (*QueryResult, error) {
	start := time.Now()
	ext := s.ext
	if err := ext.require(externalFeatureTableRows); err != nil {
		return nil, err
	}
	if err := validateExternalTarget(database, table); err != nil {
		return nil, err
	}
	if len(pageToken) > externalPageTokenMaxBytes {
		return nil, errors.New("invalid: 分页令牌过长")
	}
	call, err := ext.begin(ctx, true)
	if err != nil {
		return nil, err
	}
	defer ext.end(call)
	result := &QueryResult{Rows: [][]any{}}
	pager := newExternalPager()
	pager.rowLimit = int(limit)
	pager.onRow = func(row []any, _ int) error {
		result.Rows = append(result.Rows, row)
		return nil
	}
	maxRows := int(limit)
	if maxRows > dbMaxResultRows {
		maxRows = dbMaxResultRows
	}
	summary, err := ext.conn.TableRows(call.ctx, ExternalTableRows{
		RequestID: call.requestID,
		Database:  database,
		Table:     table,
		Limit:     int(limit),
		Offset:    int(offset),
		PageToken: pageToken,
		MaxRows:   maxRows,
		MaxBytes:  dbMaxResultBytes,
	}, pager.page)
	if errors.Is(err, errExternalStopPaging) && pager.stopped {
		err, summary = nil, nil
	}
	if err != nil {
		return nil, call.callError(err, externalMetadataTimeout, false)
	}
	if summary != nil && summary.Cancelled != "" {
		return nil, externalCancelledError(summary.Cancelled)
	}
	if err := pager.finish(summary); err != nil {
		return nil, err
	}
	if summary != nil {
		token, err := validateExternalPageToken(summary.NextPageToken)
		if err != nil {
			return nil, err
		}
		result.NextPageToken = token
	}
	result.Columns = pager.resultColumns()
	result.RowCount = len(result.Rows)
	result.Notice = pager.notice()
	result.ExecutionMs = time.Since(start).Milliseconds()
	return result, nil
}

func validateExternalTarget(database, table string) error {
	if len(database) > externalNameMaxBytes || len(table) > externalNameMaxBytes {
		return errors.New("invalid: 数据库或表名过长")
	}
	if strings.TrimSpace(table) == "" {
		return errors.New("invalid: 数据表名称不能为空")
	}
	return nil
}

// ── metadata ──

func (e *externalSession) metadata(ctx context.Context, kind ExternalMetadataKind, database, table string, out any) error {
	feature, known := externalMetadataFeature[kind]
	if !known {
		return fmt.Errorf("unsupported: 未知元数据类型 %s", kind)
	}
	if err := e.require(feature); err != nil {
		return err
	}
	if len(database) > externalNameMaxBytes || len(table) > externalNameMaxBytes {
		return errors.New("invalid: 数据库或表名过长")
	}
	call, err := e.begin(ctx, false)
	if err != nil {
		return err
	}
	defer e.end(call)
	raw, err := e.conn.Metadata(call.ctx, ExternalMetadataRequest{Kind: kind, Database: database, Table: table})
	if err != nil {
		return call.callError(err, externalMetadataTimeout, false)
	}
	return decodeExternalMetadata(kind, raw, out)
}

func externalNavigationSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	var info DbSchemaInfo
	if err := s.ext.metadata(ctx, ExternalMetadataNavigation, "", "", &info); err != nil {
		return nil, err
	}
	if err := validateExternalSchemaInfo(&info, newExternalObjectBudget()); err != nil {
		return nil, err
	}
	normalizeSchemaInfo(&info)
	return &info, nil
}

// externalSchema is db_get_schema: navigation plus, with metadata.tables, the
// tables of databases the navigation left empty (bounded).
func externalSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	info, err := externalNavigationSchema(ctx, s)
	if err != nil {
		return nil, err
	}
	if !s.ext.has(externalFeatureMetadataTables) {
		return info, nil
	}
	budget := newExternalObjectBudget()
	if err := validateExternalSchemaInfo(info, budget); err != nil {
		return nil, err
	}
	filled := 0
	for i := range info.Databases {
		if len(info.Databases[i].Tables) > 0 || filled >= externalSchemaFillMaxDBs {
			continue
		}
		filled++
		tables, err := externalDatabaseTablesWithBudget(ctx, s, info.Databases[i].Name, budget)
		if err != nil {
			return nil, err
		}
		info.Databases[i].Tables = tables
	}
	normalizeSchemaInfo(info)
	return info, nil
}

func externalDatabaseTablesWithBudget(ctx context.Context, s *dbSession, database string, budget *externalObjectBudget) ([]SchemaTable, error) {
	var tables []SchemaTable
	if err := s.ext.metadata(ctx, ExternalMetadataTables, database, "", &tables); err != nil {
		return nil, err
	}
	if err := validateExternalSchemaTables(tables, budget); err != nil {
		return nil, err
	}
	if tables == nil {
		tables = []SchemaTable{}
	}
	return tables, nil
}

func externalDatabaseTables(ctx context.Context, s *dbSession, database string) ([]SchemaTable, error) {
	return externalDatabaseTablesWithBudget(ctx, s, database, newExternalObjectBudget())
}

func externalTableColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	if !s.ext.has(externalFeatureMetadataColumns) {
		if s.ext.has(externalFeatureTableRows) {
			return deriveColumnsFromRows(ctx, s, database, table)
		}
		return nil, s.ext.require(externalFeatureMetadataColumns)
	}
	var columns []ColumnInfo
	if err := s.ext.metadata(ctx, ExternalMetadataColumns, database, table, &columns); err != nil {
		return nil, err
	}
	if err := validateExternalColumnInfos(columns, newExternalObjectBudget()); err != nil {
		return nil, err
	}
	if columns == nil {
		columns = []ColumnInfo{}
	}
	return columns, nil
}

// externalPrimaryKeys returns an empty key set when the driver does not
// declare metadata.primaryKeys, which keeps row editing disabled.
func externalPrimaryKeys(ctx context.Context, s *dbSession, database, table string) (*PrimaryKeyInfo, error) {
	if !s.ext.has(externalFeatureMetadataPKs) {
		return &PrimaryKeyInfo{Columns: []string{}}, nil
	}
	var pk PrimaryKeyInfo
	if err := s.ext.metadata(ctx, ExternalMetadataPrimaryKeys, database, table, &pk); err != nil {
		return nil, err
	}
	if err := validateExternalNameList("主键列", pk.Columns, newExternalObjectBudget()); err != nil {
		return nil, err
	}
	if err := validateExternalName("约束", pk.ConstraintName, true); err != nil {
		return nil, err
	}
	if pk.Columns == nil {
		pk.Columns = []string{}
	}
	return &pk, nil
}

func externalIndexes(ctx context.Context, s *dbSession, database, table string) ([]DbIndexInfo, error) {
	var indexes []DbIndexInfo
	if err := s.ext.metadata(ctx, ExternalMetadataIndexes, database, table, &indexes); err != nil {
		return nil, err
	}
	if err := validateExternalIndexes(indexes, newExternalObjectBudget()); err != nil {
		return nil, err
	}
	if indexes == nil {
		indexes = []DbIndexInfo{}
	}
	return indexes, nil
}

func externalForeignKeys(ctx context.Context, s *dbSession, database, table string) ([]DbForeignKeyInfo, error) {
	var keys []DbForeignKeyInfo
	if err := s.ext.metadata(ctx, ExternalMetadataForeignKeys, database, table, &keys); err != nil {
		return nil, err
	}
	if err := validateExternalForeignKeys(keys, newExternalObjectBudget()); err != nil {
		return nil, err
	}
	if keys == nil {
		keys = []DbForeignKeyInfo{}
	}
	return keys, nil
}

func externalTriggers(ctx context.Context, s *dbSession, database, table string) ([]DbTriggerInfo, error) {
	var triggers []DbTriggerInfo
	if err := s.ext.metadata(ctx, ExternalMetadataTriggers, database, table, &triggers); err != nil {
		return nil, err
	}
	if err := validateExternalTriggers(triggers, newExternalObjectBudget()); err != nil {
		return nil, err
	}
	if triggers == nil {
		triggers = []DbTriggerInfo{}
	}
	return triggers, nil
}

func externalTableDDL(ctx context.Context, s *dbSession, database, table string) (string, error) {
	var ddl string
	if err := s.ext.metadata(ctx, ExternalMetadataDDL, database, table, &ddl); err != nil {
		return "", err
	}
	return ddl, nil
}

// ── row mutations ──

var externalMutationFeature = map[ExternalMutationOp]string{
	ExternalMutationUpdateCell: externalFeatureRowsUpdate,
	ExternalMutationInsertRow:  externalFeatureRowsInsert,
	ExternalMutationDeleteRow:  externalFeatureRowsDelete,
}

func externalMutate(ctx context.Context, s *dbSession, mutation ExternalMutation) (int64, error) {
	ext := s.ext
	if err := ext.require(externalMutationFeature[mutation.Op]); err != nil {
		return 0, err
	}
	if ext.info.ReadOnly {
		return 0, errors.New("forbidden: 插件驱动连接为只读")
	}
	if err := validateExternalTarget(mutation.Database, mutation.Table); err != nil {
		return 0, err
	}
	switch mutation.Op {
	case ExternalMutationUpdateCell:
		if len(mutation.PrimaryKey) == 0 {
			return 0, errors.New("invalid: db_update_cell 需要主键")
		}
		if strings.TrimSpace(mutation.Column) == "" || len(mutation.Column) > externalNameMaxBytes {
			return 0, errors.New("invalid: 列名无效")
		}
	case ExternalMutationDeleteRow:
		if len(mutation.PrimaryKey) == 0 {
			return 0, errors.New("invalid: db_delete_row 需要主键")
		}
	case ExternalMutationInsertRow:
		if len(mutation.Fields) == 0 {
			return 0, errors.New("invalid: db_insert_row 需要字段")
		}
	default:
		return 0, fmt.Errorf("unsupported: 未知行操作 %s", mutation.Op)
	}
	call, err := ext.begin(ctx, true)
	if err != nil {
		return 0, err
	}
	defer ext.end(call)
	mutation.RequestID = call.requestID
	n, err := ext.conn.Mutate(call.ctx, mutation)
	if err != nil {
		return 0, call.callError(err, time.Duration(dbDMLTimeoutSecs)*time.Second, true)
	}
	if n < 0 {
		return 0, externalProtocolError("插件返回了负的影响行数")
	}
	return n, nil
}

// ── destructive operations ──

// externalDestructive keeps the built-in typed confirmation, verifies the
// target through plugin metadata and sends a structured action; the host
// never forwards SQL built by the WebView.
func externalDestructive(ctx context.Context, s *dbSession, action, database string, table *string, confirmation string) error {
	ext := s.ext
	var wire, feature string
	databaseLevel := false
	switch action {
	case "drop_table":
		wire, feature = "dropTable", externalFeatureDropTable
	case "clear_table":
		wire, feature = "clearTable", externalFeatureClearTable
	case "drop_database":
		wire, feature, databaseLevel = "dropDatabase", externalFeatureDropDatabase, true
	case "clear_database":
		wire, feature, databaseLevel = "clearDatabase", externalFeatureClearDatabase, true
	default:
		return errors.New("unsupported: 插件驱动不支持该危险操作")
	}
	if err := ext.require(feature); err != nil {
		return err
	}
	if err := ext.require(externalFeatureMetadataTables); err != nil {
		return err
	}
	if ext.info.ReadOnly {
		return errors.New("forbidden: 插件驱动连接为只读")
	}
	tableName := strings.TrimSpace(optionalString(table))
	if strings.TrimSpace(database) == "" {
		return fmt.Errorf("数据库名称不能为空")
	}
	target := database
	if databaseLevel {
		if table != nil {
			return fmt.Errorf("库级危险操作不能指定数据表")
		}
	} else {
		if tableName == "" {
			return fmt.Errorf("数据表名称不能为空")
		}
		target += "." + tableName
	}
	if confirmation != target {
		return fmt.Errorf("人工确认内容与目标对象不一致")
	}
	if databaseLevel {
		if len(database) > externalNameMaxBytes {
			return errors.New("invalid: 数据库名称过长")
		}
	} else if err := validateExternalTarget(database, tableName); err != nil {
		return err
	}
	timeout := time.Duration(dbDMLTimeoutSecs) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if databaseLevel {
		info, err := externalNavigationSchema(ctx, s)
		if err != nil {
			return fmt.Errorf("校验目标对象: %w", err)
		}
		if _, ok := findSchemaDatabase(info, database); !ok {
			return fmt.Errorf("目标数据库或模式不存在")
		}
	} else {
		tables, err := externalDatabaseTables(ctx, s, database)
		if err != nil {
			return fmt.Errorf("校验目标对象: %w", err)
		}
		if !containsDataTable(tables, tableName) {
			return fmt.Errorf("目标数据表不存在或不是普通数据表")
		}
	}
	call, err := ext.begin(ctx, false)
	if err != nil {
		return err
	}
	defer ext.end(call)
	if err := ext.conn.Destructive(call.ctx, wire, database, tableName); err != nil {
		return fmt.Errorf("执行危险操作: %w", call.callError(err, timeout, true))
	}
	return nil
}

// externalExportDialect is the SQL dialect for INSERT export; generic drivers
// have no host quoting rules.
func externalExportDialect(s *dbSession) (string, error) {
	if s.ext.dialect == "generic" {
		return "", errors.New("unsupported: 通用 SQL 方言的插件驱动不支持导出 SQL INSERT")
	}
	return s.ext.dialect, nil
}

// ── snapshot writes (SQL 安全网) ──

const (
	externalSnapshotMaxSteps = 1000
	externalSnapshotMaxRows  = 1_000_000
)

// errExternalSnapshotLimit: the driver rolled a snapshotWrite back because
// its reply would not fit one message (error kind "limit"); nothing was
// written, and smaller batches may succeed.
var errExternalSnapshotLimit = errors.New("limit: 留底结果超过插件单次回复上限，已回滚")

// ErrExternalSnapshotReply is what a provider returns (wrapped) when a
// plugin.db.snapshotWrite call succeeded but its reply cannot be decoded:
// the driver committed, the host has no before-image.
var ErrExternalSnapshotReply = errors.New("protocol: 插件返回的留底结果无法解析")

// errExternalSnapshotUnread: the driver answered success (its transaction
// committed) but the reply is unusable, so the change has no backup.
type errExternalSnapshotUnread struct{ err error }

func (e *errExternalSnapshotUnread) Error() string {
	return e.err.Error() + "；驱动已提交修改，但未能留底"
}
func (e *errExternalSnapshotUnread) Unwrap() error { return e.err }

// supportsSnapshotWrite reports a plugin session whose driver can run a
// write with its before-image in one transaction (plugin.db.snapshotWrite):
// it declares transaction.snapshotWrite, is writable, and its sqlDialect
// belongs to a SQL family the safety net can write snapshot queries in.
func (s *dbSession) supportsSnapshotWrite() bool {
	if s.kind != kindPlugin || s.ext == nil || !s.ext.has(externalFeatureSnapshotWrite) || s.ext.info.ReadOnly {
		return false
	}
	if _, ok := s.ext.conn.(ExternalSnapshotConnection); !ok {
		return false
	}
	return !s.safetyWithoutTransactions()
}

// externalSnapshotRequest is one snapshotWrite call; queryID, when set, is
// the db_cancel_query key that cancels it.
type externalSnapshotRequest struct {
	database string
	steps    []ExternalSnapshotStep
	queryID  string
}

// externalSnapshotRows is a validated before/after image: values decoded to
// the Go types sqlsafety.Store keeps.
type externalSnapshotRows struct {
	columns []string
	rows    [][]any
}

type externalSnapshotStepResult struct {
	before, after *externalSnapshotRows
	affected      int64 // -1: unknown
	returned      map[string]any
}

type externalSnapshotResponse struct {
	steps        []externalSnapshotStepResult
	exceeded     bool
	exceededStep int
}

// externalSnapshotWrite sends one plugin.db.snapshotWrite and validates the
// untrusted reply. A "limit" reply is errExternalSnapshotLimit (nothing was
// written); other failures follow the usual "<code>: detail" mapping, where
// a timeout or unconfirmed cancel may have committed.
func externalSnapshotWrite(parent context.Context, s *dbSession, req externalSnapshotRequest) (*externalSnapshotResponse, error) {
	if !s.supportsSnapshotWrite() {
		return nil, errors.New("unsupported: 插件驱动不支持留底写入（transaction.snapshotWrite）")
	}
	if len(req.steps) == 0 || len(req.steps) > externalSnapshotMaxSteps {
		return nil, fmt.Errorf("invalid: 留底写入步骤数应为 1 ~ %d", externalSnapshotMaxSteps)
	}
	for _, st := range req.steps {
		if (st.Write == nil) == (st.Mutation == nil) || st.MaxRows < 0 || st.MaxRows > externalSnapshotMaxRows || (st.Snapshot != nil && st.MaxRows == 0) {
			return nil, errors.New("invalid: 留底写入步骤无效")
		}
		if st.Mutation != nil {
			if err := s.ext.require(externalMutationFeature[st.Mutation.Op]); err != nil {
				return nil, err
			}
		}
	}
	if len(req.database) > externalNameMaxBytes {
		return nil, errors.New("invalid: 数据库名称过长")
	}
	conn := s.ext.conn.(ExternalSnapshotConnection)
	timeout := time.Duration(dbDMLTimeoutSecs) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout+externalHostDeadlineSlack)
	defer cancel()
	call, err := s.ext.begin(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.ext.end(call)
	if req.queryID != "" {
		registerCancel(req.queryID, call.requestUserCancel)
		defer unregisterCancel(req.queryID)
	}
	raw, err := conn.SnapshotWrite(call.ctx, ExternalSnapshotRequest{RequestID: call.requestID, Database: req.database, Steps: req.steps})
	if errors.Is(err, ErrExternalSnapshotReply) {
		return nil, &errExternalSnapshotUnread{err: err}
	}
	if err != nil {
		err = call.callError(err, timeout, true)
		if strings.HasPrefix(err.Error(), "limit:") {
			return nil, errExternalSnapshotLimit
		}
		return nil, err
	}
	res, err := decodeExternalSnapshotResponse(raw, req.steps)
	if err != nil && (raw == nil || !raw.Exceeded) {
		// The driver answered success: its transaction committed.
		return nil, &errExternalSnapshotUnread{err: err}
	}
	return res, err
}

func decodeExternalSnapshotResponse(raw *ExternalSnapshotResponse, steps []ExternalSnapshotStep) (*externalSnapshotResponse, error) {
	switch {
	case raw == nil:
		return nil, externalProtocolError("插件未返回留底写入结果")
	case raw.Exceeded:
		if raw.ExceededStep < 0 || raw.ExceededStep >= len(steps) || steps[raw.ExceededStep].Snapshot == nil {
			return nil, externalProtocolError("插件返回的超限步骤无效")
		}
		return &externalSnapshotResponse{exceeded: true, exceededStep: raw.ExceededStep}, nil
	case len(raw.Steps) != len(steps):
		return nil, externalProtocolError("插件返回的留底写入步骤数与请求不一致")
	}
	out := &externalSnapshotResponse{steps: make([]externalSnapshotStepResult, len(steps))}
	for i, st := range raw.Steps {
		req := steps[i]
		// A snapshot answered without a before-image is taken as unreadable
		// (the record then has no key columns and is not undoable).
		if st.Affected < -1 || (st.Before != nil && req.Snapshot == nil) || (st.After != nil && req.After == nil) {
			return nil, externalProtocolError("插件返回的留底写入结果与请求不一致")
		}
		res := externalSnapshotStepResult{affected: st.Affected}
		var err error
		if res.before, err = decodeExternalSnapshotRows(st.Before, req.MaxRows); err != nil {
			return nil, err
		}
		if res.after, err = decodeExternalSnapshotRows(st.After, -1); err != nil {
			return nil, err
		}
		if len(st.Returned) > 0 {
			if req.Mutation == nil || len(req.Returning) == 0 {
				return nil, externalProtocolError("插件返回了未请求的新增行值")
			}
			res.returned = make(map[string]any, len(st.Returned))
			for name, cell := range st.Returned {
				if !slices.Contains(req.Returning, name) {
					return nil, externalProtocolError("插件返回了未请求的新增行值")
				}
				v, err := sqlsafety.DecodeCell(cell)
				if err != nil {
					return nil, externalProtocolError("插件返回的新增行值无效")
				}
				res.returned[name] = v
			}
		}
		out.steps[i] = res
	}
	return out, nil
}

// decodeExternalSnapshotRows validates an image: named columns within the
// column limit, one typed cell per column, at most maxRows rows (< 0: any).
func decodeExternalSnapshotRows(raw *ExternalSnapshotRows, maxRows int64) (*externalSnapshotRows, error) {
	if raw == nil {
		return nil, nil
	}
	if (len(raw.Columns) == 0 && len(raw.Rows) > 0) || len(raw.Columns) > externalColumnsMax || (maxRows >= 0 && int64(len(raw.Rows)) > maxRows) {
		return nil, externalProtocolError("插件返回的留底行无效")
	}
	out := &externalSnapshotRows{columns: make([]string, len(raw.Columns)), rows: make([][]any, len(raw.Rows))}
	for i, c := range raw.Columns {
		if err := validateExternalName("列", c.Name, false); err != nil {
			return nil, err
		}
		out.columns[i] = c.Name
	}
	for i, row := range raw.Rows {
		if len(row) != len(raw.Columns) {
			return nil, externalProtocolError("插件返回的留底行列数不一致")
		}
		values := make([]any, len(row))
		for j, cell := range row {
			v, err := sqlsafety.DecodeCell(cell)
			if err != nil {
				return nil, externalProtocolError("插件返回的留底值无效")
			}
			values[j] = v
		}
		out.rows[i] = values
	}
	return out, nil
}

// externalCount runs a host-built COUNT query read-only (the safety net's
// estimate, bounded by safetyEstimateTimeout) and returns its first cell.
func externalCount(parent context.Context, s *dbSession, database, statement string) (int64, error) {
	ctx, cancel := context.WithTimeout(parent, safetyEstimateTimeout)
	defer cancel()
	call, err := s.ext.begin(ctx, true)
	if err != nil {
		return 0, err
	}
	defer s.ext.end(call)
	var first []any
	pager := newExternalPager()
	pager.rowLimit = 1
	pager.onRow = func(row []any, _ int) error {
		if first == nil {
			first = row
		}
		return nil
	}
	summary, err := s.ext.conn.Execute(call.ctx, ExternalExecute{RequestID: call.requestID, Statement: statement, Database: database,
		Mode: ExternalExecuteQuery, ReadOnly: true, MaxRows: 1, MaxBytes: int64(pager.maxBytes), Timeout: safetyEstimateTimeout}, pager.page)
	if errors.Is(err, errExternalStopPaging) && pager.stopped {
		err, summary = nil, nil
	}
	if err != nil {
		return 0, call.callError(err, safetyEstimateTimeout, false)
	}
	if summary != nil && summary.Cancelled != "" {
		return 0, externalCancelledError(summary.Cancelled)
	}
	if len(first) == 0 {
		return 0, errors.New("estimate returned no rows")
	}
	if n, ok := planInt(first[0]); ok && n >= 0 {
		return n, nil
	}
	if num, ok := first[0].(json.Number); ok {
		if n, err := num.Int64(); err == nil && n >= 0 {
			return n, nil
		}
	}
	return 0, errors.New("estimate is not a row count")
}
