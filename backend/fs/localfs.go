// local_fs 命令的 Go 实现，功能与 src-tauri/src/local_fs.rs 中的核心命令一致。
//
// 安全约束（与 Rust 侧对齐）：
//   - 所有路径必须位于工程根目录内，禁止越界（path traversal）。
//   - 读写有大小上限。
//   - 在系统文件管理器中定位时拒绝敏感目录（~/.ssh 等）。
package fs

import (
	"mincode/app/backend/core"

	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	maxLocalFsReadBytes        = 5 * 1024 * 1024  // 5MB
	maxLocalFsWriteBytes       = 5 * 1024 * 1024  // 5MB
	maxLocalFsBinaryWriteBytes = 64 * 1024 * 1024 // 64MB，二进制导出（如 .xlsx）

)

func init() {
	core.Register("local_fs_list_dir", handleListDir)
	core.Register("local_fs_file_info", handleFileInfo)
	core.Register("local_fs_read_text_file", handleReadTextFile)
	core.Register("local_fs_write_text_file", handleWriteTextFile)
	core.Register("local_fs_read_selected_text_file", handleReadSelectedTextFile)
	core.Register("local_fs_write_selected_text_file", handleWriteSelectedTextFile)
	core.Register("local_fs_write_selected_binary_file", handleWriteSelectedBinaryFile)
	core.Register("local_fs_rename", handleRename)
	core.Register("local_fs_delete", handleDelete)
	core.Register("local_fs_show_in_file_manager", handleShowInFileManager)
}

// ---------- 返回结构（camelCase JSON，与 Tauri 对齐） ----------

type localFsDirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

type localFsFileInfo struct {
	IsFile bool  `json:"isFile"`
	Size   int64 `json:"size"`
}

// ---------- 安全辅助 ----------

// canonicalLocalProjectRoot 规范化工程根目录，要求其存在且为目录。
func canonicalLocalProjectRoot(projectRoot string) (string, error) {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return "", fmt.Errorf("project_root_empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("path_resolve_failed: %w", err)
	}
	canon, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("path_resolve_failed: %w", err)
	}
	info, err := os.Stat(canon)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("project_root_not_dir")
	}
	return canon, nil
}

// joinUnderRoot 将相对/子路径拼接到根目录，并确保结果仍在根目录内。
// 用于读取及创建（目标可能尚不存在，因此不做 EvalSymlinks）。
func joinUnderRoot(rootCanon, sub string) (string, error) {
	clean := filepath.Clean("/" + strings.TrimSpace(sub)) // 去除 .. 逃逸
	joined := filepath.Join(rootCanon, clean)
	if !withinRoot(rootCanon, joined) {
		return "", fmt.Errorf("path_escapes_root")
	}
	return joined, nil
}

// resolveUnderRoot 解析一个已存在的路径并校验其在根目录内。
func resolveUnderRoot(rootCanon, sub string) (string, error) {
	joined, err := joinUnderRoot(rootCanon, sub)
	if err != nil {
		return "", err
	}
	canon, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("path_resolve_failed: %w", err)
	}
	if !withinRoot(rootCanon, canon) {
		return "", fmt.Errorf("path_escapes_root")
	}
	return canon, nil
}

// resolveEntryUnderRoot 解析一个已存在的条目自身：只对父目录跟随 symlink 并校验其在根目录内，
// 最终组件保持不跟随，使删除/重命名/移动作用于链接本身而不是链接目标。
func resolveEntryUnderRoot(rootCanon, sub string) (string, error) {
	joined, err := joinUnderRoot(rootCanon, sub)
	if err != nil {
		return "", err
	}
	if joined == rootCanon {
		return rootCanon, nil
	}
	canonParent, err := filepath.EvalSymlinks(filepath.Dir(joined))
	if err != nil {
		return "", fmt.Errorf("path_resolve_failed: %w", err)
	}
	if !withinRoot(rootCanon, canonParent) {
		return "", fmt.Errorf("path_escapes_root")
	}
	entry := filepath.Join(canonParent, filepath.Base(joined))
	if _, err := os.Lstat(entry); err != nil {
		return "", fmt.Errorf("path_resolve_failed: %w", err)
	}
	return entry, nil
}

// resolveCreateTargetUnderRoot 校验不存在目标的最近现存父目录真实路径，并拒绝最终 symlink。
func resolveCreateTargetUnderRoot(rootCanon, sub string) (string, error) {
	joined, err := joinUnderRoot(rootCanon, sub)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(joined); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink_target_not_allowed")
		}
		canon, err := filepath.EvalSymlinks(joined)
		if err != nil || !withinRoot(rootCanon, canon) {
			return "", fmt.Errorf("path_escapes_root")
		}
		return canon, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("path_resolve_failed: %w", err)
	}

	parent := filepath.Dir(joined)
	for {
		if _, err := os.Lstat(parent); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("path_resolve_failed: %w", err)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("path_resolve_failed")
		}
		parent = next
	}
	canonParent, err := filepath.EvalSymlinks(parent)
	if err != nil || !withinRoot(rootCanon, canonParent) {
		return "", fmt.Errorf("path_escapes_root")
	}
	return joined, nil
}

