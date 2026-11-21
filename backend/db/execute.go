// SQL 执行与结果扫描（database/sql 通用实现）。
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"mincode/app/backend/db/sqlsafety"
)

const (
	dbQueryTimeoutSecs = 120
	dbDMLTimeoutSecs   = 600
	dbMaxResultRows    = 5000
	dbMaxResultBytes   = 16 * 1024 * 1024
)

func isDMLQuery(sql string) bool {
	u := strings.ToUpper(strings.TrimSpace(sql))
	return strings.HasPrefix(u, "INSERT") ||
		strings.HasPrefix(u, "UPDATE") ||
		strings.HasPrefix(u, "DELETE")
}

// isResultProducing 判断语句是否返回结果集（SELECT / SHOW / WITH / EXPLAIN / DESC 等），
// 只决定执行时用 Query 还是 Exec（CALL 可能返回结果集，仍走 Query）。
// 是否只读不看这里，用 isReadOnlyQuery。
func isResultProducing(sql string) bool {
	u := strings.ToUpper(strings.TrimSpace(sql))
	for _, p := range []string{"SELECT", "SHOW", "WITH", "EXPLAIN", "DESC", "DESCRIBE", "PRAGMA", "VALUES", "TABLE", "CALL"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

// isReadOnlyQuery 判断整段查询文本是否只读：按会话方言拆分后每条语句都在 SQL
// 安全网的只读白名单内（CALL 等不在其内）。空文本没有要执行的写入，视为只读。
func isReadOnlyQuery(s *dbSession, query string) bool {
	family := s.safetyFamily()
	for _, statement := range sqlsafety.Split(family, query) {
		if !sqlsafety.IsReadOnly(family, statement) {
			return false
		}
	}
	return true
}

// runQuery 在 SQL 会话上执行一条语句并返回统一结果。
func runQuery(ctx context.Context, s *dbSession, query string, database *string) (*QueryResult, error) {
	start := time.Now()
	if s.dialect == "trino" {
		query = normalizeTrinoQuery(query)
	}

	// MySQL：若传入 database，则在同一连接上先 USE。
	if s.dialect == "mysql" {
		if d := trimmedOptional(database); d != nil {
			conn, err := mysqlConnUsing(ctx, s, *d)
			if err != nil {
				return nil, err
			}
			defer conn.Close()
			return runOn(ctx, conn, query, start)
		}
	}
	return runOn(ctx, s.db, query, start)
}

// mysqlConnUsing pins one connection and switches it to database.
func mysqlConnUsing(ctx context.Context, s *dbSession, database string) (*sql.Conn, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取连接失败: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "USE `"+strings.ReplaceAll(database, "`", "``")+"`"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("切换数据库失败: %w", err)
	}
	return conn, nil
}

// runOn 在 ex（连接池、单连接或事务）上执行：返回结果集的语句走 Query，其余走 Exec。
func runOn(ctx context.Context, ex sqlExecer, query string, start time.Time) (*QueryResult, error) {
	if isResultProducing(query) {
		rows, err := ex.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanRows(rows, start)
	}
	res, err := ex.ExecContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return execResult(res, start), nil
}

// ── 只读语句的纵深防御 ──
//
// 分类器的只读白名单不可能完备，判为只读、无需确认的语句仍放进数据库自己会拦
// 写入的事务里执行：PostgreSQL / MySQL 只读事务，Oracle 系 SET TRANSACTION
// READ ONLY；SQL Server 放进事务并在读完结果后回滚。所有这类事务结束时都回滚。
// 其他方言不变。

type readGuardMode int

const (
	readGuardNone       readGuardMode = iota
	readGuardReadOnlyTx               // BEGIN ... READ ONLY via TxOptions
	readGuardOracle                   // SET TRANSACTION READ ONLY as the first statement
	readGuardRollback                 // plain transaction, always rolled back
)

func readGuardFor(dialect string) readGuardMode {
	switch dialect {
	case "postgres", "mysql":
		return readGuardReadOnlyTx
	case "oracle", "yashandb", "dm":
		return readGuardOracle
	case "sqlserver":
		return readGuardRollback
	}
	return readGuardNone
}

// beginReadGuard opens the guard transaction on conn and returns where to
// run the read plus the function that ends it (always a rollback). A driver
// or server that refuses the transaction leaves the read on conn as before.
func beginReadGuard(ctx context.Context, conn *sql.Conn, mode readGuardMode) (sqlExecer, func(), error) {
	if mode == readGuardNone {
		return conn, func() {}, nil
	}
	var opts *sql.TxOptions
	if mode == readGuardReadOnlyTx {
		opts = &sql.TxOptions{ReadOnly: true}
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		slog.Debug("read guard transaction unavailable", "error", err)
		return conn, func() {}, nil
	}
	if mode == readGuardOracle {
		if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
			// Still inside a transaction that is rolled back.
			slog.Debug("read guard SET TRANSACTION READ ONLY failed", "error", err)
		}
	}
	return tx, func() { _ = tx.Rollback() }, nil
}

// runReadOnlyQuery runs a query isReadOnlyQuery accepted, inside the read
// guard of the session's dialect.
func runReadOnlyQuery(ctx context.Context, s *dbSession, query string, database *string) (*QueryResult, error) {
	return runReadOnlyQueryMode(ctx, s, query, database, readGuardFor(s.dialect))
}

