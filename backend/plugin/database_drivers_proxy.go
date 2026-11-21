package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/db"
	"mincode/app/plugins/api"
)

// Host proxy for contributes.databaseDrivers: the db.ExternalDriverProvider
// that backend/db (kindPlugin sessions) connects through. It speaks
// plugin.db.* over the existing process runtime; handles and cursors never
// leave this file. Protocol: docs/architecture/database-driver-protocol.md.
//
// Connect runs every gate the catalog uses (point enabled, installed,
// process plugin, enabled, not failed, active workspace, database.driver
// granted, entry declared), then requires the host credential-release grant
// for (plugin, driver, connection) in every approval mode, activates the
// plugin lazily, checks the negotiated databaseDrivers details and pins the
// process generation. It never falls back to a built-in driver or another
// plugin. Decrypted credentials exist only in the connect params and in the
// redactor set of the live session.

const (
	databaseDriverConnectTimeout  = 30 * time.Second
	databaseDriverMetadataTimeout = 120 * time.Second
	databaseDriverFetchTimeout    = 60 * time.Second
	databaseDriverCloseTimeout    = 5 * time.Second
	databaseDriverCancelGrace     = 5 * time.Second
	databaseDriverDeadlineSlack   = 5 * time.Second
	databaseDriverDefaultQuery    = 120 * time.Second
	databaseDriverDefaultDML      = 600 * time.Second
	databaseDriverCallsPerPlugin  = 16
	databaseDriverSessionsMax     = 32
	databaseDriverSecretMinLength = 3
	databaseDriverRedactedMarker  = "[redacted]"
)

// databaseDriverPointEnabled is the point gate; tests override it to
// simulate a contract that does not enable the point.
var databaseDriverPointEnabled = func() bool {
	_, _, ok := enabledContributionPoint(databaseDriverPointKey)
	return ok
}

// ---------------------------------------------------------------------------
// Gates.

type databaseDriverSource struct {
	record     *pluginRecord
	pluginName string
	driver     DatabaseDriverContribution
}

// databaseDriverGateLocked checks every catalog gate. reason is the status
// reason (notInstalled, disabled, error, workspace, capability, noDriver).
// Caller holds record.mu.
func (manager *pluginManager) databaseDriverGateLocked(record *pluginRecord, driverID string) (DatabaseDriverContribution, string, error) {
	pluginID := record.id
	if record.manifest == nil || record.builtin != nil {
		return DatabaseDriverContribution{}, "noDriver", databaseDriverError(databaseDriverErrNotFound, "plugin %s has no database drivers", pluginID)
	}
	if record.manifest.isDeclarative() {
		return DatabaseDriverContribution{}, "noDriver", databaseDriverError(databaseDriverErrForbidden, "declarative plugin %s cannot provide database drivers", pluginID)
	}
	if !record.enabled {
		return DatabaseDriverContribution{}, "disabled", databaseDriverError(databaseDriverErrUnavailable, "plugin %s is disabled", pluginID)
	}
	if record.state == pluginStateError {
		return DatabaseDriverContribution{}, "error", databaseDriverError(databaseDriverErrUnavailable, "plugin %s failed; reload it to recover", pluginID)
	}
	if !record.contributionsListedLocked() {
		return DatabaseDriverContribution{}, "noDriver", databaseDriverError(databaseDriverErrUnavailable, "plugin %s is not available", pluginID)
	}
	if !manager.workspaceActiveLocked(record) {
		return DatabaseDriverContribution{}, "workspace", databaseDriverError(databaseDriverErrUnavailable, "plugin %s is not active in this workspace", pluginID)
	}
	if !slices.Contains(record.capabilities, api.CapabilityDatabaseDriver) {
		return DatabaseDriverContribution{}, "capability", databaseDriverError(databaseDriverErrForbidden, "plugin %s is not granted the database.driver capability", pluginID)
	}
	resolved := record.manifest.contributions[databaseDriverPointKey]
	if resolved != nil {
		for _, entry := range resolved.Entries {
			if driver, ok := entry.(DatabaseDriverContribution); ok && driver.ID == driverID {
				return cloneDatabaseDriverContribution(driver), "", nil
			}
		}
	}
	return DatabaseDriverContribution{}, "noDriver", databaseDriverError(databaseDriverErrNotFound, "plugin %s does not provide database driver %s", pluginID, driverID)
}

func (manager *pluginManager) databaseDriverSourceFor(pluginID, driverID string) (databaseDriverSource, error) {
	source, _, err := manager.databaseDriverSourceReason(pluginID, driverID)
	return source, err
}

