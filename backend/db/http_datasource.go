// HTTP 数据源（InfluxDB / Elasticsearch）通用分发。
//
// 目前提供连接 + Schema 列举（只读）。查询与写操作后续按需补齐，
// 当前统一由各 handler 的 kindSQL guard 返回明确“不支持”提示。
package db

import (
	"context"
	"fmt"
)

// httpSchema 根据 HTTP 子类型分发 Schema 列举。
func httpSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	switch s.httpKind {
	case httpInflux:
		return influxSchema(ctx, s)
	case httpElastic:
		return elasticsearchSchema(ctx, s)
	case httpCouch:
		return couchSchema(ctx, s)
	case httpMeili:
		return meilisearchSchema(ctx, s)
	case httpDatabricks, httpBigQuery, httpDremio, httpKylin, httpPinot, httpSnowflake:
		return warehouseSchema(ctx, s)
	default:
		return nil, fmt.Errorf("db_get_schema 不支持该 HTTP 数据源类型")
	}
}
