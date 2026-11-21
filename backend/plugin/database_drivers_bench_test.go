package plugin

// Opt-in performance acceptance benchmark for plugin database drivers
// (Phase E). It drives the built-in driver and the plugin driver through the
// same host layer, the db_* command handlers in backend/db, with the plugin
// launched as a real process by this package's manager (install + enable +
// credential grant, exactly as the desktop does).
//
// Run it through tools/bench/database-drivers/run.mjs, which starts the
// servers, builds the plugin packages and sets the environment below.
// Report: docs/development/database-driver-benchmark.md.
//
//	MINCODE_BENCH_DB_DRIVERS=1                  required gate
//	MINCODE_BENCH_CLICKHOUSE_ADDR               host:port of the native protocol (default 127.0.0.1:19000)
//	MINCODE_BENCH_CLICKHOUSE_USER / _PASSWORD   server credentials (test-only)
//	MINCODE_BENCH_CLICKHOUSE_PACKAGE            installable clickhouse-driver package directory
//	MINCODE_BENCH_DUCKDB_PACKAGE                installable duckdb-driver package directory (optional hook)
//	MINCODE_BENCH_DUCKDB_PLUGIN / _DRIVER       DuckDB plugin and driver ids (default duckdb-driver / duckdb)
//	MINCODE_BENCH_SQLITE_PACKAGE                installable sqlite-bench-driver package directory (local file; runs in
//	                                            the nocoredrivers profile, the Windows substitute for ClickHouse/DuckDB)
//	MINCODE_BENCH_ROWS                          paged-fetch table rows (default 100000)
//	MINCODE_BENCH_RUNS                          small-query runs per path (default 100)
//	MINCODE_BENCH_PAGE_SIZES                    db_get_table_rows page sizes (default 5000,500)
//	MINCODE_BENCH_PAGED_RUNS / _EXPORT_RUNS     paged-fetch and export repetitions per path (default 5 / 20)
//	MINCODE_BENCH_GATE                          allowed small-query p50 ratio (default 1.20; 0 disables every gate)
//	MINCODE_BENCH_PAGED_EXTRA_MS                allowed extra paged-fetch p50 time per page size (default 300)
//	MINCODE_BENCH_EXPORT_EXTRA_MS               allowed extra export p50 time (default 20)
//	MINCODE_BENCH_OUT                           JSON results file
//	MINCODE_BENCH_COMPARE_ENCODINGS=1           also measure a plugin session that negotiated the experimental
//	                                            columnar encoding (protocol v2 draft), interleaved with the JSON
//	                                            session (small query, paged fetch, export; result "pluginV2")

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/db"
	"mincode/app/backend/license"
)

const benchDatabase = "mincode_bench"

// benchTarget is one database engine measured through both paths. New
// engines (DuckDB, Phase E T6a) only add a target.
type benchTarget struct {
	name      string
	builtin   db.DbConnectionConfig
	plugin    db.DbConnectionConfig
	pluginID  string
	driverID  string
	pkg       string
	database  string // database argument of the db_* table commands
	table     string // paged-fetch table (rows rows)
	export    string // full-table export table (≤ 5000 rows, the host result cap)
	smallSQL  string
	slowSQL   string // runs long enough to be cancelled
	seed      []string
	network   bool // the acceptance gate applies to network databases
	gateNotes string
	// afterSeed runs once the seed session is closed (DuckDB copies the
	// seeded file so each process holds the lock on its own copy).
	afterSeed func() error
}

type benchStats struct {
	N      int     `json:"n"`
	P50    float64 `json:"p50Ms"`
	P95    float64 `json:"p95Ms"`
	Mean   float64 `json:"meanMs"`
	Min    float64 `json:"minMs"`
	Max    float64 `json:"maxMs"`
	Rows   int     `json:"rows,omitempty"`
	Bytes  int     `json:"bytes,omitempty"`
	Detail string  `json:"detail,omitempty"`
}

type benchPathResult struct {
	Connect     benchStats            `json:"connect"`
	ColdConnect *benchStats           `json:"coldConnect,omitempty"`
	SmallQuery  benchStats            `json:"smallQuery"`
	PagedFetch  map[string]benchStats `json:"pagedFetch"`
	Export      benchStats            `json:"export"`
	Metadata    benchStats            `json:"metadata"`
	Cancel      benchStats            `json:"cancel"`
	RSSIdleKB   int                   `json:"rssIdleKiB,omitempty"`
	RSSPeakKB   int                   `json:"rssPeakKiB,omitempty"`
}

type benchTargetResult struct {
	Target  string          `json:"target"`
	Rows    int             `json:"rows"`
	Builtin benchPathResult `json:"builtin"`
	Plugin  benchPathResult `json:"plugin"`
	// PluginV2 is the columnar-encoding plugin session (compare mode only).
	PluginV2 *benchPathResult   `json:"pluginV2,omitempty"`
	Ratios   map[string]float64 `json:"ratios"`
	Gate     map[string]bool    `json:"gate"`
	ExtraMs  map[string]float64 `json:"extraMs"`
	Network  bool               `json:"network"`
	// PageEncoding is the page encoding the plugin sessions negotiated:
	// "json" (protocol 1.x rows) or "columnar1" (protocol v2 draft, host
	// switch MINCODE_DB_DRIVER_COLUMNAR=1 and a plugin that announces it).
	PageEncoding string `json:"pageEncoding"`
}

