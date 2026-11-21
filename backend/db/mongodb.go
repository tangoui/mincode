// MongoDB 连接与只读浏览（go.mongodb.org/mongo-driver）。
//
// MongoDB 为非 SQL 数据源，走独立通路：
//   - 连接：buildMongoSession
//   - Schema：列出库 / 集合（mongoSchema）
//   - 数据浏览：读取集合文档（mongoTableRows / mongoQuery）
//
// 单元格更新与按主键删除走 BSON 文档编辑通路；DDL 和导入仍返回明确的不支持，
// 避免将 SQL 语义误用于 MongoDB。
package db

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// buildMongoURI 构造 mongodb:// URI（库名进路径、userinfo 百分号编码，与 Tauri 对齐）。
func buildMongoURI(cfg *DbConnectionConfig) (uri, authSource string) {
	authSource = "admin"
	if a := trimmedOptional(cfg.AuthSource); a != nil {
		authSource = *a
	}
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 27017
	}
	dbPath := "/"
	if d := trimmedOptional(cfg.Database); d != nil {
		dbPath = "/" + url.PathEscape(*d)
	}

	user := strings.TrimSpace(derefOr(cfg.Username, ""))
	if user != "" {
		// 有用户名时始终带认证信息（密码可为空）
		pass := derefOr(cfg.Password, "")
		uri = fmt.Sprintf("mongodb://%s:%s@%s:%d%s?authSource=%s",
			url.QueryEscape(user), url.QueryEscape(pass), host, port, dbPath, url.QueryEscape(authSource))
		return uri, authSource
	}
	uri = fmt.Sprintf("mongodb://%s:%d%s", host, port, dbPath)
	return uri, authSource
}

func buildMongoSession(cfg *DbConnectionConfig) (*dbSession, error) {
	uri, authSource := buildMongoURI(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientOptions := options.Client().ApplyURI(uri)
	if cfg.SSL {
		clientOptions.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("MongoDB 连接失败: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("MongoDB 连接失败: %w", err)
	}

	defaultDB := ""
	if d := trimmedOptional(cfg.Database); d != nil {
		defaultDB = *d
	}
	return &dbSession{
		kind:            kindMongo,
		mongo:           client,
		dialect:         "mongodb",
		mongoAuthSource: authSource,
		defaultDatabase: defaultDB,
	}, nil
}

// mongoScopeDatabases 返回要展示的库名列表：
// 指定了 defaultDatabase 时只展示该库，否则枚举账号可见库。
func mongoScopeDatabases(ctx context.Context, s *dbSession) ([]string, error) {
	scope := strings.TrimSpace(s.defaultDatabase)
	if scope == "" {
		if def := s.mongo.Database(""); def != nil && strings.TrimSpace(def.Name()) != "" {
			scope = strings.TrimSpace(def.Name())
		}
	}
	if scope != "" {
		return []string{scope}, nil
	}
	names, err := s.mongo.ListDatabaseNames(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("MongoDB 列出数据库失败: %w", err)
	}
	return names, nil
}

// mongoNavigationSchema 列出导航树所需的库名和集合名，不为每个集合请求统计信息。
func mongoNavigationSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	dbNames, err := mongoScopeDatabases(ctx, s)
	if err != nil {
		return nil, err
	}
	databases := make([]SchemaDatabase, 0, len(dbNames))
	for _, dbName := range dbNames {
		colNames, err := s.mongo.Database(dbName).ListCollectionNames(ctx, bson.D{})
		if err != nil {
			continue
		}
		tables := make([]SchemaTable, 0, len(colNames))
		for _, name := range colNames {
			tables = append(tables, SchemaTable{Name: name, Kind: "collection"})
		}
		databases = append(databases, SchemaDatabase{Name: dbName, Tables: tables})
	}
	if len(databases) == 0 && len(dbNames) > 0 {
		return nil, fmt.Errorf("MongoDB 无法加载任何库的集合列表（无 listCollections 权限或未通过认证），请在连接中填写有权限的「数据库」并确认「认证数据库(authSource)」与账号一致")
	}
	return &DbSchemaInfo{Databases: databases}, nil
}

// mongoSchema 列出库 / 集合及统计信息，供需要统计数据的完整 Schema 操作使用。
func mongoSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	dbNames, err := mongoScopeDatabases(ctx, s)
	if err != nil {
		return nil, err
	}
	var databases []SchemaDatabase
	for _, dbName := range dbNames {
		colNames, err := s.mongo.Database(dbName).ListCollectionNames(ctx, bson.D{})
		if err != nil {
			// 无 listCollections 权限的库跳过（与 Tauri 行为一致）
			continue
		}
		tables := make([]SchemaTable, 0, len(colNames))
		for _, name := range colNames {
			item := SchemaTable{Name: name, Kind: "collection"}
			if count, storageSize, err := mongoCollectionStats(ctx, s.mongo.Database(dbName), name); err == nil {
				item.RowCount = &count
				item.SizeBytes = &storageSize
			}
			tables = append(tables, item)
		}
		databases = append(databases, SchemaDatabase{Name: dbName, Tables: tables})
	}
	if len(databases) == 0 && len(dbNames) > 0 {
		return nil, fmt.Errorf("MongoDB 无法加载任何库的集合列表（无 listCollections 权限或未通过认证），请在连接中填写有权限的「数据库」并确认「认证数据库(authSource)」与账号一致")
	}
	return &DbSchemaInfo{Databases: databases}, nil
}

