package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/backup"
	"mincode/app/backend/core"
)

// Plugin data inside an encrypted backup (docs/superpowers/specs/
// 2026-10-29-data-backup-design.md 4.3, 4.5). Each plugin is stored under
// plugins/<id>/ as kv.json, files/<path>, configuration.json and, only when the
// user opted into secrets, secrets.json with plugin-facing secrets in
// plaintext. That plaintext exists only inside the host process: the backup
// file is encrypted with the backup password, and a restore reseals every
// secret with this device's machine key. The host namespace (enabled state,
// scope) and the reserved configuration secrets never travel.
const (
	pluginBackupPrefix        = "plugins/"
	pluginBackupKV            = "kv.json"
	pluginBackupFiles         = "files/"
	pluginBackupConfiguration = "configuration.json"
	pluginBackupSecrets       = "secrets.json"
	pluginRestoreConfigLabel  = "configuration"
)

// replacePluginDataHook is called right before each plugin's data is written
// during a restore. Tests set it to inject failures; it is nil in production.
var replacePluginDataHook func(id string) error

// rollbackPluginDataHook is called right before a plugin's data is put back
// after a failed restore. Tests set it to inject failures.
var rollbackPluginDataHook func(id string) error

// errPluginRestoreRollback marks a restore error whose rollback failed too:
// some plugin data may be left half restored.
var errPluginRestoreRollback = errors.New("回滚也失败")

// pluginBackupState is everything a backup carries for one plugin, read from
// the device. Secrets is nil unless secrets were requested; SecretKeys then
// lists every plugin-facing key, including ones this device cannot decrypt.
type pluginBackupState struct {
	pluginStoredData
	Contents   map[string][]byte
	Bytes      int64
	Secrets    map[string]string
	SecretKeys []string
}

func (state pluginBackupState) empty() bool {
	return state.pluginStoredData.empty() && len(state.Contents) == 0 && len(state.Secrets) == 0
}

func readPluginBackupState(id string, includeSecrets bool) (pluginBackupState, error) {
	data, err := readPluginDataForBackup(id)
	if err != nil {
		return pluginBackupState{}, err
	}
	state := pluginBackupState{pluginStoredData: data, Contents: map[string][]byte{}}
	err = walkPluginStorage(id, func(name string, source io.Reader) error {
		body, err := io.ReadAll(source)
		if err != nil {
			return err
		}
		state.Contents[name] = body
		state.Bytes += int64(len(body))
		return nil
	})
	if err != nil {
		return pluginBackupState{}, err
	}
	state.Files = make([]string, 0, len(state.Contents))
	for name := range state.Contents {
		state.Files = append(state.Files, name)
	}
	sort.Strings(state.Files)
	if includeSecrets {
		keys, err := pluginFacingSecretList(id)
		if err != nil {
			return pluginBackupState{}, err
		}
		state.SecretKeys = keys
		state.Secrets = map[string]string{}
		for _, key := range keys {
			value, found, err := pluginSecretGet(id, key)
			if errors.Is(err, errPluginSecretUnreadable) || (err == nil && !found) {
				// Sealed for another device or deleted meanwhile: nothing to carry.
				continue
			}
			if err != nil {
				return pluginBackupState{}, err
			}
			state.Secrets[key] = value
		}
	}
	return state, nil
}

