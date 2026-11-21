package dbimport

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// builtinTypes lists the built-in DbType values an import may target, with
// their default ports (mirrors vue-app/src/domain/database/types.ts).
var builtinTypes = map[string]int{
	"mysql": 3306, "mariadb": 3306, "tidb": 4000, "oceanbase": 2881, "polardb": 3306,
	"tdsql": 3306, "gbase": 5258, "gbase8t": 9088, "gbase8c": 5432, "goldendb": 3306,
	"starrocks": 9030, "doris": 9030, "postgres": 5432, "kingbase": 54321, "gaussdb": 5432,
	"opengauss": 5432, "highgo": 5866, "vastbase": 5432, "opentenbase": 5432, "dolt": 3306, "greatsql": 3306, "sqlite": 0, "duckdb": 0,
	"sqlserver": 1433, "oracle": 1521, "dameng": 5236, "yashandb": 1688, "firebird": 3050,
	"clickhouse": 8123, "tdengine": 6041, "mongodb": 27017, "redis": 6379, "influxdb": 8086,
	"elasticsearch": 9200, "opensearch": 9200, "cassandra": 9042, "neo4j": 7687,
	"couchdb": 5984, "meilisearch": 7700, "trino": 8080, "cockroachdb": 26257,
	"yugabyte": 5433, "greenplum": 5432, "questdb": 8812, "materialize": 6875,
	"risingwave": 4513, "pgvector": 5432, "qdrant": 6333, "milvus": 19530, "weaviate": 8080,
	"pinecone": 443, "chroma": 8000, "presto": 8080, "selectdb": 9030, "databend": 3307,
	"cloudberry": 5432, "redshift": 5439, "databricks": 443, "bigquery": 443, "dremio": 9047,
	"kylin": 7070, "pinot": 8099, "snowflake": 443,
}

func isBuiltinType(kind string) bool {
	_, ok := builtinTypes[kind]
	return ok
}

// fileTypes are built-in types addressed by a local file path, not host/port.
var fileTypes = map[string]bool{"sqlite": true, "duckdb": true}

// driverKinds are databases only reachable through a driver plugin: the key
// is the contributed databaseDrivers id, the value the database's default
// port. Which plugin provides the driver is resolved at runtime from the
// installed contributions; the importer never names a plugin.
var driverKinds = map[string]int{
	"hive": 10000, "impala": 21050, "sparksql": 10000, "kyuubi": 10009, "inceptor": 10000, "argodb": 10000,
	"hbase": 2181, "nebula": 9669,
	"uxdb": 52025, "kwdb": 26257,
	"iotdb": 6667, "influxdb3": 8181, "victoriametrics": 8428,
	"turso": 443, "cloudflare-d1": 443, "rqlite": 4001,
	"solr": 8983, "manticore": 9306, "easysearch": 9200,
	"saphana": 30015, "vertica": 5433, "exasol": 8563,
	"dynamodb": 443, "spanner": 443, "salesforce": 443,
}

