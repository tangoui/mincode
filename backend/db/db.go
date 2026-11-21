// DB 域命令注册层，对齐 Tauri src-tauri/src/db/*。
// 参数与返回均为 camelCase JSON。会话由本包内 registry 管理。
//
// 命令契约：
//
//	db_connect               {config}                                      -> string(sessionId)
//	db_detect_file_type      {filePath}                                    -> FileDatabaseInfo
//	db_disconnect            {sessionId}                                    -> void
//	db_execute_query         {sessionId, query, database?, queryId?, checkId?, confirmText?, source?} -> QueryResult
//	db_execute_query_stream  {sessionId, query, database?, queryId, checkId?, confirmText?, source?}  -> QueryStreamSummary
//	                         （非只读语句必须带 checkId，否则 sqlsafety:checkRequired；见 safety_execute.go）
//	db_cancel_query          {queryId}                                     -> bool
//	db_safety_check          {sessionId, query, database?, source?}        -> sqlsafety.Check
//	                         {sessionId, operation, database?, source?}    （不传 query 时按 operation 检查，见 safety_operations.go）
//	db_safety_history_list / db_safety_history_get / db_safety_undo_preview / db_safety_undo /
//	db_safety_settings_get / db_safety_settings_set / db_safety_purge_connection（见 safety_undo.go）
//	db_get_schema            {sessionId}                                    -> DbSchemaInfo
//	db_get_navigation_schema {sessionId}                                    -> DbSchemaInfo
//	db_get_database_tables   {sessionId, database}                          -> [SchemaTable]
//	db_get_table_columns     {sessionId, database, table}                  -> [ColumnInfo]
//	db_get_primary_keys      {sessionId, database, table}                  -> PrimaryKeyInfo
//	db_get_table_rows        {sessionId, database, table, limit?, offset?} -> QueryResult
//	db_get_table_ddl         {sessionId, database, table}                  -> string
//	db_list_indexes          {sessionId, database, table}                  -> [DbIndexInfo]
//	db_list_foreign_keys     {sessionId, database, table}                  -> [DbForeignKeyInfo]
//	db_list_triggers         {sessionId, database, table}                  -> [DbTriggerInfo]
//	db_destructive_operation {sessionId, action, database, table?, confirmation, checkId?, confirmText?} -> void
//	db_update_cell           {sessionId, database, table, primaryKey, column, value, source?, checkId?, confirmText?} -> number
//	db_insert_row            {sessionId, database, table, fields, source?, checkId?, confirmText?}  -> number
//	db_delete_row            {sessionId, database, table, primaryKey, source?, checkId?, confirmText?} -> number
//	db_apply_changes         {sessionId, database, table, changes, checkId?, confirmText?, namespace?, source?} -> {applied, recordId?}
//	                         （单行命令与整组提交都按整组留底，见 safety_rows.go）
//	db_export_table          {sessionId, database, table, format, limit?}  -> string
//	db_import_data           {sessionId, database, table, format, data, checkId?, confirmText?} -> number
//	db_execute_schema_sync   {…, selectedChangeIds, checkId?, confirmText?} -> SchemaSyncExecutionResult
//	                         （以上与 db_data_import_start、redis 改键命令：插件以外的会话必须带 checkId，
//	                         否则 sqlsafety:checkRequired；令牌按 operation 绑定，见 safety_operations.go）
//	db_check_odbc_drivers                                                  -> [OdbcDriverStatus]
//	db_detect_install_env                                                  -> InstallEnvInfo
//	redis_get_key            {sessionId, dbIndex, key}                      -> RedisKeyInfo
//	redis_set_key            {sessionId, dbIndex, key, keyType, value, checkId?, confirmText?} -> RedisKeyValue
//	redis_add_key            {sessionId, dbIndex, key, keyType, value, checkId?, confirmText?} -> RedisKeyValue
//	redis_delete_key         {sessionId, dbIndex, key, checkId?, confirmText?} -> RedisDeleteResult
//	redis_set_ttl            {sessionId, dbIndex, key, ttlSeconds, checkId?, confirmText?} -> RedisKeyValue
//	redis_get_ttl            {sessionId, dbIndex, key}                      -> RedisTtlInfo
//	redis_list_keys          {sessionId, dbIndex, pattern, cursor, count}   -> RedisKeyListResult
package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"mincode/app/backend/core"
	"mincode/app/backend/crypto"
	"mincode/app/backend/tasks"
)

