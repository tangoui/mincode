package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mincode/app/backend/core"
)

// BackupCapability tells the editor what a connection can back up. Reason
// and Limitations are stable codes that the UI translates (the archive
// manifest keeps them as codes too). Scope names what an "object" is for the
// engine: table, collection, keys (Redis logical DB), index, document,
// measurement or graph. SQLFormat reports whether the legacy SQL script
// format can run for this connection.
type BackupFormatCapability struct {
	Format       string `json:"format"`
	Export       bool   `json:"export"`
	Constrained  bool   `json:"constrained"`
	Advanced     bool   `json:"advanced"`
	Recovery     string `json:"recovery"`
	Verification string `json:"verification"`
}
type BackupCapability struct {
	Version       int                      `json:"version"`
	Formats       []BackupFormatCapability `json:"formats"`
	Engine        string                   `json:"engine"`
	Supported     bool                     `json:"supported"`
	Format        string                   `json:"format"`
	Fidelity      string                   `json:"fidelity"`
	Reason        string                   `json:"reason,omitempty"`
	Limitations   []string                 `json:"limitations"`
	Scope         string                   `json:"scope"`
	SQLFormat     bool                     `json:"sqlFormat"`
	DefaultFormat string                   `json:"defaultFormat"`
	Recovery      string                   `json:"recovery"`
	Verification  string                   `json:"verification"`
}

func backupSessionCapability(s *dbSession, engine string) BackupCapability {
	c := BackupCapability{Engine: engine, Format: "archive", Fidelity: "logical-data", Scope: "table", Limitations: []string{"noGlobalSnapshot"}}
	_, sqlErr := backupDumpDialect(s)
	c.SQLFormat = sqlErr == nil
	c.DefaultFormat = "archive"
	if c.SQLFormat {
		c.DefaultFormat = "sql"
	}
	c.Recovery = "manual"
	if s.kind == kindSQL && (s.dialect == "mysql" || s.dialect == "sqlite") {
		c.Recovery = "structured-subset-or-advanced"
	} else if s.kind == kindSQL || s.kind == kindMongo || s.kind == kindRedis {
		c.Recovery = "advanced"
	}
	// Adapter existence is not evidence of a live recovery test.
	c.Verification = "unverified"

	switch s.kind {
	case kindSQL:
		c.Supported = true
		c.Limitations = append(c.Limitations, "sqlAdvancedObjects")
	case kindMongo:
		c.Supported = true
		c.Fidelity = "bson-collections-and-indexes"
		c.Scope = "collection"
		c.Limitations = append(c.Limitations, "mongoNoOplog")
	case kindRedis:
		c.Supported = true
		c.Fidelity = "dump-with-absolute-expiry"
		c.Scope = "keys"
		c.Limitations = append(c.Limitations, "redisScan", "redisRecoverySubset")
	case kindHTTP:
		switch s.httpKind {
		case httpElastic, httpMeili:
			c.Supported = true
			c.Scope = "index"
			c.Limitations = append(c.Limitations, "searchStoredOnly")
		case httpCouch:
			c.Supported = true
			c.Scope = "document"
			c.Limitations = append(c.Limitations, "couchRevisions")
		case httpInflux:
			c.Supported = true
			c.Scope = "measurement"
		default:
			c.Supported = s.warehouse != nil
			c.Limitations = append(c.Limitations, "warehouseLogical")
		}
	case kindVector:
		c.Scope = "collection"
		if s.vector != nil {
			switch s.vector.provider {
			case "qdrant", "chroma", "weaviate", "pinecone", "milvus":
				c.Supported = true
			default:
				c.Reason = "vectorNoIterator"
			}
		}
	case kindCQL:
		c.Supported = backupFullExportSupported(s)
		c.Limitations = append(c.Limitations, "cqlTTL")
	case kindNeo4j:
		c.Supported = backupFullExportSupported(s)
		c.Scope = "graph"
		c.Limitations = append(c.Limitations, "graphIDs")
	case kindPlugin:
		if s.ext != nil && s.ext.info.BackupVersion == 1 {
			_, c.Supported = s.ext.conn.(ExternalBackupConnection)
		}
		c.Fidelity = "plugin-native"
		c.Reason = "pluginNoStream"
	default:
		c.Reason = "noAdapter"
	}
	if c.Supported {
		c.Reason = ""
	}
	if !c.Supported && c.Reason == "" {
		c.Reason = "noAdapter"
	}
	c.Version = 1
	c.Formats = []BackupFormatCapability{{Format: "archive", Export: c.Supported, Constrained: (s.kind == kindSQL && (s.dialect == "mysql" || s.dialect == "sqlite")) || s.kind == kindMongo || s.kind == kindRedis, Advanced: s.kind == kindSQL || s.kind == kindMongo || s.kind == kindRedis, Recovery: c.Recovery, Verification: "unverified"}}
	if c.SQLFormat {
		c.Formats = append(c.Formats, BackupFormatCapability{Format: "sql", Export: true, Constrained: false, Advanced: s.kind == kindSQL, Recovery: "advanced-or-native-tools", Verification: "unverified"})
	}
	return c
}