// kindAliases maps lower-case vendor names (DBeaver providers/drivers,
// DataGrip driver refs, Navicat ConnType, JDBC sub-protocols) to a kind: a
// built-in DbType or a driverKinds key.
var kindAliases = map[string]string{
	"mysql": "mysql", "mysql5": "mysql", "mysql8": "mysql", "mysqlx": "mysql", "aurora": "mysql",
	"mariadb": "mariadb", "tidb": "tidb", "oceanbase": "oceanbase", "oceanbasemysql": "oceanbase",
	"polardb": "polardb", "polardbx": "polardb", "tdsql": "tdsql", "dolt": "dolt", "greatsql": "greatsql",
	"gbase": "gbase", "gbase8a": "gbase", "gbase8t": "gbase8t", "gbasedbt": "gbase8t", "gbase8c": "gbase8c", "gbase8s": "gbase8t",
	"goldendb": "goldendb", "starrocks": "starrocks", "doris": "doris", "selectdb": "selectdb", "databend": "databend",
	"postgres": "postgres", "postgresql": "postgres", "postgre": "postgres", "pgsql": "postgres", "pgjdbc": "postgres",
	"kingbase": "kingbase", "kingbase8": "kingbase", "kingbasees": "kingbase", "kingbasees8": "kingbase",
	"gaussdb": "gaussdb", "opengauss": "opengauss", "highgo": "highgo", "highgodb": "highgo", "vastbase": "vastbase", "opentenbase": "opentenbase", "tbase": "opentenbase",
	"sqlite": "sqlite", "sqlite3": "sqlite", "duckdb": "duckdb",
	"sqlserver": "sqlserver", "mssql": "sqlserver", "jtds": "sqlserver", "azuresql": "sqlserver", "microsoft sql server": "sqlserver",
	"oracle": "oracle", "dameng": "dameng", "dm": "dameng", "dm8": "dameng", "yashandb": "yashandb", "yasdb": "yashandb",
	"firebird": "firebird", "firebirdsql": "firebird", "jaybird": "firebird",
	"clickhouse": "clickhouse", "ch": "clickhouse", "tdengine": "tdengine", "taos": "tdengine", "taos-rs": "tdengine",
	"mongodb": "mongodb", "mongodb+srv": "mongodb", "mongo": "mongodb", "redis": "redis", "influxdb": "influxdb", "influx": "influxdb",
	"elasticsearch": "elasticsearch", "elastic": "elasticsearch", "es": "elasticsearch", "opensearch": "opensearch",
	"cassandra": "cassandra", "scylla": "cassandra", "scylladb": "cassandra", "neo4j": "neo4j", "couchdb": "couchdb",
	"meilisearch": "meilisearch", "trino": "trino", "presto": "presto", "prestodb": "presto",
	"cockroachdb": "cockroachdb", "cockroach": "cockroachdb", "yugabyte": "yugabyte", "yugabytedb": "yugabyte",
	"greenplum": "greenplum", "questdb": "questdb", "materialize": "materialize", "risingwave": "risingwave",
	"cloudberry": "cloudberry", "redshift": "redshift", "databricks": "databricks", "bigquery": "bigquery",
	"dremio": "dremio", "kylin": "kylin", "pinot": "pinot", "snowflake": "snowflake",
	"hive": "hive", "hive2": "hive", "impala": "impala", "hbase": "hbase", "phoenix": "hbase",
	"sparksql": "sparksql", "spark": "sparksql", "sparkthrift": "sparksql", "kyuubi": "kyuubi",
	"inceptor": "inceptor", "transwarp": "inceptor", "argodb": "argodb",
	"uxdb": "uxdb", "kwdb": "kwdb", "kaiwudb": "kwdb", "iotdb": "iotdb", "influxdb3": "influxdb3",
	"victoriametrics": "victoriametrics", "turso": "turso", "libsql": "turso", "cloudflare-d1": "cloudflare-d1", "d1": "cloudflare-d1",
	"rqlite": "rqlite", "solr": "solr", "manticore": "manticore", "manticoresearch": "manticore", "easysearch": "easysearch",
	"saphana": "saphana", "hana": "saphana", "sap": "saphana", "vertica": "vertica", "exasol": "exasol", "exa": "exasol",
	"dynamodb": "dynamodb", "dynamo": "dynamodb", "amazondynamodb": "dynamodb", "spanner": "spanner", "cloudspanner": "spanner",
	"salesforce": "salesforce", "nebula": "nebula", "nebulagraph": "nebula",
}

var aliasSplit = regexp.MustCompile(`[._\-\s/]+`)

// resolveKind maps a vendor name to a kind, trying the exact name, the first
// token (e.g. "mysql.8", "sqlite_jdbc") and the token without version digits.
func resolveKind(names ...string) string {
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || name == "generic" {
			continue
		}
		if kind, ok := kindAliases[name]; ok {
			return kind
		}
		compact := strings.NewReplacer(" ", "", "_", "", "-", "").Replace(name)
		if kind, ok := kindAliases[compact]; ok {
			return kind
		}
		first := aliasSplit.Split(name, 2)[0]
		if kind, ok := kindAliases[first]; ok {
			return kind
		}
		if trimmed := strings.TrimRight(first, "0123456789"); trimmed != first {
			if kind, ok := kindAliases[trimmed]; ok {
				return kind
			}
		}
	}
	return ""
}

