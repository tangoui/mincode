// Clipboard 文件通道：读写系统剪贴板中的文件列表（文件 URL / HDROP）。
// 与 Finder / 资源管理器互通：应用内复制的文件可在系统文件管理器中粘贴，反之亦然。
//
// 平台实现：
//
//	macOS   => NSPasteboard 文件 URL（clipboard_files_darwin.go）
//	Windows => PowerShell Set-Clipboard -LiteralPath（路径经 stdin 传入）/ Get-Clipboard -Format FileDropList
//	Linux   => xclip text/uri-list
//
// 命令契约：
//
//	clipboard_write_file_urls {paths: []string} -> void
//	clipboard_read_file_urls                    -> []string
package misc

import (
	"mincode/app/backend/core"

	"fmt"
	"path/filepath"
)

func init() {
	core.Register("clipboard_write_file_urls", clipboardWriteFileUrlsHandler)
	core.Register("clipboard_read_file_urls", clipboardReadFileUrlsHandler)
}

const clipboardFileLimit = 200

type clipboardFilesArg struct {
	Paths []string `json:"paths"`
}

func clipboardWriteFileUrlsHandler(_, argsJson string) (string, error) {
	var a clipboardFilesArg
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(a.Paths))
	for _, raw := range a.Paths {
		clean := filepath.Clean(raw)
		if clean == "" || clean == "." {
			continue
		}
		if !filepath.IsAbs(clean) {
			abs, err := filepath.Abs(clean)
			if err != nil {
				return "", fmt.Errorf("clipboard_write_file_urls: %w", err)
			}
			clean = abs
		}
		paths = append(paths, clean)
		if len(paths) > clipboardFileLimit {
			return "", fmt.Errorf("clipboard_write_file_urls: too_many_paths")
		}
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("clipboard_write_file_urls: empty_paths")
	}
	if err := clipboardWriteFiles(paths); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func clipboardReadFileUrlsHandler(_, _ string) (string, error) {
	paths, err := clipboardReadFiles()
	if err != nil {
		return "", err
	}
	return core.EncodeResult(paths)
}
