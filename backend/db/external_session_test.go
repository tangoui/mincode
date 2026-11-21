package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/db/sqlsafety"
)

// ── fakes ──

type fakeExternalConn struct {
	mu sync.Mutex

	info       ExternalConnectionInfo
	pages      []ExternalPage
	summary    *ExternalExecuteSummary
	execErr    error
	pageDelay  time.Duration
	block      bool
	started    chan struct{}
	metadata   map[ExternalMetadataKind]string
	cancelWith CancelState

	lastExec     ExternalExecute
	lastRows     ExternalTableRows
	pageErr      error
	cancelCalls  []string
	mutations    []ExternalMutation
	destructive  []string
	closeCount   int
	metadataReqs []ExternalMetadataRequest
	table        *fakeSnapshotTable // plugin.db.snapshotWrite (SQL safety net tests below)
	readOnlyErr  error              // returned for readOnly executes (a driver refusing a write)
	metadataErr  func(req ExternalMetadataRequest) error
	execs        []ExternalExecute
}

func newFakeExternalConn(features ...string) *fakeExternalConn {
	return &fakeExternalConn{
		info:       ExternalConnectionInfo{PluginID: "acme.db", DriverID: "acme", Dialect: "postgres", Features: features, DefaultDatabase: "main"},
		started:    make(chan struct{}, 8),
		metadata:   map[ExternalMetadataKind]string{},
		cancelWith: CancelConfirmed,
	}
}

func (f *fakeExternalConn) Info() ExternalConnectionInfo { return f.info }

func (f *fakeExternalConn) runPages(ctx context.Context, page func(ExternalPage) error) (*ExternalExecuteSummary, error) {
	f.started <- struct{}{}
	if f.block {
		<-ctx.Done()
		return nil, fmt.Errorf("driver_error: interrupted: %w", ctx.Err())
	}
	for _, p := range f.pages {
		if f.pageDelay > 0 {
			time.Sleep(f.pageDelay)
		}
		if err := page(p); err != nil {
			f.mu.Lock()
			f.pageErr = err
			f.mu.Unlock()
			return nil, err // stop paging and propagate unchanged
		}
	}
	if f.execErr != nil {
		return nil, f.execErr
	}
	return f.summary, nil
}

func (f *fakeExternalConn) Execute(ctx context.Context, req ExternalExecute, page func(ExternalPage) error) (*ExternalExecuteSummary, error) {
	f.mu.Lock()
	f.lastExec = req
	f.execs = append(f.execs, req)
	refuse := req.ReadOnly && f.readOnlyErr != nil
	f.mu.Unlock()
	if refuse {
		return nil, f.readOnlyErr
	}
	return f.runPages(ctx, page)
}

func (f *fakeExternalConn) Metadata(_ context.Context, req ExternalMetadataRequest) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metadataReqs = append(f.metadataReqs, req)
	if f.metadataErr != nil {
		if err := f.metadataErr(req); err != nil {
			return nil, err
		}
	}
	raw, ok := f.metadata[req.Kind]
	if !ok {
		return nil, errors.New("not_found: no metadata")
	}
	return json.RawMessage(raw), nil
}

func (f *fakeExternalConn) TableRows(ctx context.Context, req ExternalTableRows, page func(ExternalPage) error) (*ExternalExecuteSummary, error) {
	f.mu.Lock()
	f.lastRows = req
	f.mu.Unlock()
	return f.runPages(ctx, page)
}

func (f *fakeExternalConn) Mutate(_ context.Context, req ExternalMutation) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mutations = append(f.mutations, req)
	return 1, nil
}

func (f *fakeExternalConn) Destructive(_ context.Context, action, database, table string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destructive = append(f.destructive, action+":"+database+"."+table)
	return nil
}

func (f *fakeExternalConn) Cancel(_ context.Context, requestID string) (CancelState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls = append(f.cancelCalls, requestID)
	return f.cancelWith, nil
}

func (f *fakeExternalConn) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCount++
	return nil
}

func (f *fakeExternalConn) snapshot() (cancels []string, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cancelCalls...), f.closeCount
}

type fakeExternalProvider struct {
	mu    sync.Mutex
	conns []*fakeExternalConn
	next  int
	err   error
	reqs  []ExternalConnectRequest
	// allowed lists connection ids whose credential release is allowed.
	allowed map[string]bool
}

func (p *fakeExternalProvider) ReleaseAllowed(_ ExternalDriverRef, connectionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allowed[connectionID]
}

func (p *fakeExternalProvider) Connect(_ context.Context, req ExternalConnectRequest) (ExternalConnection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	if p.err != nil {
		return nil, p.err
	}
	conn := p.conns[p.next]
	p.next++
	return conn, nil
}

// withExternalProvider installs provider for one test only
// (RegisterExternalDriverProvider panics on a second call).
func withExternalProvider(t *testing.T, provider ExternalDriverProvider) {
	t.Helper()
	externalProviderMu.Lock()
	previous := externalProvider
	externalProvider = provider
	externalProviderMu.Unlock()
	t.Cleanup(func() {
		externalProviderMu.Lock()
		externalProvider = previous
		externalProviderMu.Unlock()
	})
}

