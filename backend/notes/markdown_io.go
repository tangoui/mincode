package notes

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Markdown 导入导出。
//
// 导入：路径来自原生选择器，但仍按不可信处理——必须是绝对路径；不跟随软链接（os.Lstat 逐级检查）；
// 正文中的本地图片解析后必须仍在导入根目录内（notes_import_files 的根目录是 Markdown 文件所在目录）。
// 导入是尽力而为：单个文件或图片的问题记入 Skipped，不中断整个导入；数据库等系统错误才返回错误。
//
// 导出：写到已存在的绝对目录；所有文件与目录都以 O_EXCL / Mkdir 新建，重名加「 (2)」「 (3)」，从不覆盖。

const (
	// maxImportMarkdownBytes 是单个导入 Markdown 文件的上限；超出记为 too_large。
	maxImportMarkdownBytes = 5 << 20
	// maxImportPaths 是 notes_import_files 单次可导入的文件数上限。
	maxImportPaths = 10000
	// maxImportPathBytes 限制请求中的单个路径。
	maxImportPathBytes = 4096
	// maxExportNoteIDs 是 notes_export 单次可导出的笔记数上限。
	maxExportNoteIDs = 10000
	// maxExportNameBytes 限制导出文件 / 目录名主体（不含序号与扩展名），常见文件系统单段上限为 255 字节。
	maxExportNameBytes = 180
	// maxUniqueNameAttempts 是重名加序号的尝试上限。
	maxUniqueNameAttempts = 10000

	exportAssetsDirName = "assets"
	untitledName        = "无标题"
)

// 导入上限（包级变量，测试可调小）。
var (
	// maxImportEntries 是文件夹导入最多遍历的目录条目数（文件与目录，不含根目录）。
	maxImportEntries = 20000
	// maxImportNotes 是文件夹导入最多导入的笔记数。
	maxImportNotes = 5000
	// maxImportSkipped 是 ImportReport.Skipped 最多保留的条数；SkippedTotal 记全部数量。
	maxImportSkipped = 1000
)

// 导入跳过原因。
const (
	skipOutsideRoot   = "outside_root"
	skipSymlink       = "symlink"
	skipAssetType     = "asset_type"
	skipAssetTooLarge = "asset_too_large"
	skipMissing       = "missing"
	skipTooLarge      = "too_large"
	skipReadError     = "read_error"
)

var (
	errFileTooLarge = errors.New("notes: file too large")
	errFileChanged  = errors.New("notes: file changed while opening")

	// markdownImagePattern 匹配 ![alt](dest) 与 ![alt](<dest> "title")；dest 在第 1 组（尖括号内）或第 2 组。
	markdownImagePattern = regexp.MustCompile(`!\[(?:[^\[\]\n]|\[[^\[\]\n]*\])*\]\(\s*(?:<([^<>\n]*)>|((?:[^\s()]|\([^\s()]*\))+))(?:\s+(?:"[^"\n]*"|'[^'\n]*'|\([^()\n]*\)))?\s*\)`)
	// htmlImagePattern 匹配 <img ... src="dest">；dest 在第 1–3 组之一。
	htmlImagePattern = regexp.MustCompile(`(?i)<img\b[^>]*?\ssrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	// urlSchemePattern 匹配带协议的引用（http:、data:、mailto: 等）；单字母视为 Windows 盘符。
	urlSchemePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]+:`)
	// exportAssetPattern 在正文中查找独立的附件引用：前面须是行首、空白或 ( " ' < =，
	// 这样 https://host/__notes-assets/… 之类的外部链接不会被改写。第 1 组为引用，第 2 组为文件名。
	exportAssetPattern = regexp.MustCompile(`(?m)(?:^|[\s("'<=])(/__notes-assets/([0-9a-f]{64}\.(?:png|jpg|gif|webp)))`)
	headingPattern     = regexp.MustCompile(`^ {0,3}#[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$`)
	frontMatterKey     = regexp.MustCompile(`^[A-Za-z_][\w .-]*$`)
	yamlReservedWord   = regexp.MustCompile(`^(?i:true|false|yes|no|on|off|y|n|null|~)$`)
	yamlNumberLike     = regexp.MustCompile(`^[-+.]?[0-9]`)
	windowsReserved    = regexp.MustCompile(`^(?i:con|prn|aux|nul|com[0-9¹²³]|lpt[0-9¹²³])$`)
)

func isMarkdownName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
}

// validImportPath 校验请求中的文件系统路径：绝对路径、合法 UTF-8、无 NUL、有长度上限。
func validImportPath(path string) bool {
	return path != "" && len(path) <= maxImportPathBytes && utf8.ValidString(path) &&
		!strings.ContainsRune(path, 0) && filepath.IsAbs(path)
}