func dbSupportedTypesHandler(_, _ string) (string, error) {
	return core.EncodeResult(supportedDatabaseTypes)
}

func init() {
	core.Register("db_supported_types", dbSupportedTypesHandler)
	core.Register("db_connect", dbConnectHandler)
	core.Register("db_disconnect", dbDisconnectHandler)
	core.RegisterCtx("db_execute_query", dbExecuteQueryHandler)
	core.RegisterCtx("db_execute_query_stream", dbExecuteQueryStreamHandler)
	core.Register("db_cancel_query", dbCancelQueryHandler)
	core.RegisterCtx("db_safety_check", dbSafetyCheckHandler)
	core.Register("db_safety_history_list", dbSafetyHistoryListHandler)
	core.Register("db_safety_history_get", dbSafetyHistoryGetHandler)
	core.RegisterCtx("db_safety_undo_preview", dbSafetyUndoPreviewHandler)
	core.RegisterCtx("db_safety_undo", dbSafetyUndoHandler)
	core.Register("db_safety_settings_get", dbSafetySettingsGetHandler)
	core.Register("db_safety_settings_set", dbSafetySettingsSetHandler)
	core.Register("db_safety_purge_connection", dbSafetyPurgeConnectionHandler)
	core.Register("db_get_schema", dbGetSchemaHandler)
	core.Register("db_get_navigation_schema", dbGetNavigationSchemaHandler)
	core.Register("db_get_database_tables", dbGetDatabaseTablesHandler)
	core.Register("db_get_table_columns", dbGetTableColumnsHandler)
	core.Register("db_get_primary_keys", dbGetPrimaryKeysHandler)
	core.Register("db_get_table_rows", dbGetTableRowsHandler)
	core.Register("db_vector_create_collection", dbVectorCreateCollectionHandler)
	core.Register("db_get_table_ddl", dbGetTableDDLHandler)
	core.Register("db_list_indexes", dbListIndexesHandler)
	core.Register("db_list_foreign_keys", dbListForeignKeysHandler)
	core.Register("db_list_triggers", dbListTriggersHandler)
	core.Register("db_destructive_operation", dbDestructiveOperationHandler)
	core.Register("db_compare_schema", dbCompareSchemaHandler)
	core.Register("db_generate_schema_sync", dbGenerateSchemaSyncHandler)
	core.Register("db_execute_schema_sync", dbExecuteSchemaSyncHandler)
	core.Register("db_update_cell", dbUpdateCellHandler)
	core.Register("db_insert_row", dbInsertRowHandler)
	core.Register("db_delete_row", dbDeleteRowHandler)
	core.RegisterCtx("db_apply_changes", dbApplyChangesHandler)
	core.Register("db_export_table", dbExportTableHandler)
	core.RegisterCtx("db_import_data", dbImportDataHandler)
	core.Register("db_check_odbc_drivers", dbCheckOdbcDriversHandler)
	core.Register("db_detect_install_env", dbDetectInstallEnvHandler)
	core.Register("redis_get_key", redisGetKeyHandler)
	core.Register("redis_set_key", redisSetKeyHandler)
	core.Register("redis_add_key", redisAddKeyHandler)
	core.Register("redis_delete_key", redisDeleteKeyHandler)
	core.Register("redis_set_ttl", redisSetTTLHandler)
	core.Register("redis_get_ttl", redisGetTTLHandler)
	core.Register("redis_list_keys", redisListKeysHandler)
}

// Shutdown 取消活跃查询并关闭全部数据库会话；可重复调用。
func Shutdown() {
	cancelsMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(activeCancels))
	for id, cancel := range activeCancels {
		cancels = append(cancels, cancel)
		delete(activeCancels, id)
	}
	cancelsMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, session := range registry.drain() {
		session.close()
	}
}

// ── 查询取消：queryId -> cancel ──

var (
	cancelsMu     sync.Mutex
	activeCancels = map[string]context.CancelFunc{}
)

// Startup 保留数据库域的生命周期入口。数据库会话由用户显式断开或应用退出时释放，
// 不再按固定闲置时长自动回收，避免后台工作区中的连接无提示消失。启动时在后台按
// 保留设置清理一次 SQL 安全网的过期留底，不阻塞首个数据库命令。
func Startup(_ context.Context) {
	go pruneSafetyRecords()
}

