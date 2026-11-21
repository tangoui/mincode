package db

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mincode/app/backend/tasks"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// RestoreOptions are the user's explicit choices from the confirmation.
type RestoreOptions struct {
	// AllowExisting writes into objects that already exist (no DROP; rows
	// are added). Without it, any existing target object stops the restore.
	AllowExisting bool `json:"allowExisting"`
	// AllowDestructive lets a SQL script run its own DROP / TRUNCATE /
	// DELETE statements. MinCode SQL dumps contain DROP TABLE IF EXISTS.
	AllowDestructive bool `json:"allowDestructive"`
}

const restoreBatchRows = 500

var (
	sqlDestructivePattern = regexp.MustCompile(`(?is)^\s*(?:/\*.*?\*/\s*|--[^\n]*\n\s*|#[^\n]*\n\s*)*(DROP|TRUNCATE|DELETE)\b`)
	sqlCopyStdinPattern   = regexp.MustCompile(`(?is)^\s*COPY\b.*\bFROM\s+STDIN\b`)
	sqlUsePattern         = regexp.MustCompile("(?is)^\\s*USE\\s+[`\"\\[]?([^`\"\\];\\s]+)")
	mincodeMarkerPattern  = regexp.MustCompile(`(?m)^--\s*(Database|Table):\s*(.+?)\s*$`)
)

// planRestore inspects a backup file against the target session without
// writing anything.
func planRestore(ctx context.Context, s *dbSession, path string) (RestorePlan, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return RestorePlan{}, fmt.Errorf("备份文件不可用")
	}
	format, err := detectRestoreFormat(path)
	if err != nil {
		return RestorePlan{}, err
	}
	plan := RestorePlan{Format: format, SizeBytes: info.Size(), Objects: []RestorePlanItem{}, Conflicts: []string{}, Warnings: []string{}}
	if format == restoreSQL {
		return planSQLScript(ctx, s, path, plan)
	}
	return planArchive(ctx, s, path, plan)
}

func planSQLScript(ctx context.Context, s *dbSession, path string, plan RestorePlan) (RestorePlan, error) {
	if s.kind != kindSQL || s.db == nil {
		plan.Reason = "SQL 脚本只能恢复到 SQL 数据库连接"
		return plan, nil
	}
	r, _, closeFile, err := openSQLScript(path)
	if err != nil {
		return plan, err
	}
	defer closeFile()
	reader := newSQLStatementReader(r)
	current := ""
	seen := map[string]bool{}
	destructive := 0
	for {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		stmt, err := reader.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return plan, err
		}
		if sqlCopyStdinPattern.MatchString(stmt) {
			plan.Reason = "脚本包含 pg_dump 的 COPY ... FROM stdin 数据块，请使用 psql 恢复"
			return plan, nil
		}
		if sqlDestructivePattern.MatchString(stripLeadingSQLComments(stmt)) {
			destructive++
		}
		for _, m := range mincodeMarkerPattern.FindAllStringSubmatch(stmt, -1) {
			if m[1] == "Database" {
				current = m[2]
				continue
			}
			key := current + "\x00" + m[2]
			if !seen[key] {
				seen[key] = true
				plan.Objects = append(plan.Objects, RestorePlanItem{Database: current, Object: m[2]})
			}
		}
		if m := sqlUsePattern.FindStringSubmatch(stmt); m != nil && current == "" {
			current = m[1]
		}
		plan.Statements++
	}
	if destructive > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("脚本包含 %d 条 DROP / TRUNCATE / DELETE 语句，执行时会删除目标库中的同名对象或数据", destructive))
	}
	plan.Destructive = destructive > 0
	plan.Conflicts, err = restoreSQLConflicts(ctx, s, plan.Objects)
	if err != nil {
		return plan, err
	}
	plan.Supported = true
	return plan, nil
}

func stripLeadingSQLComments(stmt string) string {
	lines := strings.Split(stmt, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") || strings.HasPrefix(t, "#") {
			continue
		}
		return strings.Join(lines[i:], "\n")
	}
	return ""
}

