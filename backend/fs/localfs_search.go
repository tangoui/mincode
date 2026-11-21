package fs

import (
	"context"
	"mincode/app/backend/core"

	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// 与 Tauri (src-tauri/src/local_fs.rs) 对齐的搜索/遍历/替换命令。

const (
	searchMaxResults     = 300
	searchMaxFileBytes   = 1 << 20 // 1 MB
	searchPeekBytes      = 8192
	listAllMaxFiles      = 2000
	regexMaxResults      = 2000
	regexMaxFileBytes    = 2 << 20 // 2 MB
	regexBinaryPeekSize  = 512
	peekBinaryMaxBytes   = 8192
	maxLocalFsImageBytes = 20 << 20 // 20 MB
)

func init() {
	core.RegisterCtx("local_fs_search", handleSearch)
	core.Register("local_fs_list_all_files", handleListAllFiles)
	core.RegisterCtx("local_fs_regex_search", handleRegexSearch)
	core.Register("local_fs_regex_replace_preview", handleRegexReplacePreview)
	core.Register("local_fs_regex_replace_apply", handleRegexReplaceApply)
	core.Register("local_fs_copy_to", handleCopyTo)
	core.Register("local_fs_move_to", handleMoveTo)
	core.Register("local_fs_read_ssh_config", handleReadSSHConfig)
	core.Register("local_fs_read_image_data_url", handleReadImageDataUrl)
	core.Register("local_fs_peek_binary_for_preview", handlePeekBinaryForPreview)
}

// ---------- 返回结构（camelCase JSON，与 Tauri 对齐） ----------

type localFsSearchResult struct {
	Name         string  `json:"name"`
	Path         string  `json:"path"`
	RelativePath string  `json:"relativePath"`
	IsDir        bool    `json:"isDir"`
	Kind         string  `json:"kind"` // "name" | "content"
	Line         *int    `json:"line"`
	LineText     *string `json:"lineText"`
}

type regexMatch struct {
	RelativePath string `json:"relativePath"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	Col          int    `json:"col"`
	LineText     string `json:"lineText"`
	MatchStart   int    `json:"matchStart"`
	MatchEnd     int    `json:"matchEnd"`
}

type replacementHunk struct {
	Line    int    `json:"line"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type replacePreviewFile struct {
	RelativePath string            `json:"relativePath"`
	Path         string            `json:"path"`
	Hunks        []replacementHunk `json:"hunks"`
}

// ---------- 目录跳过策略（与 Rust 对齐） ----------

// findFirstContentMatch 流式读取文本文件，返回首个不区分大小写的匹配行。
func findFirstContentMatch(path, query string) (string, int, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), searchMaxFileBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if strings.Contains(strings.ToLower(line), query) {
			return line, lineNumber, true
		}
	}
	if scanner.Err() != nil {
		return "", 0, false
	}
	return "", 0, false
}

// searchSkipDirNames 同时用于 Go 遍历与 ripgrep 的 --glob 排除，保持两个引擎一致。
var searchSkipDirNames = []string{".git", "node_modules", "target", "dist", "build", ".next", ".nuxt", ".output"}

func shouldSkipSearchDir(name string) bool {
	for _, skip := range searchSkipDirNames {
		if name == skip {
			return true
		}
	}
	return false
}

// aiTreeSkipDirNames 是生成物与工具缓存目录的唯一清单。内容搜索还要把它翻译成
// ripgrep 的 --glob，两边各抄一份必然漂移，所以统一从这里派生。
var aiTreeSkipDirNames = []string{
	".git", ".hg", ".svn", "node_modules", "vendor", "target", "dist", "build",
	"out", ".next", ".nuxt", ".output", ".cache", "coverage", ".venv", "venv",
	"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".gradle", ".idea", ".vscode",
	// 包管理器与构建工具的内容寻址缓存：整棵子树都是依赖包的副本，命中的是别人的
	// 源码。一个 pnpm store 就足以把项目自身的结果全部挤出列表。
	".pnpm-store", ".turbo", ".parcel-cache", ".nyc_output", ".tox", ".terraform",
	".svelte-kit", ".astro",
}

var aiTreeSkipDirSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(aiTreeSkipDirNames))
	for _, name := range aiTreeSkipDirNames {
		set[name] = struct{}{}
	}
	return set
}()

func shouldSkipAiTreeDir(name string) bool {
	if _, skip := aiTreeSkipDirSet[name]; skip {
		return true
	}
	return shouldSkipSearchDir(name)
}

