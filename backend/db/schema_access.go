package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Read-only schema access for plugins (capability database.schema). It only
// returns metadata (databases, tables, columns, keys, indexes, comments) of
// the user's saved SQL connections; hosts, users and credentials never leave
// this package, and no SQL text from the caller is executed.

const (
	SchemaDescribeMaxTables = 100
	schemaAccessTimeout     = 2 * time.Minute
)

// schemaSQLTypes are the built-in SQL engines whose metadata readers return
// tables and columns. Other types (key-value, document, search, vector and
// HTTP warehouses) have no relational schema to generate code from.
var schemaSQLTypes = map[string]bool{
	"mysql": true, "mariadb": true, "tidb": true, "oceanbase": true, "polardb": true, "tdsql": true,
	"dolt": true, "greatsql": true,
	"gbase": true, "gbase8t": true, "goldendb": true, "starrocks": true, "doris": true, "selectdb": true, "databend": true,
	"postgres": true, "kingbase": true, "gaussdb": true, "opengauss": true, "highgo": true, "vastbase": true, "opentenbase": true,
	"gbase8c": true, "cockroachdb": true, "yugabyte": true, "questdb": true, "materialize": true, "risingwave": true,
	"greenplum": true, "cloudberry": true, "redshift": true, "pgvector": true,
	"sqlite": true, "duckdb": true, "sqlserver": true, "oracle": true, "dameng": true, "yashandb": true,
	"firebird": true, "clickhouse": true, "tdengine": true, "trino": true, "presto": true,
}

var schemaTypeAliases = map[string]string{
	"postgresql": "postgres", "cockroach": "cockroachdb", "yugabytedb": "yugabyte", "dm": "dameng",
	"taos": "tdengine", "sqlite3": "sqlite", "mssql": "sqlserver", "yasdb": "yashandb", "firebirdsql": "firebird",
}

// SchemaConnection is a saved connection as a plugin sees it: no host, user
// or credentials.
type SchemaConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Database string `json:"database,omitempty"`
}

type SchemaTableSummary struct {
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	Comment string `json:"comment,omitempty"`
}

type SchemaTableListing struct {
	Databases []string             `json:"databases"`
	Database  string               `json:"database"`
	Tables    []SchemaTableSummary `json:"tables"`
}

type SchemaColumnDescription struct {
	Name          string  `json:"name"`
	DataType      string  `json:"dataType"`
	Nullable      bool    `json:"nullable"`
	PrimaryKey    bool    `json:"primaryKey"`
	AutoIncrement bool    `json:"autoIncrement"`
	DefaultValue  *string `json:"defaultValue,omitempty"`
	Comment       string  `json:"comment,omitempty"`
}

type SchemaTableDescription struct {
	Name        string                    `json:"name"`
	Comment     string                    `json:"comment,omitempty"`
	Columns     []SchemaColumnDescription `json:"columns"`
	PrimaryKey  []string                  `json:"primaryKey"`
	Indexes     []DbIndexInfo             `json:"indexes"`
	ForeignKeys []DbForeignKeyInfo        `json:"foreignKeys"`
	// Error is set when this table could not be read; the others still are.
	Error string `json:"error,omitempty"`
}

type schemaSavedConnection struct {
	backupSavedConnection
	Tunnel any `json:"tunnel"`
}

// Test seams.
var (
	loadSchemaSavedConnections = func() ([]schemaSavedConnection, error) {
		var connections []schemaSavedConnection
		if err := readAppKVJSON(savedConnectionsKey, &connections); err != nil {
			return nil, fmt.Errorf("读取已保存连接: %w", err)
		}
		return connections, nil
	}
	openSchemaSession = func(ctx context.Context, saved *backupSavedConnection) (*dbSession, error) {
		return openBackupSession(ctx, saved)
	}
)

// schemaPluginDialects are plugin-driver sqlDialects with a relational
// schema; "generic" drivers may not, so they are left out.
var schemaPluginDialects = map[string]bool{
	"mysql": true, "postgres": true, "sqlite": true, "sqlserver": true,
	"oracle": true, "clickhouse": true, "duckdb": true, "trino": true,
}

