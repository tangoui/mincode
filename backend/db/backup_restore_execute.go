package db

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"io"
	"time"

	"mincode/app/backend/tasks"
)

type restoreMapping struct {
	Database       string `json:"database"`
	Object         string `json:"object"`
	TargetDatabase string `json:"targetDatabase"`
	TargetObject   string `json:"targetObject"`
}
type RestorePrepareRequest struct {
	ConnectionID  string           `json:"connectionId"`
	WorkspaceID   string           `json:"workspaceId,omitempty"`
	SourceDialect string           `json:"sourceDialect,omitempty"`
	Path          string           `json:"path"`
	Mode          string           `json:"mode"`
	Mappings      []restoreMapping `json:"mappings"`
	AllowExisting bool             `json:"allowExisting"`
}
type RestoreSubmitRequest struct {
	PlanID                   string `json:"planId"`
	IdempotencyKey           string `json:"idempotencyKey"`
	UnrestrictedRiskAccepted bool   `json:"unrestrictedRiskAccepted"`
	AllowExisting            bool   `json:"allowExisting"`
}
type restorePrepared struct {
	Source        preparedRestoreSource `json:"source"`
	ConnectionID  string                `json:"connectionId"`
	ConfigHash    string                `json:"configHash"`
	TargetHash    string                `json:"targetHash"`
	Mode          string                `json:"mode"`
	Mappings      []restoreMapping      `json:"mappings"`
	AllowExisting bool                  `json:"allowExisting"`
	Plan          RestorePlan           `json:"plan"`
	WorkspaceID   string                `json:"workspaceId,omitempty"`
}