// sampleLooksLikeBinaryForPreview 判断样本是否为二进制（含 NUL 或已知魔数）。
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

// simpleGlobMatch 支持 `*`（任意字符）与 `?`（单字符）。
func simpleGlobMatch(pattern, name string) bool {
	pat := []rune(pattern)
	txt := []rune(name)
	dp := make([][]bool, len(pat)+1)
	for i := range dp {
		dp[i] = make([]bool, len(txt)+1)
	}
	dp[0][0] = true
	for i := 1; i <= len(pat); i++ {
		if pat[i-1] == '*' {
			dp[i][0] = dp[i-1][0]
		}
	}
	for i := 1; i <= len(pat); i++ {
		for j := 1; j <= len(txt); j++ {
			if pat[i-1] == '*' {
				dp[i][j] = dp[i-1][j] || dp[i][j-1]
			} else if pat[i-1] == '?' || pat[i-1] == txt[j-1] {
				dp[i][j] = dp[i-1][j-1]
			}
		}
	}
	return dp[len(pat)][len(txt)]
}

// buildSearchRegex 依据 whole_word / case_sensitive 构造正则。
func buildSearchRegex(pattern string, caseSensitive, wholeWord bool) (*regexp.Regexp, error) {
	pat := pattern
	if wholeWord {
		pat = `\b` + pat + `\b`
	}
	if !caseSensitive {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	return re, nil
}

// ---------- 命令处理器 ----------

func handleSearch(ctx context.Context, _ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot    string  `json:"projectRoot"`
		ScopePath      *string `json:"scopePath"`
		Query          string  `json:"query"`
		IncludeContent bool    `json:"includeContent"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	q := strings.ToLower(strings.TrimSpace(a.Query))
	if q == "" {
		return core.EncodeResult([]localFsSearchResult{})
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	scopeArg := ""
	if a.ScopePath != nil {
		scopeArg = *a.ScopePath
	}
	scope, err := resolveUnderRoot(root, scopeArg)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(scope); statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("scope_not_a_directory")
	}

	results := make([]localFsSearchResult, 0, 32)
	stack := []string{scope}
	for len(stack) > 0 && len(results) < searchMaxResults {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			continue
		}
		for _, e := range entries {
			if len(results) >= searchMaxResults {
				break
			}
			// 跳过符号链接，防止逃出 project root。
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			full := filepath.Join(dir, e.Name())
			name := e.Name()
			rel := relPathPosix(root, full)

			if e.IsDir() {
				if strings.Contains(strings.ToLower(name), q) {
					results = append(results, localFsSearchResult{
						Name: name, Path: full, RelativePath: rel,
						IsDir: true, Kind: "name",
					})
				}
				if !shouldSkipSearchDir(name) {
					stack = append(stack, full)
				}
				continue
			}
			// 目录项自带的类型足以判断普通文件；只有需要读内容时才 lstat 取大小。
			if !e.Type().IsRegular() {
				continue
			}
			if strings.Contains(strings.ToLower(name), q) {
				results = append(results, localFsSearchResult{
					Name: name, Path: full, RelativePath: rel,
					IsDir: false, Kind: "name",
				})
				if len(results) >= searchMaxResults {
					break
				}
			}
			if !a.IncludeContent {
				continue
			}
			info, infoErr := e.Info()
			if infoErr != nil || !info.Mode().IsRegular() || info.Size() > searchMaxFileBytes {
				continue
			}
			peek, peekErr := readPeek(full, searchPeekBytes)
			if peekErr != nil || sampleLooksLikeBinaryForPreview(peek) {
				continue
			}
			line, lineNumber, found := findFirstContentMatch(full, q)
			if !found {
				continue
			}
			txt := truncateRunes(strings.TrimSpace(line), 240)
			results = append(results, localFsSearchResult{
				Name: name, Path: full, RelativePath: rel,
				IsDir: false, Kind: "content",
				Line: &lineNumber, LineText: &txt,
			})
		}
	}
	return core.EncodeResult(results)
}

func handleListAllFiles(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot string `json:"projectRoot"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	results := make([]string, 0, 128)
	queue := []string{root}
	for len(queue) > 0 && len(results) < listAllMaxFiles {
		dir := queue[0]
		queue = queue[1:]

		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			continue
		}
		type item struct {
			name  string
			full  string
			isDir bool
		}
		items := make([]item, 0, len(entries))
		for _, e := range entries {
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			items = append(items, item{name: e.Name(), full: filepath.Join(dir, e.Name()), isDir: e.IsDir()})
		}
		// 目录优先，按名称（忽略大小写）排序，保持稳定顺序。
		sort.Slice(items, func(i, j int) bool {
			if items[i].isDir != items[j].isDir {
				return items[i].isDir
			}
			return strings.ToLower(items[i].name) < strings.ToLower(items[j].name)
		})
		for _, it := range items {
			if it.isDir {
				if !shouldSkipAiTreeDir(it.name) {
					queue = append(queue, it.full)
				}
			} else {
				results = append(results, relPathPosix(root, it.full))
				if len(results) >= listAllMaxFiles {
					break
				}
			}
		}
	}
	sort.Strings(results)
	return core.EncodeResult(results)
}