func runReadOnlyQueryMode(ctx context.Context, s *dbSession, query string, database *string, mode readGuardMode) (*QueryResult, error) {
	mysqlDatabase := s.dialect == "mysql" && trimmedOptional(database) != nil
	if mode == readGuardNone && !mysqlDatabase {
		return runQuery(ctx, s, query, database)
	}
	start := time.Now()
	var conn *sql.Conn
	var err error
	if mysqlDatabase {
		conn, err = mysqlConnUsing(ctx, s, *trimmedOptional(database))
	} else if conn, err = s.db.Conn(ctx); err != nil {
		err = fmt.Errorf("获取连接失败: %w", err)
	}
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ex, finish, err := beginReadGuard(ctx, conn, mode)
	if err != nil {
		return nil, err
	}
	res, err := runOn(ctx, ex, query, start)
	finish()
	if mode == readGuardOracle && readOnlySnapshotTooOld(err) {
		// See readOnlySnapshotTooOld: read again in a rolled-back transaction.
		if ex, finish, err = beginReadGuard(ctx, conn, readGuardRollback); err != nil {
			return nil, err
		}
		defer finish()
		return runOn(ctx, ex, query, start)
	}
	return res, err
}

// readOnlySnapshotTooOld reports ORA-01466: an Oracle read-only transaction
// refuses a table whose definition changed in the last seconds (DDL time
// granularity), e.g. a SELECT right after CREATE TABLE or TRUNCATE. The read
// guard then reads in a plain transaction that is rolled back instead.
func readOnlySnapshotTooOld(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ORA-01466")
}

// scanRows 将结果集扫描为 [][]any。
func scanRows(rows *sql.Rows, start time.Time) (*QueryResult, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := &QueryResult{Columns: cols, Rows: [][]any{}}
	resultBytes := 128
	for _, column := range cols {
		resultBytes += 32 + len(column)*2
	}
	scanner := newSQLRowScanner(len(cols))
	for rows.Next() {
		row, rowBytes, err := scanner.scan(rows)
		if err != nil {
			return nil, err
		}
		if resultBytes+rowBytes > dbMaxResultBytes {
			notice := fmt.Sprintf("查询结果已截断：最多返回 %d 行或 %d MiB，请添加 LIMIT 或缩小查询范围", dbMaxResultRows, dbMaxResultBytes/(1024*1024))
			out.Notice = &notice
			break
		}
		resultBytes += rowBytes
		out.Rows = append(out.Rows, row)
		if len(out.Rows) >= dbMaxResultRows {
			notice := fmt.Sprintf("查询结果已截断：最多返回 %d 行或 %d MiB，请添加 LIMIT 或缩小查询范围", dbMaxResultRows, dbMaxResultBytes/(1024*1024))
			out.Notice = &notice
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.RowCount = len(out.Rows)
	out.ExecutionMs = time.Since(start).Milliseconds()
	return out, nil
}

// sqlRowScanner reuses the database/sql destination slices for every row in one
// result set. Returned rows remain independently allocated for safe retention.
type sqlRowScanner struct {
	holders  []any
	pointers []any
}

func newSQLRowScanner(columnCount int) *sqlRowScanner {
	holders := make([]any, columnCount)
	pointers := make([]any, columnCount)
	for index := range holders {
		pointers[index] = &holders[index]
	}
	return &sqlRowScanner{holders: holders, pointers: pointers}
}

func (scanner *sqlRowScanner) scan(rows *sql.Rows) ([]any, int, error) {
	if err := rows.Scan(scanner.pointers...); err != nil {
		return nil, 0, err
	}
	row := make([]any, len(scanner.holders))
	rowBytes := 32
	for index, value := range scanner.holders {
		row[index] = normalizeValue(value)
		rowBytes += estimateDBValueBytes(row[index]) + 16
	}
	return row, rowBytes, nil
}

func estimateDBValueBytes(v any) int {
	const valueOverhead = 24
	switch x := v.(type) {
	case nil:
		return 8
	case string:
		return valueOverhead + len(x)*2
	case []byte:
		// JSON 编码会将二进制转为 base64；同时计入 slice/string 容器开销。
		return valueOverhead + (len(x)+2)/3*4
	case bool, float32, float64, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return 16
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return 128
		}
		// JSON 长度近似 IPC 载荷，乘二并加容器开销以覆盖 Go/JS 对象驻留成本。
		return valueOverhead + len(encoded)*2
	}
}

// execResult 处理无结果集语句（INSERT/UPDATE/DELETE/DDL）。
func execResult(res sql.Result, start time.Time) *QueryResult {
	out := &QueryResult{Columns: []string{}, Rows: [][]any{}}
	if n, err := res.RowsAffected(); err == nil {
		out.AffectedRows = &n
	}
	out.ExecutionMs = time.Since(start).Milliseconds()
	return out
}

// normalizeValue 将 database/sql 扫描出的原始值转为可 JSON 序列化的形式。
// []byte 优先按 UTF-8 字符串处理（多数驱动的文本列为 []byte）。
func normalizeValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	default:
		return x
	}
}

// timeoutForQuery 依据语句类型选择超时时长。
func timeoutForQuery(query string) time.Duration {
	if isDMLQuery(query) {
		return time.Duration(dbDMLTimeoutSecs) * time.Second
	}
	return time.Duration(dbQueryTimeoutSecs) * time.Second
}