// ---------- 导入 ----------

type assetOutcome struct {
	ref    string
	reason string
}

type importer struct {
	ctx    context.Context
	db     *sql.DB
	report ImportReport
	// assets 缓存本次导入中已处理的图片（解析后的绝对路径 → 结果），同一图片只写入、只报告一次。
	assets map[string]assetOutcome
	// skipped 去重跳过项（路径 + 原因）。
	skipped map[string]bool
}

func newImporter(ctx context.Context, db *sql.DB) *importer {
	return &importer{
		ctx:     ctx,
		db:      db,
		report:  ImportReport{Skipped: []SkippedItem{}},
		assets:  map[string]assetOutcome{},
		skipped: map[string]bool{},
	}
}

func (im *importer) skip(path, reason string) {
	key := reason + "\x00" + path
	if im.skipped[key] {
		return
	}
	im.skipped[key] = true
	im.report.SkippedTotal++
	if len(im.report.Skipped) < maxImportSkipped {
		im.report.Skipped = append(im.report.Skipped, SkippedItem{Path: path, Reason: reason})
	}
}

// importFiles 导入用户选中的 Markdown 文件到 folderID；每个文件的图片根目录是它所在的目录。
func importFiles(ctx context.Context, db *sql.DB, paths []string, folderID string) (ImportReport, error) {
	if folderID != "" {
		if err := folderExists(ctx, db, folderID); err != nil {
			return ImportReport{}, err
		}
	}
	im := newImporter(ctx, db)
	target := func() (string, error) { return folderID, nil }
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return ImportReport{}, err
		}
		path = filepath.Clean(path)
		if err := im.importMarkdown(path, filepath.Dir(path), target); err != nil {
			return ImportReport{}, err
		}
	}
	return im.report, nil
}

// importFolder 递归导入 root：在 parentID 下建同名文件夹并保留层级，只导入 .md / .markdown。
// 文件夹在其中第一篇笔记导入时才创建，不产生空文件夹（如图片目录）；隐藏文件与目录（以 . 开头）忽略。
// root 本身是软链接时返回 note_import_path。遍历超过 maxImportEntries 个条目或已导入 maxImportNotes
// 篇笔记时停止，并置 Truncated。
func importFolder(ctx context.Context, db *sql.DB, root, parentID string) (ImportReport, error) {
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() { // 软链接的 Lstat 不是目录，同样拒绝
		return ImportReport{}, errImportPath
	}
	if parentID != "" {
		if err := folderExists(ctx, db, parentID); err != nil {
			return ImportReport{}, err
		}
	}
	im := newImporter(ctx, db)
	folderIDs := map[string]string{}
	var ensureFolder func(rel string) (string, error)
	ensureFolder = func(rel string) (string, error) {
		if id, ok := folderIDs[rel]; ok {
			return id, nil
		}
		parent, name := parentID, importFolderName(filepath.Base(root))
		if rel != "." {
			var err error
			if parent, err = ensureFolder(filepath.Dir(rel)); err != nil {
				return "", err
			}
			name = importFolderName(filepath.Base(rel))
		}
		folder, err := createFolder(ctx, db, parent, name)
		if err != nil {
			return "", err
		}
		folderIDs[rel] = folder.ID
		return folder.ID, nil
	}

	visited := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == root {
				return fmt.Errorf("notes: read import folder: %w", walkErr)
			}
			im.skip(path, skipReadError)
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		if visited == maxImportEntries {
			im.report.Truncated = true
			return filepath.SkipAll
		}
		visited++
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			// 不跟随；只报告看起来会被导入的软链接（Markdown 文件或目录）。
			if target, err := os.Stat(path); isMarkdownName(name) || (err == nil && target.IsDir()) {
				im.skip(path, skipSymlink)
			}
			return nil
		}
		if entry.IsDir() || !isMarkdownName(name) {
			return nil
		}
		if im.report.Notes == maxImportNotes {
			im.report.Truncated = true
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("notes: import path: %w", err)
		}
		return im.importMarkdown(path, root, func() (string, error) { return ensureFolder(rel) })
	})
	if err != nil {
		return ImportReport{}, err
	}
	return im.report, nil
}

