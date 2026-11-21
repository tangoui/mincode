package db

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// 手机端只读查看 Redis 时，单个 key 一次最多返回的元素数与字符串字节数。
// 值经云端中继转发到手机，必须有上限；完整内容仍可在桌面端查看。
const (
	cloudRedisMaxItems       = 200
	cloudRedisMaxStringBytes = 64 * 1024
	cloudRedisMaxKeyBytes    = 4 * 1024
)

// CloudRedisKeyInfo 是手机端查看的单个 key：Length 为完整长度（字符串字节数或元素个数），
// Truncated 表示 Value 只包含前一部分。
type CloudRedisKeyInfo struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	TTL       int64  `json:"ttl"`
	Length    int64  `json:"length"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary,omitempty"`
	Value     any    `json:"value"`
	ValueHex  string `json:"valueHex,omitempty"`
}

func requireCloudRedis(sessionID string) (*dbSession, func(), error) {
	session, release, err := requireSession(sessionID)
	if err != nil {
		return nil, nil, err
	}
	if session.kind != kindRedis || session.rdb == nil {
		release()
		return nil, nil, errors.New("该会话不是 Redis")
	}
	return session, release, nil
}

// CloudRedisKeys 按模式分页扫描 key（SCAN），只读。
func CloudRedisKeys(sessionID string, dbIndex int64, pattern, cursor string, count int64) (*redisKeyListResult, error) {
	if dbIndex < 0 || dbIndex > 255 {
		return nil, errors.New("Redis 库序号无效")
	}
	if len(pattern) > cloudRedisMaxKeyBytes {
		return nil, errors.New("搜索模式过长")
	}
	session, release, err := requireCloudRedis(sessionID)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return redisListKeys(ctx, session, dbIndex, strings.TrimSpace(pattern), cursor, clampInt64(count, 10, 200))
}

// CloudRedisKey 读取单个 key 的类型、TTL 与（截断后的）值，只使用读命令。
func CloudRedisKey(sessionID string, dbIndex int64, key string) (*CloudRedisKeyInfo, error) {
	if dbIndex < 0 || dbIndex > 255 {
		return nil, errors.New("Redis 库序号无效")
	}
	if key == "" || len(key) > cloudRedisMaxKeyBytes {
		return nil, errors.New("key 为空或过长")
	}
	session, release, err := requireCloudRedis(sessionID)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := redisConn(ctx, session, dbIndex)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	keyType, err := conn.Type(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if keyType == "none" {
		return nil, errors.New("key 不存在或已过期")
	}
	info := &CloudRedisKeyInfo{Key: key, Type: keyType, TTL: -2}
	if d, err := conn.TTL(ctx, key).Result(); err == nil {
		info.TTL = redisTTLSeconds(d)
	}
	value, length, err := cloudRedisReadBounded(ctx, conn.Conn, keyType, key)
	if err != nil {
		return nil, err
	}
	info.Value = value
	info.Length = length
	info.Truncated = cloudRedisTruncated(keyType, value, length)
	if redisValueHasBinary(value) {
		info.Binary = true
		if raw, ok := value.(string); ok {
			info.Value = nil
			info.ValueHex = hex.EncodeToString([]byte(raw))
		}
	}
	return info, nil
}

// cloudRedisReadBounded 只取前 cloudRedisMaxItems 个元素（字符串取前 64KB），并返回完整长度。
func cloudRedisReadBounded(ctx context.Context, conn *redis.Conn, keyType, key string) (any, int64, error) {
	const last = cloudRedisMaxItems - 1
	switch keyType {
	case "string":
		length, err := conn.StrLen(ctx, key).Result()
		if err != nil {
			return nil, 0, err
		}
		value, err := conn.GetRange(ctx, key, 0, cloudRedisMaxStringBytes-1).Result()
		return value, length, err
	case "hash":
		length, err := conn.HLen(ctx, key).Result()
		if err != nil {
			return nil, 0, err
		}
		fields := map[string]string{}
		cursor := uint64(0)
		for len(fields) < cloudRedisMaxItems {
			pairs, next, err := conn.HScan(ctx, key, cursor, "*", cloudRedisMaxItems).Result()
			if err != nil {
				return nil, 0, err
			}
			for i := 0; i+1 < len(pairs) && len(fields) < cloudRedisMaxItems; i += 2 {
				fields[pairs[i]] = pairs[i+1]
			}
			if next == 0 {
				break
			}
			cursor = next
		}
		return fields, length, nil
	case "list":
		length, err := conn.LLen(ctx, key).Result()
		if err != nil {
			return nil, 0, err
		}
		values, err := conn.LRange(ctx, key, 0, last).Result()
		return values, length, err
	case "set":
		length, err := conn.SCard(ctx, key).Result()
		if err != nil {
			return nil, 0, err
		}
		members := make([]string, 0, cloudRedisMaxItems)
		seen := map[string]bool{}
		cursor := uint64(0)
		for len(members) < cloudRedisMaxItems {
			batch, next, err := conn.SScan(ctx, key, cursor, "*", cloudRedisMaxItems).Result()
			if err != nil {
				return nil, 0, err
			}
			for _, member := range batch {
				if !seen[member] && len(members) < cloudRedisMaxItems {
					seen[member] = true
					members = append(members, member)
				}
			}
			if next == 0 {
				break
			}
			cursor = next
		}
		return members, length, nil
	case "zset":
		length, err := conn.ZCard(ctx, key).Result()
		if err != nil {
			return nil, 0, err
		}
		zs, err := conn.ZRangeWithScores(ctx, key, 0, last).Result()
		pairs := make([]any, 0, len(zs))
		for _, z := range zs {
			pairs = append(pairs, map[string]any{"member": z.Member, "score": z.Score})
		}
		return pairs, length, err
	case "stream":
		length, err := conn.XLen(ctx, key).Result()
		if err != nil {
			return nil, 0, err
		}
		entries, err := conn.XRangeN(ctx, key, "-", "+", cloudRedisMaxItems).Result()
		rows := make([]any, 0, len(entries))
		for _, entry := range entries {
			rows = append(rows, map[string]any{"id": entry.ID, "fields": entry.Values})
		}
		return rows, length, err
	default:
		return nil, 0, nil
	}
}

func cloudRedisTruncated(keyType string, value any, length int64) bool {
	switch keyType {
	case "string":
		raw, _ := value.(string)
		return int64(len(raw)) < length
	case "hash":
		fields, _ := value.(map[string]string)
		return int64(len(fields)) < length
	case "list", "set":
		items, _ := value.([]string)
		return int64(len(items)) < length
	case "zset", "stream":
		items, _ := value.([]any)
		return int64(len(items)) < length
	}
	return false
}