func connectFakeSession(t *testing.T, conns ...*fakeExternalConn) (*fakeExternalProvider, []string) {
	t.Helper()
	provider := &fakeExternalProvider{conns: conns}
	withExternalProvider(t, provider)
	ids := make([]string, 0, len(conns))
	for range conns {
		out, err := dbConnectHandler("", `{"config":{"dbType":"plugin","host":"db.local","port":5432,"ssl":false,"password":"s3cret","driver":{"pluginId":"acme.db","driverId":"acme"},"connectionId":"conn-1","options":{"mode":"fast"}}}`)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		var id string
		if err := json.Unmarshal([]byte(out), &id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		t.Cleanup(func() { registry.remove(id) })
	}
	return provider, ids
}

func pluginSession(t *testing.T, id string) *dbSession {
	t.Helper()
	s, release, err := requireSession(id)
	if err != nil {
		t.Fatal(err)
	}
	release()
	return s
}

func cols(names ...string) []ExternalColumn {
	out := make([]ExternalColumn, len(names))
	for i, name := range names {
		out[i] = ExternalColumn{Name: name}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ── connect ──

func TestExternalConnectRoutesOnlyToProvider(t *testing.T) {
	conn := newFakeExternalConn()
	provider, ids := connectFakeSession(t, conn)
	s := pluginSession(t, ids[0])
	if s.kind != kindPlugin || s.ext == nil || s.defaultDatabase != "main" || s.ext.dialect != "postgres" {
		t.Fatalf("session = %+v", s)
	}
	req := provider.reqs[0]
	if req.Ref != (ExternalDriverRef{PluginID: "acme.db", DriverID: "acme"}) || req.ConnectionID != "conn-1" || req.Options["mode"] != "fast" {
		t.Fatalf("connect request = %+v", req)
	}
	if req.Config.Password == nil || *req.Config.Password != "s3cret" {
		t.Fatal("decrypted password must reach provider.Connect")
	}
	if strings.Contains(ids[0], "acme") {
		t.Fatal("session id must be a host id")
	}
}

func TestExternalConnectFailsClosed(t *testing.T) {
	withExternalProvider(t, nil)
	cfg := `{"config":{"dbType":"sqlite","host":"","port":0,"ssl":false,"filePath":":memory:","driver":{"pluginId":"acme.db","driverId":"acme"},"connectionId":"c"}}`
	if _, err := dbConnectHandler("", cfg); !errors.Is(err, errExternalDriverUnavailable) {
		t.Fatalf("nil provider error = %v", err)
	}
	withExternalProvider(t, &fakeExternalProvider{err: errors.New("forbidden: no credential grant")})
	if _, err := dbConnectHandler("", cfg); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("provider error = %v", err)
	}
	withExternalProvider(t, &fakeExternalProvider{err: errors.New("boom")})
	if _, err := dbConnectHandler("", cfg); err == nil || !strings.HasPrefix(err.Error(), "driver_error:") {
		t.Fatalf("uncoded provider error = %v", err)
	}
	if _, err := dbConnectHandler("", `{"config":{"dbType":"plugin","host":"","port":0,"ssl":false,"driver":{"pluginId":"acme.db","driverId":"acme"}}}`); err == nil || !strings.HasPrefix(err.Error(), "invalid:") {
		t.Fatalf("missing connectionId error = %v", err)
	}

	mismatch := newFakeExternalConn()
	mismatch.info.DriverID = "other"
	withExternalProvider(t, &fakeExternalProvider{conns: []*fakeExternalConn{mismatch}})
	if _, err := dbConnectHandler("", cfg); err == nil || !strings.HasPrefix(err.Error(), "protocol:") {
		t.Fatalf("mismatch error = %v", err)
	}
	waitFor(t, "mismatched connection close", func() bool { _, closes := mismatch.snapshot(); return closes == 1 })
}

// ── execute ──

func TestExternalExecuteValidatesAndNormalizesPages(t *testing.T) {
	conn := newFakeExternalConn()
	affected := int64(0)
	conn.pages = []ExternalPage{
		{Columns: cols("id", "payload"), Rows: [][]any{{int64(1), map[string]any{"a": []any{true, nil}}}}, Notice: "plugin‮ note\x07"},
		{Rows: [][]any{{int64(math.MaxInt64), json.Number("12345678901234567890123")}}},
	}
	conn.pageDelay = 20 * time.Millisecond
	conn.summary = &ExternalExecuteSummary{AffectedRows: &affected}
	_, ids := connectFakeSession(t, conn)

	result, err := externalExecuteQuery(pluginSession(t, ids[0]), "SELECT * FROM t", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || len(result.Columns) != 2 || result.Rows[1][0] != "9223372036854775807" || result.Rows[1][1] != "12345678901234567890123" {
		t.Fatalf("result = %+v", result)
	}
	if result.Notice == nil || *result.Notice != "plugin  note" {
		t.Fatalf("notice = %v", result.Notice)
	}
	if result.ExecutionMs < 40 {
		t.Fatalf("executionMs = %d, want host wall time through the last page", result.ExecutionMs)
	}
	if conn.lastExec.Mode != ExternalExecuteAuto || conn.lastExec.ReadOnly || conn.lastExec.MaxRows != dbMaxResultRows || conn.lastExec.RequestID == "" {
		t.Fatalf("execute request = %+v", conn.lastExec)
	}

	for name, page := range map[string]ExternalPage{
		"nan":         {Columns: cols("x"), Rows: [][]any{{math.NaN()}}},
		"cell count":  {Columns: cols("x"), Rows: [][]any{{1, 2}}},
		"cell type":   {Columns: cols("x"), Rows: [][]any{{struct{}{}}}},
		"rows before": {Rows: [][]any{{1}}},
	} {
		conn.pages = []ExternalPage{page}
		conn.pageDelay = 0
		if _, err := externalExecuteQuery(pluginSession(t, ids[0]), "SELECT 1", nil, "", true); err == nil || !strings.HasPrefix(err.Error(), "protocol:") {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
	deep := any("leaf")
	for i := 0; i < externalCellDepthMax+1; i++ {
		deep = []any{deep}
	}
	conn.pages = []ExternalPage{{Columns: cols("x"), Rows: [][]any{{deep}}}}
	if _, err := externalExecuteQuery(pluginSession(t, ids[0]), "SELECT 1", nil, "", true); err == nil || !strings.HasPrefix(err.Error(), "protocol:") {
		t.Fatalf("deep cell error = %v", err)
	}
	if _, err := externalExecuteQuery(pluginSession(t, ids[0]), strings.Repeat("x", externalStatementMaxBytes+1), nil, "", true); err == nil || !strings.HasPrefix(err.Error(), "too_large:") {
		t.Fatalf("statement cap error = %v", err)
	}
}

func TestExternalExecuteStopsAtHostCaps(t *testing.T) {
	conn := newFakeExternalConn()
	for p := 0; p < 6; p++ {
		page := ExternalPage{Rows: make([][]any, 1000)}
		if p == 0 {
			page.Columns = cols("n")
		}
		for i := range page.Rows {
			page.Rows[i] = []any{int64(p*1000 + i)}
		}
		conn.pages = append(conn.pages, page)
	}
	_, ids := connectFakeSession(t, conn)
	result, err := externalExecuteQuery(pluginSession(t, ids[0]), "SELECT n", nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != dbMaxResultRows || result.Notice == nil || !strings.Contains(*result.Notice, "查询结果已截断") {
		t.Fatalf("rowCount=%d notice=%v", result.RowCount, result.Notice)
	}
	if !errors.Is(conn.pageErr, errExternalStopPaging) {
		t.Fatalf("provider page error = %v, want the stop sentinel", conn.pageErr)
	}

	conn.pages = []ExternalPage{{Columns: cols("blob"), Rows: [][]any{{strings.Repeat("x", dbMaxResultBytes)}}}}
	result, err = externalExecuteQuery(pluginSession(t, ids[0]), "SELECT blob", nil, "", true)
	if err != nil || result.RowCount != 0 || result.Notice == nil {
		t.Fatalf("byte cap result=%+v err=%v", result, err)
	}
}

func TestExternalStreamChunksLikeBuiltins(t *testing.T) {
	conn := newFakeExternalConn()
	page := ExternalPage{Columns: cols("n"), Rows: make([][]any, 600)}
	for i := range page.Rows {
		page.Rows[i] = []any{int64(i)}
	}
	conn.pages = []ExternalPage{page}
	_, ids := connectFakeSession(t, conn)
	var chunks []QueryResultChunk
	emit := func(chunk QueryResultChunk) error { chunks = append(chunks, chunk); return nil }
	summary, err := streamExternalQuery(context.Background(), pluginSession(t, ids[0]), "SELECT n", nil, "q-stream", time.Minute, true, 256, dbStreamChunkBytes, emit)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowCount != 600 || summary.ChunkCount != 3 || len(chunks) != 3 || len(chunks[0].Rows) != 256 || chunks[2].Sequence != 2 || chunks[0].Columns[0] != "n" {
		t.Fatalf("summary=%+v chunks=%d", summary, len(chunks))
	}

	conn.pages = []ExternalPage{{Columns: cols("n")}}
	chunks = nil
	summary, err = streamExternalQuery(context.Background(), pluginSession(t, ids[0]), "SELECT n WHERE false", nil, "q-empty", time.Minute, true, 256, dbStreamChunkBytes, emit)
	if err != nil || summary.ChunkCount != 1 || len(chunks) != 1 || len(chunks[0].Rows) != 0 {
		t.Fatalf("empty summary=%+v chunks=%+v err=%v", summary, chunks, err)
	}
}

// ── cancellation / timeouts / close ──

func TestExternalCancelThroughDbCancelQuery(t *testing.T) {
	useSafetyStore(t)
	conn := newFakeExternalConn(externalFeatureCancel)
	conn.block = true
	_, ids := connectFakeSession(t, conn)
	errCh := make(chan error, 1)
	go func() {
		_, err := dbExecuteQueryHandler(context.Background(), "", fmt.Sprintf(`{"sessionId":%q,"query":"SELECT pg_sleep(60)","queryId":"q-cancel"}`, ids[0]))
		errCh <- err
	}()
	<-conn.started
	out, err := dbCancelQueryHandler("", `{"queryId":"q-cancel"}`)
	if err != nil || out != "true" {
		t.Fatalf("cancel = %s %v", out, err)
	}
	err = <-errCh
	if err == nil || !strings.HasPrefix(err.Error(), "cancelled:") || !strings.Contains(err.Error(), "已确认") {
		t.Fatalf("cancelled error = %v", err)
	}
	cancels, _ := conn.snapshot()
	if len(cancels) != 1 || cancels[0] != conn.lastExec.RequestID || cancels[0] == "q-cancel" {
		t.Fatalf("plugin cancel calls = %v (requestId %s)", cancels, conn.lastExec.RequestID)
	}

	// A write needs a check first, like on built-in sessions; this driver
	// has no snapshotWrite, so it runs through plugin.db.execute (warn only).
	conn.cancelWith = CancelRequested
	out, err = dbSafetyCheckHandler(context.Background(), "", fmt.Sprintf(`{"sessionId":%q,"query":"UPDATE t SET a=1"}`, ids[0]))
	if err != nil {
		t.Fatal(err)
	}
	var check struct {
		CheckID       string `json:"checkId"`
		ConfirmTarget string `json:"confirmTarget"`
	}
	if err := json.Unmarshal([]byte(out), &check); err != nil || check.CheckID == "" {
		t.Fatalf("check = %s %v", out, err)
	}
	go func() {
		_, err := dbExecuteQueryHandler(context.Background(), "", fmt.Sprintf(`{"sessionId":%q,"query":"UPDATE t SET a=1","queryId":"q-cancel-2","checkId":%q,"confirmText":%q}`,
			ids[0], check.CheckID, check.ConfirmTarget))
		errCh <- err
	}()
	<-conn.started
	_, _ = dbCancelQueryHandler("", `{"queryId":"q-cancel-2"}`)
	if err := <-errCh; err == nil || !strings.HasPrefix(err.Error(), "cancelled:") || !strings.Contains(err.Error(), "不会回滚") {
		t.Fatalf("unconfirmed cancel error = %v", err)
	}
}

func TestExternalHostDeadlineMapsToTimeout(t *testing.T) {
	conn := newFakeExternalConn()
	conn.block = true
	_, ids := connectFakeSession(t, conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := runExternalExecute(ctx, pluginSession(t, ids[0]), "SELECT 1", nil, "", time.Second, false, newExternalPager())
	if err == nil || !strings.HasPrefix(err.Error(), "timeout:") {
		t.Fatalf("timeout error = %v", err)
	}
	waitFor(t, "best-effort plugin cancel", func() bool { cancels, _ := conn.snapshot(); return len(cancels) == 1 })
}

func TestCloseExternalSessionsCancelsAndEmits(t *testing.T) {
	blocked := newFakeExternalConn()
	blocked.block = true
	idle := newFakeExternalConn()
	other := newFakeExternalConn()
	other.info.PluginID = "other.db"
	provider, ids := connectFakeSession(t, blocked, idle)
	provider.conns = append(provider.conns, other)
	otherOut, err := dbConnectHandler("", `{"config":{"dbType":"plugin","host":"","port":0,"ssl":false,"driver":{"pluginId":"other.db","driverId":"acme"},"connectionId":"c2"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var otherID string
	_ = json.Unmarshal([]byte(otherOut), &otherID)
	t.Cleanup(func() { registry.remove(otherID) })

	var events []externalSessionsClosedPayload
	var eventsMu sync.Mutex
	core.SetEmitter(func(name string, data any) {
		if name == ExternalSessionsClosedEvent {
			eventsMu.Lock()
			events = append(events, data.(externalSessionsClosedPayload))
			eventsMu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	errCh := make(chan error, 1)
	go func() {
		_, err := externalExecuteQuery(pluginSession(t, ids[0]), "SELECT 1", nil, "", true)
		errCh <- err
	}()
	<-blocked.started

	closed := CloseExternalSessions("acme.db", "crashed\n")
	if len(closed) != 2 || closed[0] > closed[1] {
		t.Fatalf("closed = %v", closed)
	}
	if err := <-errCh; err == nil || !strings.HasPrefix(err.Error(), "unavailable:") {
		t.Fatalf("in-flight error = %v", err)
	}
	for _, id := range ids {
		if _, _, err := requireSession(id); err == nil {
			t.Fatalf("session %s still registered", id)
		}
	}
	if _, release, err := requireSession(otherID); err != nil {
		t.Fatal("other plugin session must stay open")
	} else {
		release()
	}
	eventsMu.Lock()
	if len(events) != 1 || events[0].PluginID != "acme.db" || events[0].Reason != "crashed" || len(events[0].SessionIDs) != 2 {
		t.Fatalf("events = %+v", events)
	}
	eventsMu.Unlock()
	waitFor(t, "async plugin close", func() bool {
		_, a := blocked.snapshot()
		_, b := idle.snapshot()
		return a == 1 && b == 1
	})
	if again := CloseExternalSessions("acme.db", "again"); len(again) != 0 {
		t.Fatalf("second close = %v", again)
	}
}

// ── metadata / previews ──

func TestExternalMetadataGatingAndValidation(t *testing.T) {
	conn := newFakeExternalConn(externalFeatureMetadataTables, "metadata.bogus")
	conn.metadata[ExternalMetadataNavigation] = `{"databases":[{"name":"main","tables":[]},{"name":"aux","tables":[{"name":"t0","kind":"table"}]}]}`
	conn.metadata[ExternalMetadataTables] = `[{"name":"users","kind":"table"}]`
	_, ids := connectFakeSession(t, conn)
	s := pluginSession(t, ids[0])
	if s.ext.has("metadata.bogus") {
		t.Fatal("unknown features must be dropped")
	}
	ctx := context.Background()

	schema, err := getSchema(ctx, s)
	if err != nil || len(schema.Databases) != 2 || len(schema.Databases[0].Tables) != 1 || schema.Databases[0].Tables[0].Name != "users" {
		t.Fatalf("schema = %+v err=%v", schema, err)
	}
	if _, err := listIndexes(ctx, s, "main", "users"); err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
		t.Fatalf("ungated indexes error = %v", err)
	}
	pk, err := getPrimaryKeys(ctx, s, "main", "users")
	if err != nil || len(pk.Columns) != 0 {
		t.Fatalf("pk = %+v err=%v", pk, err)
	}
	if _, err := getTableColumns(ctx, s, "main", "users"); err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
		t.Fatalf("columns without feature error = %v", err)
	}

	conn.metadata[ExternalMetadataTables] = `{"items":[{"name":"users"}]}`
	if _, err := getDatabaseTables(ctx, s, "main"); err == nil || !strings.HasPrefix(err.Error(), "protocol:") {
		t.Fatalf("wrapped metadata error = %v", err)
	}
	var many strings.Builder
	many.WriteString("[")
	for i := 0; i <= externalMetadataMaxObject; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"name":"t%d"}`, i)
	}
	many.WriteString("]")
	conn.metadata[ExternalMetadataTables] = many.String()
	if _, err := getDatabaseTables(ctx, s, "main"); err == nil || !strings.HasPrefix(err.Error(), "too_large:") {
		t.Fatalf("object cap error = %v", err)
	}
	conn.metadata[ExternalMetadataTables] = `[{"name":""}]`
	if _, err := getDatabaseTables(ctx, s, "main"); err == nil || !strings.HasPrefix(err.Error(), "protocol:") {
		t.Fatalf("empty name error = %v", err)
	}
}

func TestExternalTableRowsPreview(t *testing.T) {
	conn := newFakeExternalConn()
	_, ids := connectFakeSession(t, conn)
	s := pluginSession(t, ids[0])
	ctx := context.Background()
	if _, err := getTableRowsPage(ctx, s, "main", "users", 2, 0, "", ""); err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
		t.Fatalf("ungated tableRows error = %v", err)
	}

	s.ext.features[externalFeatureTableRows] = true
	conn.pages = []ExternalPage{{Columns: cols("id"), Rows: [][]any{{int64(1)}, {int64(2)}, {int64(3)}}}}
	conn.summary = &ExternalExecuteSummary{NextPageToken: "cursor-2"}
	result, err := getTableRowsPage(ctx, s, "main", "users", 2, 0, "cursor-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || result.Notice != nil {
		t.Fatalf("preview = %+v", result)
	}
	if conn.lastRows.Limit != 2 || conn.lastRows.PageToken != "cursor-1" || conn.lastRows.Table != "users" {
		t.Fatalf("tableRows request = %+v", conn.lastRows)
	}
	if result.NextPageToken != nil {
		t.Fatal("a cursor the host stopped early has no usable nextPageToken")
	}
	conn.pages = []ExternalPage{{Columns: cols("id"), Rows: [][]any{{int64(1)}}}}
	result, err = getTableRowsPage(ctx, s, "main", "users", 2, 0, "", "")
	if err != nil || result.NextPageToken == nil || *result.NextPageToken != "cursor-2" {
		t.Fatalf("token result = %+v err=%v", result, err)
	}
	conn.summary = &ExternalExecuteSummary{NextPageToken: strings.Repeat("x", externalPageTokenMaxBytes+1)}
	if _, err := getTableRowsPage(ctx, s, "main", "users", 2, 0, "", ""); err == nil || !strings.HasPrefix(err.Error(), "protocol:") {
		t.Fatalf("token cap error = %v", err)
	}

	conn.summary = nil
	columns, err := getTableColumns(ctx, s, "main", "users")
	if err != nil || len(columns) != 1 || columns[0].Name != "id" {
		t.Fatalf("derived columns = %+v err=%v", columns, err)
	}
	if out, err := exportTable(ctx, s, "main", "users", "sql", "", 10); err != nil || !strings.Contains(out, `INSERT INTO "main"."users"`) {
		t.Fatalf("sql export = %q err=%v", out, err)
	}
	s.ext.dialect = "generic"
	if _, err := exportTable(ctx, s, "main", "users", "sql", "", 10); err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
		t.Fatalf("generic sql export error = %v", err)
	}
	if out, err := exportTable(ctx, s, "main", "users", "csv", "", 10); err != nil || !strings.HasPrefix(out, "id") {
		t.Fatalf("csv export = %q err=%v", out, err)
	}
}

// ── mutations ──

func TestExternalRowMutationsAndDestructive(t *testing.T) {
	conn := newFakeExternalConn(externalFeatureMetadataTables)
	conn.metadata[ExternalMetadataTables] = `[{"name":"users","kind":"table"},{"name":"v_users","kind":"view"}]`
	_, ids := connectFakeSession(t, conn)
	s := pluginSession(t, ids[0])
	ctx := context.Background()

	if _, err := updateCellByPrimaryKey(ctx, s, "main", "users", map[string]any{"id": 1}, "name", "x"); err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
		t.Fatalf("ungated update error = %v", err)
	}
	s.ext.features[externalFeatureRowsUpdate] = true
	s.ext.features[externalFeatureRowsInsert] = true
	s.ext.features[externalFeatureRowsDelete] = true
	if n, err := updateCellByPrimaryKey(ctx, s, "main", "users", map[string]any{"id": 1}, "name", "x"); err != nil || n != 1 {
		t.Fatalf("update = %d %v", n, err)
	}
	if _, err := deleteRowByPrimaryKey(ctx, s, "main", "users", nil); err == nil {
		t.Fatal("delete without primary key must fail")
	}
	if n, err := importData(ctx, s, "main", "users", "json", `[{"name":"a"},{"name":"b"}]`); err != nil || n != 2 {
		t.Fatalf("import = %d %v", n, err)
	}
	if len(conn.mutations) != 3 || conn.mutations[0].Op != ExternalMutationUpdateCell || conn.mutations[0].RequestID == "" || conn.mutations[2].Op != ExternalMutationInsertRow {
		t.Fatalf("mutations = %+v", conn.mutations)
	}
	s.ext.info.ReadOnly = true
	if _, err := insertRow(ctx, s, "main", "users", map[string]any{"a": 1}); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("read-only insert error = %v", err)
	}
	s.ext.info.ReadOnly = false

	table := "users"
	if err := executeDestructiveOperation(ctx, s, "drop_table", "main", &table, "main.users"); err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
		t.Fatalf("ungated drop error = %v", err)
	}
	s.ext.features[externalFeatureDropTable] = true
	if err := executeDestructiveOperation(ctx, s, "drop_table", "main", &table, "users"); err == nil || !strings.Contains(err.Error(), "人工确认") {
		t.Fatalf("confirmation error = %v", err)
	}
	view := "v_users"
	if err := executeDestructiveOperation(ctx, s, "drop_table", "main", &view, "main.v_users"); err == nil {
		t.Fatal("dropping a view through drop_table must fail")
	}
	if err := executeDestructiveOperation(ctx, s, "drop_database", "main", nil, "main"); err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
		t.Fatalf("drop_database error = %v", err)
	}
	if err := executeDestructiveOperation(ctx, s, "drop_table", "main", &table, "main.users"); err != nil {
		t.Fatal(err)
	}
	if len(conn.destructive) != 1 || conn.destructive[0] != "dropTable:main.users" {
		t.Fatalf("destructive = %v", conn.destructive)
	}

	// Database-level actions need their own feature, the database name as
	// confirmation, no table and a database the navigation metadata lists.
	conn.metadata[ExternalMetadataNavigation] = `{"databases":[{"name":"main","tables":[]},{"name":"staging","tables":[]}]}`
	s.ext.features[externalFeatureClearDatabase] = true
	s.ext.features[externalFeatureDropDatabase] = true
	if err := executeDestructiveOperation(ctx, s, "clear_database", "staging", nil, "staging.users"); err == nil || !strings.Contains(err.Error(), "人工确认") {
		t.Fatalf("database confirmation error = %v", err)
	}
	if err := executeDestructiveOperation(ctx, s, "drop_database", "staging", &table, "staging"); err == nil {
		t.Fatal("database action with a table must fail")
	}
	if err := executeDestructiveOperation(ctx, s, "drop_database", "missing", nil, "missing"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("missing database error = %v", err)
	}
	if err := executeDestructiveOperation(ctx, s, "clear_database", "staging", nil, "staging"); err != nil {
		t.Fatal(err)
	}
	if err := executeDestructiveOperation(ctx, s, "drop_database", "staging", nil, "staging"); err != nil {
		t.Fatal(err)
	}
	if got := conn.destructive[1:]; len(got) != 2 || got[0] != "clearDatabase:staging." || got[1] != "dropDatabase:staging." {
		t.Fatalf("database destructive = %v", got)
	}
	s.ext.info.ReadOnly = true
	if err := executeDestructiveOperation(ctx, s, "drop_database", "main", nil, "main"); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("read-only drop_database error = %v", err)
	}
	s.ext.info.ReadOnly = false
}

