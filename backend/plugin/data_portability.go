package plugin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mincode/app/backend/core"
)

// Plugin data (the plugin_kv namespace plus plugin file storage) can leave the
// device in two ways: embedded in the settings config export, or as a
// per-plugin zip from the plugin list. Both share the validation and swap
// below. plugin_secrets stay on the device (they are sealed with a machine key)
// and the host's own namespace (enabled state and scope) never travels: a data
// file must not switch plugins on behind the user's back. The one host-owned
// entry that does travel is the plugin's non-secret configuration document
// ("configuration" member, configuration_store.go); configuration secrets are
// plugin_secrets and are never exported.
//
// Compatibility: hosts without the configuration point reject the
// "configuration" member (and the archive's configuration.json) as unknown.
// An import that carries no configuration leaves the stored values alone.
const pluginDataImportMaxPlugins = 512

type pluginDataSnapshot map[string]map[string]json.RawMessage

type pluginDataImportResult struct {
	Imported     []string `json:"imported"`
	NotInstalled []string `json:"notInstalled"`
}

type pluginDataSummary struct {
	ID            string `json:"id"`
	Keys          int    `json:"keys"`
	Files         int    `json:"files"`
	Bytes         int64  `json:"bytes"`
	Configuration bool   `json:"configuration,omitempty"`
}

func init() {
	core.Register("plugin_host_config_export_write", pluginHostConfigExportWriteHandler)
	core.Register("plugin_host_config_import_read", pluginHostConfigImportReadHandler)
	core.Register("plugin_host_config_import_plugins", pluginHostConfigImportPluginsHandler)
}

// validatePluginDataSnapshot checks every limit plugin_kv enforces on single
// writes before anything is replaced, so a bad file changes nothing.
func validatePluginDataSnapshot(snapshot pluginDataSnapshot) (pluginDataSnapshot, error) {
	if len(snapshot) > pluginDataImportMaxPlugins {
		return nil, fmt.Errorf("插件数据最多包含 %d 个插件", pluginDataImportMaxPlugins)
	}
	valid := pluginDataSnapshot{}
	for rawID, entries := range snapshot {
		id, err := validatePluginID(rawID)
		if err != nil {
			return nil, err
		}
		if id == hostPluginID {
			return nil, errors.New("插件数据不能包含宿主命名空间")
		}
		if _, duplicate := valid[id]; duplicate {
			return nil, fmt.Errorf("插件数据中的插件 %q 重复", id)
		}
		if len(entries) > pluginKVMaxKeys {
			return nil, fmt.Errorf("插件 %q 的数据超过 %d 个 key", id, pluginKVMaxKeys)
		}
		values := make(map[string]json.RawMessage, len(entries))
		for rawKey, value := range entries {
			key, err := validatePluginKey(rawKey)
			if err != nil {
				return nil, fmt.Errorf("插件 %q: %w", id, err)
			}
			if _, duplicate := values[key]; duplicate {
				return nil, fmt.Errorf("插件 %q 的 key %q 重复", id, key)
			}
			var compact bytes.Buffer
			if len(value) == 0 || json.Compact(&compact, value) != nil {
				return nil, fmt.Errorf("插件 %q 的 %q 不是合法 JSON", id, key)
			}
			if compact.Len() > pluginKVMaxValueBytes {
				return nil, fmt.Errorf("插件 %q 的 %q 超过 %d 字节", id, key, pluginKVMaxValueBytes)
			}
			values[key] = compact.Bytes()
		}
		valid[id] = values
	}
	return valid, nil
}

// pluginDataStage collects one plugin's incoming data. Files are validated
// against the file storage limits and, unless only validating, written to a
// private staging directory that is later renamed into place.
type pluginDataStage struct {
	ID    string
	KV    map[string]json.RawMessage
	Files int
	Bytes int64
	// Configuration is the non-secret configuration document; nil when the
	// source carried none (the stored document is then left unchanged).
	Configuration json.RawMessage
	dir           string
	root          *os.Root
	names         map[string]bool
}

