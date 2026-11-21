// SQL 安全网：Redis 与非 SQL 会话（MongoDB、HTTP 数据源、Cassandra、Neo4j、向量库）。
//
// 这些库不能留底撤回：检查只给出估算与风险，原因一律带 notUndoableStore。
//
//   - Redis 改键（redis_set_key / redis_add_key / redis_delete_key / redis_set_ttl）：检查
//     operation {kind: "redis", action: set|add|delete|ttl, dbIndex, keys: [key]}，按键数
//     估算（单键为低风险），执行命令带 checkId 消费令牌；按模式批量删除（pattern）用 SCAN
//     统计匹配键数（5 秒上限，超时标估算），FLUSHDB 用 DBSIZE，FLUSHALL 无法估算，三者都按
//     阈值或整库判定。宿主目前只有单键命令，模式与清库检查供 AI 工具在执行前展示风险。
//   - 查询窗口：按各会话执行器的能力判定只读——MongoDB、Elasticsearch、CouchDB、Meilisearch
//     与向量检索只会读；InfluxDB 的 Flux 用 to() / wideTo() 写入；HTTP 数仓与 Cassandra 按 SQL 分类；
//     Neo4j 按 Cypher 写子句判定。非只读的文本没有估算，数据修改按“无法估算”高风险确认，
//     且必须带 db_safety_check 签发的 checkId 才执行。
package db

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"mincode/app/backend/db/sqlsafety"
)

// ── Redis ──

var redisKeyActions = map[string]string{"set": "SET", "add": "ADD", "delete": "DEL", "ttl": "EXPIRE"}

// redisKeyOperation is the operation a Redis key command performs.
func redisKeyOperation(action string, dbIndex int64, key string) safetyOperation {
	return safetyOperation{Kind: "redis", Action: action, DBIndex: &dbIndex, Keys: []string{key}}
}

// requireRedisCheck consumes the token a Redis key edit must present.
func requireRedisCheck(s *dbSession, checkID, confirmText *string, action string, dbIndex int64, key string) error {
	_, _, err := consumeCheck(s, checkID, confirmText, "", operationHash(redisKeyOperation(action, dbIndex, key)))
	return err
}

// checkRedis assesses a Redis key edit, a pattern delete or a database flush.
func checkRedis(ctx context.Context, s *dbSession, op safetyOperation) (*sqlsafety.Check, safetyPlan, error) {
	if s.kind != kindRedis {
		return nil, safetyPlan{}, fmt.Errorf("非 Redis 会话")
	}
	dbIndex := *op.DBIndex
	st := sqlsafety.Statement{Kind: sqlsafety.KindData}
	est := sqlsafety.Estimate{Source: "none"}
	sc := sqlsafety.StatementCheck{}
	switch {
	case op.Pattern != "":
		if op.Action != "delete" {
			return nil, safetyPlan{}, fmt.Errorf("Redis 按模式只支持删除")
		}
		st.Text = "SCAN 0 MATCH " + op.Pattern + " → DEL"
		if n, exact, err := redisCountMatches(ctx, s, dbIndex, op.Pattern); err == nil {
			sc.Text, sc.Kind = truncateRunes(st.Text, safetyTextLimit), st.Kind
			sc.Estimate = sqlsafety.Estimate{Rows: n, Exact: exact, Source: "count"}
			sc.Risk, sc.Reasons = redisRiskForPattern(op.Pattern, n, exact, safetySettings())
			return redisCheck(s, st, sc)
		}
	case op.Action == "flushdb":
		st.Text, st.Reasons = "FLUSHDB", []sqlsafety.Reason{sqlsafety.ReasonNoWhere}
		if n, err := redisDBSize(ctx, s, dbIndex); err == nil {
			est = sqlsafety.Estimate{Rows: n, Exact: true, Source: "count"}
		}
	case op.Action == "flushall":
		st.Text, st.Reasons = "FLUSHALL", []sqlsafety.Reason{sqlsafety.ReasonNoWhere}
	case redisKeyActions[op.Action] != "" && len(op.Keys) > 0:
		st.Text = redisKeyActions[op.Action] + " " + strings.Join(op.Keys, " ")
		if op.Action == "add" {
			st.Kind = sqlsafety.KindInsert
		}
		est = sqlsafety.Estimate{Rows: int64(len(op.Keys)), Exact: true, Source: "count"}
	default:
		return nil, safetyPlan{}, fmt.Errorf("不支持的 Redis 操作: %s", op.Action)
	}
	assessed(&sc, st, est, false, []sqlsafety.Reason{sqlsafety.ReasonNotUndoableStore})
	return redisCheck(s, st, sc)
}