// mongoDatabaseTables 列出单个库的集合并附带文档数与存储大小，供对象列表按库加载。
// collStats 按集合逐个请求，限制并发以免集合很多时压垮服务端；单个失败只让该集合不显示统计。
func mongoDatabaseTables(ctx context.Context, s *dbSession, database string) ([]SchemaTable, error) {
	dbName, err := mongoResolveDatabase(s, database)
	if err != nil {
		return nil, err
	}
	db := s.mongo.Database(dbName)
	names, err := db.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("MongoDB 列出集合失败: %w", err)
	}
	sort.Strings(names)
	tables := make([]SchemaTable, len(names))
	statsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for index, name := range names {
		tables[index] = SchemaTable{Name: name, Kind: "collection"}
		wg.Add(1)
		go func(item *SchemaTable) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-statsCtx.Done():
				return
			}
			defer func() { <-slots }()
			if count, storageSize, err := mongoCollectionStats(statsCtx, db, item.Name); err == nil {
				item.RowCount = &count
				item.SizeBytes = &storageSize
			}
		}(&tables[index])
	}
	wg.Wait()
	return tables, nil
}

// mongoCollectionStats 通过 collStats 读取集合文档数与存储大小（scale=1 字节）。
// 这是元数据命令、开销低；无权限或视图不支持时返回错误，由调用方降级为不展示统计。
// MongoDB 没有表级创建/更新时间与备注，这两项保持为空。
func mongoCollectionStats(ctx context.Context, db *mongo.Database, name string) (count int64, storageSize int64, err error) {
	var stats struct {
		Count       int64 `bson:"count"`
		StorageSize int64 `bson:"storageSize"`
	}
	err = db.RunCommand(ctx, bson.D{
		{Key: "collStats", Value: name},
		{Key: "scale", Value: 1},
	}).Decode(&stats)
	return stats.Count, stats.StorageSize, err
}

// mongoResolveDatabase 解析目标库：优先入参，否则回退默认库。
func mongoResolveDatabase(s *dbSession, database string) (string, error) {
	if d := strings.TrimSpace(database); d != "" {
		return d, nil
	}
	if d := strings.TrimSpace(s.defaultDatabase); d != "" {
		return d, nil
	}
	return "", fmt.Errorf("MongoDB 操作需要指定数据库")
}

// mongoCollectionReadOnly 标记 MongoDB 保留的 system.* 集合为只读。
// 这些集合由服务端维护；即使账号能够读取，也不代表允许普通文档更新或删除。
func mongoCollectionReadOnly(database, collection string) bool {
	_ = database
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(collection)), "system.")
}

