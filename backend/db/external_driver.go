package db

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// External (plugin-provided) database drivers — Phase E seam.
//
// The plugin package implements ExternalDriverProvider and registers it from
// init(); the import direction is plugin → db, so this package never imports
// the plugin host. Wire protocol: docs/architecture/database-driver-protocol.md.
//
// This file is the contract (T0). The consumer lives in external_session.go
// (kindPlugin sessions, db_connect routing, cancellation, CloseExternalSessions)
// and external_result.go (validation of untrusted plugin output). buildSession
// keeps rejecting every config with Driver set (errExternalDriverUnavailable),
// so a plugin-bound connection can never silently fall back to a built-in
// driver; db_connect, backups and schema access route
// such configs to connectExternalSession, which enforces the credential grant.

// ExternalDriverRef names one driver contributed by one plugin.
type ExternalDriverRef struct {
	PluginID string `json:"pluginId"`
	DriverID string `json:"driverId"`
}

// ExternalConnectRequest is what the host hands a provider on connect.
// Config is already decrypted: it must never be logged, persisted or sent
// anywhere except plugin.db.connect of the referenced plugin.
type ExternalConnectRequest struct {
	Ref          ExternalDriverRef
	ConnectionID string
	Config       DbConnectionConfig
	Options      map[string]any
}

// ExternalDriverProvider resolves a driver reference to a live connection.
type ExternalDriverProvider interface {
	Connect(ctx context.Context, req ExternalConnectRequest) (ExternalConnection, error)
	// ReleaseAllowed reports whether the user already allowed connectionID's
	// credentials to cross to ref (per-connection grant or plugin trust). It
	// never prompts; Connect enforces the same rule.
	ReleaseAllowed(ref ExternalDriverRef, connectionID string) bool
}

// ExternalConnectionInfo describes a connected plugin session.
type ExternalConnectionInfo struct {
	BackupVersion   int      `json:"backupVersion,omitempty"`
	PluginID        string   `json:"pluginId"`
	DriverID        string   `json:"driverId"`
	Dialect         string   `json:"dialect"`
	Features        []string `json:"features"`
	DefaultDatabase string   `json:"defaultDatabase,omitempty"`
	ServerVersion   string   `json:"serverVersion,omitempty"`
	ReadOnly        bool     `json:"readOnly,omitempty"`
}

// ExternalExecuteMode mirrors plugin.db.execute "mode".
type ExternalExecuteMode string

const (
	ExternalExecuteAuto  ExternalExecuteMode = "auto"
	ExternalExecuteQuery ExternalExecuteMode = "query"
	ExternalExecuteExec  ExternalExecuteMode = "exec"
)

// ExternalExecute is one statement execution (plugin.db.execute + fetch).
type ExternalExecute struct {
	RequestID string
	Statement string
	Database  string
	Mode      ExternalExecuteMode
	ReadOnly  bool
	// MaxRows/MaxBytes are the total result caps; the provider pages within
	// the contract pageMaxRows/pageMaxBytes.
	MaxRows  int
	MaxBytes int64
	Timeout  time.Duration
}

// ExternalColumn is one result column.
type ExternalColumn struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// ExternalPage is one validated page of rows. Columns is set on the first
// page only. Cells are already normalized (null, bool, number, string, or
// bounded object/array).
type ExternalPage struct {
	Columns []ExternalColumn
	Rows    [][]any
	Notice  string
}

// CancelState is the outcome of plugin.db.cancel.
type CancelState string

const (
	CancelConfirmed CancelState = "confirmed"
	CancelRequested CancelState = "requested"
	CancelNotFound  CancelState = "notFound"
)

// ExternalExecuteSummary closes a paged execution.
type ExternalExecuteSummary struct {
	Columns      []ExternalColumn
	RowCount     int
	AffectedRows *int64
	Notice       string
	Truncated    bool
	// Cancelled is empty unless the execution was cancelled; then it is
	// CancelConfirmed or CancelRequested (outcome not confirmed).
	Cancelled CancelState
	// NextPageToken is the plugin.db.tableRows nextPageToken (TableRows only;
	// ≤1024 bytes, validated by the host before it reaches the WebView).
	NextPageToken string
}