func handleRegexReplacePreview(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot   string  `json:"projectRoot"`
		Pattern       string  `json:"pattern"`
		Replacement   string  `json:"replacement"`
		CaseSensitive bool    `json:"caseSensitive"`
		WholeWord     bool    `json:"wholeWord"`
		IncludeGlob   *string `json:"includeGlob"`
		ScopePath     string  `json:"scopePath"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return core.EncodeResult([]replacePreviewFile{})
	}
	re, err := buildSearchRegex(a.Pattern, a.CaseSensitive, a.WholeWord)
	if err != nil {
		return "", err
	}
	glob := globFilter(a.IncludeGlob)
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	scope, err := searchScopeUnderRoot(root, a.ScopePath)
	if err != nil {
		return "", err
	}
	prefilter := regexFilePrefilter(re)

	results := make([]replacePreviewFile, 0, 16)
	stack := []string{filepath.Join(root, filepath.FromSlash(scope))}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			continue
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			full := filepath.Join(dir, e.Name())
			name := e.Name()
			if e.IsDir() {
				if !shouldSkipSearchDir(name) {
					stack = append(stack, full)
				}
				continue
			}
			if !e.Type().IsRegular() || (glob != "" && !simpleGlobMatch(glob, name)) {
				continue
			}
			info, infoErr := e.Info()
			if infoErr != nil || !info.Mode().IsRegular() || info.Size() > regexMaxFileBytes {
				continue
			}
			rel := relPathPosix(root, full)
			resolved, resolveErr := resolveUnderRoot(root, rel)
			if resolveErr != nil {
				continue
			}
			data, dataErr := os.ReadFile(resolved)
			if dataErr != nil {
				continue
			}
			if sampleLooksLikeBinaryForPreview(peekHead(data, regexBinaryPeekSize)) {
				continue
			}
			if prefilter != nil && !prefilter.Match(data) {
				continue
			}
			hunks := make([]replacementHunk, 0)
			for lineIdx, line := range strings.Split(string(data), "\n") {
				if re.MatchString(line) {
					hunks = append(hunks, replacementHunk{
						Line:    lineIdx + 1,
						OldText: line,
						NewText: re.ReplaceAllString(line, a.Replacement),
					})
				}
			}
			if len(hunks) > 0 {
				results = append(results, replacePreviewFile{
					RelativePath: rel, Path: full, Hunks: hunks,
				})
			}
		}
	}
	return core.EncodeResult(results)
}

func handleRegexReplaceApply(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot   string   `json:"projectRoot"`
		Pattern       string   `json:"pattern"`
		Replacement   string   `json:"replacement"`
		CaseSensitive bool     `json:"caseSensitive"`
		WholeWord     bool     `json:"wholeWord"`
		FilePaths     []string `json:"filePaths"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return core.EncodeResult(0)
	}
	re, err := buildSearchRegex(a.Pattern, a.CaseSensitive, a.WholeWord)
	if err != nil {
		return "", err
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	changed := 0
	for _, fp := range a.FilePaths {
		// 在解析路径前拒绝最终组件 symlink，避免 apply 跟随预览阶段不会返回的链接。
		p, resErr := resolveCreateTargetUnderRoot(root, fp)
		if resErr != nil {
			continue
		}
		info, statErr := os.Lstat(p)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > regexMaxFileBytes {
			continue
		}
		data, dataErr := os.ReadFile(p)
		if dataErr != nil {
			continue
		}
		if sampleLooksLikeBinaryForPreview(peekHead(data, regexBinaryPeekSize)) {
			continue
		}
		newText := re.ReplaceAllString(string(data), a.Replacement)
		if newText != string(data) {
			if writeErr := os.WriteFile(p, []byte(newText), info.Mode().Perm()); writeErr == nil {
				changed++
			}
		}
	}
	return core.EncodeResult(changed)
}

