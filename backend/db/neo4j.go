//go:build !lite

// Neo4j 连接（neo4j-go-driver v5，bolt / Cypher）。
//
// Schema：CALL db.labels() —— 每个 label 作为一张“表”；
// 浏览：MATCH (n:Label) RETURN n SKIP x LIMIT y，属性键并集为列；
// 查询：任意 Cypher，records 动态映射为列/行。
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func buildNeo4jSession(cfg *DbConnectionConfig) (*dbSession, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 7687
	}
	scheme := "bolt"
	if cfg.SSL {
		scheme = "bolt+s"
	}
	uri := fmt.Sprintf("%s://%s:%d", scheme, host, port)

	auth := neo4j.NoAuth()
	if u := derefOr(cfg.Username, ""); u != "" {
		auth = neo4j.BasicAuth(u, derefOr(cfg.Password, ""), "")
	}
	driver, err := neo4j.NewDriverWithContext(uri, auth)
	if err != nil {
		return nil, fmt.Errorf("Neo4j 打开失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := driver.VerifyConnectivity(ctx); err != nil {
		_ = driver.Close(ctx)
		return nil, fmt.Errorf("Neo4j 连接失败: %w", err)
	}
	database := "neo4j"
	if d := trimmedOptional(cfg.Database); d != nil {
		database = *d
	}
	return &dbSession{
		kind:        kindNeo4j,
		neo4jDriver: driver,
		closeExtra: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = driver.Close(ctx)
		},
		neo4jDB:         database,
		defaultDatabase: database,
	}, nil
}

// neo4jRun 在目标库上执行 Cypher 并返回 EagerResult。
func neo4jRun(ctx context.Context, s *dbSession, cypher string) (*neo4j.EagerResult, error) {
	return neo4j.ExecuteQuery(ctx, s.neo4jDriver.(neo4j.DriverWithContext), cypher, nil,
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(s.neo4jDB))
}

// neo4jSchema 列出所有节点 label，作为“表”。
func neo4jSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	res, err := neo4jRun(ctx, s, "CALL db.labels() YIELD label RETURN label ORDER BY label")
	if err != nil {
		return nil, fmt.Errorf("Neo4j Schema 查询失败: %w", err)
	}
	tables := []SchemaTable{}
	for _, rec := range res.Records {
		if v, ok := rec.Get("label"); ok {
			if label, ok := v.(string); ok {
				tables = append(tables, SchemaTable{Name: label, Kind: "label"})
			}
		}
	}
	return &DbSchemaInfo{
		Databases: []SchemaDatabase{{Name: s.neo4jDB, Tables: tables}},
	}, nil
}

// neo4jTableRows 按 label 分页浏览节点。
func neo4jTableRows(ctx context.Context, s *dbSession, label string, limit, offset uint32) (*QueryResult, error) {
	cypher := fmt.Sprintf("MATCH (n:%s) RETURN n SKIP %d LIMIT %d",
		quoteCypherIdent(label), offset, limit)
	return neo4jExecuteQuery(ctx, s, cypher)
}

func quoteCypherIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// neo4jExecuteQuery 执行任意 Cypher，records 动态映射为列/行。
// 若结果只有一列且值为节点，则展开节点属性为列（含 _id / _labels）。
func neo4jExecuteQuery(ctx context.Context, s *dbSession, cypher string) (*QueryResult, error) {
	start := time.Now()
	res, err := neo4jRun(ctx, s, cypher)
	if err != nil {
		return nil, err
	}
	records := res.Records
	keys := res.Keys

	// 单列节点结果：展开属性
	if len(keys) == 1 && len(records) > 0 {
		if _, ok := records[0].Values[0].(neo4j.Node); ok {
			return neo4jNodesToResult(records, start), nil
		}
	}

	rows := make([][]any, 0, len(records))
	for _, rec := range records {
		row := make([]any, len(keys))
		for i := range keys {
			row[i] = normalizeNeo4jValue(rec.Values[i])
		}
		rows = append(rows, row)
	}
	if keys == nil {
		keys = []string{}
	}
	return &QueryResult{
		Columns:     keys,
		Rows:        rows,
		RowCount:    len(rows),
		ExecutionMs: time.Since(start).Milliseconds(),
	}, nil
}

// neo4jNodesToResult 将节点记录展开为属性列（_id、_labels + 属性键并集）。
func neo4jNodesToResult(records []*neo4j.Record, start time.Time) *QueryResult {
	columns := []string{"_id", "_labels"}
	seen := map[string]struct{}{"_id": {}, "_labels": {}}
	nodes := make([]neo4j.Node, 0, len(records))
	for _, rec := range records {
		node, ok := rec.Values[0].(neo4j.Node)
		if !ok {
			continue
		}
		nodes = append(nodes, node)
		for k := range node.Props {
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				columns = append(columns, k)
			}
		}
	}
	rows := make([][]any, 0, len(nodes))
	for _, node := range nodes {
		row := make([]any, 0, len(columns))
		for _, col := range columns {
			switch col {
			case "_id":
				row = append(row, node.ElementId)
			case "_labels":
				row = append(row, strings.Join(node.Labels, ","))
			default:
				if v, ok := node.Props[col]; ok {
					row = append(row, normalizeNeo4jValue(v))
				} else {
					row = append(row, nil)
				}
			}
		}
		rows = append(rows, row)
	}
	return &QueryResult{
		Columns:     columns,
		Rows:        rows,
		RowCount:    len(rows),
		ExecutionMs: time.Since(start).Milliseconds(),
	}
}

// normalizeNeo4jValue 将 Neo4j 值转为可 JSON 序列化的形式。
func normalizeNeo4jValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case neo4j.Node:
		if b, err := json.Marshal(x.Props); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", x.Props)
	case neo4j.Relationship:
		if b, err := json.Marshal(x.Props); err == nil {
			return x.Type + " " + string(b)
		}
		return x.Type
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case []any:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", x)
	case map[string]any:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", x)
	default:
		return x
	}
}