// ExternalMetadataKind mirrors plugin.db.metadata "kind".
type ExternalMetadataKind string

const (
	ExternalMetadataNavigation  ExternalMetadataKind = "navigation"
	ExternalMetadataTables      ExternalMetadataKind = "tables"
	ExternalMetadataColumns     ExternalMetadataKind = "columns"
	ExternalMetadataPrimaryKeys ExternalMetadataKind = "primaryKeys"
	ExternalMetadataIndexes     ExternalMetadataKind = "indexes"
	ExternalMetadataForeignKeys ExternalMetadataKind = "foreignKeys"
	ExternalMetadataTriggers    ExternalMetadataKind = "triggers"
	ExternalMetadataDDL         ExternalMetadataKind = "ddl"
)

// ExternalMetadataRequest asks for one metadata object. The result is the
// JSON of the matching host DTO (DbSchemaInfo, []SchemaTable, []ColumnInfo,
// PrimaryKeyInfo, []DbIndexInfo, ...), still to be validated by T1.
type ExternalMetadataRequest struct {
	Kind     ExternalMetadataKind
	Database string
	Table    string
}

// ExternalTableRows is a read-only table preview page request.
type ExternalTableRows struct {
	RequestID string
	Database  string
	Table     string
	Limit     int
	Offset    int
	PageToken string
	MaxRows   int
	MaxBytes  int64
}

// ExternalMutationOp mirrors plugin.db.mutate "op".
type ExternalMutationOp string

const (
	ExternalMutationUpdateCell ExternalMutationOp = "updateCell"
	ExternalMutationInsertRow  ExternalMutationOp = "insertRow"
	ExternalMutationDeleteRow  ExternalMutationOp = "deleteRow"
)

// ExternalMutation is one structured row change; never raw SQL.
type ExternalMutation struct {
	RequestID  string
	Op         ExternalMutationOp
	Database   string
	Table      string
	PrimaryKey map[string]any
	Column     string
	Value      any
	Fields     map[string]any
}

// ExternalConnection is one live plugin driver handle. Implementations bound
// every call by ctx and must be safe for concurrent use.
//
// Execute/TableRows call page sequentially. When page returns an error the
// implementation stops paging, closes the cursor (plugin.db.closeCursor) and
// returns that error unchanged or wrapped with %w: the host uses a sentinel
// to stop at its row/byte caps. Errors use the "<code>: detail" convention.
type ExternalConnection interface {
	Info() ExternalConnectionInfo
	Execute(ctx context.Context, req ExternalExecute, page func(ExternalPage) error) (*ExternalExecuteSummary, error)
	Metadata(ctx context.Context, req ExternalMetadataRequest) (json.RawMessage, error)
	TableRows(ctx context.Context, req ExternalTableRows, page func(ExternalPage) error) (*ExternalExecuteSummary, error)
	Mutate(ctx context.Context, req ExternalMutation) (int64, error)
	// Destructive runs a structured "dropTable" or "clearTable" action, or
	// "dropDatabase" / "clearDatabase" (table empty) on a whole database.
	Destructive(ctx context.Context, action, database, table string) error
	Cancel(ctx context.Context, requestID string) (CancelState, error)
	Close(ctx context.Context) error
}

// errExternalDriverUnavailable is returned for Driver-bound configs until a
// provider and the T1 session routing exist. It keeps the "<code>: detail"
// error convention of the driver protocol.
var errExternalDriverUnavailable = errors.New("unavailable: plugin database drivers are not available in this build")

var (
	externalProviderMu sync.RWMutex
	externalProvider   ExternalDriverProvider
)

// RegisterExternalDriverProvider installs the single provider (the plugin
// host). Call from init(); a second registration panics.
func RegisterExternalDriverProvider(provider ExternalDriverProvider) {
	if provider == nil {
		panic("nil external driver provider")
	}
	externalProviderMu.Lock()
	defer externalProviderMu.Unlock()
	if externalProvider != nil {
		panic("external driver provider registered twice")
	}
	externalProvider = provider
}

// registeredExternalDriverProvider returns the provider, or nil. Used by T1.
func registeredExternalDriverProvider() ExternalDriverProvider {
	externalProviderMu.RLock()
	defer externalProviderMu.RUnlock()
	return externalProvider
}

