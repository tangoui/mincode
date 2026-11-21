package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// milvusBackupPage is the entities read per keyset page. Milvus rejects
// offset+limit above 16384, so a full export cannot use offsets: it pages by
// primary key instead ("pk > last ORDER BY pk").
const milvusBackupPage = 1000

// backupMilvus exports every entity of a collection with all output fields
// (scalars, JSON, dense and sparse vectors) as Milvus returns them. Pages
// are keyset-ordered by the primary key with Strong consistency; each page
// is a separate read, not one snapshot. definition is the raw describe
// response, which carries the primary key field.
func backupMilvus(ctx context.Context, a *backupArchive, s *dbSession, database, table string, definition string) error {
	pk, numeric, err := milvusPrimaryKey(definition)
	if err != nil {
		return err
	}
	v := s.vector
	return a.object(database, table, "milvus-entity", func(out *backupArchiveStream) error {
		var last json.RawMessage
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			filter := ""
			if last != nil {
				filter = pk + " > " + string(last)
			}
			payload := map[string]any{
				"collectionName": table, "filter": filter, "limit": milvusBackupPage,
				"outputFields": []string{"*"}, "consistencyLevel": "Strong",
				// The iterator hint (what pymilvus QueryIterator sends) makes
				// the server return each page sorted by primary key.
				"iterator": true, "reduceStopForBest": true,
			}
			if v.database != "" {
				payload["dbName"] = v.database
			}
			body, err := vectorRequest(ctx, v, "POST", v.baseURL+"/v2/vectordb/entities/query", payload)
			if err != nil {
				return err
			}
			if err := validateMilvusResponse(body); err != nil {
				return err
			}
			var page struct {
				Data []map[string]json.RawMessage `json:"data"`
			}
			if err := decodeJSONNumbers(body, &page); err != nil {
				return err
			}
			if len(page.Data) == 0 {
				return nil
			}
			// Keys must be strictly ascending within the page; otherwise a
			// "pk > last" bound could skip entities, so the export fails.
			var next json.RawMessage
			for _, entity := range page.Data {
				key, ok := entity[pk]
				if !ok {
					return fmt.Errorf("Milvus entity has no primary key %q", pk)
				}
				if next != nil && !milvusKeyLess(next, key, numeric) {
					return fmt.Errorf("Milvus returned entities out of primary key order; a complete export cannot be guaranteed")
				}
				next = key
				raw, err := json.Marshal(entity)
				if err != nil {
					return err
				}
				if err := out.record(json.RawMessage(raw)); err != nil {
					return err
				}
			}
			if last != nil && !milvusKeyLess(last, next, numeric) {
				return fmt.Errorf("Milvus primary key cursor did not advance")
			}
			last = next
			if len(page.Data) < milvusBackupPage {
				return nil
			}
		}
	})
}

// milvusPrimaryKey reads the primary key field name and whether it is
// Int64 (else VarChar) from a collections/describe response.
func milvusPrimaryKey(definition string) (string, bool, error) {
	var d struct {
		Data struct {
			Fields []struct {
				Name       string `json:"name"`
				Type       string `json:"type"`
				PrimaryKey bool   `json:"primaryKey"`
			} `json:"fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(definition), &d); err != nil {
		return "", false, fmt.Errorf("Milvus collection description: %w", err)
	}
	for _, f := range d.Data.Fields {
		if !f.PrimaryKey {
			continue
		}
		if f.Name == "" || strings.ContainsAny(f.Name, " \t\r\n\"'`()<>=!&|") {
			return "", false, fmt.Errorf("Milvus primary key name %q cannot be used in a filter", f.Name)
		}
		switch strings.ToLower(f.Type) {
		case "int64":
			return f.Name, true, nil
		case "varchar":
			return f.Name, false, nil
		}
		return "", false, fmt.Errorf("unsupported Milvus primary key type %q", f.Type)
	}
	return "", false, fmt.Errorf("Milvus collection has no primary key field")
}

// milvusKeyLess compares two raw JSON primary keys of the same type.
func milvusKeyLess(a, b json.RawMessage, numeric bool) bool {
	if numeric {
		x, errA := strconv.ParseInt(string(a), 10, 64)
		y, errB := strconv.ParseInt(string(b), 10, 64)
		return errA == nil && errB == nil && x < y
	}
	var x, y string
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return x < y
}

// decodeJSONNumbers decodes keeping numbers as their exact lexemes.
func decodeJSONNumbers(body []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	return dec.Decode(v)
}
