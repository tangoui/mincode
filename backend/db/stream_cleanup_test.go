package db

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestIdleSessionCleanerRemovesIdleAndReportsIDs(t *testing.T) {
	r := newSessionRegistry()
	r.put("idle", &dbSession{})
	r.mu.Lock()
	r.timestamps["idle"] = time.Now().Add(-time.Hour)
	r.mu.Unlock()

	var reported []string
	removed := cleanupIdleSessions(r, 30*time.Minute, func(ids []string) {
		reported = append(reported, ids...)
	})
	if len(removed) != 1 || removed[0] != "idle" {
		t.Fatalf("removed = %#v", removed)
	}
	if len(reported) != 1 || reported[0] != "idle" {
		t.Fatalf("reported = %#v", reported)
	}
}

func TestIdleSessionCleanerProtectsActiveOperations(t *testing.T) {
	r := newSessionRegistry()
	r.put("active", &dbSession{})
	r.mu.Lock()
	r.timestamps["active"] = time.Now().Add(-time.Hour)
	r.mu.Unlock()
	_, release, ok := r.acquire("active")
	if !ok {
		t.Fatal("expected active session")
	}
	defer release()

	if removed := cleanupIdleSessions(r, 30*time.Minute, nil); len(removed) != 0 {
		t.Fatalf("active session removed: %#v", removed)
	}
}

func TestStreamSQLQueryEmitsOrderedBoundedChunks(t *testing.T) {
	db := openStreamingTestDB(t, 37)
	defer db.Close()

	var chunks []QueryResultChunk
	summary, err := streamSQLQuery(context.Background(), &dbSession{kind: kindSQL, db: db, dialect: "sqlite"},
		"select id, payload from items order by id", nil, "query-1", 8, 2048,
		func(chunk QueryResultChunk) error {
			chunks = append(chunks, chunk)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	rows := 0
	for index, chunk := range chunks {
		if chunk.QueryID != "query-1" || chunk.Sequence != index || len(chunk.Rows) > 8 {
			t.Fatalf("invalid chunk %d: %#v", index, chunk)
		}
		if chunk.EstimatedBytes > 2048 {
			t.Fatalf("chunk exceeds byte boundary: %d", chunk.EstimatedBytes)
		}
		rows += len(chunk.Rows)
	}
	if rows != 37 || summary.RowCount != 37 || summary.ChunkCount != len(chunks) {
		t.Fatalf("rows=%d summary=%#v chunks=%d", rows, summary, len(chunks))
	}
	firstRow := chunks[0].Rows[0]
	lastChunk := chunks[len(chunks)-1]
	lastRow := lastChunk.Rows[len(lastChunk.Rows)-1]
	if firstRow[0] != int64(0) || lastRow[0] != int64(36) || firstRow[1] != "payload-payload-payload" {
		t.Fatalf("reused scan buffers corrupted retained rows: first=%#v last=%#v", firstRow, lastRow)
	}
}

func TestStreamSQLQueryPreservesColumnsForEmptyResults(t *testing.T) {
	db := openStreamingTestDB(t, 0)
	defer db.Close()
	var chunks []QueryResultChunk
	summary, err := streamSQLQuery(context.Background(), &dbSession{kind: kindSQL, db: db, dialect: "sqlite"},
		"select id, payload from items", nil, "query-empty", 8, 2048,
		func(chunk QueryResultChunk) error {
			chunks = append(chunks, chunk)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowCount != 0 || summary.ChunkCount != 1 || len(chunks) != 1 || len(chunks[0].Columns) != 2 || len(chunks[0].Rows) != 0 {
		t.Fatalf("summary=%#v chunks=%#v", summary, chunks)
	}
}

func TestStreamSQLQueryStopsOnCancellationAndReleasesRows(t *testing.T) {
	db := openStreamingTestDB(t, 100)
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := streamSQLQuery(ctx, &dbSession{kind: kindSQL, db: db, dialect: "sqlite"},
		"select id, payload from items order by id", nil, "query-cancel", 10, 4096,
		func(QueryResultChunk) error {
			calls++
			cancel()
			return nil
		})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	stats := db.Stats()
	if stats.InUse != 0 {
		t.Fatalf("database connection still in use: %#v", stats)
	}
}

func TestStreamSQLQuerySerializesEmission(t *testing.T) {
	db := openStreamingTestDB(t, 25)
	defer db.Close()
	var mu sync.Mutex
	sequences := []int{}
	_, err := streamSQLQuery(context.Background(), &dbSession{kind: kindSQL, db: db, dialect: "sqlite"},
		"select id, payload from items order by id", nil, "query-order", 3, 1024,
		func(chunk QueryResultChunk) error {
			mu.Lock()
			sequences = append(sequences, chunk.Sequence)
			mu.Unlock()
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	for index, sequence := range sequences {
		if sequence != index {
			t.Fatalf("sequences = %#v", sequences)
		}
	}
}

func BenchmarkQueryResultIPC(b *testing.B) {
	db := openStreamingBenchmarkDB(b, 5_000)
	defer db.Close()
	session := &dbSession{kind: kindSQL, db: db, dialect: "sqlite"}
	b.Run("legacy-buffered", func(b *testing.B) {
		for index := 0; index < b.N; index++ {
			result, err := runQuery(context.Background(), session, "select id, payload from items order by id", nil)
			if err != nil || result.RowCount != 5_000 {
				b.Fatalf("result=%v err=%v", result, err)
			}
		}
	})
	b.Run("streamed-chunks", func(b *testing.B) {
		for index := 0; index < b.N; index++ {
			rows := 0
			summary, err := streamSQLQuery(context.Background(), session, "select id, payload from items order by id", nil, "bench", 256, 1024*1024, func(chunk QueryResultChunk) error {
				rows += len(chunk.Rows)
				return nil
			})
			if err != nil || rows != 5_000 || summary.RowCount != 5_000 {
				b.Fatalf("summary=%v rows=%d err=%v", summary, rows, err)
			}
		}
	})
}

func openStreamingBenchmarkDB(b *testing.B, count int) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite", b.TempDir()+"/pressure.db")
	if err != nil {
		b.Fatal(err)
	}
	populateStreamingDB(b, db, count)
	return db
}

func populateStreamingDB(tb testing.TB, db *sql.DB, count int) {
	tb.Helper()
	if _, err := db.Exec("create table items (id integer primary key, payload text)"); err != nil {
		tb.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	for index := 0; index < count; index++ {
		if _, err := tx.Exec("insert into items(id, payload) values(?, ?)", index, "payload-payload-payload"); err != nil {
			tb.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func openStreamingTestDB(t *testing.T, count int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	populateStreamingDB(t, db, count)
	return db
}
