package sqlsafety

import (
	"strings"
)

// Quoter quotes a column name for the connection's dialect.
type Quoter func(name string) string

// QualifyFunc returns the quoted, schema-qualified name of a table. An empty
// schema means the connection's current database/schema.
type QualifyFunc func(schema, table string) string

// CountSQL returns "SELECT COUNT(*) FROM <table> [<alias>] [WHERE <where>]"
// counting the rows a rewritable UPDATE/DELETE would touch.
func CountSQL(f Family, st Statement, qualify QualifyFunc) (string, bool) {
	from, ok := fromWhere(st, qualify)
	if !ok {
		return "", false
	}
	return "SELECT COUNT(*)" + from, true
}

// ExplainSQL returns the statement that asks the database for a plan of
// stmt. Oracle and DM write the plan into PLAN_TABLE (read its CARDINALITY
// afterwards). SQL Server needs SET SHOWPLAN_XML ON on the session and
// unsupported families have no known form; both return false.
func ExplainSQL(f Family, stmt string) (string, bool) {
	stmt = strings.TrimRight(strings.TrimSpace(stmt), "; \t\r\n")
	if stmt == "" {
		return "", false
	}
	switch f {
	case FamilyMySQL, FamilyPostgres, FamilySQLite, FamilyANSI:
		return "EXPLAIN " + stmt, true
	case FamilyOracle, FamilyDM:
		return "EXPLAIN PLAN FOR " + stmt, true
	}
	return "", false
}

// SnapshotSQL returns the SELECT that reads, before a rewritable
// UPDATE/DELETE runs, the rows it will touch: the primary key plus the SET
// columns for UPDATE, every column for DELETE. With forUpdate the rows are
// locked where the family supports SELECT ... FOR UPDATE.
func SnapshotSQL(f Family, st Statement, qualify QualifyFunc, quote Quoter, pk []string, forUpdate bool) (string, bool) {
	from, ok := fromWhere(st, qualify)
	if !ok {
		return "", false
	}
	prefix := ""
	if st.Alias != "" {
		prefix = st.Alias + "."
	}
	list := prefix + "*"
	if len(st.SetColumns) > 0 { // UPDATE; a rewritable DELETE has none
		cols := make([]string, 0, len(pk)+len(st.SetColumns))
		for _, c := range union(pk, st.SetColumns) {
			cols = append(cols, prefix+quote(c))
		}
		list = strings.Join(cols, ", ")
	}
	if forUpdate && f == FamilySQLServer {
		// T-SQL has no FOR UPDATE: lock with a table hint, held to the end
		// of the transaction, before the WHERE clause.
		from, _ = fromWhereHint(st, qualify, " WITH (UPDLOCK, HOLDLOCK)")
	}
	sql := "SELECT " + list + from
	if forUpdate {
		switch f {
		case FamilyMySQL, FamilyPostgres, FamilyOracle, FamilyDM:
			sql += " FOR UPDATE"
		}
	}
	return sql, true
}

