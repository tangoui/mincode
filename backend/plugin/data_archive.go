package plugin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"mincode/app/backend/core"
)

// A plugin data archive is a zip with one plugin's KV namespace (kv.json) and
// file storage (files/...), exported and imported from the plugin list.
const (
	pluginDataArchiveFormat        = "mincode-plugin-data"
	pluginDataArchiveVersion       = 1
	pluginDataArchiveManifestName  = "manifest.json"
	pluginDataArchiveKV            = "kv.json"
	pluginDataArchiveConfiguration = "configuration.json"
	pluginDataArchiveFilesPrefix   = "files/"
	pluginDataArchiveMaxManifest   = 64 * 1024
	pluginDataArchiveMaxKV         = 256 * 1024 * 1024
	pluginStorageTempPrefix        = ".storage-"
)

type pluginDataArchiveManifest struct {
	Format        string `json:"format"`
	Version       int    `json:"version"`
	PluginID      string `json:"pluginId"`
	PluginName    string `json:"pluginName,omitempty"`
	PluginVersion string `json:"pluginVersion,omitempty"`
	ExportedAt    string `json:"exportedAt"`
	Keys          int    `json:"keys"`
	Files         int    `json:"files"`
	FileBytes     int64  `json:"fileBytes"`
}

func init() {
	core.Register("plugin_host_data_export_archive", pluginHostDataExportArchiveHandler)
	core.Register("plugin_host_data_inspect_archive", pluginHostDataInspectArchiveHandler)
	core.Register("plugin_host_data_import_archive", pluginHostDataImportArchiveHandler)
}

type pluginDataArchiveArgs struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

func decodeArchiveArgs(argsJSON string) (pluginDataArchiveArgs, error) {
	var args pluginDataArchiveArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return args, err
	}
	var err error
	args.Path, err = absoluteDialogPath(args.Path)
	return args, err
}

func pluginHostDataExportArchiveHandler(_, argsJSON string) (string, error) {
	args, err := decodeArchiveArgs(argsJSON)
	if err != nil {
		return "", err
	}
	manifest, err := host().ExportPluginDataArchive(args.ID, args.Path)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(manifest)
}

func pluginHostDataInspectArchiveHandler(_, argsJSON string) (string, error) {
	args, err := decodeArchiveArgs(argsJSON)
	if err != nil {
		return "", err
	}
	archive, err := zip.OpenReader(args.Path)
	if err != nil {
		return "", errArchiveUnreadable
	}
	defer archive.Close()
	manifest, err := readPluginDataArchiveManifest(&archive.Reader)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(manifest)
}

func pluginHostDataImportArchiveHandler(_, argsJSON string) (string, error) {
	args, err := decodeArchiveArgs(argsJSON)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	manifest, err := host().ImportPluginDataArchive(ctx, args.ID, args.Path)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(manifest)
}

var (
	errArchiveUnreadable = errors.New("无法读取插件数据文件，请确认它是 MinCode 导出的 zip 文件")
	errArchiveInvalid    = errors.New("这不是 MinCode 导出的插件数据文件")
)

// ExportPluginDataArchive writes an installed plugin's data. The plugin keeps
// running: KV is read in one query and files under the storage lock.
func (manager *pluginManager) ExportPluginDataArchive(id, target string) (pluginDataArchiveManifest, error) {
	record, err := manager.record(id)
	if err != nil {
		return pluginDataArchiveManifest{}, err
	}
	manifest := pluginDataArchiveManifest{Format: pluginDataArchiveFormat, Version: pluginDataArchiveVersion,
		PluginID: record.id, ExportedAt: time.Now().UTC().Format(time.RFC3339)}
	record.mu.Lock()
	if record.manifest != nil {
		manifest.PluginName, manifest.PluginVersion = record.manifest.Name, record.manifest.Version
	}
	record.mu.Unlock()
	kv, err := pluginKVNamespaceRepository(record.id)
	if err != nil {
		return pluginDataArchiveManifest{}, err
	}
	kvJSON, err := json.MarshalIndent(kv, "", "  ")
	if err != nil {
		return pluginDataArchiveManifest{}, err
	}
	manifest.Keys = len(kv)
	configuration, err := exportableConfigurationDocument(record.id)
	if err != nil {
		return pluginDataArchiveManifest{}, err
	}
	err = writeAtomically(target, func(w io.Writer) error {
		writer := zip.NewWriter(w)
		entry, err := writer.Create(pluginDataArchiveKV)
		if err == nil {
			_, err = entry.Write(kvJSON)
		}
		if err == nil && configuration != nil {
			if entry, err = writer.Create(pluginDataArchiveConfiguration); err == nil {
				_, err = entry.Write(configuration)
			}
		}
		if err == nil {
			err = walkPluginStorage(record.id, func(name string, source io.Reader) error {
				entry, err := writer.Create(pluginDataArchiveFilesPrefix + name)
				if err != nil {
					return err
				}
				written, err := io.Copy(entry, source)
				manifest.Files++
				manifest.FileBytes += written
				return err
			})
		}
		if err == nil {
			var encoded []byte
			if encoded, err = json.MarshalIndent(manifest, "", "  "); err == nil {
				if entry, err = writer.Create(pluginDataArchiveManifestName); err == nil {
					_, err = entry.Write(encoded)
				}
			}
		}
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		return err
	})
	if err != nil {
		return pluginDataArchiveManifest{}, fmt.Errorf("导出插件数据失败: %w", err)
	}
	return manifest, nil
}