// ── schema sync and cloud read-only ──

// schemaSyncFakeConn is a plugin session with the metadata schema sync reads:
// table users(id, name) with a primary key, plus indexes from indexes.
func schemaSyncFakeConn(dialect string, columns, indexes string) *fakeExternalConn {
	conn := newFakeExternalConn(schemaSyncPluginFeatures...)
	conn.info.Dialect = dialect
	conn.metadata[ExternalMetadataNavigation] = `{"databases":[{"name":"app","tables":[{"name":"users","kind":"table"}]}]}`
	conn.metadata[ExternalMetadataTables] = `[{"name":"users","kind":"table"}]`
	conn.metadata[ExternalMetadataColumns] = columns
	conn.metadata[ExternalMetadataPrimaryKeys] = `{"columns":["id"],"constraintName":"users_pkey"}`
	conn.metadata[ExternalMetadataIndexes] = indexes
	return conn
}

func TestExternalSessionsSchemaSyncUsesDeclaredDialect(t *testing.T) {
	source := schemaSyncFakeConn("postgres",
		`[{"name":"id","dataType":"integer","nullable":false},{"name":"name","dataType":"text","nullable":true}]`,
		`[{"name":"users_name_idx","columns":["name"],"unique":false}]`)
	target := schemaSyncFakeConn("postgres", `[{"name":"id","dataType":"integer","nullable":false}]`, `[]`)
	_, ids := connectFakeSession(t, source, target)
	ctx := context.Background()
	src, dst := pluginSession(t, ids[0]), pluginSession(t, ids[1])

	comparison, err := compareSchemaSessions(ctx, src, "app", dst, "app")
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Source.Dialect != "postgres" || comparison.Target.Dialect != "postgres" {
		t.Fatalf("snapshot dialects = %q %q", comparison.Source.Dialect, comparison.Target.Dialect)
	}
	ids2 := make([]string, 0, len(comparison.Changes))
	for _, change := range comparison.Changes {
		ids2 = append(ids2, change.ID)
	}
	if len(ids2) != 2 {
		t.Fatalf("changes = %+v", comparison.Changes)
	}
	result, err := executeSchemaSync(ctx, src, "app", dst, "app", comparison.Source.Fingerprint, comparison.Target.Fingerprint, ids2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Statements) != 2 || result.Statements[0].Status != "succeeded" {
		t.Fatalf("execution = %+v", result)
	}
	// The DDL went to the target plugin as exec-mode statements, never to src.
	target.mu.Lock()
	last := target.lastExec
	target.mu.Unlock()
	if last.Mode != ExternalExecuteExec || last.ReadOnly || !strings.Contains(last.Statement, `"users"`) {
		t.Fatalf("target exec = %+v", last)
	}
	source.mu.Lock()
	sourceExec := source.lastExec.Statement
	source.mu.Unlock()
	if sourceExec != "" {
		t.Fatalf("source received DDL %q", sourceExec)
	}
}

