package db

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestRedisValueHasBinary(t *testing.T) {
	bin := string([]byte{0xff, 0xfe, 0x00, 0x01})
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"nil", nil, false},
		{"text string", "hello 世界", false},
		{"binary string", bin, true},
		{"text list", []string{"a", "b"}, false},
		{"binary list member", []string{"a", bin}, true},
		{"binary hash value", map[string]string{"f": bin}, true},
		{"binary hash field", map[string]string{bin: "v"}, true},
		{"text hash", map[string]string{"f": "v"}, false},
		{"binary zset member", []any{map[string]any{"member": bin, "score": 1.0}}, true},
		{"text zset", []any{map[string]any{"member": "m", "score": 1.0}}, false},
		{"binary stream field", []any{map[string]any{"id": "1-0", "fields": map[string]any{"f": bin}}}, true},
	}
	for _, tc := range cases {
		if got := redisValueHasBinary(tc.value); got != tc.want {
			t.Errorf("%s: redisValueHasBinary = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// 需要真实 Redis：MINCODE_TEST_REDIS_ADDR=127.0.0.1:6399 go test ./backend/db -run Redis
func TestRedisBinaryKeyIsReadOnlyAgainstRealRedis(t *testing.T) {
	addr := os.Getenv("MINCODE_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("MINCODE_TEST_REDIS_ADDR not set")
	}
	const dbIndex = 15
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: dbIndex})
	defer rdb.Close()
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	s := &dbSession{kind: kindRedis, rdb: redis.NewClient(&redis.Options{Addr: addr})}
	defer s.rdb.Close()

	raw := []byte{0xff, 0xfe, 'a', 0x00}
	if err := rdb.Set(ctx, "bin", raw, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.RPush(ctx, "binlist", "ok", raw).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "text", "hello", 0).Err(); err != nil {
		t.Fatal(err)
	}

	info, err := redisGetKey(ctx, s, dbIndex, "bin")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Binary || info.Value != nil || info.ValueHex != hex.EncodeToString(raw) {
		t.Fatalf("binary string info = %+v", info)
	}
	listInfo, err := redisGetKey(ctx, s, dbIndex, "binlist")
	if err != nil {
		t.Fatal(err)
	}
	if !listInfo.Binary {
		t.Fatalf("binary list not flagged: %+v", listInfo)
	}

	// 模拟前端拿到 U+FFFD 替换后的文本再保存。
	lossy := strings.ToValidUTF8(string(raw), "\uFFFD")
	if _, err := redisSetKey(ctx, s, dbIndex, "bin", "string", lossy); err == nil {
		t.Fatal("expected write over binary string to be rejected")
	}
	if _, err := redisSetKey(ctx, s, dbIndex, "binlist", "list", []any{"ok", lossy}); err == nil {
		t.Fatal("expected write over binary list to be rejected")
	}
	if got, _ := rdb.Get(ctx, "bin").Bytes(); string(got) != string(raw) {
		t.Fatalf("binary string changed: %x", got)
	}
	if n, _ := rdb.LLen(ctx, "binlist").Result(); n != 2 {
		t.Fatalf("binary list changed, len=%d", n)
	}

	if _, err := redisSetKey(ctx, s, dbIndex, "text", "string", "world"); err != nil {
		t.Fatalf("text write rejected: %v", err)
	}
	if _, err := redisSetKey(ctx, s, dbIndex, "fresh", "string", "new"); err != nil {
		t.Fatalf("new key write rejected: %v", err)
	}
	if got, _ := rdb.Get(ctx, "text").Result(); got != "world" {
		t.Fatalf("text value = %q", got)
	}
	textInfo, err := redisGetKey(ctx, s, dbIndex, "text")
	if err != nil {
		t.Fatal(err)
	}
	if textInfo.Binary || textInfo.Value != "world" {
		t.Fatalf("text info = %+v", textInfo)
	}
}