// InsertKeysSQL rewrites a plain single-table INSERT (Kind insert with Table
// set by Classify) so the database returns the primary key of every row it
// inserts: "... RETURNING <pk>" on Postgres and SQLite, "INSERT INTO t
// [(cols)] OUTPUT INSERTED.<pk> ..." on SQL Server. It returns false for
// other families, without a key, or when the statement already has a
// RETURNING / OUTPUT clause (its result belongs to the caller).
func InsertKeysSQL(f Family, st Statement, quote Quoter, pk []string) (string, bool) {
	if st.Kind != KindInsert || st.Table == nil || len(pk) == 0 {
		return "", false
	}
	if f != FamilyPostgres && f != FamilySQLite && f != FamilySQLServer {
		return "", false
	}
	toks := lex(f, st.Text)
	for len(toks) > 0 && toks[len(toks)-1].isPunct(";") {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 || !toks[0].isWord("INSERT") {
		return "", false
	}
	for _, t := range toks {
		if t.depth == 0 && t.isWord("RETURNING", "OUTPUT") {
			return "", false
		}
	}
	quoted := make([]string, len(pk))
	if f != FamilySQLServer {
		for i, c := range pk {
			quoted[i] = quote(c)
		}
		return st.Text[:toks[len(toks)-1].end] + " RETURNING " + strings.Join(quoted, ", "), true
	}
	// SQL Server: OUTPUT goes after the target and its column list.
	i := 1
	if i >= len(toks) || !toks[i].isWord("INTO") {
		return "", false
	}
	_, i, ok := parseTableRef(toks, i+1)
	if !ok {
		return "", false
	}
	if i < len(toks) && toks[i].isPunct("(") {
		for i++; i < len(toks) && !(toks[i].depth == 0 && toks[i].isPunct(")")); i++ {
		}
		i++
	}
	if i >= len(toks) || !toks[i].isWord("VALUES", "SELECT", "DEFAULT", "WITH") ||
		toks[i].isWord("WITH") && (i+1 >= len(toks) || toks[i+1].isPunct("(")) { // WITH (hints), not a CTE
		return "", false
	}
	for n, c := range pk {
		quoted[n] = "INSERTED." + quote(c)
	}
	at := toks[i].start
	return st.Text[:at] + "OUTPUT " + strings.Join(quoted, ", ") + " " + st.Text[at:toks[len(toks)-1].end], true
}

// ByKeysSQL returns "SELECT <columns> FROM <table> WHERE (pk1 = ? AND pk2 = ?)
// OR (...)" for keyCount keys, numbering placeholders 1..keyCount*len(pk)
// key by key; keyCount must be at least 1. No columns selects every column.
// Callers batch keys (at most 500 per statement).
func ByKeysSQL(f Family, qualify QualifyFunc, quote Quoter, table TableRef, pk []string, columns []string, keyCount int, placeholder func(n int) string) string {
	list := "*"
	if len(columns) > 0 {
		quoted := make([]string, len(columns))
		for i, c := range columns {
			quoted[i] = quote(c)
		}
		list = strings.Join(quoted, ", ")
	}
	quotedPK := make([]string, len(pk))
	for i, c := range pk {
		quotedPK[i] = quote(c)
	}
	var b strings.Builder
	b.WriteString("SELECT " + list + " FROM " + qualify(table.Schema, table.Name) + " WHERE ")
	n := 0
	for k := 0; k < keyCount; k++ {
		if k > 0 {
			b.WriteString(" OR ")
		}
		b.WriteByte('(')
		for i, c := range quotedPK {
			if i > 0 {
				b.WriteString(" AND ")
			}
			n++
			b.WriteString(c + " = " + placeholder(n))
		}
		b.WriteByte(')')
	}
	return b.String()
}

// fromWhere renders " FROM <table> [<alias>] [WHERE <where>]" for a
// rewritable statement. The alias follows the table without AS (Oracle and
// SQL Server reject AS there) so a WHERE that names it stays valid.
func fromWhere(st Statement, qualify QualifyFunc) (string, bool) {
	return fromWhereHint(st, qualify, "")
}

// fromWhereHint is fromWhere with a table hint after the table and alias.
func fromWhereHint(st Statement, qualify QualifyFunc, hint string) (string, bool) {
	if !st.Rewritable || st.Table == nil {
		return "", false
	}
	s := " FROM " + qualify(st.Table.Schema, st.Table.Name)
	if st.Alias != "" {
		s += " " + st.Alias
	}
	s += hint
	if st.Where != "" {
		s += " WHERE " + st.Where
	}
	return s, true
}

// union returns a followed by the names of b not already present. Names
// compare exactly: in case-sensitive dialects "A" and "a" are different
// columns, and both must be backed up.
func union(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, name := range b {
		dup := false
		for _, have := range out {
			if have == name {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, name)
		}
	}
	return out
}