// applyKind fills the mapped type/status of a candidate from its kind.
func (c *Candidate) applyKind(kind string) {
	switch {
	case kind == "":
		c.Status = StatusUnsupported
	case isBuiltinType(kind):
		c.DbType = kind
		c.Status = StatusReady
		if c.Port == 0 && !fileTypes[kind] {
			c.Port = builtinTypes[kind]
		}
	default:
		port, ok := driverKinds[kind]
		if !ok {
			c.Status = StatusUnsupported
			return
		}
		c.PluginDriverID = kind
		c.Status = StatusNeedsPlugin
		if c.Port == 0 {
			c.Port = port
		}
	}
	if fileTypes[c.DbType] {
		switch {
		case c.FilePath == "" && c.Database != "":
			c.FilePath, c.Database = c.Database, ""
		case c.Database == c.FilePath:
			c.Database = ""
		}
	}
}

// jdbcInfo is what a JDBC (or plain) connection URL yields.
type jdbcInfo struct {
	Kind     string
	Host     string
	Port     int
	Database string
	FilePath string
	User     string
	Password string
	SSL      bool
	Params   map[string]string
}

// parseJDBCURL parses the common JDBC URL shapes. It never fails loudly:
// unknown shapes return ok=false and the caller keeps its other fields.
func parseJDBCURL(raw string) (jdbcInfo, bool) {
	info := jdbcInfo{Params: map[string]string{}}
	value := strings.TrimSpace(raw)
	if len(value) > maxFieldBytes {
		return info, false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "jdbc:") {
		value = value[len("jdbc:"):]
		lower = lower[len("jdbc:"):]
	}
	colon := strings.Index(lower, ":")
	if colon <= 0 {
		return info, false
	}
	sub := lower[:colon]
	rest := value[colon+1:]
	switch sub {
	case "jtds", "neo4j", "oracle", "log4jdbc", "p6spy":
		// Composite sub-protocols: jdbc:jtds:sqlserver://, jdbc:neo4j:bolt://, jdbc:oracle:thin:@
		if next := strings.Index(rest, ":"); next > 0 && sub != "oracle" {
			inner := strings.ToLower(rest[:next])
			if sub == "neo4j" {
				inner = "neo4j"
			}
			sub, rest = inner, rest[next+1:]
		}
	}
	info.Kind = resolveKind(sub)
	switch info.Kind {
	case "oracle":
		parseOracleJDBC(rest, &info)
		return info, info.Host != ""
	case "sqlite", "duckdb":
		path := strings.SplitN(rest, "?", 2)[0]
		path = strings.TrimPrefix(path, "file:")
		if strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "///") {
			path = path[1:]
		}
		info.FilePath = path
		return info, path != "" && path != ":memory:" && path != "memory:"
	case "exasol":
		// jdbc:exa:host:port;schema=x
		parts := strings.SplitN(rest, ";", 2)
		host, port := splitHostPort(parts[0])
		info.Host, info.Port = host, port
		if len(parts) == 2 {
			parseSemicolonParams(parts[1], info.Params)
		}
		info.Database = info.Params["schema"]
		return info, host != ""
	case "spanner":
		// jdbc:cloudspanner:/projects/p/instances/i/databases/d
		info.Database = strings.TrimPrefix(strings.SplitN(rest, "?", 2)[0], "/")
		info.Host = "spanner.googleapis.com"
		return info, info.Database != ""
	case "hbase":
		// jdbc:phoenix:zk1,zk2:2181:/hbase
		hosts := strings.SplitN(rest, ":", 2)
		info.Host = strings.Split(hosts[0], ",")[0]
		if len(hosts) == 2 {
			if port, err := strconv.Atoi(strings.SplitN(hosts[1], ":", 2)[0]); err == nil {
				info.Port = port
			}
		}
		return info, info.Host != ""
	}
	if !strings.HasPrefix(rest, "//") {
		return info, false
	}
	parseAuthorityURL(rest, &info)
	if info.Kind == "sqlserver" {
		if db := firstNonEmpty(info.Params["databasename"], info.Params["database"]); db != "" {
			info.Database = db
		}
	}
	return info, info.Host != ""
}