// restoreSQLConflicts lists target tables that already exist.
func restoreSQLConflicts(ctx context.Context, s *dbSession, items []RestorePlanItem) ([]string, error) {
	existing := map[string]map[string]bool{}
	conflicts := []string{}
	for _, item := range items {
		if item.Object == "" {
			continue
		}
		tables, ok := existing[item.Database]
		if !ok {
			tables = map[string]bool{}
			if list, err := getDatabaseTables(ctx, s, item.Database); err == nil {
				for _, t := range list {
					tables[t.Name] = true
				}
			} else if info, err := getSchema(ctx, s); err == nil {
				for _, db := range info.Databases {
					if db.Name == item.Database || item.Database == "" {
						for _, t := range db.Tables {
							tables[t.Name] = true
						}
					}
				}
			} else {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, fmt.Errorf("restore_metadata_unavailable")
			}
			existing[item.Database] = tables
		}
		if tables[item.Object] {
			conflicts = append(conflicts, restoreObjectName(item.Database, item.Object))
		}
	}
	return conflicts, nil
}

func restoreObjectName(database, object string) string {
	if database == "" {
		return object
	}
	return database + "." + object
}

func planArchive(ctx context.Context, s *dbSession, path string, plan RestorePlan) (RestorePlan, error) {
	z, m, err := openRestoreArchive(path)
	if err != nil {
		return plan, err
	}
	defer z.Close()
	plan.Engine = m.Engine
	for _, e := range m.Entries {
		plan.Objects = append(plan.Objects, RestorePlanItem{Database: e.Database, Object: e.Object, Records: e.Records})
	}
	kind := restoreArchiveKind(m)
	switch {
	case kind == "typed-sql-row" && s.kind == kindSQL && restorePlaceholder(s.dialect, 1) != "":
		if m.Engine != s.dialect && !(m.Engine == "postgresql" && s.dialect == "postgres") {
			return plan, fmt.Errorf("restore_dialect_mismatch")
		}
		plan.Conflicts, err = restoreSQLConflicts(ctx, s, plan.Objects)
		if err != nil {
			return plan, err
		}
	case kind == "bson-base64" && s.kind == kindMongo:
		for _, item := range plan.Objects {
			names, err := s.mongo.Database(item.Database).ListCollectionNames(ctx, bson.M{"name": item.Object})
			if err != nil {
				return plan, fmt.Errorf("restore_metadata_unavailable")
			}
			if len(names) > 0 {
				plan.Conflicts = append(plan.Conflicts, restoreObjectName(item.Database, item.Object))
			}
		}
	case kind == "redis-dump-base64" && s.kind == kindRedis:
		for _, item := range plan.Objects {
			index, err := restoreRedisIndex(item.Database)
			if err != nil {
				return plan, err
			}
			conn, err := redisConn(ctx, s, index)
			if err != nil {
				return plan, err
			}
			n, err := conn.DBSize(ctx).Result()
			_ = conn.Close()
			if err != nil {
				return plan, fmt.Errorf("restore_metadata_unavailable")
			}
			if n > 0 {
				plan.Conflicts = append(plan.Conflicts, item.Database)
			}
		}
	default:
		plan.Reason = restoreUnsupportedReason(kind, s)
		return plan, nil
	}
	plan.Supported = true
	return plan, nil
}

// restoreArchiveKind is the encoding all objects share ("" if mixed/empty).
func restoreArchiveKind(m restoreArchiveManifest) string {
	kind := ""
	for _, e := range m.Entries {
		if kind != "" && kind != e.Encoding {
			return ""
		}
		kind = e.Encoding
	}
	return kind
}

func restoreUnsupportedReason(kind string, s *dbSession) string {
	switch kind {
	case "typed-sql-row":
		if s.kind != kindSQL {
			return "该归档来自 SQL 数据库，只能恢复到 SQL 数据库连接"
		}
		return "当前连接的 SQL 方言暂不支持从归档恢复"
	case "bson-base64":
		return "该归档来自 MongoDB，只能恢复到 MongoDB 连接"
	case "redis-dump-base64":
		return "该归档来自 Redis，只能恢复到 Redis 连接"
	case "":
		return "归档为空或包含多种对象格式，无法自动恢复"
	}
	return "该类型的归档暂不支持自动恢复，请按归档内 RECOVERY.txt 手动恢复"
}

func restoreRedisIndex(name string) (int64, error) {
	index, err := strconv.ParseInt(strings.TrimPrefix(name, "db"), 10, 64)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("无效的 Redis 逻辑库 %q", name)
	}
	return index, nil
}

