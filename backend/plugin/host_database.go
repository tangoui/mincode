package plugin

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"

	"mincode/app/backend/core"
	"mincode/app/backend/db"
	"mincode/app/plugins/api"
)

// Read-only schema access (capability database.schema). A plugin may list the
// user's saved SQL connections (id, name, type, default database only) and,
// after the user authorizes a connection, read its databases, tables and
// table structure. Hosts, users and credentials never reach the plugin, and
// no SQL from the plugin is executed.

const (
	databaseSchemaPromptKind  = "databaseSchema"
	databaseSchemaNameMaxLen  = 256
	databaseSchemaMaxResponse = 4 << 20
)

// databaseSchemaGrantsKVKey contains "/", which plugin IDs never do.
func databaseSchemaGrantsKVKey(id string) string { return "dbschema/" + id }

var databaseSchemaGrantsMu sync.Mutex

// "Allow" without "always" lasts while the plugin process runs: the grant is
// keyed by plugin, connection and process generation, so a restart asks
// again and revoking clears it at once.
var (
	databaseSchemaRunGrantsMu sync.Mutex
	databaseSchemaRunGrants   = map[string]map[string]uint64{}
)

func runtimeGeneration(runtimeHost *processRuntime) uint64 {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	return runtimeHost.generation
}

func databaseSchemaRunGranted(runtimeHost *processRuntime, connectionID string) bool {
	generation := runtimeGeneration(runtimeHost)
	databaseSchemaRunGrantsMu.Lock()
	defer databaseSchemaRunGrantsMu.Unlock()
	granted, ok := databaseSchemaRunGrants[runtimeHost.pluginID][connectionID]
	return ok && granted == generation
}

func grantDatabaseSchemaRun(runtimeHost *processRuntime, connectionID string) {
	generation := runtimeGeneration(runtimeHost)
	databaseSchemaRunGrantsMu.Lock()
	defer databaseSchemaRunGrantsMu.Unlock()
	grants := databaseSchemaRunGrants[runtimeHost.pluginID]
	if grants == nil {
		grants = map[string]uint64{}
		databaseSchemaRunGrants[runtimeHost.pluginID] = grants
	}
	grants[connectionID] = generation
}

// revokeDatabaseSchemaRun clears run grants for one connection ("" = all).
func revokeDatabaseSchemaRun(pluginID, connectionID string) {
	databaseSchemaRunGrantsMu.Lock()
	defer databaseSchemaRunGrantsMu.Unlock()
	if connectionID == "" {
		delete(databaseSchemaRunGrants, pluginID)
		return
	}
	delete(databaseSchemaRunGrants[pluginID], connectionID)
}

type databaseSchemaGrants struct {
	Connections []string `json:"connections"`
}

func readDatabaseSchemaGrants(pluginID string) databaseSchemaGrants {
	raw, found, err := pluginKVGetRaw(hostPluginID, databaseSchemaGrantsKVKey(pluginID))
	var grants databaseSchemaGrants
	if err != nil || !found || json.Unmarshal(raw, &grants) != nil {
		return databaseSchemaGrants{}
	}
	return grants
}

func writeDatabaseSchemaGrants(pluginID string, grants databaseSchemaGrants) error {
	slices.Sort(grants.Connections)
	grants.Connections = slices.Compact(grants.Connections)
	if len(grants.Connections) == 0 {
		return pluginKVDeleteRaw(hostPluginID, databaseSchemaGrantsKVKey(pluginID))
	}
	raw, _ := json.Marshal(grants)
	return pluginKVSetRaw(hostPluginID, databaseSchemaGrantsKVKey(pluginID), raw)
}

// Test seams.
var (
	databaseSchemaGranted = func(pluginID, connectionID string) bool {
		return slices.Contains(readDatabaseSchemaGrants(pluginID).Connections, connectionID)
	}
	grantDatabaseSchema = func(pluginID, connectionID string) error {
		databaseSchemaGrantsMu.Lock()
		defer databaseSchemaGrantsMu.Unlock()
		grants := readDatabaseSchemaGrants(pluginID)
		grants.Connections = append(grants.Connections, connectionID)
		return writeDatabaseSchemaGrants(pluginID, grants)
	}
	databaseSchemaConnections = db.ListSchemaConnections
	databaseSchemaTables      = db.ListSchemaTables
	databaseSchemaDescribe    = db.DescribeSchemaTables
)

