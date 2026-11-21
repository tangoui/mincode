package sftp

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"

	"strings"

	"mincode/app/backend/core"
)

const maxImagePreviewSize = 4 * 1024 * 1024

type imagePreviewResult struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func handleSftpReadImagePreview(_ string, argsJSON string) (string, error) {
	var args struct {
		SessionID string `json:"sessionId"`
		Path      string `json:"path"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Path) == "" || strings.ContainsAny(args.Path, "\x00\r\n") {
		return "", fmt.Errorf("远程图片路径无效")
	}
	st, release, err := acquireSession(args.SessionID)
	if err != nil {
		return "", err
	}
	defer release()

	result, err := readImagePreview(st.client, args.Path)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func readImagePreview(client remoteClient, remotePath string) (imagePreviewResult, error) {
	info, err := client.Lstat(remotePath)
	if err != nil {
		return imagePreviewResult{}, fmt.Errorf("检查远程图片失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return imagePreviewResult{}, fmt.Errorf("只能预览普通文件，不支持符号链接或目录")
	}
	if info.Size() < 0 || info.Size() > maxImagePreviewSize {
		return imagePreviewResult{}, fmt.Errorf("图片超过 4 MiB 预览限制或文件大小无效，请下载查看")
	}
	file, err := client.Open(remotePath)
	if err != nil {
		return imagePreviewResult{}, fmt.Errorf("打开远程图片失败: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxImagePreviewSize+1))
	if err != nil {
		return imagePreviewResult{}, fmt.Errorf("读取远程图片失败: %w", err)
	}
	if len(data) > maxImagePreviewSize {
		return imagePreviewResult{}, fmt.Errorf("图片超过 4 MiB 预览限制，请下载查看")
	}
	mimeType := imagePreviewMIME(data)
	if mimeType == "" {
		return imagePreviewResult{}, fmt.Errorf("不支持此图片格式，仅支持 PNG、JPEG、GIF 和 WebP")
	}
	return imagePreviewResult{MimeType: mimeType, Data: base64.StdEncoding.EncodeToString(data)}, nil
}

func imagePreviewMIME(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}