type benchReport struct {
	GeneratedAt string              `json:"generatedAt"`
	GoVersion   string              `json:"goVersion"`
	OS          string              `json:"os"`
	Arch        string              `json:"arch"`
	CPUs        int                 `json:"cpus"`
	Runs        int                 `json:"runs"`
	GateRatio   float64             `json:"gateRatio"`
	PagedExtra  float64             `json:"pagedExtraMs"`
	ExportExtra float64             `json:"exportExtraMs"`
	Targets     []benchTargetResult `json:"targets"`
}

// benchGate is the acceptance gate for network drivers: the small-query
// p50 ratio plus absolute extra time for paged fetch and export.
type benchGate struct {
	ratio         float64
	pagedExtraMs  float64
	exportExtraMs float64
}

func roundMs(value float64) float64 { return math.Round(value*100) / 100 }

func benchEnvFloat(t *testing.T, key string, fallback float64) float64 {
	t.Helper()
	raw := benchEnv(key, "")
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 {
		t.Fatalf("%s must be a non-negative number", key)
	}
	return value
}

func benchEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func benchEnvInt(t *testing.T, key string, fallback int) int {
	raw := benchEnv(key, "")
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s must be a positive integer", key)
	}
	return value
}

func benchStringPtr(value string) *string { return &value }

func TestDatabaseDriverBenchmark(t *testing.T) {
	if os.Getenv("MINCODE_BENCH_DB_DRIVERS") != "1" {
		t.Skip("set MINCODE_BENCH_DB_DRIVERS=1 (see tools/bench/database-drivers/run.mjs)")
	}
	rows := benchEnvInt(t, "MINCODE_BENCH_ROWS", 100000)
	runs := benchEnvInt(t, "MINCODE_BENCH_RUNS", 100)
	gate := benchGate{
		ratio:         benchEnvFloat(t, "MINCODE_BENCH_GATE", 1.20),
		pagedExtraMs:  benchEnvFloat(t, "MINCODE_BENCH_PAGED_EXTRA_MS", 300),
		exportExtraMs: benchEnvFloat(t, "MINCODE_BENCH_EXPORT_EXTRA_MS", 20),
	}
	pageSizes := []int{}
	for _, part := range strings.Split(benchEnv("MINCODE_BENCH_PAGE_SIZES", "5000,500"), ",") {
		size, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || size <= 0 || size > 5000 {
			t.Fatal("MINCODE_BENCH_PAGE_SIZES entries must be 1..5000 (the host result cap)")
		}
		pageSizes = append(pageSizes, size)
	}

	// A fresh host manager on temporary plugin data, installed as the process
	// singleton so the db package's provider (host()) resolves to it.
	useTemporaryPluginData(t)
	previousQuota := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) { return license.QuotaPolicy{}, "", false }
	managerOnce.Do(func() {})
	previousManager := managerInst
	managerInst = newPluginManager()
	t.Cleanup(func() {
		for _, id := range managerInst.order {
			if record, err := managerInst.record(id); err == nil {
				_ = managerInst.stopRecord(context.Background(), record, false)
			}
		}
		managerInst = previousManager
		enabledPluginQuota = previousQuota
	})

	targets := []benchTarget{}
	if pkg := benchEnv("MINCODE_BENCH_CLICKHOUSE_PACKAGE", ""); pkg != "" {
		targets = append(targets, clickHouseBenchTarget(pkg, rows))
	}
	if pkg := benchEnv("MINCODE_BENCH_DUCKDB_PACKAGE", ""); pkg != "" {
		targets = append(targets, duckDBBenchTarget(t, pkg, rows))
	}
	if pkg := benchEnv("MINCODE_BENCH_SQLITE_PACKAGE", ""); pkg != "" {
		targets = append(targets, sqliteBenchTarget(t, pkg, rows))
	}
	if len(targets) == 0 {
		t.Fatal("set MINCODE_BENCH_CLICKHOUSE_PACKAGE, MINCODE_BENCH_DUCKDB_PACKAGE and/or MINCODE_BENCH_SQLITE_PACKAGE")
	}
	// The benchmark compares the built-in driver with the plugin, so it needs
	// the full profile (no nocoredrivers/lite tag): the lean core leaves the
	// built-in ClickHouse and DuckDB drivers out.
	targets = benchTargetsWithBuiltinDriver(t, targets)

	report := benchReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339), GoVersion: runtime.Version(),
		OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Runs: runs, GateRatio: gate.ratio, PagedExtra: gate.pagedExtraMs, ExportExtra: gate.exportExtraMs,
	}
	for _, target := range targets {
		result := runBenchTarget(t, target, rows, runs, pageSizes, gate)
		report.Targets = append(report.Targets, result)
		t.Log("\n" + benchMarkdown(result))
	}
	if out := benchEnv("MINCODE_BENCH_OUT", ""); out != "" {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, result := range report.Targets {
		for metric, ok := range result.Gate {
			if !ok {
				t.Errorf("%s: plugin %s fails its gate (ratio %.2fx, extra %.2f ms)", result.Target, metric, result.Ratios[metric], result.ExtraMs[metric])
			}
		}
	}
}