func mongoCollectionWriteError(database, collection string) error {
	if !mongoCollectionReadOnly(database, collection) {
		return nil
	}
	qualifiedName := strings.TrimPrefix(strings.TrimSpace(database)+"."+strings.TrimSpace(collection), ".")
	return fmt.Errorf("MongoDB 系统集合 %q 为只读，不允许更新或删除", qualifiedName)
}

// mongoPkFilter 根据主键列/值构造过滤条件；_id 优先按 ObjectId 解析。
func mongoPkFilter(pkColumn string, pkValue any) (bson.D, error) {
	pk := strings.TrimSpace(pkColumn)
	if pk == "" {
		return nil, fmt.Errorf("MongoDB 操作需要指定主键列（通常为 _id）")
	}
	if pk == "_id" {
		if s, ok := pkValue.(string); ok {
			if oid, err := primitive.ObjectIDFromHex(strings.TrimSpace(s)); err == nil {
				return bson.D{{Key: "_id", Value: oid}}, nil
			}
			// 非 ObjectId 十六进制字符串时按原始字符串匹配
			return bson.D{{Key: "_id", Value: s}}, nil
		}
	}
	return bson.D{{Key: pk, Value: pkValue}}, nil
}

// mongoUpdateCell 更新集合中单个文档的单个字段（$set），按主键定位。
func mongoUpdateCell(ctx context.Context, s *dbSession, database, collection, pkColumn string, pkValue any, column string, value any) (int64, error) {
	dbName, err := mongoResolveDatabase(s, database)
	if err != nil {
		return 0, err
	}
	col := strings.TrimSpace(collection)
	if col == "" {
		return 0, fmt.Errorf("MongoDB 更新需要指定集合名")
	}
	if err := mongoCollectionWriteError(dbName, col); err != nil {
		return 0, err
	}
	field := strings.TrimSpace(column)
	if field == "" {
		return 0, fmt.Errorf("MongoDB 更新需要指定目标字段")
	}
	if field == "_id" {
		return 0, fmt.Errorf("MongoDB 不允许修改 _id 字段")
	}
	filter, err := mongoPkFilter(pkColumn, pkValue)
	if err != nil {
		return 0, err
	}
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := s.mongo.Database(dbName).Collection(col).UpdateOne(
		opCtx,
		filter,
		bson.D{{Key: "$set", Value: bson.D{{Key: field, Value: value}}}},
	)
	if err != nil {
		return 0, fmt.Errorf("MongoDB 更新失败: %w", err)
	}
	return res.ModifiedCount, nil
}

// mongoDeleteRow 按主键删除集合中的单个文档。_id 是 24 位十六进制字符串时，
// 会转换为 ObjectId；其他值保留原类型，以支持字符串自定义 _id。
func mongoDeleteRow(ctx context.Context, s *dbSession, database, collection, pkColumn string, pkValue any) (int64, error) {
	dbName, err := mongoResolveDatabase(s, database)
	if err != nil {
		return 0, err
	}
	col := strings.TrimSpace(collection)
	if col == "" {
		return 0, fmt.Errorf("MongoDB 删除需要指定集合名")
	}
	if err := mongoCollectionWriteError(dbName, col); err != nil {
		return 0, err
	}
	filter, err := mongoPkFilter(pkColumn, pkValue)
	if err != nil {
		return 0, err
	}
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := s.mongo.Database(dbName).Collection(col).DeleteOne(opCtx, filter)
	if err != nil {
		return 0, fmt.Errorf("MongoDB 删除失败: %w", err)
	}
	return res.DeletedCount, nil
}

