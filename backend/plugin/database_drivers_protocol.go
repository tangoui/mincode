package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/db"
)

// Wire layer of the plugin.db.* client (docs/architecture/database-driver-protocol.md).
// Everything a driver returns is untrusted: this file bounds and shape-checks
// results before they reach backend/db (which validates cells, columns and
// metadata DTOs again) and maps plugin errors to "<code>: detail" with every
// plugin-originated message passed through the log redactor.

const (
	databaseDriverProtocolVersion = 1
	// Protocol 1.0 page budget: every plugin supports it, and the host uses it
	// unless protocol 1.1 page limits were negotiated.
	databaseDriverPageMaxBytes = 524288
	databaseDriverPageMaxRows  = 1000
	// Protocol 1.1 ceilings (negotiated through "minor": 1 and "pageLimits").
	// A 2 MiB page travels as one reply line; the runtime accepts it only as
	// the response to a plugin.db.execute/fetch/tableRows call of a
	// generation that negotiated 1.1 (pluginRPCMaxLargeReply, 4 MiB).
	databaseDriverProtocolMinor     = 1
	databaseDriverPageMaxBytesV11   = 2 << 20
	databaseDriverPageMaxRowsV11    = 5000
	databaseDriverCursorIdleMs      = 60000
	databaseDriverMetadataMaxBytes  = 8 << 20
	databaseDriverOpaqueIDMax       = 128
	databaseDriverServerVersionMax  = 128
	databaseDriverPageTokenMax      = 1024
	databaseDriverNoticeMaxRunes    = 1024
	databaseDriverErrorMaxRunes     = 1000
	databaseDriverNegotiatedDrivers = 256
)

// databaseDriverRequestPageRows caps the rows the host asks for per page,
// even when a larger page was negotiated: pages this small let the plugin
// encode page N+1 while the host decodes page N (both sides prefetch),
// whereas one large page serialises the two. The negotiated byte budget
// still applies in full, so pages of wide rows fit.
const databaseDriverRequestPageRows = 1000

const (
	databaseDriverMethodConnect     = "plugin.db.connect"
	databaseDriverMethodExecute     = "plugin.db.execute"
	databaseDriverMethodFetch       = "plugin.db.fetch"
	databaseDriverMethodCloseCursor = "plugin.db.closeCursor"
	databaseDriverMethodCancel      = "plugin.db.cancel"
	databaseDriverMethodMetadata    = "plugin.db.metadata"
	databaseDriverMethodTableRows   = "plugin.db.tableRows"
	databaseDriverMethodBackup      = "plugin.db.backup"
	databaseDriverMethodMutate      = "plugin.db.mutate"
	databaseDriverMethodDestructive = "plugin.db.destructive"
	databaseDriverMethodSnapshot    = "plugin.db.snapshotWrite"
	databaseDriverMethodClose       = "plugin.db.close"
)

// ---------------------------------------------------------------------------
// Negotiation.

type databaseDriverNegotiation struct {
	Protocol int      `json:"protocol"`
	Drivers  []string `json:"drivers"`
	// Protocol 1.1 (optional; hosts before 1.1 ignore both members).
	Minor      int `json:"minor"`
	PageLimits *struct {
		MaxRows  int   `json:"maxRows"`
		MaxBytes int64 `json:"maxBytes"`
	} `json:"pageLimits"`
	// Protocol v2 draft (optional; hosts before it ignore the member): page
	// encodings the plugin can send (database_drivers_columnar.go).
	Encodings []string `json:"encodings"`
}

// databaseDriverPageLimits is the page budget of one process generation.
type databaseDriverPageLimits struct {
	rows  int
	bytes int64
}

// large reports whether pages may exceed the 1 MiB runtime line cap.
func (limits databaseDriverPageLimits) large() bool {
	return limits.bytes > databaseDriverPageMaxBytes || limits.rows > databaseDriverPageMaxRows
}

// requestRows is the most rows the host asks for in one page.
func (limits databaseDriverPageLimits) requestRows() int {
	return min(limits.rows, databaseDriverRequestPageRows)
}

