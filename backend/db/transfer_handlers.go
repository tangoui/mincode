package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"mincode/app/backend/core"
	"mincode/app/backend/storage"
	"mincode/app/backend/tasks"
)

var transferMu sync.Mutex
var transferManager *tasks.Manager
var transferStore *tasks.Store
var transferStopping bool

func databaseTransferManager() (*tasks.Manager, *tasks.Store, error) {
	transferMu.Lock()
	defer transferMu.Unlock()
	if transferStopping {
		return nil, nil, tasks.ErrStopped
	}
	if transferManager != nil {
		return transferManager, transferStore, nil
	}
	db, err := storage.DB()
	if err != nil {
		return nil, nil, err
	}
	own, e := storage.Path()
	if e != nil {
		return nil, nil, e
	}
	// No old process can own a spool after startup recovery. The directory is
	// application-created, never archive-controlled.
	stagingRoot := filepath.Join(filepath.Dir(own), "transfer-staging")
	if e = cleanupTransferStaging(stagingRoot); e != nil {
		return nil, nil, e
	}
	transferStore = tasks.NewStore(db)
	transferManager, err = tasks.NewManager(context.Background(), transferStore, map[tasks.Kind]tasks.Executor{tasks.KindRestore: diagnosedTransfer(executeRestore), tasks.KindImport: diagnosedTransfer(executeTableImport), tasks.KindBackup: diagnosedTransfer(executeBackupTransfer)}, func(task tasks.Task) {
		core.Emit("db-transfer:task", task)
		if task.Kind == tasks.KindBackup {
			var p backupTransferPayload
			if json.Unmarshal(task.Request.Payload, &p) == nil {
				core.Emit(backupRunEvent, BackupRunEvent{JobID: p.Job.ID, JobName: p.Job.Name, ConnectionID: p.Job.ConnectionID, NotifyOnSuccess: p.Job.NotifyOnSuccess, Run: backupRunFromTask(task)})
			}
		}
	})
	if err == nil {
		transferManager.SetCleanup(cleanupTransferTask)
	}
	return transferManager, transferStore, err
}
func StopDatabaseTransfers(ctx context.Context) {
	transferMu.Lock()
	transferStopping = true
	m := transferManager
	transferMu.Unlock()
	if m != nil {
		_ = m.Shutdown(ctx)
	}
}
func restoreTargetFingerprint(ctx context.Context, s *dbSession, mappings []restoreMapping) (string, error) {
	values := []any{}
	for _, m := range mappings {
		switch s.kind {
		case kindSQL:
			info, err := getSchema(ctx, s)
			if err != nil {
				return "", fmt.Errorf("restore_metadata_unavailable")
			}
			for i := range info.Databases {
				for j := range info.Databases[i].Tables {
					table := &info.Databases[i].Tables[j]
					table.RowCount = nil
					table.SizeBytes = nil
					table.CreatedAt = nil
					table.UpdatedAt = nil
				}
			}
			values = append(values, info)
			for _, db := range info.Databases {
				if db.Name == m.TargetDatabase || (s.dialect == "sqlite" && m.TargetDatabase == "main") {
					for _, t := range db.Tables {
						if t.Name == m.TargetObject {
							if s.dialect == "sqlite" || s.dialect == "mysql" {
								schema, err := readRestoreSchema(ctx, s, m.TargetDatabase, m.TargetObject)
								if err == nil {
									values = append(values, schema)
								} else {
									ddl, e := getTableDDL(ctx, s, m.TargetDatabase, m.TargetObject)
									if e != nil {
										return "", fmt.Errorf("restore_metadata_unavailable")
									}
									values = append(values, ddl)
								}
							} else {
								ddl, e := getTableDDL(ctx, s, m.TargetDatabase, m.TargetObject)
								if e != nil {
									return "", fmt.Errorf("restore_metadata_unavailable")
								}
								values = append(values, ddl)
							}
						}
					}
				}
			}
		case kindMongo:
			names, err := s.mongo.Database(m.TargetDatabase).ListCollectionNames(ctx, map[string]any{})
			if err != nil {
				return "", fmt.Errorf("restore_metadata_unavailable")
			}
			sort.Strings(names)
			values = append(values, names)
			metadata, indexes, err := mongoTargetMetadata(ctx, s, m.TargetDatabase, m.TargetObject)
			if err != nil {
				return "", err
			}
			values = append(values, metadata, indexes)
		case kindRedis:
			index, err := restoreRedisIndex(m.TargetDatabase)
			if err != nil {
				return "", err
			}
			conn, err := redisConn(ctx, s, index)
			if err != nil {
				return "", err
			}
			n, err := conn.DBSize(ctx).Result()
			conn.Close()
			if err != nil {
				return "", fmt.Errorf("restore_metadata_unavailable")
			}
			values = append(values, n)
		default:
			return "", fmt.Errorf("restore_target_unsupported")
		}
	}
	data, _ := json.Marshal(values)
	return transferSchemaHash(string(data)), nil
}
func prepareRestore(ctx context.Context, req RestorePrepareRequest) (RestorePlan, error) {
	if req.Mode == "" {
		req.Mode = "constrained"
	}
	if req.Mode != "constrained" && req.Mode != "advanced" {
		return RestorePlan{}, fmt.Errorf("restore_invalid_mode")
	}
	if req.ConnectionID == "" || len(req.ConnectionID) > 128 || req.Path == "" || len(req.Path) > 4096 || len(req.Mappings) > 5000 {
		return RestorePlan{}, fmt.Errorf("restore_invalid_request")
	}
	_, store, err := databaseTransferManager()
	if err != nil {
		return RestorePlan{}, err
	}
	saved, err := loadSavedConnection(req.ConnectionID)
	if err != nil {
		return RestorePlan{}, err
	}
	if _, err = transferResourceKeys(saved); err != nil {
		return RestorePlan{}, err
	}
	own, err := storage.Path()
	if err != nil {
		return RestorePlan{}, err
	}
	source, err := stageRestoreSource(ctx, req.Path, filepath.Join(filepath.Dir(own), "transfer-staging"), defaultTransferLimits())
	if err != nil {
		return RestorePlan{}, err
	}
	keep := false
	defer func() {
		if !keep {
			source.Close()
		} else {
			_ = source.file.Close()
		}
	}()
	s, err := openBackupSession(ctx, saved)
	if err != nil {
		return RestorePlan{}, fmt.Errorf("restore_target_unavailable")
	}
	defer s.close()
	var plan RestorePlan
	if req.Mode == "constrained" && source.Format == restoreArchive {
		_, manifest, e := readPreparedManifest(source)
		if e != nil {
			return plan, e
		}
		plan = RestorePlan{Format: restoreArchive, Engine: manifest.Engine, Supported: true, SizeBytes: source.Size, Objects: []RestorePlanItem{}, Conflicts: []string{}, Warnings: []string{}}
		for _, entry := range manifest.Entries {
			plan.Objects = append(plan.Objects, RestorePlanItem{Database: entry.Database, Object: entry.Object, Records: entry.Records})
		}
		if s.kind == kindSQL && manifest.Engine != s.dialect {
			return plan, fmt.Errorf("restore_dialect_mismatch")
		}
	} else {
		plan, err = planRestore(ctx, s, source.Path)
		if err != nil {
			return plan, err
		}
	}
	plan.Mode = req.Mode
	if source.Format == restoreSQL {
		dialect, err := reviewScriptDialect(ctx, source)
		if err != nil {
			return plan, err
		}
		if dialect == "" {
			dialect = req.SourceDialect
		}
		if dialect == "" {
			plan.Supported = false
			plan.Reason = "restore_source_dialect_required"
			return plan, nil
		}
		if dialect != s.dialect {
			return plan, fmt.Errorf("restore_dialect_mismatch")
		}
	}
	if !plan.Supported {
		return plan, nil
	}
	mappings := req.Mappings
	if req.Mode == "advanced" {
		mappings = []restoreMapping{}
		for _, item := range plan.Objects {
			mappings = append(mappings, restoreMapping{item.Database, item.Object, item.Database, item.Object})
		}
		if len(mappings) == 0 {
			mappings = []restoreMapping{{TargetDatabase: s.defaultDatabase}}
		}
		plan.Warnings = append(plan.Warnings, "restore_unrestricted_execution")
	}
	if req.Mode == "constrained" {
		if source.Format != restoreArchive || !((s.kind == kindSQL && (s.dialect == "mysql" || s.dialect == "sqlite")) || s.kind == kindMongo || s.kind == kindRedis) {
			plan.Supported = false
			plan.Reason = "restore_constrained_requires_structured_archive"
			return plan, nil
		}
		if len(mappings) == 0 {
			plan.Supported = false
			plan.Reason = "restore_target_mapping_required"
			return plan, nil
		}
		z, manifest, err := readPreparedManifest(source)
		if err != nil {
			return plan, err
		}
		schemas, err := archiveSchemas(z, manifest)
		if err != nil {
			return plan, err
		}
		if err := validatePreparedSchemaColumns(z, manifest, schemas); err != nil {
			return plan, err
		}
		seen := map[string]bool{}
		plan.Conflicts = []string{}
		for _, mapping := range mappings {
			if !restoreIdentifier(mapping.TargetDatabase) || !restoreIdentifier(mapping.TargetObject) {
				return plan, fmt.Errorf("restore_invalid_mapping")
			}
			key := mapping.TargetDatabase + "\x00" + mapping.TargetObject
			if seen[key] {
				return plan, fmt.Errorf("restore_duplicate_target")
			}
			seen[key] = true
			found := false
			for _, entry := range manifest.Entries {
				if entry.Database == mapping.Database && entry.Object == mapping.Object {
					found = true
				}
			}
			if !found {
				return plan, fmt.Errorf("restore_source_object_missing")
			}

			if s.kind == kindMongo || s.kind == kindRedis {
				if err := validateNativeRestoreMapping(z, manifest, s, mapping); err != nil {
					return plan, err
				}
				if s.kind == kindMongo && req.AllowExisting {
					if err := validateMongoAppend(ctx, s, z, manifest, mapping); err != nil {
						return plan, err
					}
				}
				continue
			}
			schema, ok := schemas[mapping.Database+"\x00"+mapping.Object]
			if !ok {
				return plan, fmt.Errorf("restore_schema_not_certified")
			}
			if _, err = schema.Statements(s.dialect, mapping.TargetDatabase, mapping.TargetObject); err != nil {
				return plan, err
			}
			conflicts, err := restoreSQLConflicts(ctx, s, []RestorePlanItem{{Database: mapping.TargetDatabase, Object: mapping.TargetObject}})
			if err != nil {
				return plan, err
			}
			plan.Conflicts = append(plan.Conflicts, conflicts...)
			if len(conflicts) > 0 {
				target, err := readRestoreSchema(ctx, s, mapping.TargetDatabase, mapping.TargetObject)
				if err != nil {
					return plan, err
				}
				if !restoreSchemasCompatible(schema, target) {
					return plan, fmt.Errorf("restore_append_schema_mismatch")
				}
			}
		}
	}
	if req.Mode == "constrained" {
		if err := validateRestoreDestination(ctx, s, mappings, req.AllowExisting); err != nil {
			return plan, err
		}
	}
	hash, err := restoreTargetFingerprint(ctx, s, mappings)
	if err != nil {
		return plan, err
	}
	plan.PlanID = uuid.NewString()
	plan.ExpiresAt = time.Now().Add(defaultTransferLimits().Expiry).UnixMilli()
	fileIdentity, err := captureTransferTargetFile(saved)
	if err != nil {
		return plan, err
	}
	prepared := restorePrepared{Source: *source, ConnectionID: saved.ID, ConfigHash: connectionFingerprint(saved), TargetHash: hash, Mode: req.Mode, Mappings: mappings, AllowExisting: req.AllowExisting, Plan: plan, WorkspaceID: req.WorkspaceID}
	payload, err := json.Marshal(prepared)
	if err != nil {
		return plan, err
	}
	fingerprint := transferSchemaHash(string(payload))
	if err = store.Prepare(ctx, tasks.Preparation{ID: plan.PlanID, Kind: tasks.KindRestore, Fingerprint: fingerprint, ExpiresAt: plan.ExpiresAt, Payload: payload}); err != nil {
		return plan, err
	}
	if fileIdentity != nil {
		targetFilePins.Lock()
		targetFilePins.files[plan.PlanID] = fileIdentity
		targetFilePins.Unlock()
	}
	keep = true
	time.AfterFunc(defaultTransferLimits().Expiry, func() {
		released, err := store.ReleasePreparation(context.Background(), plan.PlanID)
		if err == nil && released {
			source.Close()
			releaseTargetFilePin(plan.PlanID)
		}
	})
	return plan, nil
}
func submitRestore(ctx context.Context, req RestoreSubmitRequest) (tasks.Task, error) {
	manager, store, err := databaseTransferManager()
	if err != nil {
		return tasks.Task{}, err
	}
	if task, e := store.Submission(ctx, tasks.KindRestore, req.IdempotencyKey, req.PlanID); e == nil {
		return task, nil
	} else if !errors.Is(e, tasks.ErrNotFound) {
		return tasks.Task{}, e
	}
	p, err := store.Preparation(ctx, req.PlanID)
	if err != nil {
		return tasks.Task{}, err
	}
	var prepared restorePrepared
	if err = json.Unmarshal(p.Payload, &prepared); err != nil {
		return tasks.Task{}, err
	}
	if prepared.Mode == "advanced" && !req.UnrestrictedRiskAccepted {
		return tasks.Task{}, fmt.Errorf("restore_unrestricted_consent_required")
	}
	if prepared.AllowExisting != req.AllowExisting {
		return tasks.Task{}, fmt.Errorf("restore_review_changed")
	}
	if len(prepared.Plan.Conflicts) > 0 && !req.AllowExisting {
		return tasks.Task{}, fmt.Errorf("restore_target_not_empty")
	}
	saved, err := loadSavedConnection(prepared.ConnectionID)
	if err != nil {
		return tasks.Task{}, err
	}
	if connectionFingerprint(saved) != prepared.ConfigHash {
		return tasks.Task{}, fmt.Errorf("restore_connection_changed")
	}
	keys, err := transferResourceKeys(saved)
	if err != nil {
		return tasks.Task{}, err
	}
	return manager.Submit(ctx, tasks.Request{Kind: tasks.KindRestore, IdempotencyKey: req.IdempotencyKey, PreparationID: p.ID, Fingerprint: p.Fingerprint, ConnectionID: saved.ID, WorkspaceID: prepared.WorkspaceID, Resources: keys})
}
func init() {
	core.RegisterCtx("db_transfer_task_get", func(ctx context.Context, _, raw string) (string, error) {
		var a struct {
			TaskID string `json:"taskId"`
		}
		if err := strictTransferJSON([]byte(raw), &a); err != nil {
			return "", err
		}
		m, _, err := databaseTransferManager()
		if err != nil {
			return "", err
		}
		task, err := m.Get(ctx, a.TaskID)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(task)
	})
	core.RegisterCtx("db_transfer_task_list", func(ctx context.Context, _, raw string) (string, error) {
		var a struct {
			Kind        tasks.Kind `json:"kind"`
			JobID       string     `json:"jobId"`
			WorkspaceID string     `json:"workspaceId"`
			ActiveOnly  bool       `json:"activeOnly"`
			Limit       int        `json:"limit"`
			Cursor      string     `json:"cursor"`
		}
		if err := strictTransferJSON([]byte(raw), &a); err != nil {
			return "", err
		}
		m, _, err := databaseTransferManager()
		if err != nil {
			return "", err
		}
		page, err := m.List(ctx, tasks.Filter{Kind: a.Kind, JobID: a.JobID, WorkspaceID: a.WorkspaceID, ActiveOnly: a.ActiveOnly, Limit: a.Limit, Cursor: a.Cursor})
		if err != nil {
			return "", err
		}
		return core.EncodeResult(page)
	})
	core.RegisterCtx("db_transfer_task_cancel", func(ctx context.Context, _, raw string) (string, error) {
		var a struct {
			TaskID string `json:"taskId"`
		}
		if err := strictTransferJSON([]byte(raw), &a); err != nil {
			return "", err
		}
		m, _, err := databaseTransferManager()
		if err != nil {
			return "", err
		}
		task, err := m.Cancel(ctx, a.TaskID)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(task)
	})
}