// mongoInsertRow 向集合插入一个新文档。fields 中的键值对直接映射为文档字段。
// 若 _id 为空字符串则省略（由 MongoDB 自动生成 ObjectId）；若为 24 位十六进制
// 字符串则转换为 primitive.ObjectID。
func mongoInsertRow(ctx context.Context, s *dbSession, database, collection string, fields map[string]any) (int64, error) {
	dbName, err := mongoResolveDatabase(s, database)
	if err != nil {
		return 0, err
	}
	col := strings.TrimSpace(collection)
	if col == "" {
		return 0, fmt.Errorf("MongoDB 插入需要指定集合名")
	}
	if err := mongoCollectionWriteError(dbName, col); err != nil {
		return 0, err
	}
	if len(fields) == 0 {
		return 0, fmt.Errorf("插入字段为空")
	}
	doc := make(bson.D, 0, len(fields))
	for k, v := range fields {
		if k == "_id" {
			// 空字符串表示让 MongoDB 自动生成 ObjectId
			if str, ok := v.(string); ok && strings.TrimSpace(str) == "" {
				continue
			}
			// 24 位十六进制字符串转 ObjectId
			if str, ok := v.(string); ok {
				if oid, oidErr := primitive.ObjectIDFromHex(strings.TrimSpace(str)); oidErr == nil {
					doc = append(doc, bson.E{Key: "_id", Value: oid})
					continue
				}
			}
		}
		doc = append(doc, bson.E{Key: k, Value: v})
	}
	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = s.mongo.Database(dbName).Collection(col).InsertOne(opCtx, doc)
	if err != nil {
		return 0, fmt.Errorf("MongoDB 插入失败: %w", err)
	}
	return 1, nil
}

// mongoTableRows 读取集合文档（分页），将顶层字段展开为结果列，便于直接浏览集合数据。
func mongoTableRows(ctx context.Context, s *dbSession, database, collection string, limit, offset int64) (*QueryResult, error) {
	dbName, err := mongoResolveDatabase(s, database)
	if err != nil {
		return nil, err
	}
	col := strings.TrimSpace(collection)
	if col == "" {
		return nil, fmt.Errorf("MongoDB 需要指定集合名")
	}
	if limit <= 0 {
		limit = 100
	}
	findOpts := options.Find().SetLimit(limit)
	if offset > 0 {
		findOpts.SetSkip(offset)
	}
	cur, err := s.mongo.Database(dbName).Collection(col).Find(ctx, bson.D{}, findOpts)
	if err != nil {
		return nil, fmt.Errorf("MongoDB 查询集合失败: %w", err)
	}
	defer cur.Close(ctx)

	docs := make([]bson.M, 0)
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("MongoDB 解码文档失败: %w", err)
		}
		docs = append(docs, doc)
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("MongoDB 遍历文档失败: %w", err)
	}
	return mongoDocsToQueryResult(docs), nil
}

// mongoDocToJSONValue 将 BSON 文档转成可 JSON 序列化的值（ObjectID / 时间等转字符串）。
func mongoDocToJSONValue(v any) any {
	switch val := v.(type) {
	case bson.M:
		m := make(map[string]any, len(val))
		for k, item := range val {
			m[k] = mongoDocToJSONValue(item)
		}
		return m
	case bson.D:
		m := make(map[string]any, len(val))
		for _, e := range val {
			m[e.Key] = mongoDocToJSONValue(e.Value)
		}
		return m
	case bson.A:
		arr := make([]any, len(val))
		for i, item := range val {
			arr[i] = mongoDocToJSONValue(item)
		}
		return arr
	case primitive.ObjectID:
		// 返回纯 hex 字符串（而非 ObjectID("...") 包装），便于前端回传后按 _id 精确定位。
		return val.Hex()
	case fmt.Stringer:
		return val.String()
	default:
		return v
	}
}

// mongoDocsToQueryResult 将 MongoDB 文档的顶层字段统一展开为结果列。
// `_id` 始终置顶；嵌套对象与数组仍作为单个 JSON 值展示，避免破坏原始结构。
func mongoDocsToQueryResult(docs []bson.M) *QueryResult {
	if len(docs) == 0 {
		return &QueryResult{Columns: []string{}, Rows: [][]any{}, RowCount: 0}
	}

	columns := make([]string, 0)
	seen := make(map[string]struct{})
	hasID := false
	for _, doc := range docs {
		if _, ok := doc["_id"]; ok {
			hasID = true
		}
		for key := range doc {
			if key == "_id" {
				continue
			}
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				columns = append(columns, key)
			}
		}
	}
	if hasID {
		columns = append([]string{"_id"}, columns...)
	}

	rows := make([][]any, 0, len(docs))
	for _, doc := range docs {
		row := make([]any, len(columns))
		for index, key := range columns {
			if value, ok := doc[key]; ok {
				row[index] = mongoDocToJSONValue(value)
			}
		}
		rows = append(rows, row)
	}

	return &QueryResult{Columns: columns, Rows: rows, RowCount: len(rows)}
}

