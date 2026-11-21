// 会话管理与连接建立。
//
// 采用渐进式策略：SQLite / DuckDB / PostgreSQL 系 / MySQL 系 / SQLServer / Oracle /
// YashanDB / Firebird / ClickHouse / 达梦 DM / TDengine 使用 database/sql 驱动实现；
// Redis、MongoDB 使用各自专用驱动；InfluxDB / Elasticsearch 走 HTTP 通路（非 SQL）。
package db

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
)

// sessionKind 会话底层类型。
type sessionKind int

const (
	kindSQL    sessionKind = iota // database/sql（SQLite / PG / MySQL / SQLServer / Oracle / YashanDB / Firebird / ClickHouse / DM / TDengine / Trino）
	kindRedis                     // go-redis
	kindMongo                     // mongo-driver（非 SQL）
	kindHTTP                      // HTTP 数据源（InfluxDB / Elasticsearch / OpenSearch / CouchDB，非 SQL）
	kindCQL                       // Cassandra / ScyllaDB（gocql）
	kindNeo4j                     // Neo4j（bolt / Cypher）
	kindVector                    // HTTP 向量数据库（Qdrant / Milvus / Weaviate / Pinecone / Chroma）
	kindPlugin                    // 插件提供的数据库驱动（ExternalConnection 代理，见 external_session.go）
)

// httpDbKind HTTP 数据源子类型。
type httpDbKind int

const (
	httpInflux     httpDbKind = iota // InfluxDB
	httpElastic                      // Elasticsearch / OpenSearch
	httpCouch                        // CouchDB
	httpMeili                        // Meilisearch
	httpDatabricks                   // Databricks SQL Statement Execution API
	httpBigQuery                     // Google BigQuery REST API
	httpDremio                       // Dremio SQL REST API
	httpKylin                        // Apache Kylin REST API
	httpPinot                        // Apache Pinot SQL HTTP API
	httpSnowflake                    // Snowflake SQL API
)

// dbSession 一个已建立的数据库会话。
type dbSession struct {
	kind sessionKind

	// SQL 系（SQLite / DuckDB / PostgreSQL / MySQL / SQLServer / Oracle / ClickHouse）
	db      *sql.DB
	dialect string // sqlite / postgres / mysql / sqlserver / oracle / yashandb / firebird / clickhouse —— 方言相关语句
	// engine 是连接配置的数据库类型（小写，如 materialize），区分共用同一方言的引擎。
	engine string

	// Redis
	rdb *redis.Client

	// MongoDB
	mongo           *mongo.Client
	mongoAuthSource string // authSource（默认 admin），用于列库/集合

	// HTTP 数据源（InfluxDB / Elasticsearch / OpenSearch / CouchDB）
	httpKind    httpDbKind
	httpClient  *http.Client
	httpBaseURL string
	httpToken   string // InfluxDB Token / Elasticsearch ApiKey
	httpUser    string
	httpPass    string
	httpOrg     string // InfluxDB org

	// Full 版专属会话资源。使用 any 避免 Lite 编译时链接 Cassandra / Neo4j 驱动。
	cql         any
	neo4jDriver any
	neo4jDB     string
	closeExtra  func()

	// 向量数据库。pgvector 继续复用 PostgreSQL database/sql 会话，但挂载同一份
	// 元数据，以便集合浏览和向量查询共享一套后端协议。
	vector *vectorSession

	// 通过 REST/HTTP 接入的云数仓与分析数据库。
	warehouse *warehouseHTTPConfig

	// 插件驱动会话（kindPlugin）。插件句柄只保存在 ExternalConnection 内部，
	// 前端与 db_* 命令只使用宿主 sessionId。
	ext *externalSession

	// 连接时配置的默认库（MySQL USE / 结果展示用 / MongoDB 作用域库）
	defaultDatabase string

	// sessionID 是注册表中的会话 ID（put 时写入），SQL 安全网令牌按它绑定会话。
	sessionID string
	// connectionID 是前端保存的连接 ID（db_connect 时写入，内置与插件驱动都保存），
	// SQL 安全网的操作记录按它归属连接。
	connectionID string
}