func TestExternalSessionsSchemaSyncRejections(t *testing.T) {
	generic := schemaSyncFakeConn("generic", `[]`, `[]`)
	noIndexes := schemaSyncFakeConn("postgres", `[]`, `[]`)
	delete(noIndexes.metadata, ExternalMetadataIndexes)
	noIndexes.info.Features = []string{externalFeatureMetadataTables, externalFeatureMetadataColumns, externalFeatureMetadataPKs}
	mysql := schemaSyncFakeConn("mysql", `[]`, `[]`)
	postgres := schemaSyncFakeConn("postgres", `[]`, `[]`)
	_, ids := connectFakeSession(t, generic, noIndexes, mysql, postgres)
	ctx := context.Background()
	if _, err := compareSchemaSessions(ctx, pluginSession(t, ids[0]), "app", pluginSession(t, ids[3]), "app"); err == nil || !strings.Contains(err.Error(), "unsupported:") {
		t.Fatalf("generic dialect error = %v", err)
	}
	if _, err := compareSchemaSessions(ctx, pluginSession(t, ids[1]), "app", pluginSession(t, ids[3]), "app"); err == nil || !strings.Contains(err.Error(), externalFeatureMetadataIndexes) {
		t.Fatalf("missing feature error = %v", err)
	}
	if _, err := compareSchemaSessions(ctx, pluginSession(t, ids[2]), "app", pluginSession(t, ids[3]), "app"); err == nil || !strings.Contains(err.Error(), "同类型") {
		t.Fatalf("dialect mismatch error = %v", err)
	}
	target := pluginSession(t, ids[3])
	target.ext.info.ReadOnly = true
	if err := externalExecStatement(ctx, target, "DROP TABLE x", time.Second); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("read-only exec error = %v", err)
	}
}

func TestExternalSessionsCloudReadOnly(t *testing.T) {
	conn := newFakeExternalConn(externalFeatureReadOnly)
	conn.metadata[ExternalMetadataNavigation] = `{"databases":[{"name":"main","tables":[]}]}`
	rows := make([][]any, 0, 600)
	for i := 0; i < 600; i++ {
		rows = append(rows, []any{i})
	}
	conn.pages = []ExternalPage{{Columns: cols("n"), Rows: rows}}
	_, ids := connectFakeSession(t, conn)
	if _, err := CloudSchema(ids[0]); err != nil {
		t.Fatalf("cloud schema: %v", err)
	}
	result, err := CloudReadOnlyQuery(ids[0], "SELECT n FROM t")
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != cloudReadOnlyMaxRows || len(result.Rows) != cloudReadOnlyMaxRows {
		t.Fatalf("rows = %d", result.RowCount)
	}
	conn.mu.Lock()
	last := conn.lastExec
	conn.mu.Unlock()
	if !last.ReadOnly || last.Mode != ExternalExecuteQuery {
		t.Fatalf("cloud execute = %+v", last)
	}
	if _, err := CloudReadOnlyQuery(ids[0], "DELETE FROM t"); err == nil {
		t.Fatal("cloud read-only query must reject writes before reaching the plugin")
	}

	// A driver without readOnly is never opened for the cloud companion.
	unsafe := newFakeExternalConn()
	provider := &fakeExternalProvider{conns: []*fakeExternalConn{unsafe}}
	withExternalProvider(t, provider)
	password := "s3cret"
	_, err = ConnectCloudReadOnly(CloudReadOnlyConnection{Config: DbConnectionConfig{
		DbType: "plugin", Password: &password, ConnectionID: "conn-1",
		Driver: &ExternalDriverRef{PluginID: "acme.db", DriverID: "acme"},
	}})
	if err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
		t.Fatalf("cloud connect without readOnly = %v", err)
	}
	waitFor(t, "unsafe connection close", func() bool { _, closes := unsafe.snapshot(); return closes == 1 })
}

func TestExternalDriverReleaseAllowed(t *testing.T) {
	withExternalProvider(t, nil)
	ref := ExternalDriverRef{PluginID: "acme.db", DriverID: "acme"}
	if ExternalDriverReleaseAllowed(ref, "conn-1") {
		t.Fatal("no provider must not allow release")
	}
	withExternalProvider(t, &fakeExternalProvider{allowed: map[string]bool{"conn-1": true}})
	if !ExternalDriverReleaseAllowed(ref, "conn-1") || ExternalDriverReleaseAllowed(ref, "conn-2") || ExternalDriverReleaseAllowed(ref, "") {
		t.Fatal("release must follow the provider")
	}
}