// pluginIDsWithSecrets lists plugins with stored secrets, excluding the host.
func pluginIDsWithSecrets() ([]string, error) {
	db, err := openPluginDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT DISTINCT plugin_id FROM plugin_secrets WHERE plugin_id <> ?", hostPluginID)
	if err != nil {
		return nil, fmt.Errorf("列出插件密钥: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func pluginBackupIDs(includeSecrets bool) ([]string, error) {
	seen := map[string]bool{}
	lists := []func() ([]string, error){pluginIDsWithData, pluginIDsWithConfiguration}
	if includeSecrets {
		lists = append(lists, pluginIDsWithSecrets)
	}
	for _, list := range lists {
		ids, err := list()
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if _, err := pluginStorageRoot(id); err == nil && id != hostPluginID {
				seen[id] = true
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

// pluginTitle is the installed plugin's display name, or its id.
func (manager *pluginManager) pluginTitle(id string) string {
	if manager == nil {
		return id
	}
	record, err := manager.record(id)
	if err != nil {
		return id
	}
	record.mu.Lock()
	name := strings.TrimSpace(recordMetaUnlocked(record).Name)
	record.mu.Unlock()
	if name == "" {
		return id
	}
	return name
}

// collectPluginBackupEntries reads every plugin's data (installed or not) as
// backup entries, with one section summary per plugin, both sorted by id.
func (manager *pluginManager) collectPluginBackupEntries(includeSecrets bool) ([]backup.Entry, []backup.SectionSummary, error) {
	ids, err := pluginBackupIDs(includeSecrets)
	if err != nil {
		return nil, nil, err
	}
	entries := []backup.Entry{}
	sections := []backup.SectionSummary{}
	for _, id := range ids {
		state, err := readPluginBackupState(id, includeSecrets)
		if err != nil {
			return nil, nil, fmt.Errorf("读取插件 %q 的数据失败: %w", id, err)
		}
		if state.empty() {
			continue
		}
		pluginEntries, err := pluginBackupStateEntries(id, state)
		if err != nil {
			return nil, nil, fmt.Errorf("读取插件 %q 的数据失败: %w", id, err)
		}
		entries = append(entries, pluginEntries...)
		items := len(state.KV) + len(state.Files)
		if state.Configuration != nil {
			items++
		}
		sections = append(sections, backup.SectionSummary{ID: backup.PluginSectionID(id), Title: manager.pluginTitle(id),
			Items: items, Bytes: state.Bytes, Plugin: id})
	}
	return entries, sections, nil
}

func pluginBackupStateEntries(id string, state pluginBackupState) ([]backup.Entry, error) {
	prefix := pluginBackupPrefix + id + "/"
	kv := state.KV
	if kv == nil {
		kv = map[string]json.RawMessage{}
	}
	kvJSON, err := json.Marshal(kv)
	if err != nil {
		return nil, err
	}
	entries := []backup.Entry{{Path: prefix + pluginBackupKV, Data: kvJSON}}
	for _, name := range state.Files {
		entries = append(entries, backup.Entry{Path: prefix + pluginBackupFiles + name, Data: state.Contents[name]})
	}
	if state.Configuration != nil {
		entries = append(entries, backup.Entry{Path: prefix + pluginBackupConfiguration, Data: bytes.Clone(state.Configuration)})
	}
	if len(state.Secrets) > 0 {
		secretsJSON, err := json.Marshal(state.Secrets)
		if err != nil {
			return nil, err
		}
		entries = append(entries, backup.Entry{Path: prefix + pluginBackupSecrets, Data: secretsJSON})
	}
	return entries, nil
}

// pluginRestorePlan is one plugin's restore, computed against the data on
// this device. KV is the final namespace; Files holds the backup contents to
// write; Remove lists local-only files dropped in overwrite mode;
// Configuration is the final document (nil leaves it alone); Secrets are the
// backup's plaintext secrets (nil leaves the plugin's secrets alone).
type pluginRestorePlan struct {
	ID            string
	Mode          backup.Mode
	KV            map[string]json.RawMessage
	Files         map[string][]byte
	Remove        []string
	Configuration json.RawMessage
	Secrets       map[string]string
	Changes       []backup.Change
}

// planPluginRestore validates the backup entries under plugins/<id>/ (other
// entries are ignored) and merges them with the local data.
func planPluginRestore(rawID string, entries []backup.Entry, mode backup.Mode) (pluginRestorePlan, error) {
	if mode != backup.ModeOverwrite && mode != backup.ModeMerge {
		return pluginRestorePlan{}, fmt.Errorf("未知的恢复方式 %q", mode)
	}
	// A validating stage checks the id and every KV and file limit.
	stage, err := newPluginDataStage(rawID, false)
	if err != nil {
		return pluginRestorePlan{}, err
	}
	id := stage.ID
	prefix := pluginBackupPrefix + id + "/"
	files := map[string][]byte{}
	var secrets map[string]string
	seenKV := false
	for _, entry := range entries {
		rest, ok := strings.CutPrefix(entry.Path, prefix)
		if !ok {
			continue
		}
		switch {
		case rest == pluginBackupKV:
			if seenKV {
				return pluginRestorePlan{}, fmt.Errorf("插件 %q 的 KV 重复", id)
			}
			seenKV = true
			var values map[string]json.RawMessage
			if err := json.Unmarshal(entry.Data, &values); err != nil || values == nil {
				return pluginRestorePlan{}, fmt.Errorf("插件 %q 的 KV 格式无效", id)
			}
			if err := stage.setKV(values); err != nil {
				return pluginRestorePlan{}, err
			}
		case strings.HasPrefix(rest, pluginBackupFiles):
			name := strings.TrimPrefix(rest, pluginBackupFiles)
			if err := stage.addFile(name, bytes.NewReader(entry.Data)); err != nil {
				return pluginRestorePlan{}, err
			}
			files[name] = entry.Data
		case rest == pluginBackupConfiguration:
			if stage.Configuration != nil {
				return pluginRestorePlan{}, fmt.Errorf("插件 %q 的设置重复", id)
			}
			if err := stage.setConfiguration(entry.Data); err != nil {
				return pluginRestorePlan{}, err
			}
		case rest == pluginBackupSecrets:
			if secrets != nil {
				return pluginRestorePlan{}, fmt.Errorf("插件 %q 的密钥重复", id)
			}
			if secrets, err = parseRestoredSecrets(id, entry.Data); err != nil {
				return pluginRestorePlan{}, err
			}
		default:
			return pluginRestorePlan{}, fmt.Errorf("插件 %q 的备份条目 %q 无效", id, rest)
		}
	}

	local, err := readPluginBackupState(id, secrets != nil)
	if err != nil {
		return pluginRestorePlan{}, fmt.Errorf("读取插件 %q 的数据失败: %w", id, err)
	}
	plan := pluginRestorePlan{ID: id, Mode: mode, Files: files, Secrets: secrets}
	kv, changes := backup.MergeKV(local.KV, stage.KV, mode)
	plan.KV = kv
	plan.Changes = appendLabelled(plan.Changes, "kv/", changes)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	_, remove, changes := backup.MergeFileSet(local.Files, names, mode)
	plan.Remove = remove
	plan.Changes = appendLabelled(plan.Changes, pluginBackupFiles, changes)

	if stage.Configuration != nil {
		document, kind, err := mergeRestoredConfiguration(local.Configuration, stage.Configuration, mode)
		if err != nil {
			return pluginRestorePlan{}, fmt.Errorf("插件 %q 的设置无效: %w", id, err)
		}
		plan.Configuration = document
		if kind != "" {
			plan.Changes = append(plan.Changes, backup.Change{Kind: kind, Label: pluginRestoreConfigLabel})
		}
	}

	if secrets != nil {
		// Compared as JSON strings; labels carry keys only, never values.
		_, changes := backup.MergeKV(secretsAsKV(local.Secrets), secretsAsKV(secrets), mode)
		plan.Changes = appendLabelled(plan.Changes, "secrets/", changes)
	}
	return plan, nil
}

func appendLabelled(out []backup.Change, prefix string, changes []backup.Change) []backup.Change {
	for _, change := range changes {
		out = append(out, backup.Change{Kind: change.Kind, Label: prefix + change.Label})
	}
	return out
}

func secretsAsKV(secrets map[string]string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(secrets))
	for key, value := range secrets {
		encoded, _ := json.Marshal(value)
		out[key] = encoded
	}
	return out
}

// parseRestoredSecrets applies the host.secrets.set limits to secrets.json.
func parseRestoredSecrets(id string, data []byte) (map[string]string, error) {
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return nil, fmt.Errorf("插件 %q 的密钥格式无效", id)
	}
	if len(raw) > pluginSecretMaxKeys {
		return nil, fmt.Errorf("插件 %q 的密钥超过 %d 个", id, pluginSecretMaxKeys)
	}
	secrets := make(map[string]string, len(raw))
	for rawKey, value := range raw {
		_, key, err := validatePluginSecretKey(id, rawKey)
		if err != nil || key != rawKey || pluginSecretKeyReserved(key) {
			return nil, fmt.Errorf("插件 %q 的密钥名 %q 无效", id, rawKey)
		}
		if value == "" || len(value) > pluginSecretMaxValueBytes || !utf8.ValidString(value) {
			return nil, fmt.Errorf("插件 %q 的密钥 %q 无效", id, key)
		}
		secrets[key] = value
	}
	return secrets, nil
}

// mergeRestoredConfiguration returns the configuration document to store and
// the change kind ("" when nothing changes). Merge combines settings by key:
// global keys, and per workspace the keys of each workspace in the backup.
func mergeRestoredConfiguration(local, incoming json.RawMessage, mode backup.Mode) (json.RawMessage, string, error) {
	in, err := parseConfigurationDocument(incoming)
	if err != nil {
		return nil, "", err
	}
	kind := backup.ChangeUpdate
	if local == nil {
		kind = backup.ChangeAdd
	} else if mode == backup.ModeMerge {
		current, err := parseConfigurationDocument(local)
		if err != nil {
			return nil, "", err
		}
		for key, value := range in.Global {
			current.Global[key] = value
		}
		for workspaceID, values := range in.Workspaces {
			target := current.Workspaces[workspaceID]
			if target == nil {
				target = map[string]json.RawMessage{}
				current.Workspaces[workspaceID] = target
			}
			for key, value := range values {
				target[key] = value
			}
		}
		in = current
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil, "", err
	}
	if local != nil {
		if canonical, err := canonicalConfiguration(local); err == nil && bytes.Equal(canonical, encoded) {
			kind = ""
		}
	}
	return encoded, kind, nil
}

func canonicalConfiguration(raw json.RawMessage) ([]byte, error) {
	document, err := parseConfigurationDocument(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(document)
}

// pluginRestoreSnapshot is a plugin's data before the restore, kept in
// memory to roll back. Secrets are captured only when the plan writes them.
type pluginRestoreSnapshot struct {
	state              pluginBackupState
	configurationRaw   json.RawMessage
	configurationFound bool
}

func takePluginRestoreSnapshot(plan pluginRestorePlan) (pluginRestoreSnapshot, error) {
	state, err := readPluginBackupState(plan.ID, plan.Secrets != nil)
	if err != nil {
		return pluginRestoreSnapshot{}, err
	}
	snapshot := pluginRestoreSnapshot{state: state}
	if plan.Configuration != nil {
		configurationMu.Lock()
		snapshot.configurationRaw, snapshot.configurationFound, err = pluginKVGetRaw(hostPluginID, configurationKVKey(plan.ID))
		configurationMu.Unlock()
		if err != nil {
			return pluginRestoreSnapshot{}, err
		}
	}
	return snapshot, nil
}

// buildPluginRestoreStage stages the final file tree and validates the final
// KV namespace and configuration with the import limits.
func buildPluginRestoreStage(id string, mode backup.Mode, kv map[string]json.RawMessage, files map[string][]byte, remove []string, local map[string][]byte, configuration json.RawMessage) (stage *pluginDataStage, err error) {
	stage, err = newPluginDataStage(id, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			stage.cleanup()
			stage = nil
		}
	}()
	if err := stage.setKV(kv); err != nil {
		return stage, err
	}
	if configuration != nil {
		if err := stage.setConfiguration(configuration); err != nil {
			return stage, err
		}
	}
	tree := map[string][]byte{}
	if mode != backup.ModeOverwrite {
		for name, body := range local {
			tree[name] = body
		}
		for _, name := range remove {
			delete(tree, name)
		}
	}
	for name, body := range files {
		tree[name] = body
	}
	names := make([]string, 0, len(tree))
	for name := range tree {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := stage.addFile(name, bytes.NewReader(tree[name])); err != nil {
			return stage, err
		}
	}
	if err := stage.root.Close(); err != nil {
		return stage, err
	}
	stage.root = nil
	return stage, nil
}

type pluginRestoreWork struct {
	plan     pluginRestorePlan
	snapshot pluginRestoreSnapshot
	stage    *pluginDataStage
}

// applyPluginRestore writes each plan, plugin by plugin, in plan order. The
// plan for deferID (the data-backup plugin running the restore) is skipped;
// the caller applies it with applyDeferredPluginRestore afterwards. Installed
// plugins are stopped for the write and restarted if they were running;
// plugins that are not installed get their data too and are listed in
// notInstalled. On any failure every plugin already written is put back to
// its state before the call and the error names the failing plugin.
func (manager *pluginManager) applyPluginRestore(ctx context.Context, plans []pluginRestorePlan, deferID string) (applied []string, notInstalled []string, err error) {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	return manager.applyPluginRestoreLocked(ctx, plans, deferID)
}

// applyDeferredPluginRestore stops the plugin, writes its plan and restarts
// it, rolling its data back if the write fails.
func (manager *pluginManager) applyDeferredPluginRestore(ctx context.Context, plan pluginRestorePlan) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	_, _, err := manager.applyPluginRestoreLocked(ctx, []pluginRestorePlan{plan}, "")
	return err
}

func (manager *pluginManager) applyPluginRestoreLocked(ctx context.Context, plans []pluginRestorePlan, deferID string) ([]string, []string, error) {
	var work []*pluginRestoreWork
	defer func() {
		for _, item := range work {
			if item.stage != nil {
				item.stage.cleanup()
			}
		}
	}()
	seen := map[string]bool{}
	for _, plan := range plans {
		if deferID != "" && plan.ID == deferID {
			continue
		}
		if seen[plan.ID] {
			return nil, nil, fmt.Errorf("插件 %q 重复", plan.ID)
		}
		seen[plan.ID] = true
		snapshot, err := takePluginRestoreSnapshot(plan)
		if err != nil {
			return nil, nil, fmt.Errorf("读取插件 %q 的数据失败: %w", plan.ID, err)
		}
		item := &pluginRestoreWork{plan: plan, snapshot: snapshot}
		work = append(work, item)
		item.stage, err = buildPluginRestoreStage(plan.ID, plan.Mode, plan.KV, plan.Files, plan.Remove, snapshot.state.Contents, plan.Configuration)
		if err != nil {
			return nil, nil, fmt.Errorf("恢复插件 %q 的数据失败: %w", plan.ID, err)
		}
	}

	applied, notInstalled := []string{}, []string{}
	var touched []*pluginRestoreWork
	for _, item := range work {
		plan := item.plan
		installed, err := manager.withPluginStopped(ctx, plan.ID, "plugin data restored", func() error {
			if replacePluginDataHook != nil {
				if err := replacePluginDataHook(plan.ID); err != nil {
					return err
				}
			}
			touched = append(touched, item)
			return writePluginRestore(plan.ID, item.stage, plan.Configuration, false, plan.Secrets, nil, plan.Mode == backup.ModeOverwrite)
		})
		if err != nil {
			err = fmt.Errorf("恢复插件 %q 的数据失败: %w", plan.ID, err)
			if rollbackErr := manager.rollbackPluginRestore(context.WithoutCancel(ctx), touched); rollbackErr != nil {
				return []string{}, []string{}, fmt.Errorf("%w；%w: %v", err, errPluginRestoreRollback, rollbackErr)
			}
			return []string{}, []string{}, err
		}
		applied = append(applied, plan.ID)
		if !installed {
			notInstalled = append(notInstalled, plan.ID)
		}
	}
	return applied, notInstalled, nil
}

// rollbackPluginRestore puts every touched plugin back to its snapshot, newest
// first, and reports every plugin it could not restore.
func (manager *pluginManager) rollbackPluginRestore(ctx context.Context, touched []*pluginRestoreWork) error {
	var failures []error
	for i := len(touched) - 1; i >= 0; i-- {
		item := touched[i]
		id, snapshot := item.plan.ID, item.snapshot
		stage, err := buildPluginRestoreStage(id, backup.ModeOverwrite, snapshot.state.KV, snapshot.state.Contents, nil, nil, nil)
		if err != nil {
			failures = append(failures, fmt.Errorf("插件 %q: %w", id, err))
			continue
		}
		var configuration json.RawMessage
		deleteConfiguration := false
		if item.plan.Configuration != nil {
			if snapshot.configurationFound {
				configuration = snapshot.configurationRaw
			} else {
				deleteConfiguration = true
			}
		}
		var secrets map[string]string
		if item.plan.Secrets != nil {
			secrets = snapshot.state.Secrets
		}
		_, err = manager.withPluginStopped(ctx, id, "plugin data restore rolled back", func() error {
			if rollbackPluginDataHook != nil {
				if err := rollbackPluginDataHook(id); err != nil {
					return err
				}
			}
			return writePluginRestore(id, stage, configuration, deleteConfiguration, secrets, snapshot.state.SecretKeys, true)
		})
		stage.cleanup()
		if err != nil {
			failures = append(failures, fmt.Errorf("插件 %q: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

// withPluginStopped runs write with the plugin stopped, as
// applyPluginDataStages does, and restarts it if it was running. A plugin
// that is not installed is written directly and reported as not installed.
func (manager *pluginManager) withPluginStopped(ctx context.Context, id, logMessage string, write func() error) (installed bool, err error) {
	record, recordErr := manager.record(id)
	if recordErr != nil {
		return false, write()
	}
	record.mu.Lock()
	running := record.state == pluginStateRunning
	record.mu.Unlock()
	if err := manager.stopRecord(ctx, record, false); err != nil {
		return true, fmt.Errorf("停止插件 %q 失败: %w", id, err)
	}
	writeErr := write()
	if running {
		if err := manager.startRecord(ctx, record); err != nil {
			record.appendLog("error", "restart after data restore failed: "+err.Error())
		}
	}
	if writeErr == nil {
		record.appendLog("info", logMessage)
		manager.emitState(record)
	}
	return true, writeErr
}

// writePluginRestore swaps in the staged KV and file tree, then writes the
// configuration document (nil and !deleteConfiguration leaves it) and the
// secrets (nil leaves them). With replaceSecrets, plugin-facing keys absent
// from secrets are deleted, except those listed in keepSecretKeys.
func writePluginRestore(id string, stage *pluginDataStage, configuration json.RawMessage, deleteConfiguration bool, secrets map[string]string, keepSecretKeys []string, replaceSecrets bool) error {
	if err := replacePluginData(id, stage.KV, stage.dir, stage.Files > 0); err != nil {
		return err
	}
	if configuration != nil || deleteConfiguration {
		configurationMu.Lock()
		var err error
		if deleteConfiguration {
			err = pluginKVDeleteRaw(hostPluginID, configurationKVKey(id))
		} else {
			err = pluginKVSetRaw(hostPluginID, configurationKVKey(id), configuration)
		}
		configurationMu.Unlock()
		if err != nil {
			return fmt.Errorf("写入插件设置失败: %w", err)
		}
		core.Emit(pluginConfigurationChangedEvent, map[string]any{"pluginId": id, "keys": []string{}})
	}
	if secrets == nil {
		return nil
	}
	if replaceSecrets {
		keep := map[string]bool{}
		for _, key := range keepSecretKeys {
			keep[key] = true
		}
		current, err := pluginFacingSecretList(id)
		if err != nil {
			return err
		}
		for _, key := range current {
			if _, ok := secrets[key]; !ok && !keep[key] {
				if err := pluginSecretDelete(id, key); err != nil {
					return err
				}
			}
		}
	}
	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := pluginSecretSet(id, key, secrets[key]); err != nil {
			return fmt.Errorf("写入插件密钥 %q 失败: %w", key, err)
		}
	}
	return nil
}