// sessionRegistry 会话注册表，key 为 sessionId。
type sessionRegistry struct {
	mu               sync.Mutex
	sessions         map[string]*dbSession
	timestamps       map[string]time.Time
	activeOperations map[string]int
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{
		sessions:         map[string]*dbSession{},
		timestamps:       map[string]time.Time{},
		activeOperations: map[string]int{},
	}
}

var registry = newSessionRegistry()

func (r *sessionRegistry) put(id string, s *dbSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s.sessionID = id
	r.sessions[id] = s
	r.timestamps[id] = time.Now()
}

func (r *sessionRegistry) acquire(id string) (*dbSession, func(), bool) {
	r.mu.Lock()
	s, ok := r.sessions[id]
	if !ok {
		r.mu.Unlock()
		return nil, nil, false
	}
	r.activeOperations[id]++
	r.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.activeOperations[id] > 0 {
				r.activeOperations[id]--
			}
			if _, exists := r.sessions[id]; exists {
				r.timestamps[id] = time.Now()
			}
		})
	}
	return s, release, true
}

func (r *sessionRegistry) remove(id string) {
	r.mu.Lock()
	s := r.sessions[id]
	delete(r.sessions, id)
	delete(r.timestamps, id)
	delete(r.activeOperations, id)
	r.mu.Unlock()

	if s != nil {
		s.close()
	}
}

// removeWhere 移除满足 match 的会话并返回 id → 会话；不关闭会话，由调用方负责。
func (r *sessionRegistry) removeWhere(match func(*dbSession) bool) map[string]*dbSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]*dbSession{}
	for id, s := range r.sessions {
		if s == nil || !match(s) {
			continue
		}
		out[id] = s
		delete(r.sessions, id)
		delete(r.timestamps, id)
		delete(r.activeOperations, id)
	}
	return out
}

func (r *sessionRegistry) drain() []*dbSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*dbSession, 0, len(r.sessions))
	for id, s := range r.sessions {
		out = append(out, s)
		delete(r.sessions, id)
		delete(r.timestamps, id)
		delete(r.activeOperations, id)
	}
	return out
}

// removeIdle 移除空闲超过 maxIdleSecs 秒的会话，返回被移除的 id 列表。
func (r *sessionRegistry) removeIdle(maxIdleSecs int64) []string {
	return r.removeIdleDuration(time.Duration(maxIdleSecs) * time.Second)
}

func (r *sessionRegistry) removeIdleDuration(maxIdle time.Duration) []string {
	r.mu.Lock()
	now := time.Now()
	var idle []string
	closing := make([]*dbSession, 0)
	for id, ts := range r.timestamps {
		if r.activeOperations[id] > 0 || now.Sub(ts) < maxIdle {
			continue
		}
		idle = append(idle, id)
		closing = append(closing, r.sessions[id])
		delete(r.sessions, id)
		delete(r.timestamps, id)
		delete(r.activeOperations, id)
	}
	r.mu.Unlock()

	for _, s := range closing {
		s.close()
	}
	return idle
}

func (s *dbSession) close() {
	if s == nil {
		return
	}
	if s.db != nil {
		_ = s.db.Close()
	}
	if s.rdb != nil {
		_ = s.rdb.Close()
	}
	if s.mongo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.mongo.Disconnect(ctx)
	}
	if s.closeExtra != nil {
		s.closeExtra()
	}
	if s.ext != nil {
		s.ext.close()
	}
}

// buildSession 根据配置建立会话。
func buildSession(cfg *DbConnectionConfig) (*dbSession, error) {
	if cfg.Driver != nil {
		// Plugin-bound connections are routed only by connectExternalSession
		// (db_connect, backups, schema access, cloud read-only); never build a
		// built-in session for them (no silent fallback).
		return nil, errExternalDriverUnavailable
	}
	session, err := buildSessionForType(cfg)
	if err != nil {
		return nil, err
	}
	session.engine = strings.ToLower(strings.TrimSpace(cfg.DbType))
	configureSessionSQLPool(session)
	return session, nil
}

func buildSessionForType(cfg *DbConnectionConfig) (*dbSession, error) {
	return buildProfileSession(cfg)
}

// derefOr 返回指针指向的值，nil 时返回 fallback。
func derefOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}