// withinRoot 判断 target 是否位于 root 之内（含 root 自身）。
func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

// ---------- 命令处理器 ----------

func handleListDir(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot string  `json:"projectRoot"`
		DirPath     *string `json:"dirPath"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	sub := ""
	if a.DirPath != nil {
		sub = *a.DirPath
	}
	dir, err := resolveUnderRoot(root, sub)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 目录不存在时返回空列表（如 .mincode/rules 尚未创建），避免框架打印 ERR 日志。
		if os.IsNotExist(err) {
			return core.EncodeResult([]localFsDirEntry{})
		}
		return "", fmt.Errorf("read_dir: %w", err)
	}
	out := make([]localFsDirEntry, 0, len(entries))
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		rel, relErr := filepath.Rel(root, full)
		if relErr != nil {
			rel = e.Name()
		}
		out = append(out, localFsDirEntry{
			Name:  e.Name(),
			Path:  filepath.ToSlash(rel),
			IsDir: e.IsDir(),
		})
	}
	return core.EncodeResult(out)
}

func handleFileInfo(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot string `json:"projectRoot"`
		FilePath    string `json:"filePath"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	p, err := resolveUnderRoot(root, a.FilePath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	return core.EncodeResult(localFsFileInfo{
		IsFile: info.Mode().IsRegular(),
		Size:   info.Size(),
	})
}

func handleReadTextFile(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot string `json:"projectRoot"`
		FilePath    string `json:"filePath"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	p, err := resolveUnderRoot(root, a.FilePath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("path_is_directory")
	}
	if info.Size() > maxLocalFsReadBytes {
		return "", fmt.Errorf("file_too_large: %d bytes (max %d)", info.Size(), maxLocalFsReadBytes)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	// 返回值为一个 JSON 字符串（前端 invoke<string> 反序列化后即为文本内容）。
	b, err := json.Marshal(string(data))
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(b), nil
}

func handleWriteTextFile(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot string `json:"projectRoot"`
		FilePath    string `json:"filePath"`
		Content     string `json:"content"`
		// CreateOnly 仅用于新建（插件文件模板）：目标已存在（文件、目录或
		// symlink）时返回 already_exists，以 O_EXCL 独占创建，权限固定 0644。
		CreateOnly bool `json:"createOnly"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	if a.CreateOnly {
		joined, err := joinUnderRoot(root, a.FilePath)
		if err != nil {
			return "", err
		}
		if joined == root {
			return "", fmt.Errorf("already_exists")
		}
		if _, err := os.Lstat(joined); err == nil {
			return "", fmt.Errorf("already_exists")
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("path_resolve_failed: %w", err)
		}
	}
	p, err := resolveCreateTargetUnderRoot(root, a.FilePath)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(p); statErr == nil && info.IsDir() {
		return "", fmt.Errorf("path_is_directory")
	}
	if int64(len(a.Content)) > maxLocalFsWriteBytes {
		return "", fmt.Errorf("content_too_large: %d bytes (max %d)", len(a.Content), maxLocalFsWriteBytes)
	}
	if parent := filepath.Dir(p); parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return "", fmt.Errorf("create_dir_all: %w", err)
		}
	}
	if a.CreateOnly {
		if err := createTextFileExclusive(p, a.Content); err != nil {
			return "", err
		}
	} else if err := os.WriteFile(p, []byte(a.Content), 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return "", nil
}

// createTextFileExclusive 以 O_EXCL 新建文件（不跟随、不覆盖已有路径），权限 0644，
// 从不设置可执行位。
func createTextFileExclusive(p, content string) error {
	file, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("already_exists")
		}
		return fmt.Errorf("write: %w", err)
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// handleReadSelectedTextFile 读取由 原生文件选择器明确授权的文本文件。
// 它仅服务于配置导入，限制为普通文件且最大 5MB。
func handleReadSelectedTextFile(_ string, argsJson string) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	p := strings.TrimSpace(a.Path)
	if p == "" {
		return "", fmt.Errorf("path_empty")
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not_a_file")
	}
	if info.Size() > maxLocalFsReadBytes {
		return "", fmt.Errorf("file_too_large: %d bytes (max %d)", info.Size(), maxLocalFsReadBytes)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return core.EncodeResult(string(data))
}

// handleWriteSelectedTextFile 写入由 原生保存对话框明确授权的目标文件。
func handleWriteSelectedTextFile(_ string, argsJson string) (string, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	p := strings.TrimSpace(a.Path)
	if p == "" {
		return "", fmt.Errorf("path_empty")
	}
	if int64(len(a.Content)) > maxLocalFsWriteBytes {
		return "", fmt.Errorf("content_too_large: %d bytes (max %d)", len(a.Content), maxLocalFsWriteBytes)
	}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return "", fmt.Errorf("path_is_directory")
	}
	if err := os.WriteFile(p, []byte(a.Content), 0o600); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return "", nil
}

