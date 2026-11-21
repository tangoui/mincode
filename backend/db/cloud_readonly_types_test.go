package db

import (
	"database/sql"
	"strings"
	"testing"
)

func registerCloudTestSession(t *testing.T, id string, session *dbSession) {
	t.Helper()
	registry.put(id, session)
	t.Cleanup(func() { registry.remove(id) })
}

func TestCloudReadOnlyQueryRunsOnSQLiteAndRejectsWrites(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Exec(`CREATE TABLE users (id INTEGER, name TEXT); INSERT INTO users VALUES (1, 'a'), (2, 'b')`); err != nil {
		t.Fatal(err)
	}
	registerCloudTestSession(t, "cloud-db-sqlite", &dbSession{kind: kindSQL, dialect: "sqlite", db: conn})

	// 桌面端模板常带结尾分号，SQLite 没有只读事务也能查询。
	result, err := CloudReadOnlyQuery("cloud-db-sqlite", "SELECT * FROM users ORDER BY id;")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("rows = %v", result.Rows)
	}
	for _, query := range []string{
		"DELETE FROM users",
		"SELECT 1; DELETE FROM users",
		"UPDATE users SET name = 'x'",
	} {
		if _, err := CloudReadOnlyQuery("cloud-db-sqlite", query); err == nil {
			t.Fatalf("%q should be rejected", query)
		}
	}
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM users WHERE name IN ('a', 'b')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("data changed: count=%d err=%v", count, err)
	}
}

// 非 SQL 会话的写语句在交给执行器之前就被拒绝（这些会话没有真实连接，执行即会出错）。
func TestCloudReadOnlyQueryRejectsNonSQLWritesBeforeExecuting(t *testing.T) {
	cases := []struct {
		name    string
		session *dbSession
		query   string
	}{
		{"neo4j delete", &dbSession{kind: kindNeo4j}, "MATCH (n) DETACH DELETE n"},
		{"neo4j create", &dbSession{kind: kindNeo4j}, "CREATE (n:User {name: 'x'})"},
		{"cassandra delete", &dbSession{kind: kindCQL}, "DELETE FROM users WHERE id = 1"},
		{"influx write", &dbSession{kind: kindHTTP, httpKind: httpInflux}, `from(bucket: "b") |> range(start: -1h) |> to(bucket: "c")`},
		{"warehouse insert", &dbSession{kind: kindHTTP, httpKind: httpSnowflake}, "INSERT INTO t VALUES (1)"},
	}
	for _, tc := range cases {
		registerCloudTestSession(t, "cloud-db-"+tc.name, tc.session)
		_, err := CloudReadOnlyQuery("cloud-db-"+tc.name, tc.query)
		if err == nil || !strings.Contains(err.Error(), "只读") {
			t.Errorf("%s: err = %v, want read-only rejection", tc.name, err)
		}
	}
}

func TestCloudReadOnlyQueryPointsRedisToKeyBrowser(t *testing.T) {
	registerCloudTestSession(t, "cloud-db-redis", &dbSession{kind: kindRedis})
	if _, err := CloudReadOnlyQuery("cloud-db-redis", "GET k"); err == nil || !strings.Contains(err.Error(), "key 浏览") {
		t.Fatalf("err = %v", err)
	}
}