func readArchiveEntry(file *zip.File, max int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(max) {
		return nil, fmt.Errorf("插件数据文件中的 %s 过大", file.Name)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("插件数据文件中的 %s 过大", file.Name)
	}
	return data, nil
}

func readPluginDataArchiveManifest(archive *zip.Reader) (pluginDataArchiveManifest, error) {
	for _, file := range archive.File {
		if file.Name != pluginDataArchiveManifestName {
			continue
		}
		raw, err := readArchiveEntry(file, pluginDataArchiveMaxManifest)
		if err != nil {
			return pluginDataArchiveManifest{}, err
		}
		var manifest pluginDataArchiveManifest
		if json.Unmarshal(raw, &manifest) != nil || manifest.Format != pluginDataArchiveFormat {
			return pluginDataArchiveManifest{}, errArchiveInvalid
		}
		if manifest.Version != pluginDataArchiveVersion {
			return pluginDataArchiveManifest{}, errors.New("插件数据文件的版本不受当前 MinCode 支持，请升级后再导入")
		}
		if _, err := validatePluginID(manifest.PluginID); err != nil || manifest.PluginID == hostPluginID {
			return pluginDataArchiveManifest{}, errArchiveInvalid
		}
		return manifest, nil
	}
	return pluginDataArchiveManifest{}, errArchiveInvalid
}

// ImportPluginDataArchive replaces one installed plugin's KV namespace and file
// storage with the archive's content, after the whole archive is validated and
// staged. Plugin secrets are left as they are.
func (manager *pluginManager) ImportPluginDataArchive(ctx context.Context, id, source string) (pluginDataArchiveManifest, error) {
	record, err := manager.record(id)
	if err != nil {
		return pluginDataArchiveManifest{}, err
	}
	archive, err := zip.OpenReader(source)
	if err != nil {
		return pluginDataArchiveManifest{}, errArchiveUnreadable
	}
	defer archive.Close()
	manifest, err := readPluginDataArchiveManifest(&archive.Reader)
	if err != nil {
		return pluginDataArchiveManifest{}, err
	}
	if manifest.PluginID != record.id {
		return pluginDataArchiveManifest{}, fmt.Errorf("该文件是插件 %q 的数据，不能导入到 %q", manifest.PluginID, record.id)
	}
	stage, err := newPluginDataStage(record.id, true)
	if err != nil {
		return pluginDataArchiveManifest{}, err
	}
	defer stage.cleanup()
	if err := unpackPluginDataArchive(&archive.Reader, stage); err != nil {
		return pluginDataArchiveManifest{}, err
	}
	if _, err := manager.applyPluginDataStages(ctx, []*pluginDataStage{stage}); err != nil {
		return pluginDataArchiveManifest{}, err
	}
	manifest.Keys, manifest.Files, manifest.FileBytes = len(stage.KV), stage.Files, stage.Bytes
	return manifest, nil
}

func unpackPluginDataArchive(archive *zip.Reader, stage *pluginDataStage) error {
	seen := map[string]bool{}
	for _, file := range archive.File {
		name := file.Name
		if seen[name] {
			return fmt.Errorf("插件数据文件中的 %s 重复", name)
		}
		seen[name] = true
		switch {
		case name == pluginDataArchiveManifestName:
		case name == pluginDataArchiveKV:
			raw, err := readArchiveEntry(file, pluginDataArchiveMaxKV)
			if err != nil {
				return err
			}
			var values map[string]json.RawMessage
			if err := json.Unmarshal(raw, &values); err != nil {
				return errors.New("插件数据文件中的 kv.json 无效")
			}
			if err := stage.setKV(values); err != nil {
				return err
			}
		case name == pluginDataArchiveConfiguration:
			raw, err := readArchiveEntry(file, int64(configurationValuesMax))
			if err != nil {
				return err
			}
			if err := stage.setConfiguration(raw); err != nil {
				return err
			}
		case strings.HasPrefix(name, pluginDataArchiveFilesPrefix):
			if file.FileInfo().IsDir() {
				continue
			}
			if !file.Mode().IsRegular() {
				return fmt.Errorf("插件数据文件中的 %q 不是普通文件", name)
			}
			reader, err := file.Open()
			if err != nil {
				return err
			}
			err = stage.addFile(strings.TrimPrefix(name, pluginDataArchiveFilesPrefix), reader)
			reader.Close()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("插件数据文件包含未知内容 %q", name)
		}
	}
	return nil
}
