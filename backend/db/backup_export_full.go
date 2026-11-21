//go:build !lite

package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gocql/gocql"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func backupFullExportSupported(s *dbSession) bool { return s.kind == kindNeo4j || s.kind == kindCQL }
func backupFullExport(ctx context.Context, a *backupArchive, s *dbSession, target BackupTarget) error {
	if s.kind == kindNeo4j {
		return backupNeo4j(ctx, a, s, target)
	}
	session, ok := s.cql.(*gocql.Session)
	if !ok {
		return fmt.Errorf("invalid Cassandra session")
	}
	meta, err := session.KeyspaceMetadata(target.Database)
	if err != nil {
		return err
	}
	tables := target.Tables
	if len(tables) == 0 {
		for name := range meta.Tables {
			tables = append(tables, name)
		}
	}
	for _, table := range tables {
		t, ok := meta.Tables[table]
		if !ok {
			return fmt.Errorf("unknown Cassandra table")
		}
		if err := a.metadata(target.Database, table, t); err != nil {
			return err
		}
		if err := a.object(target.Database, table, "cql-typed-row", func(out *backupArchiveStream) error {
			iter := session.Query("SELECT * FROM " + quoteCQLIdent(target.Database) + "." + quoteCQLIdent(table)).WithContext(ctx).PageSize(256).Iter()
			defer iter.Close()
			for {
				row := map[string]any{}
				if !iter.MapScan(row) {
					break
				}
				typed := map[string]any{}
				for name, value := range row {
					v, err := backupCQLValue(value)
					if err != nil {
						return err
					}
					typed[name] = v
				}
				if err := out.record(typed); err != nil {
					return err
				}
			}
			return iter.Close()
		}); err != nil {
			return err
		}
	}
	return nil
}
func backupCQLValue(v any) (any, error) {
	if scalar, err := backupTyped(v); err == nil {
		return scalar, nil
	}
	switch x := v.(type) {
	case gocql.UUID:
		return backupTypedValue{"uuid", x.String()}, nil
	}
	// gocql compound values retain their Go type alongside a JSON value. Fail
	// on unsupported encodings rather than coercing them into display strings.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return backupTypedValue{fmt.Sprintf("cql:%T", v), json.RawMessage(raw)}, nil
}
func backupNeo4j(ctx context.Context, a *backupArchive, s *dbSession, target BackupTarget) error {
	if len(target.Tables) > 0 && (len(target.Tables) != 1 || target.Tables[0] != "graph") {
		return fmt.Errorf("Neo4j backup selects the whole graph to preserve relationship endpoints")
	}
	driver := s.neo4jDriver.(neo4j.DriverWithContext)
	session := driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: target.Database, AccessMode: neo4j.AccessModeRead, FetchSize: 256})
	defer session.Close(ctx)
	for _, spec := range []struct{ name, query string }{
		{"nodes", "MATCH (n) RETURN elementId(n) AS id, labels(n) AS labels, properties(n) AS properties"},
		{"relationships", "MATCH (a)-[r]->(b) RETURN elementId(r) AS id, elementId(a) AS source, elementId(b) AS target, type(r) AS type, properties(r) AS properties"},
		{"indexes", "SHOW INDEXES"}, {"constraints", "SHOW CONSTRAINTS"},
	} {
		if err := a.object(target.Database, spec.name, "neo4j-record", func(out *backupArchiveStream) error {
			result, err := session.Run(ctx, spec.query, nil)
			if err != nil {
				return err
			}
			for result.Next(ctx) {
				record := result.Record()
				row := map[string]any{}
				for i, key := range record.Keys {
					v, err := backupGraphValue(record.Values[i])
					if err != nil {
						return err
					}
					row[key] = v
				}
				if err := out.record(row); err != nil {
					return err
				}
			}
			return result.Err()
		}); err != nil {
			return err
		}
	}
	return nil
}
func backupGraphValue(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			p, err := backupGraphValue(v)
			if err != nil {
				return nil, err
			}
			out[k] = p
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			p, err := backupGraphValue(v)
			if err != nil {
				return nil, err
			}
			out[i] = p
		}
		return out, nil
	case []string:
		return x, nil
	default:
		return backupTyped(v)
	}
}
