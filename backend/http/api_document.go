package http

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"mincode/app/backend/core"
)

// API 文档（OpenAPI / Postman / Apifox / Apipost）导入导出单独放宽大小上限；
// 通用的 local_fs_*_selected_text_file 仍保持 5MB。路径来自原生文件对话框。
const apiDocumentMaxBytes = 200 * 1024 * 1024

func init() {
	core.Register("http_api_document_read", apiDocumentReadHandler)
	core.Register("http_api_document_write", apiDocumentWriteHandler)
}

type apiDocumentPathArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func apiDocumentReadHandler(_ string, argsJSON string) (string, error) {
	var args apiDocumentPathArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		return "", errors.New("path_empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("读取 API 文档信息: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not_a_file")
	}
	if info.Size() > apiDocumentMaxBytes {
		return "", errors.New("api_document_too_large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 API 文档: %w", err)
	}
	if !utf8.Valid(data) {
		return "", errors.New("invalid_api_document")
	}
	return core.EncodeResult(string(data))
}

func apiDocumentWriteHandler(_ string, argsJSON string) (string, error) {
	var args apiDocumentPathArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		return "", errors.New("path_empty")
	}
	if len(args.Content) > apiDocumentMaxBytes {
		return "", errors.New("api_document_too_large")
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return "", errors.New("path_is_directory")
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o600); err != nil {
		return "", fmt.Errorf("写入 API 文档: %w", err)
	}
	return core.EncodeResult(nil)
}