func parseAuthorityURL(rest string, info *jdbcInfo) {
	body := strings.TrimPrefix(rest, "//")
	query := ""
	if index := strings.IndexAny(body, "?;"); index >= 0 {
		query = body[index:]
		body = body[:index]
	}
	authority, path, _ := strings.Cut(body, "/")
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		userInfo := authority[:at]
		authority = authority[at+1:]
		user, password, hasPassword := strings.Cut(userInfo, ":")
		info.User = unescape(user)
		if hasPassword {
			info.Password = unescape(password)
		}
	}
	// Multi-host URLs (replicas, failover): keep the first host.
	authority = strings.Split(authority, ",")[0]
	info.Host, info.Port = splitHostPort(authority)
	info.Database = unescape(strings.Split(path, "/")[0])
	for _, segment := range strings.FieldsFunc(query, func(r rune) bool { return r == '?' || r == '&' || r == ';' }) {
		key, value, _ := strings.Cut(segment, "=")
		info.Params[strings.ToLower(strings.TrimSpace(key))] = unescape(strings.TrimSpace(value))
	}
	info.User = firstNonEmpty(info.User, info.Params["user"], info.Params["username"])
	info.Password = firstNonEmpty(info.Password, info.Params["password"])
	info.SSL = sslFromParams(info.Params)
}

func parseSemicolonParams(value string, params map[string]string) {
	for _, segment := range strings.Split(value, ";") {
		key, v, ok := strings.Cut(segment, "=")
		if ok {
			params[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(v)
		}
	}
}

var oracleDescriptorPart = regexp.MustCompile(`(?i)\((HOST|PORT|SERVICE_NAME|SID)\s*=\s*([^)]+)\)`)

func parseOracleJDBC(rest string, info *jdbcInfo) {
	// thin:@host:port:SID | thin:@//host:port/service | thin:user/pass@host... | thin:@(DESCRIPTION=...)
	_, after, found := strings.Cut(rest, "@")
	if !found {
		return
	}
	if strings.HasPrefix(after, "(") {
		for _, match := range oracleDescriptorPart.FindAllStringSubmatch(after, -1) {
			value := strings.TrimSpace(match[2])
			switch strings.ToUpper(match[1]) {
			case "HOST":
				if info.Host == "" {
					info.Host = value
				}
			case "PORT":
				if info.Port == 0 {
					info.Port, _ = strconv.Atoi(value)
				}
			case "SERVICE_NAME", "SID":
				info.Database = value
			}
		}
		return
	}
	after = strings.SplitN(strings.TrimPrefix(after, "//"), "?", 2)[0]
	if slash := strings.Index(after, "/"); slash >= 0 {
		info.Database = after[slash+1:]
		info.Host, info.Port = splitHostPort(after[:slash])
		return
	}
	parts := strings.Split(after, ":")
	info.Host = parts[0]
	if len(parts) > 1 {
		info.Port, _ = strconv.Atoi(parts[1])
	}
	if len(parts) > 2 {
		info.Database = parts[2]
	}
}

func splitHostPort(value string) (string, int) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", 0
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		parsed, _ := strconv.Atoi(port)
		return host, validPort(parsed)
	}
	return strings.Trim(value, "[]"), 0
}

func sslFromParams(params map[string]string) bool {
	for _, key := range []string{"ssl", "usessl", "tls", "encrypt", "requiressl", "usesslconnection"} {
		if isTruthy(params[key]) {
			return true
		}
	}
	switch strings.ToLower(params["sslmode"]) {
	case "require", "verify-ca", "verify-full", "required", "verify_ca", "verify_identity":
		return true
	}
	return false
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "required", "require":
		return true
	}
	return false
}

func unescape(value string) string {
	if decoded, err := url.PathUnescape(value); err == nil {
		return decoded
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validPort(port int) int {
	if port < 1 || port > 65535 {
		return 0
	}
	return port
}

func parsePort(value string) int {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return validPort(port)
}