func TestExternalSessionsStreamNeedsQueryID(t *testing.T) {
	_, ids := connectFakeSession(t, newFakeExternalConn())
	if _, err := dbExecuteQueryStreamHandler(context.Background(), "", fmt.Sprintf(`{"sessionId":%q,"query":"SELECT 1"}`, ids[0])); err == nil {
		t.Fatal("stream without queryId must fail")
	}
}

// ── SQL safety net: plugin.db.snapshotWrite ──

// fakeSnapshotTable is the users(id, name) table a fake plugin driver
// applies snapshotWrite steps to, inside an all-or-nothing "transaction".
type fakeSnapshotTable struct {
	mu    sync.Mutex
	rows  map[int64]string
	calls []ExternalSnapshotRequest
	// reply, when set, answers instead of the table.
	reply func(req ExternalSnapshotRequest) (*ExternalSnapshotResponse, error)
}

func (f *fakeExternalConn) SnapshotWrite(_ context.Context, req ExternalSnapshotRequest) (*ExternalSnapshotResponse, error) {
	t := f.table
	if t == nil {
		return nil, fmt.Errorf("unsupported: no snapshot table")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, req)
	if t.reply != nil {
		return t.reply(req)
	}
	work := map[int64]string{}
	for k, v := range t.rows {
		work[k] = v
	}
	res := &ExternalSnapshotResponse{}
	for i, step := range req.Steps {
		var out ExternalSnapshotStepResult
		if step.Snapshot != nil {
			out.Before = fakeSelect(work, step.Snapshot)
			if int64(len(out.Before.Rows)) > step.MaxRows {
				return &ExternalSnapshotResponse{Steps: []ExternalSnapshotStepResult{}, Exceeded: true, ExceededStep: i}, nil
			}
		}
		n, err := fakeApply(work, step)
		if err != nil {
			return nil, err
		}
		out.Affected = n
		if step.After != nil {
			out.After = fakeSelect(work, step.After)
		}
		if step.Mutation != nil && len(step.Returning) > 0 {
			out.Returned = map[string]json.RawMessage{"id": typedCell(int64(len(work) + 100))}
		}
		res.Steps = append(res.Steps, out)
	}
	t.rows = work
	return res, nil
}

func fakeInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return -1
}

// fakeSelect answers a by-key SELECT (args are keys) or, without args, the
// rows with id < 3 (the UPDATE the tests run).
func fakeSelect(work map[int64]string, st *ExternalSnapshotStatement) *ExternalSnapshotRows {
	out := &ExternalSnapshotRows{Columns: cols("id", "name"), Rows: [][]json.RawMessage{}}
	var keys []int64
	if len(st.Args) == 0 {
		for k := range work {
			if k < 3 {
				keys = append(keys, k)
			}
		}
	} else {
		for _, a := range st.Args {
			if k := fakeInt(a); !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		if name, ok := work[k]; ok {
			out.Rows = append(out.Rows, []json.RawMessage{typedCell(k), typedCell([]byte(name))})
		}
	}
	return out
}

func fakeApply(work map[int64]string, step ExternalSnapshotStep) (int64, error) {
	if m := step.Mutation; m != nil {
		switch m.Op {
		case ExternalMutationUpdateCell:
			k := fakeInt(m.PrimaryKey["id"])
			if _, ok := work[k]; !ok {
				return 0, nil
			}
			work[k] = fmt.Sprint(m.Value)
		case ExternalMutationDeleteRow:
			k := fakeInt(m.PrimaryKey["id"])
			if _, ok := work[k]; !ok {
				return 0, nil
			}
			delete(work, k)
		case ExternalMutationInsertRow:
			k, ok := m.Fields["id"]
			id := int64(len(work) + 100)
			if ok {
				id = fakeInt(k)
			}
			if _, dup := work[id]; dup {
				return 0, fmt.Errorf("database: duplicate key")
			}
			work[id] = fmt.Sprint(m.Fields["name"])
		}
		return 1, nil
	}
	text, args := step.Write.Statement, step.Write.Args
	switch {
	case strings.Contains(text, "1 = 0"):
		return 0, nil
	case strings.HasPrefix(text, "UPDATE") && len(args) == 0:
		var n int64
		for k := range work {
			if k < 3 {
				work[k], n = "z", n+1
			}
		}
		return n, nil
	case strings.HasPrefix(text, "UPDATE"):
		work[fakeInt(args[1])] = fmt.Sprint(args[0])
	case strings.HasPrefix(text, "INSERT"):
		work[fakeInt(args[0])] = fmt.Sprint(args[1])
	case strings.HasPrefix(text, "DELETE"):
		delete(work, fakeInt(args[0]))
	default:
		return 0, fmt.Errorf("database: unexpected statement")
	}
	return 1, nil
}

func typedCell(v any) json.RawMessage {
	var c map[string]any
	switch x := v.(type) {
	case nil:
		c = map[string]any{"t": "null"}
	case int64:
		c = map[string]any{"t": "int", "v": strconv.FormatInt(x, 10)}
	case string:
		c = map[string]any{"t": "str", "v": x}
	case []byte:
		c = map[string]any{"t": "bytes", "v": x} // encoding/json writes base64
	}
	b, _ := json.Marshal(c)
	return b
}

// pluginSafetyConn is a postgres-dialect plugin driver with users(id, name),
// primary key id, two rows and the given extra features.
func pluginSafetyConn(features ...string) *fakeExternalConn {
	base := []string{externalFeatureMetadataTables, externalFeatureMetadataColumns, externalFeatureMetadataPKs}
	conn := newFakeExternalConn(append(base, features...)...)
	conn.metadata[ExternalMetadataTables] = `[{"name":"users","kind":"table"}]`
	conn.metadata[ExternalMetadataColumns] = `[{"name":"id","dataType":"integer","nullable":false},{"name":"name","dataType":"text","nullable":true}]`
	conn.metadata[ExternalMetadataPrimaryKeys] = `{"columns":["id"]}`
	conn.pages = []ExternalPage{{Columns: cols("count"), Rows: [][]any{{2}}}}
	conn.table = &fakeSnapshotTable{rows: map[int64]string{1: "a", 2: "b", 3: "c"}}
	return conn
}

func pluginQueryCheck(t *testing.T, sessionID, query string) *sqlsafety.Check {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"sessionId": sessionID, "query": query, "database": "main"})
	out, err := dbSafetyCheckHandler(context.Background(), "", string(raw))
	if err != nil {
		t.Fatalf("db_safety_check: %v", err)
	}
	var c sqlsafety.Check
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func pluginExecute(sessionID, query, checkID, confirm string) (*QueryResult, error) {
	a := map[string]any{"sessionId": sessionID, "query": query, "database": "main"}
	out, err := dbExecuteQueryHandler(context.Background(), "", withToken(a, checkID, confirm))
	if err != nil {
		return nil, err
	}
	var res QueryResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	return &res, nil
}

const pluginUpdate = "UPDATE users SET name = 'z' WHERE id < 3"