func registerCancel(queryID string, cancel context.CancelFunc) {
	if queryID == "" {
		return
	}
	cancelsMu.Lock()
	activeCancels[queryID] = cancel
	cancelsMu.Unlock()
}

func unregisterCancel(queryID string) {
	if queryID == "" {
		return
	}
	cancelsMu.Lock()
	delete(activeCancels, queryID)
	cancelsMu.Unlock()
}

// requireSession 取会话，不存在则报错。
func requireSession(sessionID string) (*dbSession, error) {
	s, ok := registry.get(sessionID)
	if !ok {
		return nil, fmt.Errorf("会话不存在或已断开")
	}
	return s, nil
}

// ── 会话生命周期 ──

type connectArgs struct {
	Config DbConnectionConfig `json:"config"`
}

func dbConnectHandler(_, argsJSON string) (string, error) {
	var a connectArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	cfg := a.Config
	cfg.ConnectionID = strings.TrimSpace(cfg.ConnectionID)
	if len(cfg.ConnectionID) > 128 {
		return "", fmt.Errorf("connectionId 过长")
	}
	pwd, err := crypto.DecryptOptional(cfg.Password)
	if err != nil {
		return "", err
	}
	cfg.Password = pwd
	tok, err := crypto.DecryptOptional(cfg.Token)
	if err != nil {
		return "", err
	}
	cfg.Token = tok

	start := time.Now()
	s, err := buildSession(&cfg)
	// Only log driver type and duration; connection settings and errors may contain secrets.
	slog.Debug("database connect", "type", cfg.DbType, "duration_ms", time.Since(start).Milliseconds(), "success", err == nil)
	if err != nil {
		return "", err
	}
	s.connectionID = cfg.ConnectionID
	sessionID := uuid.NewString()
	registry.put(sessionID, s)
	return core.EncodeResult(sessionID)
}

type sessionIDArgs struct {
	SessionID string `json:"sessionId"`
}

func dbDisconnectHandler(_, argsJSON string) (string, error) {
	var a sessionIDArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	registry.remove(a.SessionID)
	return core.EncodeResult(nil)
}

// ── 查询执行 ──

type executeQueryArgs struct {
	SessionID string  `json:"sessionId"`
	Query     string  `json:"query"`
	Database  *string `json:"database"`
	QueryID   *string `json:"queryId"`
	// SQL 安全网：非只读语句必须带 db_safety_check 签发的 checkId；高风险时
	// confirmText 必须与检查结果的 confirmTarget 一致。source 记入操作记录。
	CheckID     *string `json:"checkId"`
	ConfirmText *string `json:"confirmText"`
	Source      string  `json:"source"`
}

// runNonSQLQuery 在非 SQL 会话（HTTP 数据源、向量检索、Cassandra、Neo4j、MongoDB）上
// 执行查询窗口文本；调用方负责先做只读或确认检查。
func runNonSQLQuery(ctx context.Context, s *dbSession, query string, database *string) (*QueryResult, error) {
	switch {
	case s.kind == kindHTTP:
		switch s.httpKind {
		case httpInflux:
			return influxExecuteQuery(ctx, s, query)
		case httpElastic:
			return elasticsearchExecuteQuery(ctx, s, query)
		case httpCouch:
			return couchExecuteQuery(ctx, s, query)
		case httpMeili:
			return meilisearchExecuteQuery(ctx, s, query)
		case httpDatabricks, httpBigQuery, httpDremio, httpKylin, httpPinot, httpSnowflake:
			return warehouseExecuteQuery(ctx, s, query)
		}
	case s.vector != nil:
		return vectorExecuteQuery(ctx, s, query, database)
	case s.kind == kindCQL:
		return cassandraExecuteQuery(ctx, s, query)
	case s.kind == kindNeo4j:
		return neo4jExecuteQuery(ctx, s, query)
	case s.kind == kindMongo:
		return mongoExecuteQuery(ctx, s, query)
	}
	return nil, fmt.Errorf("该会话类型不支持查询")
}