func schemaConnectionKind(connection schemaSavedConnection) (string, bool) {
	if connection.ID == "" || connection.Tunnel != nil {
		return "", false
	}
	kind := strings.ToLower(strings.TrimSpace(connection.DbType))
	if alias, ok := schemaTypeAliases[kind]; ok {
		kind = alias
	}
	if connection.Driver != nil {
		// Plugin-bound: a built-in SQL type it replaces, or a plugin-only
		// driver whose dialect is checked once the session is open. The
		// credential grant is enforced by connectExternalSession.
		if connection.Driver.PluginID == "" || connection.Driver.DriverID == "" {
			return "", false
		}
		if kind == "plugin" || schemaSQLTypes[kind] {
			return kind, true
		}
		return "", false
	}
	if !schemaSQLTypes[kind] || !slices.Contains(supportedDatabaseTypes, kind) {
		return "", false
	}
	return kind, true
}

// ListSchemaConnections lists the saved connections whose schema can be read:
// SQL engines of this build or a plugin driver, without an SSH tunnel.
func ListSchemaConnections() ([]SchemaConnection, error) {
	connections, err := loadSchemaSavedConnections()
	if err != nil {
		return nil, err
	}
	out := make([]SchemaConnection, 0, len(connections))
	for _, connection := range connections {
		kind, ok := schemaConnectionKind(connection)
		if !ok {
			continue
		}
		name := strings.TrimSpace(connection.Name)
		if name == "" {
			name = connection.ID
		}
		out = append(out, SchemaConnection{ID: connection.ID, Name: name, Kind: kind, Database: strings.TrimSpace(derefOr(connection.Database, ""))})
	}
	return out, nil
}

var ErrSchemaConnectionNotFound = errors.New("连接不存在，或不是可读取结构的 SQL 连接（不支持 SSH 隧道）")

func withSchemaSession(ctx context.Context, connectionID string, run func(context.Context, *dbSession, schemaSavedConnection) error) error {
	connections, err := loadSchemaSavedConnections()
	if err != nil {
		return err
	}
	index := slices.IndexFunc(connections, func(item schemaSavedConnection) bool { return item.ID == connectionID })
	if index < 0 {
		return ErrSchemaConnectionNotFound
	}
	connection := connections[index]
	if _, ok := schemaConnectionKind(connection); !ok {
		return ErrSchemaConnectionNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, schemaAccessTimeout)
	defer cancel()
	session, err := openSchemaSession(ctx, &connection.backupSavedConnection)
	if err != nil {
		return err
	}
	defer session.close()
	switch {
	case session.kind == kindSQL:
	case session.kind == kindPlugin && schemaPluginDialects[session.ext.dialect] && session.ext.has(externalFeatureMetadataColumns):
	default:
		return ErrSchemaConnectionNotFound
	}
	return run(ctx, session, connection)
}

func schemaQueryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeoutForQuery("SELECT"))
}

// ListSchemaTables lists the databases (or schemas) of a connection and the
// tables of one of them: database, else the connection default, else the first.
func ListSchemaTables(ctx context.Context, connectionID, database string) (*SchemaTableListing, error) {
	var listing *SchemaTableListing
	err := withSchemaSession(ctx, connectionID, func(ctx context.Context, session *dbSession, connection schemaSavedConnection) error {
		queryCtx, cancel := schemaQueryContext(ctx)
		info, err := getNavigationSchema(queryCtx, session)
		cancel()
		if err != nil {
			return fmt.Errorf("读取数据库列表: %w", err)
		}
		listing = &SchemaTableListing{Databases: []string{}, Tables: []SchemaTableSummary{}}
		tables := map[string][]SchemaTable{}
		for _, item := range info.Databases {
			listing.Databases = append(listing.Databases, item.Name)
			tables[item.Name] = item.Tables
		}
		database = strings.TrimSpace(database)
		if database == "" {
			if preferred := strings.TrimSpace(derefOr(connection.Database, "")); slices.Contains(listing.Databases, preferred) {
				database = preferred
			} else if len(listing.Databases) > 0 {
				database = listing.Databases[0]
			}
		}
		if database == "" {
			return nil
		}
		if !slices.Contains(listing.Databases, database) {
			return fmt.Errorf("数据库 %q 不存在", database)
		}
		listing.Database = database
		items := tables[database]
		if len(items) == 0 {
			queryCtx, cancel := schemaQueryContext(ctx)
			loaded, err := getDatabaseTables(queryCtx, session, database)
			cancel()
			if err == nil {
				items = loaded
			}
		}
		for _, item := range items {
			listing.Tables = append(listing.Tables, SchemaTableSummary{Name: item.Name, Kind: item.Kind, Comment: strings.TrimSpace(derefOr(item.Comment, ""))})
		}
		sort.SliceStable(listing.Tables, func(i, j int) bool { return listing.Tables[i].Name < listing.Tables[j].Name })
		return nil
	})
	return listing, err
}