// backupCapabilityText is the Chinese text of a capability code for run
// history and notifications, which store plain messages. The editor shows
// translated text from the same codes.
var backupCapabilityText = map[string]string{
	"noGlobalSnapshot":    "不包含账号、权限、事务日志，也不是全局时间点快照",
	"sqlAdvancedObjects":  "存储过程、外键等高级对象可能需要单独恢复",
	"mongoNoOplog":        "文档按读取时间导出，不是 oplog 一致性快照",
	"redisRecoverySubset": "自动恢复仅支持基础 RDB 编码；压缩、紧凑编码和流等需使用原生工具手工恢复",
	"redisScan":           "SCAN 不是全局快照，导出期间修改的 key 可能前后不一致；不含集群拓扑",
	"searchStoredOnly":    "只能导出已存储的字段，未存储字段需从源数据重建",
	"couchRevisions":      "不包含冲突修订和历史版本",
	"warehouseLogical":    "只导出当前授权可读的结果数据，不是底层存储的物理备份",
	"cqlTTL":              "不保留 TTL 和写入时间戳",
	"graphIDs":            "恢复时节点和关系会获得新 ID",
	"vectorNoIterator":    "该向量库没有完整遍历接口，分页浏览不能作为备份",
	"pluginNoStream":      "该驱动插件未提供原生备份流，不能用预览数据冒充完整备份",
	"noAdapter":           "该连接类型没有备份适配器",
}

func backupCapabilityMessages(codes ...string) []string {
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		if text, ok := backupCapabilityText[code]; ok {
			out = append(out, text)
		} else if code != "" {
			out = append(out, code)
		}
	}
	return out
}

func init() {
	core.RegisterCtx("db_backup_discover", func(ctx context.Context, _, raw string) (string, error) {
		var p struct {
			ConnectionID string `json:"connectionId"`
			Database     string `json:"database"`
		}
		if err := core.DecodeArgs(raw, &p); err != nil {
			return "", err
		}
		if strings.TrimSpace(p.ConnectionID) == "" || len(p.ConnectionID) > 128 || len(p.Database) > 4096 {
			return "", fmt.Errorf("无效的备份连接或数据库")
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		saved, err := loadSavedConnection(p.ConnectionID)
		if err != nil {
			return "", err
		}
		s, err := openBackupSession(ctx, saved)
		if err != nil {
			return "", err
		}
		defer s.close()
		c := backupSessionCapability(s, saved.DbType)
		schema, err := backupDiscovery(ctx, s, p.Database)
		normalizeSchemaInfo(schema)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(struct {
			Capability BackupCapability `json:"capability"`
			Schema     *DbSchemaInfo    `json:"schema"`
		}{c, schema})
	})
}

func backupDiscovery(ctx context.Context, s *dbSession, database string) (*DbSchemaInfo, error) {
	if s.kind == kindNeo4j {
		return &DbSchemaInfo{Databases: []SchemaDatabase{{Name: s.neo4jDB, Tables: []SchemaTable{{Name: "graph", Kind: "graph"}}}}}, nil
	}
	if s.kind == kindRedis {
		indexes := redisSchemaDBIndexes(ctx, s)
		if len(indexes) == 0 {
			indexes = []int64{0}
		}
		out := &DbSchemaInfo{Databases: []SchemaDatabase{}}
		for _, n := range indexes {
			out.Databases = append(out.Databases, SchemaDatabase{Name: fmt.Sprintf("db%d", n), Tables: []SchemaTable{}})
		}
		return out, nil
	}
	if s.kind == kindSQL && s.vector != nil && s.vector.provider == "pgvector" {
		// pgvector is an extension, not a filter for the scope of a backup.
		return postgresSchema(ctx, s)
	}
	if database == "" {
		return getNavigationSchema(ctx, s)
	}
	if s.kind == kindMongo {
		names, err := s.mongo.Database(database).ListCollectionNames(ctx, map[string]any{})
		if err != nil {
			return nil, err
		}
		tables := []SchemaTable{}
		for _, name := range names {
			tables = append(tables, SchemaTable{Name: name, Kind: "collection"})
		}
		return &DbSchemaInfo{Databases: []SchemaDatabase{{Name: database, Tables: tables}}}, nil
	}
	if s.kind == kindPlugin || (s.kind == kindSQL && (s.dialect == "mysql" || s.dialect == "sqlite" || supportsTableStats(s.dialect))) {
		tables, err := getDatabaseTables(ctx, s, database)
		if err != nil {
			return nil, err
		}
		if tables == nil {
			tables = []SchemaTable{}
		}
		return &DbSchemaInfo{Databases: []SchemaDatabase{{Name: database, Tables: tables}}}, nil
	}
	return getSchema(ctx, s)
}