func dbExecuteQueryHandler(callCtx context.Context, _, argsJSON string) (string, error) {
	var a executeQueryArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	if s.kind != kindSQL && s.kind != kindHTTP && s.kind != kindCQL && s.kind != kindNeo4j && s.kind != kindMongo {
		if s.kind != kindVector {
			return "", fmt.Errorf("该会话类型不支持查询")
		}
	}
	if nonSQLQuerySession(s) {
		if err := requireNonSQLCheck(s, a); err != nil {
			return "", err
		}
	}
	ctx, cancel := context.WithTimeout(callCtx, timeoutForQuery(a.Query))
	defer cancel()
	queryID := ""
	if a.QueryID != nil {
		queryID = *a.QueryID
	}
	if queryID != "" {
		registerCancel(queryID, cancel)
		defer unregisterCancel(queryID)
	}
	if nonSQLQuerySession(s) {
		res, err := runNonSQLQuery(ctx, s, a.Query, a.Database)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(res)
	}
	res, err := executeSQLQuery(ctx, s, a)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

func dbExecuteQueryStreamHandler(callCtx context.Context, _, argsJSON string) (string, error) {
	var a executeQueryArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if a.QueryID == nil || *a.QueryID == "" {
		return "", fmt.Errorf("流式查询需要 queryId")
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	if s.kind != kindSQL {
		return "", fmt.Errorf("该会话类型暂不支持流式查询")
	}
	ctx, cancel := context.WithTimeout(callCtx, timeoutForQuery(a.Query))
	defer cancel()
	queryID := *a.QueryID
	registerCancel(queryID, cancel)
	defer unregisterCancel(queryID)
	if !readPath(s, a) {
		// Writes run guarded; their result is emitted once they finish.
		res, err := executeSQLQuery(ctx, s, a)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(emitGuardedResult(queryID, res))
	}
	emitted := 0
	err = readHookError(s, a.Query)
	var summary *QueryStreamSummary
	if err == nil {
		summary, err = streamSQLQuery(ctx, s, a.Query, a.Database, queryID, dbStreamChunkRows, dbStreamChunkBytes, func(chunk QueryResultChunk) error {
			emitQueryChunk(chunk)
			emitted++
			return ctx.Err()
		})
	}
	if err != nil {
		if emitted == 0 && isReadOnlyViolation(s.dialect, err) {
			return "", forceWriteRequired(s, a)
		}
		return "", err
	}
	return core.EncodeResult(summary)
}

type cancelQueryArgs struct {
	QueryID string `json:"queryId"`
}

func dbCancelQueryHandler(_, argsJSON string) (string, error) {
	var a cancelQueryArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	cancelsMu.Lock()
	cancel, ok := activeCancels[a.QueryID]
	if ok {
		delete(activeCancels, a.QueryID)
	}
	cancelsMu.Unlock()
	if ok {
		cancel()
	}
	return core.EncodeResult(ok)
}

type destructiveOperationArgs struct {
	SessionID    string  `json:"sessionId"`
	Action       string  `json:"action"`
	Database     string  `json:"database"`
	Table        *string `json:"table"`
	Confirmation string  `json:"confirmation"`
	// SQL 安全网：db_safety_check operation {kind: "destructive", action, table} 签发的令牌。
	CheckID     *string `json:"checkId"`
	ConfirmText *string `json:"confirmText"`
}

func dbDestructiveOperationHandler(_, argsJSON string) (string, error) {
	var a destructiveOperationArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	op := safetyOperation{Kind: "destructive", Action: a.Action, Table: optionalString(a.Table)}
	check, plan, err := consumeCheck(s, a.CheckID, a.ConfirmText, strings.TrimSpace(a.Database), operationHash(op))
	if err != nil {
		return "", err
	}
	guard := &destructiveGuard{check: check, plan: plan}
	if err := runDestructiveOperation(context.Background(), s, guard, a.Action, a.Database, a.Table, a.Confirmation); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ── Schema / 表结构 ──

func dbGetSchemaHandler(_, argsJSON string) (string, error) {
	var a sessionIDArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	info, err := getSchema(ctx, s)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(info)
}

func dbGetNavigationSchemaHandler(_, argsJSON string) (string, error) {
	var a sessionIDArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	start := time.Now()
	info, err := getNavigationSchema(ctx, s)
	slog.Debug("database navigation schema", "kind", s.kind, "dialect", s.dialect, "duration_ms", time.Since(start).Milliseconds(), "success", err == nil)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(info)
}

type databaseArgs struct {
	SessionID string `json:"sessionId"`
	Database  string `json:"database"`
}

func dbGetDatabaseTablesHandler(_, argsJSON string) (string, error) {
	var a databaseArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	tables, err := getDatabaseTables(ctx, s, a.Database)
	if err != nil {
		return "", err
	}
	if tables == nil {
		tables = []SchemaTable{}
	}
	return core.EncodeResult(tables)
}

type tableArgs struct {
	SessionID string `json:"sessionId"`
	Database  string `json:"database"`
	Table     string `json:"table"`
}

func dbGetTableColumnsHandler(_, argsJSON string) (string, error) {
	var a tableArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	cols, err := getTableColumns(ctx, s, a.Database, a.Table)
	if err != nil {
		return "", err
	}
	if cols == nil {
		cols = []ColumnInfo{}
	}
	return core.EncodeResult(cols)
}

func dbGetPrimaryKeysHandler(_, argsJSON string) (string, error) {
	var a tableArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	pk, err := getPrimaryKeys(ctx, s, a.Database, a.Table)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(pk)
}

type tableRowsArgs struct {
	SessionID string  `json:"sessionId"`
	Database  string  `json:"database"`
	Table     string  `json:"table"`
	Limit     *uint32 `json:"limit"`
	Offset    *uint32 `json:"offset"`
	PageToken *string `json:"pageToken"`
	Namespace *string `json:"namespace"`
}

func dbGetTableRowsHandler(_, argsJSON string) (string, error) {
	var a tableRowsArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	var limit, offset uint32
	if a.Limit != nil {
		limit = *a.Limit
	}
	if a.Offset != nil {
		offset = *a.Offset
	}
	res, err := getTableRowsPage(ctx, s, a.Database, a.Table, limit, offset, optionalString(a.PageToken), optionalString(a.Namespace))
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type vectorCreateCollectionArgs struct {
	SessionID  string         `json:"sessionId"`
	Name       string         `json:"name"`
	Dimension  int            `json:"dimension"`
	Metric     string         `json:"metric"`
	Cloud      string         `json:"cloud"`
	Region     string         `json:"region"`
	VectorName string         `json:"vectorName"`
	Options    map[string]any `json:"options"`
}

func dbVectorCreateCollectionHandler(_, argsJSON string) (string, error) {
	var a vectorCreateCollectionArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(dbDMLTimeoutSecs)*time.Second)
	defer cancel()
	if err := vectorCreateCollection(ctx, s, vectorCreateCollectionRequest{
		Name: a.Name, Dimension: a.Dimension, Metric: a.Metric, Cloud: a.Cloud,
		Region: a.Region, VectorName: a.VectorName, Options: a.Options,
	}); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func dbGetTableDDLHandler(_, argsJSON string) (string, error) {
	var a tableArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	ddl, err := getTableDDL(ctx, s, a.Database, a.Table)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(ddl)
}

func dbListIndexesHandler(_, argsJSON string) (string, error) {
	var a tableArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	idx, err := listIndexes(ctx, s, a.Database, a.Table)
	if err != nil {
		return "", err
	}
	if idx == nil {
		idx = []DbIndexInfo{}
	}
	return core.EncodeResult(idx)
}

type schemaCompareArgs struct {
	SourceSessionID string `json:"sourceSessionId"`
	SourceDatabase  string `json:"sourceDatabase"`
	TargetSessionID string `json:"targetSessionId"`
	TargetDatabase  string `json:"targetDatabase"`
}

type schemaSyncArgs struct {
	schemaCompareArgs
	SourceFingerprint string   `json:"sourceFingerprint"`
	TargetFingerprint string   `json:"targetFingerprint"`
	SelectedChangeIDs []string `json:"selectedChangeIds"`
	// SQL 安全网（仅执行）：db_safety_check operation {kind: "schemaSync", …} 签发的令牌，
	// sessionId 为目标会话、database 为目标库。
	CheckID     *string `json:"checkId"`
	ConfirmText *string `json:"confirmText"`
}

func requireSchemaSyncSessions(a schemaCompareArgs) (*dbSession, *dbSession, error) {
	source, err := requireSession(a.SourceSessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("源数据库会话: %w", err)
	}
	target, err := requireSession(a.TargetSessionID)
	if err != nil {

		return nil, nil, fmt.Errorf("目标数据库会话: %w", err)
	}
	if source == target || a.SourceSessionID == a.TargetSessionID {

		return nil, nil, fmt.Errorf("源连接和目标连接不能相同")
	}
	return source, target, nil
}

func dbCompareSchemaHandler(_, argsJSON string) (string, error) {
	var a schemaCompareArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	source, target, err := requireSchemaSyncSessions(a)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	comparison, err := compareSchemaSessions(ctx, source, a.SourceDatabase, target, a.TargetDatabase)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(comparison)
}

func dbGenerateSchemaSyncHandler(_, argsJSON string) (string, error) {
	var a schemaSyncArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	source, target, err := requireSchemaSyncSessions(a.schemaCompareArgs)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	comparison, err := compareSchemaSessions(ctx, source, a.SourceDatabase, target, a.TargetDatabase)
	if err != nil {
		return "", err
	}
	if comparison.Source.Fingerprint != a.SourceFingerprint {
		return "", fmt.Errorf("源结构已变化，请重新对比后再生成 SQL")
	}
	if comparison.Target.Fingerprint != a.TargetFingerprint {
		return "", fmt.Errorf("目标结构已变化，请重新对比后再生成 SQL")
	}
	plan, err := generateSchemaSyncPlan(comparison.Target.Dialect, a.TargetDatabase, comparison.Changes, a.SelectedChangeIDs, a.SourceFingerprint, a.TargetFingerprint)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(plan)
}

func dbExecuteSchemaSyncHandler(_, argsJSON string) (string, error) {
	var a schemaSyncArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	source, target, err := requireSchemaSyncSessions(a.schemaCompareArgs)
	if err != nil {
		return "", err
	}

	check, plan, err := consumeCheck(target, a.CheckID, a.ConfirmText, strings.TrimSpace(a.TargetDatabase), operationHash(schemaSyncOperation(a)))
	if err != nil {
		return "", err
	}
	guard := &schemaSyncGuard{target: target, check: check, plan: plan}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("ALTER TABLE"))
	defer cancel()
	result, err := executeSchemaSync(ctx, source, a.SourceDatabase, target, a.TargetDatabase, a.SourceFingerprint, a.TargetFingerprint, a.SelectedChangeIDs, guard)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func dbListForeignKeysHandler(_, argsJSON string) (string, error) {
	var a tableArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	fks, err := listForeignKeys(ctx, s, a.Database, a.Table)
	if err != nil {
		return "", err
	}
	if fks == nil {
		fks = []DbForeignKeyInfo{}
	}
	return core.EncodeResult(fks)
}

func dbListTriggersHandler(_, argsJSON string) (string, error) {
	var a tableArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	trs, err := listTriggers(ctx, s, a.Database, a.Table)
	if err != nil {
		return "", err
	}
	if trs == nil {
		trs = []DbTriggerInfo{}
	}
	return core.EncodeResult(trs)
}

// ── 行编辑 ──

type updateCellArgs struct {
	SessionID   string         `json:"sessionId"`
	Database    string         `json:"database"`
	Table       string         `json:"table"`
	PrimaryKey  map[string]any `json:"primaryKey"`
	Column      string         `json:"column"`
	Value       any            `json:"value"`
	Namespace   string         `json:"namespace"`
	Source      string         `json:"source"`
	CheckID     *string        `json:"checkId"`
	ConfirmText *string        `json:"confirmText"`
}

// The single-row commands run as change groups of one (safety_rows.go):
// backed up like the grid, no checkId needed — except to resubmit after a
// sqlsafety:reconfirm, with the check it carries.
func dbUpdateCellHandler(_, argsJSON string) (string, error) {
	var a updateCellArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("UPDATE"))
	defer cancel()
	if len(a.PrimaryKey) == 0 {
		return "", fmt.Errorf("db_update_cell requires primaryKey")
	}
	n, err := applySingleRow(ctx, s, a.Database, a.Table, a.Namespace, a.Source, a.CheckID, a.ConfirmText,
		rowChange{Op: rowOpUpdate, PrimaryKey: a.PrimaryKey, Values: map[string]any{a.Column: a.Value}})
	if err != nil {
		return "", err
	}
	return core.EncodeResult(n)
}

type insertRowArgs struct {
	SessionID   string         `json:"sessionId"`
	Database    string         `json:"database"`
	Table       string         `json:"table"`
	Fields      map[string]any `json:"fields"`
	Namespace   string         `json:"namespace"`
	Source      string         `json:"source"`
	CheckID     *string        `json:"checkId"`
	ConfirmText *string        `json:"confirmText"`
}

func dbInsertRowHandler(_, argsJSON string) (string, error) {
	var a insertRowArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("INSERT"))
	defer cancel()
	n, err := applySingleRow(ctx, s, a.Database, a.Table, a.Namespace, a.Source, a.CheckID, a.ConfirmText, rowChange{Op: rowOpInsert, Values: a.Fields})
	if err != nil {
		return "", err
	}
	return core.EncodeResult(n)
}

type deleteRowArgs struct {
	SessionID   string         `json:"sessionId"`
	Database    string         `json:"database"`
	Table       string         `json:"table"`
	PrimaryKey  map[string]any `json:"primaryKey"`
	Namespace   string         `json:"namespace"`
	Source      string         `json:"source"`
	CheckID     *string        `json:"checkId"`
	ConfirmText *string        `json:"confirmText"`
}

func dbDeleteRowHandler(_, argsJSON string) (string, error) {
	var a deleteRowArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("DELETE"))
	defer cancel()
	if len(a.PrimaryKey) == 0 {
		return "", fmt.Errorf("db_delete_row requires primaryKey")
	}
	n, err := applySingleRow(ctx, s, a.Database, a.Table, a.Namespace, a.Source, a.CheckID, a.ConfirmText, rowChange{Op: rowOpDelete, PrimaryKey: a.PrimaryKey})
	if err != nil {
		return "", err
	}
	return core.EncodeResult(n)
}

// ── 导出 / 导入 ──

type exportTableArgs struct {
	SessionID string  `json:"sessionId"`
	Database  string  `json:"database"`
	Table     string  `json:"table"`
	Format    string  `json:"format"`
	Mode      string  `json:"mode"`
	Limit     *uint32 `json:"limit"`
}

func dbExportTableHandler(_, argsJSON string) (string, error) {
	var a exportTableArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeoutForQuery("SELECT"))
	defer cancel()
	var limit uint32
	if a.Limit != nil {
		limit = *a.Limit
	}
	out, err := exportTable(ctx, s, a.Database, a.Table, a.Format, a.Mode, limit)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(out)
}