// ExternalDriverReleaseAllowed reports whether a saved connection bound to ref
// may connect without a new credential prompt (cloud companion listing).
func ExternalDriverReleaseAllowed(ref ExternalDriverRef, connectionID string) bool {
	provider := registeredExternalDriverProvider()
	if provider == nil || connectionID == "" || len(connectionID) > externalRefMaxLength {
		return false
	}
	return provider.ReleaseAllowed(ref, connectionID)
}

// ExternalBackupConnection provides a negotiated native stream independently of
// normalized Execute/TableRows results. Chunks are bounded by the RPC page budget.
type ExternalBackupConnection interface {
	Backup(ctx context.Context, requestID, database, table string, chunk func([]byte) error) error
}

// ── plugin.db.snapshotWrite (feature transaction.snapshotWrite) ──

// ExternalSnapshotStatement is one SQL statement with positional JSON scalar
// arguments (nil, bool, number, string).
type ExternalSnapshotStatement struct {
	Statement string `json:"statement"`
	Args      []any  `json:"args"`
}

// ExternalSnapshotMutation is a structured row edit inside a snapshotWrite
// step, shaped like plugin.db.mutate minus handle/requestId/database.
type ExternalSnapshotMutation struct {
	Op         ExternalMutationOp `json:"op"`
	Table      string             `json:"table"`
	PrimaryKey map[string]any     `json:"primaryKey,omitempty"`
	Column     string             `json:"column,omitempty"`
	Value      any                `json:"value,omitempty"`
	Fields     map[string]any     `json:"fields,omitempty"`
}

// ExternalSnapshotStep runs Snapshot (optional), then exactly one of Write
// or Mutation, then After (optional). MaxRows bounds the Snapshot rows (1 to
// 1,000,000 with a snapshot, 0 without one); Returning applies to an
// insertRow mutation only.
type ExternalSnapshotStep struct {
	Snapshot  *ExternalSnapshotStatement `json:"snapshot,omitempty"`
	Write     *ExternalSnapshotStatement `json:"write,omitempty"`
	Mutation  *ExternalSnapshotMutation  `json:"mutation,omitempty"`
	After     *ExternalSnapshotStatement `json:"after,omitempty"`
	MaxRows   int64                      `json:"maxRows"`
	Returning []string                   `json:"returning,omitempty"`
}

// ExternalSnapshotRequest is one plugin.db.snapshotWrite call: every step
// runs in one transaction in Database that commits only when all succeeded.
type ExternalSnapshotRequest struct {
	RequestID string
	Database  string
	Steps     []ExternalSnapshotStep
}

// ExternalSnapshotRows is a before/after image as the plugin sent it: each
// cell is still a raw typed cell ({"t":…,"v":…}), validated and decoded by
// the host (externalSnapshotWrite).
type ExternalSnapshotRows struct {
	Columns []ExternalColumn    `json:"columns"`
	Rows    [][]json.RawMessage `json:"rows"`
}

// ExternalSnapshotStepResult is the outcome of one step; Affected is -1 when
// the driver cannot report it.
type ExternalSnapshotStepResult struct {
	Before   *ExternalSnapshotRows      `json:"before,omitempty"`
	Affected int64                      `json:"affected"`
	After    *ExternalSnapshotRows      `json:"after,omitempty"`
	Returned map[string]json.RawMessage `json:"returned,omitempty"`
}

// ExternalSnapshotResponse answers a request. When Exceeded is set the
// transaction was rolled back and ExceededStep names the step whose snapshot
// had more than MaxRows rows; Steps is then empty.
type ExternalSnapshotResponse struct {
	Steps        []ExternalSnapshotStepResult `json:"steps"`
	Exceeded     bool                         `json:"exceeded"`
	ExceededStep int                          `json:"exceededStep"`
}

// ExternalSnapshotConnection is implemented by connections that can send
// plugin.db.snapshotWrite. The host only uses it when the driver declares
// transaction.snapshotWrite. A reply that does not fit the line budget is
// an error with code "limit" (rolled back, retry in smaller batches).
type ExternalSnapshotConnection interface {
	SnapshotWrite(ctx context.Context, req ExternalSnapshotRequest) (*ExternalSnapshotResponse, error)
}