func newPluginDataStage(rawID string, write bool) (*pluginDataStage, error) {
	id, err := validatePluginID(rawID)
	if err != nil {
		return nil, err
	}
	if id == hostPluginID {
		return nil, errors.New("插件数据不能包含宿主命名空间")
	}
	if _, err := pluginStorageRoot(id); err != nil {
		return nil, err
	}
	stage := &pluginDataStage{ID: id, KV: map[string]json.RawMessage{}, names: map[string]bool{}}
	if !write {
		return stage, nil
	}
	base := filepath.Join(userDataDir(), pluginDataDirectory, "plugin-storage-import")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	stage.dir = filepath.Join(base, hex.EncodeToString(random[:]))
	if err := os.Mkdir(stage.dir, 0o700); err != nil {
		return nil, err
	}
	if stage.root, err = os.OpenRoot(stage.dir); err != nil {
		_ = os.RemoveAll(stage.dir)
		return nil, err
	}
	return stage, nil
}

func (stage *pluginDataStage) setKV(values map[string]json.RawMessage) error {
	valid, err := validatePluginDataSnapshot(pluginDataSnapshot{stage.ID: values})
	if err != nil {
		return err
	}
	stage.KV = valid[stage.ID]
	return nil
}

func (stage *pluginDataStage) setConfiguration(raw json.RawMessage) error {
	document, err := parseConfigurationDocument(raw)
	if err != nil {
		return fmt.Errorf("插件 %q 的设置无效: %w", stage.ID, err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return err
	}
	stage.Configuration = encoded
	return nil
}

func (stage *pluginDataStage) addFile(name string, source io.Reader) error {
	if err := validPluginStoragePath(name, false); err != nil || strings.HasPrefix(path.Base(name), pluginStorageTempPrefix) {
		return fmt.Errorf("插件 %q 的文件路径 %q 无效", stage.ID, name)
	}
	if stage.names[name] {
		return fmt.Errorf("插件 %q 的文件 %q 重复", stage.ID, name)
	}
	stage.names[name] = true
	stage.Files++
	if stage.Files > pluginStorageMaxFiles {
		return fmt.Errorf("插件 %q 的文件超过 %d 个", stage.ID, pluginStorageMaxFiles)
	}
	target := io.Discard
	if stage.root != nil {
		if parent := path.Dir(name); parent != "." {
			if err := stage.root.MkdirAll(parent, 0o700); err != nil {
				return err
			}
		}
		file, err := stage.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("插件 %q 的文件 %q 无法暂存: %w", stage.ID, name, err)
		}
		defer file.Close()
		target = file
	}
	// Declared sizes are untrusted; the limit also guards against zip bombs.
	written, err := io.Copy(target, io.LimitReader(source, pluginStorageMaxFileBytes+1))
	if err != nil {
		return fmt.Errorf("插件 %q 的文件 %q 无效: %w", stage.ID, name, err)
	}
	if written > pluginStorageMaxFileBytes {
		return fmt.Errorf("插件 %q 的文件 %q 超过单个文件上限", stage.ID, name)
	}
	stage.Bytes += written
	if stage.Bytes > pluginStorageMaxBytes {
		return fmt.Errorf("插件 %q 的文件超过插件存储容量上限", stage.ID)
	}
	return nil
}

func (stage *pluginDataStage) summary() pluginDataSummary {
	return pluginDataSummary{ID: stage.ID, Keys: len(stage.KV), Files: stage.Files, Bytes: stage.Bytes, Configuration: stage.Configuration != nil}
}

func (stage *pluginDataStage) cleanup() {
	if stage.root != nil {
		_ = stage.root.Close()
		stage.root = nil
	}
	if stage.dir != "" {
		_ = os.RemoveAll(stage.dir)
		_ = os.RemoveAll(stage.dir + ".previous")
	}
}

func cleanupStages(stages []*pluginDataStage) {
	for _, stage := range stages {
		stage.cleanup()
	}
}