type importDataArgs struct {
	SessionID string `json:"sessionId"`
	Database  string `json:"database"`
	Table     string `json:"table"`
	Format    string `json:"format"`
	Data      string `json:"data"`
	// SQL 安全网：db_safety_check operation {kind: "import", table, format, data} 签发的令牌。
	CheckID     *string `json:"checkId"`
	ConfirmText *string `json:"confirmText"`
}

func dbImportDataHandler(ctx context.Context, _, argsJSON string) (string, error) {
	var a importDataArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	_, _, err = consumeCheck(s, a.CheckID, a.ConfirmText, strings.TrimSpace(a.Database), operationHash(importOperation(a.Table, a.Format, a.Data)))

	if err != nil {
		return "", err
	}
	id, err := prepareTableImport(ctx, importPrepareRequest{SessionID: a.SessionID, Database: a.Database, Table: a.Table, Format: a.Format, Data: a.Data})
	if err != nil {
		return "", err
	}
	task, err := submitTableImport(ctx, id, id)
	if err != nil {
		return "", err
	}
	m, _, err := databaseTransferManager()
	if err != nil {
		return "", err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := m.Get(context.WithoutCancel(ctx), task.ID)
		if err != nil {
			return "", err
		}
		if snapshot.State.Terminal() {
			if snapshot.State != tasks.StateSucceeded {
				return "", fmt.Errorf("import_task_failed: %s", snapshot.ID)
			}
			return core.EncodeResult(snapshot.Result.Records)
		}
		select {
		case <-ctx.Done():
			_, _ = m.Cancel(context.Background(), task.ID)
			return "", fmt.Errorf("import_task_cancel_requested: %s", task.ID)
		case <-ticker.C:
		}
	}
}

