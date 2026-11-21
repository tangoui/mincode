//go:build !lite

// Firebird 连接、系统表元数据查询与 DDL 重建。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	_ "github.com/nakagami/firebirdsql"
)

func buildFirebirdSession(cfg *DbConnectionConfig) (*dbSession, error) {
	database := strings.TrimSpace(derefOr(cfg.Database, ""))
	if database == "" {
		return nil, fmt.Errorf("Firebird 数据库路径或别名不能为空")
	}

	dsn, err := buildFirebirdDSN(cfg)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("firebirdsql", dsn)
	if err != nil {
		return nil, fmt.Errorf("Firebird 打开失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("Firebird 连接失败: %w", err)
	}
	return &dbSession{
		kind:            kindSQL,
		db:              db,
		dialect:         "firebird",
		defaultDatabase: database,
	}, nil
}

func buildFirebirdDSN(cfg *DbConnectionConfig) (string, error) {
	database := strings.TrimSpace(derefOr(cfg.Database, ""))
	if database == "" {
		return "", fmt.Errorf("Firebird 数据库路径或别名不能为空")
	}
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	port := cfg.Port
	if port == 0 {
		port = 3050
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("Firebird 端口无效: %d", port)
	}

	username := strings.TrimSpace(derefOr(cfg.Username, ""))
	if username == "" {
		username = "SYSDBA"
	}
	path := database
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := &url.URL{
		Scheme: "firebird",
		User:   url.UserPassword(username, derefOr(cfg.Password, "")),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   path,
	}
	query := u.Query()
	query.Set("charset", "UTF8")
	query.Set("wire_crypt", strconv.FormatBool(cfg.SSL))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func firebirdSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT TRIM(r.RDB$RELATION_NAME),
		CASE WHEN r.RDB$VIEW_BLR IS NULL THEN 'table' ELSE 'view' END
		FROM RDB$RELATIONS r
		WHERE COALESCE(r.RDB$SYSTEM_FLAG, 0) = 0
		ORDER BY r.RDB$RELATION_NAME`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make([]SchemaTable, 0)
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		tables = append(tables, SchemaTable{Name: strings.TrimSpace(name), Kind: strings.ToLower(kind)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(s.defaultDatabase)
	if name == "" {
		name = "main"
	}
	return &DbSchemaInfo{Databases: []SchemaDatabase{{Name: name, Tables: tables}}}, nil
}

func firebirdColumns(ctx context.Context, s *dbSession, table string) ([]ColumnInfo, error) {
	primaryKey, err := firebirdPrimaryKeys(ctx, s, table)
	if err != nil {
		return nil, err
	}
	primaryColumns := make(map[string]struct{}, len(primaryKey.Columns))
	for _, column := range primaryKey.Columns {
		primaryColumns[column] = struct{}{}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT TRIM(rf.RDB$FIELD_NAME),
		f.RDB$FIELD_TYPE, COALESCE(f.RDB$FIELD_SUB_TYPE, 0),
		f.RDB$FIELD_LENGTH, f.RDB$CHARACTER_LENGTH, f.RDB$FIELD_PRECISION,
		f.RDB$FIELD_SCALE, rf.RDB$NULL_FLAG, rf.RDB$DEFAULT_SOURCE, rf.RDB$DESCRIPTION
		FROM RDB$RELATION_FIELDS rf
		JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = rf.RDB$FIELD_SOURCE
		WHERE rf.RDB$RELATION_NAME = ?
		ORDER BY rf.RDB$FIELD_POSITION`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make([]ColumnInfo, 0)
	for rows.Next() {
		var name string
		var fieldType int64
		var subtype, fieldLength, characterLength, precision, scale, notNull sql.NullInt64
		var defaultSource, description any
		if err := rows.Scan(
			&name, &fieldType, &subtype, &fieldLength, &characterLength, &precision,
			&scale, &notNull, &defaultSource, &description,
		); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		column := ColumnInfo{
			Name:         name,
			DataType:     firebirdColumnType(fieldType, subtype, fieldLength, characterLength, precision, scale),
			Nullable:     !notNull.Valid || notNull.Int64 != 1,
			Comment:      firebirdOptionalText(description),
			DefaultValue: firebirdDefaultValue(defaultSource),
		}
		_, column.IsPrimaryKey = primaryColumns[name]
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// RDB$IDENTITY_TYPE 从 Firebird 3 开始存在；旧版本查询失败时保持 false。
	identityRows, identityErr := s.db.QueryContext(ctx, `SELECT TRIM(RDB$FIELD_NAME)
		FROM RDB$RELATION_FIELDS
		WHERE RDB$RELATION_NAME = ? AND RDB$IDENTITY_TYPE IS NOT NULL`, table)
	if identityErr == nil {
		identityColumns := map[string]struct{}{}
		for identityRows.Next() {
			var name string
			if identityRows.Scan(&name) == nil {
				identityColumns[strings.TrimSpace(name)] = struct{}{}
			}
		}
		_ = identityRows.Close()
		for i := range columns {
			_, columns[i].AutoIncrement = identityColumns[columns[i].Name]
		}
	}
	return columns, nil
}

func firebirdColumnType(fieldType int64, subtype, fieldLength, characterLength, precision, scale sql.NullInt64) string {
	if firebirdNumericFieldType(fieldType) && subtype.Valid && (subtype.Int64 == 1 || subtype.Int64 == 2) {
		name := "NUMERIC"
		if subtype.Int64 == 2 {
			name = "DECIMAL"
		}
		p := precision.Int64
		if p <= 0 {
			switch fieldType {
			case 7:
				p = 4
			case 8:
				p = 9
			case 16:
				p = 18
			case 26:
				p = 38
			}
		}
		s := scale.Int64
		if s < 0 {
			s = -s
		}
		if p > 0 {
			return fmt.Sprintf("%s(%d,%d)", name, p, s)
		}
		return name
	}

	switch fieldType {
	case 7:
		return "SMALLINT"
	case 8:
		return "INTEGER"
	case 10:
		return "FLOAT"
	case 12:
		return "DATE"
	case 13:
		return "TIME"
	case 14:
		return fmt.Sprintf("CHAR(%d)", firebirdTextLength(fieldLength, characterLength))
	case 16:
		return "BIGINT"
	case 23:
		return "BOOLEAN"
	case 24:
		return "DECFLOAT(16)"
	case 25:
		return "DECFLOAT(34)"
	case 26:
		return "INT128"
	case 27:
		return "DOUBLE PRECISION"
	case 28:
		return "TIME WITH TIME ZONE"
	case 29:
		return "TIMESTAMP WITH TIME ZONE"
	case 35:
		return "TIMESTAMP"
	case 37:
		return fmt.Sprintf("VARCHAR(%d)", firebirdTextLength(fieldLength, characterLength))
	case 40:
		return fmt.Sprintf("CSTRING(%d)", firebirdTextLength(fieldLength, characterLength))
	case 261:
		if subtype.Valid && subtype.Int64 == 1 {
			return "BLOB SUB_TYPE TEXT"
		}
		return "BLOB"
	default:
		return fmt.Sprintf("TYPE_%d", fieldType)
	}
}

func firebirdNumericFieldType(fieldType int64) bool {
	switch fieldType {
	case 7, 8, 16, 26:
		return true
	default:
		return false
	}
}

func firebirdTextLength(fieldLength, characterLength sql.NullInt64) int64 {
	if characterLength.Valid && characterLength.Int64 > 0 {
		return characterLength.Int64
	}
	if fieldLength.Valid && fieldLength.Int64 > 0 {
		return fieldLength.Int64
	}
	return 1
}

func firebirdOptionalText(value any) *string {
	if value == nil {
		return nil
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", normalizeValue(value)))
	if text == "" {
		return nil
	}
	return &text
}

func firebirdDefaultValue(value any) *string {
	text := firebirdOptionalText(value)
	if text == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*text)
	if len(trimmed) >= len("DEFAULT") && strings.EqualFold(trimmed[:len("DEFAULT")], "DEFAULT") {
		trimmed = strings.TrimSpace(trimmed[len("DEFAULT"):])
	}
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func firebirdPrimaryKeys(ctx context.Context, s *dbSession, table string) (*PrimaryKeyInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT TRIM(rc.RDB$CONSTRAINT_NAME), TRIM(seg.RDB$FIELD_NAME)
		FROM RDB$RELATION_CONSTRAINTS rc
		JOIN RDB$INDEX_SEGMENTS seg ON seg.RDB$INDEX_NAME = rc.RDB$INDEX_NAME
		WHERE rc.RDB$CONSTRAINT_TYPE = 'PRIMARY KEY' AND rc.RDB$RELATION_NAME = ?
		ORDER BY seg.RDB$FIELD_POSITION`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPrimaryKeyInfo(rows)
}

func firebirdIndexes(ctx context.Context, s *dbSession, table string) ([]DbIndexInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT TRIM(i.RDB$INDEX_NAME), TRIM(seg.RDB$FIELD_NAME),
		COALESCE(i.RDB$UNIQUE_FLAG, 0)
		FROM RDB$INDICES i
		JOIN RDB$INDEX_SEGMENTS seg ON seg.RDB$INDEX_NAME = i.RDB$INDEX_NAME
		WHERE i.RDB$RELATION_NAME = ? AND COALESCE(i.RDB$SYSTEM_FLAG, 0) = 0
		ORDER BY i.RDB$INDEX_NAME, seg.RDB$FIELD_POSITION`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	indexes := make([]DbIndexInfo, 0)
	positions := map[string]int{}
	for rows.Next() {
		var name, column string
		var unique int
		if err := rows.Scan(&name, &column, &unique); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		if position, ok := positions[name]; ok {
			indexes[position].Columns = append(indexes[position].Columns, strings.TrimSpace(column))
			continue
		}
		positions[name] = len(indexes)
		indexes = append(indexes, DbIndexInfo{
			Name: name, Columns: []string{strings.TrimSpace(column)}, Unique: unique != 0,
		})
	}
	return indexes, rows.Err()
}

func firebirdForeignKeys(ctx context.Context, s *dbSession, table string) ([]DbForeignKeyInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT TRIM(rc.RDB$CONSTRAINT_NAME),
		TRIM(local_seg.RDB$FIELD_NAME), TRIM(referenced_rc.RDB$RELATION_NAME),
		TRIM(referenced_seg.RDB$FIELD_NAME)
		FROM RDB$RELATION_CONSTRAINTS rc
		JOIN RDB$REF_CONSTRAINTS ref ON ref.RDB$CONSTRAINT_NAME = rc.RDB$CONSTRAINT_NAME
		JOIN RDB$RELATION_CONSTRAINTS referenced_rc
		  ON referenced_rc.RDB$CONSTRAINT_NAME = ref.RDB$CONST_NAME_UQ
		JOIN RDB$INDEX_SEGMENTS local_seg ON local_seg.RDB$INDEX_NAME = rc.RDB$INDEX_NAME
		JOIN RDB$INDEX_SEGMENTS referenced_seg
		  ON referenced_seg.RDB$INDEX_NAME = referenced_rc.RDB$INDEX_NAME
		 AND referenced_seg.RDB$FIELD_POSITION = local_seg.RDB$FIELD_POSITION
		WHERE rc.RDB$CONSTRAINT_TYPE = 'FOREIGN KEY' AND rc.RDB$RELATION_NAME = ?
		ORDER BY rc.RDB$CONSTRAINT_NAME, local_seg.RDB$FIELD_POSITION`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	foreignKeys := make([]DbForeignKeyInfo, 0)
	positions := map[string]int{}
	for rows.Next() {
		var name, column, referencedTable, referencedColumn string
		if err := rows.Scan(&name, &column, &referencedTable, &referencedColumn); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		if position, ok := positions[name]; ok {
			foreignKeys[position].Columns = append(foreignKeys[position].Columns, strings.TrimSpace(column))
			foreignKeys[position].ReferencedColumns = append(
				foreignKeys[position].ReferencedColumns, strings.TrimSpace(referencedColumn),
			)
			continue
		}
		positions[name] = len(foreignKeys)
		foreignKeys = append(foreignKeys, DbForeignKeyInfo{
			Name:              name,
			Columns:           []string{strings.TrimSpace(column)},
			ReferencedTable:   strings.TrimSpace(referencedTable),
			ReferencedColumns: []string{strings.TrimSpace(referencedColumn)},
		})
	}
	return foreignKeys, rows.Err()
}

func firebirdTriggers(ctx context.Context, s *dbSession, table string) ([]DbTriggerInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT TRIM(RDB$TRIGGER_NAME), RDB$TRIGGER_TYPE
		FROM RDB$TRIGGERS
		WHERE RDB$RELATION_NAME = ? AND COALESCE(RDB$SYSTEM_FLAG, 0) = 0
		ORDER BY RDB$TRIGGER_SEQUENCE, RDB$TRIGGER_NAME`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	triggers := make([]DbTriggerInfo, 0)
	for rows.Next() {
		var name string
		var triggerType int64
		if err := rows.Scan(&name, &triggerType); err != nil {
			return nil, err
		}
		timing, event := firebirdTriggerTimingEvent(triggerType)
		triggers = append(triggers, DbTriggerInfo{Name: strings.TrimSpace(name), Timing: timing, Event: event})
	}
	return triggers, rows.Err()
}

func firebirdTriggerTimingEvent(triggerType int64) (string, string) {
	switch triggerType {
	case 1:
		return "BEFORE", "INSERT"
	case 2:
		return "AFTER", "INSERT"
	case 3:
		return "BEFORE", "UPDATE"
	case 4:
		return "AFTER", "UPDATE"
	case 5:
		return "BEFORE", "DELETE"
	case 6:
		return "AFTER", "DELETE"
	case 17:
		return "BEFORE", "INSERT OR UPDATE"
	case 18:
		return "AFTER", "INSERT OR UPDATE"
	case 25:
		return "BEFORE", "INSERT OR DELETE"
	case 26:
		return "AFTER", "INSERT OR DELETE"
	case 27:
		return "BEFORE", "UPDATE OR DELETE"
	case 28:
		return "AFTER", "UPDATE OR DELETE"
	case 113:
		return "BEFORE", "INSERT OR UPDATE OR DELETE"
	case 114:
		return "AFTER", "INSERT OR UPDATE OR DELETE"
	default:
		return "", fmt.Sprintf("TYPE %d", triggerType)
	}
}

func firebirdTableDDL(ctx context.Context, s *dbSession, table string) (string, error) {
	var viewSource any
	err := s.db.QueryRowContext(ctx, `SELECT RDB$VIEW_SOURCE FROM RDB$RELATIONS
		WHERE RDB$RELATION_NAME = ? AND COALESCE(RDB$SYSTEM_FLAG, 0) = 0`, table).Scan(&viewSource)
	if err != nil {
		return "", err
	}
	if source := firebirdOptionalText(viewSource); source != nil {
		return fmt.Sprintf("CREATE VIEW %s AS\n%s;", firebirdQuoteIdentifier(table), strings.TrimSuffix(*source, ";")), nil
	}

	columns, err := firebirdColumns(ctx, s, table)
	if err != nil {
		return "", err
	}
	if len(columns) == 0 {
		return "", fmt.Errorf("表 %s 不存在或无列", table)
	}
	primaryKey, err := firebirdPrimaryKeys(ctx, s, table)
	if err != nil {
		return "", err
	}
	foreignKeys, err := firebirdForeignKeys(ctx, s, table)
	if err != nil {
		return "", err
	}

	definitions := make([]string, 0, len(columns)+1+len(foreignKeys))
	for _, column := range columns {
		definition := "  " + firebirdQuoteIdentifier(column.Name) + " " + column.DataType
		if column.AutoIncrement {
			definition += " GENERATED BY DEFAULT AS IDENTITY"
		}
		if column.DefaultValue != nil {
			definition += " DEFAULT " + *column.DefaultValue
		}
		if !column.Nullable {
			definition += " NOT NULL"
		}
		definitions = append(definitions, definition)
	}
	if len(primaryKey.Columns) > 0 {
		constraint := ""
		if primaryKey.ConstraintName != "" {
			constraint = "CONSTRAINT " + firebirdQuoteIdentifier(primaryKey.ConstraintName) + " "
		}
		definitions = append(definitions, "  "+constraint+"PRIMARY KEY ("+firebirdQuotedColumns(primaryKey.Columns)+")")
	}
	for _, foreignKey := range foreignKeys {
		constraint := ""
		if foreignKey.Name != "" {
			constraint = "CONSTRAINT " + firebirdQuoteIdentifier(foreignKey.Name) + " "
		}
		definitions = append(definitions, "  "+constraint+"FOREIGN KEY ("+
			firebirdQuotedColumns(foreignKey.Columns)+") REFERENCES "+
			firebirdQuoteIdentifier(foreignKey.ReferencedTable)+" ("+
			firebirdQuotedColumns(foreignKey.ReferencedColumns)+")")
	}
	return "CREATE TABLE " + firebirdQuoteIdentifier(table) + " (\n" +
		strings.Join(definitions, ",\n") + "\n);", nil
}

func firebirdQuotedColumns(columns []string) string {
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, firebirdQuoteIdentifier(column))
	}
	return strings.Join(quoted, ", ")
}

func firebirdQuoteIdentifier(identifier string) string {
	return `"` + escapeDoubleQuote(identifier) + `"`
}
