package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	dbStreamChunkRows  = 256
	dbStreamChunkBytes = 1024 * 1024
)

type QueryResultChunk struct {
	QueryID        string   `json:"queryId"`
	Sequence       int      `json:"sequence"`
	Columns        []string `json:"columns,omitempty"`
	Rows           [][]any  `json:"rows"`
	EstimatedBytes int      `json:"estimatedBytes"`
}

type QueryStreamSummary struct {
	QueryID      string  `json:"queryId"`
	RowCount     int     `json:"rowCount"`
	ChunkCount   int     `json:"chunkCount"`
	AffectedRows *int64  `json:"affectedRows,omitempty"`
	ExecutionMs  int64   `json:"executionMs"`
	Notice       *string `json:"notice,omitempty"`
}

type queryChunkEmitter func(QueryResultChunk) error

func streamSQLQuery(ctx context.Context, s *dbSession, query string, database *string, queryID string, maxChunkRows, maxChunkBytes int, emit queryChunkEmitter) (*QueryStreamSummary, error) {
	start := time.Now()
	if !isResultProducing(query) {
		result, err := runReadOnlyQuery(ctx, s, query, database)
		if err != nil {
			return nil, err
		}
		return &QueryStreamSummary{QueryID: queryID, AffectedRows: result.AffectedRows, ExecutionMs: result.ExecutionMs, Notice: result.Notice}, nil
	}
	if maxChunkRows <= 0 {
		maxChunkRows = dbStreamChunkRows
	}
	if maxChunkBytes <= 0 {
		maxChunkBytes = dbStreamChunkBytes
	}

	rows, done, err := querySQLRows(ctx, s, query, database)
	if err != nil {
		return nil, err
	}
	defer done()
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	summary := &QueryStreamSummary{QueryID: queryID}
	chunk := QueryResultChunk{QueryID: queryID, Columns: columns, Rows: [][]any{}, EstimatedBytes: estimateColumnsBytes(columns)}
	totalBytes := chunk.EstimatedBytes
	flush := func() error {
		if len(chunk.Rows) == 0 {
			return nil
		}
		chunk.Sequence = summary.ChunkCount
		if err := emit(chunk); err != nil {
			return err
		}
		summary.ChunkCount++
		chunk = QueryResultChunk{QueryID: queryID, Rows: [][]any{}, EstimatedBytes: 128}
		return nil
	}

	scanner := newSQLRowScanner(len(columns))
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, rowBytes, err := scanner.scan(rows)
		if err != nil {
			return nil, err
		}
		if totalBytes+rowBytes > dbMaxResultBytes || summary.RowCount >= dbMaxResultRows {
			notice := fmt.Sprintf("查询结果已截断：最多返回 %d 行或 %d MiB，请添加 LIMIT 或缩小查询范围", dbMaxResultRows, dbMaxResultBytes/(1024*1024))
			summary.Notice = &notice
			break
		}
		if len(chunk.Rows) > 0 && (len(chunk.Rows) >= maxChunkRows || chunk.EstimatedBytes+rowBytes > maxChunkBytes) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		if rowBytes+chunk.EstimatedBytes > maxChunkBytes && len(chunk.Rows) == 0 {
			notice := "查询结果已截断：单行数据超过流式分块内存边界，请缩小返回列"
			summary.Notice = &notice
			break
		}
		chunk.Rows = append(chunk.Rows, row)
		chunk.EstimatedBytes += rowBytes
		totalBytes += rowBytes
		summary.RowCount++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if summary.RowCount == 0 {
		chunk.Sequence = 0
		if err := emit(chunk); err != nil {
			return nil, err
		}
		summary.ChunkCount = 1
	} else if err := flush(); err != nil {
		return nil, err
	}
	summary.ExecutionMs = time.Since(start).Milliseconds()
	return summary, nil
}

// querySQLRows opens the rows of a read-only streaming query inside the
// session's read guard; call done after closing the rows.
func querySQLRows(ctx context.Context, s *dbSession, query string, database *string) (*sql.Rows, func(), error) {
	if s.dialect == "trino" {
		query = normalizeTrinoQuery(query)
	}
	if s.dialect == "mysql" {
		if d := trimmedOptional(database); d != nil {
			return nil, nil, fmt.Errorf("流式查询暂不支持 MySQL 临时切库，请先选择连接默认库")
		}
	}
	mode := readGuardFor(s.dialect)
	if mode == readGuardNone {
		rows, err := s.db.QueryContext(ctx, query)
		return rows, func() {}, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("获取连接失败: %w", err)
	}
	ex, finish, err := beginReadGuard(ctx, conn, mode)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	rows, err := ex.QueryContext(ctx, query)
	if mode == readGuardOracle && readOnlySnapshotTooOld(err) {
		finish()
		if ex, finish, err = beginReadGuard(ctx, conn, readGuardRollback); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
		rows, err = ex.QueryContext(ctx, query)
	}
	done := func() {
		finish()
		_ = conn.Close()
	}
	if err != nil {
		done()
		return nil, nil, err
	}
	return rows, done, nil
}

func estimateColumnsBytes(columns []string) int {
	bytes := 128
	for _, column := range columns {
		bytes += 32 + len(column)*2
	}
	return bytes
}