func (manager *pluginManager) databaseDriverSourceReason(pluginID, driverID string) (databaseDriverSource, string, error) {
	pluginID = strings.TrimSpace(pluginID)
	driverID = strings.TrimSpace(driverID)
	if pluginID == "" || driverID == "" {
		return databaseDriverSource{}, "notInstalled", databaseDriverError(databaseDriverErrNotFound, "pluginId and driverId are required")
	}
	if !databaseDriverPointEnabled() {
		return databaseDriverSource{}, "noDriver", databaseDriverError(databaseDriverErrUnavailable, "plugin database drivers are not enabled in this host")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return databaseDriverSource{}, "notInstalled", databaseDriverError(databaseDriverErrNotFound, "plugin %s is not installed", pluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	driver, reason, err := manager.databaseDriverGateLocked(record, driverID)
	if err != nil {
		return databaseDriverSource{}, reason, err
	}
	return databaseDriverSource{record: record, pluginName: record.manifest.Name, driver: driver}, "", nil
}

// databaseDriverUnavailableReason is the status reason for a reference, or
// "" when the driver is offered. A running generation that did not
// negotiate the driver reports "protocol".
func (manager *pluginManager) databaseDriverUnavailableReason(pluginID, driverID string) string {
	source, reason, err := manager.databaseDriverSourceReason(pluginID, driverID)
	if err != nil {
		return reason
	}
	record := source.record
	record.mu.Lock()
	runtimeHost := record.runtime
	running := record.state == pluginStateRunning
	record.mu.Unlock()
	if running && runtimeHost != nil {
		raw, _, ok := runtimeHost.featureDetails(databaseDriverPointKey)
		if !ok || parseDatabaseDriverNegotiation(raw, source.driver.ID) != nil {
			return "protocol"
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Per-plugin limits and session registry.

type databaseDriverPluginSessions struct {
	sessions map[*databaseDriverConnection]struct{}
	calls    int
}

var databaseDriverRegistry = struct {
	mu      sync.Mutex
	plugins map[string]*databaseDriverPluginSessions
}{plugins: map[string]*databaseDriverPluginSessions{}}

func databaseDriverPluginLocked(pluginID string) *databaseDriverPluginSessions {
	state := databaseDriverRegistry.plugins[pluginID]
	if state == nil {
		state = &databaseDriverPluginSessions{sessions: map[*databaseDriverConnection]struct{}{}}
		databaseDriverRegistry.plugins[pluginID] = state
	}
	return state
}

func databaseDriverPruneLocked(pluginID string) {
	if state := databaseDriverRegistry.plugins[pluginID]; state != nil && state.calls == 0 && len(state.sessions) == 0 {
		delete(databaseDriverRegistry.plugins, pluginID)
	}
}

// acquireDatabaseDriverCall takes one of the plugin's 16 call slots.
func acquireDatabaseDriverCall(pluginID string) (func(), error) {
	databaseDriverRegistry.mu.Lock()
	defer databaseDriverRegistry.mu.Unlock()
	state := databaseDriverPluginLocked(pluginID)
	if state.calls >= databaseDriverCallsPerPlugin {
		return nil, databaseDriverError(databaseDriverErrBusy, "database driver plugin %s already runs %d calls", pluginID, databaseDriverCallsPerPlugin)
	}
	state.calls++
	var once sync.Once
	return func() {
		once.Do(func() {
			databaseDriverRegistry.mu.Lock()
			defer databaseDriverRegistry.mu.Unlock()
			if state := databaseDriverRegistry.plugins[pluginID]; state != nil {
				state.calls--
				databaseDriverPruneLocked(pluginID)
			}
		})
	}, nil
}

// reserveDatabaseDriverSession registers a connecting session (32 per plugin).
func reserveDatabaseDriverSession(conn *databaseDriverConnection) error {
	databaseDriverRegistry.mu.Lock()
	defer databaseDriverRegistry.mu.Unlock()
	state := databaseDriverPluginLocked(conn.pluginID)
	if len(state.sessions) >= databaseDriverSessionsMax {
		databaseDriverPruneLocked(conn.pluginID)
		return databaseDriverError(databaseDriverErrBusy, "database driver plugin %s already has %d open sessions", conn.pluginID, databaseDriverSessionsMax)
	}
	state.sessions[conn] = struct{}{}
	return nil
}

func releaseDatabaseDriverSession(conn *databaseDriverConnection) {
	databaseDriverRegistry.mu.Lock()
	defer databaseDriverRegistry.mu.Unlock()
	if state := databaseDriverRegistry.plugins[conn.pluginID]; state != nil {
		delete(state.sessions, conn)
		databaseDriverPruneLocked(conn.pluginID)
	}
}

// detachDatabaseDriverSessions removes and returns every session of pluginID.
func detachDatabaseDriverSessions(pluginID string) []*databaseDriverConnection {
	databaseDriverRegistry.mu.Lock()
	defer databaseDriverRegistry.mu.Unlock()
	state := databaseDriverRegistry.plugins[pluginID]
	if state == nil {
		return nil
	}
	conns := make([]*databaseDriverConnection, 0, len(state.sessions))
	for conn := range state.sessions {
		conns = append(conns, conn)
	}
	state.sessions = map[*databaseDriverConnection]struct{}{}
	databaseDriverPruneLocked(pluginID)
	return conns
}

func databaseDriverSessionCount(pluginID string) int {
	databaseDriverRegistry.mu.Lock()
	defer databaseDriverRegistry.mu.Unlock()
	if state := databaseDriverRegistry.plugins[pluginID]; state != nil {
		return len(state.sessions)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Credential redaction: the password/token values handed to a live session.

var databaseDriverSecrets = struct {
	mu     sync.RWMutex
	values map[string]map[*databaseDriverConnection][]string
}{values: map[string]map[*databaseDriverConnection][]string{}}

func registerDatabaseDriverSecrets(conn *databaseDriverConnection, secrets []string) {
	var values []string
	for _, secret := range secrets {
		if len(secret) < databaseDriverSecretMinLength {
			continue
		}
		values = append(values, secret)
		if encoded, err := json.Marshal(secret); err == nil {
			if escaped := string(encoded[1 : len(encoded)-1]); escaped != secret {
				values = append(values, escaped)
			}
		}
	}
	if len(values) == 0 {
		return
	}
	// Longest first so a secret containing another is replaced whole.
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	databaseDriverSecrets.mu.Lock()
	defer databaseDriverSecrets.mu.Unlock()
	byConn := databaseDriverSecrets.values[conn.pluginID]
	if byConn == nil {
		byConn = map[*databaseDriverConnection][]string{}
		databaseDriverSecrets.values[conn.pluginID] = byConn
	}
	byConn[conn] = values
}

func forgetDatabaseDriverSecrets(conn *databaseDriverConnection) {
	databaseDriverSecrets.mu.Lock()
	defer databaseDriverSecrets.mu.Unlock()
	if byConn := databaseDriverSecrets.values[conn.pluginID]; byConn != nil {
		delete(byConn, conn)
		if len(byConn) == 0 {
			delete(databaseDriverSecrets.values, conn.pluginID)
		}
	}
}

// redactDatabaseDriverSecrets is the registered log redactor. It runs under
// record.mu and only takes the secrets read lock.
func redactDatabaseDriverSecrets(pluginID, message string) string {
	databaseDriverSecrets.mu.RLock()
	defer databaseDriverSecrets.mu.RUnlock()
	for _, values := range databaseDriverSecrets.values[pluginID] {
		for _, secret := range values {
			if strings.Contains(message, secret) {
				message = strings.ReplaceAll(message, secret, databaseDriverRedactedMarker)
			}
		}
	}
	return message
}

// ---------------------------------------------------------------------------
// Provider.

type databaseDriverProvider struct {
	manager func() *pluginManager
}

func (provider databaseDriverProvider) Connect(ctx context.Context, req db.ExternalConnectRequest) (db.ExternalConnection, error) {
	return provider.manager().connectDatabaseDriver(ctx, req)
}

func (databaseDriverProvider) ReleaseAllowed(ref db.ExternalDriverRef, connectionID string) bool {
	allowed, err := databaseDriverReleaseAllowed(strings.TrimSpace(ref.PluginID), strings.TrimSpace(ref.DriverID), connectionID)
	return err == nil && allowed
}

// buildDatabaseDriverConfig sends only the connection fields the driver
// declares (plus ssl), with options validated against the declared schema
// and defaults applied. It returns the secret values for redaction.
func buildDatabaseDriverConfig(driver DatabaseDriverContribution, config db.DbConnectionConfig, options map[string]any) (map[string]any, []string, error) {
	declared := map[string]bool{}
	for _, field := range driver.Connection.Fields {
		declared[field] = true
	}
	out := map[string]any{"ssl": config.SSL}
	var secrets []string
	setText := func(field string, value *string) {
		if declared[field] && value != nil && *value != "" {
			out[field] = *value
			if databaseDriverFieldSecret(field) {
				secrets = append(secrets, *value)
			}
		}
	}
	if declared["host"] && strings.TrimSpace(config.Host) != "" {
		out["host"] = config.Host
	}
	if declared["port"] && config.Port > 0 {
		if config.Port > 65535 {
			return nil, nil, databaseDriverError(databaseDriverErrInvalidRequest, "port must be between 1 and 65535")
		}
		out["port"] = config.Port
	}
	setText("database", config.Database)
	setText("username", config.Username)
	setText("password", config.Password)
	setText("token", config.Token)
	setText("filePath", config.FilePath)
	setText("org", config.Org)
	setText("authSource", config.AuthSource)
	for _, field := range driver.Connection.Required {
		if field == "ssl" {
			continue
		}
		if _, ok := out[field]; !ok {
			return nil, nil, databaseDriverError(databaseDriverErrInvalidRequest, "connection field %s is required by driver %s", field, driver.ID)
		}
	}
	validated := map[string]any{}
	for key, value := range options {
		option, ok := driver.Connection.Options[key]
		if !ok {
			return nil, nil, databaseDriverError(databaseDriverErrInvalidRequest, "driver %s has no option %s", driver.ID, key)
		}
		if value == nil {
			continue
		}
		normalized, err := validateDatabaseDriverOptionValue(option, value, true)
		if err != nil {
			return nil, nil, databaseDriverError(databaseDriverErrInvalidRequest, "option %s %v", key, err)
		}
		validated[key] = normalized
	}
	for key, option := range driver.Connection.Options {
		if _, ok := validated[key]; !ok && option.Default != nil {
			validated[key] = option.Default
		}
	}
	out["options"] = validated
	return out, secrets, nil
}

func (manager *pluginManager) connectDatabaseDriver(ctx context.Context, req db.ExternalConnectRequest) (db.ExternalConnection, error) {
	pluginID := strings.TrimSpace(req.Ref.PluginID)
	driverID := strings.TrimSpace(req.Ref.DriverID)
	source, err := manager.databaseDriverSourceFor(pluginID, driverID)
	if err != nil {
		return nil, err
	}
	if err := validateDatabaseDriverConnectionID(req.ConnectionID); err != nil {
		return nil, err
	}
	// An explicit per-connection grant, or the user's explicit trust in the plugin.
	allowed, err := databaseDriverReleaseAllowed(pluginID, driverID, req.ConnectionID)
	if err != nil {
		return nil, databaseDriverError(databaseDriverErrUnavailable, "credential-release grants cannot be read")
	}
	if !allowed {
		return nil, databaseDriverError(databaseDriverErrUntrusted, "connection %s is not bound to plugin %s driver %s", req.ConnectionID, pluginID, driverID)
	}
	options := req.Options
	if options == nil {
		options = req.Config.Options
	}
	config, secrets, err := buildDatabaseDriverConfig(source.driver, req.Config, options)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, databaseDriverConnectTimeout)
	defer cancel()
	record := source.record
	validate := func(record *pluginRecord) error {
		_, _, err := manager.databaseDriverGateLocked(record, driverID)
		return err
	}
	if err := manager.startRecordValidated(ctx, record, validate); err != nil {
		if hasDatabaseDriverCode(err.Error()) {
			return nil, err
		}
		return nil, databaseDriverError(databaseDriverErrUnavailable, "database driver plugin %s could not start: %s", pluginID, sanitizeDatabaseDriverText(pluginID, err.Error(), databaseDriverErrorMaxRunes))
	}
	record.mu.Lock()
	driver, _, gateErr := manager.databaseDriverGateLocked(record, driverID)
	runtimeHost := record.runtime
	running := record.state == pluginStateRunning
	record.mu.Unlock()
	if gateErr != nil {
		return nil, gateErr
	}
	if runtimeHost == nil || !running {
		return nil, databaseDriverError(databaseDriverErrUnavailable, "database driver plugin %s is not running", pluginID)
	}
	details, generation, ok := runtimeHost.featureDetails(databaseDriverPointKey)
	if !ok {
		return nil, databaseDriverError(databaseDriverErrUnavailable, "plugin does not implement database driver %s", driverID)
	}
	if err := parseDatabaseDriverNegotiation(details, driverID); err != nil {
		return nil, err
	}
	encoding := databaseDriverRequestedEncoding(details, databaseDriverColumnarEnabled())

	conn := &databaseDriverConnection{
		pluginID:   pluginID,
		driverID:   driverID,
		runtime:    runtimeHost,
		generation: generation,
		limits:     negotiatedDatabaseDriverPageLimits(details),
	}
	if err := reserveDatabaseDriverSession(conn); err != nil {
		return nil, err
	}
	registerDatabaseDriverSecrets(conn, secrets)
	release, err := acquireDatabaseDriverCall(pluginID)
	if err != nil {
		conn.discard()
		return nil, err
	}
	raw, err := conn.call(ctx, databaseDriverMethodConnect, databaseDriverConnectParams{
		DriverID: driverID,
		Config:   config,
		Limits: databaseDriverLimits{
			PageMaxBytes: int(conn.limits.bytes),
			PageMaxRows:  conn.limits.rows,
			CursorIdleMs: databaseDriverCursorIdleMs,
		},
		TimeoutMs: databaseDriverConnectTimeout.Milliseconds(),
		Encoding:  encoding,
	}, databaseDriverConnectTimeout)
	release()
	// The params held the only other copy of the credentials.
	config = nil
	if err != nil {
		conn.discard()
		return nil, err
	}
	result, err := decodeDatabaseDriverConnectResult(raw)
	if err != nil {
		conn.discard()
		return nil, err
	}
	conn.handle = result.Handle
	if conn.columnar, err = databaseDriverAcceptedEncoding(encoding, result.Info.Encoding); err != nil {
		go conn.Close(context.Background())
		return nil, err
	}
	conn.info = db.ExternalConnectionInfo{
		BackupVersion:   result.Info.BackupVersion,
		PluginID:        pluginID,
		DriverID:        driverID,
		Dialect:         driver.SQLDialect,
		Features:        append([]string(nil), driver.Features...),
		DefaultDatabase: result.Info.DefaultDatabase,
		ServerVersion:   sanitizeDatabaseDriverText(pluginID, result.Info.ServerVersion, databaseDriverServerVersionMax),
		ReadOnly:        result.Info.ReadOnly,
	}
	// A lifecycle transition may have raced the connect: the hook detached
	// every registered session, so a detached connection is already dead.
	if conn.closed.Load() || runtimeHost.Generation() != generation {
		go conn.Close(context.Background())
		return nil, databaseDriverError(databaseDriverErrUnavailable, "database driver plugin %s stopped while connecting", pluginID)
	}
	return conn, nil
}

func hasDatabaseDriverCode(message string) bool {
	for _, code := range []string{databaseDriverErrNotFound, databaseDriverErrUnavailable, databaseDriverErrForbidden, databaseDriverErrUntrusted, databaseDriverErrInvalidRequest} {
		if strings.HasPrefix(message, code+":") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Connection.

type databaseDriverConnection struct {
	pluginID   string
	driverID   string
	runtime    *processRuntime
	generation uint64
	handle     string
	info       db.ExternalConnectionInfo
	// limits is the page budget negotiated for the generation (protocol 1.0
	// or 1.1); every execute/fetch/tableRows asks for at most
	// limits.requestRows() rows and limits.bytes bytes.
	limits databaseDriverPageLimits
	// columnar is set when connect negotiated the columnar page encoding
	// (protocol v2 draft); pages may then carry column blocks.
	columnar bool

	closed    atomic.Bool
	closeOnce sync.Once
}

var _ db.ExternalConnection = (*databaseDriverConnection)(nil)

func (conn *databaseDriverConnection) Info() db.ExternalConnectionInfo {
	info := conn.info
	info.Features = append([]string(nil), conn.info.Features...)
	return info
}

// call sends one plugin.db.* request pinned to the connect generation,
// bounded by timeout. A host deadline becomes "timeout:"; a cancelled or
// expired parent ctx passes through for backend/db to classify.
func (conn *databaseDriverConnection) call(parent context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	if conn.closed.Load() && method != databaseDriverMethodClose && method != databaseDriverMethodConnect {
		return nil, databaseDriverError(databaseDriverErrUnavailable, "database driver session is closed")
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, databaseDriverError(databaseDriverErrInvalidRequest, "%s params cannot be encoded", method)
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var raw json.RawMessage
	if conn.limits.large() && databaseDriverPageMethod(method) {
		raw, err = conn.runtime.callGenerationLarge(ctx, conn.generation, method, encoded)
	} else {
		raw, err = conn.runtime.callGeneration(ctx, conn.generation, method, encoded)
	}
	if err != nil {
		if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, databaseDriverError(databaseDriverErrTimeout, "%s did not finish within %d seconds", method, int(timeout/time.Second))
		}
		return nil, mapDatabaseDriverCallError(conn.pluginID, method, err)
	}
	return raw, nil
}

// databaseDriverPageMethod reports the methods that answer result pages,
// the only replies allowed above the 1 MiB line cap (protocol 1.1).
func databaseDriverPageMethod(method string) bool {
	switch method {
	case databaseDriverMethodExecute, databaseDriverMethodFetch, databaseDriverMethodTableRows:
		return true
	}
	return false
}

// closeCursorAsync releases a plugin cursor after truncation, cancellation
// or an error; best effort and never blocking the caller.
func (conn *databaseDriverConnection) closeCursorAsync(cursor string) {
	if cursor == "" || conn.closed.Load() {
		return
	}
	go func() {
		_, _ = conn.call(context.Background(), databaseDriverMethodCloseCursor,
			databaseDriverCursorParams{Handle: conn.handle, Cursor: cursor}, databaseDriverCloseTimeout)
	}()
}

// pageBudget is the budget of a first page: the host's per-page row request
// and the negotiated page bytes, lowered to the caller's total caps.
func (conn *databaseDriverConnection) pageBudget(requestedRows int, requestedBytes int64) (int, int64) {
	rows, bytes := conn.limits.requestRows(), conn.limits.bytes
	if requestedRows > 0 && requestedRows < rows {
		rows = requestedRows
	}
	if requestedBytes > 0 && requestedBytes < bytes {
		bytes = requestedBytes
	}
	return rows, bytes
}

// drain feeds the first page (header-validated by the caller) and every
// fetched page to page. When page, a fetch or a decode fails, the open
// cursor is closed and the error returned unchanged.
//
// Continuation pages are fetched ahead (prefetchDatabaseDriverPages): the
// plugin produces page N+1 and the host decodes it while page N is being
// validated, so plugin work, transport and host decoding overlap instead of
// adding up. Only one plugin.db.fetch per cursor is ever in flight, and at
// most databaseDriverPrefetchDepth decoded pages wait ahead of the consumer.
func (conn *databaseDriverConnection) drain(ctx context.Context, method string, first *databaseDriverPageResult, firstMaxRows int, page func(db.ExternalPage) error) (*db.ExternalExecuteSummary, error) {
	summary := &db.ExternalExecuteSummary{
		Columns:       first.Columns,
		AffectedRows:  first.AffectedRows,
		NextPageToken: first.NextPageToken,
	}
	var prefetch *databaseDriverPrefetch
	if !first.Done {
		prefetch = conn.prefetchDatabaseDriverPages(ctx, first.Cursor)
	}
	// The first page arrives header-validated only, so the fetch-ahead is
	// already running while its rows are decoded.
	if err := first.decodeRows(method, firstMaxRows); err != nil {
		if prefetch != nil {
			conn.closeCursorAsync(prefetch.stop())
		}
		return nil, err
	}
	current := first
	for {
		notice := sanitizeDatabaseDriverText(conn.pluginID, current.Notice, databaseDriverNoticeMaxRunes)
		if err := page(db.ExternalPage{Columns: current.Columns, Rows: current.decodedRows, Notice: notice}); err != nil {
			if prefetch != nil {
				conn.closeCursorAsync(prefetch.stop())
			}
			return nil, err
		}
		summary.RowCount += len(current.decodedRows)
		if current.Done {
			if prefetch != nil {
				prefetch.cancel()
			}
			return summary, nil
		}
		next, ok := <-prefetch.pages
		if !ok {
			// Only a cancelled caller context closes pages without a result.
			next.err = ctx.Err()
			if next.err == nil {
				next.err = context.Canceled
			}
		}
		if next.err != nil {
			conn.closeCursorAsync(prefetch.stop())
			return nil, next.err
		}
		current = next.page
	}
}

// databaseDriverPrefetchDepth bounds the decoded pages buffered ahead of
// the consumer (each at most the negotiated page bytes of JSON).
const databaseDriverPrefetchDepth = 1

type databaseDriverPrefetched struct {
	page *databaseDriverPageResult
	err  error
}

type databaseDriverPrefetch struct {
	pages  chan databaseDriverPrefetched
	cancel context.CancelFunc
	// open receives, once the fetcher exits, the cursor that is still open
	// on the plugin side ("" after a done page).
	open chan string
}

// stop cancels the fetch-ahead, waits for both stages to exit and returns
// the cursor the caller must close.
func (prefetch *databaseDriverPrefetch) stop() string {
	prefetch.cancel()
	for range prefetch.pages {
	}
	return <-prefetch.open
}

// prefetchDatabaseDriverPages runs two stages: a fetcher that sends
// plugin.db.fetch and validates the page header (so the next fetch can go
// out at once), and a decoder that decodes the rows. Both stop on ctx
// cancellation, an error or the done page; pages is closed afterwards.
func (conn *databaseDriverConnection) prefetchDatabaseDriverPages(parent context.Context, cursor string) *databaseDriverPrefetch {
	ctx, cancel := context.WithCancel(parent)
	prefetch := &databaseDriverPrefetch{
		pages:  make(chan databaseDriverPrefetched, databaseDriverPrefetchDepth),
		cancel: cancel,
		open:   make(chan string, 1),
	}
	headers := make(chan databaseDriverPrefetched, 1)
	go func() {
		defer close(headers)
		open := cursor
		defer func() { prefetch.open <- open }()
		for {
			raw, err := conn.call(ctx, databaseDriverMethodFetch, databaseDriverFetchParams{
				Handle:   conn.handle,
				Cursor:   open,
				MaxRows:  conn.limits.requestRows(),
				MaxBytes: conn.limits.bytes,
			}, databaseDriverFetchTimeout)
			var next *databaseDriverPageResult
			if err == nil {
				next, err = decodeDatabaseDriverPageHeaderEncoded(databaseDriverMethodFetch, raw, conn.limits.bytes, false, conn.columnar)
			}
			if err == nil && next.Done {
				open = ""
			}
			select {
			case headers <- databaseDriverPrefetched{page: next, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil || next.Done {
				return
			}
			open = next.Cursor
		}
	}()
	go func() {
		defer close(prefetch.pages)
		for header := range headers {
			if header.err == nil {
				header.err = header.page.decodeRows(databaseDriverMethodFetch, conn.limits.requestRows())
			}
			select {
			case prefetch.pages <- header:
			case <-ctx.Done():
				header.err = ctx.Err()
			}
			if header.err != nil {
				// Stop the fetcher and wait for it so open is reported.
				cancel()
				for range headers {
				}
				return
			}
		}
	}()
	return prefetch
}

func (conn *databaseDriverConnection) Execute(ctx context.Context, req db.ExternalExecute, page func(db.ExternalPage) error) (*db.ExternalExecuteSummary, error) {
	release, err := acquireDatabaseDriverCall(conn.pluginID)
	if err != nil {
		return nil, err
	}
	defer release()
	if !validDatabaseDriverOpaqueID(req.RequestID) {
		return nil, databaseDriverError(databaseDriverErrInvalidRequest, "requestId is invalid")
	}
	mode := req.Mode
	switch mode {
	case "":
		mode = db.ExternalExecuteAuto
	case db.ExternalExecuteAuto, db.ExternalExecuteQuery, db.ExternalExecuteExec:
	default:
		return nil, databaseDriverError(databaseDriverErrInvalidRequest, "unknown execute mode %s", mode)
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = databaseDriverDefaultQuery
	}
	maxRows, maxBytes := conn.pageBudget(req.MaxRows, req.MaxBytes)
	raw, err := conn.call(ctx, databaseDriverMethodExecute, databaseDriverExecuteParams{
		Handle:    conn.handle,
		RequestID: req.RequestID,
		Statement: req.Statement,
		Database:  req.Database,
		Mode:      string(mode),
		ReadOnly:  req.ReadOnly,
		MaxRows:   maxRows,
		MaxBytes:  maxBytes,
		TimeoutMs: timeout.Milliseconds(),
	}, timeout+databaseDriverDeadlineSlack)
	if err != nil {
		return nil, err
	}
	first, err := decodeDatabaseDriverPageHeaderEncoded(databaseDriverMethodExecute, raw, maxBytes, true, conn.columnar)
	if err != nil {
		return nil, err
	}
	return conn.drain(ctx, databaseDriverMethodExecute, first, maxRows, page)
}

func (conn *databaseDriverConnection) TableRows(ctx context.Context, req db.ExternalTableRows, page func(db.ExternalPage) error) (*db.ExternalExecuteSummary, error) {
	release, err := acquireDatabaseDriverCall(conn.pluginID)
	if err != nil {
		return nil, err
	}
	defer release()
	if !validDatabaseDriverOpaqueID(req.RequestID) {
		return nil, databaseDriverError(databaseDriverErrInvalidRequest, "requestId is invalid")
	}
	if req.Limit < 0 || req.Offset < 0 {
		return nil, databaseDriverError(databaseDriverErrInvalidRequest, "limit and offset must not be negative")
	}
	maxRows, maxBytes := conn.pageBudget(req.MaxRows, req.MaxBytes)
	raw, err := conn.call(ctx, databaseDriverMethodTableRows, databaseDriverTableRowsParams{
		Handle:    conn.handle,
		RequestID: req.RequestID,
		Database:  req.Database,
		Table:     req.Table,
		Limit:     req.Limit,
		Offset:    req.Offset,
		PageToken: req.PageToken,
		ReadOnly:  true,
		MaxRows:   maxRows,
		MaxBytes:  maxBytes,
	}, databaseDriverMetadataTimeout)
	if err != nil {
		return nil, err
	}
	first, err := decodeDatabaseDriverPageHeaderEncoded(databaseDriverMethodTableRows, raw, maxBytes, true, conn.columnar)
	if err != nil {
		return nil, err
	}
	return conn.drain(ctx, databaseDriverMethodTableRows, first, maxRows, page)
}

// Metadata returns {value} results as-is and joins paged {items, cursor}
// results into one JSON array, within 8 MiB per request chain.
func (conn *databaseDriverConnection) Metadata(ctx context.Context, req db.ExternalMetadataRequest) (json.RawMessage, error) {
	release, err := acquireDatabaseDriverCall(conn.pluginID)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, databaseDriverMetadataTimeout)
	defer cancel()
	var items []json.RawMessage
	total := 2 // "[" + "]"
	cursor := ""
	for {
		raw, err := conn.call(ctx, databaseDriverMethodMetadata, databaseDriverMetadataParams{
			Handle:   conn.handle,
			Kind:     string(req.Kind),
			Database: req.Database,
			Table:    req.Table,
			Cursor:   cursor,
			MaxBytes: int64(databaseDriverMetadataMaxBytes - total + 2),
		}, databaseDriverMetadataTimeout)
		if err != nil {
			if cursor != "" && !errors.Is(err, context.Canceled) {
				conn.closeCursorAsync(cursor)
			}
			return nil, err
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil {
			return nil, databaseDriverProtocolError("invalid plugin.db.metadata result")
		}
		if value, ok := members["value"]; ok && cursor == "" {
			if len(value) > databaseDriverMetadataMaxBytes {
				return nil, databaseDriverError(databaseDriverErrTooLarge, "metadata exceeds %d bytes", databaseDriverMetadataMaxBytes)
			}
			return append(json.RawMessage(nil), value...), nil
		}
		var result databaseDriverMetadataResult
		if err := json.Unmarshal(raw, &result); err != nil || result.Items == nil {
			if cursor != "" {
				conn.closeCursorAsync(cursor)
			}
			return nil, databaseDriverProtocolError("plugin.db.metadata must return value or items")
		}
		for _, item := range result.Items {
			total += len(item) + 1
		}
		items = append(items, result.Items...)
		if total > databaseDriverMetadataMaxBytes {
			conn.closeCursorAsync(result.Cursor)
			return nil, databaseDriverError(databaseDriverErrTooLarge, "metadata exceeds %d bytes", databaseDriverMetadataMaxBytes)
		}
		if result.Cursor == "" {
			break
		}
		if !validDatabaseDriverOpaqueID(result.Cursor) || len(result.Items) == 0 {
			conn.closeCursorAsync(result.Cursor)
			return nil, databaseDriverProtocolError("plugin.db.metadata returned an invalid continuation")
		}
		cursor = result.Cursor
	}
	joined := make([]byte, 0, total)
	joined = append(joined, '[')
	for index, item := range items {
		if index > 0 {
			joined = append(joined, ',')
		}
		joined = append(joined, item...)
	}
	return append(joined, ']'), nil
}

func (conn *databaseDriverConnection) Mutate(ctx context.Context, req db.ExternalMutation) (int64, error) {
	release, err := acquireDatabaseDriverCall(conn.pluginID)
	if err != nil {
		return 0, err
	}
	defer release()
	if !validDatabaseDriverOpaqueID(req.RequestID) {
		return 0, databaseDriverError(databaseDriverErrInvalidRequest, "requestId is invalid")
	}
	switch req.Op {
	case db.ExternalMutationUpdateCell, db.ExternalMutationInsertRow, db.ExternalMutationDeleteRow:
	default:
		return 0, databaseDriverError(databaseDriverErrInvalidRequest, "unknown mutation %s", req.Op)
	}
	raw, err := conn.call(ctx, databaseDriverMethodMutate, databaseDriverMutateParams{
		Handle:     conn.handle,
		RequestID:  req.RequestID,
		Op:         string(req.Op),
		Database:   req.Database,
		Table:      req.Table,
		PrimaryKey: req.PrimaryKey,
		Column:     req.Column,
		Value:      req.Value,
		Fields:     req.Fields,
	}, databaseDriverDefaultDML+databaseDriverDeadlineSlack)
	if err != nil {
		return 0, err
	}
	var result struct {
		AffectedRows *int64 `json:"affectedRows"`
	}
	if json.Unmarshal(raw, &result) != nil || result.AffectedRows == nil {
		return 0, databaseDriverProtocolError("invalid plugin.db.mutate result")
	}
	return *result.AffectedRows, nil
}

func (conn *databaseDriverConnection) Destructive(ctx context.Context, action, database, table string) error {
	release, err := acquireDatabaseDriverCall(conn.pluginID)
	if err != nil {
		return err
	}
	defer release()
	switch action {
	case "dropTable", "clearTable":
		if table == "" {
			return databaseDriverError(databaseDriverErrInvalidRequest, "destructive action %s needs a table", action)
		}
	case "dropDatabase", "clearDatabase":
		if table != "" || database == "" {
			return databaseDriverError(databaseDriverErrInvalidRequest, "destructive action %s takes a database and no table", action)
		}
	default:
		return databaseDriverError(databaseDriverErrInvalidRequest, "unknown destructive action %s", action)
	}
	requestID := fmt.Sprintf("d%d", time.Now().UnixNano())
	raw, err := conn.call(ctx, databaseDriverMethodDestructive, databaseDriverDestructiveParams{
		Handle:    conn.handle,
		RequestID: requestID,
		Action:    action,
		Database:  database,
		Table:     table,
	}, databaseDriverDefaultDML+databaseDriverDeadlineSlack)
	if err != nil {
		return err
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.OK {
		return databaseDriverProtocolError("invalid plugin.db.destructive result")
	}
	return nil
}

var _ db.ExternalSnapshotConnection = (*databaseDriverConnection)(nil)

// SnapshotWrite sends plugin.db.snapshotWrite (the write timeout, like
// mutate). The reply is only decoded here; backend/db validates its shape
// and typed cells.
func (conn *databaseDriverConnection) SnapshotWrite(ctx context.Context, req db.ExternalSnapshotRequest) (*db.ExternalSnapshotResponse, error) {
	release, err := acquireDatabaseDriverCall(conn.pluginID)
	if err != nil {
		return nil, err
	}
	defer release()
	if !validDatabaseDriverOpaqueID(req.RequestID) {
		return nil, databaseDriverError(databaseDriverErrInvalidRequest, "requestId is invalid")
	}
	if len(req.Steps) == 0 {
		return nil, databaseDriverError(databaseDriverErrInvalidRequest, "snapshotWrite needs at least one step")
	}
	steps := make([]db.ExternalSnapshotStep, len(req.Steps))
	for i, step := range req.Steps {
		for _, st := range []*db.ExternalSnapshotStatement{step.Snapshot, step.Write, step.After} {
			if st != nil && st.Args == nil {
				st.Args = []any{}
			}
		}
		steps[i] = step
	}
	raw, err := conn.call(ctx, databaseDriverMethodSnapshot, databaseDriverSnapshotParams{
		Handle:    conn.handle,
		RequestID: req.RequestID,
		Database:  req.Database,
		Steps:     steps,
	}, databaseDriverDefaultDML+databaseDriverDeadlineSlack)
	if err != nil {
		if strings.HasPrefix(err.Error(), "committed:") {
			// The driver answered success but its result could not be sent:
			// recorded as applied without a backup, never resubmitted.
			return nil, fmt.Errorf("%w (plugin.db.snapshotWrite)", db.ErrExternalSnapshotReply)
		}
		return nil, err
	}
	var result db.ExternalSnapshotResponse
	if json.Unmarshal(raw, &result) != nil {
		// The call succeeded, so the driver committed: say so (backend/db
		// records the change without undo) rather than a bare protocol error.
		return nil, fmt.Errorf("%w (plugin.db.snapshotWrite)", db.ErrExternalSnapshotReply)
	}
	// Step counts, exceededStep and row shapes are validated by backend/db
	// (decodeExternalSnapshotResponse), which knows whether the write committed.
	return &result, nil
}

// Cancel sends plugin.db.cancel with the 5 s grace; an unanswered cancel
// reports CancelRequested (outcome unknown). It needs no call slot.
func (conn *databaseDriverConnection) Cancel(ctx context.Context, requestID string) (db.CancelState, error) {
	if !validDatabaseDriverOpaqueID(requestID) {
		return db.CancelNotFound, databaseDriverError(databaseDriverErrInvalidRequest, "requestId is invalid")
	}
	raw, err := conn.call(ctx, databaseDriverMethodCancel, databaseDriverCancelParams{Handle: conn.handle, RequestID: requestID}, databaseDriverCancelGrace)
	if err != nil {
		return db.CancelRequested, err
	}
	state, err := decodeDatabaseDriverCancelResult(raw)
	if err != nil {
		return db.CancelRequested, err
	}
	return state, nil
}

// Close sends plugin.db.close once (5 s, best effort) and forgets the
// session and its secrets.
func (conn *databaseDriverConnection) Close(ctx context.Context) error {
	var err error
	conn.closeOnce.Do(func() {
		wasClosed := conn.closed.Swap(true)
		if !wasClosed && conn.handle != "" {
			_, err = conn.call(ctx, databaseDriverMethodClose, databaseDriverHandleParams{Handle: conn.handle}, databaseDriverCloseTimeout)
		}
		conn.discard()
	})
	return err
}

// discard drops the session without any plugin call.
func (conn *databaseDriverConnection) discard() {
	conn.closed.Store(true)
	releaseDatabaseDriverSession(conn)
	forgetDatabaseDriverSecrets(conn)
}

// ---------------------------------------------------------------------------
// Lifecycle.

// closeDatabaseDriverSessions ends every session of pluginID: stop, crash,
// disable, reload, uninstall and development generation changes. Sessions
// are marked unavailable (no fallback) and backend/db emits
// db:sessions-closed.
func closeDatabaseDriverSessions(pluginID, reason string) {
	conns := detachDatabaseDriverSessions(pluginID)
	for _, conn := range conns {
		conn.closed.Store(true)
		forgetDatabaseDriverSecrets(conn)
	}
	ids := db.CloseExternalSessions(pluginID, reason)
	if len(conns) > 0 || len(ids) > 0 {
		slog.Info("database driver sessions closed", "plugin", pluginID, "reason", reason, "sessions", len(ids))
	}
}

func databaseDriverLifecycleReason(record *pluginRecord) string {
	record.mu.Lock()
	defer record.mu.Unlock()
	switch {
	case !record.enabled:
		return "disabled"
	case record.state == pluginStateError:
		return "crashed"
	}
	return "stopped"
}

// processID returns the running process id, or 0.
func (runtimeHost *processRuntime) processID() int {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	if runtimeHost.cmd == nil || runtimeHost.cmd.Process == nil {
		return 0
	}
	return runtimeHost.cmd.Process.Pid
}

// DatabaseDriverProcessID returns the pid of a running process plugin for
// benchmark RSS sampling; it never starts the plugin.
func DatabaseDriverProcessID(pluginID string) (int, bool) {
	return host().databaseDriverProcessID(pluginID)
}

func (manager *pluginManager) databaseDriverProcessID(pluginID string) (int, bool) {
	record, err := manager.record(strings.TrimSpace(pluginID))
	if err != nil {
		return 0, false
	}
	record.mu.Lock()
	runtimeHost := record.runtime
	running := record.state == pluginStateRunning
	record.mu.Unlock()
	if runtimeHost == nil || !running {
		return 0, false
	}
	pid := runtimeHost.processID()
	return pid, pid > 0
}

// ---------------------------------------------------------------------------
// Catalog command.

type databaseDriverListItem struct {
	PluginID      string `json:"pluginId"`
	PluginName    string `json:"pluginName"`
	PluginVersion string `json:"pluginVersion"`
	DatabaseDriverContribution
}

// databaseDrivers lists every offered driver (same gates as connect).
func (manager *pluginManager) databaseDrivers() []databaseDriverListItem {
	items := []databaseDriverListItem{}
	if !databaseDriverPointEnabled() {
		return items
	}
	manager.mu.Lock()
	records := manager.orderedRecordsLocked()
	manager.mu.Unlock()
	for _, record := range records {
		record.mu.Lock()
		if record.manifest != nil {
			if resolved := record.manifest.contributions[databaseDriverPointKey]; resolved != nil {
				for _, entry := range resolved.Entries {
					entryDriver, ok := entry.(DatabaseDriverContribution)
					if !ok {
						continue
					}
					if driver, _, err := manager.databaseDriverGateLocked(record, entryDriver.ID); err == nil {
						items = append(items, databaseDriverListItem{
							PluginID:                   record.id,
							PluginName:                 record.manifest.Name,
							PluginVersion:              record.manifest.Version,
							DatabaseDriverContribution: driver,
						})
					}
				}
			}
		}
		record.mu.Unlock()
	}
	return items
}

func pluginHostDatabaseDriversListHandler(_, _ string) (string, error) {
	return core.EncodeResult(map[string]any{"drivers": host().databaseDrivers()})
}

func init() {
	db.RegisterExternalDriverProvider(databaseDriverProvider{manager: host})
	registerLogRedactor(redactDatabaseDriverSecrets)
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		closeDatabaseDriverSessions(record.id, databaseDriverLifecycleReason(record))
	})
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, reason removalReason) {
		if reason == removalReasonUninstall {
			closeDatabaseDriverSessions(pluginID, "uninstalled")
		}
	})
	core.Register("plugin_host_database_drivers_list", pluginHostDatabaseDriversListHandler)
}

func (conn *databaseDriverConnection) Backup(ctx context.Context, requestID, database, table string, chunk func([]byte) error) error {
	if conn.info.BackupVersion != 1 {
		return databaseDriverError(databaseDriverErrUnavailable, "native backup stream was not negotiated")
	}
	release, err := acquireDatabaseDriverCall(conn.pluginID)
	if err != nil {
		return err
	}
	defer release()
	if !validDatabaseDriverOpaqueID(requestID) {
		return databaseDriverError(databaseDriverErrInvalidRequest, "invalid backup request ID")
	}
	maxRows, maxBytes := conn.pageBudget(8, 512<<10)
	raw, err := conn.call(ctx, databaseDriverMethodBackup, map[string]any{"handle": conn.handle, "requestId": requestID, "database": database, "table": table, "maxRows": maxRows, "maxBytes": maxBytes}, databaseDriverMetadataTimeout)
	if err != nil {
		return err
	}
	first, err := decodeDatabaseDriverPageHeader(databaseDriverMethodBackup, raw, maxBytes, true)
	if err != nil {
		return err
	}
	_, err = conn.drain(ctx, databaseDriverMethodBackup, first, maxRows, func(page db.ExternalPage) error {
		if page.Notice != "" {
			return databaseDriverProtocolError("native backup returned a truncation notice")
		}
		for _, row := range page.Rows {
			if len(row) != 1 {
				return databaseDriverProtocolError("invalid native backup chunk")
			}
			encoded, ok := row[0].(string)
			if !ok {
				return databaseDriverProtocolError("invalid native backup chunk encoding")
			}
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(data) > 16*1024 {
				return databaseDriverProtocolError("invalid native backup chunk size or base64")
			}
			if err = chunk(data); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}
