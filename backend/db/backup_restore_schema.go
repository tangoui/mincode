package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type restoreDefault struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type restoreColumn struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Nullable   bool            `json:"nullable"`
	PrimaryKey int             `json:"primaryKey,omitempty"`
	Default    *restoreDefault `json:"default,omitempty"`
}
type restoreIndex struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}
type restoreSchema struct {
	Version   int             `json:"version"`
	Dialect   string          `json:"dialect"`
	Columns   []restoreColumn `json:"columns"`
	Indexes   []restoreIndex  `json:"indexes,omitempty"`
	Engine    string          `json:"engine,omitempty"`
	Charset   string          `json:"charset,omitempty"`
	Collation string          `json:"collation,omitempty"`
}

var restoreTypePattern = regexp.MustCompile(`^([A-Z]+)(?:\(([0-9]+)(?:,\s*([0-9]+))?\))?( UNSIGNED)?$`)
var restoreNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
var restoreSQLToken = regexp.MustCompile("'(?:(?:'')|[^'])*'|\"(?:\"\"|[^\"])*\"|`(?:``|[^`])*`|\\[(?:[^\\]])*\\]|[A-Za-z_][A-Za-z_0-9]*|[0-9]+|[^\\s]")

func restoreIdentifier(s string) bool {
	return s != "" && len(s) <= 1024 && !strings.ContainsRune(s, 0)
}
func validateRestoreType(dialect, typ string) error {
	m := restoreTypePattern.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(typ)))
	if m == nil {
		return fmt.Errorf("restore_schema_type_unsupported")
	}
	allowed := map[string]bool{"INTEGER": true, "INT": true, "BIGINT": true, "SMALLINT": true, "TINYINT": true, "REAL": true, "DOUBLE": true, "FLOAT": true, "TEXT": true, "BLOB": true, "VARCHAR": true, "CHAR": true, "NUMERIC": true, "DECIMAL": true, "BOOLEAN": true}
	if dialect == "mysql" {
		for _, k := range []string{"MEDIUMINT", "DATETIME", "TIMESTAMP", "DATE", "TIME", "VARBINARY", "BINARY", "LONGBLOB", "MEDIUMBLOB", "TINYBLOB", "LONGTEXT", "MEDIUMTEXT", "TINYTEXT", "BIT"} {
			allowed[k] = true
		}
	}
	if !allowed[m[1]] || (m[4] != "" && dialect != "mysql") {
		return fmt.Errorf("restore_schema_type_unsupported")
	}
	if m[2] != "" {
		n, err := strconv.Atoi(m[2])
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("restore_schema_type_parameters")
		}
	}
	if m[3] != "" {
		a, _ := strconv.Atoi(m[2])
		b, err := strconv.Atoi(m[3])
		if err != nil || b > a || a > 65 || b > 30 || (m[1] != "DECIMAL" && m[1] != "NUMERIC") {
			return fmt.Errorf("restore_schema_type_parameters")
		}
	}
	return nil
}
func (s restoreSchema) Statements(dialect, database, table string) ([]string, error) {
	if dialect == "sqlite" && database != "main" {
		return nil, fmt.Errorf("restore_target_namespace_unsupported")
	}
	if s.Version != 2 || s.Dialect != dialect || (dialect != "sqlite" && dialect != "mysql") || !restoreIdentifier(table) || !restoreIdentifier(database) || len(s.Columns) == 0 || len(s.Columns) > 4096 {
		return nil, fmt.Errorf("restore_schema_unsupported")
	}
	names := map[string]bool{}
	primary := map[int]string{}
	parts := []string{}
	for _, c := range s.Columns {
		if !restoreIdentifier(c.Name) || names[strings.ToLower(c.Name)] || validateRestoreType(dialect, c.Type) != nil {
			return nil, fmt.Errorf("restore_schema_column_unsupported")
		}
		names[strings.ToLower(c.Name)] = true
		part := schemaQuoteIdentifier(dialect, c.Name) + " " + strings.ToUpper(c.Type)
		if c.PrimaryKey > 0 {
			if primary[c.PrimaryKey] != "" {
				return nil, fmt.Errorf("restore_schema_primary_key")
			}
			primary[c.PrimaryKey] = c.Name
		}
		if !c.Nullable {
			part += " NOT NULL"
		}
		if c.Default != nil {
			switch c.Default.Kind {
			case "null":
				if c.Default.Value != "" {
					return nil, fmt.Errorf("restore_schema_default")
				}
				part += " DEFAULT NULL"
			case "string":
				if strings.ContainsRune(c.Default.Value, 0) {
					return nil, fmt.Errorf("restore_schema_default")
				}
				if dialect == "mysql" && strings.Contains(c.Default.Value, "\\") {
					return nil, fmt.Errorf("restore_schema_default")
				}
				part += " DEFAULT '" + strings.ReplaceAll(c.Default.Value, "'", "''") + "'"
			case "number":
				if !restoreNumberPattern.MatchString(c.Default.Value) {
					return nil, fmt.Errorf("restore_schema_default")
				}
				part += " DEFAULT " + c.Default.Value
			default:
				return nil, fmt.Errorf("restore_schema_default")
			}
		}
		if c.PrimaryKey == 1 && lenPrimary(s.Columns) == 1 {
			part += " PRIMARY KEY"
		}
		parts = append(parts, part)
	}
	if len(primary) > 1 {
		pk := []string{}
		for i := 1; i <= len(primary); i++ {
			if primary[i] == "" {
				return nil, fmt.Errorf("restore_schema_primary_key")
			}
			pk = append(pk, schemaQuoteIdentifier(dialect, primary[i]))
		}
		parts = append(parts, "PRIMARY KEY ("+strings.Join(pk, ", ")+")")
	}
	qualified, err := qualifiedTable(dialect, database, table)
	if err != nil {
		return nil, err
	}
	create := "CREATE TABLE " + qualified + " (" + strings.Join(parts, ", ") + ")"
	if dialect == "mysql" {
		if s.Engine != "InnoDB" {
			return nil, fmt.Errorf("restore_schema_engine_unsupported")
		}
		allowedCharsets := map[string]bool{"utf8mb4": true, "utf8mb3": true, "utf8": true, "latin1": true, "ascii": true, "binary": true}
		if !allowedCharsets[s.Charset] || s.Collation == "" || !strings.HasPrefix(s.Collation, s.Charset+"_") {
			return nil, fmt.Errorf("restore_schema_collation_unsupported")
		}
		for _, c := range s.Collation {
			if !(unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_') || c > 127 {
				return nil, fmt.Errorf("restore_schema_collation_unsupported")
			}
		}
		create += " ENGINE=InnoDB DEFAULT CHARSET=" + s.Charset + " COLLATE=" + s.Collation
	}
	out := []string{create}
	indexNames := map[string]bool{}
	for _, idx := range s.Indexes {
		if !restoreIdentifier(idx.Name) || indexNames[idx.Name] || len(idx.Columns) == 0 {
			return nil, fmt.Errorf("restore_schema_index")
		}
		indexNames[idx.Name] = true
		cols := []string{}
		for _, col := range idx.Columns {
			if !names[strings.ToLower(col)] {
				return nil, fmt.Errorf("restore_schema_index")
			}
			cols = append(cols, schemaQuoteIdentifier(dialect, col))
		}
		unique := ""
		if idx.Unique {
			unique = "UNIQUE "
		}
		// Index names are regenerated under the reviewed table to avoid global SQLite name collisions.
		sum := transferSchemaHash(table + "\x00" + idx.Name)
		out = append(out, "CREATE "+unique+"INDEX "+schemaQuoteIdentifier(dialect, "mincode_"+sum[:24])+" ON "+qualified+" ("+strings.Join(cols, ", ")+")")
	}
	return out, nil
}
func lenPrimary(cols []restoreColumn) int {
	n := 0
	for _, c := range cols {
		if c.PrimaryKey > 0 {
			n++
		}
	}
	return n
}
func parseRestoreDefault(raw sql.NullString) (*restoreDefault, error) {
	if !raw.Valid {
		return nil, nil
	}
	v := raw.String
	if strings.EqualFold(v, "NULL") {
		return &restoreDefault{Kind: "null"}, nil
	}
	if restoreNumberPattern.MatchString(v) {
		return &restoreDefault{Kind: "number", Value: v}, nil
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		inner := v[1 : len(v)-1]
		if strings.Contains(strings.ReplaceAll(inner, "''", ""), "'") {
			return nil, fmt.Errorf("restore_schema_default")
		}
		return &restoreDefault{Kind: "string", Value: strings.ReplaceAll(inner, "''", "'")}, nil
	}
	return nil, fmt.Errorf("restore_schema_default_unsupported")
}
func canonicalSchemaTokens(raw string) string {
	tokens := restoreSQLToken.FindAllString(raw, -1)
	for i, t := range tokens {
		if len(t) >= 2 && t[0] == '\'' {
			continue
		}
		if len(t) >= 2 && (t[0] == '"' || t[0] == '`' || t[0] == '[') {
			quote := t[:1]
			t = t[1 : len(t)-1]
			t = strings.ReplaceAll(t, quote+quote, quote)
		}
		tokens[i] = strings.ToUpper(t)
	}
	return strings.Join(tokens, "\x00")
}
func readRestoreSchema(ctx context.Context, s *dbSession, database, table string) (restoreSchema, error) {
	out := restoreSchema{Version: 2, Dialect: s.dialect, Columns: []restoreColumn{}, Indexes: []restoreIndex{}}
	if s.dialect == "mysql" {
		return readMySQLRestoreSchema(ctx, s, database, table)
	}
	if s.dialect != "sqlite" || database != "main" {
		return out, fmt.Errorf("restore_schema_unsupported")
	}
	var original string
	if err := s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&original); err != nil {
		return out, err
	}
	var triggers int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='trigger' AND tbl_name=?", table).Scan(&triggers); err != nil {
		return out, err
	}
	if triggers > 0 {
		return out, fmt.Errorf("restore_schema_trigger_unsupported")
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_xinfo("+quoteSQLiteIdent(table)+")")
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var cid, notnull, pk, hidden int
		var col restoreColumn
		var def sql.NullString
		if err := rows.Scan(&cid, &col.Name, &col.Type, &notnull, &def, &pk, &hidden); err != nil {
			rows.Close()
			return out, err
		}
		if hidden != 0 {
			rows.Close()
			return out, fmt.Errorf("restore_schema_generated_unsupported")
		}
		col.Nullable = notnull == 0
		col.PrimaryKey = pk
		col.Default, err = parseRestoreDefault(def)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Columns = append(out.Columns, col)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	// A deliberately exact subset, not a general SQL parser. Reconstructing this
	// grammar must account for every token in the original CREATE TABLE.
	statements, err := out.Statements("sqlite", "main", table)
	if err != nil {
		return out, err
	}
	candidate := strings.Replace(statements[0], quoteSQLiteIdent("main")+".", "", 1)
	if canonicalSchemaTokens(original) != canonicalSchemaTokens(candidate) {
		return out, fmt.Errorf("restore_schema_definition_unsupported")
	}
	rows, err = s.db.QueryContext(ctx, "PRAGMA index_list("+quoteSQLiteIdent(table)+")")
	if err != nil {
		return out, err
	}
	var indexes []restoreIndex
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return out, err
		}
		if origin == "pk" {
			continue
		}
		if origin != "c" || partial != 0 {
			rows.Close()
			return out, fmt.Errorf("restore_schema_index_unsupported")
		}
		indexes = append(indexes, restoreIndex{Name: name, Unique: unique != 0})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, idx := range indexes {
		rows, err = s.db.QueryContext(ctx, "PRAGMA index_xinfo("+quoteSQLiteIdent(idx.Name)+")")
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var seq, cid, desc, key int
			var name, coll sql.NullString
			if err := rows.Scan(&seq, &cid, &name, &desc, &coll, &key); err != nil {
				rows.Close()
				return out, err
			}
			if key == 0 {
				continue
			}
			if cid < 0 || !name.Valid || desc != 0 || (coll.Valid && !strings.EqualFold(coll.String, "BINARY")) {
				rows.Close()
				return out, fmt.Errorf("restore_schema_index_unsupported")
			}
			idx.Columns = append(idx.Columns, name.String)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		out.Indexes = append(out.Indexes, idx)
	}
	_, err = out.Statements("sqlite", database, table)
	return out, err
}
func readMySQLRestoreSchema(ctx context.Context, s *dbSession, database, table string) (restoreSchema, error) {
	out := restoreSchema{Version: 2, Dialect: "mysql", Columns: []restoreColumn{}, Indexes: []restoreIndex{}}
	var kind string
	err := s.db.QueryRowContext(ctx, "SELECT TABLE_TYPE, ENGINE, TABLE_COLLATION FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", database, table).Scan(&kind, &out.Engine, &out.Collation)
	if err != nil {
		return out, err
	}
	if kind != "BASE TABLE" || out.Engine != "InnoDB" {
		return out, fmt.Errorf("restore_schema_engine_unsupported")
	}
	out.Charset = strings.SplitN(out.Collation, "_", 2)[0]
	grants, err := s.db.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return out, fmt.Errorf("restore_trigger_visibility_unavailable")
	}
	grantLines := []string{}
	for grants.Next() {
		var line string
		if err := grants.Scan(&line); err != nil {
			grants.Close()
			return out, err
		}
		grantLines = append(grantLines, line)
	}
	err = grants.Err()
	grants.Close()
	if err != nil {
		return out, err
	}
	if !mysqlTriggerVisible(grantLines, database) {
		return out, fmt.Errorf("restore_trigger_visibility_unavailable")
	}

	for _, query := range []string{
		"SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE EVENT_OBJECT_SCHEMA=? AND EVENT_OBJECT_TABLE=?",
		"SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=? AND TABLE_NAME=? AND CONSTRAINT_TYPE NOT IN ('PRIMARY KEY','UNIQUE')",
	} {
		var n int
		if err := s.db.QueryRowContext(ctx, query, database, table).Scan(&n); err != nil {
			return out, err
		}
		if n != 0 {
			return out, fmt.Errorf("restore_schema_effects_unsupported")
		}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT,EXTRA,GENERATION_EXPRESSION,COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION", database, table)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c restoreColumn
		var nullable, extra, expression string
		var def, coll sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &nullable, &def, &extra, &expression, &coll); err != nil {
			rows.Close()
			return out, err
		}
		if extra != "" || expression != "" || (coll.Valid && coll.String != out.Collation) {
			rows.Close()
			return out, fmt.Errorf("restore_schema_column_effects_unsupported")
		}
		c.Nullable = nullable == "YES"
		c.Type = strings.ToUpper(c.Type)
		if def.Valid {
			if restoreNumberPattern.MatchString(def.String) {
				c.Default = &restoreDefault{Kind: "number", Value: def.String}
			} else {
				c.Default = &restoreDefault{Kind: "string", Value: def.String}
			}
		}
		out.Columns = append(out.Columns, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, "SELECT INDEX_NAME,NON_UNIQUE,SEQ_IN_INDEX,COLUMN_NAME,SUB_PART,COLLATION,INDEX_TYPE FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY INDEX_NAME,SEQ_IN_INDEX", database, table)
	if err != nil {
		return out, err
	}
	indexes := map[string]*restoreIndex{}
	for rows.Next() {
		var name, typ string
		var nonUnique, seq int
		var column, coll sql.NullString
		var sub sql.NullInt64
		if err := rows.Scan(&name, &nonUnique, &seq, &column, &sub, &coll, &typ); err != nil {
			rows.Close()
			return out, err
		}
		if !column.Valid || sub.Valid || typ != "BTREE" || (coll.Valid && coll.String != "A") {
			rows.Close()
			return out, fmt.Errorf("restore_schema_index_unsupported")
		}
		if name == "PRIMARY" {
			found := false
			for i := range out.Columns {
				if out.Columns[i].Name == column.String {
					out.Columns[i].PrimaryKey = seq
					found = true
				}
			}
			if !found {
				rows.Close()
				return out, fmt.Errorf("restore_schema_primary_key")
			}
			continue
		}
		idx := indexes[name]
		if idx == nil {
			idx = &restoreIndex{Name: name, Unique: nonUnique == 0}
			indexes[name] = idx
		}
		idx.Columns = append(idx.Columns, column.String)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	names := []string{}
	for name := range indexes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out.Indexes = append(out.Indexes, *indexes[name])
	}
	_, err = out.Statements("mysql", database, table)
	return out, err
}

// SHOW GRANTS role grants are deliberately not inferred: missing proven
// TRIGGER visibility must not turn an invisible trigger into "no trigger".
func mysqlTriggerVisible(grants []string, database string) bool {
	for _, grant := range grants {
		upper := strings.ToUpper(grant)
		on := strings.Index(upper, " ON ")
		to := strings.Index(upper, " TO ")
		if !strings.HasPrefix(upper, "GRANT ") || on < 0 || to < on {
			continue
		}
		privileges := strings.Split(upper[len("GRANT "):on], ",")
		visible := false
		for _, p := range privileges {
			p = strings.TrimSpace(p)
			if p == "TRIGGER" || p == "ALL PRIVILEGES" {
				visible = true
			}
		}
		if !visible {
			continue
		}
		scope := strings.TrimSpace(grant[on+4 : to])
		if scope == "*.*" || scope == "`"+strings.ReplaceAll(database, "`", "``")+"`.*" {
			return true
		}
	}
	return false
}
