package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Per-database object lists with statistics for the object overview. Each dialect reads its
// catalog once for the selected schema. Compatible databases may lack some statistics views,
// so a failed statistics query falls back to that schema's plain object list.
//
// Only data-level facts are reported: row counts are catalog estimates where the engine keeps
// them, sizes include indexes, and "updated" is left empty where the catalog only records DDL
// changes, so the column never suggests a data modification that did not happen.

const tableStatsTimeout = 10 * time.Second

type tableStatsRow struct {
	name      string
	kind      string
	rowCount  sql.NullInt64
	sizeBytes sql.NullInt64
	createdAt sql.NullTime
	comment   sql.NullString
}

func sqlDatabaseTablesWithStats(ctx context.Context, s *dbSession, database string) ([]SchemaTable, error) {
	database = strings.TrimSpace(database)
	if database == "" {
		return nil, fmt.Errorf("按库加载对象需要指定数据库或 Schema")
	}
	statsCtx, cancel := context.WithTimeout(ctx, tableStatsTimeout)
	defer cancel()
	tables, err := tableStatsQuery(statsCtx, s, database)
	if err == nil {
		return tables, nil
	}
	info, schemaErr := dispatchGetSchema(ctx, s)
	if schemaErr != nil {
		return nil, fmt.Errorf("读取对象统计失败: %v；读取对象列表失败: %w", err, schemaErr)
	}
	for _, item := range info.Databases {
		if item.Name == database {
			if item.Tables == nil {
				return []SchemaTable{}, nil
			}
			return item.Tables, nil
		}
	}
	return []SchemaTable{}, nil
}

// tableStatsQuery is replaceable so tests can simulate a database without statistics views.
var tableStatsQuery = queryTableStats

func supportsTableStats(dialect string) bool {
	switch dialect {
	case "postgres", "sqlserver", "oracle", "yashandb", "dm", "clickhouse", "duckdb":
		return true
	}
	return false
}

