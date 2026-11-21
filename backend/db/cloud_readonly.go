package db

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
	"time"

	"mincode/app/backend/crypto"
	"mincode/app/backend/db/sqlsafety"
)

// CloudReadOnlyConnection 是 cloudcompanion 可用的最小数据库连接配置。
// Driver 非空时经插件驱动连接：仍须通过桌面端写入的凭据授权（与桌面端连接同一套
// ExternalDriverProvider 校验），且驱动必须声明 readOnly，绝不回落到内置驱动。
type CloudReadOnlyConnection struct {
	Config DbConnectionConfig
}

func ConnectCloudReadOnly(connection CloudReadOnlyConnection) (string, error) {
	cfg := connection.Config
	password, err := crypto.DecryptOptional(cfg.Password)
	if err != nil {
		return "", err
	}
	cfg.Password = password
	token, err := crypto.DecryptOptional(cfg.Token)
	if err != nil {
		return "", err
	}
	cfg.Token = token
	var session *dbSession
	if cfg.Driver != nil {
		session, err = connectExternalSession(context.Background(), &cfg)
	} else {
		session, err = buildSession(&cfg)
	}
	if err != nil {
		return "", err
	}
	switch {
	case session.kind == kindPlugin && !session.ext.has(externalFeatureReadOnly):
		session.close()
		return "", errors.New("unsupported: 插件驱动未声明 readOnly，移动端无法保证只读")
	}
	// 其余类型都可以连接：查询统一经 CloudReadOnlyQuery 判定只读；
	// Redis 只经 CloudRedisKeys / CloudRedisKey 浏览，不开放任意命令。
	sessionID := "cloud-db-" + uuid.NewString()
	registry.put(sessionID, session)
	return sessionID, nil
}

func CloudSchema(sessionID string) (*DbSchemaInfo, error) {
	session, release, err := requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if session.kind == kindRedis {
		// 只列出有数据的逻辑库，key 由手机按需分页扫描。
		return redisNavigationSchema(ctx, session)
	}
	return getSchema(ctx, session)
}

func CloudReadOnlyQuery(sessionID, query string) (*QueryResult, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 64*1024 {
		return nil, errors.New("查询为空或超过长度限制")
	}
	session, release, err := requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var result *QueryResult
	switch {
	case session.kind == kindRedis:
		return nil, errors.New("Redis 请在手机上用 key 浏览查看")
	case session.kind == kindPlugin:
		if query, err = normalizeCloudReadOnlyQuery(query); err != nil {
			return nil, err
		}
		result, err = externalReadOnlyQuery(ctx, session, query, cloudReadOnlyMaxRows)
	case nonSQLQuerySession(session):
		// MongoDB、ES、CouchDB、Meilisearch、向量检索的执行器只会读；InfluxDB、Cassandra、
		// HTTP 数仓与 Neo4j 按桌面端查询窗口同一套规则判定，任何非只读语句都拒绝。
		for _, statement := range nonSQLStatements(session, query) {
			if statement.Kind != sqlsafety.KindRead {
				return nil, errors.New("手机端只允许只读查询")
			}
		}
		result, err = runNonSQLQuery(ctx, session, query, nil)
	case session.kind == kindSQL:
		if query, err = normalizeCloudReadOnlyQuery(query); err != nil {
			return nil, err
		}
		if !isReadOnlyQuery(session, query) {
			return nil, errors.New("手机端只允许只读查询")
		}
		result, err = runCloudReadOnlySQL(ctx, session, query)
	default:
		return nil, errors.New("该数据库类型暂不支持查询")
	}
	if err != nil {
		return nil, err
	}
	if len(result.Rows) > cloudReadOnlyMaxRows {
		result.Rows = result.Rows[:cloudReadOnlyMaxRows]
		result.RowCount = len(result.Rows)
	}
	return result, nil
}

// 手机端单次查询最多返回的行数。
const cloudReadOnlyMaxRows = 500

// runCloudReadOnlySQL 在桌面端同一套只读保护里执行已判定只读的单条语句：
// PostgreSQL / MySQL 只读事务、Oracle 系 SET TRANSACTION READ ONLY、SQL Server
// 回滚事务。驱动不支持事务（SQLite、ClickHouse、Doris 等）时只靠前面的只读语句检查。
func runCloudReadOnlySQL(ctx context.Context, session *dbSession, query string) (*QueryResult, error) {
	return runReadOnlyQueryMode(ctx, session, query, nil, readGuardFor(session.dialect))
}

func CloseCloudReadOnly(sessionID string) {
	registry.remove(sessionID)
}

func normalizeCloudReadOnlyQuery(query string) (string, error) {
	// 结尾的分号（桌面端模板常带）不算多条语句。
	trimmed := strings.TrimRight(strings.TrimSpace(query), "; \t\r\n")
	if trimmed == "" || len(trimmed) > 64*1024 {
		return "", errors.New("SQL 为空或超过长度限制")
	}
	clean, err := stripCloudSQLLiteralsAndComments(trimmed)
	if err != nil {
		return "", err
	}
	lower := strings.ToLower(strings.TrimSpace(clean))
	if strings.Contains(lower, ";") {
		return "", errors.New("移动端仅允许单条只读 SQL")
	}
	first := strings.Fields(lower)
	if len(first) == 0 || (first[0] != "select" && first[0] != "with" && first[0] != "explain") {
		return "", errors.New("移动端仅允许 SELECT、WITH 或 EXPLAIN")
	}
	tokens := cloudSQLTokens(lower)
	for _, token := range tokens {
		switch token {
		case "insert", "update", "delete", "merge", "replace", "alter", "drop",
			"create", "truncate", "grant", "revoke", "call", "execute", "copy", "lock":
			return "", fmt.Errorf("移动端只读 SQL 禁止关键字 %s", token)
		}
	}
	for i := 0; i+1 < len(tokens); i++ {
		if (tokens[i] == "into" && tokens[i+1] == "outfile") ||
			(tokens[i] == "for" && tokens[i+1] == "update") {
			return "", fmt.Errorf("移动端只读 SQL 禁止关键字 %s %s", tokens[i], tokens[i+1])
		}
	}
	return trimmed, nil
}

func cloudSQLTokens(query string) []string {
	return strings.FieldsFunc(query, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_'
	})
}

func stripCloudSQLLiteralsAndComments(query string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(query); {
		switch {
		case query[i] == '\'' || query[i] == '"':
			quote := query[i]
			out.WriteByte(' ')
			i++
			closed := false
			for i < len(query) {
				if query[i] == quote {
					if i+1 < len(query) && query[i+1] == quote {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return "", errors.New("SQL 字符串未闭合")
			}
		case i+1 < len(query) && query[i:i+2] == "--":
			// A bare CR ends the comment too, as it does on the server.
			for i < len(query) && query[i] != '\n' && query[i] != '\r' {
				i++
			}
			out.WriteByte(' ')
		case i+1 < len(query) && query[i:i+2] == "/*":
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				return "", errors.New("SQL 注释未闭合")
			}
			i += end + 4
			out.WriteByte(' ')
		default:
			out.WriteByte(query[i])
			i++
		}
	}
	return out.String(), nil
}