// DescribeSchemaTables reads columns, primary key, indexes and foreign keys of
// up to SchemaDescribeMaxTables tables of one database.
func DescribeSchemaTables(ctx context.Context, connectionID, database string, tables []string) ([]SchemaTableDescription, error) {
	database = strings.TrimSpace(database)
	if database == "" || len(tables) == 0 || len(tables) > SchemaDescribeMaxTables {
		return nil, fmt.Errorf("需要指定数据库和 1-%d 张表", SchemaDescribeMaxTables)
	}
	var out []SchemaTableDescription
	err := withSchemaSession(ctx, connectionID, func(ctx context.Context, session *dbSession, _ schemaSavedConnection) error {
		comments := map[string]string{}
		queryCtx, cancel := schemaQueryContext(ctx)
		info, err := getNavigationSchema(queryCtx, session)
		cancel()
		if err == nil {
			for _, item := range info.Databases {
				if item.Name != database {
					continue
				}
				items := item.Tables
				if len(items) == 0 {
					queryCtx, cancel := schemaQueryContext(ctx)
					items, _ = getDatabaseTables(queryCtx, session, database)
					cancel()
				}
				for _, table := range items {
					comments[table.Name] = strings.TrimSpace(derefOr(table.Comment, ""))
				}
			}
		}
		out = make([]SchemaTableDescription, 0, len(tables))
		for _, table := range tables {
			if err := ctx.Err(); err != nil {
				return err
			}
			out = append(out, describeSchemaTable(ctx, session, database, table, comments[table]))
		}
		return nil
	})
	return out, err
}

func describeSchemaTable(ctx context.Context, session *dbSession, database, table, comment string) SchemaTableDescription {
	out := SchemaTableDescription{Name: table, Comment: comment, Columns: []SchemaColumnDescription{}, PrimaryKey: []string{},
		Indexes: []DbIndexInfo{}, ForeignKeys: []DbForeignKeyInfo{}}
	queryCtx, cancel := schemaQueryContext(ctx)
	columns, err := getTableColumns(queryCtx, session, database, table)
	cancel()
	if err != nil {
		out.Error = fmt.Sprintf("读取字段失败: %v", err)
		return out
	}
	queryCtx, cancel = schemaQueryContext(ctx)
	primaryKey, err := getPrimaryKeys(queryCtx, session, database, table)
	cancel()
	if err == nil && primaryKey != nil && len(primaryKey.Columns) > 0 {
		out.PrimaryKey = append(out.PrimaryKey, primaryKey.Columns...)
	} else {
		for _, column := range columns {
			if column.IsPrimaryKey {
				out.PrimaryKey = append(out.PrimaryKey, column.Name)
			}
		}
	}
	for _, column := range columns {
		out.Columns = append(out.Columns, SchemaColumnDescription{
			Name: column.Name, DataType: column.DataType, Nullable: column.Nullable,
			PrimaryKey:    column.IsPrimaryKey || slices.Contains(out.PrimaryKey, column.Name),
			AutoIncrement: column.AutoIncrement, DefaultValue: column.DefaultValue,
			Comment: strings.TrimSpace(derefOr(column.Comment, "")),
		})
	}
	// Indexes and foreign keys are optional: not every engine exposes them.
	queryCtx, cancel = schemaQueryContext(ctx)
	if indexes, err := listIndexes(queryCtx, session, database, table); err == nil && indexes != nil {
		out.Indexes = indexes
	}
	cancel()
	queryCtx, cancel = schemaQueryContext(ctx)
	if foreignKeys, err := listForeignKeys(queryCtx, session, database, table); err == nil && foreignKeys != nil {
		out.ForeignKeys = foreignKeys
	}
	cancel()
	return out
}
