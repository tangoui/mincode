package db

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// fakeRedisDBServer 是只认 SELECT/DBSIZE/PING 的最小 RESP2 服务端；每条连接独立
// 记录当前 DB，DBSIZE 直接返回当前 DB 编号，便于断言连接实际所在的 DB。
type fakeRedisDBServer struct {
	ln       net.Listener
	accepted atomic.Int32
}

func startFakeRedisDBServer(t *testing.T) *fakeRedisDBServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &fakeRedisDBServer{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			srv.accepted.Add(1)
			go srv.serve(c)
		}
	}()
	return srv
}

func (srv *fakeRedisDBServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	db := 0
	for {
		args, err := readRESPCommand(r)
		if err != nil {
			return
		}
		var reply string
		switch strings.ToUpper(args[0]) {
		case "SELECT":
			n, err := strconv.Atoi(args[len(args)-1])
			if err != nil || n < 0 || len(args) != 2 {
				reply = "-ERR DB index is out of range\r\n"
				break
			}
			db = n
			reply = "+OK\r\n"
		case "DBSIZE":
			reply = fmt.Sprintf(":%d\r\n", db)
		case "PING":
			reply = "+PONG\r\n"
		default:
			reply = "-ERR unknown command\r\n"
		}
		if _, err := io.WriteString(c, reply); err != nil {
			return
		}
	}
}

func readRESPCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("unexpected RESP line %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("bad array header %q", line)
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "$")))
		if err != nil || size < 0 {
			return nil, fmt.Errorf("bad bulk header %q", header)
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

func TestRedisConnRestoresDefaultDBBeforeReturningToPool(t *testing.T) {
	for _, defaultDB := range []int{0, 2} {
		t.Run(fmt.Sprintf("defaultDB=%d", defaultDB), func(t *testing.T) {
			srv := startFakeRedisDBServer(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			rdb := redis.NewClient(&redis.Options{
				Addr:            srv.ln.Addr().String(),
				DB:              defaultDB,
				Protocol:        2,
				DisableIdentity: true,
				PoolSize:        1,
				MaxRetries:      -1,
			})
			defer rdb.Close()
			s := &dbSession{kind: kindRedis, rdb: rdb}

			for _, target := range []int64{5, int64(defaultDB)} {
				conn, err := redisConn(ctx, s, target)
				if err != nil {
					t.Fatalf("redisConn(%d): %v", target, err)
				}
				if got, err := conn.DBSize(ctx).Result(); err != nil || got != target {
					t.Fatalf("DB inside redisConn(%d) = %d, %v", target, got, err)
				}
				if err := conn.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
				// PoolSize=1：连接池中的下一条命令复用同一物理连接，必须回到默认 DB。
				if got, err := rdb.DBSize(ctx).Result(); err != nil || got != int64(defaultDB) {
					t.Fatalf("pooled DB after redisConn(%d) = %d, %v; want %d", target, got, err, defaultDB)
				}
			}
			if n := srv.accepted.Load(); n != 1 {
				t.Fatalf("server accepted %d connections, want the pooled connection to be reused", n)
			}
		})
	}
}

func TestRedisConnFailedSelectKeepsPoolOnDefaultDB(t *testing.T) {
	srv := startFakeRedisDBServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rdb := redis.NewClient(&redis.Options{
		Addr:            srv.ln.Addr().String(),
		Protocol:        2,
		DisableIdentity: true,
		PoolSize:        1,
		MaxRetries:      -1,
	})
	defer rdb.Close()
	s := &dbSession{kind: kindRedis, rdb: rdb}

	if _, err := redisConn(ctx, s, -1); err == nil || !strings.Contains(err.Error(), "SELECT -1") {
		t.Fatalf("redisConn(-1) err = %v, want SELECT error", err)
	}
	if got, err := rdb.DBSize(ctx).Result(); err != nil || got != 0 {
		t.Fatalf("pooled DB after failed SELECT = %d, %v; want 0", got, err)
	}
}