var databaseDriverPageLimitsV10 = databaseDriverPageLimits{rows: databaseDriverPageMaxRows, bytes: databaseDriverPageMaxBytes}

// negotiatedDatabaseDriverPageLimits returns the page budget agreed in the
// initialize details: protocol 1.1 ("minor" >= 1 with "pageLimits" at least
// the 1.0 budget) gets min(plugin, host 1.1 ceiling); anything else, including
// a 1.0 plugin or malformed limits, keeps the 1.0 budget.
func negotiatedDatabaseDriverPageLimits(raw json.RawMessage) databaseDriverPageLimits {
	var details databaseDriverNegotiation
	if json.Unmarshal(raw, &details) != nil || details.Protocol != databaseDriverProtocolVersion ||
		details.Minor < databaseDriverProtocolMinor || details.PageLimits == nil ||
		details.PageLimits.MaxRows < databaseDriverPageMaxRows || details.PageLimits.MaxBytes < databaseDriverPageMaxBytes {
		return databaseDriverPageLimitsV10
	}
	return databaseDriverPageLimits{
		rows:  min(details.PageLimits.MaxRows, databaseDriverPageMaxRowsV11),
		bytes: min(details.PageLimits.MaxBytes, databaseDriverPageMaxBytesV11),
	}
}

// databaseDriverRuntimeMaxMessage is the runtimeFeature.MaxMessage hook: the
// stdout line cap of a generation that negotiated databaseDrivers.
func databaseDriverRuntimeMaxMessage(details json.RawMessage) int {
	if negotiatedDatabaseDriverPageLimits(details).large() {
		return pluginRPCMaxLargeReply
	}
	return pluginRPCMaxMessage
}