func validateRestoreDestination(ctx context.Context, s *dbSession, mappings []restoreMapping, appendMode bool) error {
	seen := map[string]bool{}
	for _, m := range mappings {
		if seen[m.TargetDatabase] {
			continue
		}
		seen[m.TargetDatabase] = true
		switch s.kind {
		case kindSQL:
			if s.dialect == "sqlite" && m.TargetDatabase != "main" {
				return fmt.Errorf("restore_target_namespace_unsupported")
			}
			tables, err := getDatabaseTables(ctx, s, m.TargetDatabase)
			if err != nil {
				return fmt.Errorf("restore_metadata_unavailable")
			}
			if !appendMode && len(tables) > 0 {
				return fmt.Errorf("restore_target_not_empty")
			}
			if s.dialect == "mysql" {
				var n int
				if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME=?", m.TargetDatabase).Scan(&n); err != nil || n != 1 {
					return fmt.Errorf("restore_target_namespace_missing")
				}
			}
		case kindMongo:
			names, err := s.mongo.Database(m.TargetDatabase).ListCollectionNames(ctx, map[string]any{})
			if err != nil {
				return fmt.Errorf("restore_metadata_unavailable")
			}
			if !appendMode && len(names) > 0 {
				return fmt.Errorf("restore_target_not_empty")
			}
		case kindRedis:
			index, err := restoreRedisIndex(m.TargetDatabase)
			if err != nil {
				return err
			}
			conn, err := redisConn(ctx, s, index)
			if err != nil {
				return err
			}
			n, err := conn.DBSize(ctx).Result()
			conn.Close()
			if err != nil {
				return fmt.Errorf("restore_metadata_unavailable")
			}
			if !appendMode && n > 0 {
				return fmt.Errorf("restore_target_not_empty")
			}
		}
	}
	return nil
}

