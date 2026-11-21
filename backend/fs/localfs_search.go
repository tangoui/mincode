package fs

import (
	"mincode/app/backend/core"

	"fmt"
	"os"
	"path/filepath"
)

func init() {
	core.Register("local_fs_move_to", handleMoveTo)
}

// ---------- 目录跳过策略（与 Rust 对齐） ----------

// searchSkipDirNames 同时用于 Go 遍历与 ripgrep 的 --glob 排除，保持两个引擎一致。
func sampleLooksLikeBinaryForPreview(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	s := sample
	if len(s) >= 3 && s[0] == 0xEF && s[1] == 0xBB && s[2] == 0xBF {
		s = s[3:]
	}
	if len(s) == 0 {
		return false
	}
	// UTF-16 LE/BE BOM：放行给编辑器。
	if len(s) >= 2 && ((s[0] == 0xFF && s[1] == 0xFE) || (s[0] == 0xFE && s[1] == 0xFF)) {
		return false
	}
	for _, b := range s {
		if b == 0 {
			return true
		}
	}
	return fingerprintBinaryMagic(s)
}

func fingerprintBinaryMagic(s []byte) bool {
	has := func(prefix []byte) bool {
		return len(s) >= len(prefix) && string(s[:len(prefix)]) == string(prefix)
	}
	// 图片
	if has([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return true
	}
	if len(s) >= 3 && s[0] == 0xFF && s[1] == 0xD8 && s[2] == 0xFF {
		return true
	}
	if has([]byte("GIF87a")) || has([]byte("GIF89a")) {
		return true
	}
	if has([]byte("RIFF")) && len(s) >= 12 {
		return true
	}
	if has([]byte("BM")) && len(s) >= 14 {
		return true
	}
	if len(s) >= 4 && ((s[0] == 0 && s[1] == 0 && s[2] == 1 && s[3] == 0) ||
		(s[0] == 0 && s[1] == 0 && s[2] == 2 && s[3] == 0)) {
		return true
	}
	// 压缩包
	if has([]byte{'P', 'K', 0x03, 0x04}) || has([]byte{'P', 'K', 0x05, 0x06}) || has([]byte{'P', 'K', 0x07, 0x08}) {
		return true
	}
	if has([]byte{0x1f, 0x8b}) {
		return true
	}
	if has([]byte{0x28, 0xB5, 0x2F, 0xFD}) {
		return true
	}
	if has([]byte{'R', 'a', 'r', '!', 0x1a, 0x07, 0x00}) || has([]byte{'R', 'a', 'r', '!', 0x1a, 0x07, 0x01}) {
		return true
	}
	if has([]byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}) {
		return true
	}
	// 文档
	if has([]byte("%PDF-")) {
		return true
	}
	if has([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) {
		return true
	}
	// 可执行 / 目标文件
	if has([]byte("MZ")) {
		return true
	}
	if has([]byte{0x7F, 'E', 'L', 'F'}) {
		return true
	}
	if has([]byte{0xFE, 0xED, 0xFA, 0xCE}) || has([]byte{0xCE, 0xFA, 0xED, 0xFE}) ||
		has([]byte{0xFE, 0xED, 0xFA, 0xCF}) || has([]byte{0xCF, 0xFA, 0xED, 0xFE}) {
		return true
	}
	if has([]byte{0xCA, 0xFE, 0xBA, 0xBE}) {
		return true
	}
	if has([]byte{0x00, 'a', 's', 'm'}) {
		return true
	}
	// 数据库
	if has([]byte("SQLite format 3\x00")) {
		return true
	}
	// 音视频
	if has([]byte("OggS")) {
		return true
	}
	if has([]byte("fLaC")) {
		return true
	}
	if has([]byte("ID3")) {
		return true
	}
	if len(s) >= 2 && s[0] == 0xFF && (s[1]&0xE0) == 0xE0 {
		return true
	}
	if has([]byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return true
	}
	return false
}

// ---------- 命令处理器 ----------

func handleMoveTo(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot string `json:"projectRoot"`
		SrcPath     string `json:"srcPath"`
		DestDir     string `json:"destDir"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	// 移动作用于条目自身（symlink 移动链接本身）。
	srcEntry, err := resolveEntryUnderRoot(root, a.SrcPath)
	if err != nil {
		return "", err
	}
	if srcEntry == root {
		return "", fmt.Errorf("cannot_move_project_root")
	}
	destDir, err := resolveUnderRoot(root, a.DestDir)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(destDir); statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("dest_not_a_directory")
	}
	dest := filepath.Join(destDir, filepath.Base(srcEntry))
	if !withinRoot(root, dest) {
		return "", fmt.Errorf("path_escapes_root")
	}
	if dest == srcEntry {
		return "", nil
	}
	if withinRoot(srcEntry, destDir) {
		return "", fmt.Errorf("dest_inside_source")
	}
	if _, statErr := os.Lstat(dest); statErr == nil {
		return "", fmt.Errorf("already_exists")
	}
	if err := os.Rename(srcEntry, dest); err != nil {
		return "", fmt.Errorf("move: %w", err)
	}
	return "", nil
}

// ---------- 内部辅助 ----------
