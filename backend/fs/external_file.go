package fs

import (
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/core"
)

const maxExternalPreviewBytes = 20 * 1024 * 1024

type externalFileInfo struct {
	Path                 string `json:"path"`
	Name                 string `json:"name"`
	Kind                 string `json:"kind"`
	MimeType             string `json:"mimeType,omitempty"`
	Size                 int64  `json:"size"`
	TextPreviewAvailable bool   `json:"textPreviewAvailable,omitempty"`
}

type externalFilePathArgs struct {
	Path string `json:"path"`
}

func init() {
	core.Register("local_fs_inspect_external_file", handleInspectExternalFile)
	core.Register("local_fs_read_external_text_file", handleReadExternalTextFile)
	core.Register("local_fs_read_external_data_url", handleReadExternalDataURL)
	core.Register("local_fs_write_external_text_file", handleWriteExternalTextFile)
	core.Register("local_fs_open_external_file", handleOpenExternalFile)
}

func handleInspectExternalFile(_, argsJSON string) (string, error) {
	var args externalFilePathArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	path, info, err := externalPath(args.Path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		root, err := canonicalLocalProjectRoot(path)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(externalFileInfo{Path: root, Name: filepath.Base(root), Kind: "directory"})
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not_a_file")
	}
	kind, mimeType, err := inspectExternalFile(path, info)
	if err != nil {
		return "", err
	}
	textPreview := false
	if kind == "binary" && info.Size() > maxLocalFsReadBytes {
		textPreview, err = externalTextPreviewAvailable(path)
		if err != nil {
			return "", err
		}
	}
	return core.EncodeResult(externalFileInfo{
		Path:                 path,
		Name:                 filepath.Base(path),
		Kind:                 kind,
		MimeType:             mimeType,
		Size:                 info.Size(),
		TextPreviewAvailable: textPreview,
	})
}

func handleReadExternalTextFile(_, argsJSON string) (string, error) {
	path, info, err := decodeExternalRegularFile(argsJSON)
	if err != nil {
		return "", err
	}
	if info.Size() > maxLocalFsReadBytes {
		return "", fmt.Errorf("file_too_large: %d bytes (max %d)", info.Size(), maxLocalFsReadBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	if !utf8.Valid(data) || hasBinaryNull(data) {
		return "", fmt.Errorf("not_a_text_file")
	}
	return core.EncodeResult(string(data))
}

func handleReadExternalDataURL(_, argsJSON string) (string, error) {
	path, info, err := decodeExternalRegularFile(argsJSON)
	if err != nil {
		return "", err
	}
	if info.Size() > maxExternalPreviewBytes {
		return "", fmt.Errorf("file_too_large: %d bytes (max %d)", info.Size(), maxExternalPreviewBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return core.EncodeResult("data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data))
}

func handleWriteExternalTextFile(_, argsJSON string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	path, info, err := externalRegularFile(args.Path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxLocalFsWriteBytes || int64(len(args.Content)) > maxLocalFsWriteBytes {
		return "", fmt.Errorf("content_too_large: %d bytes (max %d)", len(args.Content), maxLocalFsWriteBytes)
	}
	if err := os.WriteFile(path, []byte(args.Content), info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return "", nil
}

func handleOpenExternalFile(_, argsJSON string) (string, error) {
	path, _, err := decodeExternalRegularFile(argsJSON)
	if err != nil {
		return "", err
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", path)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		command = exec.Command("xdg-open", path)
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("open_external_file: %w", err)
	}
	return "", nil
}

func decodeExternalRegularFile(argsJSON string) (string, os.FileInfo, error) {
	var args externalFilePathArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", nil, err
	}
	return externalRegularFile(args.Path)
}

func externalRegularFile(rawPath string) (string, os.FileInfo, error) {
	path, info, err := externalPath(rawPath)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("not_a_file")
	}
	return path, info, nil
}

func externalPath(rawPath string) (string, os.FileInfo, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", nil, fmt.Errorf("path_empty")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", nil, fmt.Errorf("path_resolve_failed: %w", err)
	}
	absolutePath = filepath.Clean(absolutePath)
	info, err := os.Stat(absolutePath)
	if err != nil {
		return "", nil, fmt.Errorf("stat: %w", err)
	}
	return absolutePath, info, nil
}

func inspectExternalFile(path string, info os.FileInfo) (string, string, error) {
	extension := strings.ToLower(filepath.Ext(path))
	mimeType := mime.TypeByExtension(extension)
	if info.Size() > maxExternalPreviewBytes && (extension == ".pdf" || strings.HasPrefix(mimeType, "image/")) {
		return "binary", mimeType, nil
	}
	if extension == ".pdf" {
		return "pdf", "application/pdf", nil
	}
	if strings.HasPrefix(mimeType, "image/") {
		return "image", mimeType, nil
	}
	if info.Size() > maxLocalFsReadBytes {
		return "binary", mimeType, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read: %w", err)
	}
	if utf8.Valid(data) && !hasBinaryNull(data) {
		if mimeType == "" {
			mimeType = "text/plain"
		}
		return "text", mimeType, nil
	}
	return "binary", mimeType, nil
}

func hasBinaryNull(data []byte) bool {
	limit := len(data)
	if limit > 8*1024 {
		limit = 8 * 1024
	}
	for _, value := range data[:limit] {
		if value == 0 {
			return true
		}
	}
	return false
}
