package db

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRestoreRoundTripMySQL backs up and restores against a real MySQL /
// MariaDB server. It runs only when MINCODE_TEST_MYSQL_DSN points at a
// disposable server (e.g. "root:pw@tcp(127.0.0.1:3306)/"); it creates and
// drops its own uniquely named databases and touches nothing else.
func TestRestoreRoundTripMySQL(t *testing.T) {
	dsn := os.Getenv("MINCODE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set MINCODE_TEST_MYSQL_DSN to a disposable MySQL/MariaDB server")
	}
	admin, err := sql.Open("mysql", dsn+"?multiStatements=false&parseTime=true&loc=UTC")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	suffix := fmt.Sprint(time.Now().UnixNano())
	src := "mincode_restore_" + suffix
	for _, stmt := range []string{
		"CREATE DATABASE `" + src + "` CHARACTER SET utf8mb4",
		"CREATE TABLE `" + src + "`.`items` (id BIGINT PRIMARY KEY, name VARCHAR(64) NOT NULL, price DECIMAL(30,10), data VARBINARY(16), at DATETIME(6), INDEX idx_name (name))",
		"INSERT INTO `" + src + "`.`items` VALUES (9223372036854775807, 'quote''; DROP', 12345678901234567890.0123456789, X'00FF80', '2024-01-02 03:04:05.123456'), (2, '中文', NULL, NULL, NULL)",
	} {
		if _, err := admin.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS `" + src + "`") })
	open := func() *dbSession {
		db, err := sql.Open("mysql", dsn+"?parseTime=true&loc=UTC")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return &dbSession{kind: kindSQL, dialect: "mysql", db: db}
	}
	dump := func(rows *sql.Rows) string {
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id int64
			var name string
			var price, data, at sql.NullString
			if err := rows.Scan(&id, &name, &price, &data, &at); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%d|%s|%v|%x|%v", id, name, price, data.String, at))
		}
		return strings.Join(out, "\n")
	}
	query := func(db string) string {
		rows, err := admin.Query("SELECT id, name, price, data, CAST(at AS CHAR) FROM `" + db + "`.`items` ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		return dump(rows)
	}
	for _, format := range []string{"archive", "sql"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			source := open()
			job := BackupJob{ID: "mysql-" + format, FileNamePrefix: "m", Format: format, Destination: dir, Gzip: true,
				Targets: []BackupTarget{{Database: src}}}
			var result backupResult
			if format == "archive" {
				result, err = writeBackupArchive(context.Background(), job, source, "mysql", time.Now())
			} else {
				s, e := newSessionBackupSource(context.Background(), source)
				if e != nil {
					t.Fatal(e)
				}
				result, err = writeBackupFile(context.Background(), job, s, backupDumpMeta{DbType: "mysql", StartedAt: time.Now()}, time.Now())
				s.close()
			}
			if err != nil {
				t.Fatal(err)
			}
			want := query(src)
			// Restore into the source name after dropping it: both formats
			// target the database recorded in the backup.
			if _, err := admin.Exec("DROP DATABASE `" + src + "`"); err != nil {
				t.Fatal(err)
			}
			target := open()
			plan, err := planRestore(context.Background(), target, result.File)
			if err != nil || !plan.Supported {
				t.Fatalf("plan: %+v %v", plan, err)
			}
			if _, err := runRestore(context.Background(), target, result.File, RestoreOptions{AllowDestructive: plan.Destructive}, nil); err != nil {
				t.Fatalf("restore: %v", err)
			}
			if got := query(src); got != want {
				t.Fatalf("restored data differs\nwant:\n%s\ngot:\n%s", want, got)
			}
			var idx int
			if err := admin.QueryRow("SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = 'items' AND INDEX_NAME = 'idx_name'", src).Scan(&idx); err != nil || idx == 0 {
				t.Fatalf("index not restored: %d %v", idx, err)
			}
		})
	}
}

// The constrained mapped-target route must not drop or rewrite the source.
func TestScopedRestoreRoundTripMySQL(t *testing.T) {
	dsn := os.Getenv("MINCODE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("disposable MySQL DSN not configured")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ParseTime = true
	cfg.MultiStatements = false
	cfg.Loc = time.UTC
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := "mincode_src_" + fmt.Sprint(time.Now().UnixNano())
	target := source + "_dst"
	for _, name := range []string{source, target} {
		if _, err := db.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4"); err != nil {
			t.Fatal(err)
		}
		name := name
		t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS `" + name + "`") })
	}
	_, err = db.Exec("CREATE TABLE `" + source + "`.items(id BIGINT PRIMARY KEY, price DECIMAL(30,10), at DATETIME(6), data VARBINARY(8))")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO `" + source + "`.items VALUES(9223372036854775807,12345678901234567890.0123456789,'2024-01-02 03:04:05.123456',X'00FF80')"); err != nil {
		t.Fatal(err)
	}
	s := &dbSession{kind: kindSQL, dialect: "mysql", db: db}
	out, err := writeBackupArchive(context.Background(), BackupJob{ID: "mapped", Destination: t.TempDir(), FileNamePrefix: "mapped", Format: "archive", Targets: []BackupTarget{{Database: source}}}, s, "mysql", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := stageRestoreSource(context.Background(), out.File, t.TempDir(), defaultTransferLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	p := restorePrepared{Source: *prepared, Mode: "constrained", Mappings: []restoreMapping{{Database: source, Object: "items", TargetDatabase: target, TargetObject: "items"}}}
	if _, err := runConstrainedFixture(t, s, p); err != nil {
		t.Fatal(err)
	}
	read := func(database string) string {
		var id, price, at, data string
		if err := db.QueryRow("SELECT CAST(id AS CHAR),CAST(price AS CHAR),CAST(at AS CHAR),HEX(data) FROM `"+database+"`.items").Scan(&id, &price, &at, &data); err != nil {
			t.Fatal(err)
		}
		return id + "|" + price + "|" + at + "|" + data
	}
	if got, want := read(target), read(source); got != want {
		t.Fatalf("mapped restore differs: %s / %s", got, want)
	}
}