// importMarkdown 导入一个 Markdown 文件；文件本身的问题记入 Skipped，只有系统错误才返回。
// folder 在笔记确定要写入时才调用，返回目标文件夹 id。
func (im *importer) importMarkdown(path, root string, folder func() (string, error)) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		im.skip(path, skipMissing)
		return nil
	case err != nil:
		im.skip(path, skipReadError)
		return nil
	case info.Mode()&fs.ModeSymlink != 0:
		im.skip(path, skipSymlink)
		return nil
	case !info.Mode().IsRegular():
		im.skip(path, skipReadError)
		return nil
	case info.Size() > maxImportMarkdownBytes:
		im.skip(path, skipTooLarge)
		return nil
	}
	data, err := readRegularFile(path, info, maxImportMarkdownBytes)
	if errors.Is(err, errFileTooLarge) {
		im.skip(path, skipTooLarge)
		return nil
	}
	if err != nil {
		im.skip(path, skipReadError)
		return nil
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		im.skip(path, skipReadError)
		return nil
	}
	meta, body := splitFrontMatter(text)
	if len(body) > maxBodyBytes {
		im.skip(path, skipTooLarge)
		return nil
	}
	body, err = im.rewriteImages(body, filepath.Dir(path), root)
	if err != nil {
		return err
	}
	if len(body) > maxBodyBytes {
		im.skip(path, skipTooLarge)
		return nil
	}

	title := cleanImportTitle(meta.title)
	if title == "" {
		title = cleanImportTitle(firstHeading(body))
	}
	if title == "" {
		base := filepath.Base(path)
		title = cleanImportTitle(strings.TrimSuffix(base, filepath.Ext(base)))
	}
	folderID, err := folder()
	if err != nil {
		return err
	}
	if err := insertImportedNote(im.ctx, im.db, folderID, title, body, meta.favorite, importTags(meta.tags)); err != nil {
		return err
	}
	im.report.Notes++
	return nil
}

// insertImportedNote 在一个事务中写入笔记及其标签。
func insertImportedNote(ctx context.Context, db *sql.DB, folderID, title, body string, favorite bool, tags []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notes: import note: %w", err)
	}
	defer tx.Rollback()
	note, err := insertNote(ctx, tx, folderID, title, body, favorite)
	if err != nil {
		return err
	}
	if len(tags) > 0 {
		if _, err := setTagsTx(ctx, tx, note.ID, tags); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("notes: import note: %w", err)
	}
	return nil
}

// readRegularFile 读取 Lstat 得到的普通文件，确认打开的仍是同一文件（防止检查后被替换为软链接），
// 超过 limit 字节返回 errFileTooLarge。
func readRegularFile(path string, info fs.FileInfo, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errFileChanged
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errFileTooLarge
	}
	return data, nil
}

type imageSpan struct {
	start, end int
}

// rewriteImages 把正文中引用本地图片的 ![](…) 与 <img src="…"> 写入附件并改写为 ref；
// 围栏代码块中的内容保持原样。
func (im *importer) rewriteImages(body, mdDir, root string) (string, error) {
	var out strings.Builder
	for _, segment := range splitFences(body) {
		if segment.code {
			out.WriteString(segment.text)
			continue
		}
		rewritten, err := im.rewriteSegment(segment.text, mdDir, root)
		if err != nil {
			return "", err
		}
		out.WriteString(rewritten)
	}
	return out.String(), nil
}

func (im *importer) rewriteSegment(text, mdDir, root string) (string, error) {
	var spans []imageSpan
	collect := func(pattern *regexp.Regexp) {
		for _, match := range pattern.FindAllStringSubmatchIndex(text, -1) {
			for group := 1; group*2+1 < len(match); group++ {
				if match[group*2] >= 0 {
					spans = append(spans, imageSpan{match[group*2], match[group*2+1]})
					break
				}
			}
		}
	}
	collect(markdownImagePattern)
	collect(htmlImagePattern)
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	var out strings.Builder
	last := 0
	for _, span := range spans {
		if span.start < last {
			continue // 与前一处重叠（如 alt 中的 <img>），保持原样
		}
		ref, ok, err := im.importImage(text[span.start:span.end], mdDir, root)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		out.WriteString(text[last:span.start])
		out.WriteString(ref)
		last = span.end
	}
	out.WriteString(text[last:])
	return out.String(), nil
}