// applyPluginDataStages replaces each staged plugin's KV namespace and file
// storage. Installed plugins are stopped first so a running process cannot
// write stale state over the imported data, and restarted afterwards if they
// were running. Data for plugins that are not installed is kept: plugin data
// survives uninstall, so it is picked up when the plugin is installed later.
func (manager *pluginManager) applyPluginDataStages(ctx context.Context, stages []*pluginDataStage) (pluginDataImportResult, error) {
	result := pluginDataImportResult{Imported: []string{}, NotInstalled: []string{}}
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	for _, stage := range stages {
		if stage.root != nil {
			_ = stage.root.Close()
			stage.root = nil
		}
		record, err := manager.record(stage.ID)
		if err != nil {
			result.NotInstalled = append(result.NotInstalled, stage.ID)
			record = nil
		}
		running := false
		if record != nil {
			record.mu.Lock()
			running = record.state == pluginStateRunning
			record.mu.Unlock()
			if err := manager.stopRecord(ctx, record, false); err != nil {
				return result, fmt.Errorf("停止插件 %q 失败: %w", stage.ID, err)
			}
		}
		importErr := replacePluginData(stage.ID, stage.KV, stage.dir, stage.Files > 0)
		if importErr == nil && stage.Configuration != nil {
			configurationMu.Lock()
			importErr = pluginKVSetRaw(hostPluginID, configurationKVKey(stage.ID), stage.Configuration)
			configurationMu.Unlock()
			if importErr == nil {
				core.Emit(pluginConfigurationChangedEvent, map[string]any{"pluginId": stage.ID, "keys": []string{}})
			}
		}
		if record != nil {
			if running {
				if err := manager.startRecord(ctx, record); err != nil {
					record.mu.Lock()
					record.appendLogLocked("error", "restart after data import failed: "+err.Error())
					record.mu.Unlock()
				}
			}
			if importErr == nil {
				record.mu.Lock()
				record.appendLogLocked("info", "plugin data imported")
				record.mu.Unlock()
				manager.emitState(record)
			}
		}
		if importErr != nil {
			return result, fmt.Errorf("导入插件 %q 的数据失败: %w", stage.ID, importErr)
		}
		result.Imported = append(result.Imported, stage.ID)
	}
	return result, nil
}