// benchTargetsWithBuiltinDriver drops targets whose built-in driver is not in
// this build and skips the test when none remain.
func benchTargetsWithBuiltinDriver(t *testing.T, targets []benchTarget) []benchTarget {
	t.Helper()
	var supported []string
	benchDispatch(t, "db_supported_types", map[string]any{}, &supported)
	available := make(map[string]bool, len(supported))
	for _, dbType := range supported {
		available[dbType] = true
	}
	kept := targets[:0]
	for _, target := range targets {
		if available[target.builtin.DbType] {
			kept = append(kept, target)
			continue
		}
		t.Logf("%s: built-in driver is not in this build (nocoredrivers/lite profile); the built-in vs plugin comparison needs the full profile, run go test without -tags nocoredrivers", target.name)
	}
	if len(kept) == 0 {
		t.Skip("database driver benchmark requires the full build profile (built-in drivers); run go test without -tags nocoredrivers or lite")
	}
	return kept
}

func clickHouseBenchTarget(pkg string, rows int) benchTarget {
	addr := benchEnv("MINCODE_BENCH_CLICKHOUSE_ADDR", "127.0.0.1:19000")
	host, portText, _ := strings.Cut(addr, ":")
	port, _ := strconv.Atoi(portText)
	base := db.DbConnectionConfig{
		DbType: "clickhouse", Host: host, Port: port,
		Username: benchStringPtr(benchEnv("MINCODE_BENCH_CLICKHOUSE_USER", "default")),
		Password: benchStringPtr(benchEnv("MINCODE_BENCH_CLICKHOUSE_PASSWORD", "")),
		Database: benchStringPtr("default"),
	}
	plugin := base
	plugin.Driver = &db.ExternalDriverRef{PluginID: "clickhouse-driver", DriverID: "clickhouse"}
	plugin.ConnectionID = "bench-clickhouse"
	// compression none matches the built-in driver (T5 parity note).
	plugin.Options = map[string]any{"compression": "none"}
	table := fmt.Sprintf("rows_%d", rows)
	return benchTarget{
		name: "clickhouse", builtin: base, plugin: plugin, pluginID: "clickhouse-driver", driverID: "clickhouse", pkg: pkg,
		database: benchDatabase, table: table, export: "export_5000", network: true,
		smallSQL: "SELECT id, name, category, amount, created_at FROM " + benchDatabase + "." + table + " WHERE id < 20 ORDER BY id",
		slowSQL:  "SELECT count() FROM numbers_mt(100000000000) WHERE NOT ignore(sipHash64(number))",
		seed: []string{
			"CREATE DATABASE IF NOT EXISTS " + benchDatabase,
			"DROP TABLE IF EXISTS " + benchDatabase + "." + table,
			"CREATE TABLE " + benchDatabase + "." + table + " (id UInt64, name String, category LowCardinality(String), amount Float64, created_at DateTime, payload String) ENGINE = MergeTree ORDER BY id",
			fmt.Sprintf("INSERT INTO %s.%s SELECT number, concat('row-', toString(number)), ['alpha','beta','gamma','delta'][number %% 4 + 1], number / 7, toDateTime('2026-01-01 00:00:00') + number, repeat('x', 64) FROM numbers(%d)", benchDatabase, table, rows),
			"DROP TABLE IF EXISTS " + benchDatabase + ".export_5000",
			"CREATE TABLE " + benchDatabase + ".export_5000 ENGINE = MergeTree ORDER BY id AS SELECT * FROM " + benchDatabase + "." + table + " WHERE id < 5000",
		},
	}
}

// duckDBBenchTarget measures built-in DuckDB against the duckdb-driver plugin
// (Phase E T6a; connection field filePath, option accessMode). DuckDB takes
// an exclusive file lock per process, so the host and the plugin process each
// open their own copy of the same seeded file.
func duckDBBenchTarget(t *testing.T, pkg string, rows int) benchTarget {
	dir := t.TempDir()
	file := filepath.Join(dir, "bench.duckdb")
	pluginFile := filepath.Join(dir, "bench-plugin.duckdb")
	base := db.DbConnectionConfig{DbType: "duckdb", FilePath: benchStringPtr(file)}
	plugin := base
	plugin.FilePath = benchStringPtr(pluginFile)
	pluginID := benchEnv("MINCODE_BENCH_DUCKDB_PLUGIN", "duckdb-driver")
	driverID := benchEnv("MINCODE_BENCH_DUCKDB_DRIVER", "duckdb")
	plugin.Driver = &db.ExternalDriverRef{PluginID: pluginID, DriverID: driverID}
	plugin.ConnectionID = "bench-duckdb"
	plugin.Options = map[string]any{"accessMode": "readWrite"}
	table := fmt.Sprintf("rows_%d", rows)
	return benchTarget{
		name: "duckdb", builtin: base, plugin: plugin, pluginID: pluginID, driverID: driverID, pkg: pkg,
		database: benchEnv("MINCODE_BENCH_DUCKDB_DATABASE", "main"), table: table, export: "export_5000",
		smallSQL: "SELECT id, name, category, amount, created_at FROM " + table + " WHERE id < 20 ORDER BY id",
		slowSQL:  "SELECT count(*) FROM range(100000000000) t(x) WHERE hash(x) = 0",
		afterSeed: func() error {
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			return os.WriteFile(pluginFile, data, 0o600)
		},
		seed: []string{
			"DROP TABLE IF EXISTS " + table,
			fmt.Sprintf("CREATE TABLE %s AS SELECT range AS id, 'row-' || range AS name, ['alpha','beta','gamma','delta'][range %% 4 + 1] AS category, range / 7 AS amount, TIMESTAMP '2026-01-01 00:00:00' + to_seconds(range) AS created_at, repeat('x', 64) AS payload FROM range(%d)", table, rows),
			"DROP TABLE IF EXISTS export_5000",
			"CREATE TABLE export_5000 AS SELECT * FROM " + table + " WHERE id < 5000",
			"CHECKPOINT",
		},
		gateNotes: "local file; reported separately (Phase E scope update)",
	}
}