// restorePlaceholder returns the n-th bind placeholder of a dialect, or ""
// when archive restore is not supported for it.
func restorePlaceholder(dialect string, n int) string {
	switch dialect {
	case "mysql", "sqlite", "duckdb", "firebird", "clickhouse":
		return "?"
	case "postgres":
		return "$" + strconv.Itoa(n)
	case "sqlserver":
		return "@p" + strconv.Itoa(n)
	case "oracle", "yashandb", "dm":
		return ":" + strconv.Itoa(n)
	}
	return ""
}

// restoreProgressFunc receives throttled progress.
type restoreProgressFunc func(RestoreProgress)

type restoreRunner struct {
	mappings   map[string]restoreMapping
	pinned     *os.File
	pinnedSize int64
	reporter   *tasks.Reporter
	ctx        context.Context
	s          *dbSession
	opts       RestoreOptions
	progress   restoreProgressFunc
	state      RestoreProgress
	last       time.Time
	result     RestoreResult
}

func (r *restoreRunner) tick(force bool) {
	if r.progress == nil {
		return
	}
	if force || time.Since(r.last) > 250*time.Millisecond {
		r.last = time.Now()
		r.progress(r.state)
	}
}

// runRestore re-plans (the target may have changed since the preview),
// enforces the user's choices and writes the backup into the target.
func runRestore(ctx context.Context, s *dbSession, path string, opts RestoreOptions, progress restoreProgressFunc) (RestoreResult, error) {
	return runRestoreReported(ctx, s, path, opts, progress, nil)
}
func runRestoreReported(ctx context.Context, s *dbSession, path string, opts RestoreOptions, progress restoreProgressFunc, reporter *tasks.Reporter) (RestoreResult, error) {
	start := time.Now()
	plan, err := planRestore(ctx, s, path)
	if err != nil {
		return RestoreResult{}, err
	}
	if !plan.Supported {
		return RestoreResult{}, errors.New(plan.Reason)
	}
	if len(plan.Conflicts) > 0 && !opts.AllowExisting {
		return RestoreResult{}, fmt.Errorf("目标中已存在 %d 个同名对象（%s），未允许写入已有对象", len(plan.Conflicts), strings.Join(firstN(plan.Conflicts, 5), "、"))
	}
	if plan.Destructive && !opts.AllowDestructive {
		return RestoreResult{}, fmt.Errorf("脚本包含删除语句，未允许执行")
	}
	r := &restoreRunner{reporter: reporter, ctx: ctx, s: s, opts: opts, progress: progress, result: RestoreResult{Warnings: []string{}}}
	r.state.TotalBytes = plan.SizeBytes
	if plan.Format == restoreSQL {
		err = r.sqlScript(path)
	} else {
		err = r.archive(path)
	}
	r.result.DurationMs = time.Since(start).Milliseconds()
	r.result.Statements = r.state.Statements
	r.result.Records = r.state.Records
	r.tick(true)
	if err != nil && ctx.Err() != nil {
		return r.result, errRestoreCancelled
	}
	return r.result, err
}

func firstN(items []string, n int) []string {
	if len(items) > n {
		return append(items[:n:n], "…")
	}
	return items
}

