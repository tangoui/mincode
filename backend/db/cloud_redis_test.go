package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// 需要真实 Redis：MINCODE_TEST_REDIS_ADDR=127.0.0.1:6399 go test ./backend/db -run CloudRedis
func TestCloudRedisReadOnlyBrowseAgainstRealRedis(t *testing.T) {
	addr := os.Getenv("MINCODE_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("MINCODE_TEST_REDIS_ADDR not set")
	}
	const dbIndex = 14
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: dbIndex})
	defer rdb.Close()
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	big := strings.Repeat("x", cloudRedisMaxStringBytes+10)
	rdb.Set(ctx, "mc:big", big, 0)
	rdb.Set(ctx, "mc:small", "hello 世界", 0)
	for i := 0; i < 250; i++ {
		rdb.RPush(ctx, "mc:list", i)
	}
	rdb.HSet(ctx, "mc:hash", "a", "1", "b", "2")
	rdb.Set(ctx, "other", "1", 0)

	session := &dbSession{kind: kindRedis, rdb: redis.NewClient(&redis.Options{Addr: addr})}
	const sessionID = "cloud-db-redis-test"
	registry.put(sessionID, session)
	defer registry.remove(sessionID)

	keys, err := CloudRedisKeys(sessionID, dbIndex, "mc:*", "0", 50)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys.Keys) != 4 {
		t.Fatalf("keys = %+v, want 4 mc:* keys", keys.Keys)
	}

	list, err := CloudRedisKey(sessionID, dbIndex, "mc:list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.Type != "list" || list.Length != 250 || !list.Truncated || len(list.Value.([]string)) != cloudRedisMaxItems {
		t.Fatalf("list = %+v", list)
	}

	str, err := CloudRedisKey(sessionID, dbIndex, "mc:big")
	if err != nil {
		t.Fatalf("big: %v", err)
	}
	if !str.Truncated || str.Length != int64(len(big)) || len(str.Value.(string)) != cloudRedisMaxStringBytes {
		t.Fatalf("big truncated=%v length=%d", str.Truncated, str.Length)
	}

	hash, err := CloudRedisKey(sessionID, dbIndex, "mc:hash")
	if err != nil || hash.Truncated || hash.Length != 2 {
		t.Fatalf("hash = %+v, err %v", hash, err)
	}
	small, err := CloudRedisKey(sessionID, dbIndex, "mc:small")
	if err != nil || small.Value != "hello 世界" || small.Truncated {
		t.Fatalf("small = %+v, err %v", small, err)
	}

	if _, err := CloudRedisKey(sessionID, dbIndex, "mc:missing"); err == nil {
		t.Fatal("missing key should fail")
	}
	// 只读：浏览前后数据不变。
	if n, _ := rdb.DBSize(ctx).Result(); n != 5 {
		t.Fatalf("dbsize = %d, want 5", n)
	}
}

func TestCloudRedisRejectsNonRedisSession(t *testing.T) {
	const sessionID = "cloud-db-sql-test"
	registry.put(sessionID, &dbSession{kind: kindSQL})
	defer registry.remove(sessionID)
	if _, err := CloudRedisKeys(sessionID, 0, "*", "0", 10); err == nil {
		t.Fatal("SQL session must not be browsed as Redis")
	}
	if _, err := CloudRedisKey(sessionID, 0, "k"); err == nil {
		t.Fatal("SQL session must not be read as Redis")
	}
	if _, err := CloudRedisKey(sessionID, 300, "k"); err == nil {
		t.Fatal("invalid db index must fail")
	}
}