// sqliteBenchTarget measures the built-in SQLite driver (modernc.org/sqlite)
// against tools/bench/database-drivers/sqlite-bench-driver, a benchmark-only
// plugin on the same pure-Go engine. Both are in every build profile and need
// neither Docker nor cgo, so this target runs where ClickHouse and DuckDB
// cannot (Windows without Docker, a cgo toolchain that cannot link DuckDB).
// Like DuckDB it is a local in-process engine: reported, not gated.
func sqliteBenchTarget(t *testing.T, pkg string, rows int) benchTarget {
	file := filepath.Join(t.TempDir(), "bench.sqlite")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	base := db.DbConnectionConfig{DbType: "sqlite", FilePath: benchStringPtr(file)}
	plugin := base
	plugin.Driver = &db.ExternalDriverRef{PluginID: "sqlite-bench-driver", DriverID: "sqlite"}
	plugin.ConnectionID = "bench-sqlite"
	table := fmt.Sprintf("rows_%d", rows)
	return benchTarget{
		name: "sqlite", builtin: base, plugin: plugin, pluginID: "sqlite-bench-driver", driverID: "sqlite", pkg: pkg,
		database: "main", table: table, export: "export_5000",
		smallSQL: "SELECT id, name, category, amount, created_at FROM " + table + " WHERE id < 20 ORDER BY id",
		slowSQL:  "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c",
		seed: []string{
			"DROP TABLE IF EXISTS " + table,
			"CREATE TABLE " + table + " (id INTEGER PRIMARY KEY, name TEXT, category TEXT, amount REAL, created_at TEXT, payload TEXT)",
			fmt.Sprintf("INSERT INTO %s WITH RECURSIVE seq(x) AS (SELECT 0 UNION ALL SELECT x + 1 FROM seq WHERE x < %d) SELECT x, 'row-' || x, CASE x %% 4 WHEN 0 THEN 'alpha' WHEN 1 THEN 'beta' WHEN 2 THEN 'gamma' ELSE 'delta' END, x / 7.0, datetime('2026-01-01 00:00:00', '+' || x || ' seconds'), replace(hex(zeroblob(32)), '00', 'x') FROM seq", table, rows-1),
			"DROP TABLE IF EXISTS export_5000",
			"CREATE TABLE export_5000 AS SELECT * FROM " + table + " WHERE id < 5000",
		},
		gateNotes: "local file, same engine on both paths; reported separately (Windows substitute)",
	}
}

// ---------------------------------------------------------------------------
// db_* command helpers (the same handlers the desktop bridge dispatches).

func benchDispatch(t *testing.T, command string, args any, out any) time.Duration {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	start := benchNow()
	result, err := core.Dispatch(command, string(encoded))
	elapsed := benchNow() - start
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(result), out); err != nil {
			t.Fatalf("%s: decode: %v", command, err)
		}
	}
	return elapsed
}

func benchConnect(t *testing.T, config db.DbConnectionConfig) (string, time.Duration) {
	t.Helper()
	var sessionID string
	elapsed := benchDispatch(t, "db_connect", map[string]any{"config": config}, &sessionID)
	return sessionID, elapsed
}

func benchDisconnect(t *testing.T, sessionID string) {
	benchDispatch(t, "db_disconnect", map[string]any{"sessionId": sessionID}, nil)
}

