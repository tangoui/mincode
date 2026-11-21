package db

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"mincode/app/backend/tasks"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Archive paths use numeric identities, never database-controlled filenames.
// Each object is a JSONL stream of native records, not a UI result projection.
type backupArchive struct {
	z               *zip.Writer
	store           bool
	stats           backupDumpStats
	entries         []backupArchiveEntry
	metadataEntries []backupArchiveEntry
}
type backupArchiveEntry struct {
	Path     string `json:"path"`
	Database string `json:"database"`
	Object   string `json:"object"`
	Encoding string `json:"encoding"`
	Records  int64  `json:"records"`
	SHA256   string `json:"sha256"`
}
type backupArchiveStream struct {
	io.Writer
	digest  hash.Hash
	records int64
}

func (w *backupArchiveStream) record(v any) error {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return err
	}
	w.records++
	return nil
}
func (a *backupArchive) object(database, object, encoding string, run func(*backupArchiveStream) error) error {
	name := fmt.Sprintf("objects/%06d.jsonl", len(a.entries)+1)
	out, err := a.create(name)
	if err != nil {
		return err
	}
	digest := sha256.New()
	stream := &backupArchiveStream{Writer: io.MultiWriter(out, digest), digest: digest}
	if err := run(stream); err != nil {
		return fmt.Errorf("导出对象 %q: %w", object, err)
	}
	a.entries = append(a.entries, backupArchiveEntry{name, database, object, encoding, stream.records, hex.EncodeToString(digest.Sum(nil))})
	a.stats.Tables++
	a.stats.Rows += stream.records
	return nil
}
func (a *backupArchive) metadata(database, object string, v any) error {
	name := fmt.Sprintf("metadata/%06d.json", len(a.metadataEntries)+1)
	out, err := a.create(name)
	if err != nil {
		return err
	}
	digest := sha256.New()
	if err := json.NewEncoder(io.MultiWriter(out, digest)).Encode(struct {
		Database   string `json:"database"`
		Object     string `json:"object"`
		Definition any    `json:"definition"`
	}{database, object, v}); err != nil {
		return err
	}
	a.metadataEntries = append(a.metadataEntries, backupArchiveEntry{name, database, object, "native-metadata-json", 0, hex.EncodeToString(digest.Sum(nil))})
	return nil
}
func (a *backupArchive) jsonFile(name string, v any) error {
	out, err := a.create(name)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(v)
}
func writeBackupArchive(ctx context.Context, job BackupJob, s *dbSession, engine string, now time.Time) (backupResult, error) {
	capability := backupSessionCapability(s, engine)
	if !capability.Supported {
		return backupResult{}, fmt.Errorf("%s", strings.Join(backupCapabilityMessages(capability.Reason), ""))
	}
	info, err := os.Stat(job.Destination)
	if err != nil || !info.IsDir() {
		return backupResult{}, fmt.Errorf("备份目录不可用")
	}
	// Job identity prevents retention collisions between jobs with the same prefix.
	identity := sha256.Sum256([]byte(job.ID))
	prefix := job.FileNamePrefix + "-" + hex.EncodeToString(identity[:16])
	final := ""
	for seq := 1; ; seq++ {
		name := strings.TrimSuffix(backupFileName(prefix, now, seq, false), ".sql") + ".zip"
		candidate := filepath.Join(job.Destination, name)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			final = candidate
			break
		} else if err != nil {
			return backupResult{}, err
		}
	}
	tmp, err := os.CreateTemp(job.Destination, ".backup-*.tmp")
	if err != nil {
		return backupResult{}, err
	}
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmp.Name())
		}
	}()
	z := zip.NewWriter(tmp)
	// Store is selected per entry by the writer's method below when compression is off.
	a := &backupArchive{z: z, store: !job.Gzip}
	if err := exportBackupArchive(ctx, a, s, job.Targets); err != nil {
		return backupResult{}, err
	}
	manifest := struct {
		Version     int                  `json:"version"`
		Engine      string               `json:"engine"`
		JobID       string               `json:"jobId"`
		StartedAt   time.Time            `json:"startedAt"`
		Completed   bool                 `json:"completed"`
		Fidelity    string               `json:"fidelity"`
		Consistency string               `json:"consistency"`
		Limitations []string             `json:"limitations"`
		Entries     []backupArchiveEntry `json:"entries"`
		Metadata    []backupArchiveEntry `json:"metadata"`
	}{archiveVersion(s), engine, job.ID, now, true, capability.Fidelity, "read-time; no global point-in-time snapshot", capability.Limitations, a.entries, a.metadataEntries}
	if err := a.jsonFile("manifest.json", manifest); err != nil {
		return backupResult{}, err
	}
	out, err := a.create("RECOVERY.txt")
	if err != nil {
		return backupResult{}, err
	}
	if _, err = io.WriteString(out, backupRecovery(s)); err != nil {
		return backupResult{}, err
	}
	if err = z.Close(); err != nil {
		return backupResult{}, err
	}
	if err = tmp.Sync(); err != nil {
		return backupResult{}, err
	}
	if err = tmp.Close(); err != nil {
		return backupResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return backupResult{}, err
	}
	effect, err := beginTransferEffect(ctx, "backup-publication")
	if err != nil {
		return backupResult{}, err
	}
	if err = os.Link(tmp.Name(), final); err != nil {
		return backupResult{}, err
	}
	committed = true
	_ = os.Remove(tmp.Name())
	st, err := os.Stat(final)
	if err != nil {
		return backupResult{}, err
	}
	if err := finishTransferEffect(ctx, effect, tasks.EffectOutcome{Files: 1, Records: a.stats.Rows, Objects: int64(a.stats.Tables), Bytes: st.Size()}); err != nil {
		return backupResult{}, err
	}
	removed, warnings := applyBackupRetentionPatternContext(ctx, job.Destination, prefix, final, job.Retention, now, `\.zip`)
	return backupResult{File: final, SizeBytes: st.Size(), Tables: a.stats.Tables, Rows: a.stats.Rows, Removed: removed, Warnings: append(backupCapabilityMessages(capability.Limitations...), warnings...)}, nil
}
func backupRecovery(s *dbSession) string {
	instructions := map[sessionKind]string{
		kindMongo:  "Decode bson-base64 records to their original BSON bytes. Create collections using metadata options, insert documents, then recreate indexes. Views must be recreated from their definitions; do not insert their result rows.",
		kindCQL:    "Create the keyspace with explicitly reviewed replication settings. Recreate user-defined types and tables from native metadata before inserting typed rows using bound CQL parameters. TTLs, counter mutations and write timestamps require special handling; do not assume identical expiry or last-write-wins semantics.",
		kindNeo4j:  "Recreate nodes from labels and typed properties, keeping a temporary map from archived elementId to new node ID. Recreate relationships using that map, then indexes and constraints. Never use archived IDs as live database IDs. Temporal and spatial values require the corresponding native driver constructors.",
		kindRedis:  "Decode base64 key and DUMP payload. RESTORE key absoluteExpiry payload ABSTTL, or RESTORE key 0 payload for persistent keys. Do not revive records whose expiry has passed. Restore only into an explicitly selected empty logical DB; no automatic REPLACE.",
		kindSQL:    "Use metadata to create compatible target objects, then decode typed row values and insert with parameterized statements. Binary values are base64; exact numbers are strings with native type tags. Review unsupported object classes and consistency before recovery.",
		kindHTTP:   "Use the engine's native import APIs. Preserve original identifiers and type metadata, recreate schema/settings, load records, then build indexes. Search indexes are not physical server snapshots.",
		kindVector: "Recreate collections with the recorded dimensions, metrics and schema, then import original point/object records with vectors and metadata. Recreate aliases/indexes after loading. Restore tenant and namespace scope explicitly.",
	}
	return "MinCode logical archive v1\nValidate manifest completion and every object SHA256 before recovery.\n" + instructions[s.kind] + "\nNever restore into a production database without an independent review and explicit confirmation. This archive does not contain users, credentials, cluster configuration, transaction logs or point-in-time recovery history.\n"
}

func (a *backupArchive) create(name string) (io.Writer, error) {
	method := uint16(zip.Deflate)
	if a.store {
		method = zip.Store
	}
	return a.z.CreateHeader(&zip.FileHeader{Name: name, Method: method})
}

func archiveVersion(s *dbSession) int {
	if s.kind == kindSQL && (s.dialect == "sqlite" || s.dialect == "mysql") {
		return 2
	}
	return 1
}