// importImage 处理一个图片引用：非本地引用（http(s)、data: 等）原样保留且不报告；本地图片写入附件
// 返回 ref；无法导入的记入 Skipped 并保留原文。
func (im *importer) importImage(dest, mdDir, root string) (string, bool, error) {
	raw := strings.TrimSpace(dest)
	if raw == "" || strings.HasPrefix(raw, assetRefPrefix) || strings.HasPrefix(raw, "//") {
		return "", false, nil
	}
	var path string
	if strings.HasPrefix(strings.ToLower(raw), "file:") {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) {
			im.skip(raw, skipOutsideRoot)
			return "", false, nil
		}
		path = parsed.Path
		if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
			path = path[1:]
		}
		path = filepath.FromSlash(path)
		if !filepath.IsAbs(path) {
			im.skip(raw, skipOutsideRoot)
			return "", false, nil
		}
	} else if urlSchemePattern.MatchString(raw) {
		return "", false, nil
	} else {
		path = raw
		if i := strings.IndexAny(path, "?#"); i >= 0 {
			path = path[:i]
		}
		if decoded, err := url.PathUnescape(path); err == nil {
			path = decoded
		}
		if path == "" {
			return "", false, nil
		}
		path = filepath.FromSlash(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(mdDir, path)
		}
	}
	path = filepath.Clean(path)

	outcome, done := im.assets[path]
	if !done {
		var err error
		if outcome, err = im.storeImage(path, root); err != nil {
			return "", false, err
		}
		im.assets[path] = outcome
	}
	if outcome.reason != "" {
		im.skip(path, outcome.reason)
		return "", false, nil
	}
	return outcome.ref, true, nil
}

// storeImage 校验 path 位于 root 内、各级都不是软链接、是普通图片文件，然后写入附件。
func (im *importer) storeImage(path, root string) (assetOutcome, error) {
	info, reason := lstatInsideRoot(root, path)
	if reason != "" {
		return assetOutcome{reason: reason}, nil
	}
	if !info.Mode().IsRegular() {
		return assetOutcome{reason: skipAssetType}, nil
	}
	if info.Size() > maxAssetBytes {
		return assetOutcome{reason: skipAssetTooLarge}, nil
	}
	data, err := readRegularFile(path, info, maxAssetBytes)
	if errors.Is(err, errFileTooLarge) {
		return assetOutcome{reason: skipAssetTooLarge}, nil
	}
	if err != nil {
		return assetOutcome{reason: skipReadError}, nil
	}
	ref, err := putAsset(filepath.Base(path), data)
	switch {
	case errors.Is(err, errAssetType):
		return assetOutcome{reason: skipAssetType}, nil
	case errors.Is(err, errAssetTooLarge):
		return assetOutcome{reason: skipAssetTooLarge}, nil
	case err != nil:
		return assetOutcome{}, err
	}
	im.report.Assets++
	return assetOutcome{ref: ref}, nil
}

// lstatInsideRoot 检查已 Clean 的 path 位于 root 之下，并从 root 起逐级 Lstat：任何一级是软链接返回
// symlink，不存在返回 missing，越界返回 outside_root。
func lstatInsideRoot(root, path string) (fs.FileInfo, string) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, skipOutsideRoot
	}
	parts := strings.Split(rel, string(filepath.Separator))
	current := root
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, skipMissing
		case err != nil:
			return nil, skipReadError
		case info.Mode()&fs.ModeSymlink != 0:
			return nil, skipSymlink
		case i == len(parts)-1:
			return info, ""
		case !info.IsDir():
			return nil, skipMissing
		}
	}
	return nil, skipMissing
}

type textSegment struct {
	text string
	code bool
}

// splitFences 把正文分成普通文本与围栏代码块（``` / ~~~，含围栏行）；未闭合的围栏延续到正文末尾。
func splitFences(body string) []textSegment {
	var segments []textSegment
	var current strings.Builder
	inCode := false
	var fenceChar byte
	fenceLen := 0
	flush := func(code bool) {
		if current.Len() > 0 {
			segments = append(segments, textSegment{text: current.String(), code: code})
			current.Reset()
		}
	}
	for _, line := range strings.SplitAfter(body, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		char, length := fenceMarker(trimmed)
		switch {
		case !inCode && indent <= 3 && length >= 3 && !(char == '`' && strings.Contains(trimmed[length:], "`")):
			flush(false)
			inCode, fenceChar, fenceLen = true, char, length
			current.WriteString(line)
		case inCode && indent <= 3 && char == fenceChar && length >= fenceLen && strings.TrimSpace(trimmed[length:]) == "":
			current.WriteString(line)
			flush(true)
			inCode = false
		default:
			current.WriteString(line)
		}
	}
	flush(inCode)
	return segments
}

// fenceMarker 返回行首连续的 ` 或 ~ 及其个数。
func fenceMarker(line string) (byte, int) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(line) && line[n] == line[0] {
		n++
	}
	return line[0], n
}

// firstHeading 返回围栏代码块之外第一个一级 ATX 标题（# 标题）的文本。
func firstHeading(body string) string {
	for _, segment := range splitFences(body) {
		if segment.code {
			continue
		}
		for _, line := range strings.Split(segment.text, "\n") {
			if match := headingPattern.FindStringSubmatch(strings.TrimRight(line, "\r")); match != nil {
				if heading := strings.TrimSpace(match[1]); heading != "" {
					return heading
				}
			}
		}
	}
	return ""
}