func redisCheck(s *dbSession, st sqlsafety.Statement, sc sqlsafety.StatementCheck) (*sqlsafety.Check, safetyPlan, error) {
	check := &sqlsafety.Check{SessionID: s.sessionID, Statements: []sqlsafety.StatementCheck{sc}}
	return check, safetyPlan{statements: []safetyResolvedStatement{{parsed: st}}}, nil
}

// redisRiskForPattern rates deleting the count keys pattern matched (exact
// false when the scan timed out): over the threshold, or a pattern matching
// every key, is high risk; never undoable.
func redisRiskForPattern(pattern string, count int64, exact bool, settings sqlsafety.Settings) (sqlsafety.Risk, []sqlsafety.Reason) {
	st := sqlsafety.Statement{Kind: sqlsafety.KindData}
	if strings.Trim(pattern, "*") == "" {
		st.Reasons = []sqlsafety.Reason{sqlsafety.ReasonNoWhere}
	}
	risk, reasons := sqlsafety.Assess(st, sqlsafety.Estimate{Rows: count, Exact: exact, Source: "count"}, settings)
	return risk, append(reasons, sqlsafety.ReasonNotUndoableStore)
}

// redisCountMatches counts the keys of database dbIndex matching pattern
// with SCAN for at most safetyEstimateTimeout; exact is false when the scan
// was cut short (the count is then a lower bound).
func redisCountMatches(parent context.Context, s *dbSession, dbIndex int64, pattern string) (int64, bool, error) {
	ctx, cancel := context.WithTimeout(parent, safetyEstimateTimeout)
	defer cancel()
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return 0, false, err
	}
	defer conn.Close()
	var cursor uint64
	var n int64
	for {
		keys, next, err := conn.Scan(ctx, cursor, pattern, 1000).Result()
		if err != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				return n, false, nil
			}
			return 0, false, err
		}
		n += int64(len(keys))
		if cursor = next; cursor == 0 {
			return n, true, nil
		}
	}
}

func redisDBSize(parent context.Context, s *dbSession, dbIndex int64) (int64, error) {
	ctx, cancel := context.WithTimeout(parent, safetyEstimateTimeout)
	defer cancel()
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	return conn.DBSize(ctx).Result()
}

// ── 非 SQL 查询窗口 ──

// nonSQLQuerySession reports sessions whose query window text is not SQL
// run through database/sql (pgvector sessions run vector searches).
func nonSQLQuerySession(s *dbSession) bool {
	switch s.kind {
	case kindMongo, kindHTTP, kindCQL, kindNeo4j, kindVector:
		return true
	}
	return s.kind == kindSQL && s.vector != nil
}

// fluxWrite matches the Flux write functions to() and wideTo() (also as experimental.to()).
var fluxWrite = regexp.MustCompile(`(?i)\b(wide)?to\s*\(`)

// nonSQLStatements classifies query window text of a non-SQL session (see
// the file comment); SQL-like dialects split into statements.
func nonSQLStatements(s *dbSession, query string) []sqlsafety.Statement {
	text := strings.TrimSpace(query)
	one := func(kind sqlsafety.Kind) []sqlsafety.Statement {
		return []sqlsafety.Statement{{Text: text, Kind: kind}}
	}
	switch {
	case s.vector != nil || s.kind == kindVector || s.kind == kindMongo:
		return one(sqlsafety.KindRead)
	case s.kind == kindHTTP:
		switch s.httpKind {
		case httpInflux:
			if fluxWrite.MatchString(stripComments(text, "//")) {
				return one(sqlsafety.KindData)
			}
			return one(sqlsafety.KindRead)
		case httpElastic, httpCouch, httpMeili:
			return one(sqlsafety.KindRead)
		}
		return classifySQLLike(query)
	case s.kind == kindCQL:
		return classifySQLLike(query)
	case s.kind == kindNeo4j:
		return one(cypherKind(text))
	}
	return one(sqlsafety.KindUnknown)
}