// ── 驱动检测 / 安装 ──

func dbCheckOdbcDriversHandler(_, _ string) (string, error) {
	return core.EncodeResult(checkODBCDrivers())
}

func dbDetectInstallEnvHandler(_, _ string) (string, error) {
	return core.EncodeResult(detectInstallEnv())
}

// ── Redis ──

type redisKeyArgs struct {
	SessionID string `json:"sessionId"`
	DBIndex   int64  `json:"dbIndex"`
	Key       string `json:"key"`
	// SQL 安全网（仅 redis_delete_key）：operation {kind: "redis", action: "delete", dbIndex, keys: [key]}。
	CheckID     *string `json:"checkId"`
	ConfirmText *string `json:"confirmText"`
}

func redisOperationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), redisOperationTimeout)
}

func redisGetKeyHandler(_, argsJSON string) (string, error) {
	var a redisKeyArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := redisOperationContext()
	defer cancel()
	info, err := redisGetKey(ctx, s, a.DBIndex, a.Key)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(info)
}

type redisSetKeyArgs struct {
	SessionID string `json:"sessionId"`
	DBIndex   int64  `json:"dbIndex"`
	Key       string `json:"key"`
	KeyType   string `json:"keyType"`
	Value     any    `json:"value"`
	// SQL 安全网：operation {kind: "redis", action: "set"（redis_set_key）| "add"（redis_add_key）, dbIndex, keys: [key]}。
	CheckID     *string `json:"checkId"`
	ConfirmText *string `json:"confirmText"`
}

