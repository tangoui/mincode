package db

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/mongo"
)

// mongoDeclaredColumns 在集合没有文档时，从集合的 $jsonSchema 校验规则和索引键推断字段。
// 两者都是可选的服务端元数据；读取失败时按“没有声明”处理，由调用方兜底。
func mongoDeclaredColumns(ctx context.Context, database *mongo.Database, collection string) []ColumnInfo {
	var columns []ColumnInfo
	if specs, err := database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: collection}}); err == nil && len(specs) == 1 {
		columns = mongoColumnsFromValidator(specs[0].Options)
	}
	var indexKeys []bson.Raw
	if indexes, err := database.Collection(collection).Indexes().ListSpecifications(ctx); err == nil {
		for _, index := range indexes {
			indexKeys = append(indexKeys, index.KeysDocument)
		}
	}
	return mergeMongoIndexColumns(columns, indexKeys)
}

var mongoSchemaTypeNames = map[string]string{
	"string": "String", "objectId": "ObjectId", "int": "Int32", "long": "Int64",
	"double": "Double", "decimal": "Decimal128", "number": "Double", "integer": "Int64",
	"bool": "Boolean", "boolean": "Boolean", "date": "DateTime", "timestamp": "Timestamp",
	"object": "Document", "array": "Array", "binData": "Binary", "null": "Null",
}

// mongoColumnsFromValidator 读取 options.validator.$jsonSchema 的顶层 properties。
func mongoColumnsFromValidator(options bson.Raw) []ColumnInfo {
	var spec struct {
		Validator struct {
			JSONSchema struct {
				Required   []string `bson:"required"`
				Properties bson.D   `bson:"properties"`
			} `bson:"$jsonSchema"`
		} `bson:"validator"`
	}
	if len(options) == 0 || bson.Unmarshal(options, &spec) != nil {
		return nil
	}
	schema := spec.Validator.JSONSchema
	required := make(map[string]bool, len(schema.Required))
	for _, name := range schema.Required {
		required[name] = true
	}
	note := "来自集合校验规则"
	columns := make([]ColumnInfo, 0, len(schema.Properties))
	for _, property := range schema.Properties {
		var definition struct {
			BSONType    any    `bson:"bsonType"`
			Type        any    `bson:"type"`
			Description string `bson:"description"`
		}
		if raw, err := bson.Marshal(property.Value); err == nil {
			_ = bson.Unmarshal(raw, &definition)
		}
		types, allowsNull := mongoSchemaTypes(definition.BSONType)
		if len(types) == 0 {
			types, allowsNull = mongoSchemaTypes(definition.Type)
		}
		dataType := strings.Join(types, " | ")
		if dataType == "" {
			dataType = "Unknown"
		}
		comment := note
		if description := strings.TrimSpace(definition.Description); description != "" {
			comment = description
		}
		commentCopy := comment
		columns = append(columns, ColumnInfo{
			Name:         property.Key,
			DataType:     dataType,
			Nullable:     allowsNull || !required[property.Key],
			IsPrimaryKey: property.Key == "_id",
			Comment:      &commentCopy,
		})
	}
	return columns
}

// mongoSchemaTypes 把 bsonType/type（字符串或字符串数组）映射为界面使用的 BSON 类型名。
func mongoSchemaTypes(value any) (types []string, allowsNull bool) {
	var names []string
	switch typed := value.(type) {
	case string:
		names = []string{typed}
	case bson.A:
		for _, item := range typed {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
	}
	for _, name := range names {
		if name == "null" {
			allowsNull = true
			continue
		}
		if mapped, ok := mongoSchemaTypeNames[name]; ok {
			types = append(types, mapped)
		} else {
			types = append(types, name)
		}
	}
	return types, allowsNull
}

// mergeMongoIndexColumns 追加索引键中出现、但校验规则未声明的字段，并保证 _id 在首位。
func mergeMongoIndexColumns(columns []ColumnInfo, indexKeys []bson.Raw) []ColumnInfo {
	seen := make(map[string]bool, len(columns))
	for _, column := range columns {
		seen[column.Name] = true
	}
	var indexed []string
	for _, keys := range indexKeys {
		elements, err := keys.Elements()
		if err != nil {
			continue
		}
		for _, element := range elements {
			name := element.Key()
			// 文本索引的内部键不是文档字段。
			if name == "_fts" || name == "_ftsx" || seen[name] {
				continue
			}
			seen[name] = true
			indexed = append(indexed, name)
		}
	}
	if len(columns) == 0 && (len(indexed) == 0 || (len(indexed) == 1 && indexed[0] == "_id")) {
		// 只有默认的 _id 索引，不能说明集合声明了任何字段。
		return nil
	}
	sort.Strings(indexed)
	note := "来自索引"
	for _, name := range indexed {
		comment := note
		dataType := "Unknown"
		if name == "_id" {
			dataType = "ObjectId"
		}
		columns = append(columns, ColumnInfo{Name: name, DataType: dataType, Nullable: name != "_id", IsPrimaryKey: name == "_id", Comment: &comment})
	}
	sort.SliceStable(columns, func(i, j int) bool { return columns[i].Name == "_id" && columns[j].Name != "_id" })
	return columns
}

// mongoIndexes 列出集合索引。与 SQL 方言省略主键一致，省略默认的 _id_ 索引；
// 非升序键（降序、hashed、2dsphere 等）在字段名后标注类型。
func mongoIndexes(ctx context.Context, s *dbSession, database, collection string) ([]DbIndexInfo, error) {
	dbName, err := mongoResolveDatabase(s, database)
	if err != nil {
		return nil, err
	}
	specs, err := s.mongo.Database(dbName).Collection(strings.TrimSpace(collection)).Indexes().ListSpecifications(ctx)
	if err != nil {
		return nil, fmt.Errorf("MongoDB 读取索引失败: %w", err)
	}
	indexes := make([]DbIndexInfo, 0, len(specs))
	for _, spec := range specs {
		if spec.Name == "_id_" {
			continue
		}
		indexes = append(indexes, DbIndexInfo{
			Name:    spec.Name,
			Columns: mongoIndexKeyLabels(spec.KeysDocument),
			Unique:  spec.Unique != nil && *spec.Unique,
		})
	}
	return indexes, nil
}

func mongoIndexKeyLabels(keys bson.Raw) []string {
	elements, err := keys.Elements()
	if err != nil {
		return []string{}
	}
	labels := make([]string, 0, len(elements))
	text := false
	for _, element := range elements {
		name := element.Key()
		if name == "_fts" || name == "_ftsx" {
			text = true
			continue
		}
		value := element.Value()
		switch {
		case value.Type == bsontype.String:
			labels = append(labels, fmt.Sprintf("%s (%s)", name, value.StringValue()))
		case mongoIndexDirection(value) < 0:
			labels = append(labels, name+" DESC")
		default:
			labels = append(labels, name)
		}
	}
	if text {
		labels = append(labels, "(text)")
	}
	return labels
}

func mongoIndexDirection(value bson.RawValue) float64 {
	switch value.Type {
	case bsontype.Int32:
		return float64(value.Int32())
	case bsontype.Int64:
		return float64(value.Int64())
	case bsontype.Double:
		return value.Double()
	}
	return 1
}