// cleanImportTitle 把控制字符与双向控制字符换成空格、去首尾空白并截断到标题上限。
func cleanImportTitle(title string) string {
	return truncateRunes(strings.TrimSpace(replaceUnsafeRunes(title, ' ')), maxTitleRunes)
}

func importFolderName(name string) string {
	name = truncateRunes(strings.TrimSpace(replaceUnsafeRunes(name, ' ')), maxFolderNameRunes)
	if name == "" || name == string(filepath.Separator) {
		return untitledName
	}
	return name
}

func replaceUnsafeRunes(value string, replacement rune) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || isBidiControl(r) || r == utf8.RuneError {
			return replacement
		}
		return r
	}, value)
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return strings.TrimSpace(string([]rune(value)[:limit]))
}

// importTags 宽松规整 front matter 中的标签：丢弃空的、过长的或含控制字符的标签，不区分大小写去重，
// 超出数量上限的部分舍弃。
func importTags(raw []string) []string {
	seen := map[string]bool{}
	tags := []string{}
	for _, value := range raw {
		name := strings.TrimSpace(value)
		if name == "" || utf8.RuneCountInString(name) > maxTagRunes || !singleLine(name) || seen[tagKey(name)] {
			continue
		}
		seen[tagKey(name)] = true
		tags = append(tags, name)
		if len(tags) == maxTagsPerNote {
			break
		}
	}
	return tags
}

// ---------- front matter ----------

type frontMatter struct {
	title    string
	tags     []string
	favorite bool
}

// splitFrontMatter 识别文首 --- 包围的 YAML front matter（结束行为 --- 或 ...），只读取 title、
// tags（流式数组、块数组或逗号分隔字符串）与 favorite（布尔），其余键忽略；返回去掉整个块
// （及其后的空行）的正文。块内出现不像 YAML 的行（如分隔线加普通段落），或不含任何已知键
// （title / tags / favorite）时不视为 front matter，整段保留在正文中。
func splitFrontMatter(text string) (frontMatter, string) {
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.SplitAfter(text, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], " \t\r\n") != "---" {
		return frontMatter{}, text
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if marker := strings.TrimRight(lines[i], " \t\r\n"); marker == "---" || marker == "..." {
			end = i
			break
		}
	}
	if end < 0 {
		return frontMatter{}, text
	}
	meta, ok := parseFrontMatter(lines[1:end])
	if !ok {
		return frontMatter{}, text
	}
	body := strings.Join(lines[end+1:], "")
	for {
		switch {
		case strings.HasPrefix(body, "\r\n"):
			body = body[2:]
		case strings.HasPrefix(body, "\n"):
			body = body[1:]
		default:
			return meta, body
		}
	}
}

func parseFrontMatter(lines []string) (frontMatter, bool) {
	var meta frontMatter
	key := ""
	known := false
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " \t\r\n")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' || line[0] == '-' {
			if item, isItem := strings.CutPrefix(trimmed, "-"); isItem && (item == "" || item[0] == ' ' || item[0] == '\t') {
				if key == "tags" {
					if value := yamlScalarValue(strings.TrimSpace(item)); value != "" {
						meta.tags = append(meta.tags, value)
					}
				}
				continue
			}
			if line[0] == '-' {
				return frontMatter{}, false
			}
			continue // 其他键的缩进续行
		}
		name, value, found := strings.Cut(line, ":")
		if !found || !frontMatterKey.MatchString(strings.TrimSpace(name)) {
			return frontMatter{}, false
		}
		key = strings.ToLower(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		switch key {
		case "title", "tags", "favorite":
			known = true
		}
		switch key {
		case "title":
			meta.title = yamlScalarValue(value)
		case "tags":
			meta.tags = parseTagsValue(value)
		case "favorite":
			switch strings.ToLower(yamlScalarValue(value)) {
			case "true", "yes", "on":
				meta.favorite = true
			default:
				meta.favorite = false
			}
		}
	}
	return meta, known
}

// parseTagsValue 解析 tags: 同一行的值：[a, "b, c"] 流式数组，或逗号分隔字符串（可带引号）。
// 为空时等待后续的块数组项。
func parseTagsValue(value string) []string {
	var tags []string
	if strings.HasPrefix(value, "[") {
		inner := strings.TrimPrefix(value, "[")
		if i := strings.LastIndex(inner, "]"); i >= 0 {
			inner = inner[:i]
		}
		for _, item := range splitFlowItems(inner) {
			if tag := yamlScalarValue(strings.TrimSpace(item)); tag != "" {
				tags = append(tags, tag)
			}
		}
		return tags
	}
	for _, item := range strings.Split(yamlScalarValue(value), ",") {
		if tag := strings.TrimSpace(item); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// splitFlowItems 按逗号拆分流式数组，引号内的逗号不拆。
func splitFlowItems(inner string) []string {
	var items []string
	var quote byte
	start := 0
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0 && c == quote:
			if quote == '\'' && i+1 < len(inner) && inner[i+1] == '\'' {
				i++
				continue
			}
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == 0 && c == ',':
			items = append(items, inner[start:i])
			start = i + 1
		}
	}
	return append(items, inner[start:])
}

