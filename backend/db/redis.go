// Redis 连接与 Key CRUD（redis/go-redis v9）。
package db

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"
)

const redisOperationTimeout = 15 * time.Second

var redisAddKeyScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) ~= 0 then
  return 0
end
return redis.call(ARGV[1], KEYS[1], unpack(ARGV, 2)) and 1
`)

func secondsToDuration(sec int64) time.Duration {
	return time.Duration(sec) * time.Second
}

func buildRedisSession(cfg *DbConnectionConfig) (*dbSession, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 6379
	}
	opt := &redis.Options{
		Addr:     fmt.Sprintf("%s:%d", host, port),
		Password: derefOr(cfg.Password, ""),
	}
	if u := derefOr(cfg.Username, ""); u != "" {
		opt.Username = u
	}
	if cfg.SSL {
		opt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("Redis 连接失败: %w", err)
	}
	return &dbSession{kind: kindRedis, rdb: rdb}, nil
}

// redisDBConn 是从连接池借出并 SELECT 到指定 DB 的单连接。go-redis v9 的
// Conn.Close 会把底层连接原样归还连接池，若不切回客户端配置的默认 DB，后续经
// 连接池执行的命令会落到错误的 DB。
type redisDBConn struct {
	*redis.Conn
	defaultDB int
	switched  bool
}

// Close 先 SELECT 回默认 DB 再归还连接；切回失败（网络错误/超时）时 go-redis
// 会把该连接标记为坏连接，Close 时直接丢弃而不是放回连接池。
func (c *redisDBConn) Close() error {
	if c.switched {
		ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
		_ = c.Conn.Select(ctx, c.defaultDB).Err()
		cancel()
	}
	return c.Conn.Close()
}

// redisConn 从连接池借出单连接并 SELECT 到 dbIndex；调用方必须 Close。
func redisConn(ctx context.Context, s *dbSession, dbIndex int64) (*redisDBConn, error) {
	if s.kind != kindRedis || s.rdb == nil {
		return nil, fmt.Errorf("非 Redis 会话")
	}
	defaultDB := s.rdb.Options().DB
	// SELECT 可能已在服务端执行（例如超时），因此只要目标不是默认 DB，Close 时都切回。
	conn := &redisDBConn{Conn: s.rdb.Conn(), defaultDB: defaultDB, switched: dbIndex != int64(defaultDB)}
	if err := conn.Do(ctx, "SELECT", dbIndex).Err(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("Redis SELECT %d: %w", dbIndex, err)
	}
	return conn, nil
}

// ── 返回结构 ──

type redisKeyInfo struct {
	Type    string `json:"type"`
	TTL     int64  `json:"ttl"`
	Value   any    `json:"value"`
	DBIndex int64  `json:"dbIndex"`
	// Binary 表示值（或集合中的任一成员/字段）含非 UTF-8 字节。JSON 传输会把这些
	// 字节替换成 U+FFFD，因此此类值只能只读查看，不能按文本回写。
	Binary bool `json:"binary,omitempty"`
	// ValueHex 仅在 string 类型且 Binary 时返回原始字节的十六进制，此时 Value 为 nil。
	ValueHex string `json:"valueHex,omitempty"`
}

type redisKeyValue struct {
	Success bool `json:"success"`
}

type redisDeleteResult struct {
	Success bool  `json:"success"`
	Deleted int64 `json:"deleted"`
}

type redisTtlInfo struct {
	TTL int64 `json:"ttl"`
}

type redisKeyListItem struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	TTL  int64  `json:"ttl"`
}

type redisKeyListResult struct {
	Keys       []redisKeyListItem `json:"keys"`
	NextCursor string             `json:"nextCursor"`
	Total      int64              `json:"total"`
}

// ── 命令实现 ──

func redisGetKey(ctx context.Context, s *dbSession, dbIndex int64, key string) (*redisKeyInfo, error) {
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	keyType, err := conn.Type(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis TYPE: %w", err)
	}
	ttl := int64(-2)
	if d, err := conn.TTL(ctx, key).Result(); err == nil {
		ttl = redisTTLSeconds(d)
	}

	// 读取失败时沿用历史行为返回空值，不让 GetKey 整体失败。
	value, _ := redisReadValue(ctx, conn.Conn, keyType, key)
	info := &redisKeyInfo{Type: keyType, TTL: ttl, Value: value, DBIndex: dbIndex}
	if redisValueHasBinary(value) {
		info.Binary = true
		if raw, ok := value.(string); ok {
			info.Value = nil
			info.ValueHex = hex.EncodeToString([]byte(raw))
		}
	}
	return info, nil
}

// redisReadValue 按类型读取完整值，返回结构与 redis_get_key 的 value 字段一致。
func redisReadValue(ctx context.Context, conn *redis.Conn, keyType, key string) (any, error) {
	switch keyType {
	case "string":
		return conn.Get(ctx, key).Result()
	case "hash":
		return conn.HGetAll(ctx, key).Result()
	case "list":
		return conn.LRange(ctx, key, 0, -1).Result()
	case "set":
		return conn.SMembers(ctx, key).Result()
	case "zset":
		zs, err := conn.ZRangeWithScores(ctx, key, 0, -1).Result()
		pairs := make([]any, 0, len(zs))
		for _, z := range zs {
			pairs = append(pairs, map[string]any{"member": z.Member, "score": z.Score})
		}
		return pairs, err
	case "stream":
		entries, err := conn.XRange(ctx, key, "-", "+").Result()
		rows := make([]any, 0, len(entries))
		for _, entry := range entries {
			rows = append(rows, map[string]any{"id": entry.ID, "fields": entry.Values})
		}
		return rows, err
	default:
		return nil, nil
	}
}

// redisValueHasBinary 判断 redisReadValue 的结果中是否有非 UTF-8 字符串。
func redisValueHasBinary(value any) bool {
	switch v := value.(type) {
	case string:
		return !utf8.ValidString(v)
	case []string:
		for _, item := range v {
			if !utf8.ValidString(item) {
				return true
			}
		}
	case map[string]string:
		for field, item := range v {
			if !utf8.ValidString(field) || !utf8.ValidString(item) {
				return true
			}
		}
	case map[string]any:
		for field, item := range v {
			if !utf8.ValidString(field) || redisValueHasBinary(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if redisValueHasBinary(item) {
				return true
			}
		}
	}
	return false
}

// redisEnsureTextKey 拒绝覆盖含二进制数据的已有 Key：前端/AI 拿到的是经 JSON
// 替换过的文本，回写会永久破坏原始字节。
func redisEnsureTextKey(ctx context.Context, conn *redis.Conn, key string) error {
	keyType, err := conn.Type(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("Redis TYPE: %w", err)
	}
	if keyType == "none" {
		return nil
	}
	value, err := redisReadValue(ctx, conn, keyType, key)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("Redis 读取 Key %q 以检查二进制内容: %w", key, err)
	}
	if redisValueHasBinary(value) {
		return fmt.Errorf("Redis Key %q 含非 UTF-8 二进制数据，按文本写入会破坏原值，已拒绝写入", key)
	}
	return nil
}

func redisAddKey(ctx context.Context, s *dbSession, dbIndex int64, key, keyType string, value any) (*redisKeyValue, error) {
	if keyType == "stream" {
		return redisAddStream(ctx, s, dbIndex, key, value)
	}
	commandArgs, err := redisAddCommandArgs(keyType, value)
	if err != nil {
		return nil, err
	}
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	created, err := redisAddKeyScript.Run(ctx, conn, []string{key}, commandArgs...).Int64()
	if err != nil {
		return nil, fmt.Errorf("Redis 创建 Key %q: %w", key, err)
	}
	if created != 1 {
		return nil, fmt.Errorf("Redis Key %q 已存在，不能覆盖", key)
	}
	return &redisKeyValue{Success: true}, nil
}

func redisAddStream(ctx context.Context, s *dbSession, dbIndex int64, key string, value any) (*redisKeyValue, error) {
	entries, ok := value.([]any)
	if !ok || len(entries) == 0 {
		return nil, fmt.Errorf("Redis stream 值必须是非空数组，每个元素包含 fields 对象")
	}
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	exists, err := conn.Exists(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis EXISTS: %w", err)
	}
	if exists > 0 {
		return nil, fmt.Errorf("Redis Key %q 已存在，不能覆盖", key)
	}
	for _, item := range entries {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fields := make(map[string]any)
		if f, ok := m["fields"].(map[string]any); ok {
			fields = f
		}
		if len(fields) == 0 {
			continue
		}
		id := "*"
		if idStr, ok := m["id"].(string); ok && idStr != "" && idStr != "*" {
			id = idStr
		}
		if err := conn.XAdd(ctx, &redis.XAddArgs{Stream: key, ID: id, Values: fields}).Err(); err != nil {
			return nil, fmt.Errorf("Redis XADD: %w", err)
		}
	}
	return &redisKeyValue{Success: true}, nil
}

func redisAddCommandArgs(keyType string, value any) ([]any, error) {
	switch keyType {
	case "string":
		return []any{"SET", toRedisString(value)}, nil
	case "hash":
		m, ok := value.(map[string]any)
		if !ok || len(m) == 0 {
			return nil, fmt.Errorf("Redis hash 值必须是非空对象")
		}
		fields := make([]string, 0, len(m))
		for field := range m {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		args := make([]any, 1, 1+len(fields)*2)
		args[0] = "HSET"
		for _, field := range fields {
			args = append(args, field, toRedisString(m[field]))
		}
		return args, nil
	case "list", "set":
		values, ok := value.([]any)
		if !ok || len(values) == 0 {
			return nil, fmt.Errorf("Redis %s 值必须是非空数组", keyType)
		}
		command := "RPUSH"
		if keyType == "set" {
			command = "SADD"
		}
		args := make([]any, 1, 1+len(values))
		args[0] = command
		for _, item := range values {
			args = append(args, toRedisString(item))
		}
		return args, nil
	case "zset":
		values, ok := value.([]any)
		if !ok || len(values) == 0 {
			return nil, fmt.Errorf("Redis zset 值必须是非空数组")
		}
		args := make([]any, 1, 1+len(values)*2)
		args[0] = "ZADD"
		for _, item := range values {
			member, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Redis zset 元素必须包含 member 和 score")
			}
			if _, ok := member["member"]; !ok {
				return nil, fmt.Errorf("Redis zset 元素缺少 member")
			}
			score, err := redisFloat(member["score"])
			if err != nil {
				return nil, fmt.Errorf("Redis zset score 无效: %w", err)
			}
			args = append(args, strconv.FormatFloat(score, 'f', -1, 64), toRedisString(member["member"]))
		}
		return args, nil
	default:
		return nil, fmt.Errorf("不支持的 Redis 类型: %s", keyType)
	}
}

func redisFloat(value any) (float64, error) {
	switch number := value.(type) {
	case float64:
		return number, nil
	case int64:
		return float64(number), nil
	case int:
		return float64(number), nil
	case string:
		parsed, err := strconv.ParseFloat(number, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("必须是数字")
	}
}

func redisSetKey(ctx context.Context, s *dbSession, dbIndex int64, key, keyType string, value any) (*redisKeyValue, error) {
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := redisEnsureTextKey(ctx, conn.Conn, key); err != nil {
		return nil, err
	}

	switch keyType {
	case "string":
		if err := conn.Set(ctx, key, toRedisString(value), 0).Err(); err != nil {
			return nil, fmt.Errorf("Redis SET: %w", err)
		}
	case "hash":
		if m, ok := value.(map[string]any); ok && len(m) > 0 {
			fields := make([]any, 0, len(m)*2)
			for f, v := range m {
				fields = append(fields, f, toRedisString(v))
			}
			if err := conn.HSet(ctx, key, fields...).Err(); err != nil {
				return nil, fmt.Errorf("Redis HSET: %w", err)
			}
		}
	case "list":
		if arr, ok := value.([]any); ok {
			_ = conn.Del(ctx, key).Err()
			vals := make([]any, 0, len(arr))
			for _, v := range arr {
				vals = append(vals, toRedisString(v))
			}
			if len(vals) > 0 {
				if err := conn.RPush(ctx, key, vals...).Err(); err != nil {
					return nil, fmt.Errorf("Redis RPUSH: %w", err)
				}
			}
		}
	case "set":
		if arr, ok := value.([]any); ok {
			_ = conn.Del(ctx, key).Err()
			vals := make([]any, 0, len(arr))
			for _, v := range arr {
				vals = append(vals, toRedisString(v))
			}
			if len(vals) > 0 {
				if err := conn.SAdd(ctx, key, vals...).Err(); err != nil {
					return nil, fmt.Errorf("Redis SADD: %w", err)
				}
			}
		}
	case "zset":
		if arr, ok := value.([]any); ok {
			_ = conn.Del(ctx, key).Err()
			members := make([]redis.Z, 0, len(arr))
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					members = append(members, redis.Z{
						Member: toRedisString(m["member"]),
						Score:  toFloat(m["score"]),
					})
				}
			}
			if len(members) > 0 {
				if err := conn.ZAdd(ctx, key, members...).Err(); err != nil {
					return nil, fmt.Errorf("Redis ZADD: %w", err)
				}
			}
		}
	case "stream":
		if arr, ok := value.([]any); ok {
			_ = conn.Del(ctx, key).Err()
			for _, item := range arr {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				fields := make(map[string]any)
				if f, ok := m["fields"].(map[string]any); ok {
					fields = f
				}
				if len(fields) == 0 {
					continue
				}
				id := "*"
				if idStr, ok := m["id"].(string); ok && idStr != "" && idStr != "*" {
					id = idStr
				}
				if err := conn.XAdd(ctx, &redis.XAddArgs{Stream: key, ID: id, Values: fields}).Err(); err != nil {
					return nil, fmt.Errorf("Redis XADD: %w", err)
				}
			}
		}
	default:
		return nil, fmt.Errorf("不支持的 Redis 类型: %s", keyType)
	}
	return &redisKeyValue{Success: true}, nil
}

func redisDeleteKey(ctx context.Context, s *dbSession, dbIndex int64, key string) (*redisDeleteResult, error) {
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	n, err := conn.Del(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis DEL: %w", err)
	}
	return &redisDeleteResult{Success: true, Deleted: n}, nil
}

func redisSetTTL(ctx context.Context, s *dbSession, dbIndex int64, key string, ttlSeconds int64) (*redisKeyValue, error) {
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if ttlSeconds < 0 {
		if err := conn.Persist(ctx, key).Err(); err != nil {
			return nil, fmt.Errorf("Redis PERSIST: %w", err)
		}
	} else {
		if err := conn.Expire(ctx, key, secondsToDuration(ttlSeconds)).Err(); err != nil {
			return nil, fmt.Errorf("Redis EXPIRE: %w", err)
		}
	}
	return &redisKeyValue{Success: true}, nil
}

// redisTTLSeconds converts a go-redis TTL reply to seconds. go-redis returns the
// sentinels -1 (no expiry) and -2 (missing key) as raw nanosecond durations, so
// converting them with Seconds() truncates both to 0. A 0 written back through
// redis_set_ttl becomes EXPIRE key 0 and deletes the key.
func redisTTLSeconds(d time.Duration) int64 {
	if d == -1 || d == -2 {
		return int64(d)
	}
	return int64(d / time.Second)
}

func redisGetTTL(ctx context.Context, s *dbSession, dbIndex int64, key string) (*redisTtlInfo, error) {
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	d, err := conn.TTL(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("Redis TTL: %w", err)
	}
	return &redisTtlInfo{TTL: redisTTLSeconds(d)}, nil
}

// redisCaseInsensitiveContainsPattern recognises the contains-search form emitted
// by the workspace (for example *order*). Redis SCAN MATCH is case-sensitive;
// the desktop key finder is intentionally not, so user-entered key fragments can
// find existing keys regardless of casing. Explicit glob expressions retain their
// native Redis matching behavior.
func redisCaseInsensitiveContainsPattern(pattern string) (string, bool) {
	if len(pattern) < 3 || !strings.HasPrefix(pattern, "*") || !strings.HasSuffix(pattern, "*") {
		return "", false
	}
	needle := pattern[1 : len(pattern)-1]
	if needle == "" || strings.ContainsAny(needle, "*?[") {
		return "", false
	}
	return strings.ToLower(needle), true
}

func redisListKeys(ctx context.Context, s *dbSession, dbIndex int64, pattern, cursor string, count int64) (*redisKeyListResult, error) {
	conn, err := redisConn(ctx, s, dbIndex)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	total, _ := conn.DBSize(ctx).Result()

	scanPattern := pattern
	if scanPattern == "" {
		scanPattern = "*"
	}
	caseInsensitiveNeedle, caseInsensitiveSearch := redisCaseInsensitiveContainsPattern(scanPattern)
	if caseInsensitiveSearch {
		// SCAN MATCH cannot do case-insensitive matching, so scan the selected
		// logical DB and apply the contains filter locally.
		scanPattern = "*"
	}

	pageSize := int(clampInt64(count, 10, 500))
	isFullScan := scanPattern == "*" && !caseInsensitiveSearch
	var scanCount int64
	if isFullScan {
		scanCount = clampInt64(count, 100, 500)
	} else {
		scanCount = clampInt64(count*50, 500, 5000)
	}

	cur, _ := strconv.ParseUint(cursor, 10, 64)
	nextCursor := "0"
	rawKeys := make([]string, 0, pageSize)
	maxRounds := 512
	if isFullScan {
		maxRounds = 8
	}
	for i := 0; i < maxRounds; i++ {
		keys, next, err := conn.Scan(ctx, cur, scanPattern, scanCount).Result()
		if err != nil {
			return nil, fmt.Errorf("Redis SCAN: %w", err)
		}
		if caseInsensitiveSearch {
			for _, key := range keys {
				if strings.Contains(strings.ToLower(key), caseInsensitiveNeedle) {
					rawKeys = append(rawKeys, key)
				}
			}
		} else {
			rawKeys = append(rawKeys, keys...)
		}
		nextCursor = strconv.FormatUint(next, 10)
		cur = next
		if len(rawKeys) >= pageSize || next == 0 {
			break
		}
	}
	if len(rawKeys) > pageSize {
		rawKeys = rawKeys[:pageSize]
	}

	types := make([]*redis.StatusCmd, len(rawKeys))
	ttls := make([]*redis.DurationCmd, len(rawKeys))
	pipe := conn.Pipeline()
	for i, key := range rawKeys {
		types[i] = pipe.Type(ctx, key)
		ttls[i] = pipe.TTL(ctx, key)
	}
	if len(rawKeys) > 0 {
		// Keep the key names visible if TYPE/TTL is denied for individual keys.
		// As before, failed metadata commands fall back to empty type / -2 TTL.
		_, _ = pipe.Exec(ctx)
	}

	items := make([]redisKeyListItem, 0, len(rawKeys))
	for i, key := range rawKeys {
		keyType, _ := types[i].Result()
		ttl := int64(-2)
		if duration, err := ttls[i].Result(); err == nil {
			ttl = redisTTLSeconds(duration)
		}
		items = append(items, redisKeyListItem{Key: key, Type: keyType, TTL: ttl})
	}
	return &redisKeyListResult{Keys: items, NextCursor: nextCursor, Total: total}, nil
}

// ── 辅助 ──

func toRedisString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	default:
		return 0
	}
}

func clampInt64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ── db_get_schema（Redis 对象树） ──

const (
	redisSchemaKeySampleLimit = 120
	redisSchemaScanTimeout    = 4 * time.Second
)

// redisNavigationSchema only lists populated logical databases; the Redis workspace
// loads keys on demand instead of scanning every database during connection.
func redisNavigationSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	if s.kind != kindRedis || s.rdb == nil {
		return nil, fmt.Errorf("非 Redis 会话")
	}
	indexes := redisSchemaDBIndexes(ctx, s)
	databases := make([]SchemaDatabase, 0, len(indexes))
	for _, index := range indexes {
		databases = append(databases, SchemaDatabase{Name: fmt.Sprintf("db%d", index), Tables: []SchemaTable{}})
	}
	return &DbSchemaInfo{Databases: databases}, nil
}

// redisSchema 构建 Redis 左侧对象树：按逻辑库分组，每库内按 key 类型
// （string/hash/list/set/zset/stream）归入文件夹，最多采样 120 个 key。
func redisSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	if s.kind != kindRedis || s.rdb == nil {
		return nil, fmt.Errorf("非 Redis 会话")
	}

	dbIndexes := redisSchemaDBIndexes(ctx, s)
	databases := make([]SchemaDatabase, 0, len(dbIndexes))

	for _, dbIdx := range dbIndexes {
		keys := redisSchemaScanKeys(ctx, s, dbIdx)
		if len(keys) == 0 {
			// 跳过空库，减少噪音（与 DataGrip 行为一致）。
			continue
		}
		keyTypes := redisSchemaKeyTypes(ctx, s, dbIdx, keys)

		// 按类型分组，固定顺序输出文件夹。
		groups := map[string][]SchemaTable{}
		for i, k := range keys {
			t := "string"
			if i < len(keyTypes) && keyTypes[i] != "" {
				t = keyTypes[i]
			}
			groups[t] = append(groups[t], SchemaTable{Name: k, Kind: t})
		}

		typeOrder := []string{"string", "hash", "list", "set", "zset", "stream"}
		tables := make([]SchemaTable, 0, len(keys)+len(typeOrder))
		for _, t := range typeOrder {
			group := groups[t]
			if len(group) == 0 {
				continue
			}
			// 文件夹行 kind="folder"
			tables = append(tables, SchemaTable{Name: t, Kind: "folder"})
			sort.Slice(group, func(a, b int) bool { return group[a].Name < group[b].Name })
			tables = append(tables, group...)
		}

		databases = append(databases, SchemaDatabase{
			Name:   fmt.Sprintf("db%d", dbIdx),
			Tables: tables,
		})
	}

	return &DbSchemaInfo{Databases: databases}, nil
}

// redisSchemaDBIndexes 解析 INFO keyspace 得到有 key 的逻辑库；云 Redis 可能
// 禁用 INFO，此时回退到 DBSIZE 只探测 db0，避免盲扫 16 个库拖慢界面。
func redisSchemaDBIndexes(ctx context.Context, s *dbSession) []int64 {
	var indexes []int64
	if info, err := s.rdb.Info(ctx, "keyspace").Result(); err == nil {
		indexes = parseRedisKeyspaceDBIndexes(info)
	}
	if len(indexes) == 0 {
		if size, err := s.rdb.DBSize(ctx).Result(); err == nil && size > 0 {
			indexes = append(indexes, 0)
		}
	}
	sort.Slice(indexes, func(a, b int) bool { return indexes[a] < indexes[b] })
	// 去重
	out := indexes[:0]
	var prev int64 = -1
	for _, v := range indexes {
		if v != prev {
			out = append(out, v)
			prev = v
		}
	}
	return out
}

// parseRedisKeyspaceDBIndexes 解析 INFO keyspace，形如 `db0:keys=3,expires=0,...`，
// 只保留 keys>0 的库。
func parseRedisKeyspaceDBIndexes(info string) []int64 {
	var indexes []int64
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "db")
		if !ok {
			continue
		}
		idxRaw, stats, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		idx, err := strconv.ParseInt(idxRaw, 10, 64)
		if err != nil {
			continue
		}
		keys := int64(0)
		for _, part := range strings.Split(stats, ",") {
			if v, ok := strings.CutPrefix(part, "keys="); ok {
				keys, _ = strconv.ParseInt(v, 10, 64)
				break
			}
		}
		if keys > 0 {
			indexes = append(indexes, idx)
		}
	}
	return indexes
}

// redisSchemaScanKeys 在指定库上 SCAN 采样 key 名（限量 + 超时），失败/超时返回空。
func redisSchemaScanKeys(ctx context.Context, s *dbSession, dbIdx int64) []string {
	scanCtx, cancel := context.WithTimeout(ctx, redisSchemaScanTimeout)
	defer cancel()

	conn, err := redisConn(scanCtx, s, dbIdx)
	if err != nil {
		return nil
	}
	defer conn.Close()

	out := make([]string, 0, redisSchemaKeySampleLimit)
	var cur uint64
	for i := 0; i < 512; i++ {
		batch, next, err := conn.Scan(scanCtx, cur, "*", 256).Result()
		if err != nil {
			break
		}
		for _, k := range batch {
			if len(out) >= redisSchemaKeySampleLimit {
				return out
			}
			out = append(out, k)
		}
		cur = next
		if next == 0 {
			break
		}
	}
	return out
}

// redisSchemaKeyTypes 用 pipeline 批量查询 key 类型，与 keys 顺序一一对应。
func redisSchemaKeyTypes(ctx context.Context, s *dbSession, dbIdx int64, keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	typeCtx, cancel := context.WithTimeout(ctx, redisSchemaScanTimeout)
	defer cancel()

	conn, err := redisConn(typeCtx, s, dbIdx)
	if err != nil {
		return fallbackStringTypes(len(keys))
	}
	defer conn.Close()

	pipe := conn.Pipeline()
	cmds := make([]*redis.StatusCmd, len(keys))
	for i, k := range keys {
		cmds[i] = pipe.Type(typeCtx, k)
	}
	if _, err := pipe.Exec(typeCtx); err != nil {
		return fallbackStringTypes(len(keys))
	}
	types := make([]string, len(keys))
	for i, c := range cmds {
		if t, err := c.Result(); err == nil && t != "" {
			types[i] = t
		} else {
			types[i] = "string"
		}
	}
	return types
}

func fallbackStringTypes(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "string"
	}
	return out
}

// redisDBIndexFromName 从 Schema 库名（形如 "db0"、"db3"）解析出逻辑库索引。
// 非法或空值回退为 0，与前端左侧树的 dbN 命名保持一致。
func redisDBIndexFromName(database string) int64 {
	d := strings.TrimSpace(database)
	d = strings.TrimPrefix(d, "db")
	if d == "" {
		return 0
	}
	if n, err := strconv.ParseInt(d, 10, 64); err == nil && n >= 0 {
		return n
	}
	return 0
}

// redisColumns 为单个 key 返回“列信息”（与 Tauri 对齐）：Redis 无表结构，
// 用两列描述该 key —— key（主键，值为 key 名）与 type（该 key 的 Redis 类型）。
// 左侧对象树点击一个 key 时会触发 db_get_table_columns，此处保证不再报
// “该会话类型不支持列查询”。
func redisColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	dbIdx := redisDBIndexFromName(database)
	keyType := "unknown"
	if conn, err := redisConn(ctx, s, dbIdx); err == nil {
		defer conn.Close()
		if t, err := conn.Type(ctx, table).Result(); err == nil && t != "" {
			keyType = t
		}
	}
	keyName := table
	return []ColumnInfo{
		{
			Name:          "key",
			DataType:      "redis",
			Nullable:      false,
			IsPrimaryKey:  true,
			DefaultValue:  &keyName,
			AutoIncrement: false,
		},
		{
			Name:          "type",
			DataType:      keyType,
			Nullable:      false,
			IsPrimaryKey:  false,
			DefaultValue:  nil,
			AutoIncrement: false,
		},
	}, nil
}
