package db

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/bson"
)

func mongoRestoreMetadataEqual(a, b bson.M) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
func mongoAppendIndexSafe(index bson.M) bool { _, ttl := index["expireAfterSeconds"]; return !ttl }
func mongoTargetMetadata(ctx context.Context, s *dbSession, database, collection string) (bson.M, []bson.M, error) {
	db := s.mongo.Database(database)
	cursor, err := db.ListCollections(ctx, bson.M{"name": collection})
	if err != nil {
		return nil, nil, fmt.Errorf("restore_metadata_unavailable")
	}
	defer cursor.Close(ctx)
	if !cursor.Next(ctx) {
		if err := cursor.Err(); err != nil {
			return nil, nil, fmt.Errorf("restore_metadata_unavailable")
		}
		return nil, nil, nil
	}
	var info bson.M
	if err := cursor.Decode(&info); err != nil {
		return nil, nil, err
	}
	indexes, err := db.Collection(collection).Indexes().List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("restore_metadata_unavailable")
	}
	defer indexes.Close(ctx)
	specs := []bson.M{}
	for indexes.Next(ctx) {
		var spec bson.M
		if err := indexes.Decode(&spec); err != nil {
			return nil, nil, err
		}
		delete(spec, "v")
		delete(spec, "ns")
		specs = append(specs, spec)
	}
	if err := indexes.Err(); err != nil {
		return nil, nil, fmt.Errorf("restore_metadata_unavailable")
	}
	sort.Slice(specs, func(i, j int) bool { return fmt.Sprint(specs[i]["name"]) < fmt.Sprint(specs[j]["name"]) })
	return bson.M{"type": info["type"], "options": info["options"]}, specs, nil
}
func validateMongoAppend(ctx context.Context, s *dbSession, z *zip.Reader, m restoreArchiveManifest, mapping restoreMapping) error {
	target, indexes, err := mongoTargetMetadata(ctx, s, mapping.TargetDatabase, mapping.TargetObject)
	if err != nil {
		return err
	}
	if target == nil {
		return nil
	}
	for _, entry := range m.Entries {
		if entry.Database != mapping.Database || entry.Object != mapping.Object {
			continue
		}
		def, rawIndexes, err := restoreMongoMetadata(z, entry, m.Metadata)
		if err != nil {
			return err
		}
		var source bson.M
		if err := bson.Unmarshal(def, &source); err != nil {
			return err
		}
		expected := bson.M{"type": source["type"], "options": source["options"]}
		if !mongoRestoreMetadataEqual(expected, target) {
			return fmt.Errorf("restore_append_schema_mismatch")
		}
		existing := map[string]bson.M{}
		for _, idx := range indexes {
			existing[fmt.Sprint(idx["name"])] = idx
		}
		for _, raw := range rawIndexes {
			var idx bson.M
			if err := bson.Unmarshal(raw, &idx); err != nil {
				return err
			}
			delete(idx, "v")
			delete(idx, "ns")
			if !mongoAppendIndexSafe(idx) {
				return fmt.Errorf("restore_append_ttl_index_forbidden")
			}
			found := existing[fmt.Sprint(idx["name"])]
			if found == nil || !mongoRestoreMetadataEqual(idx, found) {
				return fmt.Errorf("restore_append_index_mismatch")
			}
		}
		return nil
	}
	return fmt.Errorf("restore_source_object_missing")
}
