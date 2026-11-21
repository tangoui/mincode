package sqlsafety

import (
	"fmt"
	"strings"
	"testing"
)

// testQuoting mirrors the db package's qualifiedTable/quoteColumn closures
// for a connection whose current database is "db" (mysql), "public"
// (postgres) or "dbo" (sqlserver).
func testQuoting(f Family) (QualifyFunc, Quoter) {
	quote := func(name string) string {
		switch f {
		case FamilyMySQL:
			return "`" + strings.ReplaceAll(name, "`", "``") + "`"
		case FamilySQLServer:
			return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
		}
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
	qualify := func(schema, table string) string {
		if schema == "" {
			switch f {
			case FamilyMySQL:
				schema = "db"
			case FamilyPostgres:
				schema = "public"
			case FamilySQLServer:
				schema = "dbo"
			case FamilySQLite:
				return quote(table)
			}
		}
		if schema == "" {
			return quote(table)
		}
		return quote(schema) + "." + quote(table)
	}
	return qualify, quote
}

func mustClassify(t *testing.T, f Family, sql string) Statement {
	t.Helper()
	st := Classify(f, sql)
	if !st.Rewritable {
		t.Fatalf("Classify(%s, %q) not rewritable: %+v", f, sql, st)
	}
	return st
}

func TestSnapshotSQL(t *testing.T) {
	cases := []struct {
		family    Family
		sql       string
		pk        []string
		forUpdate bool
		want      string
	}{
		{FamilyMySQL, "UPDATE t SET a=1 WHERE b=2", []string{"id"}, true,
			"SELECT `id`, `a` FROM `db`.`t` WHERE b=2 FOR UPDATE"},
		{FamilyPostgres, "DELETE FROM t WHERE b=2", []string{"id"}, true,
			`SELECT * FROM "public"."t" WHERE b=2 FOR UPDATE`},
		{FamilySQLServer, "UPDATE t SET a=1 WHERE b=2", []string{"id"}, true,
			"SELECT [id], [a] FROM [dbo].[t] WITH (UPDLOCK, HOLDLOCK) WHERE b=2"},
		{FamilySQLServer, "UPDATE t SET a=1 WHERE b=2", []string{"id"}, false,
			"SELECT [id], [a] FROM [dbo].[t] WHERE b=2"},
		{FamilySQLServer, "DELETE FROM t", []string{"id"}, true,
			"SELECT * FROM [dbo].[t] WITH (UPDLOCK, HOLDLOCK)"},
		{FamilyOracle, "DELETE FROM s.t WHERE b=2", []string{"ID"}, true,
			`SELECT * FROM "s"."t" WHERE b=2 FOR UPDATE`},
		{FamilyDM, "UPDATE t SET a=1 WHERE b=2", []string{"ID"}, true,
			`SELECT "ID", "a" FROM "t" WHERE b=2 FOR UPDATE`},
		{FamilySQLite, "UPDATE t SET a=1 WHERE b=2", []string{"id"}, true,
			`SELECT "id", "a" FROM "t" WHERE b=2`},
		{FamilyANSI, "DELETE FROM t WHERE b=2", []string{"id"}, true,
			`SELECT * FROM "t" WHERE b=2`},
		// forUpdate=false never locks.
		{FamilyMySQL, "UPDATE t SET a=1 WHERE b=2", []string{"id"}, false,
			"SELECT `id`, `a` FROM `db`.`t` WHERE b=2"},
		// pk ∪ SetColumns: a SET column that is also the key appears once.
		{FamilyMySQL, "UPDATE t SET id=5, a=1, b=2 WHERE c=3", []string{"id"}, false,
			"SELECT `id`, `a`, `b` FROM `db`.`t` WHERE c=3"},
		// Exact-match dedupe: "A" differs from pk "a" on Postgres and is kept.
		{FamilyPostgres, `UPDATE t SET "A"=1, a=2 WHERE b=2`, []string{"a"}, false,
			`SELECT "a", "A" FROM "public"."t" WHERE b=2`},
		// Composite key, no pk at all.
		{FamilyPostgres, "UPDATE t SET a=1 WHERE b=2", []string{"k1", "k2"}, false,
			`SELECT "k1", "k2", "a" FROM "public"."t" WHERE b=2`},
		{FamilyPostgres, "UPDATE t SET a=1 WHERE b=2", nil, false,
			`SELECT "a" FROM "public"."t" WHERE b=2`},
		// No WHERE: no WHERE clause emitted.
		{FamilyPostgres, "DELETE FROM public.t", []string{"id"}, false,
			`SELECT * FROM "public"."t"`},
		// Alias: FROM <t> <alias>, select list qualified with it.
		{FamilyMySQL, "UPDATE `Order` o SET o.status = 'x;y' WHERE o.id = 3", []string{"id"}, true,
			"SELECT o.`id`, o.`status` FROM `db`.`Order` o WHERE o.id = 3 FOR UPDATE"},
		{FamilySQLServer, "DELETE FROM [dbo].[Order] AS o WHERE o.id = 3", []string{"id"}, true,
			"SELECT o.* FROM [dbo].[Order] o WITH (UPDLOCK, HOLDLOCK) WHERE o.id = 3"},
		{FamilyOracle, "UPDATE t x SET x.a = 1 WHERE x.b = 2", []string{"ID"}, true,
			`SELECT x."ID", x."a" FROM "t" x WHERE x.b = 2 FOR UPDATE`},
		// WHERE text spliced verbatim, literals intact.
		{FamilyPostgres, "DELETE FROM t WHERE note = 'a;b -- c' AND n = $$x$$", []string{"id"}, false,
			`SELECT * FROM "public"."t" WHERE note = 'a;b -- c' AND n = $$x$$`},
	}
	for _, c := range cases {
		st := mustClassify(t, c.family, c.sql)
		qualify, quote := testQuoting(c.family)
		got, ok := SnapshotSQL(c.family, st, qualify, quote, c.pk, c.forUpdate)
		if !ok || got != c.want {
			t.Errorf("SnapshotSQL(%s, %q) = %q, %v; want %q", c.family, c.sql, got, ok, c.want)
		}
	}
}

func TestSnapshotSQLRejectsNonRewritable(t *testing.T) {
	qualify, quote := testQuoting(FamilyPostgres)
	for _, sql := range []string{
		"UPDATE t SET a=1 FROM u WHERE t.id=u.id",
		"INSERT INTO t VALUES (1)",
		"SELECT * FROM t",
	} {
		st := Classify(FamilyPostgres, sql)
		if got, ok := SnapshotSQL(FamilyPostgres, st, qualify, quote, []string{"id"}, true); ok || got != "" {
			t.Errorf("SnapshotSQL(%q) = %q, %v; want \"\", false", sql, got, ok)
		}
		if got, ok := CountSQL(FamilyPostgres, st, qualify); ok || got != "" {
			t.Errorf("CountSQL(%q) = %q, %v; want \"\", false", sql, got, ok)
		}
	}
	// Rewritable=false on an otherwise filled statement is still refused.
	st := mustClassify(t, FamilyPostgres, "DELETE FROM t WHERE b=2")
	st.Rewritable = false
	if got, ok := SnapshotSQL(FamilyPostgres, st, qualify, quote, nil, false); ok || got != "" {
		t.Errorf("SnapshotSQL(Rewritable=false) = %q, %v", got, ok)
	}
	if got, ok := CountSQL(FamilyPostgres, st, qualify); ok || got != "" {
		t.Errorf("CountSQL(Rewritable=false) = %q, %v", got, ok)
	}
}

func TestCountSQL(t *testing.T) {
	cases := []struct {
		family Family
		sql    string
		want   string
	}{
		{FamilyMySQL, "UPDATE t SET a=1 WHERE b=2", "SELECT COUNT(*) FROM `db`.`t` WHERE b=2"},
		{FamilyPostgres, "DELETE FROM public.t", `SELECT COUNT(*) FROM "public"."t"`},
		{FamilySQLServer, "DELETE FROM t", "SELECT COUNT(*) FROM [dbo].[t]"},
		{FamilyOracle, "DELETE FROM t WHERE 1=1", `SELECT COUNT(*) FROM "t" WHERE 1=1`},
		{FamilyMySQL, "UPDATE `Order` o SET o.status = 'x;y' WHERE o.id = 3",
			"SELECT COUNT(*) FROM `db`.`Order` o WHERE o.id = 3"},
		{FamilySQLServer, "UPDATE t AS x SET a = 1 WHERE x.b = 'it''s'",
			"SELECT COUNT(*) FROM [dbo].[t] x WHERE x.b = 'it''s'"},
	}
	for _, c := range cases {
		st := mustClassify(t, c.family, c.sql)
		qualify, _ := testQuoting(c.family)
		got, ok := CountSQL(c.family, st, qualify)
		if !ok || got != c.want {
			t.Errorf("CountSQL(%s, %q) = %q, %v; want %q", c.family, c.sql, got, ok, c.want)
		}
	}
}

func TestExplainSQL(t *testing.T) {
	cases := []struct {
		family Family
		want   string
		ok     bool
	}{
		{FamilyMySQL, "EXPLAIN DELETE FROM t WHERE b=2", true},
		{FamilyPostgres, "EXPLAIN DELETE FROM t WHERE b=2", true},
		{FamilySQLite, "EXPLAIN DELETE FROM t WHERE b=2", true},
		{FamilyANSI, "EXPLAIN DELETE FROM t WHERE b=2", true},
		{FamilyOracle, "EXPLAIN PLAN FOR DELETE FROM t WHERE b=2", true},
		{FamilyDM, "EXPLAIN PLAN FOR DELETE FROM t WHERE b=2", true},
		{FamilySQLServer, "", false},
		{FamilyNone, "", false},
	}
	for _, c := range cases {
		got, ok := ExplainSQL(c.family, "  DELETE FROM t WHERE b=2 ; ")
		if ok != c.ok || got != c.want {
			t.Errorf("ExplainSQL(%s) = %q, %v; want %q, %v", c.family, got, ok, c.want, c.ok)
		}
	}
	if got, ok := ExplainSQL(FamilyMySQL, " ; "); ok || got != "" {
		t.Errorf("ExplainSQL(empty) = %q, %v", got, ok)
	}
}

func TestByKeysSQL(t *testing.T) {
	pg := func(n int) string { return fmt.Sprintf("$%d", n) }
	q := func(int) string { return "?" }
	cases := []struct {
		family   Family
		table    TableRef
		pk       []string
		columns  []string
		keyCount int
		ph       func(int) string
		want     string
	}{
		{FamilyPostgres, TableRef{Name: "t"}, []string{"k1", "k2"}, []string{"k1", "k2", "a"}, 2, pg,
			`SELECT "k1", "k2", "a" FROM "public"."t" WHERE ("k1" = $1 AND "k2" = $2) OR ("k1" = $3 AND "k2" = $4)`},
		{FamilyMySQL, TableRef{Schema: "s", Name: "t"}, []string{"id"}, nil, 3, q,
			"SELECT * FROM `s`.`t` WHERE (`id` = ?) OR (`id` = ?) OR (`id` = ?)"},
		{FamilySQLServer, TableRef{Name: "Order"}, []string{"id"}, []string{"id", "status"}, 1,
			func(n int) string { return fmt.Sprintf("@p%d", n) },
			"SELECT [id], [status] FROM [dbo].[Order] WHERE ([id] = @p1)"},
	}
	for _, c := range cases {
		qualify, quote := testQuoting(c.family)
		got := ByKeysSQL(c.family, qualify, quote, c.table, c.pk, c.columns, c.keyCount, c.ph)
		if got != c.want {
			t.Errorf("ByKeysSQL(%s, %v) = %q; want %q", c.family, c.table, got, c.want)
		}
	}
}

func TestInsertKeysSQL(t *testing.T) {
	cases := []struct {
		family Family
		sql    string
		pk     []string
		want   string // "" means not rewritable
	}{
		{FamilyPostgres, "INSERT INTO t (a) VALUES (1), (2);", []string{"id"}, `INSERT INTO t (a) VALUES (1), (2) RETURNING "id"`},
		{FamilyPostgres, "INSERT INTO t DEFAULT VALUES", []string{"k1", "k2"}, `INSERT INTO t DEFAULT VALUES RETURNING "k1", "k2"`},
		{FamilySQLite, "INSERT INTO t (a) SELECT a FROM u -- tail", []string{"id"}, `INSERT INTO t (a) SELECT a FROM u RETURNING "id"`},
		{FamilySQLServer, "INSERT INTO dbo.t (a, b) VALUES (1, 'x)')", []string{"id"}, "INSERT INTO dbo.t (a, b) OUTPUT INSERTED.[id] VALUES (1, 'x)')"},
		{FamilySQLServer, "INSERT INTO t DEFAULT VALUES", []string{"a", "b"}, "INSERT INTO t OUTPUT INSERTED.[a], INSERTED.[b] DEFAULT VALUES"},
		{FamilySQLServer, "INSERT INTO t SELECT a FROM u", []string{"id"}, "INSERT INTO t OUTPUT INSERTED.[id] SELECT a FROM u"},
		{FamilySQLServer, "INSERT INTO t WITH (TABLOCK) (a) VALUES (1)", []string{"id"}, ""},
		// Already returning: left alone (the user's RETURNING is their own result).
		{FamilyPostgres, "INSERT INTO t (a) VALUES (1) RETURNING id", []string{"id"}, ""},
		{FamilySQLServer, "INSERT INTO t OUTPUT inserted.id VALUES (1)", []string{"id"}, ""},
		// Families without a returning clause, no key, not a plain insert.
		{FamilyMySQL, "INSERT INTO t (a) VALUES (1)", []string{"id"}, ""},
		{FamilyOracle, "INSERT INTO t (a) VALUES (1)", []string{"ID"}, ""},
		{FamilyPostgres, "INSERT INTO t (a) VALUES (1)", nil, ""},
		{FamilyPostgres, "INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING", []string{"id"}, ""},
		{FamilyPostgres, "UPDATE t SET a = 1", []string{"id"}, ""},
	}
	for _, c := range cases {
		st := Classify(c.family, c.sql)
		_, quote := testQuoting(c.family)
		got, ok := InsertKeysSQL(c.family, st, quote, c.pk)
		if ok != (c.want != "") || got != c.want {
			t.Errorf("InsertKeysSQL(%s, %q) = %q, %v; want %q", c.family, c.sql, got, ok, c.want)
		}
	}
}