func (r *restoreRunner) sqlScript(path string) error {
	var in io.Reader
	var counter *countingReader
	var closeFile func()
	var err error
	if r.pinned != nil {
		_, err = r.pinned.Seek(0, 0)
		if err != nil {
			return err
		}
		counter = &countingReader{r: r.pinned}
		br := bufio.NewReader(counter)
		head, _ := br.Peek(2)
		in = br
		closeFile = func() {}
		if len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
			gz, e := gzip.NewReader(br)
			if e != nil {
				return e
			}
			in = gz
			closeFile = func() { _ = gz.Close() }
		}
	} else {
		in, counter, closeFile, err = openSQLScript(path)
		if err != nil {
			return err
		}
	}
	defer closeFile()
	conn, err := r.s.db.Conn(r.ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	reader := newSQLStatementReader(in)
	transactionEffect := ""
	defer func() {
		if transactionEffect != "" {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		stmt, err := reader.next()
		if err == io.EOF {
			if transactionEffect != "" {
				return fmt.Errorf("restore_transaction_uncommitted")
			}
			return nil
		}
		if err != nil {
			return err
		}
		command := scriptTransactionCommand(stmt)
		if command == "begin" {
			if transactionEffect != "" {
				return fmt.Errorf("restore_nested_transaction_unsupported")
			}
			transactionEffect, err = r.beginEffect("sql-transaction")
			if err != nil {
				return err
			}
			// Empty IDs are permitted only by the legacy unjournaled test adapter.
			if transactionEffect == "" {
				transactionEffect = "legacy"
			}
			if _, err := conn.ExecContext(r.ctx, stmt); err != nil {
				return fmt.Errorf("restore_transaction_begin_failed")
			}
		} else if transactionEffect != "" {
			if _, err := conn.ExecContext(r.ctx, stmt); err != nil {
				return fmt.Errorf("restore_statement_failed")
			}
			if command == "commit" || command == "rollback" {
				outcome := tasks.EffectOutcome{Objects: 1}
				if command == "rollback" {
					outcome = tasks.EffectOutcome{NoEffect: true}
				}
				if err := r.finishEffect(transactionEffect, outcome); err != nil {
					return err
				}
				transactionEffect = ""
			}
		} else {
			if command == "commit" || command == "rollback" {
				return fmt.Errorf("restore_transaction_boundary_invalid")
			}
			effect, err := r.beginEffect("sql-statement")
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(r.ctx, stmt); err != nil {
				return fmt.Errorf("restore_statement_failed: %d", r.state.Statements+1)
			}
			if err := r.finishEffect(effect, tasks.EffectOutcome{Objects: 1}); err != nil {
				return err
			}
		}
		r.state.Statements++
		r.state.BytesRead = counter.n
		r.tick(false)
	}

}

// restoreSnippet shows the start of a failing statement without dumping
// the whole (possibly huge) data block into the error.
func restoreSnippet(stmt string) string {
	stmt = strings.Join(strings.Fields(stripLeadingSQLComments(stmt)), " ")
	if len([]rune(stmt)) > 120 {
		return string([]rune(stmt)[:120]) + "…"
	}
	return stmt
}

func (r *restoreRunner) archive(path string) error {
	var z *zip.Reader
	var m restoreArchiveManifest
	if r.pinned != nil {
		var err error
		z, err = zip.NewReader(r.pinned, r.pinnedSize)
		if err != nil {
			return err
		}
		for _, f := range z.File {
			if f.Name == "manifest.json" {
				if err := readZipJSON(f, &m); err != nil {
					return err
				}
			}
		}
	} else {
		opened, manifest, err := openRestoreArchive(path)
		if err != nil {
			return err
		}
		defer opened.Close()
		z = &opened.Reader
		m = manifest
	}

	files := map[string]*zip.File{}
	for _, f := range z.File {
		files[f.Name] = f
	}
	for _, entry := range m.Entries {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		r.state.Object = restoreObjectName(entry.Database, entry.Object)
		r.tick(true)
		f := files[entry.Path]
		var err error
		switch entry.Encoding {
		case "typed-sql-row":
			err = r.sqlObject(z, m, entry, f)
		case "bson-base64":
			err = r.mongoObject(z, m, entry, f)
		case "redis-dump-base64":
			err = r.redisObject(entry, f)
		default:
			err = fmt.Errorf("不支持的对象格式 %s", entry.Encoding)
		}
		if err != nil {
			return fmt.Errorf("恢复 %s: %w", r.state.Object, err)
		}
		r.result.Objects++
	}
	return nil
}

// eachLine streams the JSON lines of one archive object.
func eachLine(f *zip.File, fn func([]byte) error) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 1<<20), 512<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (r *restoreRunner) sqlObject(z *zip.Reader, m restoreArchiveManifest, entry backupArchiveEntry, f *zip.File) error {
	dialect := r.s.dialect
	if dialect == "yashandb" {
		dialect = "oracle"
	}
	conflicts, err := restoreSQLConflicts(r.ctx, r.s, []RestorePlanItem{{Database: entry.Database, Object: entry.Object}})
	if err != nil {
		return err
	}
	exists := len(conflicts) > 0
	ddl, err := restoreDDL(z, entry, m.Metadata)
	if err != nil {
		return err
	}
	for _, stmt := range restoreDatabasePrelude(dialect, entry.Database) {
		effect, err := r.beginEffect("archive-database-prelude")
		if err != nil {
			return err
		}
		if _, err := r.s.db.ExecContext(r.ctx, stmt); err != nil {
			return fmt.Errorf("restore_database_prepare_failed")
		}
		if err := r.finishEffect(effect, tasks.EffectOutcome{}); err != nil {
			return err
		}
	}
	if !exists {
		effect, err := r.beginEffect("archive-create-table")
		if err != nil {
			return err
		}
		if _, err := r.s.db.ExecContext(r.ctx, ddl[0]); err != nil {
			return fmt.Errorf("restore_create_failed")
		}
		if err := r.finishEffect(effect, tasks.EffectOutcome{Objects: 1}); err != nil {
			return err
		}
	}
	qualified, err := qualifiedTable(dialect, entry.Database, entry.Object)
	if err != nil {
		return err
	}
	src := &sessionBackupSource{s: r.s, dialect: dialect}
	insertable := map[string]bool{}
	for _, c := range src.insertColumns(r.ctx, entry.Database, entry.Object) {
		insertable[c] = true
	}
	var columns []backupColumn
	var keep []int
	var insert string
	var tx *sql.Tx
	var stmt *sql.Stmt
	inBatch := 0
	batchEffect := ""
	commit := func() error {
		if tx == nil {
			return nil
		}
		_ = stmt.Close()
		err := tx.Commit()
		count := inBatch
		tx, stmt, inBatch = nil, nil, 0
		if err != nil {
			return err
		}
		r.state.Records += int64(count)
		return r.finishEffect(batchEffect, tasks.EffectOutcome{Records: int64(count)})
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	err = eachLine(f, func(line []byte) error {
		if columns == nil {
			var head struct {
				Columns []backupColumn `json:"columns"`
			}
			if err := json.Unmarshal(line, &head); err != nil || head.Columns == nil {
				return fmt.Errorf("缺少列定义")
			}
			columns = head.Columns
			names := []string{}
			marks := []string{}
			for i, c := range columns {
				// Generated columns are recomputed by the server.
				if len(insertable) > 0 && !insertable[c.Name] {
					continue
				}
				keep = append(keep, i)
				names = append(names, schemaQuoteIdentifier(dialect, c.Name))
				marks = append(marks, restorePlaceholder(dialect, len(marks)+1))
			}
			insert = "INSERT INTO " + qualified + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
			return nil
		}
		var cells []backupTypedValue
		if err := json.Unmarshal(line, &cells); err != nil {
			return fmt.Errorf("第 %d 行数据损坏", r.state.Records+1)
		}
		if len(cells) != len(columns) {
			return fmt.Errorf("第 %d 行列数不一致", r.state.Records+1)
		}
		args := make([]any, len(keep))
		for i, idx := range keep {
			v, err := restoreSQLRowValue(cells[idx])
			if err != nil {
				return fmt.Errorf("列 %s: %w", columns[idx].Name, err)
			}
			if cells[idx].Type == "timestamp" {
				if t, err := time.Parse(time.RFC3339Nano, v.(string)); err == nil {
					v = t
				}
			}
			args[i] = v
		}
		if tx == nil {
			var err error
			batchEffect, err = r.beginEffect("archive-insert-batch")
			if err != nil {
				return err
			}
			if tx, err = r.s.db.BeginTx(r.ctx, nil); err != nil {
				return err
			}
			if stmt, err = tx.PrepareContext(r.ctx, insert); err != nil {
				return err
			}
		}
		if _, err := stmt.ExecContext(r.ctx, args...); err != nil {
			return fmt.Errorf("写入第 %d 行: %w", r.state.Records+1, err)
		}
		inBatch++
		if inBatch >= restoreBatchRows {
			if err := commit(); err != nil {
				return err
			}
			r.tick(false)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := commit(); err != nil {
		return err
	}
	if !exists {
		for _, stmt := range ddl[1:] {
			effect, err := r.beginEffect("archive-index")
			if err != nil {
				return err
			}
			if _, err := r.s.db.ExecContext(r.ctx, stmt); err != nil {
				return fmt.Errorf("restore_index_failed")
			}
			if err := r.finishEffect(effect, tasks.EffectOutcome{}); err != nil {
				return err
			}
		}
	}
	r.state.Statements++
	return nil
}

func restoreDatabasePrelude(dialect, database string) []string {
	if strings.TrimSpace(database) == "" {
		return nil
	}
	switch dialect {
	case "mysql":
		q := schemaQuoteIdentifier("mysql", database)
		return []string{"CREATE DATABASE IF NOT EXISTS " + q, "USE " + q}
	case "postgres":
		return []string{"CREATE SCHEMA IF NOT EXISTS " + schemaQuoteIdentifier("postgres", backupPostgresSchema(database))}
	}
	return nil
}

func (r *restoreRunner) mongoObject(z *zip.Reader, m restoreArchiveManifest, entry backupArchiveEntry, f *zip.File) error {
	targetDatabase, targetObject := entry.Database, entry.Object
	if mapping, ok := r.mappings[entry.Database+"\x00"+entry.Object]; ok {
		targetDatabase, targetObject = mapping.TargetDatabase, mapping.TargetObject
	}
	db := r.s.mongo.Database(targetDatabase)
	def, indexes, err := restoreMongoMetadata(z, entry, m.Metadata)
	if err != nil {
		return err
	}
	var info struct {
		Type    string `bson:"type"`
		Options bson.D `bson:"options"`
	}
	_ = bson.Unmarshal(def, &info)
	names, err := db.ListCollectionNames(r.ctx, bson.D{{Key: "name", Value: targetObject}})
	if err != nil {
		return err
	}
	if len(names) == 0 {
		cmd := append(bson.D{{Key: "create", Value: targetObject}}, info.Options...)
		effect, err := r.beginEffect("mongo-create")
		if err != nil {
			return err
		}
		if err := db.RunCommand(r.ctx, cmd).Err(); err != nil {
			return fmt.Errorf("restore_collection_create_failed")
		}
		if err := r.finishEffect(effect, tasks.EffectOutcome{Objects: 1}); err != nil {
			return err
		}
	}
	if info.Type == "view" {
		return nil
	}
	coll := db.Collection(targetObject)
	batch := make([]any, 0, restoreBatchRows)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		effect, err := r.beginEffect("mongo-insert-batch")
		if err != nil {
			return err
		}
		result, err := coll.InsertMany(r.ctx, batch, options.InsertMany().SetOrdered(true))
		if err != nil {
			return fmt.Errorf("restore_mongo_batch_uncertain")
		}
		count := int64(len(result.InsertedIDs))
		r.state.Records += count
		batch = batch[:0]
		r.tick(false)
		return r.finishEffect(effect, tasks.EffectOutcome{Records: count})
	}
	if err := eachLine(f, func(line []byte) error {
		doc, err := restoreMongoDoc(line)
		if err != nil {
			return err
		}
		batch = append(batch, doc)
		if len(batch) >= restoreBatchRows {
			return flush()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	var specs []bson.D
	for _, raw := range indexes {
		var spec bson.D
		if err := bson.Unmarshal(raw, &spec); err != nil {
			return err
		}
		name := ""
		kept := bson.D{}
		for _, e := range spec {
			switch e.Key {
			case "v", "ns":
				continue
			case "name":
				name, _ = e.Value.(string)
			}
			kept = append(kept, e)
		}
		if name != "_id_" {
			specs = append(specs, kept)
		}
	}
	if len(specs) > 0 && len(names) == 0 {
		effect, err := r.beginEffect("mongo-create-indexes")
		if err != nil {
			return err
		}
		if err := db.RunCommand(r.ctx, bson.D{{Key: "createIndexes", Value: targetObject}, {Key: "indexes", Value: specs}}).Err(); err != nil {
			return fmt.Errorf("restore_mongo_index_failed")
		}
		if err := r.finishEffect(effect, tasks.EffectOutcome{}); err != nil {
			return err
		}
	}
	return nil
}

func restoreMongoMetadata(z *zip.Reader, entry backupArchiveEntry, metadata []backupArchiveEntry) (bson.Raw, []bson.Raw, error) {
	for _, md := range metadata {
		if md.Database != entry.Database || md.Object != entry.Object {
			continue
		}
		for _, f := range z.File {
			if f.Name != md.Path {
				continue
			}
			var doc struct {
				Definition struct {
					Definition string   `json:"definition"`
					Indexes    []string `json:"indexes"`
				} `json:"definition"`
			}
			if err := readZipJSON(f, &doc); err != nil {
				return nil, nil, err
			}
			def, err := base64.StdEncoding.DecodeString(doc.Definition.Definition)
			if err != nil {
				return nil, nil, err
			}
			var indexes []bson.Raw
			for _, s := range doc.Definition.Indexes {
				raw, err := base64.StdEncoding.DecodeString(s)
				if err != nil {
					return nil, nil, err
				}
				indexes = append(indexes, raw)
			}
			return def, indexes, nil
		}
	}
	return nil, nil, fmt.Errorf("缺少集合定义")
}

func (r *restoreRunner) redisObject(entry backupArchiveEntry, f *zip.File) error {
	target := entry.Database
	if mapping, ok := r.mappings[entry.Database+"\x00"+entry.Object]; ok {
		target = mapping.TargetDatabase
	}
	index, err := restoreRedisIndex(target)
	if err != nil {
		return err
	}
	conn, err := redisConn(r.ctx, r.s, index)
	if err != nil {
		return err
	}
	defer conn.Close()
	now := time.Now().UnixMilli()
	expired, busy := 0, 0
	err = eachLine(f, func(line []byte) error {
		var rec restoreRedisRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("记录损坏")
		}
		key, err := base64.StdEncoding.DecodeString(rec.Key)
		if err != nil {
			return err
		}
		payload, err := base64.StdEncoding.DecodeString(rec.Dump)
		if err != nil {
			return err
		}
		expiresAt, _ := strconv.ParseInt(rec.ExpiresAtMs, 10, 64)
		args := []any{"RESTORE", string(key), int64(0), string(payload)}
		if expiresAt > 0 {
			if expiresAt <= now {
				expired++
				return nil
			}
			args = []any{"RESTORE", string(key), expiresAt, string(payload), "ABSTTL"}
		}
		effect, err := r.beginEffect("redis-restore-key")
		if err != nil {
			return err
		}
		if err := conn.Do(r.ctx, args...).Err(); err != nil {
			// Never REPLACE: an existing key is kept and counted.
			if strings.Contains(err.Error(), "BUSYKEY") {
				busy++
				return r.finishEffect(effect, tasks.EffectOutcome{NoEffect: true})
			}
			return err
		}
		r.state.Records++
		r.tick(false)
		return r.finishEffect(effect, tasks.EffectOutcome{Records: 1})
	})
	if expired > 0 {
		r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("%s：%d 个 key 已过期，未恢复", entry.Database, expired))
	}
	if busy > 0 {
		r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("%s：%d 个 key 在目标中已存在，已保留原值", entry.Database, busy))
	}
	return err
}

func (r *restoreRunner) beginEffect(kind string) (string, error) {
	if r.reporter == nil {
		return "", nil
	}
	return r.reporter.BeginEffect(r.ctx, tasks.EffectIntent{Kind: kind})
}
func (r *restoreRunner) finishEffect(id string, outcome tasks.EffectOutcome) error {
	if r.reporter == nil {
		return nil
	}
	return r.reporter.FinishEffect(r.ctx, id, outcome)
}

// This classifies transaction boundaries for accounting, not SQL authorization.
func scriptTransactionCommand(stmt string) string {
	for {
		stmt = strings.TrimSpace(stmt)
		if strings.HasPrefix(stmt, "--") || strings.HasPrefix(stmt, "#") {
			if i := strings.IndexByte(stmt, '\n'); i >= 0 {
				stmt = stmt[i+1:]
				continue
			}
			return ""
		}
		if strings.HasPrefix(stmt, "/*") {
			if i := strings.Index(stmt, "*/"); i >= 0 {
				stmt = stmt[i+2:]
				continue
			}
		}
		break
	}
	fields := strings.Fields(strings.ToUpper(stmt))
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "BEGIN":
		return "begin"
	case "START":
		if len(fields) > 1 && fields[1] == "TRANSACTION" {
			return "begin"
		}
	case "COMMIT", "END":
		return "commit"
	case "ROLLBACK":
		if len(fields) > 1 && fields[1] == "TO" {
			return ""
		}
		return "rollback"
	}
	return ""
}