// databaseSchemaDetails describes the connection in the approval prompt.
type databaseSchemaDetails struct {
	ConnectionID string `json:"connectionId"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Database     string `json:"database,omitempty"`
}

func init() {
	registerHostMethod("host.database.connections", nil, api.CapabilityDatabaseSchema, hostDatabaseConnections)
	registerHostMethod("host.database.tables", nil, api.CapabilityDatabaseSchema, hostDatabaseTables)
	registerHostMethod("host.database.describe", nil, api.CapabilityDatabaseSchema, hostDatabaseDescribe)
	core.Register("plugin_database_schema_grants_get", databaseSchemaGrantsGetHandler)
	core.Register("plugin_database_schema_grants_revoke", databaseSchemaGrantsRevokeHandler)
}

func databaseSchemaError(err error) *rpcError {
	if errors.Is(err, db.ErrSchemaConnectionNotFound) {
		return &rpcError{Code: -32002, Message: err.Error(), Data: map[string]string{"code": "not_found"}}
	}
	return &rpcError{Code: -32000, Message: err.Error()}
}

func hostDatabaseConnections(runtimeHost *processRuntime, _ json.RawMessage) (any, *rpcError) {
	connections, err := databaseSchemaConnections()
	if err != nil {
		return nil, databaseSchemaError(err)
	}
	granted := readDatabaseSchemaGrants(runtimeHost.pluginID).Connections
	out := make([]map[string]any, 0, len(connections))
	for _, connection := range connections {
		item := map[string]any{"id": connection.ID, "name": connection.Name, "kind": connection.Kind,
			"granted": slices.Contains(granted, connection.ID)}
		if connection.Database != "" {
			item["database"] = connection.Database
		}
		out = append(out, item)
	}
	return map[string]any{"connections": out}, nil
}

// authorizeDatabaseSchema asks the user the first time a plugin reads a
// connection; "always" remembers the connection for this plugin.
func authorizeDatabaseSchema(runtimeHost *processRuntime, connectionID string, timeoutMs int) *rpcError {
	approvalTimeout, rpcErr := promptTimeout(timeoutMs)
	if rpcErr != nil {
		return rpcErr
	}
	connections, err := databaseSchemaConnections()
	if err != nil {
		return databaseSchemaError(err)
	}
	index := slices.IndexFunc(connections, func(item db.SchemaConnection) bool { return item.ID == connectionID })
	if index < 0 {
		return databaseSchemaError(db.ErrSchemaConnectionNotFound)
	}
	if databaseSchemaGranted(runtimeHost.pluginID, connectionID) || databaseSchemaRunGranted(runtimeHost, connectionID) {
		return nil
	}
	connection := connections[index]
	items := []quickPickItem{{ID: "once", Label: "once"}, {ID: "always", Label: "always"}}
	event := promptEvent{Kind: databaseSchemaPromptKind, PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(),
		Title: cleanHostText(connection.Name, hostUITitleMaxRunes, false), Items: items,
		Database: &databaseSchemaDetails{ConnectionID: connection.ID, Name: cleanHostText(connection.Name, hostUITitleMaxRunes, false),
			Kind: connection.Kind, Database: cleanHostText(connection.Database, hostUITitleMaxRunes, false)}}
	reply, rpcErr := hostPrompts.ask(runtimeHost.promptContext(), event,
		&pendingPrompt{pluginID: runtimeHost.pluginID, kind: databaseSchemaPromptKind, items: map[string]bool{"once": true, "always": true}}, approvalTimeout)
	if rpcErr != nil {
		return rpcErr
	}
	if reply.Cancelled {
		return &rpcError{Code: -32003, Message: "the user declined access to the connection", Data: map[string]string{"code": "declined"}}
	}
	if slices.Contains(reply.IDs, "always") {
		if err := grantDatabaseSchema(runtimeHost.pluginID, connectionID); err != nil {
			return &rpcError{Code: -32000, Message: "could not save the authorization"}
		}
		return nil
	}
	grantDatabaseSchemaRun(runtimeHost, connectionID)
	return nil
}

func validDatabaseSchemaName(value string) bool {
	return value != "" && len(value) <= databaseSchemaNameMaxLen && !strings.ContainsFunc(value, unsafeTextRune)
}

func hostDatabaseTables(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		ConnectionID string `json:"connectionId"`
		Database     string `json:"database"`
		TimeoutMs    int    `json:"timeoutMs"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	connectionID := strings.TrimSpace(request.ConnectionID)
	database := strings.TrimSpace(request.Database)
	if !validDatabaseSchemaName(connectionID) || (database != "" && !validDatabaseSchemaName(database)) {
		return nil, hostUIParamsError("connectionId is required and names must be printable")
	}
	if rpcErr := authorizeDatabaseSchema(runtimeHost, connectionID, request.TimeoutMs); rpcErr != nil {
		return nil, rpcErr
	}
	listing, err := databaseSchemaTables(runtimeHost.promptContext(), connectionID, database)
	if err != nil {
		return nil, databaseSchemaError(err)
	}
	return listing, nil
}