func TestPluginGuardedExecuteUsesSnapshotWrite(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	conn := pluginSafetyConn(externalFeatureSnapshotWrite)
	_, ids := connectFakeSession(t, conn)

	if _, err := pluginExecute(ids[0], pluginUpdate, "", ""); err == nil || !strings.Contains(err.Error(), safetyCodeCheckRequired) {
		t.Fatalf("write without a token = %v, want checkRequired", err)
	}
	c := pluginQueryCheck(t, ids[0], pluginUpdate)
	if !c.Undoable || c.Risk != sqlsafety.RiskLow || c.Statements[0].Estimate.Rows != 2 || c.Statements[0].Estimate.Source != "count" {
		t.Fatalf("check = %+v", c)
	}
	if !conn.lastExec.ReadOnly || !strings.Contains(conn.lastExec.Statement, "COUNT(*)") {
		t.Fatalf("estimate ran as %+v, want a read-only COUNT", conn.lastExec)
	}
	res, err := pluginExecute(ids[0], pluginUpdate, c.CheckID, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.AffectedRows == nil || *res.AffectedRows != 2 {
		t.Fatalf("affected = %v", res.AffectedRows)
	}
	calls := conn.table.calls
	// The unqualified table was resolved in the driver's own schema: the call
	// goes with an empty database, like the lookup (pluginTargetDatabase).
	if len(calls) != 1 || len(calls[0].Steps) != 1 || calls[0].Database != "" {
		t.Fatalf("snapshotWrite calls = %+v", calls)
	}
	step := calls[0].Steps[0]
	if step.Snapshot == nil || !strings.Contains(step.Snapshot.Statement, `FROM "users"`) || !strings.HasSuffix(step.Snapshot.Statement, "FOR UPDATE") ||
		step.Write == nil || step.Write.Statement != pluginUpdate || step.After == nil || step.MaxRows != sqlsafety.DefaultSettings().HighRiskRows {
		t.Fatalf("step = %+v", step)
	}
	recs := listRecords(t, store)
	if len(recs) != 1 || !recs[0].Undoable || recs[0].Affected != 2 || recs[0].DbType != "plugin" {
		t.Fatalf("records = %+v", recs)
	}
	_, rows, err := store.Get(recs[0].ID)
	if err != nil || len(rows.Before) != 2 || len(rows.After) != 2 || string(rows.Before[0][1].([]byte)) != "a" || string(rows.After[0][1].([]byte)) != "z" {
		t.Fatalf("rows = %+v %v", rows, err)
	}

	// Undo: the conflict read and the restore are snapshotWrite calls; the
	// restore is one call with one step per row.
	before := len(conn.table.calls)
	if _, err := undo(t, ids[0], recs[0].ID, "skipConflicts", ""); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if got := conn.table.rows; got[1] != "a" || got[2] != "b" {
		t.Fatalf("after undo rows = %v", got)
	}
	restore := conn.table.calls[len(conn.table.calls)-1]
	if len(conn.table.calls)-before != 3 || len(restore.Steps) != 2 || restore.Steps[0].Write == nil || restore.Steps[0].Snapshot == nil {
		t.Fatalf("undo calls = %d, restore = %+v", len(conn.table.calls)-before, restore)
	}

	// More rows than the check allowed: rolled back by the driver, reconfirm.
	conn.table.rows = map[int64]string{1: "a", 2: "b", 0: "x", -1: "y"}
	useSafetySettings(t, sqlsafety.Settings{Enabled: true, HighRiskRows: 2, MaxSnapshotRows: 100, RetentionDays: 7, MaxTotalBytes: 1 << 30})
	c = pluginQueryCheck(t, ids[0], pluginUpdate)
	_, err = pluginExecute(ids[0], pluginUpdate, c.CheckID, "")
	w := safetyErrorOf(t, err, safetyCodeReconfirm)
	if w.Check == nil || w.Check.Risk != sqlsafety.RiskHigh {
		t.Fatalf("reconfirm check = %+v", w.Check)
	}
	if conn.table.rows[1] != "a" {
		t.Fatal("exceeded snapshot must leave the table unchanged")
	}

	// A reply too large for one line: rolled back, reconfirmed without undo.
	conn.table.reply = func(ExternalSnapshotRequest) (*ExternalSnapshotResponse, error) {
		return nil, fmt.Errorf("limit: snapshot results exceed the reply limit")
	}
	useSafetySettings(t, sqlsafety.DefaultSettings())
	c = pluginQueryCheck(t, ids[0], pluginUpdate)
	_, err = pluginExecute(ids[0], pluginUpdate, c.CheckID, "")
	w = safetyErrorOf(t, err, safetyCodeReconfirm)
	if w.Check.Undoable || !hasReason(w.Check.Statements[0].Reasons, sqlsafety.ReasonOverSnapshotLimit) {
		t.Fatalf("limit reconfirm = %+v", w.Check)
	}
}

func TestPluginWithoutFeatureWarnsOnly(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	conn := pluginSafetyConn()
	_, ids := connectFakeSession(t, conn)

	if _, err := pluginExecute(ids[0], pluginUpdate, "", ""); err == nil || !strings.Contains(err.Error(), safetyCodeCheckRequired) {
		t.Fatalf("write without a token = %v, want checkRequired", err)
	}
	c := pluginQueryCheck(t, ids[0], pluginUpdate)
	if c.Undoable || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonNotUndoableStore) {
		t.Fatalf("check = %+v", c)
	}
	conn.pages = nil
	conn.summary = &ExternalExecuteSummary{AffectedRows: new(int64)}
	if _, err := pluginExecute(ids[0], pluginUpdate, c.CheckID, ""); err != nil {
		t.Fatal(err)
	}
	if conn.lastExec.Statement != pluginUpdate || conn.lastExec.ReadOnly || len(conn.table.calls) != 0 {
		t.Fatalf("exec = %+v, snapshot calls = %d", conn.lastExec, len(conn.table.calls))
	}
	if recs := listRecords(t, store); len(recs) != 1 || recs[0].Undoable || recs[0].UndoReason != sqlsafety.ReasonNotUndoableStore {
		t.Fatalf("records = %+v", recs)
	}
	// Reads still run without a token.
	if _, err := pluginExecute(ids[0], "SELECT * FROM users", "", ""); err != nil {
		t.Fatal(err)
	}
	// Destructive operations and imports need their token on plugin sessions too.
	if err := destructive(ids[0], "drop_table", "main", "users", "main.users", "", ""); err == nil || !strings.Contains(err.Error(), safetyCodeCheckRequired) {
		t.Fatalf("destructive without a token = %v", err)
	}
	if _, err := dbImportDataHandler(context.Background(), "", withToken(importArgs(ids[0], "users", `[{"id":5}]`), "", "")); err == nil || !strings.Contains(err.Error(), safetyCodeCheckRequired) {
		t.Fatalf("import without a token = %v", err)
	}

	// An engine without transactions warns with noTransaction even when the
	// driver declares the feature.
	ch := pluginSafetyConn(externalFeatureSnapshotWrite)
	ch.info.Dialect = "clickhouse"
	_, chIDs := connectFakeSession(t, ch)
	c = pluginQueryCheck(t, chIDs[0], pluginUpdate)
	if c.Undoable || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonNoTransaction) {
		t.Fatalf("clickhouse check = %+v", c)
	}
}

func TestPluginApplyChangesSingleCall(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	conn := pluginSafetyConn(externalFeatureSnapshotWrite, externalFeatureRowsUpdate, externalFeatureRowsInsert, externalFeatureRowsDelete)
	_, ids := connectFakeSession(t, conn)
	changes := []map[string]any{
		{"op": "delete", "primaryKey": map[string]any{"id": 2}},
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"name": "one"}},
		{"op": "insert", "values": map[string]any{"id": 9, "name": "nine"}},
	}
	if _, err := applyChanges(t, ids[0], "users", changes, "", ""); err == nil || !strings.Contains(err.Error(), safetyCodeCheckRequired) {
		t.Fatalf("apply without a token = %v, want checkRequired", err)
	}
	c := opCheck(t, ids[0], rowsOp("users", changes), "main")
	if !c.Undoable {
		t.Fatalf("check = %+v", c)
	}
	res, err := applyChanges(t, ids[0], "users", changes, c.CheckID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(conn.table.calls) != 1 || len(conn.table.calls[0].Steps) != 3 {
		t.Fatalf("snapshotWrite calls = %+v", conn.table.calls)
	}
	for i, op := range []ExternalMutationOp{ExternalMutationDeleteRow, ExternalMutationUpdateCell, ExternalMutationInsertRow} {
		if m := conn.table.calls[0].Steps[i].Mutation; m == nil || m.Op != op || m.Table != "users" {
			t.Fatalf("step %d mutation = %+v", i, m)
		}
	}
	if got := conn.table.rows; got[1] != "one" || got[9] != "nine" || got[2] != "" {
		t.Fatalf("rows = %v", got)
	}
	rec, rows, err := store.Get(res.RecordID)
	if err != nil || !rec.Undoable || rec.Kind != sqlsafety.KindRows || len(rows.Before) != 2 || len(rows.After) != 1 || len(rows.Inserted) != 1 {
		t.Fatalf("record = %+v rows = %+v err = %v", rec, rows, err)
	}
	if _, err := undo(t, ids[0], rec.ID, "skipConflicts", ""); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if got := conn.table.rows; got[1] != "a" || got[2] != "b" || len(got) != 3 {
		t.Fatalf("after undo rows = %v", got)
	}

	// A driver without the matching rows.* feature gets no mutation steps.
	conn.table.calls = nil
	delete(pluginSession(t, ids[0]).ext.features, externalFeatureRowsInsert)
	c = opCheck(t, ids[0], rowsOp("users", changes[:2]), "main")
	if _, err := applyChanges(t, ids[0], "users", changes[:2], c.CheckID, ""); err != nil {
		t.Fatal(err)
	}
	if len(conn.table.calls) != 1 {
		t.Fatalf("update+delete group calls = %d", len(conn.table.calls))
	}
	c = opCheck(t, ids[0], rowsOp("users", changes[2:]), "main")
	if c.Undoable {
		t.Fatalf("insert without rows.insert must not be undoable: %+v", c)
	}
}

// A conflict read whose reply would not fit one message is retried with the
// keys split, never given up or taken as "no rows".
func TestPluginReadByKeySplitsOnLimit(t *testing.T) {
	conn := pluginSafetyConn(externalFeatureSnapshotWrite)
	_, ids := connectFakeSession(t, conn)
	conn.table.reply = func(req ExternalSnapshotRequest) (*ExternalSnapshotResponse, error) {
		st := req.Steps[0]
		if len(st.Snapshot.Args) > 1 || !strings.Contains(st.Write.Statement, "WHERE 1 = 0") {
			return nil, fmt.Errorf("limit: too large")
		}
		return &ExternalSnapshotResponse{Steps: []ExternalSnapshotStepResult{{Before: fakeSelect(conn.table.rows, st.Snapshot)}}}, nil
	}
	got, err := pluginReadByKey(context.Background(), pluginSession(t, ids[0]), "main", sqlsafety.TableRef{Schema: "main", Name: "users"},
		[]string{"id"}, nil, [][]any{{int64(1)}, {int64(2)}})
	if err != nil || len(got) != 2 || len(conn.table.calls) != 3 {
		t.Fatalf("read = %v %v after %d calls", got, err, len(conn.table.calls))
	}
}

// ── fix round 1 ──