func handleCopyTo(_ string, argsJson string) (string, error) {
	return handleCopyOrMove(argsJson, false)
}

func handleMoveTo(_ string, argsJson string) (string, error) {
	return handleCopyOrMove(argsJson, true)
}

func handleCopyOrMove(argsJson string, move bool) (string, error) {
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
	// 移动作用于条目自身（symlink 移动链接本身）；复制读取链接目标内容，但目标仍沿用条目名称。
	srcEntry, err := resolveEntryUnderRoot(root, a.SrcPath)
	if err != nil {
		return "", err
	}
	if srcEntry == root {
		return "", fmt.Errorf("cannot_move_project_root")
	}
	srcData := srcEntry
	if !move {
		srcData, err = resolveUnderRoot(root, a.SrcPath)
		if err != nil {
			return "", err
		}
		if srcData == root {
			return "", fmt.Errorf("cannot_move_project_root")
		}
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
	if withinRoot(srcData, destDir) {
		return "", fmt.Errorf("dest_inside_source")
	}
	if _, statErr := os.Lstat(dest); statErr == nil {
		return "", fmt.Errorf("already_exists")
	}
	if move {
		if err := os.Rename(srcEntry, dest); err != nil {
			return "", fmt.Errorf("move: %w", err)
		}
	} else {
		if err := copyPath(srcData, dest); err != nil {
			return "", fmt.Errorf("copy: %w", err)
		}
	}
	return "", nil
}

func handleReadSSHConfig(_ string, _ string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户主目录: %w", err)
	}
	configPath := filepath.Join(home, ".ssh", "config")
	if _, statErr := os.Stat(configPath); statErr != nil {
		return "", fmt.Errorf("~/.ssh/config 不存在")
	}
	data, readErr := os.ReadFile(configPath)
	if readErr != nil {
		return "", readErr
	}
	return core.EncodeResult(string(data))
}

func handleReadImageDataUrl(_ string, argsJson string) (string, error) {
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
	info, statErr := os.Stat(p)
	if statErr != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("not_a_file")
	}
	mime := imageMimeFromPath(p)
	if mime == "" {
		return "", fmt.Errorf("unsupported_image_type")
	}
	if info.Size() > maxLocalFsImageBytes {
		return "", fmt.Errorf("image_too_large: %d bytes (max %d)", info.Size(), int64(maxLocalFsImageBytes))
	}
	data, readErr := os.ReadFile(p)
	if readErr != nil {
		return "", fmt.Errorf("read: %w", readErr)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return core.EncodeResult(fmt.Sprintf("data:%s;base64,%s", mime, encoded))
}

func handlePeekBinaryForPreview(_ string, argsJson string) (string, error) {
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
	info, statErr := os.Stat(p)
	if statErr != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("not_a_file")
	}
	peek, peekErr := readPeek(p, peekBinaryMaxBytes)
	if peekErr != nil {
		return "", fmt.Errorf("read: %w", peekErr)
	}
	return core.EncodeResult(sampleLooksLikeBinaryForPreview(peek))
}

func imageMimeFromPath(p string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
	switch ext {
	case "png":
		return "image/png"
	case "jpg", "jpeg", "jfif":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	case "ico":
		return "image/x-icon"
	case "avif":
		return "image/avif"
	case "tif", "tiff":
		return "image/tiff"
	case "svg":
		return "image/svg+xml"
	default:
		return ""
	}
}

// ---------- 内部辅助 ----------

func relPathPosix(root, full string) string {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return filepath.ToSlash(full)
	}
	return filepath.ToSlash(rel)
}

func globFilter(g *string) string {
	if g == nil {
		return ""
	}
	return strings.TrimSpace(*g)
}

func truncateRunes(s string, max int) string {
	// 绝大多数行不需要截断：先计数，避免为每一行分配 []rune。
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

func peekHead(data []byte, n int) []byte {
	if len(data) < n {
		return data
	}
	return data[:n]
}

func readPeek(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := f.Read(buf)
	if err != nil && read == 0 {
		return nil, err
	}
	return buf[:read], nil
}

// copyPath 递归复制文件或目录。
func copyPath(src, dest string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
			return err
		}
		entries, readErr := os.ReadDir(src)
		if readErr != nil {
			return readErr
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink != 0 {
				continue // 跳过符号链接
			}
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dest, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, data, info.Mode().Perm())
}