// mongoQuerySpec 前端 buildMongoQuery 生成的 JSON 查询格式。
type mongoQuerySpec struct {
	DB            string          `json:"db"`
	Collection    string          `json:"collection"`
	Filter        json.RawMessage `json:"filter"`
	Limit         *int64          `json:"limit"`
	Skip          *int64          `json:"skip"`
	CursorAfterID *string         `json:"cursorAfterId"`
}

// mongoExecuteQuery 执行前端下发的 MongoDB JSON 查询：
//
//	{"db":"...","collection":"...","filter":{},"limit":100,"skip":0,"cursorAfterId":"..."}
//
// 与 Tauri 对齐：将每个文档的顶层 key 展开为列（保持首次出现顺序），
// 便于前端定位 _id 列做游标分页；空结果返回空列集。
func mongoExecuteQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	var spec mongoQuerySpec
	if err := json.Unmarshal([]byte(query), &spec); err != nil {
		return nil, fmt.Errorf("MongoDB 查询格式错误（需要 JSON）: %w", err)
	}
	col := strings.TrimSpace(spec.Collection)
	if col == "" {
		return nil, fmt.Errorf("缺少 collection 字段")
	}
	dbName, err := mongoResolveDatabase(s, spec.DB)
	if err != nil {
		return nil, err
	}

	limit := int64(100)
	if spec.Limit != nil {
		limit = *spec.Limit
	}
	skip := int64(0)
	if spec.Skip != nil && *spec.Skip > 0 {
		skip = *spec.Skip
	}

	// 解析 filter（可为空）
	filter := bson.M{}
	if len(spec.Filter) > 0 && strings.TrimSpace(string(spec.Filter)) != "null" {
		if err := bson.UnmarshalExtJSON(spec.Filter, true, &filter); err != nil {
			return nil, fmt.Errorf("MongoDB filter 解析失败: %w", err)
		}
	}

	// 游标分页：cursorAfterId 优先于 skip，按 _id 升序取下一页
	useCursor := false
	if spec.CursorAfterID != nil && strings.TrimSpace(*spec.CursorAfterID) != "" {
		oid, oidErr := primitive.ObjectIDFromHex(strings.TrimSpace(*spec.CursorAfterID))
		if oidErr == nil {
			cursorCond := bson.M{"_id": bson.M{"$gt": oid}}
			if len(filter) == 0 {
				filter = cursorCond
			} else {
				filter = bson.M{"$and": bson.A{filter, cursorCond}}
			}
			useCursor = true
		}
	}

	findOpts := options.Find().SetLimit(limit)
	if useCursor {
		findOpts.SetSort(bson.D{{Key: "_id", Value: 1}})
	} else if skip > 0 {
		findOpts.SetSkip(skip)
	}

	cur, err := s.mongo.Database(dbName).Collection(col).Find(ctx, filter, findOpts)
	if err != nil {
		return nil, fmt.Errorf("MongoDB 查询集合失败: %w", err)
	}
	defer cur.Close(ctx)

	docs := make([]bson.M, 0)
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("MongoDB 解码文档失败: %w", err)
		}
		docs = append(docs, doc)
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("MongoDB 遍历文档失败: %w", err)
	}

	return mongoDocsToQueryResult(docs), nil
}

