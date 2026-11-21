package db

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func mustMarshal(t *testing.T, value any) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMongoColumnsFromValidator(t *testing.T) {
	options := mustMarshal(t, bson.D{{Key: "validator", Value: bson.D{{Key: "$jsonSchema", Value: bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"name"}},
		{Key: "properties", Value: bson.D{
			{Key: "name", Value: bson.D{{Key: "bsonType", Value: "string"}, {Key: "description", Value: "会话名称"}}},
			{Key: "owner", Value: bson.D{{Key: "bsonType", Value: bson.A{"objectId", "null"}}}},
			{Key: "count", Value: bson.D{{Key: "type", Value: "integer"}}},
		}},
	}}}}})
	columns := mongoColumnsFromValidator(options)
	if len(columns) != 3 {
		t.Fatalf("columns = %+v", columns)
	}
	if got := columns[0]; got.Name != "name" || got.DataType != "String" || got.Nullable || got.Comment == nil || *got.Comment != "会话名称" {
		t.Fatalf("name column = %+v", got)
	}
	if got := columns[1]; got.Name != "owner" || got.DataType != "ObjectId" || !got.Nullable {
		t.Fatalf("owner column = %+v", got)
	}
	if got := columns[2]; got.Name != "count" || got.DataType != "Int64" || !got.Nullable {
		t.Fatalf("count column = %+v", got)
	}
	if columns := mongoColumnsFromValidator(mustMarshal(t, bson.D{{Key: "capped", Value: true}})); len(columns) != 0 {
		t.Fatalf("collection without validator produced columns: %+v", columns)
	}
}

func TestMergeMongoIndexColumns(t *testing.T) {
	idOnly := []bson.Raw{mustMarshal(t, bson.D{{Key: "_id", Value: 1}})}
	if columns := mergeMongoIndexColumns(nil, idOnly); columns != nil {
		t.Fatalf("default _id index must not count as declared fields: %+v", columns)
	}
	indexes := append(idOnly,
		mustMarshal(t, bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}),
		mustMarshal(t, bson.D{{Key: "_fts", Value: "text"}, {Key: "_ftsx", Value: 1}}),
	)
	name := "来自集合校验规则"
	columns := mergeMongoIndexColumns([]ColumnInfo{{Name: "user_id", DataType: "String", Comment: &name}}, indexes)
	var names []string
	for _, column := range columns {
		names = append(names, column.Name)
	}
	want := []string{"_id", "user_id", "created_at"}
	if len(names) != len(want) {
		t.Fatalf("columns = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("columns = %v, want %v", names, want)
		}
	}
	if columns[1].DataType != "String" || !columns[0].IsPrimaryKey || columns[2].DataType != "Unknown" {
		t.Fatalf("merged columns = %+v", columns)
	}
}

func TestMongoIndexKeyLabels(t *testing.T) {
	cases := []struct {
		keys bson.D
		want []string
	}{
		{bson.D{{Key: "user_id", Value: int32(1)}, {Key: "created_at", Value: int32(-1)}}, []string{"user_id", "created_at DESC"}},
		{bson.D{{Key: "location", Value: "2dsphere"}}, []string{"location (2dsphere)"}},
		{bson.D{{Key: "score", Value: -1.0}}, []string{"score DESC"}},
		{bson.D{{Key: "_fts", Value: "text"}, {Key: "_ftsx", Value: int32(1)}}, []string{"(text)"}},
	}
	for _, c := range cases {
		got := mongoIndexKeyLabels(mustMarshal(t, c.keys))
		if len(got) != len(c.want) {
			t.Fatalf("labels for %v = %v, want %v", c.keys, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("labels for %v = %v, want %v", c.keys, got, c.want)
			}
		}
	}
}