// A rewritten statement whose row count the driver cannot report is saved
// without undo: its backup cannot be shown complete.
func TestPluginUnknownAffectedNotUndoable(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	conn := pluginSafetyConn(externalFeatureSnapshotWrite)
	_, ids := connectFakeSession(t, conn)
	conn.table.reply = func(req ExternalSnapshotRequest) (*ExternalSnapshotResponse, error) {
		return &ExternalSnapshotResponse{Steps: []ExternalSnapshotStepResult{{Before: &ExternalSnapshotRows{Columns: cols("id", "name"), Rows: [][]json.RawMessage{}},
			After: &ExternalSnapshotRows{Columns: cols("id", "name"), Rows: [][]json.RawMessage{}}, Affected: -1}}}, nil
	}
	c := pluginQueryCheck(t, ids[0], pluginUpdate)
	if _, err := pluginExecute(ids[0], pluginUpdate, c.CheckID, ""); err != nil {
		t.Fatal(err)
	}
	if recs := listRecords(t, store); len(recs) != 1 || recs[0].Undoable || recs[0].UndoReason != sqlsafety.ReasonNotUndoableStore {
		t.Fatalf("records = %+v", recs)
	}
}

// A reply that cannot be read after the driver committed: an audit record
// without undo, and the statement counts as applied in partial.
func TestPluginUnreadableReplyCountsAsApplied(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	for name, reply := range map[string]func(ExternalSnapshotRequest) (*ExternalSnapshotResponse, error){
		"proxy": func(ExternalSnapshotRequest) (*ExternalSnapshotResponse, error) {
			return nil, fmt.Errorf("%w (plugin.db.snapshotWrite)", ErrExternalSnapshotReply)
		},
		"shape": func(ExternalSnapshotRequest) (*ExternalSnapshotResponse, error) {
			return &ExternalSnapshotResponse{Steps: []ExternalSnapshotStepResult{}}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := useSafetyStore(t)
			conn := pluginSafetyConn(externalFeatureSnapshotWrite)
			_, ids := connectFakeSession(t, conn)
			conn.table.reply = reply
			c := pluginQueryCheck(t, ids[0], pluginUpdate)
			_, err := pluginExecute(ids[0], pluginUpdate, c.CheckID, "")
			w := safetyErrorOf(t, err, safetyCodePartial)
			if w.Applied == nil || *w.Applied != 1 || !strings.Contains(w.Message, "驱动已提交修改，但未能留底") {
				t.Fatalf("partial = %+v", w)
			}
			if recs := listRecords(t, store); len(recs) != 1 || recs[0].Undoable || recs[0].UndoReason != sqlsafety.ReasonNotUndoableStore {
				t.Fatalf("records = %+v", recs)
			}
		})
	}
}

// Plugin query-window reads keep going to the driver with readOnly false
// (execute and stream): driver read-only modes are narrower than the
// classifier, e.g. a read inside an open user transaction.
func TestPluginReadsSentReadWrite(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	conn := pluginSafetyConn()
	conn.pages, conn.summary = nil, &ExternalExecuteSummary{}
	conn.readOnlyErr = errors.New("driver_error: read-only execute refused")
	_, ids := connectFakeSession(t, conn)
	if _, err := pluginExecute(ids[0], "SELECT * FROM users", "", ""); err != nil || conn.lastExec.ReadOnly {
		t.Fatalf("read = %v, readOnly %v", err, conn.lastExec.ReadOnly)
	}
	out, err := dbExecuteQueryStreamHandler(context.Background(), "", fmt.Sprintf(`{"sessionId":%q,"query":"SELECT 1","queryId":"q-ro"}`, ids[0]))
	if err != nil || conn.lastExec.ReadOnly || conn.lastExec.Statement != "SELECT 1" {
		t.Fatalf("stream = %s %v, exec %+v", out, err, conn.lastExec)
	}
}

func TestPluginArgsRefuseBinary(t *testing.T) {
	r := &guardedRun{settings: sqlsafety.DefaultSettings()}
	if _, _, err := r.pluginWire(guardedStep{Snapshot: "SELECT 1", Args: []any{[]byte{0xff, 0x00}}}); !errors.Is(err, errPluginArg) {
		t.Fatalf("binary arg error = %v", err)
	}
}

// Destructive actions and imports on plugin sessions succeed with their token.
func TestPluginTokenedDestructiveAndImport(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	useTransferManager(t)
	conn := pluginSafetyConn(externalFeatureDropTable, externalFeatureRowsInsert)
	_, ids := connectFakeSession(t, conn)

	data := `[{"id":5,"name":"e"},{"id":6,"name":"f"}]`
	c := opCheck(t, ids[0], importOp("users", data), "main")
	if out, err := dbImportDataHandler(context.Background(), "", withToken(importArgs(ids[0], "users", data), c.CheckID, c.ConfirmTarget)); err != nil || out != "2" {
		t.Fatalf("import = %s %v", out, err)
	}
	if len(conn.mutations) != 2 || conn.mutations[0].Op != ExternalMutationInsertRow {
		t.Fatalf("import mutations = %+v", conn.mutations)
	}

	c = opCheck(t, ids[0], map[string]any{"kind": "destructive", "action": "drop_table", "table": "users"}, "main")
	if err := destructive(ids[0], "drop_table", "main", "users", "main.users", c.CheckID, c.ConfirmTarget); err != nil {
		t.Fatal(err)
	}
	if len(conn.destructive) != 1 || conn.destructive[0] != "dropTable:main.users" {
		t.Fatalf("destructive = %v", conn.destructive)
	}
	var found bool
	for _, rec := range listRecords(t, store) {
		if rec.Source == "destructive" {
			found = !rec.Undoable && rec.Table.Name == "users" && rec.ConnectionID == "conn-1"
		}
	}
	if !found {
		t.Fatalf("no destructive record in %+v", listRecords(t, store))
	}
}

// Schema sync to a plugin target needs its token too.
func TestPluginSchemaSyncNeedsToken(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	source := schemaSyncFakeConn("postgres",
		`[{"name":"id","dataType":"integer","nullable":false},{"name":"name","dataType":"text","nullable":true}]`, `[]`)
	target := schemaSyncFakeConn("postgres", `[{"name":"id","dataType":"integer","nullable":false}]`, `[]`)
	_, ids := connectFakeSession(t, source, target)
	comparison, err := compareSchemaSessions(context.Background(), pluginSession(t, ids[0]), "app", pluginSession(t, ids[1]), "app")
	if err != nil {
		t.Fatal(err)
	}
	var changeIDs []string
	for _, ch := range comparison.Changes {
		changeIDs = append(changeIDs, ch.ID)
	}
	args := map[string]any{"sourceSessionId": ids[0], "sourceDatabase": "app", "targetSessionId": ids[1], "targetDatabase": "app",
		"sourceFingerprint": comparison.Source.Fingerprint, "targetFingerprint": comparison.Target.Fingerprint, "selectedChangeIds": changeIDs}
	_, err = dbExecuteSchemaSyncHandler("", withToken(args, "", ""))
	safetyErrorOf(t, err, safetyCodeCheckRequired)
	c := opCheck(t, ids[1], map[string]any{"kind": "schemaSync", "sourceSessionId": ids[0], "sourceDatabase": "app",
		"sourceFingerprint": comparison.Source.Fingerprint, "targetFingerprint": comparison.Target.Fingerprint, "selectedChangeIds": changeIDs}, "app")
	out, err := dbExecuteSchemaSyncHandler("", withToken(args, c.CheckID, c.ConfirmTarget))
	if err != nil || !strings.Contains(out, `"completed":true`) {
		t.Fatalf("sync = %s %v", out, err)
	}
	if target.lastExec.Mode != ExternalExecuteExec {
		t.Fatalf("target exec = %+v", target.lastExec)
	}
	if recs := listRecords(t, store); len(recs) != len(changeIDs) || recs[0].Source != "designer" || recs[0].Undoable {
		t.Fatalf("records = %+v", recs)
	}
}

// Plugin drivers run unqualified names in the connection's own schema
// (connect info defaultDatabase), whatever database the request names, and
// their metadata may match names case-insensitively: the check must resolve
// in that schema and use the spelling the table listing reports.
func TestPluginSafetyResolvesUnqualifiedNamesInDefaultSchema(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	conn := pluginSafetyConn(externalFeatureSnapshotWrite)
	conn.metadata[ExternalMetadataTables] = `[{"name":"USERS","kind":"table"}]`
	_, ids := connectFakeSession(t, conn)

	raw, _ := json.Marshal(map[string]any{"sessionId": ids[0], "query": "UPDATE users SET name = 'z' WHERE id < 3", "database": "other"})
	out, err := dbSafetyCheckHandler(context.Background(), "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var c sqlsafety.Check
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatal(err)
	}
	plan, ok := takeSafetyPlan(c.CheckID)
	if !ok || len(plan.statements) != 1 || !plan.statements[0].resolved {
		t.Fatalf("plan = %+v (check %+v)", plan, c)
	}
	// Looked up with an empty database, so the backup names the table
	// unqualified, in the driver's own schema like the statement.
	if got := *plan.statements[0].parsed.Table; got != (sqlsafety.TableRef{Schema: "", Name: "USERS"}) {
		t.Fatalf("resolved table = %+v, want USERS in the driver's own schema", got)
	}
	for _, req := range conn.metadataReqs {
		if req.Database == "other" {
			t.Fatalf("looked up in the request's database first: %+v", conn.metadataReqs)
		}
	}
}