// yamlScalarValue 解析单个 YAML 标量：双引号（支持转义）、单引号（” 表示 '）或去掉 # 注释的普通值。
func yamlScalarValue(value string) string {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return ""
	case value[0] == '"':
		for i := 1; i < len(value); i++ {
			if value[i] == '\\' {
				i++
				continue
			}
			if value[i] == '"' {
				quoted := value[:i+1]
				if unquoted, err := strconv.Unquote(quoted); err == nil {
					return unquoted
				}
				inner := quoted[1 : len(quoted)-1]
				return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(inner)
			}
		}
		return strings.TrimPrefix(value, `"`)
	case value[0] == '\'':
		var out strings.Builder
		for i := 1; i < len(value); i++ {
			if value[i] == '\'' {
				if i+1 < len(value) && value[i+1] == '\'' {
					out.WriteByte('\'')
					i++
					continue
				}
				return out.String()
			}
			out.WriteByte(value[i])
		}
		return out.String()
	case value[0] == '#':
		return ""
	}
	if i := strings.Index(value, " #"); i >= 0 {
		value = value[:i]
	}
	if i := strings.Index(value, "\t#"); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

// yamlScalar 把字符串写成 YAML 标量：安全时用普通值，否则用双引号（Go 转义与 YAML 双引号转义兼容）。
func yamlScalar(value string) string {
	if yamlPlainSafe(value) {
		return value
	}
	return strconv.Quote(value)
}

func yamlPlainSafe(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value[:1], "-?!&*|>%@`") ||
		strings.ContainsAny(value, ":#,[]{}\"'\\") || yamlReservedWord.MatchString(value) || yamlNumberLike.MatchString(value) {
		return false
	}
	for _, r := range value {
		if !strconv.IsPrint(r) {
			return false
		}
	}
	return true
}

// exportFrontMatter 生成 title、tags、favorite 的 front matter；空值省略，全部为空时不写。
func exportFrontMatter(note Note) string {
	var lines []string
	if strings.TrimSpace(note.Title) != "" {
		lines = append(lines, "title: "+yamlScalar(note.Title))
	}
	if len(note.Tags) > 0 {
		items := make([]string, len(note.Tags))
		for i, tag := range note.Tags {
			items[i] = yamlScalar(tag)
		}
		lines = append(lines, "tags: ["+strings.Join(items, ", ")+"]")
	}
	if note.Favorite {
		lines = append(lines, "favorite: true")
	}
	if len(lines) == 0 {
		return ""
	}
	return "---\n" + strings.Join(lines, "\n") + "\n---\n\n"
}

// ---------- 导出 ----------

type exporter struct {
	ctx      context.Context
	db       *sql.DB
	assetDir string
	result   ExportResult
	// assetDirs 缓存每个导出根目录使用的附件目录名（通常是 assets）。
	assetDirs map[string]string
	// copied 缓存「导出根目录 + 附件文件名」→ 附件目录中的文件名。
	copied  map[string]string
	missing map[string]bool
}

// exportNotes 把 noteIDs 中的笔记写到 destDir，并把 folderID 文件夹导出为 destDir 下的同名目录。
func exportNotes(ctx context.Context, db *sql.DB, noteIDs []string, folderID, destDir string) (ExportResult, error) {
	info, err := os.Stat(destDir)
	if err != nil || !info.IsDir() {
		return ExportResult{}, errExportDest
	}
	// 先确认全部笔记存在再写文件，避免部分写出后才报 note_not_found；正文在写出时逐篇读取。
	for _, id := range noteIDs {
		if err := requireNote(ctx, db, id); err != nil {
			return ExportResult{}, err
		}
	}
	var folder Folder
	var tree Tree
	if folderID != "" {
		if folder, err = loadFolder(ctx, db, folderID); err != nil {
			return ExportResult{}, err
		}
		if tree, err = loadTree(ctx, db); err != nil {
			return ExportResult{}, err
		}
	}
	assetDir, err := assetsDir()
	if err != nil {
		return ExportResult{}, err
	}
	ex := &exporter{
		ctx:       ctx,
		db:        db,
		assetDir:  assetDir,
		result:    ExportResult{Written: []string{}, MissingAssets: []string{}},
		assetDirs: map[string]string{},
		copied:    map[string]string{},
		missing:   map[string]bool{},
	}
	for _, id := range noteIDs {
		note, err := getNote(ctx, db, id)
		if errors.Is(err, errNoteNotFound) {
			continue // 导出期间被删除
		}
		if err != nil {
			return ex.result, err
		}
		if err := ex.writeNote(note, destDir, destDir, 0); err != nil {
			return ex.result, err
		}
	}
	if folderID != "" {
		dir, err := createUniqueDir(destDir, safeExportName(folder.Name), false)
		if err != nil {
			return ex.result, err
		}
		if err := ex.writeFolder(tree, folder.ID, dir, dir, 0); err != nil {
			return ex.result, err
		}
	}
	return ex.result, nil
}