func cleanupTransferStaging(root string) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("transfer_staging_invalid")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "restore-") || (strings.HasPrefix(entry.Name(), "import-") && strings.HasSuffix(entry.Name(), ".jsonl")) {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

var restoreDialectHeader = regexp.MustCompile(`(?m)^-- Type: [^
]*\(dialect ([a-z0-9]+)\)`)

func reviewScriptDialect(ctx context.Context, p *preparedRestoreSource) (string, error) {
	r, _, closeFile, err := openSQLScript(p.Path)
	if err != nil {
		return "", err
	}
	defer closeFile()
	buf := make([]byte, 64<<10)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	match := restoreDialectHeader.FindSubmatch(buf[:n])
	if len(match) == 2 {
		return string(match[1]), nil
	}
	return "", nil
}

func cleanupTransferTask(task tasks.Task) {
	switch task.Kind {
	case tasks.KindRestore:
		var p restorePrepared
		if json.Unmarshal(task.Request.Payload, &p) == nil {
			p.Source.Close()
		}
		releaseTargetFilePin(task.Request.PreparationID)
	case tasks.KindImport:
		var p preparedImport
		if json.Unmarshal(task.Request.Payload, &p) == nil {
			_ = os.Remove(p.Path)
		}
		importPreparations.Lock()
		delete(importPreparations.ids, task.Request.PreparationID)
		importPreparations.Unlock()
	}
}

func diagnosedTransfer(executor tasks.Executor) tasks.Executor {
	return func(ctx context.Context, task tasks.Task, r *tasks.Reporter) (tasks.Result, error) {
		result, err := executor(ctx, task, r)
		if err != nil {
			switch err.Error() {
			case "restore_target_changed", "restore_connection_changed", "restore_target_file_changed", "restore_prepared_changed", "restore_metadata_unavailable", "restore_schema_not_certified", "restore_append_schema_mismatch", "restore_append_index_mismatch", "restore_append_ttl_index_forbidden", "restore_trigger_visibility_unavailable", "restore_target_not_empty", "restore_transaction_uncommitted", "restore_native_execution_failed", "restore_advanced_execution_failed", "transfer_application_storage_forbidden", "import_prepared_changed", "import_target_changed", "import_metadata_unavailable", "import_session_expired", "import_insert_failed", "import_commit_uncertain", "backup_execution_failed":
				err = tasks.Diagnostic(err.Error())
			}
		}
		return result, err
	}
}