// handleWriteSelectedBinaryFile 写入由 原生保存对话框明确授权的二进制文件，
// 内容以 base64 传输（如表数据导出的 .xlsx）。
func handleWriteSelectedBinaryFile(_ string, argsJson string) (string, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	p := strings.TrimSpace(a.Path)
	if p == "" {
		return "", fmt.Errorf("path_empty")
	}
	data, err := base64.StdEncoding.DecodeString(a.Content)
	if err != nil {
		return "", fmt.Errorf("content_invalid_base64: %w", err)
	}
	if int64(len(data)) > maxLocalFsBinaryWriteBytes {
		return "", fmt.Errorf("content_too_large: %d bytes (max %d)", len(data), maxLocalFsBinaryWriteBytes)
	}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return "", fmt.Errorf("path_is_directory")
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return "", nil
}

func handleRename(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot string `json:"projectRoot"`
		OldPath     string `json:"oldPath"`
		NewName     string `json:"newName"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	name := strings.TrimSpace(a.NewName)
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return "", fmt.Errorf("invalid_name")
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	oldAbs, err := resolveEntryUnderRoot(root, a.OldPath)
	if err != nil {
		return "", err
	}
	if oldAbs == root {
		return "", fmt.Errorf("cannot_rename_project_root")
	}
	newAbs := filepath.Join(filepath.Dir(oldAbs), name)
	newRel, err := filepath.Rel(root, newAbs)
	if err != nil {
		return "", fmt.Errorf("path_resolve_failed: %w", err)
	}
	newAbs, err = resolveCreateTargetUnderRoot(root, newRel)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(newAbs); statErr == nil {
		return "", fmt.Errorf("already_exists")
	}
	if err := os.Rename(oldAbs, newAbs); err != nil {
		return "", fmt.Errorf("rename: %w", err)
	}
	return "", nil
}

func handleDelete(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot string `json:"projectRoot"`
		Path        string `json:"path"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	abs, err := resolveEntryUnderRoot(root, a.Path)
	if err != nil {
		return "", err
	}
	if abs == root {
		return "", fmt.Errorf("cannot_delete_project_root")
	}
	// abs 为 symlink 时 RemoveAll 只移除链接本身。
	if err := os.RemoveAll(abs); err != nil {
		return "", fmt.Errorf("delete: %w", err)
	}
	return "", nil
}

// openInFileManager 在系统文件管理器中打开目标目录。
func openInFileManager(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open_file_manager: %w", err)
	}
	return nil
}

// handleShowInFileManager 对齐 Rust local_fs_show_in_file_manager：
// 单参数 path（绝对路径，不限工程根），拒绝敏感目录，文件则在管理器中"选中定位"。
func handleShowInFileManager(_ string, argsJson string) (string, error) {
	var a struct {
		Path   string `json:"path"`
		Select bool   `json:"select"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("path_empty")
	}
	canon, err := filepath.EvalSymlinks(a.Path)
	if err != nil {
		if abs, absErr := filepath.Abs(a.Path); absErr == nil {
			canon = abs
		} else {
			return "", fmt.Errorf("path_resolve_failed: %w", err)
		}
	}
	if err := rejectSensitiveLocateDir(canon); err != nil {
		return "", err
	}

	info, statErr := os.Stat(canon)
	isDir := statErr == nil && info.IsDir()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		if isDir && !a.Select {
			cmd = exec.Command("explorer", canon)
		} else {
			cmd = exec.Command("explorer", "/select,"+canon)
		}
	case "darwin":
		if isDir && !a.Select {
			cmd = exec.Command("open", canon)
		} else {
			cmd = exec.Command("open", "-R", canon)
		}
	default:
		// Linux：无统一 select 语义，定位到所在目录。
		target := canon
		if !isDir {
			target = filepath.Dir(canon)
		}
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("file_manager_spawn: %w", err)
	}
	return "", nil
}

// rejectSensitiveLocateDir 拒绝在 ~/.ssh 等敏感目录中定位文件（对齐 Rust 敏感目录检查）。
func rejectSensitiveLocateDir(canon string) error {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	sensitive := []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".config", "gh"),
		filepath.Join(home, ".docker"),
		filepath.Join(home, ".kube"),
	}
	for _, root := range sensitive {
		rootCanon := root
		if rc, rcErr := filepath.EvalSymlinks(root); rcErr == nil {
			rootCanon = rc
		}
		if canon == rootCanon {
			return fmt.Errorf("拒绝在敏感目录中定位: %s", root)
		}
		if rel, relErr := filepath.Rel(rootCanon, canon); relErr == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("拒绝在敏感目录中定位: %s", root)
		}
	}
	return nil
}
