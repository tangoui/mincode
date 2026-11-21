package db

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func splitAll(t *testing.T, script string) []string {
	t.Helper()
	r := newSQLStatementReader(strings.NewReader(script))
	var out []string
	for {
		stmt, err := r.next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, stmt)
	}
}

func TestSQLStatementReaderSplitsSafely(t *testing.T) {
	script := `-- MinCode database backup
-- Job: nightly
SET NAMES utf8mb4;
CREATE TABLE "a;b" (id int, note text DEFAULT 'x;y');
INSERT INTO t VALUES ('it''s; fine', 'back\'slash;', "dq;");
/* block ; comment */ INSERT INTO t VALUES (1);
# hash comment ;
/*!40101 SET character_set_client = utf8 */;
CREATE FUNCTION f() RETURNS int AS $body$ BEGIN RETURN 1; END; $body$ LANGUAGE plpgsql;
DELIMITER //
CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END //
DELIMITER ;
SELECT 3`
	want := []string{
		"-- MinCode database backup\n-- Job: nightly\nSET NAMES utf8mb4",
		`CREATE TABLE "a;b" (id int, note text DEFAULT 'x;y')`,
		`INSERT INTO t VALUES ('it''s; fine', 'back\'slash;', "dq;")`,
		"/* block ; comment */ INSERT INTO t VALUES (1)",
		"# hash comment ;\n/*!40101 SET character_set_client = utf8 */",
		"CREATE FUNCTION f() RETURNS int AS $body$ BEGIN RETURN 1; END; $body$ LANGUAGE plpgsql",
		"CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END",
		"SELECT 3",
	}
	if got := splitAll(t, script); !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestSQLStatementReaderSkipsCommentsAndRejectsTruncation(t *testing.T) {
	if got := splitAll(t, "-- only\n-- comments\n;\n\n-- Backup completed\n"); len(got) != 0 {
		t.Fatalf("comment-only fragments must be skipped: %q", got)
	}
	r := newSQLStatementReader(strings.NewReader("INSERT INTO t VALUES ('cut off"))
	if _, err := r.next(); !errors.Is(err, errUnterminatedSQL) {
		t.Fatalf("a truncated file must fail, got %v", err)
	}
	r = newSQLStatementReader(strings.NewReader("SELECT '" + strings.Repeat("x", 100) + "'"))
	r.maxBytes = 50
	if _, err := r.next(); !errors.Is(err, errStatementTooLarge) {
		t.Fatalf("an oversized statement must fail, got %v", err)
	}
}