// A table the driver's own schema does not hold is not found, even when the
// request's database (the schema selected in the sidebar) holds it: the
// statement would run in the driver's schema.
func TestPluginSafetyMissingInOwnSchemaIsNotFound(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	conn := pluginSafetyConn(externalFeatureSnapshotWrite)
	conn.metadata[ExternalMetadataTables] = `[]`
	conn.metadata[ExternalMetadataColumns] = `[]`
	_, ids := connectFakeSession(t, conn)
	raw, _ := json.Marshal(map[string]any{"sessionId": ids[0], "query": "DELETE FROM users WHERE id <= 1", "database": "other"})
	out, err := dbSafetyCheckHandler(context.Background(), "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var c sqlsafety.Check
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatal(err)
	}
	if st := c.Statements[0]; st.Undoable || !slices.Contains(st.Reasons, sqlsafety.ReasonTableNotFound) {
		t.Fatalf("check = %+v", st)
	}
	for _, req := range conn.metadataReqs {
		if req.Database == "other" {
			t.Fatalf("fell back to the request's database: %+v", conn.metadataReqs)
		}
	}
}

// A driver that cannot answer the empty-database lookup leaves an
// unqualified target unparsed: the request's database (which holds the
// table) never stands in, because the statement may run elsewhere.
func TestPluginSafetyEmptyLookupFailureIsNotUndoable(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	conn := pluginSafetyConn(externalFeatureSnapshotWrite)
	conn.metadataErr = func(req ExternalMetadataRequest) error {
		if req.Database == "" {
			return errors.New("invalid: database is required")
		}
		return nil
	}
	_, ids := connectFakeSession(t, conn)
	const query = "DELETE FROM users WHERE id <= 1"
	c := pluginQueryCheck(t, ids[0], query) // database "main" holds users
	st := c.Statements[0]
	if st.Undoable || c.Undoable || !slices.Contains(st.Reasons, sqlsafety.ReasonUnparsed) || st.Risk != sqlsafety.RiskHigh {
		t.Fatalf("check = %+v", st)
	}
	for _, req := range conn.metadataReqs {
		if req.Database != "" {
			t.Fatalf("fell back to database %q: %+v", req.Database, conn.metadataReqs)
		}
	}
	if _, err := pluginExecute(ids[0], query, c.CheckID, c.ConfirmTarget); err != nil {
		t.Fatal(err)
	}
	if len(conn.table.calls) != 0 {
		t.Fatalf("backed up a table the statement may not touch: %+v", conn.table.calls)
	}
	if recs := listRecords(t, store); len(recs) != 1 || recs[0].Undoable {
		t.Fatalf("records = %+v", recs)
	}
}

// Resolved through the empty-database lookup, the count, the snapshot with
// the statement, and the undo reads and writes all go with that same empty
// database, never the request's: a driver that switches to the request's
// database (dbdriver UseDatabase) would otherwise run them in another schema
// than the one the key and columns came from.
func TestPluginSafetyOwnSchemaRequestsUseEmptyDatabase(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	conn := pluginSafetyConn(externalFeatureSnapshotWrite)
	_, ids := connectFakeSession(t, conn)

	c := pluginQueryCheck(t, ids[0], pluginUpdate) // request database "main"
	if !c.Undoable || c.Statements[0].Estimate.Source != "count" {
		t.Fatalf("check = %+v", c)
	}
	if !strings.Contains(conn.lastExec.Statement, "COUNT(*)") || conn.lastExec.Database != "" {
		t.Fatalf("count sent as %+v, want an empty database", conn.lastExec)
	}
	if _, err := pluginExecute(ids[0], pluginUpdate, c.CheckID, ""); err != nil {
		t.Fatal(err)
	}
	recs := listRecords(t, store)
	if len(recs) != 1 || !recs[0].OwnSchema || recs[0].Database != "main" || recs[0].Table.Schema != "" {
		t.Fatalf("records = %+v", recs)
	}
	if _, err := undo(t, ids[0], recs[0].ID, "skipConflicts", ""); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if len(conn.table.calls) < 3 {
		t.Fatalf("snapshotWrite calls = %+v", conn.table.calls)
	}
	for i, call := range conn.table.calls {
		if call.Database != "" {
			t.Fatalf("snapshotWrite call %d sent with database %q", i, call.Database)
		}
	}
	// The undo's own record keeps the table in the driver's own schema.
	for _, rec := range listRecords(t, store) {
		if rec.Source == "undo" && !rec.OwnSchema {
			t.Fatalf("undo record = %+v", rec)
		}
	}

	// A qualified target still goes with the request's database.
	conn.table.calls = nil
	const qualified = "UPDATE app.users SET name = 'q' WHERE id < 3"
	c = pluginQueryCheck(t, ids[0], qualified)
	if !c.Undoable || conn.lastExec.Database != "main" {
		t.Fatalf("qualified check = %+v, count %+v", c, conn.lastExec)
	}
	if _, err := pluginExecute(ids[0], qualified, c.CheckID, ""); err != nil {
		t.Fatal(err)
	}
	if len(conn.table.calls) != 1 || conn.table.calls[0].Database != "main" {
		t.Fatalf("qualified snapshotWrite calls = %+v", conn.table.calls)
	}
}

// A statement that is not snapshotted (no primary key) runs in the same
// database its rows were counted in: with same-named tables in the driver's
// own schema and the request's database, a driver that switches to the
// request's database (UseDatabase) must not rate the risk from one table
// and run the statement on the other.
func TestPluginUnsnapshottedStatementRunsWhereItWasCounted(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	conn := pluginSafetyConn(externalFeatureSnapshotWrite)
	conn.metadata[ExternalMetadataPrimaryKeys] = `{"columns":[]}` // users exists in both "" and "main"
	_, ids := connectFakeSession(t, conn)

	c := pluginQueryCheck(t, ids[0], pluginUpdate) // request database "main"
	if st := c.Statements[0]; st.Undoable || st.Estimate.Source != "count" {
		t.Fatalf("check = %+v", st)
	}
	count := conn.lastExec
	if _, err := pluginExecute(ids[0], pluginUpdate, c.CheckID, c.ConfirmTarget); err != nil {
		t.Fatal(err)
	}
	run := conn.lastExec
	if !strings.Contains(count.Statement, "COUNT(*)") || run.Statement != pluginUpdate {
		t.Fatalf("count %+v, run %+v", count, run)
	}
	if count.Database != run.Database || run.Database != "" {
		t.Fatalf("counted in %q, ran in %q: want both in the driver's own schema", count.Database, run.Database)
	}
	if len(conn.table.calls) != 0 {
		t.Fatalf("snapshotted: %+v", conn.table.calls)
	}
}

// Query text the SQL classifier does not know (PromQL, nGQL, a search DSL)
// on a driver that enforces read-only executes runs read-only without a
// token; only a refusal makes it a write that needs a check.
func TestPluginUnknownQueryTriesReadOnlyFirst(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	conn := newFakeExternalConn(externalFeatureReadOnly)
	conn.info.Dialect = "generic"
	conn.pages = []ExternalPage{{Columns: cols("value"), Rows: [][]any{{1}}}}
	_, ids := connectFakeSession(t, conn)

	res, err := pluginExecute(ids[0], `rate(http_requests_total[5m])`, "", "")
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("unknown read = %+v, %v", res, err)
	}
	if !conn.lastExec.ReadOnly {
		t.Fatalf("ran as %+v, want readOnly", conn.lastExec)
	}

	conn.readOnlyErr = errors.New("invalid: read-only execution refused a write")
	if _, err := pluginExecute(ids[0], `INSERT VERTEX player(name) VALUES "p1":("a")`, "", ""); err == nil || !strings.Contains(err.Error(), safetyCodeCheckRequired) {
		t.Fatalf("refused read-only = %v, want checkRequired", err)
	}

	// Without the readOnly feature nothing is tried: the check is required.
	plain := newFakeExternalConn()
	plain.info.Dialect = "generic"
	_, plainIDs := connectFakeSession(t, plain)
	if _, err := pluginExecute(plainIDs[0], `rate(http_requests_total[5m])`, "", ""); err == nil || !strings.Contains(err.Error(), safetyCodeCheckRequired) {
		t.Fatalf("no readOnly feature = %v, want checkRequired", err)
	}
	if len(plain.execs) != 0 {
		t.Fatalf("ran without a token: %+v", plain.execs)
	}
}

// The read-only probe is only for drivers without a SQL dialect and text
// that is not SQL: a SQL-dialect driver declaring readOnly, or SQL text the
// classifier did not clear, needs the check and nothing runs.
func TestPluginReadOnlyProbeNeverRunsSQL(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	for _, tc := range []struct{ dialect, query string }{
		{"postgres", `VACUUM t`},
		{"oracle", `rate(http_requests_total[5m])`},
		{"mysql", `SELECT 1 /*! , SLEEP(1) */`},
		{"clickhouse", `OPTIMIZE TABLE t FINAL`},
		{"generic", `VACUUM t`},
		{"generic", `SET search_path = x`},
		{"generic", `(SELECT 1); (DELETE FROM t)`},
	} {
		conn := newFakeExternalConn(externalFeatureReadOnly)
		conn.info.Dialect = tc.dialect
		conn.pages = []ExternalPage{{Columns: cols("value"), Rows: [][]any{{1}}}}
		_, ids := connectFakeSession(t, conn)
		if _, err := pluginExecute(ids[0], tc.query, "", ""); err == nil || !strings.Contains(err.Error(), safetyCodeCheckRequired) {
			t.Fatalf("%s %q = %v, want checkRequired", tc.dialect, tc.query, err)
		}
		if len(conn.execs) != 0 {
			t.Fatalf("%s %q ran without a token: %+v", tc.dialect, tc.query, conn.execs)
		}
	}
}