// mongoColumns 采样集合内最多 50 条文档，推断字段及其 BSON 类型（与 Tauri 对齐）。
// MongoDB 无固定 schema，因此列信息为“采样推断”，字段按首次出现顺序返回，
// 每个字段的类型取首个非缺失取值对应的 BSON 类型名。
func mongoColumns(ctx context.Context, s *dbSession, database, collection string) ([]ColumnInfo, error) {
	dbName, err := mongoResolveDatabase(s, database)
	if err != nil {
		return nil, err
	}
	col := strings.TrimSpace(collection)
	if col == "" {
		return nil, fmt.Errorf("MongoDB 需要指定集合名")
	}

	// 采样列推断加超时保护，避免大集合或慢网络下前端「加载表结构」永久转圈。
	// log 等海量集合无排序 Find 会走 COLLSCAN 较慢，故：
	//  1) 采样量压到 20 条即可推断字段结构；
	//  2) SetMaxTime 让 MongoDB 服务端主动限时中断慢查询；
	//  3) 客户端 context 兜底 6s。
	sampleCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	findOpts := options.Find().
		SetLimit(20).
		SetBatchSize(20).
		SetMaxTime(5 * time.Second)
	cur, err := s.mongo.Database(dbName).Collection(col).Find(sampleCtx, bson.D{}, findOpts)
	if err != nil {
		return nil, fmt.Errorf("MongoDB 查询集合失败: %w", err)
	}
	defer cur.Close(sampleCtx)

	// 保持字段首次出现顺序，类型取首次出现值
	fieldOrder := make([]string, 0)
	fieldTypes := make(map[string]string)
	for cur.Next(sampleCtx) {
		var doc bson.D
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("MongoDB 解码文档失败: %w", err)
		}
		for _, e := range doc {
			if _, ok := fieldTypes[e.Key]; !ok {
				fieldTypes[e.Key] = bsonTypeName(e.Value)
				fieldOrder = append(fieldOrder, e.Key)
			}
		}
	}
	// 采样超时/中断时降级：只要已采到字段就返回，避免前端「加载表结构」永久转圈。
	// 若一个字段都没采到且确实出错，才向上抛错。
	if err := cur.Err(); err != nil && len(fieldOrder) == 0 {
		return nil, fmt.Errorf("MongoDB 遍历文档失败: %w", err)
	}

	// 集合没有可采样的文档：退回到集合声明的结构（$jsonSchema 校验规则与索引字段），
	// 都没有时仍返回 _id，并在备注里说明原因，避免看起来像只有一个字段。
	if len(fieldOrder) == 0 {
		declared := mongoDeclaredColumns(sampleCtx, s.mongo.Database(dbName), col)
		if len(declared) > 0 {
			return declared, nil
		}
		note := "集合暂无文档，无法采样推断字段"
		return []ColumnInfo{{Name: "_id", DataType: "ObjectId", Nullable: true, IsPrimaryKey: true, Comment: &note}}, nil
	}

	// _id 强制置顶：即使采样文档里 _id 不在首位（或用 bson.M 乱序），主键列也应排最前。
	if _, ok := fieldTypes["_id"]; ok && (len(fieldOrder) == 0 || fieldOrder[0] != "_id") {
		reordered := make([]string, 0, len(fieldOrder))
		reordered = append(reordered, "_id")
		for _, name := range fieldOrder {
			if name != "_id" {
				reordered = append(reordered, name)
			}
		}
		fieldOrder = reordered
	}

	cols := make([]ColumnInfo, 0, len(fieldOrder))
	for _, name := range fieldOrder {
		cols = append(cols, ColumnInfo{
			Name:          name,
			DataType:      fieldTypes[name],
			Nullable:      true,
			IsPrimaryKey:  name == "_id",
			DefaultValue:  nil,
			AutoIncrement: false,
		})
	}
	return cols, nil
}

// bsonTypeName 将 bson.D 解码后的 Go 值映射为 BSON 类型名（与 Tauri bson_type_name 对齐）。
func bsonTypeName(v any) string {
	switch v.(type) {
	case float64, float32:
		return "Double"
	case string:
		return "String"
	case bson.A, []any:
		return "Array"
	case bson.D, bson.M:
		return "Document"
	case bool:
		return "Boolean"
	case nil:
		return "Null"
	case int32:
		return "Int32"
	case int64, int:
		return "Int64"
	case primitive.ObjectID:
		return "ObjectId"
	case primitive.DateTime:
		return "DateTime"
	case primitive.Timestamp:
		return "Timestamp"
	case primitive.Binary:
		return "Binary"
	case primitive.Decimal128:
		return "Decimal128"
	default:
		return "Unknown"
	}
}