func queryTableStats(ctx context.Context, s *dbSession, database string) ([]SchemaTable, error) {
	var query string
	args := []any{database}
	switch s.dialect {
	case "postgres":
		// Matches information_schema.tables: ordinary, partitioned and foreign tables plus views
		// the current role can access. reltuples is -1 before the first ANALYZE (PostgreSQL 14+),
		// in which case the statistics collector's live-tuple count is used.
		query = `
			SELECT c.relname,
			       CASE WHEN c.relkind = 'v' THEN 'view' ELSE 'table' END,
			       CASE WHEN c.relkind IN ('r', 'p') THEN
			            CASE WHEN c.reltuples >= 0 THEN c.reltuples::bigint ELSE st.n_live_tup END
			       END,
			       CASE WHEN c.relkind IN ('r', 'p') THEN pg_total_relation_size(c.oid) END,
			       NULL::timestamp,
			       obj_description(c.oid, 'pg_class')
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_stat_all_tables st ON st.relid = c.oid
			WHERE n.nspname = $1
			  AND c.relkind IN ('r', 'p', 'v', 'f')
			  AND (pg_has_role(c.relowner, 'USAGE')
			       OR has_table_privilege(c.oid, 'SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER'))
			ORDER BY c.relname`
	case "sqlserver":
		// sys.objects.modify_date only tracks DDL, so it is not reported as an update time.
		query = `
			SELECT o.name,
			       CASE WHEN o.type = 'V' THEN 'view' ELSE 'table' END,
			       CASE WHEN o.type = 'U' THEN (
			            SELECT SUM(p.rows) FROM sys.partitions p
			            WHERE p.object_id = o.object_id AND p.index_id IN (0, 1)) END,
			       CASE WHEN o.type = 'U' THEN (
			            SELECT CAST(SUM(a.total_pages) AS bigint) * 8192
			            FROM sys.partitions p JOIN sys.allocation_units a ON a.container_id = p.partition_id
			            WHERE p.object_id = o.object_id) END,
			       o.create_date,
			       CAST(ep.value AS nvarchar(4000))
			FROM sys.objects o
			JOIN sys.schemas sc ON sc.schema_id = o.schema_id
			LEFT JOIN sys.extended_properties ep
			       ON ep.class = 1 AND ep.major_id = o.object_id AND ep.minor_id = 0 AND ep.name = 'MS_Description'
			WHERE sc.name = @p1 AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0
			ORDER BY o.name`
	case "oracle", "yashandb", "dm":
		// NUM_ROWS comes from the last statistics gathering. Segment sizes need DBA views, so
		// size is not reported; LAST_DDL_TIME is DDL-only and is not reported as an update time.
		first, second := placeholder(s.dialect, 1), placeholder(s.dialect, 2)
		query = fmt.Sprintf(`
			SELECT t.TABLE_NAME, 'table', t.NUM_ROWS, NULL, o.CREATED, c.COMMENTS
			FROM ALL_TABLES t
			LEFT JOIN ALL_OBJECTS o ON o.OWNER = t.OWNER AND o.OBJECT_NAME = t.TABLE_NAME AND o.OBJECT_TYPE = 'TABLE'
			LEFT JOIN ALL_TAB_COMMENTS c ON c.OWNER = t.OWNER AND c.TABLE_NAME = t.TABLE_NAME
			WHERE t.OWNER = %s
			UNION ALL
			SELECT v.VIEW_NAME, 'view', NULL, NULL, o.CREATED, c.COMMENTS
			FROM ALL_VIEWS v
			LEFT JOIN ALL_OBJECTS o ON o.OWNER = v.OWNER AND o.OBJECT_NAME = v.VIEW_NAME AND o.OBJECT_TYPE = 'VIEW'
			LEFT JOIN ALL_TAB_COMMENTS c ON c.OWNER = v.OWNER AND c.TABLE_NAME = v.VIEW_NAME
			WHERE v.OWNER = %s
			ORDER BY 1`, first, second)
		args = append(args, database)
	case "clickhouse":
		query = `
			SELECT name,
			       if(positionCaseInsensitive(engine, 'View') > 0, 'view', 'table'),
			       total_rows, total_bytes, CAST(NULL AS Nullable(DateTime)), comment
			FROM system.tables
			WHERE database = ?
			ORDER BY name`
	case "duckdb":
		// DuckDB exposes an estimated row count per table but no per-table storage size.
		query = `
			SELECT table_name, 'table', estimated_size, NULL, NULL, comment
			FROM duckdb_tables()
			WHERE database_name = current_database() AND NOT internal AND schema_name = ?
			UNION ALL
			SELECT view_name, 'view', NULL, NULL, NULL, comment
			FROM duckdb_views()
			WHERE database_name = current_database() AND NOT internal AND schema_name = ?
			ORDER BY 1`
		args = append(args, database)
	default:
		return nil, fmt.Errorf("%s 不支持对象统计", s.dialect)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := make([]SchemaTable, 0)
	for rows.Next() {
		var row tableStatsRow
		if err := rows.Scan(&row.name, &row.kind, &row.rowCount, &row.sizeBytes, &row.createdAt, &row.comment); err != nil {
			return nil, err
		}
		tables = append(tables, row.schemaTable())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func (row tableStatsRow) schemaTable() SchemaTable {
	// Oracle-compatible UNION ALL pads the CHAR literals to a common length.
	kind := strings.TrimSpace(row.kind)
	item := SchemaTable{Name: row.name, Kind: kind}
	if kind == "table" {
		if row.rowCount.Valid && row.rowCount.Int64 >= 0 {
			value := row.rowCount.Int64
			item.RowCount = &value
		}
		if row.sizeBytes.Valid && row.sizeBytes.Int64 >= 0 {
			value := row.sizeBytes.Int64
			item.SizeBytes = &value
		}
	}
	if row.createdAt.Valid && !row.createdAt.Time.IsZero() {
		value := row.createdAt.Time.Format("2006-01-02 15:04:05")
		item.CreatedAt = &value
	}
	if comment := strings.TrimSpace(row.comment.String); row.comment.Valid && comment != "" {
		item.Comment = &comment
	}
	return item
}
