package db

import (
	"testing"
	"time"
)

func TestRedisTTLSecondsKeepsSentinels(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want int64
	}{
		// go-redis 以原始纳秒返回 -1/-2 哨兵；截断成 0 会让保存时执行 EXPIRE key 0 删除键。
		{"no expiry", time.Duration(-1), -1},
		{"missing key", time.Duration(-2), -2},
		{"seconds", 90 * time.Second, 90},
		{"sub-second remainder", 1500 * time.Millisecond, 1},
		{"expiring now", 400 * time.Millisecond, 0},
	}
	for _, c := range cases {
		if got := redisTTLSeconds(c.in); got != c.want {
			t.Errorf("%s: redisTTLSeconds(%v) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}
