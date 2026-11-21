package db

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"mincode/app/backend/core"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func exportBackupArchive(ctx context.Context, a *backupArchive, s *dbSession, targets []BackupTarget) error {
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch s.kind {
		case kindRedis:
			if err := backupRedis(ctx, a, s, target); err != nil {
				return err
			}
			continue
		case kindMongo:
			if err := backupMongo(ctx, a, s, target); err != nil {
				return err
			}
			continue
		case kindCQL, kindNeo4j:
			if err := backupFullExport(ctx, a, s, target); err != nil {
				return err
			}
			continue
		}
		tables := append([]string(nil), target.Tables...)
		if len(tables) == 0 {
			schema, err := backupDiscovery(ctx, s, target.Database)
			if err != nil {
				return err
			}
			found := false
			for _, db := range schema.Databases {
				if db.Name == target.Database {
					found = true
					for _, t := range db.Tables {
						if !strings.Contains(strings.ToLower(t.Kind), "view") {
							tables = append(tables, t.Name)
						}
					}
				}
			}
			if !found {
				return fmt.Errorf("找不到备份范围 %q", target.Database)
			}
		}
		sort.Strings(tables)
		for _, table := range tables {
			if err := ctx.Err(); err != nil {
				return err
			}
			var err error
			switch s.kind {
			case kindSQL:
				err = backupSQLArchive(ctx, a, s, target.Database, table)
			case kindHTTP:
				err = backupHTTPObject(ctx, a, s, target.Database, table)
			case kindVector:
				err = backupVectorObject(ctx, a, s, target.Database, table)
			default:
				err = fmt.Errorf("该连接缺少完整备份适配器")
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func backupMongo(ctx context.Context, a *backupArchive, s *dbSession, target BackupTarget) error {
	db := s.mongo.Database(target.Database)
	// Raw collection definitions preserve BSON values and option ordering.
	cursor, err := db.ListCollections(ctx, bson.D{})
	if err != nil {
		return err
	}
	defer cursor.Close(ctx)
	selected := map[string]bool{}
	for _, name := range target.Tables {
		selected[name] = true
	}
	found := map[string]bool{}
	for cursor.Next(ctx) {
		var def struct {
			Name string `bson:"name"`
			Type string `bson:"type"`
		}
		if err := bson.Unmarshal(cursor.Current, &def); err != nil {
			return err
		}
		if len(selected) > 0 && !selected[def.Name] {
			continue
		}
		found[def.Name] = true
		definition := base64.StdEncoding.EncodeToString(cursor.Current)
		indexes := []string{}
		if def.Type != "view" {
			ic, err := db.Collection(def.Name).Indexes().List(ctx)
			if err != nil {
				return err
			}
			for ic.Next(ctx) {
				indexes = append(indexes, base64.StdEncoding.EncodeToString(ic.Current))
			}
			err = ic.Err()
			_ = ic.Close(ctx)
			if err != nil {
				return err
			}
		}
		if err := a.metadata(target.Database, def.Name, map[string]any{"encoding": "bson-base64", "definition": definition, "indexes": indexes}); err != nil {
			return err
		}
		if err := a.object(target.Database, def.Name, "bson-base64", func(out *backupArchiveStream) error {
			if def.Type == "view" {
				return nil
			}
			docs, err := db.Collection(def.Name).Find(ctx, bson.D{}, options.Find().SetBatchSize(256))
			if err != nil {
				return err
			}
			defer docs.Close(ctx)
			for docs.Next(ctx) {
				if err := out.record(map[string]string{"bson": base64.StdEncoding.EncodeToString(docs.Current)}); err != nil {
					return err
				}
			}
			return docs.Err()
		}); err != nil {
			return err
		}
	}
	if err := cursor.Err(); err != nil {
		return err
	}
	for name := range selected {
		if !found[name] {
			return fmt.Errorf("集合不存在: %q", name)
		}
	}
	return nil
}

var backupRedisDump = redis.NewScript(`
local v = redis.call('DUMP', KEYS[1])
if not v then return {} end
local ttl = redis.call('PTTL', KEYS[1])
local now = redis.call('TIME')
local expires = 0
if ttl >= 0 then expires = now[1]*1000 + math.floor(now[2]/1000) + ttl end
return {v, tostring(expires)}
`)

func backupRedis(ctx context.Context, a *backupArchive, s *dbSession, target BackupTarget) error {
	name := strings.TrimPrefix(target.Database, "db")
	index, err := strconv.ParseInt(name, 10, 64)
	if err != nil || index < 0 {
		return fmt.Errorf("无效 Redis DB")
	}
	conn, err := redisConn(ctx, s, index)
	if err != nil {
		return err
	}
	defer conn.Close()
	if len(target.Tables) > 0 {
		return fmt.Errorf("Redis 备份按逻辑 DB 执行，不能使用文本 key 选择替代二进制 key")
	}
	return a.object(target.Database, "keys", "redis-dump-base64", func(out *backupArchiveStream) error {
		// SCAN may repeat keys. Record identities are deduplicated in a private
		// disk-backed SQLite set, so backup memory does not grow with key count.
		file, err := os.CreateTemp("", core.AppDataNamespace+"-redis-backup-*.sqlite")
		if err != nil {
			return err
		}
		name := file.Name()
		_ = file.Close()
		defer os.Remove(name)
		set, err := sql.Open("sqlite", name)
		if err != nil {
			return err
		}
		defer set.Close()
		if _, err = set.ExecContext(ctx, "CREATE TABLE seen (key BLOB PRIMARY KEY) WITHOUT ROWID"); err != nil {
			return err
		}
		var cursor uint64
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			keys, next, err := conn.Scan(ctx, cursor, "*", 256).Result()
			if err != nil {
				return err
			}
			for _, key := range keys {
				inserted, err := set.ExecContext(ctx, "INSERT OR IGNORE INTO seen(key) VALUES(?)", []byte(key))
				if err != nil {
					return err
				}
				count, err := inserted.RowsAffected()
				if err != nil {
					return err
				}
				if count == 0 {
					continue
				}
				values, err := backupRedisDump.Run(ctx, conn, []string{key}).Slice()
				if err != nil {
					return err
				}
				if len(values) == 0 {
					return fmt.Errorf("Redis key disappeared during backup; retry with a quiescent source")
				}
				if len(values) != 2 {
					return fmt.Errorf("invalid Redis DUMP response")
				}
				payload, ok := values[0].(string)
				if !ok {
					return fmt.Errorf("invalid Redis DUMP payload")
				}
				if err := out.record(map[string]any{"key": base64.StdEncoding.EncodeToString([]byte(key)), "dump": base64.StdEncoding.EncodeToString([]byte(payload)), "expiresAtMs": values[1]}); err != nil {
					return err
				}
			}
			if next == 0 {
				return nil
			}
			if next == cursor {
				return fmt.Errorf("Redis SCAN cursor did not advance")
			}
			cursor = next
		}
	})
}

type backupTypedValue struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

func backupTyped(v any) (backupTypedValue, error) {
	switch x := v.(type) {
	case nil:
		return backupTypedValue{"null", nil}, nil
	case []byte:
		return backupTypedValue{"binary-base64", base64.StdEncoding.EncodeToString(x)}, nil
	case time.Time:
		return backupTypedValue{"timestamp", x.Format(time.RFC3339Nano)}, nil
	case string:
		return backupTypedValue{"string", x}, nil
	case bool:
		return backupTypedValue{"bool", x}, nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return backupTypedValue{fmt.Sprintf("%T", v), fmt.Sprint(v)}, nil
	case float32:
		return backupTypedValue{"float32", strconv.FormatFloat(float64(x), 'g', -1, 32)}, nil
	case float64:
		return backupTypedValue{"float64", strconv.FormatFloat(x, 'g', -1, 64)}, nil
	case json.Number:
		return backupTypedValue{"number", string(x)}, nil
	default:
		return backupTypedValue{}, fmt.Errorf("unsupported native value type %T; refusing lossy export", v)
	}
}
func backupSQLArchive(ctx context.Context, a *backupArchive, s *dbSession, database, table string) error {
	dialect := s.dialect
	if dialect == "yashandb" {
		dialect = "oracle"
	}
	qualified, err := qualifiedTable(dialect, database, table)
	if err != nil {
		return err
	}
	source := &sessionBackupSource{s: s, dialect: dialect, rowQuerier: s.db}
	// Definitions are mandatory: permission errors cannot silently drop structure.
	ddl, err := source.tableDDL(ctx, database, table)
	if err != nil {
		return err
	}
	definition := map[string]any{"ddl": ddl}
	if s.dialect == "sqlite" || s.dialect == "mysql" {
		schema, schemaErr := readRestoreSchema(ctx, s, database, table)
		if schemaErr == nil {
			definition["schema"] = schema
		} else {
			definition["constrainedUnavailable"] = "schema_not_certified"
		}
	}
	if err := a.metadata(database, table, definition); err != nil {
		return err
	}
	return a.object(database, table, "typed-sql-row", func(out *backupArchiveStream) error {
		return source.streamSQLRows(ctx, "SELECT * FROM "+qualified, nil, func(cols []backupColumn) error {
			return json.NewEncoder(out).Encode(map[string]any{"columns": cols})
		}, func(values []any) error {
			row := make([]backupTypedValue, len(values))
			for i, v := range values {
				cell, err := backupTyped(v)
				if err != nil {
					return err
				}
				row[i] = cell
			}
			return out.record(row)
		})
	})
}