func hostDatabaseDescribe(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		ConnectionID string   `json:"connectionId"`
		Database     string   `json:"database"`
		Tables       []string `json:"tables"`
		TimeoutMs    int      `json:"timeoutMs"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	connectionID := strings.TrimSpace(request.ConnectionID)
	database := strings.TrimSpace(request.Database)
	if !validDatabaseSchemaName(connectionID) || !validDatabaseSchemaName(database) ||
		len(request.Tables) == 0 || len(request.Tables) > db.SchemaDescribeMaxTables {
		return nil, hostUIParamsError("connectionId, database and 1-100 tables are required")
	}
	tables := make([]string, 0, len(request.Tables))
	for _, table := range request.Tables {
		if !validDatabaseSchemaName(table) {
			return nil, hostUIParamsError("table names must be printable")
		}
		if !slices.Contains(tables, table) {
			tables = append(tables, table)
		}
	}
	if rpcErr := authorizeDatabaseSchema(runtimeHost, connectionID, request.TimeoutMs); rpcErr != nil {
		return nil, rpcErr
	}
	described, err := databaseSchemaDescribe(runtimeHost.promptContext(), connectionID, database, tables)
	if err != nil {
		return nil, databaseSchemaError(err)
	}
	result := map[string]any{"tables": described}
	if raw, _ := json.Marshal(result); len(raw) > databaseSchemaMaxResponse {
		return nil, &rpcError{Code: -32000, Message: "the schema is too large; describe fewer tables at a time", Data: map[string]string{"code": "too_large"}}
	}
	return result, nil
}

// databaseSchemaGrantsGetHandler lists the connections a plugin may read
// without asking, with names for display (deleted connections keep the id).
func databaseSchemaGrantsGetHandler(_, args string) (string, error) {
	var input struct {
		ID string `json:"id"`
	}
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	return core.EncodeResult(databaseSchemaGrantsView(readDatabaseSchemaGrants(strings.TrimSpace(input.ID))))
}

type databaseSchemaGrantView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

func databaseSchemaGrantsView(grants databaseSchemaGrants) map[string]any {
	connections, _ := databaseSchemaConnections()
	out := make([]databaseSchemaGrantView, 0, len(grants.Connections))
	for _, id := range grants.Connections {
		view := databaseSchemaGrantView{ID: id, Name: id}
		if index := slices.IndexFunc(connections, func(item db.SchemaConnection) bool { return item.ID == id }); index >= 0 {
			view.Name, view.Kind = connections[index].Name, connections[index].Kind
		}
		out = append(out, view)
	}
	return map[string]any{"connections": out}
}

// databaseSchemaGrantsRevokeHandler removes one connection ("" removes all).
// Only the desktop UI calls it; plugins cannot change their own grants.
func databaseSchemaGrantsRevokeHandler(_, args string) (string, error) {
	var input struct {
		ID           string `json:"id"`
		ConnectionID string `json:"connectionId"`
	}
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	id := strings.TrimSpace(input.ID)
	connectionID := strings.TrimSpace(input.ConnectionID)
	revokeDatabaseSchemaRun(id, connectionID)
	databaseSchemaGrantsMu.Lock()
	defer databaseSchemaGrantsMu.Unlock()
	grants := readDatabaseSchemaGrants(id)
	if connectionID != "" {
		grants.Connections = slices.DeleteFunc(grants.Connections, func(item string) bool { return item == connectionID })
	} else {
		grants.Connections = nil
	}
	if err := writeDatabaseSchemaGrants(id, grants); err != nil {
		return "", err
	}
	return core.EncodeResult(databaseSchemaGrantsView(grants))
}