// writeFolder 写出 folderID 中的笔记（按 position），再递归写出子文件夹。base 是附件所在的导出根目录。
func (ex *exporter) writeFolder(tree Tree, folderID, dir, base string, depth int) error {
	for _, summary := range tree.Notes {
		if summary.FolderID != folderID {
			continue
		}
		note, err := getNote(ex.ctx, ex.db, summary.ID)
		if errors.Is(err, errNoteNotFound) {
			continue // 导出期间被删除
		}
		if err != nil {
			return err
		}
		if err := ex.writeNote(note, dir, base, depth); err != nil {
			return err
		}
	}
	for _, child := range tree.Folders {
		if child.ParentID != folderID {
			continue
		}
		childDir, err := createUniqueDir(dir, safeExportName(child.Name), depth == 0)
		if err != nil {
			return err
		}
		if err := ex.writeFolder(tree, child.ID, childDir, base, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (ex *exporter) writeNote(note Note, dir, base string, depth int) error {
	if err := ex.ctx.Err(); err != nil {
		return err
	}
	body, err := ex.rewriteAssets(note.Body, base, depth)
	if err != nil {
		return err
	}
	path, err := createUniqueFile(dir, safeExportName(note.Title), ".md", strings.NewReader(exportFrontMatter(note)+body))
	if err != nil {
		return err
	}
	ex.result.Written = append(ex.result.Written, path)
	return nil
}

// rewriteAssets 把正文中的 /__notes-assets/<文件名> 复制到 base 下的附件目录并改写为相对路径；
// 附件目录中找不到的保留原文并记入 MissingAssets。
func (ex *exporter) rewriteAssets(body, base string, depth int) (string, error) {
	var out strings.Builder
	last := 0
	for _, match := range exportAssetPattern.FindAllStringSubmatchIndex(body, -1) {
		start, end := match[2], match[3]
		ref, name := body[start:end], body[match[4]:match[5]]
		copied, ok, err := ex.copyAsset(base, name)
		if err != nil {
			return "", err
		}
		if !ok {
			if !ex.missing[ref] {
				ex.missing[ref] = true
				ex.result.MissingAssets = append(ex.result.MissingAssets, ref)
			}
			continue
		}
		dirName, err := ex.exportAssetsDir(base)
		if err != nil {
			return "", err
		}
		out.WriteString(body[last:start])
		out.WriteString(strings.Repeat("../", depth) + escapeRefSegment(dirName) + "/" + escapeRefSegment(copied))
		last = end
	}
	out.WriteString(body[last:])
	return out.String(), nil
}

func escapeRefSegment(name string) string {
	return strings.ReplaceAll(name, " ", "%20")
}

// copyAsset 把附件复制到 base 的附件目录，返回目标文件名；源文件不存在时 ok 为 false。
// 目标已存在且内容相同时直接复用，否则改用「名称 (2).扩展名」等，从不覆盖。
func (ex *exporter) copyAsset(base, name string) (string, bool, error) {
	key := base + "\x00" + name
	if copied, ok := ex.copied[key]; ok {
		return copied, true, nil
	}
	src := filepath.Join(ex.assetDir, name)
	info, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("notes: read asset: %w", err)
	}
	dirName, err := ex.exportAssetsDir(base)
	if err != nil {
		return "", false, err
	}
	dir := filepath.Join(base, dirName)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 1; n <= maxUniqueNameAttempts; n++ {
		candidate := numberedName(stem, ext, n)
		target := filepath.Join(dir, candidate)
		created, err := copyFileExclusive(src, target)
		if err != nil {
			return "", false, err
		}
		if created || sameFileContent(target, src) {
			ex.copied[key] = candidate
			return candidate, true, nil
		}
	}
	return "", false, fmt.Errorf("notes: export asset %s: too many name conflicts", name)
}

// exportAssetsDir 返回 base 下用于存放图片的目录名：已有普通目录 assets 时复用，不存在时创建，
// 被文件或软链接占用时改用 assets (2) 等。
func (ex *exporter) exportAssetsDir(base string) (string, error) {
	if name, ok := ex.assetDirs[base]; ok {
		return name, nil
	}
	for n := 1; n <= maxUniqueNameAttempts; n++ {
		name := numberedName(exportAssetsDirName, "", n)
		path := filepath.Join(base, name)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", fmt.Errorf("notes: create export assets dir: %w", err)
			}
			info, err = os.Lstat(path)
		}
		if err != nil {
			return "", fmt.Errorf("notes: export assets dir: %w", err)
		}
		if info.IsDir() {
			ex.assetDirs[base] = name
			return name, nil
		}
	}
	return "", errors.New("notes: export assets dir: too many name conflicts")
}