func redisSetKeyHandler(_, argsJSON string) (string, error) {
	var a redisSetKeyArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	if err := requireRedisCheck(s, a.CheckID, a.ConfirmText, "set", a.DBIndex, a.Key); err != nil {
		return "", err
	}
	ctx, cancel := redisOperationContext()
	defer cancel()
	res, err := redisSetKey(ctx, s, a.DBIndex, a.Key, a.KeyType, a.Value)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

func redisAddKeyHandler(_, argsJSON string) (string, error) {
	var a redisSetKeyArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	if err := requireRedisCheck(s, a.CheckID, a.ConfirmText, "add", a.DBIndex, a.Key); err != nil {
		return "", err
	}
	ctx, cancel := redisOperationContext()
	defer cancel()
	res, err := redisAddKey(ctx, s, a.DBIndex, a.Key, a.KeyType, a.Value)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

func redisDeleteKeyHandler(_, argsJSON string) (string, error) {
	var a redisKeyArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	if err := requireRedisCheck(s, a.CheckID, a.ConfirmText, "delete", a.DBIndex, a.Key); err != nil {
		return "", err
	}
	ctx, cancel := redisOperationContext()
	defer cancel()
	res, err := redisDeleteKey(ctx, s, a.DBIndex, a.Key)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type redisSetTTLArgs struct {
	SessionID  string `json:"sessionId"`
	DBIndex    int64  `json:"dbIndex"`
	Key        string `json:"key"`
	TTLSeconds int64  `json:"ttlSeconds"`
	// SQL 安全网：operation {kind: "redis", action: "ttl", dbIndex, keys: [key]}。
	CheckID     *string `json:"checkId"`
	ConfirmText *string `json:"confirmText"`
}

func redisSetTTLHandler(_, argsJSON string) (string, error) {
	var a redisSetTTLArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	if err := requireRedisCheck(s, a.CheckID, a.ConfirmText, "ttl", a.DBIndex, a.Key); err != nil {
		return "", err
	}
	ctx, cancel := redisOperationContext()
	defer cancel()
	res, err := redisSetTTL(ctx, s, a.DBIndex, a.Key, a.TTLSeconds)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

func redisGetTTLHandler(_, argsJSON string) (string, error) {
	var a redisKeyArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := redisOperationContext()
	defer cancel()
	res, err := redisGetTTL(ctx, s, a.DBIndex, a.Key)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}

type redisListKeysArgs struct {
	SessionID string `json:"sessionId"`
	DBIndex   int64  `json:"dbIndex"`
	Pattern   string `json:"pattern"`
	Cursor    string `json:"cursor"`
	Count     int64  `json:"count"`
}

func redisListKeysHandler(_, argsJSON string) (string, error) {
	var a redisListKeysArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	s, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}

	ctx, cancel := redisOperationContext()
	defer cancel()
	res, err := redisListKeys(ctx, s, a.DBIndex, a.Pattern, a.Cursor, a.Count)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(res)
}