// replacePluginData swaps the staged file tree into place and then replaces the
// KV namespace; a failed KV write restores the previous files.
func replacePluginData(id string, kv map[string]json.RawMessage, staging string, hasFiles bool) error {
	root, err := pluginStorageRoot(id)
	if err != nil {
		return err
	}
	pluginStorageMu.Lock()
	defer pluginStorageMu.Unlock()
	backup := ""
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("plugin storage root is not a directory")
		}
		backup = staging + ".previous"
		if err := os.Rename(root, backup); err != nil {
			return fmt.Errorf("备份插件文件失败: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	restore := func() {
		_ = os.RemoveAll(root)
		if backup != "" {
			_ = os.Rename(backup, root)
		}
	}
	if hasFiles {
		if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
			restore()
			return err
		}
		if err := os.Rename(staging, root); err != nil {
			restore()
			return fmt.Errorf("写入插件文件失败: %w", err)
		}
	}
	if err := pluginKVReplaceNamespacesRepository(pluginDataSnapshot{id: kv}); err != nil {
		restore()
		return err
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func pluginKVReplaceNamespacesRepository(snapshot pluginDataSnapshot) error {
	ids := make([]string, 0, len(snapshot))
	for id := range snapshot {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		customAiPromptLock(id).Lock()
	}
	defer func() {
		for i := len(ids) - 1; i >= 0; i-- {
			customAiPromptLock(ids[i]).Unlock()
		}
	}()
	for _, id := range ids {
		raw, exists := snapshot[id][customAiPromptsKey]
		_, found, err := pluginKVGetRaw(id, customAiPromptsKey)
		if err != nil {
			return err
		}
		if exists || found {
			encoded, err := customAiPromptReplacement(id, raw, !exists)
			if err != nil {
				return err
			}
			if snapshot[id] == nil {
				snapshot[id] = map[string]json.RawMessage{}
			}
			snapshot[id][customAiPromptsKey] = encoded
		}
	}

	db, err := openPluginDB()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("开始导入插件数据: %w", err)
	}
	defer tx.Rollback()
	for id, entries := range snapshot {
		if _, err := tx.Exec("DELETE FROM plugin_kv WHERE plugin_id = ?", id); err != nil {
			return fmt.Errorf("清理插件 %q 的数据: %w", id, err)
		}
		for key, value := range entries {
			if _, err := tx.Exec("INSERT INTO plugin_kv(plugin_id, key, value) VALUES(?, ?, ?)", id, key, []byte(value)); err != nil {
				return fmt.Errorf("写入插件 %q 的 %q: %w", id, key, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交插件数据: %w", err)
	}
	return nil
}

func pluginKVNamespaceRepository(id string) (map[string]json.RawMessage, error) {
	db, err := openPluginDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT key, value FROM plugin_kv WHERE plugin_id = ? ORDER BY key", id)
	if err != nil {
		return nil, fmt.Errorf("读取插件 %q 的数据: %w", id, err)
	}
	defer rows.Close()
	values := map[string]json.RawMessage{}
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		// Every write path validates JSON; a corrupt row is skipped rather than failing the export.
		if json.Valid(value) {
			values[key] = json.RawMessage(bytes.Clone(value))
		}
	}
	return values, rows.Err()
}

// pluginIDsWithData lists every plugin (installed or not) that has KV rows or a
// file storage directory, excluding the host namespace.
func pluginIDsWithData() ([]string, error) {
	db, err := openPluginDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT DISTINCT plugin_id FROM plugin_kv WHERE plugin_id <> ?", hostPluginID)
	if err != nil {
		return nil, fmt.Errorf("列出插件数据: %w", err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		seen[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	configured, err := pluginIDsWithConfiguration()
	if err != nil {
		return nil, err
	}
	for _, id := range configured {
		seen[id] = true
	}
	entries, err := os.ReadDir(filepath.Join(userDataDir(), pluginDataDirectory, pluginStorageDirectory))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := pluginStorageRoot(entry.Name()); err == nil && entry.Name() != hostPluginID {
				seen[entry.Name()] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// walkPluginStorage visits every stored file under the storage lock, which also
// serializes the plugin's own file writes, so each file is read whole.
func walkPluginStorage(id string, visit func(name string, source io.Reader) error) error {
	pluginStorageMu.Lock()
	defer pluginStorageMu.Unlock()
	root, err := openPluginStorage(id, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("plugin storage contains a symlink")
		}
		if entry.IsDir() || strings.HasPrefix(path.Base(name), pluginStorageTempPrefix) {
			return nil
		}
		source, err := root.Open(name)
		if err != nil {
			return err
		}
		defer source.Close()
		info, err := source.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("plugin storage contains a non-regular file")
		}
		return visit(name, io.LimitReader(source, pluginStorageMaxFileBytes))
	})
}

// writeAtomically creates the target through a temporary sibling, so a failed
// export never leaves a truncated file behind.
func writeAtomically(target string, write func(io.Writer) error) error {
	temp, err := os.CreateTemp(filepath.Dir(target), ".mincode-export-*")
	if err != nil {
		return fmt.Errorf("无法写入所选位置: %w", err)
	}
	name := temp.Name()
	defer os.Remove(name)
	buffered := bufio.NewWriterSize(temp, 1<<20)
	err = write(buffered)
	if err == nil {
		err = buffered.Flush()
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		return fmt.Errorf("保存文件失败: %w", err)
	}
	return nil
}

// ---- settings config export ----

var configExportKeyOrder = []string{"format", "version", "exportedAt", "includesSecrets", "values"}

func absoluteDialogPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("请通过对话框选择文件")
	}
	return path, nil
}

func pluginHostConfigExportWriteHandler(_, argsJSON string) (string, error) {
	var args struct {
		Path   string                     `json:"path"`
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	target, err := absoluteDialogPath(args.Path)
	if err != nil {
		return "", err
	}
	if len(args.Config) == 0 {
		return "", errors.New("配置内容为空")
	}
	summaries, err := writeConfigExportWithPlugins(target, args.Config)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]any{"plugins": summaries})
}

// writeConfigExportWithPlugins streams the settings export with a "plugins"
// member: { pluginId: { kv: {key: JSON}, files: {path: base64} } }. Files are
// base64-encoded straight from disk, so large storage never sits in memory.
func writeConfigExportWithPlugins(target string, config map[string]json.RawMessage) ([]pluginDataSummary, error) {
	ids, err := pluginIDsWithData()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(config))
	for _, key := range configExportKeyOrder {
		if _, ok := config[key]; ok {
			keys = append(keys, key)
		}
	}
	rest := []string{}
	for key := range config {
		if key != "plugins" && !slicesContains(configExportKeyOrder, key) {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	summaries := []pluginDataSummary{}
	err = writeAtomically(target, func(w io.Writer) error {
		if _, err := io.WriteString(w, "{\n"); err != nil {
			return err
		}
		for _, key := range keys {
			var indented bytes.Buffer
			if err := json.Indent(&indented, config[key], "  ", "  "); err != nil {
				return fmt.Errorf("配置字段 %q 无效", key)
			}
			if err := writeJSONKey(w, "  ", key); err != nil {
				return err
			}
			if _, err := w.Write(append(indented.Bytes(), ",\n"...)); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "  \"plugins\": {"); err != nil {
			return err
		}
		first := true
		for _, id := range ids {
			summary, err := writePluginDataJSON(w, id, &first)
			if err != nil {
				return fmt.Errorf("导出插件 %q 的数据失败: %w", id, err)
			}
			if summary != nil {
				summaries = append(summaries, *summary)
			}
		}
		_, err := io.WriteString(w, "\n  }\n}\n")
		return err
	})
	return summaries, err
}

func slicesContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func writeJSONKey(w io.Writer, indent, key string) error {
	encoded, _ := json.Marshal(key)
	_, err := fmt.Fprintf(w, "%s%s: ", indent, encoded)
	return err
}

// pluginStoredData is what the exports read for one plugin before streaming
// file bodies: the KV namespace, the stored file names and the exportable
// configuration document (nil when there is none).
type pluginStoredData struct {
	KV            map[string]json.RawMessage
	Files         []string
	Configuration json.RawMessage
}

func (data pluginStoredData) empty() bool {
	return len(data.KV) == 0 && len(data.Files) == 0 && data.Configuration == nil
}

// readPluginDataForBackup reads a plugin's KV, file list and configuration;
// the settings export and the encrypted backup share it.
func readPluginDataForBackup(id string) (pluginStoredData, error) {
	kv, err := pluginKVNamespaceRepository(id)
	if err != nil {
		return pluginStoredData{}, err
	}
	files, err := pluginStorageList(id, "")
	if err != nil {
		return pluginStoredData{}, err
	}
	configuration, err := exportableConfigurationDocument(id)
	if err != nil {
		return pluginStoredData{}, err
	}
	return pluginStoredData{KV: kv, Files: files, Configuration: configuration}, nil
}

func writePluginDataJSON(w io.Writer, id string, first *bool) (*pluginDataSummary, error) {
	data, err := readPluginDataForBackup(id)
	if err != nil {
		return nil, err
	}
	if data.empty() {
		return nil, nil
	}
	kv, configuration := data.KV, data.Configuration
	kvJSON, err := json.Marshal(kv)
	if err != nil {
		return nil, err
	}
	separator := "\n"
	if !*first {
		separator = ",\n"
	}
	*first = false
	if _, err := io.WriteString(w, separator); err != nil {
		return nil, err
	}
	if err := writeJSONKey(w, "    ", id); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(w, "{\"kv\": %s, \"files\": {", kvJSON); err != nil {
		return nil, err
	}
	summary := &pluginDataSummary{ID: id, Keys: len(kv), Configuration: configuration != nil}
	err = walkPluginStorage(id, func(name string, source io.Reader) error {
		prefix := ""
		if summary.Files > 0 {
			prefix = ", "
		}
		encodedName, _ := json.Marshal(name)
		if _, err := fmt.Fprintf(w, "%s%s: \"", prefix, encodedName); err != nil {
			return err
		}
		encoder := base64.NewEncoder(base64.StdEncoding, w)
		written, err := io.Copy(encoder, source)
		if err == nil {
			err = encoder.Close()
		}
		if err != nil {
			return err
		}
		summary.Files++
		summary.Bytes += written
		_, err = io.WriteString(w, "\"")
		return err
	})
	if err != nil {
		return nil, err
	}
	if _, err = io.WriteString(w, "}"); err != nil {
		return nil, err
	}
	if configuration != nil {
		if _, err = fmt.Fprintf(w, ", \"configuration\": %s", configuration); err != nil {
			return nil, err
		}
	}
	_, err = io.WriteString(w, "}")
	return summary, err
}

// pluginIDsWithConfiguration lists plugins with a stored configuration document.
func pluginIDsWithConfiguration() ([]string, error) {
	db, err := openPluginDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT key FROM plugin_kv WHERE plugin_id = ? AND key LIKE ?", hostPluginID, "%.configuration")
	if err != nil {
		return nil, fmt.Errorf("列出插件设置: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(key, ".configuration")
		// Other host keys end in ".configuration" too when the plugin ID does
		// ("full-trust/acme.configuration"); only a real plugin ID counts.
		if pluginIDPattern.MatchString(id) && id != hostPluginID {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// exportableConfigurationDocument returns the stored non-secret configuration
// document, or nil when there is none or it cannot be parsed.
func exportableConfigurationDocument(id string) (json.RawMessage, error) {
	configurationMu.Lock()
	document, readErr, err := loadConfigurationDocument(id)
	configurationMu.Unlock()
	if err != nil {
		return nil, err
	}
	if readErr != nil || (len(document.Global) == 0 && len(document.Workspaces) == 0) {
		return nil, nil
	}
	return json.Marshal(document)
}

func pluginHostConfigImportReadHandler(_, argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	source, err := absoluteDialogPath(args.Path)
	if err != nil {
		return "", err
	}
	config, stages, err := readConfigExportFile(source, false)
	if err != nil {
		return "", err
	}
	summaries := make([]pluginDataSummary, 0, len(stages))
	for _, stage := range stages {
		summaries = append(summaries, stage.summary())
	}
	return core.EncodeResult(map[string]any{"config": config, "plugins": summaries})
}

func pluginHostConfigImportPluginsHandler(_, argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	source, err := absoluteDialogPath(args.Path)
	if err != nil {
		return "", err
	}
	_, stages, err := readConfigExportFile(source, true)
	if err != nil {
		return "", err
	}
	defer cleanupStages(stages)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := host().applyPluginDataStages(ctx, stages)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

// readConfigExportFile streams a settings export: every top-level member except
// "plugins" is returned as-is for the frontend, and the plugin section is fully
// validated (and staged when write is true) before anything is applied.
func readConfigExportFile(source string, write bool) (config map[string]json.RawMessage, stages []*pluginDataStage, err error) {
	file, err := os.Open(source)
	if err != nil {
		return nil, nil, fmt.Errorf("无法读取配置文件: %w", err)
	}
	defer file.Close()
	defer func() {
		if err != nil {
			cleanupStages(stages)
			stages = nil
		}
	}()
	invalid := errors.New("配置文件格式无效")
	decoder := json.NewDecoder(bufio.NewReaderSize(file, 1<<20))
	if !expectDelim(decoder, '{') {
		return nil, nil, invalid
	}
	config = map[string]json.RawMessage{}
	for decoder.More() {
		key, ok := nextString(decoder)
		if !ok {
			return nil, nil, invalid
		}
		if key != "plugins" {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return nil, nil, invalid
			}
			config[key] = raw
			continue
		}
		if stages, err = readPluginsSection(decoder, write); err != nil {
			return nil, stages, err
		}
	}
	if !expectDelim(decoder, '}') {
		return nil, stages, invalid
	}
	return config, stages, nil
}

func readPluginsSection(decoder *json.Decoder, write bool) (stages []*pluginDataStage, err error) {
	invalid := errors.New("配置文件中的插件数据格式无效")
	if !expectDelim(decoder, '{') {
		return nil, invalid
	}
	seen := map[string]bool{}
	for decoder.More() {
		id, ok := nextString(decoder)
		if !ok {
			return stages, invalid
		}
		stage, err := newPluginDataStage(id, write)
		if err != nil {
			return stages, err
		}
		stages = append(stages, stage)
		if seen[stage.ID] {
			return stages, fmt.Errorf("插件数据中的插件 %q 重复", stage.ID)
		}
		seen[stage.ID] = true
		if len(stages) > pluginDataImportMaxPlugins {
			return stages, fmt.Errorf("插件数据最多包含 %d 个插件", pluginDataImportMaxPlugins)
		}
		if !expectDelim(decoder, '{') {
			return stages, invalid
		}
		for decoder.More() {
			field, ok := nextString(decoder)
			if !ok {
				return stages, invalid
			}
			switch field {
			case "kv":
				var values map[string]json.RawMessage
				if err := decoder.Decode(&values); err != nil {
					return stages, invalid
				}
				if err := stage.setKV(values); err != nil {
					return stages, err
				}
			case "configuration":
				var raw json.RawMessage
				if err := decoder.Decode(&raw); err != nil {
					return stages, invalid
				}
				if err := stage.setConfiguration(raw); err != nil {
					return stages, err
				}
			case "files":
				if !expectDelim(decoder, '{') {
					return stages, invalid
				}
				for decoder.More() {
					name, ok := nextString(decoder)
					if !ok {
						return stages, invalid
					}
					var encoded string
					if err := decoder.Decode(&encoded); err != nil {
						return stages, invalid
					}
					if err := stage.addFile(name, base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))); err != nil {
						return stages, err
					}
				}
				if !expectDelim(decoder, '}') {
					return stages, invalid
				}
			default:
				return stages, invalid
			}
		}
		if !expectDelim(decoder, '}') {
			return stages, invalid
		}
	}
	if !expectDelim(decoder, '}') {
		return stages, invalid
	}
	return stages, nil
}

func expectDelim(decoder *json.Decoder, want json.Delim) bool {
	token, err := decoder.Token()
	delim, ok := token.(json.Delim)
	return err == nil && ok && delim == want
}

func nextString(decoder *json.Decoder) (string, bool) {
	token, err := decoder.Token()
	value, ok := token.(string)
	return value, err == nil && ok
}