// parseDatabaseDriverNegotiation validates the initialize "databaseDrivers"
// member and reports whether driverID is implemented.
func parseDatabaseDriverNegotiation(raw json.RawMessage, driverID string) error {
	var details databaseDriverNegotiation
	if err := json.Unmarshal(raw, &details); err != nil || details.Protocol != databaseDriverProtocolVersion ||
		len(details.Drivers) > databaseDriverNegotiatedDrivers || !slices.Contains(details.Drivers, driverID) {
		return databaseDriverError(databaseDriverErrUnavailable, "plugin does not implement database driver %s", driverID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Params.

type databaseDriverLimits struct {
	PageMaxBytes int `json:"pageMaxBytes"`
	PageMaxRows  int `json:"pageMaxRows"`
	CursorIdleMs int `json:"cursorIdleMs"`
}

type databaseDriverConnectParams struct {
	DriverID  string               `json:"driverId"`
	Config    map[string]any       `json:"config"`
	Limits    databaseDriverLimits `json:"limits"`
	TimeoutMs int64                `json:"timeoutMs"`
	// Encoding requests an announced page encoding (protocol v2 draft);
	// omitted otherwise, so pre-v2 plugins see unchanged params.
	Encoding string `json:"encoding,omitempty"`
}

type databaseDriverExecuteParams struct {
	Handle    string `json:"handle"`
	RequestID string `json:"requestId"`
	Statement string `json:"statement"`
	Database  string `json:"database,omitempty"`
	Mode      string `json:"mode"`
	ReadOnly  bool   `json:"readOnly"`
	MaxRows   int    `json:"maxRows"`
	MaxBytes  int64  `json:"maxBytes"`
	TimeoutMs int64  `json:"timeoutMs"`
}

type databaseDriverFetchParams struct {
	Handle   string `json:"handle"`
	Cursor   string `json:"cursor"`
	MaxRows  int    `json:"maxRows"`
	MaxBytes int64  `json:"maxBytes"`
}

type databaseDriverCursorParams struct {
	Handle string `json:"handle"`
	Cursor string `json:"cursor"`
}

type databaseDriverCancelParams struct {
	Handle    string `json:"handle"`
	RequestID string `json:"requestId"`
}

type databaseDriverMetadataParams struct {
	Handle   string `json:"handle"`
	Kind     string `json:"kind"`
	Database string `json:"database,omitempty"`
	Table    string `json:"table,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	MaxBytes int64  `json:"maxBytes"`
}

type databaseDriverTableRowsParams struct {
	Handle    string `json:"handle"`
	RequestID string `json:"requestId"`
	Database  string `json:"database"`
	Table     string `json:"table"`
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
	PageToken string `json:"pageToken,omitempty"`
	ReadOnly  bool   `json:"readOnly"`
	MaxRows   int    `json:"maxRows"`
	MaxBytes  int64  `json:"maxBytes"`
}

type databaseDriverMutateParams struct {
	Handle     string         `json:"handle"`
	RequestID  string         `json:"requestId"`
	Op         string         `json:"op"`
	Database   string         `json:"database"`
	Table      string         `json:"table"`
	PrimaryKey map[string]any `json:"primaryKey,omitempty"`
	Column     string         `json:"column,omitempty"`
	Value      any            `json:"value,omitempty"`
	Fields     map[string]any `json:"fields,omitempty"`
}

type databaseDriverDestructiveParams struct {
	Handle    string `json:"handle"`
	RequestID string `json:"requestId"`
	Action    string `json:"action"`
	Database  string `json:"database"`
	Table     string `json:"table"`
}

type databaseDriverSnapshotParams struct {
	Handle    string                    `json:"handle"`
	RequestID string                    `json:"requestId"`
	Database  string                    `json:"database"`
	Steps     []db.ExternalSnapshotStep `json:"steps"`
}

type databaseDriverHandleParams struct {
	Handle string `json:"handle"`
}

// ---------------------------------------------------------------------------
// Results.

type databaseDriverConnectResult struct {
	Handle string `json:"handle"`
	Info   struct {
		ServerVersion   string `json:"serverVersion"`
		DefaultDatabase string `json:"defaultDatabase"`
		ReadOnly        bool   `json:"readOnly"`
		BackupVersion   int    `json:"backupVersion"`
		Encoding        string `json:"encoding"`
	} `json:"info"`
}

type databaseDriverPageResult struct {
	Columns       []db.ExternalColumn `json:"columns"`
	Rows          json.RawMessage     `json:"rows"`
	AffectedRows  *int64              `json:"affectedRows"`
	Notice        string              `json:"notice"`
	Cursor        string              `json:"cursor"`
	Done          bool                `json:"done"`
	NextPageToken string              `json:"nextPageToken"`
	// Encoding and Data carry a columnar page (protocol v2 draft) instead
	// of Rows; only accepted on a connection that negotiated it.
	Encoding string          `json:"encoding"`
	Data     json.RawMessage `json:"data"`

	decodedRows [][]any
	// columnar is the base64 text of Data; maxBytes the requested budget.
	columnar []byte
	maxBytes int64
}

type databaseDriverMetadataResult struct {
	Value  json.RawMessage   `json:"value"`
	Items  []json.RawMessage `json:"items"`
	Cursor string            `json:"cursor"`
}

func databaseDriverProtocolError(format string, args ...any) error {
	return databaseDriverError(databaseDriverErrProtocol, format, args...)
}

func validDatabaseDriverOpaqueID(value string) bool {
	return value != "" && len(value) <= databaseDriverOpaqueIDMax && utf8.ValidString(value) &&
		!strings.ContainsFunc(value, unicode.IsControl)
}

func decodeDatabaseDriverConnectResult(raw json.RawMessage) (databaseDriverConnectResult, error) {
	var result databaseDriverConnectResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, databaseDriverProtocolError("invalid plugin.db.connect result")
	}
	if !validDatabaseDriverOpaqueID(result.Handle) {
		return result, databaseDriverProtocolError("plugin.db.connect returned an invalid handle")
	}
	if result.Info.BackupVersion < 0 || result.Info.BackupVersion > 1 {
		return result, databaseDriverProtocolError("unsupported native backup version")
	}
	if len(result.Info.ServerVersion) > databaseDriverServerVersionMax {
		return result, databaseDriverProtocolError("plugin.db.connect serverVersion exceeds %d bytes", databaseDriverServerVersionMax)
	}
	return result, nil
}

// decodeDatabaseDriverPage validates one execute/fetch/tableRows page
// against the page budget the host asked for.
func decodeDatabaseDriverPage(method string, raw json.RawMessage, maxRows int, maxBytes int64, first bool) (*databaseDriverPageResult, error) {
	page, err := decodeDatabaseDriverPageHeader(method, raw, maxBytes, first)
	if err != nil {
		return nil, err
	}
	if err := page.decodeRows(method, maxRows); err != nil {
		return nil, err
	}
	return page, nil
}

// decodeDatabaseDriverPageHeader validates everything but the row cells, so
// the cursor is known (and the next fetch can be sent) before the rows are
// decoded. Rows stay raw until decodeRows. It accepts JSON rows only; a
// connection that negotiated the columnar encoding uses
// decodeDatabaseDriverPageHeaderEncoded.
func decodeDatabaseDriverPageHeader(method string, raw json.RawMessage, maxBytes int64, first bool) (*databaseDriverPageResult, error) {
	return decodeDatabaseDriverPageHeaderEncoded(method, raw, maxBytes, first, false)
}

// decodeDatabaseDriverPageHeaderEncoded is decodeDatabaseDriverPageHeader
// that also accepts a columnar page when columnar was negotiated. A
// connection that negotiated it may still answer JSON rows.
func decodeDatabaseDriverPageHeaderEncoded(method string, raw json.RawMessage, maxBytes int64, first, columnar bool) (*databaseDriverPageResult, error) {
	var page databaseDriverPageResult
	if header, rows, data, ok := splitDatabaseDriverPageRows(raw); ok {
		// Rows are validated by decodeRows; everything else by Unmarshal.
		if err := json.Unmarshal(header, &page); err != nil {
			return nil, databaseDriverProtocolError("invalid %s result", method)
		}
		page.Rows, page.Data = rows, data
	} else if err := json.Unmarshal(raw, &page); err != nil {
		return nil, databaseDriverProtocolError("invalid %s result", method)
	}
	page.maxBytes = maxBytes
	// Before v2 unknown members were ignored, so a stray "data" without
	// "encoding" still is on a connection that did not negotiate an encoding;
	// an explicit "encoding" there is a protocol error, never decoded.
	if !columnar && page.Encoding == "" {
		page.Data = nil
	}
	if page.Encoding != "" || page.Data != nil {
		if !columnar || page.Encoding != databaseDriverEncodingColumnar {
			return nil, databaseDriverProtocolError("%s returned a page encoding that was not negotiated", method)
		}
		if page.Rows != nil {
			return nil, databaseDriverProtocolError("%s returned both rows and columnar data", method)
		}
		data := page.Data
		// A base64 string never needs escapes; anything else is rejected.
		if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' || bytes.IndexByte(data[1:len(data)-1], '\\') >= 0 || bytes.IndexByte(data[1:len(data)-1], '"') >= 0 {
			return nil, databaseDriverProtocolError("%s columnar data must be a base64 string", method)
		}
		if int64(len(data)-2) > maxBytes {
			return nil, databaseDriverProtocolError("%s page exceeds the requested %d bytes", method, maxBytes)
		}
		page.columnar, page.Data = data[1:len(data)-1], nil
	}
	if !first && page.Columns != nil {
		return nil, databaseDriverProtocolError("%s returned columns on a continuation page", method)
	}
	rows := bytes.TrimSpace(page.Rows)
	if len(rows) == 0 || bytes.Equal(rows, []byte("null")) {
		rows = nil
	}
	if int64(len(rows)) > maxBytes {
		return nil, databaseDriverProtocolError("%s page exceeds the requested %d bytes", method, maxBytes)
	}
	page.Rows = rows
	if page.Done {
		page.Cursor = ""
	} else if !validDatabaseDriverOpaqueID(page.Cursor) {
		return nil, databaseDriverProtocolError("%s returned an unfinished page without a valid cursor", method)
	}
	if len(page.NextPageToken) > databaseDriverPageTokenMax {
		return nil, databaseDriverProtocolError("%s nextPageToken exceeds %d bytes", method, databaseDriverPageTokenMax)
	}
	return &page, nil
}

// splitDatabaseDriverPageRows lifts the "rows" member (and the columnar
// "data" member) out of a page object so the header is unmarshalled without
// scanning a page of cells a second time (the rows are decoded, and
// validated, once by decodeRows). It checks only the object structure around
// the members; the header object it returns holds every other member
// verbatim for json.Unmarshal to validate. It reports false, so the caller
// falls back to a full Unmarshal, whenever the input is not a plain object, a
// key is escaped, a member repeats, or a key other than "rows"/"data" would
// match those fields case-insensitively.
func splitDatabaseDriverPageRows(raw []byte) (header, rows, data []byte, ok bool) {
	i := skipJSONSpace(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return nil, nil, nil, false
	}
	header = make([]byte, 0, 256)
	header = append(header, '{')
	i = skipJSONSpace(raw, i+1)
	if i < len(raw) && raw[i] == '}' {
		return nil, nil, nil, false // empty object: let Unmarshal handle it
	}
	for {
		if i >= len(raw) || raw[i] != '"' {
			return nil, nil, nil, false
		}
		keyStart := i
		keyEnd := skipJSONString(raw, i)
		if keyEnd < 0 || bytes.IndexByte(raw[i:keyEnd], '\\') >= 0 {
			return nil, nil, nil, false
		}
		key := raw[i+1 : keyEnd-1]
		i = skipJSONSpace(raw, keyEnd)
		if i >= len(raw) || raw[i] != ':' {
			return nil, nil, nil, false
		}
		start := skipJSONSpace(raw, i+1)
		var end int
		if string(key) == "data" && start < len(raw) && raw[start] == '"' {
			// Columnar data is a base64 string: find its end with one
			// IndexByte instead of a byte loop. A backslash cannot occur in
			// base64, so fall back to Unmarshal (which then rejects it).
			k := bytes.IndexByte(raw[start+1:], '"')
			if k < 0 || bytes.IndexByte(raw[start+1:start+1+k], '\\') >= 0 {
				return nil, nil, nil, false
			}
			end = start + k + 2
		} else {
			end = skipJSONValue(raw, start)
		}
		if end < 0 {
			return nil, nil, nil, false
		}
		switch {
		case string(key) == "rows" && rows == nil:
			rows = raw[start:end]
		case string(key) == "data" && data == nil:
			data = raw[start:end]
		case bytes.EqualFold(key, []byte("rows")), bytes.EqualFold(key, []byte("data")):
			return nil, nil, nil, false
		default:
			if len(header) > 1 {
				header = append(header, ',')
			}
			header = append(header, raw[keyStart:keyEnd]...)
			header = append(header, ':')
			header = append(header, raw[start:end]...)
		}
		i = skipJSONSpace(raw, end)
		if i >= len(raw) {
			return nil, nil, nil, false
		}
		if raw[i] == '}' {
			if skipJSONSpace(raw, i+1) != len(raw) {
				return nil, nil, nil, false
			}
			return append(header, '}'), rows, data, true
		}
		if raw[i] != ',' {
			return nil, nil, nil, false
		}
		i = skipJSONSpace(raw, i+1)
	}
}

func skipJSONSpace(raw []byte, i int) int {
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r') {
		i++
	}
	return i
}

// skipJSONString returns the index after the string starting at raw[i] (a
// quote), or -1. Content is not validated here.
func skipJSONString(raw []byte, i int) int {
	for i++; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

// skipJSONValue returns the index after the value starting at raw[i], or -1
// when its extent cannot be found. It only balances brackets outside
// strings; the value itself is validated by whoever decodes it.
func skipJSONValue(raw []byte, i int) int {
	if i >= len(raw) {
		return -1
	}
	switch raw[i] {
	case '"':
		return skipJSONString(raw, i)
	case '{', '[':
		depth := 0
		for i < len(raw) {
			switch raw[i] {
			case '"':
				if i = skipJSONString(raw, i); i < 0 {
					return -1
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return -1
	}
	start := i
	for i < len(raw) {
		switch raw[i] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			if i == start {
				return -1
			}
			return i
		}
		i++
	}
	return -1
}

// decodeRows decodes the raw rows of a header-validated page into
// decodedRows and enforces the requested row budget.
func (page *databaseDriverPageResult) decodeRows(method string, maxRows int) error {
	if page.columnar != nil {
		rows, err := decodeDatabaseDriverColumnar(method, page.columnar, maxRows, page.maxBytes)
		if err != nil {
			return err
		}
		page.decodedRows, page.columnar = rows, nil
		return nil
	}
	if page.Rows != nil {
		decoder := json.NewDecoder(bytes.NewReader(page.Rows))
		decoder.UseNumber()
		if err := decoder.Decode(&page.decodedRows); err != nil {
			return databaseDriverProtocolError("%s rows must be an array of arrays", method)
		}
		for _, row := range page.decodedRows {
			if row == nil {
				return databaseDriverProtocolError("%s rows must be an array of arrays", method)
			}
		}
	}
	page.Rows = nil
	if len(page.decodedRows) > maxRows {
		return databaseDriverProtocolError("%s page has %d rows, more than the requested %d", method, len(page.decodedRows), maxRows)
	}
	return nil
}

func decodeDatabaseDriverCancelResult(raw json.RawMessage) (db.CancelState, error) {
	var result struct {
		State db.CancelState `json:"state"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", databaseDriverProtocolError("invalid plugin.db.cancel result")
	}
	switch result.State {
	case db.CancelConfirmed, db.CancelRequested, db.CancelNotFound:
		return result.State, nil
	}
	return "", databaseDriverProtocolError("plugin.db.cancel returned an unknown state")
}

// ---------------------------------------------------------------------------
// Errors.

var databaseDriverKindCodes = map[string]string{
	"database":    databaseDriverErrDriver,
	"connection":  databaseDriverErrUnavailable,
	"cancelled":   databaseDriverErrCancelled,
	"timeout":     databaseDriverErrTimeout,
	"unsupported": "unsupported",
	"invalid":     databaseDriverErrInvalidRequest,
	"busy":        databaseDriverErrBusy,
	// plugin.db.snapshotWrite rolled back because its reply would not fit
	// one message; backend/db retries in smaller batches or reconfirms
	// without undo, never assuming a commit.
	"limit": "limit",
	// plugin.db.snapshotWrite succeeded in the driver (it may have
	// committed) but the SDK could not send its result; the snapshotWrite
	// proxy turns it into db.ErrExternalSnapshotReply.
	"committed": "committed",
}

// sanitizeDatabaseDriverText redacts known secrets, strips control
// characters and bounds plugin-originated text.
func sanitizeDatabaseDriverText(pluginID, text string, maxRunes int) string {
	text = strings.ToValidUTF8(text, "�")
	text = redactPluginMessage(pluginID, text)
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > maxRunes {
		runes := []rune(text)
		text = string(runes[:maxRunes]) + "…"
	}
	return text
}

// mapDatabaseDriverCallError converts a runtime call failure into the
// "<code>: detail" convention. Context errors pass through unchanged so
// backend/db can tell user cancellation and host deadlines apart.
func mapDatabaseDriverCallError(pluginID, method string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var rpcErr *processRPCError
	if errors.As(err, &rpcErr) {
		kind, _ := rpcErr.Data["kind"].(string)
		message := sanitizeDatabaseDriverText(pluginID, rpcErr.Message, databaseDriverErrorMaxRunes)
		if message == "" {
			message = "plugin call failed"
		}
		if kind == "" && rpcErr.Code == -32000 && rpcErr.Message == "plugin process exited" {
			return databaseDriverError(databaseDriverErrDriver, "plugin process exited")
		}
		code, ok := databaseDriverKindCodes[kind]
		if !ok {
			code = databaseDriverErrDriver
		}
		return fmt.Errorf("%s: %s", code, message)
	}
	message := err.Error()
	switch {
	case message == "plugin process is not running", message == "plugin process generation changed":
		return databaseDriverError(databaseDriverErrUnavailable, "database driver plugin %s is no longer running", pluginID)
	case strings.Contains(message, "exceeds 1 MiB"):
		return databaseDriverError(databaseDriverErrTooLarge, "%s request exceeds the 1 MiB message limit", method)
	}
	return databaseDriverError(databaseDriverErrDriver, "%s", sanitizeDatabaseDriverText(pluginID, message, databaseDriverErrorMaxRunes))
}