func benchQuery(t *testing.T, sessionID, sql string) (db.QueryResult, time.Duration) {
	t.Helper()
	var result db.QueryResult
	elapsed := benchDispatch(t, "db_execute_query", map[string]any{"sessionId": sessionID, "query": sql}, &result)
	return result, elapsed
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func summarize(samples []float64) benchStats {
	if len(samples) == 0 {
		return benchStats{}
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	sum := 0.0
	for _, value := range sorted {
		sum += value
	}
	percentile := func(p float64) float64 {
		index := int(math.Ceil(p*float64(len(sorted)))) - 1
		return sorted[max(0, min(index, len(sorted)-1))]
	}
	round := func(value float64) float64 { return math.Round(value*100) / 100 }
	return benchStats{
		N: len(sorted), P50: round(percentile(0.50)), P95: round(percentile(0.95)), Mean: round(sum / float64(len(sorted))),
		Min: round(sorted[0]), Max: round(sorted[len(sorted)-1]),
	}
}

// rssSampler polls a process's resident set size (processRSSKiB: ps on
// POSIX, the working set on Windows).
type rssSampler struct {
	pid  int
	stop chan struct{}
	done chan struct{}
	mu   sync.Mutex
	peak int
}

func startRSSSampler(pid int) *rssSampler {
	sampler := &rssSampler{pid: pid, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(sampler.done)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			if rss := processRSSKiB(pid); rss > 0 {
				sampler.mu.Lock()
				sampler.peak = max(sampler.peak, rss)
				sampler.mu.Unlock()
			}
			select {
			case <-sampler.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return sampler
}

func (sampler *rssSampler) finish() int {
	close(sampler.stop)
	<-sampler.done
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	return sampler.peak
}

// ---------------------------------------------------------------------------
// Measurements.

func runBenchTarget(t *testing.T, target benchTarget, rows, runs int, pageSizes []int, gate benchGate) benchTargetResult {
	t.Helper()
	manager := managerInst
	if _, err := manager.InstallLocal(target.pkg); err != nil {
		t.Fatalf("%s: install plugin: %v", target.name, err)
	}
	if err := manager.SetEnabled(context.Background(), target.pluginID, true); err != nil {
		t.Fatalf("%s: enable plugin: %v", target.name, err)
	}
	if err := manager.grantDatabaseDriver(target.pluginID, target.driverID, target.plugin.ConnectionID); err != nil {
		t.Fatalf("%s: grant: %v", target.name, err)
	}
	record, err := manager.record(target.pluginID)
	if err != nil {
		t.Fatal(err)
	}

	// Seed through the built-in path (same server/file for both paths).
	seedSession, _ := benchConnect(t, target.builtin)
	for _, statement := range target.seed {
		benchQuery(t, seedSession, statement)
	}
	benchDisconnect(t, seedSession)
	if target.afterSeed != nil {
		if err := target.afterSeed(); err != nil {
			t.Fatalf("%s: after seed: %v", target.name, err)
		}
	}

	result := benchTargetResult{Target: target.name, Rows: rows, Network: target.network, Ratios: map[string]float64{}, Gate: map[string]bool{}, ExtraMs: map[string]float64{}}
	result.Builtin.PagedFetch = map[string]benchStats{}
	result.Plugin.PagedFetch = map[string]benchStats{}

	// Connect: cold = plugin process stopped before db_connect (process start,
	// initialize/negotiation, plugin.db.connect); warm = process running.
	cold := []float64{}
	for range 5 {
		if err := manager.stopRecord(context.Background(), record, false); err != nil {
			t.Fatal(err)
		}
		sessionID, elapsed := benchConnect(t, target.plugin)
		cold = append(cold, ms(elapsed))
		benchDisconnect(t, sessionID)
	}
	coldStats := summarize(cold)
	result.Plugin.ColdConnect = &coldStats
	builtinConnect, pluginConnect := []float64{}, []float64{}
	for range 20 {
		sessionID, elapsed := benchConnect(t, target.builtin)
		builtinConnect = append(builtinConnect, ms(elapsed))
		benchDisconnect(t, sessionID)
		sessionID, elapsed = benchConnect(t, target.plugin)
		pluginConnect = append(pluginConnect, ms(elapsed))
		benchDisconnect(t, sessionID)
	}
	result.Builtin.Connect = summarize(builtinConnect)
	result.Plugin.Connect = summarize(pluginConnect)

	builtinSession, _ := benchConnect(t, target.builtin)
	// Encoding comparison: the main plugin session keeps JSON pages and a
	// second session negotiates the columnar encoding; the three sessions run
	// interleaved, so they see the same machine load.
	compareEncodings := benchEnv("MINCODE_BENCH_COMPARE_ENCODINGS", "") == "1"
	previousColumnar := databaseDriverColumnarEnabled
	if compareEncodings {
		databaseDriverColumnarEnabled = func() bool { return false }
	}
	pluginSession, _ := benchConnect(t, target.plugin)
	result.PageEncoding = benchPageEncoding(target.pluginID)
	v2Session := ""
	if compareEncodings {
		databaseDriverColumnarEnabled = func() bool { return true }
		v2Session, _ = benchConnect(t, target.plugin)
		databaseDriverColumnarEnabled = previousColumnar
		if benchPageEncoding(target.pluginID) != "mixed" {
			t.Fatalf("%s: the plugin did not negotiate the columnar encoding (ServerOptions.ColumnarEncoding)", target.name)
		}
		result.PluginV2 = &benchPathResult{PagedFetch: map[string]benchStats{}}
	}
	t.Cleanup(func() {
		_, _ = core.Dispatch("db_disconnect", `{"sessionId":"`+builtinSession+`"}`)
		_, _ = core.Dispatch("db_disconnect", `{"sessionId":"`+pluginSession+`"}`)
		if v2Session != "" {
			_, _ = core.Dispatch("db_disconnect", `{"sessionId":"`+v2Session+`"}`)
		}
	})
	pid, ok := DatabaseDriverProcessID(target.pluginID)
	if !ok {
		t.Fatalf("%s: plugin process id unavailable", target.name)
	}
	result.Plugin.RSSIdleKB = processRSSKiB(pid)
	sampler := startRSSSampler(pid)

	// Small query: warm-up, then alternate paths so drift hits both equally.
	for range 5 {
		benchQuery(t, builtinSession, target.smallSQL)
		benchQuery(t, pluginSession, target.smallSQL)
		if v2Session != "" {
			benchQuery(t, v2Session, target.smallSQL)
		}
	}
	builtinSmall, pluginSmall, v2Small := []float64{}, []float64{}, []float64{}
	var builtinRows, pluginRows, v2Rows int
	var v2Result db.QueryResult
	for range runs {
		r, elapsed := benchQuery(t, builtinSession, target.smallSQL)
		builtinSmall, builtinRows = append(builtinSmall, ms(elapsed)), r.RowCount
		r, elapsed = benchQuery(t, pluginSession, target.smallSQL)
		pluginSmall, pluginRows = append(pluginSmall, ms(elapsed)), r.RowCount
		if v2Session != "" {
			v2Result, elapsed = benchQuery(t, v2Session, target.smallSQL)
			v2Small, v2Rows = append(v2Small, ms(elapsed)), v2Result.RowCount
			// Both encodings must deliver exactly the JSON path's rows.
			if !reflect.DeepEqual(v2Result.Rows, r.Rows) || !reflect.DeepEqual(v2Result.Columns, r.Columns) {
				t.Fatalf("%s: columnar rows differ from JSON rows\n columnar %v\n json     %v", target.name, v2Result.Rows, r.Rows)
			}
		}
	}
	if builtinRows != pluginRows || builtinRows == 0 || (v2Session != "" && v2Rows != pluginRows) {
		t.Fatalf("%s: small query rows builtin=%d plugin=%d v2=%d", target.name, builtinRows, pluginRows, v2Rows)
	}
	result.Builtin.SmallQuery = summarize(builtinSmall)
	result.Plugin.SmallQuery = summarize(pluginSmall)
	result.Builtin.SmallQuery.Rows, result.Plugin.SmallQuery.Rows = builtinRows, pluginRows
	if result.PluginV2 != nil {
		result.PluginV2.SmallQuery = summarize(v2Small)
		result.PluginV2.SmallQuery.Rows = v2Rows
	}

	// Paged fetch of the whole table through db_get_table_rows (limit/offset).
	fetchAll := func(sessionID string, pageSize int) (time.Duration, int) {
		total, fetched := time.Duration(0), 0
		for offset := 0; offset < rows; offset += pageSize {
			var page db.QueryResult
			total += benchDispatch(t, "db_get_table_rows", map[string]any{
				"sessionId": sessionID, "database": target.database, "table": target.table, "limit": pageSize, "offset": offset,
			}, &page)
			fetched += page.RowCount
		}
		return total, fetched
	}
	for _, pageSize := range pageSizes {
		key := strconv.Itoa(pageSize)
		builtinFetch, pluginFetch, v2Fetch := []float64{}, []float64{}, []float64{}
		fetchAll(builtinSession, pageSize)
		fetchAll(pluginSession, pageSize)
		if v2Session != "" {
			fetchAll(v2Session, pageSize)
		}
		for range benchEnvInt(t, "MINCODE_BENCH_PAGED_RUNS", 5) {
			elapsed, fetched := fetchAll(builtinSession, pageSize)
			if fetched != rows {
				t.Fatalf("%s: builtin paged fetch returned %d rows", target.name, fetched)
			}
			builtinFetch = append(builtinFetch, ms(elapsed))
			elapsed, fetched = fetchAll(pluginSession, pageSize)
			if fetched != rows {
				t.Fatalf("%s: plugin paged fetch returned %d rows", target.name, fetched)
			}
			pluginFetch = append(pluginFetch, ms(elapsed))
			if v2Session != "" {
				elapsed, fetched = fetchAll(v2Session, pageSize)
				if fetched != rows {
					t.Fatalf("%s: columnar plugin paged fetch returned %d rows", target.name, fetched)
				}
				v2Fetch = append(v2Fetch, ms(elapsed))
			}
		}
		stats := summarize(builtinFetch)
		stats.Rows = rows
		result.Builtin.PagedFetch[key] = stats
		stats = summarize(pluginFetch)
		stats.Rows = rows
		result.Plugin.PagedFetch[key] = stats
		if result.PluginV2 != nil {
			stats = summarize(v2Fetch)
			stats.Rows = rows
			result.PluginV2.PagedFetch[key] = stats
		}
	}

	// Full-table export through db_export_table (CSV) of the 5000-row table:
	// the existing export path returns at most 5000 rows (host result cap).
	exportOnce := func(sessionID string) (time.Duration, int) {
		var content string
		elapsed := benchDispatch(t, "db_export_table", map[string]any{
			"sessionId": sessionID, "database": target.database, "table": target.export, "format": "csv", "limit": 10000,
		}, &content)
		return elapsed, len(content)
	}
	exportOnce(builtinSession)
	exportOnce(pluginSession)
	if v2Session != "" {
		exportOnce(v2Session)
	}
	builtinExport, pluginExport, v2Export := []float64{}, []float64{}, []float64{}
	var builtinBytes, pluginBytes, v2Bytes int
	for range benchEnvInt(t, "MINCODE_BENCH_EXPORT_RUNS", 20) {
		elapsed, size := exportOnce(builtinSession)
		builtinExport, builtinBytes = append(builtinExport, ms(elapsed)), size
		elapsed, size = exportOnce(pluginSession)
		pluginExport, pluginBytes = append(pluginExport, ms(elapsed)), size
		if v2Session != "" {
			elapsed, size = exportOnce(v2Session)
			v2Export, v2Bytes = append(v2Export, ms(elapsed)), size
		}
	}
	if result.PluginV2 != nil {
		if v2Bytes != pluginBytes {
			t.Fatalf("%s: columnar export %d bytes, JSON export %d bytes", target.name, v2Bytes, pluginBytes)
		}
		result.PluginV2.Export = summarize(v2Export)
		result.PluginV2.Export.Rows, result.PluginV2.Export.Bytes = 5000, v2Bytes
	}
	result.Builtin.Export = summarize(builtinExport)
	result.Plugin.Export = summarize(pluginExport)
	result.Builtin.Export.Rows, result.Plugin.Export.Rows = 5000, 5000
	result.Builtin.Export.Bytes, result.Plugin.Export.Bytes = builtinBytes, pluginBytes
	result.Plugin.RSSPeakKB = sampler.finish()

	// Metadata: the object-browser load sequence for one table. Only commands
	// both paths support are timed (the built-in ClickHouse driver has no
	// db_get_primary_keys), so the two sums stay comparable.
	metadataArgs := func(command, sessionID string) map[string]any {
		switch command {
		case "db_get_navigation_schema":
			return map[string]any{"sessionId": sessionID}
		case "db_get_database_tables":
			return map[string]any{"sessionId": sessionID, "database": target.database}
		default:
			return map[string]any{"sessionId": sessionID, "database": target.database, "table": target.table}
		}
	}
	metadataCommands, skipped := []string{}, []string{}
	for _, command := range []string{"db_get_navigation_schema", "db_get_database_tables", "db_get_table_columns", "db_get_primary_keys", "db_get_table_ddl"} {
		builtinArgs, _ := json.Marshal(metadataArgs(command, builtinSession))
		pluginArgs, _ := json.Marshal(metadataArgs(command, pluginSession))
		_, builtinErr := core.Dispatch(command, string(builtinArgs))
		_, pluginErr := core.Dispatch(command, string(pluginArgs))
		if builtinErr != nil || pluginErr != nil {
			skipped = append(skipped, command)
			continue
		}
		metadataCommands = append(metadataCommands, command)
	}
	metadataOnce := func(sessionID string) time.Duration {
		total := time.Duration(0)
		for _, command := range metadataCommands {
			total += benchDispatch(t, command, metadataArgs(command, sessionID), nil)
		}
		return total
	}
	metadataOnce(builtinSession)
	metadataOnce(pluginSession)
	builtinMeta, pluginMeta := []float64{}, []float64{}
	for range 20 {
		builtinMeta = append(builtinMeta, ms(metadataOnce(builtinSession)))
		pluginMeta = append(pluginMeta, ms(metadataOnce(pluginSession)))
	}
	result.Builtin.Metadata = summarize(builtinMeta)
	result.Plugin.Metadata = summarize(pluginMeta)
	result.Builtin.Metadata.Detail = strings.Join(metadataCommands, " + ")
	if len(skipped) > 0 {
		result.Builtin.Metadata.Detail += " (skipped, unsupported by a path: " + strings.Join(skipped, ", ") + ")"
	}
	result.Plugin.Metadata.Detail = result.Builtin.Metadata.Detail

	// Cancellation: time from db_cancel_query to db_execute_query returning.
	cancelOnce := func(sessionID string) float64 {
		queryID := fmt.Sprintf("bench-cancel-%d", time.Now().UnixNano())
		returned := make(chan time.Duration, 1)
		go func() {
			encoded, _ := json.Marshal(map[string]any{"sessionId": sessionID, "query": target.slowSQL, "queryId": queryID})
			_, _ = core.Dispatch("db_execute_query", string(encoded))
			returned <- benchNow()
		}()
		time.Sleep(400 * time.Millisecond)
		start := benchNow()
		var cancelled bool
		benchDispatch(t, "db_cancel_query", map[string]any{"queryId": queryID}, &cancelled)
		select {
		case end := <-returned:
			if !cancelled {
				t.Fatalf("%s: slow query finished before cancel", target.name)
			}
			return ms(end - start)
		case <-time.After(30 * time.Second):
			t.Fatalf("%s: query did not return 30s after cancel", target.name)
			return 0
		}
	}
	builtinCancel, pluginCancel := []float64{}, []float64{}
	for range 5 {
		builtinCancel = append(builtinCancel, cancelOnce(builtinSession))
		pluginCancel = append(pluginCancel, cancelOnce(pluginSession))
	}
	result.Builtin.Cancel = summarize(builtinCancel)
	result.Plugin.Cancel = summarize(pluginCancel)

	ratio := func(plugin, builtin float64) float64 {
		if builtin <= 0 {
			return 0
		}
		return math.Round(plugin/builtin*1000) / 1000
	}
	result.Ratios["smallQueryP50"] = ratio(result.Plugin.SmallQuery.P50, result.Builtin.SmallQuery.P50)
	result.Ratios["smallQueryP95"] = ratio(result.Plugin.SmallQuery.P95, result.Builtin.SmallQuery.P95)
	for _, pageSize := range pageSizes {
		key := strconv.Itoa(pageSize)
		result.Ratios["pagedFetch"+key] = ratio(result.Plugin.PagedFetch[key].P50, result.Builtin.PagedFetch[key].P50)
	}
	result.Ratios["export"] = ratio(result.Plugin.Export.P50, result.Builtin.Export.P50)
	result.Ratios["metadata"] = ratio(result.Plugin.Metadata.P50, result.Builtin.Metadata.P50)
	result.Ratios["connectWarm"] = ratio(result.Plugin.Connect.P50, result.Builtin.Connect.P50)
	result.Ratios["cancel"] = ratio(result.Plugin.Cancel.P50, result.Builtin.Cancel.P50)
	result.ExtraMs["export"] = roundMs(result.Plugin.Export.P50 - result.Builtin.Export.P50)
	for _, pageSize := range pageSizes {
		key := strconv.Itoa(pageSize)
		result.ExtraMs["pagedFetch"+key] = roundMs(result.Plugin.PagedFetch[key].P50 - result.Builtin.PagedFetch[key].P50)
	}
	if v2 := result.PluginV2; v2 != nil {
		// V2: columnar plugin vs built-in; V2vsV1: columnar vs JSON plugin.
		result.Ratios["smallQueryP50V2"] = ratio(v2.SmallQuery.P50, result.Builtin.SmallQuery.P50)
		result.Ratios["smallQueryP50V2vsV1"] = ratio(v2.SmallQuery.P50, result.Plugin.SmallQuery.P50)
		result.Ratios["exportV2"] = ratio(v2.Export.P50, result.Builtin.Export.P50)
		result.Ratios["exportV2vsV1"] = ratio(v2.Export.P50, result.Plugin.Export.P50)
		result.ExtraMs["exportV2"] = roundMs(v2.Export.P50 - result.Builtin.Export.P50)
		for _, pageSize := range pageSizes {
			key := strconv.Itoa(pageSize)
			result.Ratios["pagedFetch"+key+"V2"] = ratio(v2.PagedFetch[key].P50, result.Builtin.PagedFetch[key].P50)
			result.Ratios["pagedFetch"+key+"V2vsV1"] = ratio(v2.PagedFetch[key].P50, result.Plugin.PagedFetch[key].P50)
			result.ExtraMs["pagedFetch"+key+"V2"] = roundMs(v2.PagedFetch[key].P50 - result.Builtin.PagedFetch[key].P50)
		}
	}
	if gate.ratio > 0 && target.network {
		result.Gate["smallQueryP50"] = result.Ratios["smallQueryP50"] <= gate.ratio
		result.Gate["export"] = result.ExtraMs["export"] <= gate.exportExtraMs
		for _, pageSize := range pageSizes {
			metric := "pagedFetch" + strconv.Itoa(pageSize)
			result.Gate[metric] = result.ExtraMs[metric] <= gate.pagedExtraMs
		}
	}
	return result
}

// benchPageEncoding reports the page encoding of the plugin's open sessions.
func benchPageEncoding(pluginID string) string {
	databaseDriverRegistry.mu.Lock()
	defer databaseDriverRegistry.mu.Unlock()
	encoding := ""
	if state := databaseDriverRegistry.plugins[pluginID]; state != nil {
		for conn := range state.sessions {
			current := "json"
			if conn.columnar {
				current = databaseDriverEncodingColumnar
			}
			if encoding != "" && encoding != current {
				return "mixed"
			}
			encoding = current
		}
	}
	return encoding
}

func benchMarkdown(result benchTargetResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d rows, plugin pages %s)\n| metric | built-in p50 (p95) ms | plugin p50 (p95) ms | ratio |\n| --- | --- | --- | --- |\n", result.Target, result.Rows, result.PageEncoding)
	row := func(name string, builtin, plugin benchStats, ratioKey string) {
		fmt.Fprintf(&b, "| %s | %.2f (%.2f) | %.2f (%.2f) | %.3f |\n", name, builtin.P50, builtin.P95, plugin.P50, plugin.P95, result.Ratios[ratioKey])
	}
	if result.Plugin.ColdConnect != nil {
		fmt.Fprintf(&b, "| connect (plugin cold) | – | %.2f (%.2f) | – |\n", result.Plugin.ColdConnect.P50, result.Plugin.ColdConnect.P95)
	}
	row("connect (warm)", result.Builtin.Connect, result.Plugin.Connect, "connectWarm")
	row("small query", result.Builtin.SmallQuery, result.Plugin.SmallQuery, "smallQueryP50")
	keys := make([]string, 0, len(result.Builtin.PagedFetch))
	for key := range result.Builtin.PagedFetch {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		row("paged fetch, page "+key, result.Builtin.PagedFetch[key], result.Plugin.PagedFetch[key], "pagedFetch"+key)
	}
	row("export CSV 5000 rows", result.Builtin.Export, result.Plugin.Export, "export")
	if v2 := result.PluginV2; v2 != nil {
		row("small query, columnar", result.Builtin.SmallQuery, v2.SmallQuery, "smallQueryP50V2")
		for _, key := range keys {
			row("paged fetch, page "+key+", columnar", result.Builtin.PagedFetch[key], v2.PagedFetch[key], "pagedFetch"+key+"V2")
		}
		row("export CSV 5000 rows, columnar", result.Builtin.Export, v2.Export, "exportV2")
	}
	row("metadata load", result.Builtin.Metadata, result.Plugin.Metadata, "metadata")
	row("cancel latency", result.Builtin.Cancel, result.Plugin.Cancel, "cancel")
	fmt.Fprintf(&b, "plugin RSS idle %d KiB, peak %d KiB; gate %v\n", result.Plugin.RSSIdleKB, result.Plugin.RSSPeakKB, result.Gate)
	return b.String()
}