func executeRestore(ctx context.Context, task tasks.Task, reporter *tasks.Reporter) (tasks.Result, error) {
	var p restorePrepared
	if err := json.Unmarshal(task.Request.Payload, &p); err != nil {
		return tasks.Result{}, fmt.Errorf("restore_invalid_preparation")
	}
	defer p.Source.Close()
	defer releaseTargetFilePin(task.Request.PreparationID)
	if err := p.Source.Verify(ctx); err != nil {
		return tasks.Result{}, err
	}
	saved, err := loadSavedConnection(p.ConnectionID)
	if err != nil {
		return tasks.Result{}, err
	}
	if connectionFingerprint(saved) != p.ConfigHash {
		return tasks.Result{}, fmt.Errorf("restore_connection_changed")
	}
	if err = verifyTransferTargetFile(task.Request.PreparationID, saved); err != nil {
		return tasks.Result{}, err
	}
	if _, err = transferResourceKeys(saved); err != nil {
		return tasks.Result{}, err
	}
	s, err := openBackupSession(ctx, saved)
	if err != nil {
		return tasks.Result{}, fmt.Errorf("restore_target_unavailable")
	}
	defer s.close()
	hash, err := restoreTargetFingerprint(ctx, s, p.Mappings)
	if err != nil {
		return tasks.Result{}, err
	}
	if hash != p.TargetHash {
		return tasks.Result{}, fmt.Errorf("restore_target_changed")
	}
	if p.Mode == "constrained" {
		if err := validateRestoreDestination(ctx, s, p.Mappings, p.AllowExisting); err != nil {
			return tasks.Result{}, err
		}
		if s.kind == kindSQL {
			return executeConstrainedRestore(ctx, s, p, reporter)
		}
		return executeNativePreparedRestore(ctx, s, p, reporter)
	}
	runner := &restoreRunner{ctx: ctx, s: s, reporter: reporter, pinned: p.Source.file, pinnedSize: p.Source.Size, opts: RestoreOptions{AllowExisting: p.AllowExisting, AllowDestructive: true}, mappings: map[string]restoreMapping{}}
	for _, mapping := range p.Mappings {
		runner.mappings[mapping.Database+"\x00"+mapping.Object] = mapping
	}
	if p.Source.Format == restoreSQL {
		err = runner.sqlScript("")
	} else {
		err = runner.archive("")
	}
	if err != nil {
		if ctx.Err() != nil {
			return tasks.Result{}, ctx.Err()
		}
		return tasks.Result{Certainty: tasks.EffectsUncertain}, fmt.Errorf("restore_advanced_execution_failed")
	}
	if p.Source.Format == restoreSQL {
		return tasks.Result{Certainty: tasks.EffectsUncertain}, nil
	}
	return tasks.Result{}, nil
}
func archiveSchemas(z *zip.Reader, m restoreArchiveManifest) (map[string]restoreSchema, error) {
	schemas := map[string]restoreSchema{}
	for _, e := range m.Metadata {
		for _, f := range z.File {
			if f.Name != e.Path {
				continue
			}
			var doc struct {
				Definition struct {
					Schema *restoreSchema `json:"schema"`
				}
			}
			if err := readZipJSON(f, &doc); err != nil {
				return nil, err
			}
			if doc.Definition.Schema != nil {
				schemas[e.Database+"\x00"+e.Object] = *doc.Definition.Schema
			}
		}
	}
	return schemas, nil
}
func readPreparedManifest(p *preparedRestoreSource) (*zip.Reader, restoreArchiveManifest, error) {
	z, err := zip.NewReader(p.file, p.Size)
	if err != nil {
		return nil, restoreArchiveManifest{}, err
	}
	for _, f := range z.File {
		if f.Name == "manifest.json" {
			var m restoreArchiveManifest
			err := readZipJSON(f, &m)
			return z, m, err
		}
	}
	return nil, restoreArchiveManifest{}, fmt.Errorf("restore_manifest_missing")
}
func executeConstrainedRestore(ctx context.Context, s *dbSession, p restorePrepared, r *tasks.Reporter) (tasks.Result, error) {
	if s.kind != kindSQL {
		return tasks.Result{}, fmt.Errorf("restore_constrained_engine_unsupported")
	}
	z, m, err := readPreparedManifest(&p.Source)
	if err != nil {
		return tasks.Result{}, err
	}
	schemas, err := archiveSchemas(z, m)
	if err != nil {
		return tasks.Result{}, err
	}
	for _, mapping := range p.Mappings {
		schema := schemas[mapping.Database+"\x00"+mapping.Object]
		statements, err := schema.Statements(s.dialect, mapping.TargetDatabase, mapping.TargetObject)
		if err != nil {
			return tasks.Result{}, err
		}
		existing, err := restoreSQLConflicts(ctx, s, []RestorePlanItem{{Database: mapping.TargetDatabase, Object: mapping.TargetObject}})
		if err != nil {
			return tasks.Result{}, err
		}
		if len(existing) > 0 {
			if !p.AllowExisting {
				return tasks.Result{}, fmt.Errorf("restore_target_not_empty")
			}
			target, err := readRestoreSchema(ctx, s, mapping.TargetDatabase, mapping.TargetObject)
			if err != nil {
				return tasks.Result{}, err
			}
			// Compare structural descriptors, ignoring source index names only.
			if !restoreSchemasCompatible(schema, target) {
				return tasks.Result{}, fmt.Errorf("restore_append_schema_mismatch")
			}
		} else {
			id, err := r.BeginEffect(ctx, tasks.EffectIntent{Kind: "create-table"})
			if err != nil {
				return tasks.Result{}, err
			}
			if _, err = s.db.ExecContext(ctx, statements[0]); err != nil {
				return tasks.Result{}, fmt.Errorf("restore_create_failed")
			}
			if err = r.FinishEffect(ctx, id, tasks.EffectOutcome{Objects: 1}); err != nil {
				return tasks.Result{}, err
			}
		}
		for _, entry := range m.Entries {
			if entry.Database != mapping.Database || entry.Object != mapping.Object {
				continue
			}
			for _, f := range z.File {
				if f.Name == entry.Path {
					if err := restorePreparedRows(ctx, s, mapping, f, r); err != nil {
						return tasks.Result{}, err
					}
				}
			}
		}
		if len(existing) == 0 {
			for _, stmt := range statements[1:] {
				id, err := r.BeginEffect(ctx, tasks.EffectIntent{Kind: "create-index"})
				if err != nil {
					return tasks.Result{}, err
				}
				if _, err = s.db.ExecContext(ctx, stmt); err != nil {
					return tasks.Result{}, fmt.Errorf("restore_index_failed")
				}
				if err = r.FinishEffect(ctx, id, tasks.EffectOutcome{}); err != nil {
					return tasks.Result{}, err
				}
			}
		}
	}
	return tasks.Result{}, nil
}
func restoreSchemasCompatible(a, b restoreSchema) bool {
	for i := range a.Indexes {
		a.Indexes[i].Name = ""
	}
	for i := range b.Indexes {
		b.Indexes[i].Name = ""
	}
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
func restorePreparedRows(ctx context.Context, s *dbSession, m restoreMapping, f *zip.File, r *tasks.Reporter) error {
	in, err := f.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 4096), defaultTransferLimits().RecordBytes)
	if !sc.Scan() {
		return fmt.Errorf("restore_columns_missing")
	}
	var head struct {
		Columns []backupColumn `json:"columns"`
	}
	if err := strictTransferJSON(sc.Bytes(), &head); err != nil {
		return err
	}
	qualified, err := qualifiedTable(s.dialect, m.TargetDatabase, m.TargetObject)
	if err != nil {
		return err
	}
	names, marks := []string{}, []string{}
	for i, c := range head.Columns {
		names = append(names, schemaQuoteIdentifier(s.dialect, c.Name))
		marks = append(marks, restorePlaceholder(s.dialect, i+1))
	}
	insert := "INSERT INTO " + qualified + " (" + joinSQL(names) + ") VALUES (" + joinSQL(marks) + ")"
	for {
		batch := [][]any{}
		for len(batch) < restoreBatchRows && sc.Scan() {
			var cells []backupTypedValue
			if err := strictTransferJSON(sc.Bytes(), &cells); err != nil {
				return err
			}
			args := []any{}
			for _, cell := range cells {
				v, err := restoreBoundValue(cell)
				if err != nil {
					return err
				}
				args = append(args, v)
			}
			batch = append(batch, args)
		}
		if err := sc.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		id, err := r.BeginEffect(ctx, tasks.EffectIntent{Kind: "insert-batch"})
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, insert)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("restore_insert_prepare_failed")
		}
		for _, args := range batch {
			if _, err = stmt.ExecContext(ctx, args...); err != nil {
				break
			}
		}
		_ = stmt.Close()
		if err != nil {
			rollbackErr := tx.Rollback()
			if rollbackErr == nil {
				if ack := r.FinishEffect(ctx, id, tasks.EffectOutcome{NoEffect: true}); ack != nil {
					return ack
				}
			}
			return fmt.Errorf("restore_insert_failed")
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("restore_commit_uncertain")
		}
		if err = r.FinishEffect(ctx, id, tasks.EffectOutcome{Records: int64(len(batch))}); err != nil {
			return err
		}
	}
}
func joinSQL(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// Keep execution on backend-owned bytes. This helper verifies and pins a staged
// file before any target operation; the owner releases it after task completion.
func reopenPrepared(ctx context.Context, p *preparedRestoreSource) error {
	if err := p.Verify(ctx); err != nil {
		return err
	}
	_, err := p.file.Seek(0, io.SeekStart)
	return err
}

func validatePreparedSchemaColumns(z *zip.Reader, m restoreArchiveManifest, schemas map[string]restoreSchema) error {
	for _, entry := range m.Entries {
		schema, ok := schemas[entry.Database+"\x00"+entry.Object]
		if !ok {
			continue
		}
		found := false
		for _, f := range z.File {
			if f.Name != entry.Path {
				continue
			}
			in, err := f.Open()
			if err != nil {
				return err
			}
			sc := bufio.NewScanner(in)
			sc.Buffer(make([]byte, 4096), defaultTransferLimits().RecordBytes)
			if !sc.Scan() {
				in.Close()
				return fmt.Errorf("restore_columns_missing")
			}
			var head struct {
				Columns []backupColumn `json:"columns"`
			}
			err = strictTransferJSON(sc.Bytes(), &head)
			in.Close()
			if err != nil {
				return err
			}
			if len(head.Columns) != len(schema.Columns) {
				return fmt.Errorf("restore_schema_header_mismatch")
			}
			for i, c := range head.Columns {
				if c.Name != schema.Columns[i].Name {
					return fmt.Errorf("restore_schema_header_mismatch")
				}
			}
			found = true
		}
		if !found {
			return fmt.Errorf("restore_object_missing")
		}
	}
	return nil
}

func validateNativeRestoreMapping(z *zip.Reader, m restoreArchiveManifest, s *dbSession, mapping restoreMapping) error {
	for _, entry := range m.Entries {
		if entry.Database != mapping.Database || entry.Object != mapping.Object {
			continue
		}
		if s.kind == kindRedis {
			if entry.Encoding != "redis-dump-base64" {
				return fmt.Errorf("restore_encoding_mismatch")
			}
			_, err := restoreRedisIndex(mapping.TargetDatabase)
			return err
		}
		if entry.Encoding != "bson-base64" {
			return fmt.Errorf("restore_encoding_mismatch")
		}
		def, _, err := restoreMongoMetadata(z, entry, m.Metadata)
		if err != nil {
			return err
		}
		var info struct {
			Type    string `bson:"type"`
			Options bson.M `bson:"options"`
		}
		if err := bson.Unmarshal(def, &info); err != nil {
			return err
		}
		if info.Type != "collection" {
			return fmt.Errorf("restore_native_view_requires_advanced")
		}
		for key := range info.Options {
			switch key {
			case "validator", "validationLevel", "validationAction", "capped", "size", "max", "collation":
			default:
				return fmt.Errorf("restore_native_options_require_advanced")
			}
		}
		return nil
	}
	return fmt.Errorf("restore_source_object_missing")
}
func executeNativePreparedRestore(ctx context.Context, s *dbSession, p restorePrepared, reporter *tasks.Reporter) (tasks.Result, error) {
	z, m, err := readPreparedManifest(&p.Source)
	if err != nil {
		return tasks.Result{}, err
	}
	runner := &restoreRunner{ctx: ctx, s: s, reporter: reporter, mappings: map[string]restoreMapping{}}
	for _, mapping := range p.Mappings {
		if s.kind == kindMongo && p.AllowExisting {
			if err := validateMongoAppend(ctx, s, z, m, mapping); err != nil {
				return tasks.Result{}, err
			}
		}
		runner.mappings[mapping.Database+"\x00"+mapping.Object] = mapping
	}
	for _, entry := range m.Entries {
		if _, ok := runner.mappings[entry.Database+"\x00"+entry.Object]; !ok {
			continue
		}
		for _, f := range z.File {
			if f.Name != entry.Path {
				continue
			}
			if s.kind == kindMongo {
				err = runner.mongoObject(z, m, entry, f)
			} else {
				err = runner.redisObject(entry, f)
			}
			if err != nil {
				if ctx.Err() != nil {
					return tasks.Result{}, ctx.Err()
				}
				return tasks.Result{}, fmt.Errorf("restore_native_execution_failed")
			}
		}
	}
	return tasks.Result{}, nil
}

func restoreBoundValue(cell backupTypedValue) (any, error) {
	value, err := restoreSQLRowValue(cell)
	if err != nil {
		return nil, err
	}
	if cell.Type == "timestamp" {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("restore_invalid_timestamp")
		}
		value, err = time.Parse(time.RFC3339Nano, text)
	}
	return value, err
}