func classifySQLLike(query string) []sqlsafety.Statement {
	var out []sqlsafety.Statement
	for i, segment := range sqlsafety.Split(sqlsafety.FamilyANSI, query) {
		st := sqlsafety.Classify(sqlsafety.FamilyANSI, segment)
		st.Index = i
		out = append(out, st)
	}
	return out
}

func stripComments(text, line string) string {
	var b strings.Builder
	for _, l := range strings.Split(text, "\n") {
		if i := strings.Index(l, line); i >= 0 {
			l = l[:i]
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

var (
	cypherProcedure = regexp.MustCompile(`\bCALL\b`)
	cypherData      = regexp.MustCompile(`\b(MERGE|DELETE|DETACH|SET|REMOVE|DROP|FOREACH|LOAD)\b`)
	cypherCreate    = regexp.MustCompile(`\bCREATE\b`)
)

// cypherKind is the kind of a Cypher query by its clauses, ignoring string
// literals and comments: CALL is a procedure, MERGE/DELETE/SET/REMOVE/DROP/
// FOREACH/LOAD CSV change data, CREATE alone inserts, anything else reads.
func cypherKind(text string) sqlsafety.Kind {
	code := strings.ToUpper(cypherCode(text))
	switch {
	case cypherProcedure.MatchString(code):
		return sqlsafety.KindProcedure
	case cypherData.MatchString(code):
		return sqlsafety.KindData
	case cypherCreate.MatchString(code):
		return sqlsafety.KindInsert
	}
	return sqlsafety.KindRead
}

// cypherCode blanks Cypher string literals, backquoted names and comments.
func cypherCode(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			for i++; i < len(text) && text[i] != c; i++ {
				if text[i] == '\\' && c != '`' {
					i++
				}
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			// openCypher ends line comments at CR or LF.
			for i < len(text) && text[i] != '\n' && text[i] != '\r' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				i = len(text)
			} else {
				i += end + 3
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// checkNonSQLQuery assesses non-SQL query window text: all-read text gets
// risk none and no token; anything else is not undoable, has no estimate,
// and gets a token bound to the session, database and queryHash(query).
func checkNonSQLQuery(s *dbSession, query, database string) *sqlsafety.Check {
	statements := nonSQLStatements(s, query)
	check := &sqlsafety.Check{SessionID: s.sessionID, Database: database, Risk: sqlsafety.RiskNone}
	readOnly := true
	for _, st := range statements {
		readOnly = readOnly && st.Kind == sqlsafety.KindRead
	}
	plan := safetyPlan{}
	for _, st := range statements {
		sc := sqlsafety.StatementCheck{Index: st.Index, Text: truncateRunes(st.Text, safetyTextLimit), Kind: st.Kind, Table: st.Table,
			Estimate: sqlsafety.Estimate{Source: "none"}, Risk: sqlsafety.RiskNone}
		if st.Kind != sqlsafety.KindRead {
			assessed(&sc, st, sqlsafety.Estimate{Source: "none"}, false, []sqlsafety.Reason{sqlsafety.ReasonNotUndoableStore})
		}
		check.Statements = append(check.Statements, sc)
		plan.statements = append(plan.statements, safetyResolvedStatement{index: st.Index, parsed: st})
	}
	if readOnly {
		return check
	}
	issueCheck(check, plan, queryHash(query))
	return check
}

// requireNonSQLCheck lets read-only text through and makes anything else
// present its check, like SQL writes.
func requireNonSQLCheck(s *dbSession, a executeQueryArgs) error {
	for _, st := range nonSQLStatements(s, a.Query) {
		if st.Kind != sqlsafety.KindRead {
			_, _, err := requireCheck(s, a)
			return err
		}
	}
	return nil
}