// copyFileExclusive 以 O_EXCL 新建 target 并复制 src；target 已存在时返回 created=false。
func copyFileExclusive(src, target string) (created bool, err error) {
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("notes: export asset: %w", err)
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(target)
		}
	}()
	in, err := os.Open(src)
	if err != nil {
		return false, fmt.Errorf("notes: export asset: %w", err)
	}
	defer in.Close()
	if _, err = io.Copy(out, in); err != nil {
		return false, fmt.Errorf("notes: export asset: %w", err)
	}
	if err = out.Close(); err != nil {
		return false, fmt.Errorf("notes: export asset: %w", err)
	}
	return true, nil
}

// sameFileContent 判断 a 是与 b 内容相同的普通文件（附件不超过 20 MiB，整读比较）。
func sameFileContent(a, b string) bool {
	infoA, err := os.Lstat(a)
	if err != nil || !infoA.Mode().IsRegular() {
		return false
	}
	infoB, err := os.Stat(b)
	if err != nil || infoA.Size() != infoB.Size() {
		return false
	}
	dataA, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	dataB, err := os.ReadFile(b)
	return err == nil && bytes.Equal(dataA, dataB)
}

func numberedName(stem, ext string, n int) string {
	if n == 1 {
		return stem + ext
	}
	return fmt.Sprintf("%s (%d)%s", stem, n, ext)
}

// createUniqueFile 以 O_EXCL 新建 dir/name.ext（重名依次加序号）并写入内容，返回绝对路径。
func createUniqueFile(dir, name, ext string, content io.Reader) (path string, err error) {
	for n := 1; n <= maxUniqueNameAttempts; n++ {
		path = filepath.Join(dir, numberedName(name, ext, n))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("notes: export note: %w", err)
		}
		if _, err := io.Copy(file, content); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return "", fmt.Errorf("notes: export note: %w", err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("notes: export note: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("notes: export note %s: too many name conflicts", name)
}

// createUniqueDir 新建 dir/name（重名依次加序号）；reserveAssets 时跳过 assets（留给图片目录）。
func createUniqueDir(dir, name string, reserveAssets bool) (string, error) {
	for n := 1; n <= maxUniqueNameAttempts; n++ {
		candidate := numberedName(name, "", n)
		if reserveAssets && strings.EqualFold(candidate, exportAssetsDirName) {
			continue
		}
		path := filepath.Join(dir, candidate)
		err := os.Mkdir(path, 0o755)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("notes: export folder: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("notes: export folder %s: too many name conflicts", name)
}

// safeExportName 把标题 / 文件夹名转为跨平台安全的文件名主体：/\:*?"<>| 与控制字符换成 _，
// 开头的 . 换成 _（避免隐藏文件与 ..），去掉结尾的空格和点，截断到上限，Windows 保留名（含 COM¹ 等）
// 在第一个点之前加 _；
// 结果为空时用「无标题」。
func safeExportName(name string) string {
	safe := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsControl(r) || isBidiControl(r) || r == utf8.RuneError {
			return '_'
		}
		return r
	}, name)
	safe = strings.TrimSpace(safe)
	if strings.HasPrefix(safe, ".") {
		safe = "_" + safe[1:]
	}
	if len(safe) > maxExportNameBytes {
		cut := maxExportNameBytes
		for cut > 0 && !utf8.RuneStart(safe[cut]) {
			cut--
		}
		safe = safe[:cut]
	}
	safe = strings.TrimRight(safe, ". ")
	if safe == "" {
		return untitledName
	}
	// Windows 按第一个点之前的部分判断设备名（con.txt、NUL.tar.gz 都是设备），所以 _ 插在第一个点之前。
	stem, rest, hasDot := strings.Cut(safe, ".")
	if windowsReserved.MatchString(strings.TrimRight(stem, " ")) {
		safe = stem + "_"
		if hasDot {
			safe += "." + rest
		}
	}
	return safe
}
