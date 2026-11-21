package sqlsafety

import (
	"reflect"
	"sort"
	"testing"
)

type classifyCase struct {
	family      Family
	sql         string
	kind        Kind
	table       *TableRef
	alias       string
	setColumns  []string
	where       string
	rewritable  bool
	destructive bool
	reasons     []Reason
}

func TestClassify(t *testing.T) {
	cases := []classifyCase{
		// Brief cases.
		{family: FamilyMySQL, sql: "UPDATE `Order` o SET o.status = 'x;y' WHERE o.id = 3", kind: KindData,
			table: &TableRef{Name: "Order"}, alias: "o", setColumns: []string{"status"}, where: "o.id = 3", rewritable: true},
		{family: FamilyPostgres, sql: "DELETE FROM public.t", kind: KindData,
			table: &TableRef{Schema: "public", Name: "t"}, rewritable: true, reasons: []Reason{ReasonNoWhere}},
		{family: FamilyPostgres, sql: "UPDATE t SET a=1 FROM u WHERE t.id=u.id", kind: KindData,
			reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", kind: KindData,
			reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "WITH x AS (SELECT 1) SELECT * FROM x", kind: KindRead},
		{family: FamilyMySQL, sql: "SELECT * INTO OUTFILE '/tmp/a' FROM t", kind: KindData,
			reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "EXEC dbo.purge", kind: KindProcedure},
		{family: FamilyOracle, sql: "DELETE FROM t WHERE 1=1", kind: KindData,
			table: &TableRef{Name: "t"}, where: "1=1", rewritable: true, reasons: []Reason{ReasonAlwaysTrue}},
		{family: FamilyNone, sql: "ALTER TABLE t DELETE WHERE id=1", kind: KindData},
		{family: FamilyMySQL, sql: "INSERT INTO t SELECT * FROM u", kind: KindInsert, table: &TableRef{Name: "t"}},
		{family: FamilyMySQL, sql: "TRUNCATE TABLE t", kind: KindStructure, table: &TableRef{Name: "t"}, destructive: true},
		{family: FamilyMySQL, sql: "EXPLAIN ANALYZE DELETE FROM t", kind: KindData, reasons: []Reason{ReasonUnparsed}},

		// UPDATE / DELETE parsing.
		{family: FamilySQLServer, sql: "UPDATE [dbo].[t] SET [a] = 1, b = N'x' WHERE id = 2", kind: KindData,
			table: &TableRef{Schema: "dbo", Name: "t"}, setColumns: []string{"a", "b"}, where: "id = 2", rewritable: true},
		{family: FamilyPostgres, sql: `UPDATE "S"."T" AS x SET "Col" = 1 WHERE x.id = 2 RETURNING *`, kind: KindData,
			table: &TableRef{Schema: "S", Name: "T"}, alias: "x", setColumns: []string{"Col"}, where: "x.id = 2", rewritable: true},
		// ONLY excludes child/partition rows the snapshot would include.
		{family: FamilyPostgres, sql: "UPDATE ONLY t SET a=1 WHERE b=2", kind: KindData,
			reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "DELETE FROM ONLY t WHERE b=2", kind: KindData,
			reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "DELETE FROM ONLY t", kind: KindData,
			reasons: []Reason{ReasonNoWhere, ReasonUnparsed}},
		{family: FamilyPostgres, sql: "DELETE FROM t WHERE id = 1 /* tail */ RETURNING id; ", kind: KindData,
			table: &TableRef{Name: "t"}, where: "id = 1", rewritable: true},
		{family: FamilyMySQL, sql: "DELETE FROM t WHERE id = 1 -- note", kind: KindData,
			table: &TableRef{Name: "t"}, where: "id = 1", rewritable: true},
		{family: FamilyMySQL, sql: "UPDATE t SET a = CASE WHEN b > 1 THEN 'x' ELSE 'y' END, c = (SELECT 1 FROM u) WHERE t.id = 1", kind: KindData,
			table: &TableRef{Name: "t"}, setColumns: []string{"a", "c"}, where: "t.id = 1", rewritable: true},
		{family: FamilyMySQL, sql: "UPDATE LOW_PRIORITY IGNORE t SET a = 1", kind: KindData,
			table: &TableRef{Name: "t"}, setColumns: []string{"a"}, rewritable: true, reasons: []Reason{ReasonNoWhere}},
		{family: FamilyOracle, sql: "DELETE t WHERE id = 1 OR 1 = 1", kind: KindData,
			table: &TableRef{Name: "t"}, where: "id = 1 OR 1 = 1", rewritable: true, reasons: []Reason{ReasonAlwaysTrue}},
		{family: FamilyPostgres, sql: "DELETE FROM t WHERE (TRUE)", kind: KindData,
			table: &TableRef{Name: "t"}, where: "(TRUE)", rewritable: true, reasons: []Reason{ReasonAlwaysTrue}},
		{family: FamilyMySQL, sql: "DELETE FROM t WHERE 'a'='a' AND 1", kind: KindData,
			table: &TableRef{Name: "t"}, where: "'a'='a' AND 1", rewritable: true, reasons: []Reason{ReasonAlwaysTrue}},
		{family: FamilyMySQL, sql: "DELETE FROM t WHERE 1 = 2", kind: KindData,
			table: &TableRef{Name: "t"}, where: "1 = 2", rewritable: true},
		{family: FamilyMySQL, sql: "DELETE FROM t WHERE id IN (SELECT id FROM t WHERE x = 1)", kind: KindData,
			reasons: []Reason{ReasonUnparsed}},
		{family: FamilyMySQL, sql: "UPDATE t SET a = 1 WHERE b = 2 LIMIT 1", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyMySQL, sql: "UPDATE t SET a = 1 ORDER BY id", kind: KindData, reasons: []Reason{ReasonUnparsed, ReasonNoWhere}},
		{family: FamilyMySQL, sql: "UPDATE a, b SET a.x = b.x WHERE a.id = b.id", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyMySQL, sql: "UPDATE a JOIN b ON a.id = b.id SET a.x = 1 WHERE b.y = 2", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyMySQL, sql: "DELETE a FROM a JOIN b ON a.id = b.id WHERE b.y = 2", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "DELETE FROM t USING u WHERE t.id = u.id", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "UPDATE TOP (5) t SET a = 1 WHERE b = 2", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "DELETE TOP (5) FROM t WHERE b = 2", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "UPDATE t SET (a, b) = (1, 2) WHERE id = 1", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "DELETE FROM t WHERE CURRENT OF c", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "WITH x AS (SELECT 1 AS id) UPDATE t SET a = 1 WHERE id IN (SELECT id FROM x)", kind: KindData,
			reasons: []Reason{ReasonUnparsed}},
		{family: FamilyNone, sql: "DELETE FROM t", kind: KindData},
		{family: FamilyPostgres, sql: "UPDATE t SET a = 1 WHERE b IS DISTINCT FROM c", kind: KindData,
			table: &TableRef{Name: "t"}, setColumns: []string{"a"}, where: "b IS DISTINCT FROM c", rewritable: true},
		{family: FamilyOracle, sql: "UPDATE t SET a = 1 WHERE id = :id RETURNING a INTO :v", kind: KindData,
			table: &TableRef{Name: "t"}, setColumns: []string{"a"}, where: "id = :id", rewritable: true},

		// Other data statements.
		{family: FamilyMySQL, sql: "INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = 2", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyMySQL, sql: "INSERT INTO t (a) VALUES (1)", kind: KindInsert, table: &TableRef{Name: "t"}},
		{family: FamilyMySQL, sql: "REPLACE INTO t VALUES (1)", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		// SQLite INSERT OR REPLACE deletes conflicting rows first: a data change, not a clean insert.
		{family: FamilySQLite, sql: "INSERT OR REPLACE INTO t (id, a) VALUES (1, 2)", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLite, sql: "insert or replace into t values (1)", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLite, sql: "INSERT OR IGNORE INTO t VALUES (1)", kind: KindInsert},
		// A data-modifying CTE inside the INSERT's query changes another table.
		{family: FamilyPostgres, sql: "INSERT INTO t WITH d AS (DELETE FROM u RETURNING *) SELECT * FROM d", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "INSERT INTO t WITH d AS (UPDATE u SET a = 1 RETURNING id) SELECT id FROM d", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "INSERT INTO t WITH d AS (INSERT INTO u VALUES (1) RETURNING id) SELECT id FROM d", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "INSERT INTO t SELECT id FROM u FOR UPDATE", kind: KindInsert, table: &TableRef{Name: "t"}},
		{family: FamilyOracle, sql: "MERGE INTO t USING u ON (t.id = u.id) WHEN MATCHED THEN UPDATE SET t.a = u.a", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyANSI, sql: "UPSERT INTO t VALUES (1)", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "COPY t FROM STDIN", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "COPY t TO STDOUT", kind: KindUnknown},
		{family: FamilyMySQL, sql: "LOAD DATA INFILE 'a.csv' INTO TABLE t", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "SELECT * INTO t2 FROM t", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyNone, sql: "ALTER TABLE db.t ON CLUSTER c UPDATE a = 1 WHERE id = 1", kind: KindData},

		// Structure.
		{family: FamilyPostgres, sql: "DROP TABLE IF EXISTS public.t CASCADE", kind: KindStructure,
			table: &TableRef{Schema: "public", Name: "t"}, destructive: true},
		{family: FamilyMySQL, sql: "DROP TEMPORARY TABLE t", kind: KindStructure, table: &TableRef{Name: "t"}, destructive: true},
		{family: FamilyMySQL, sql: "DROP TABLE a, b", kind: KindStructure, destructive: true},
		{family: FamilyPostgres, sql: "TRUNCATE ONLY s.t RESTART IDENTITY", kind: KindStructure,
			table: &TableRef{Schema: "s", Name: "t"}, destructive: true},
		{family: FamilyPostgres, sql: "TRUNCATE t", kind: KindStructure, table: &TableRef{Name: "t"}, destructive: true},
		{family: FamilyMySQL, sql: "TRUNCATE TABLE t PARTITION (p0, p1)", kind: KindStructure, table: &TableRef{Name: "t"},
			destructive: true, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "TRUNCATE TABLE t WITH (PARTITIONS (2, 4 TO 6))", kind: KindStructure, table: &TableRef{Name: "t"},
			destructive: true, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyOracle, sql: "DROP TABLE t CASCADE CONSTRAINTS PURGE", kind: KindStructure, table: &TableRef{Name: "t"}, destructive: true},
		{family: FamilyMySQL, sql: "DROP VIEW v", kind: KindStructure},
		{family: FamilyMySQL, sql: "CREATE TABLE t (id INT)", kind: KindStructure},
		{family: FamilyMySQL, sql: "ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES u (id) ON DELETE CASCADE", kind: KindStructure},
		{family: FamilyMySQL, sql: "GRANT SELECT ON t TO u", kind: KindStructure},
		{family: FamilyOracle, sql: "REVOKE SELECT ON t FROM u", kind: KindStructure},
		{family: FamilyMySQL, sql: "RENAME TABLE a TO b", kind: KindStructure},
		{family: FamilyPostgres, sql: "COMMENT ON TABLE t IS 'x'", kind: KindStructure},

		// Procedures and scripts.
		{family: FamilyMySQL, sql: "CALL p(1)", kind: KindProcedure},
		{family: FamilyOracle, sql: "EXECUTE p", kind: KindProcedure},
		{family: FamilyOracle, sql: "BEGIN DELETE FROM t; END;", kind: KindProcedure},
		{family: FamilyOracle, sql: "DECLARE v NUMBER; BEGIN v := 1; END;", kind: KindProcedure},
		{family: FamilySQLServer, sql: "BEGIN DELETE FROM t; END", kind: KindProcedure},
		{family: FamilyPostgres, sql: "DO $$BEGIN DELETE FROM t; END$$", kind: KindProcedure},
		{family: FamilyPostgres, sql: "BEGIN", kind: KindScript},
		{family: FamilyMySQL, sql: "BEGIN WORK", kind: KindScript},
		{family: FamilySQLServer, sql: "BEGIN TRAN", kind: KindScript},
		{family: FamilyMySQL, sql: "START TRANSACTION", kind: KindScript},
		{family: FamilyMySQL, sql: "COMMIT", kind: KindScript},
		{family: FamilyMySQL, sql: "ROLLBACK", kind: KindScript},

		// Reads.
		{family: FamilyMySQL, sql: "SELECT * FROM t FOR UPDATE", kind: KindRead},
		{family: FamilyMySQL, sql: "SHOW TABLES", kind: KindRead},
		{family: FamilyMySQL, sql: "DESC t", kind: KindRead},
		{family: FamilyMySQL, sql: "DESCRIBE t", kind: KindRead},
		{family: FamilyMySQL, sql: "EXPLAIN DELETE FROM t", kind: KindRead},
		{family: FamilyPostgres, sql: "EXPLAIN (ANALYZE, BUFFERS) UPDATE t SET a = 1", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "EXPLAIN ANALYZE SELECT 1", kind: KindUnknown},
		{family: FamilySQLite, sql: "PRAGMA table_info(t)", kind: KindRead},
		{family: FamilySQLite, sql: "PRAGMA foreign_keys = OFF", kind: KindUnknown},
		{family: FamilyPostgres, sql: "VALUES (1), (2)", kind: KindRead},
		{family: FamilyPostgres, sql: "TABLE t", kind: KindRead},
		{family: FamilyPostgres, sql: "WITH a AS (SELECT * FROM t FOR UPDATE) SELECT * FROM a", kind: KindRead},
		{family: FamilyPostgres, sql: "WITH a AS (INSERT INTO t VALUES (1) RETURNING id) SELECT * FROM a", kind: KindInsert},
		{family: FamilyNone, sql: "SELECT 1", kind: KindRead},
		{family: FamilyMySQL, sql: "  -- lead\n SELECT 1 ;", kind: KindRead},

		// Alias.
		{family: FamilyMySQL, sql: "DELETE FROM t AS x WHERE x.id = 1", kind: KindData,
			table: &TableRef{Name: "t"}, alias: "x", where: "x.id = 1", rewritable: true},
		{family: FamilySQLServer, sql: "UPDATE t SET a = 1 WHERE id = 1", kind: KindData,
			table: &TableRef{Name: "t"}, setColumns: []string{"a"}, where: "id = 1", rewritable: true},

		// Read-bypass attempts (review round 1): none of these may be read.
		{family: FamilySQLServer, sql: "SELECT CASE WHEN 1=1 THEN 'a' END FOR XML PATH(''); DELETE FROM t", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT CASE WHEN 1=1 THEN 'a' END AS v FOR JSON PATH; DELETE FROM t", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 DELETE FROM t", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 UPDATE t SET a = 1", kind: KindUnknown},
		{family: FamilyPostgres, sql: "EXPLAIN ANALYSE DELETE FROM t", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "EXPLAIN (ANALYSE) DELETE FROM t", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyPostgres, sql: "SELECT 1; DELETE FROM t", kind: KindUnknown},
		{family: FamilyPostgres, sql: "SELECT 1; SELECT 2;", kind: KindUnknown},
		{family: FamilyMySQL, sql: "SELECT 1 /*!; DELETE FROM t */", kind: KindUnknown},
		{family: FamilyMySQL, sql: "SELECT 1 /*! INTO OUTFILE '/tmp/x' */", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyMySQL, sql: "SELECT 1 /*!50000 FROM dual */", kind: KindUnknown},
		{family: FamilyMySQL, sql: "DELETE FROM t WHERE id = 1 /*! OR 1=1 */", kind: KindData, reasons: []Reason{ReasonUnparsed, ReasonAlwaysTrue}},
		{family: FamilyMySQL, sql: "/*!40101 SET NAMES utf8 */", kind: KindUnknown},
		{family: FamilyOracle, sql: "BEGIN CASE x WHEN 1 THEN NULL; END CASE; END; DELETE FROM t", kind: KindUnknown},
		{family: FamilyOracle, sql: "BEGIN CASE x WHEN 1 THEN NULL; END CASE; END;", kind: KindProcedure},
		// Semicolon-less second statements (review round 2): never rewritable.
		{family: FamilySQLServer, sql: "UPDATE t SET a=1 WHERE id=1 DELETE FROM u", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "DELETE FROM t WHERE id=1 DROP TABLE u", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "DELETE FROM t WHERE id=1 UPDATE u SET a=1", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "DELETE FROM t WHERE id=1 EXEC p", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "DELETE t WHERE id=1 INSERT INTO u VALUES(1)", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "UPDATE t SET a=1 EXEC p", kind: KindData, reasons: []Reason{ReasonUnparsed, ReasonNoWhere}},
		{family: FamilySQLServer, sql: "DELETE FROM t WHERE id=1 SELECT 1", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "DELETE FROM t SHUTDOWN", kind: KindData, reasons: []Reason{ReasonUnparsed, ReasonNoWhere}},
		{family: FamilyPostgres, sql: "DELETE FROM t WHERE id=1 DELETE FROM u", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilyMySQL, sql: "UPDATE t SET a = IF(b > 1, 2, 3) WHERE id IN (SELECT id FROM u)", kind: KindData,
			table: &TableRef{Name: "t"}, setColumns: []string{"a"}, where: "id IN (SELECT id FROM u)", rewritable: true},
		{family: FamilySQLServer, sql: "SELECT 1 SHUTDOWN", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 KILL 53", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 DBCC SHRINKDATABASE(x)", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 BACKUP DATABASE x TO DISK = 'a'", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 RESTORE DATABASE x FROM DISK = 'a'", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 DENY SELECT ON t TO u", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 DISABLE TRIGGER tr ON t", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 BEGIN TRAN", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 WRITETEXT t.c @p 'x'", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 DECLARE @x int", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 SET @x = 1", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 IF 1 = 1 PRINT 'x'", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 FETCH NEXT FROM c", kind: KindUnknown},
		// Review round 3: Service Broker and other T-SQL statements, linked-server pass-through.
		{family: FamilySQLServer, sql: "SELECT 1 RECEIVE * FROM q", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 SEND ON CONVERSATION @h (N'x')", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 END CONVERSATION @h WITH CLEANUP", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 MOVE CONVERSATION @h TO @g", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 GET CONVERSATION GROUP @g FROM q", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 ADD SIGNATURE TO p BY CERTIFICATE c", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 SETUSER 'x'", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 REVERT", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT 1 IF(1=1) RECEIVE * FROM q", kind: KindUnknown},
		{family: FamilySQLServer, sql: "DELETE FROM t WHERE id=1 RECEIVE * FROM q", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "SELECT * FROM OPENQUERY(srv, 'SELECT 1')", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT * FROM t WHERE id IN (SELECT id FROM OPENROWSET('SQLNCLI', 'x', 'SELECT 1'))", kind: KindUnknown},
		{family: FamilySQLServer, sql: "SELECT * FROM OPENDATASOURCE('SQLNCLI', 'x').db.dbo.t", kind: KindUnknown},
		{family: FamilySQLServer, sql: "DELETE FROM t WHERE id IN (SELECT id FROM OPENQUERY(srv, 'SELECT 1'))", kind: KindData, reasons: []Reason{ReasonUnparsed}},
		{family: FamilySQLServer, sql: "SELECT CASE WHEN a = 1 THEN 'x' END AS v FROM t", kind: KindRead},
		{family: FamilySQLServer, sql: "SELECT * FROM t ORDER BY id OFFSET 5 ROWS FETCH NEXT 5 ROWS ONLY", kind: KindRead},
		{family: FamilyPostgres, sql: "SELECT * FROM t FETCH FIRST 5 ROWS ONLY", kind: KindRead},
		{family: FamilyMySQL, sql: "SELECT IF(a > 1, 'x', 'y') FROM t", kind: KindRead},
		{family: FamilyMySQL, sql: "SELECT CAST(a AS CHAR CHARACTER SET utf8mb4) FROM t", kind: KindRead},
		{family: FamilyPostgres, sql: "SELECT * FROM t FOR NO KEY UPDATE", kind: KindRead},
		{family: FamilySQLite, sql: "PRAGMA main.table_info('t')", kind: KindRead},
		{family: FamilySQLite, sql: "PRAGMA journal_mode", kind: KindRead},
		{family: FamilySQLite, sql: "PRAGMA integrity_check(10)", kind: KindRead},
		{family: FamilySQLite, sql: "PRAGMA journal_mode(WAL)", kind: KindUnknown},
		{family: FamilySQLite, sql: "PRAGMA journal_mode = WAL", kind: KindUnknown},
		{family: FamilySQLite, sql: "PRAGMA user_version = 3", kind: KindUnknown},
		{family: FamilySQLite, sql: "PRAGMA writable_schema", kind: KindUnknown},
		{family: FamilySQLite, sql: "PRAGMA optimize", kind: KindUnknown},
		{family: FamilySQLite, sql: "PRAGMA table_info(t) x", kind: KindUnknown},

		// Unknown.
		{family: FamilyPostgres, sql: "VACUUM", kind: KindUnknown},
		{family: FamilyPostgres, sql: "", kind: KindUnknown},
	}
	for _, tc := range cases {
		t.Run(string(tc.family)+"/"+tc.sql, func(t *testing.T) {
			st := Classify(tc.family, tc.sql)
			if st.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", st.Kind, tc.kind)
			}
			if !reflect.DeepEqual(st.Table, tc.table) {
				t.Errorf("Table = %+v, want %+v", st.Table, tc.table)
			}
			if !equalStrings(st.SetColumns, tc.setColumns) {
				t.Errorf("SetColumns = %q, want %q", st.SetColumns, tc.setColumns)
			}
			if st.Alias != tc.alias {
				t.Errorf("Alias = %q, want %q", st.Alias, tc.alias)
			}
			if st.Where != tc.where {
				t.Errorf("Where = %q, want %q", st.Where, tc.where)
			}
			if st.Rewritable != tc.rewritable {
				t.Errorf("Rewritable = %v, want %v", st.Rewritable, tc.rewritable)
			}
			if st.Destructive != tc.destructive {
				t.Errorf("Destructive = %v, want %v", st.Destructive, tc.destructive)
			}
			if !equalReasons(st.Reasons, tc.reasons) {
				t.Errorf("Reasons = %q, want %q", st.Reasons, tc.reasons)
			}
			if got, want := IsReadOnly(tc.family, tc.sql), tc.kind == KindRead; got != want {
				t.Errorf("IsReadOnly = %v, want %v", got, want)
			}
		})
	}
}

func TestClassifyKeepsText(t *testing.T) {
	st := Classify(FamilyMySQL, "  SELECT 1  ")
	if st.Text != "SELECT 1" {
		t.Fatalf("Text = %q", st.Text)
	}
}

func TestSplitRespectsQuotesAndComments(t *testing.T) {
	cases := []struct {
		family Family
		sql    string
		want   []string
	}{
		{FamilyPostgres, "UPDATE t SET a='1;2'; /* ; */ DELETE FROM u WHERE b = $$x;y$$;",
			[]string{"UPDATE t SET a='1;2'", "/* ; */ DELETE FROM u WHERE b = $$x;y$$"}},
		{FamilyPostgres, "-- a;\nSELECT 1", []string{"-- a;\nSELECT 1"}},
		{FamilyPostgres, ";; ; -- only comment", nil},
		{FamilyPostgres, "SELECT $tag$ a;$$; $tag$; SELECT $1; SELECT 2", []string{"SELECT $tag$ a;$$; $tag$", "SELECT $1", "SELECT 2"}},
		{FamilyPostgres, "/* /* ; */ ; */ SELECT 1; SELECT 2", []string{"/* /* ; */ ; */ SELECT 1", "SELECT 2"}},
		{FamilyPostgres, `SELECT E'a\';b'; SELECT "x;y"`, []string{`SELECT E'a\';b'`, `SELECT "x;y"`}},
		{FamilyPostgres, "SELECT 'a''b;c'; SELECT 2", []string{"SELECT 'a''b;c'", "SELECT 2"}},
		{FamilyPostgres, "BEGIN; UPDATE t SET a = 1; COMMIT;", []string{"BEGIN", "UPDATE t SET a = 1", "COMMIT"}},
		{FamilyPostgres, "SELECT CASE WHEN a THEN 1 END; END; SELECT 2", []string{"SELECT CASE WHEN a THEN 1 END", "END", "SELECT 2"}},
		{FamilyMySQL, `SELECT 'a\';b'; SELECT "c;d"; SELECT ` + "`e;f`", []string{`SELECT 'a\';b'`, `SELECT "c;d"`, "SELECT `e;f`"}},
		{FamilyMySQL, "# c;\nSELECT 1; -- d;\nSELECT 2", []string{"# c;\nSELECT 1", "-- d;\nSELECT 2"}},
		{FamilyMySQL, "/*!40101 SET NAMES utf8 */; SELECT 1", []string{"/*!40101 SET NAMES utf8 */", "SELECT 1"}},
		{FamilyMySQL, "BEGIN; DELETE FROM t; COMMIT", []string{"BEGIN", "DELETE FROM t", "COMMIT"}},
		{FamilyMySQL, "CREATE DEFINER=`root`@`%` PROCEDURE p() BEGIN SELECT 1; IF x THEN SELECT 2; END IF; END; SELECT 3",
			[]string{"CREATE DEFINER=`root`@`%` PROCEDURE p() BEGIN SELECT 1; IF x THEN SELECT 2; END IF; END", "SELECT 3"}},
		{FamilySQLite, "CREATE TRIGGER tr AFTER INSERT ON t BEGIN UPDATE u SET a = 1; END; SELECT [x;y]",
			[]string{"CREATE TRIGGER tr AFTER INSERT ON t BEGIN UPDATE u SET a = 1; END", "SELECT [x;y]"}},
		{FamilySQLServer, "SELECT [a;b] FROM t; SELECT 1", []string{"SELECT [a;b] FROM t", "SELECT 1"}},
		{FamilySQLServer, "BEGIN TRAN; UPDATE t SET a = 1; COMMIT;", []string{"BEGIN TRAN", "UPDATE t SET a = 1", "COMMIT"}},
		{FamilySQLServer, "BEGIN TRY DELETE FROM t; END TRY BEGIN CATCH SELECT 1; END CATCH; SELECT 2",
			[]string{"BEGIN TRY DELETE FROM t; END TRY BEGIN CATCH SELECT 1; END CATCH", "SELECT 2"}},
		{FamilyOracle, "BEGIN UPDATE t SET a = 1; DELETE FROM u; END;\nSELECT 1 FROM dual;",
			[]string{"BEGIN UPDATE t SET a = 1; DELETE FROM u; END", "SELECT 1 FROM dual"}},
		{FamilyOracle, "DECLARE v NUMBER; BEGIN IF v > 0 THEN v := CASE WHEN v > 1 THEN 2 END; END IF; END; SELECT 1 FROM dual",
			[]string{"DECLARE v NUMBER; BEGIN IF v > 0 THEN v := CASE WHEN v > 1 THEN 2 END; END IF; END", "SELECT 1 FROM dual"}},
		{FamilyOracle, "CREATE OR REPLACE PROCEDURE p IS v NUMBER; BEGIN NULL; END p; SELECT 1 FROM dual",
			[]string{"CREATE OR REPLACE PROCEDURE p IS v NUMBER; BEGIN NULL; END p", "SELECT 1 FROM dual"}},
		{FamilyOracle, "CREATE PACKAGE pk AS PROCEDURE a; FUNCTION b RETURN NUMBER; END pk; SELECT 1 FROM dual",
			[]string{"CREATE PACKAGE pk AS PROCEDURE a; FUNCTION b RETURN NUMBER; END pk", "SELECT 1 FROM dual"}},
		{FamilyDM, "CREATE PACKAGE BODY pk AS PROCEDURE a IS BEGIN NULL; END a; END pk; SELECT 1",
			[]string{"CREATE PACKAGE BODY pk AS PROCEDURE a IS BEGIN NULL; END a; END pk", "SELECT 1"}},
		{FamilySQLServer, "SELECT CASE WHEN 1=1 THEN 'a' END FOR XML PATH(''); DELETE FROM t",
			[]string{"SELECT CASE WHEN 1=1 THEN 'a' END FOR XML PATH('')", "DELETE FROM t"}},
		{FamilySQLServer, "SELECT CASE WHEN 1=1 THEN 'a' END AS v FOR JSON PATH; DELETE FROM t",
			[]string{"SELECT CASE WHEN 1=1 THEN 'a' END AS v FOR JSON PATH", "DELETE FROM t"}},
		{FamilyOracle, "BEGIN CASE x WHEN 1 THEN NULL; END CASE; END; DELETE FROM t",
			[]string{"BEGIN CASE x WHEN 1 THEN NULL; END CASE; END", "DELETE FROM t"}},
		{FamilyMySQL, "CREATE PROCEDURE p() BEGIN CASE x WHEN 1 THEN SELECT 1; END CASE; WHILE y DO SELECT 2; END WHILE; END; DELETE FROM t",
			[]string{"CREATE PROCEDURE p() BEGIN CASE x WHEN 1 THEN SELECT 1; END CASE; WHILE y DO SELECT 2; END WHILE; END", "DELETE FROM t"}},
		{FamilyMySQL, "SELECT 1 /*!; DELETE FROM t */", []string{"SELECT 1 /*!; DELETE FROM t */"}},
		{FamilyOracle, "UPDATE t SET a = 1; DELETE FROM t", []string{"UPDATE t SET a = 1", "DELETE FROM t"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.family)+"/"+tc.sql, func(t *testing.T) {
			got := Split(tc.family, tc.sql)
			if !equalStrings(got, tc.want) {
				t.Errorf("Split = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFamilyFor(t *testing.T) {
	want := map[string]Family{
		"mysql": FamilyMySQL, "postgres": FamilyPostgres, "sqlserver": FamilySQLServer,
		"oracle": FamilyOracle, "yashandb": FamilyOracle, "dm": FamilyDM, "sqlite": FamilySQLite,
		"duckdb": FamilyANSI, "firebird": FamilyANSI, "generic": FamilyANSI,
		"clickhouse": FamilyNone, "trino": FamilyNone, "tdengine": FamilyNone, "mongodb": FamilyNone,
		"redis": FamilyNone, "": FamilyNone, " MySQL ": FamilyMySQL,
	}
	for dialect, fam := range want {
		if got := FamilyFor(dialect); got != fam {
			t.Errorf("FamilyFor(%q) = %q, want %q", dialect, got, fam)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func equalReasons(a, b []Reason) bool {
	x := make([]string, 0, len(a))
	for _, r := range a {
		x = append(x, string(r))
	}
	y := make([]string, 0, len(b))
	for _, r := range b {
		y = append(y, string(r))
	}
	sort.Strings(x)
	sort.Strings(y)
	return equalStrings(x, y)
}

func TestClassifyInsertTarget(t *testing.T) {
	cases := []struct {
		family Family
		sql    string
		table  *TableRef
		rows   int64
		query  string
	}{
		{FamilyMySQL, "INSERT INTO t (a) VALUES (1), (2), (3)", &TableRef{Name: "t"}, 3, ""},
		{FamilyMySQL, "INSERT IGNORE INTO app.t VALUES (1, 'a;b')", &TableRef{Schema: "app", Name: "t"}, 1, ""},
		{FamilyMySQL, "INSERT INTO t VALUES ROW(1), ROW(2)", &TableRef{Name: "t"}, 2, ""},
		{FamilyPostgres, `INSERT INTO "T" (a) VALUES ((1 + 2)), (3) RETURNING id`, &TableRef{Name: "T"}, 2, ""},
		{FamilyPostgres, "INSERT INTO t DEFAULT VALUES", &TableRef{Name: "t"}, 1, ""},
		{FamilyPostgres, "INSERT INTO t (a) SELECT a FROM u WHERE b > 1 RETURNING id", &TableRef{Name: "t"}, 0, "SELECT a FROM u WHERE b > 1"},
		{FamilySQLite, "INSERT INTO t WITH x AS (SELECT 1) SELECT * FROM x", &TableRef{Name: "t"}, 0, "WITH x AS (SELECT 1) SELECT * FROM x"},
		{FamilyMySQL, "INSERT t VALUES (1)", nil, 0, ""},     // no INTO
		{FamilyMySQL, "INSERT INTO t SET a = 1", nil, 0, ""}, // SET form
		{FamilyOracle, "INSERT ALL INTO t VALUES (1) SELECT * FROM dual", nil, 0, ""},
		{FamilySQLServer, "INSERT INTO t OUTPUT inserted.id VALUES (1)", nil, 0, ""},
		{FamilyMySQL, "INSERT INTO t VALUES (1) (2)", nil, 0, ""},
		{FamilyNone, "INSERT INTO t VALUES (1)", nil, 0, ""},
	}
	for _, tc := range cases {
		st := Classify(tc.family, tc.sql)
		if st.Kind != KindInsert || len(st.Reasons) != 0 || st.Rewritable {
			t.Errorf("%q: kind %q reasons %v rewritable %v", tc.sql, st.Kind, st.Reasons, st.Rewritable)
		}
		if !reflect.DeepEqual(st.Table, tc.table) || st.InsertRows != tc.rows || st.InsertQuery != tc.query {
			t.Errorf("%q: table %+v rows %d query %q", tc.sql, st.Table, st.InsertRows, tc.query)
		}
	}
}

func TestLooksLikeSQL(t *testing.T) {
	for _, q := range []string{"VACUUM t", "(SELECT 1)", "SET x = 1", "ADD JAR /tmp/x.jar", "BACKUP DATABASE d TO DISK = 'x'",
		"RESTORE DATABASE d", "DFS -ls /", "REPL DUMP db", "{call p()}", "  { ? = call f(1) }"} {
		if !LooksLikeSQL(FamilyANSI, q) {
			t.Errorf("LooksLikeSQL(%q) = false, want true", q)
		}
	}
	for _, q := range []string{"rate(http_requests_total[5m])", "MATCH (v) RETURN v", "GO FROM \"p1\" OVER follow", "USE nba", "FETCH PROP ON player \"p1\"", "scan 't1'", "q=*:*"} {
		if LooksLikeSQL(FamilyANSI, q) {
			t.Errorf("LooksLikeSQL(%q) = true, want false", q)
		}
	}
}
