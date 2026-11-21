package notes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/core"
)

const (
	// maxRequestBytes 限制单个命令请求体；正文上限之外留出 JSON 转义余量。
	maxRequestBytes = 12 << 20
	// maxBodyBytes 是单篇笔记正文上限（UTF-8 字节）；图片走附件，不进正文。
	maxBodyBytes       = 4 << 20
	maxTitleRunes      = 500
	maxFolderNameRunes = 200
	maxIDBytes         = 128
	maxJSONDepth       = 16
)

func init() {
	register("notes_tree", handleTree)
	register("notes_get", handleGet)
	register("notes_create", handleCreate)
	register("notes_update", handleUpdate)
	register("notes_delete", handleDelete)
	register("notes_move", handleMove)
	register("notes_folder_create", handleFolderCreate)
	register("notes_folder_rename", handleFolderRename)
	register("notes_folder_move", handleFolderMove)
	register("notes_folder_delete", handleFolderDelete)
	register("notes_tags_list", handleTagsList)
	register("notes_tags_set", handleTagsSet)
	register("notes_links_add", handleLinksAdd)
	register("notes_links_remove", handleLinksRemove)
	register("notes_links_for_object", handleLinksForObject)
	register("notes_links_counts", handleLinksCounts)
	register("notes_links_all", handleLinksAll)
	register("notes_search", handleSearch)
	registerSized("notes_asset_put", maxAssetRequestBytes, errAssetTooLarge, handleAssetPut)
	register("notes_import_files", handleImportFiles)
	register("notes_import_folder", handleImportFolder)
	register("notes_export", handleExport)
}

// register 注册一个命令：限制请求体、严格解码参数到 A，再把结果编码为 JSON。
func register[A any](name string, run func(context.Context, A) (any, error)) {
	registerSized(name, maxRequestBytes, errRequestTooLarge, run)
}

// registerSized 与 register 相同，但使用自定义的请求体上限与超限错误（图片上传的 base64 体积更大）。
func registerSized[A any](name string, limit int, tooLarge error, run func(context.Context, A) (any, error)) {
	core.RegisterCtx(name, func(ctx context.Context, _, raw string) (string, error) {
		if len(raw) > limit {
			return "", tooLarge
		}
		var args A
		if err := decodeStrict(raw, &args); err != nil {
			return "", err
		}
		result, err := run(ctx, args)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(result)
	})
}

type empty struct{}

func handleTree(ctx context.Context, _ empty) (any, error) {
	startAssetCollector()
	db, err := openNotesReadDB()
	if err != nil {
		return nil, err
	}
	return loadTree(ctx, db)
}

type idArgs struct {
	ID *string `json:"id"`
}

func handleGet(ctx context.Context, args idArgs) (any, error) {
	if !validID(args.ID) {
		return nil, errInvalidRequest
	}
	db, err := openNotesReadDB()
	if err != nil {
		return nil, err
	}
	return getNote(ctx, db, *args.ID)
}

type createArgs struct {
	FolderID *string `json:"folderId"`
	Title    *string `json:"title"`
	Body     *string `json:"body"`
}

func handleCreate(ctx context.Context, args createArgs) (any, error) {
	folderID, title, body := deref(args.FolderID), deref(args.Title), deref(args.Body)
	if !validOptionalID(folderID) || !validTitle(title) || !validBody(body) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return createNote(ctx, db, folderID, title, body)
}

type updateArgs struct {
	ID       *string `json:"id"`
	Title    *string `json:"title"`
	Body     *string `json:"body"`
	Favorite *bool   `json:"favorite"`
}

func handleUpdate(ctx context.Context, args updateArgs) (any, error) {
	if !validID(args.ID) || (args.Title != nil && !validTitle(*args.Title)) || (args.Body != nil && !validBody(*args.Body)) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return updateNote(ctx, db, *args.ID, args.Title, args.Body, args.Favorite)
}

func handleDelete(ctx context.Context, args idArgs) (any, error) {
	if !validID(args.ID) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return empty{}, deleteNote(ctx, db, *args.ID)
}

type moveArgs struct {
	ID       *string  `json:"id"`
	FolderID *string  `json:"folderId"`
	Position *float64 `json:"position"`
}

func handleMove(ctx context.Context, args moveArgs) (any, error) {
	if !validID(args.ID) || args.FolderID == nil || !validOptionalID(*args.FolderID) || !validPosition(args.Position) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return empty{}, moveNote(ctx, db, *args.ID, *args.FolderID, *args.Position)
}

type folderCreateArgs struct {
	ParentID *string `json:"parentId"`
	Name     *string `json:"name"`
}

func handleFolderCreate(ctx context.Context, args folderCreateArgs) (any, error) {
	parentID := deref(args.ParentID)
	name, ok := folderName(args.Name)
	if !ok || !validOptionalID(parentID) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return createFolder(ctx, db, parentID, name)
}

type folderRenameArgs struct {
	ID   *string `json:"id"`
	Name *string `json:"name"`
}

func handleFolderRename(ctx context.Context, args folderRenameArgs) (any, error) {
	name, ok := folderName(args.Name)
	if !ok || !validID(args.ID) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return empty{}, renameFolder(ctx, db, *args.ID, name)
}

type folderMoveArgs struct {
	ID       *string  `json:"id"`
	ParentID *string  `json:"parentId"`
	Position *float64 `json:"position"`
}

func handleFolderMove(ctx context.Context, args folderMoveArgs) (any, error) {
	if !validID(args.ID) || args.ParentID == nil || !validOptionalID(*args.ParentID) || !validPosition(args.Position) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return empty{}, moveFolder(ctx, db, *args.ID, *args.ParentID, *args.Position)
}

func handleFolderDelete(ctx context.Context, args idArgs) (any, error) {
	if !validID(args.ID) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return empty{}, deleteFolder(ctx, db, *args.ID)
}

func handleTagsList(ctx context.Context, _ empty) (any, error) {
	db, err := openNotesReadDB()
	if err != nil {
		return nil, err
	}
	return listTags(ctx, db)
}

type tagsSetArgs struct {
	ID   *string   `json:"id"`
	Tags *[]string `json:"tags"`
}

func handleTagsSet(ctx context.Context, args tagsSetArgs) (any, error) {
	tags, ok := normalizeTags(args.Tags)
	if !ok || !validID(args.ID) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return setTags(ctx, db, *args.ID, tags)
}

type linksAddArgs struct {
	ID   *string `json:"id"`
	Link *Link   `json:"link"`
}

func handleLinksAdd(ctx context.Context, args linksAddArgs) (any, error) {
	if !validID(args.ID) || args.Link == nil {
		return nil, errInvalidRequest
	}
	link := *args.Link
	if !linkObjectTypes[link.ObjectType] {
		return nil, errLinkType
	}
	if !validLinkID(link.ObjectID, true) || !validLinkID(link.ParentID, false) || !validLinkID(link.ScopeID, false) ||
		!validSnapshot(link.NameSnapshot) || !validSnapshot(link.ScopeNameSnapshot) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return addLink(ctx, db, *args.ID, link)
}

type linksRemoveArgs struct {
	ID         *string `json:"id"`
	ObjectType string  `json:"objectType"`
	ObjectID   string  `json:"objectId"`
	ScopeID    string  `json:"scopeId"`
}

func handleLinksRemove(ctx context.Context, args linksRemoveArgs) (any, error) {
	if !validID(args.ID) {
		return nil, errInvalidRequest
	}
	if !linkObjectTypes[args.ObjectType] {
		return nil, errLinkType
	}
	if !validLinkID(args.ObjectID, true) || !validLinkID(args.ScopeID, false) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return empty{}, removeLink(ctx, db, *args.ID, args.ObjectType, args.ObjectID, args.ScopeID)
}

type linksForObjectArgs struct {
	ObjectType string  `json:"objectType"`
	ObjectID   string  `json:"objectId"`
	ScopeID    *string `json:"scopeId"`
}

// handleLinksForObject：scopeId 省略时匹配任意工作区，给出（含空串）时精确匹配。
func handleLinksForObject(ctx context.Context, args linksForObjectArgs) (any, error) {
	if !linkObjectTypes[args.ObjectType] {
		return nil, errLinkType
	}
	if !validLinkID(args.ObjectID, true) || (args.ScopeID != nil && !validLinkID(*args.ScopeID, false)) {
		return nil, errInvalidRequest
	}
	db, err := openNotesReadDB()
	if err != nil {
		return nil, err
	}
	return notesForObject(ctx, db, args.ObjectType, args.ObjectID, args.ScopeID)
}

type linksCountsArgs struct {
	ObjectType string    `json:"objectType"`
	ObjectIDs  *[]string `json:"objectIds"`
}

func handleLinksCounts(ctx context.Context, args linksCountsArgs) (any, error) {
	if !linkObjectTypes[args.ObjectType] {
		return nil, errLinkType
	}
	if args.ObjectIDs == nil || len(*args.ObjectIDs) > maxCountObjectIDs {
		return nil, errInvalidRequest
	}
	for _, id := range *args.ObjectIDs {
		if !validLinkID(id, true) {
			return nil, errInvalidRequest
		}
	}
	db, err := openNotesReadDB()
	if err != nil {
		return nil, err
	}
	return linkCounts(ctx, db, args.ObjectType, *args.ObjectIDs)
}

func handleLinksAll(ctx context.Context, _ empty) (any, error) {
	db, err := openNotesReadDB()
	if err != nil {
		return nil, err
	}
	return allLinks(ctx, db)
}

type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// handleSearch：搜索词去首尾空白后为空时返回 []；limit 未给出或非正数用默认值，超过上限按上限。
func handleSearch(ctx context.Context, args searchArgs) (any, error) {
	query := strings.TrimSpace(args.Query)
	if utf8.RuneCountInString(query) > maxQueryRunes || strings.ContainsRune(query, 0) {
		return nil, errInvalidRequest
	}
	if query == "" {
		return []SearchResult{}, nil
	}
	db, err := openNotesReadDB()
	if err != nil {
		return nil, err
	}
	return searchNotes(ctx, db, query, searchLimit(args.Limit))
}

type importFilesArgs struct {
	Paths    *[]string `json:"paths"`
	FolderID *string   `json:"folderId"`
}

// handleImportFiles：paths 必须给出（可为空数组），每项是 .md / .markdown 绝对路径。
func handleImportFiles(ctx context.Context, args importFilesArgs) (any, error) {
	folderID := deref(args.FolderID)
	if args.Paths == nil || len(*args.Paths) > maxImportPaths || !validOptionalID(folderID) {
		return nil, errInvalidRequest
	}
	for _, path := range *args.Paths {
		if !validImportPath(path) || !isMarkdownName(path) {
			return nil, errInvalidRequest
		}
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return importFiles(ctx, db, *args.Paths, folderID)
}

type importFolderArgs struct {
	Path     *string `json:"path"`
	FolderID *string `json:"folderId"`
}

func handleImportFolder(ctx context.Context, args importFolderArgs) (any, error) {
	folderID := deref(args.FolderID)
	if args.Path == nil || !validImportPath(*args.Path) || !validOptionalID(folderID) {
		return nil, errInvalidRequest
	}
	db, err := openNotesDB()
	if err != nil {
		return nil, err
	}
	return importFolder(ctx, db, *args.Path, folderID)
}

type exportArgs struct {
	NoteIDs  *[]string `json:"noteIds"`
	FolderID *string   `json:"folderId"`
	DestDir  *string   `json:"destDir"`
}

// handleExport：destDir 必须是已存在的绝对目录；noteIds 与 folderId 都为空时返回 note_export_empty。
func handleExport(ctx context.Context, args exportArgs) (any, error) {
	folderID := deref(args.FolderID)
	if args.DestDir == nil || !validImportPath(*args.DestDir) || !validOptionalID(folderID) {
		return nil, errInvalidRequest
	}
	var noteIDs []string
	if args.NoteIDs != nil {
		if len(*args.NoteIDs) > maxExportNoteIDs {
			return nil, errInvalidRequest
		}
		seen := map[string]bool{}
		for _, id := range *args.NoteIDs {
			if !validID(&id) {
				return nil, errInvalidRequest
			}
			if !seen[id] {
				seen[id] = true
				noteIDs = append(noteIDs, id)
			}
		}
	}
	if len(noteIDs) == 0 && folderID == "" {
		return nil, errExportEmpty
	}
	db, err := openNotesReadDB()
	if err != nil {
		return nil, err
	}
	return exportNotes(ctx, db, noteIDs, folderID, filepath.Clean(*args.DestDir))
}

// normalizeTags 去掉每个标签首尾空白、丢弃空标签、按不区分大小写去重（保留首次出现的写法），
// 并校验单行、长度与数量上限。tags 必须给出（可为空数组）。
func normalizeTags(raw *[]string) ([]string, bool) {
	if raw == nil {
		return nil, false
	}
	seen := map[string]bool{}
	tags := []string{}
	for _, value := range *raw {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		if utf8.RuneCountInString(name) > maxTagRunes || !singleLine(name) {
			return nil, false
		}
		if key := tagKey(name); !seen[key] {
			seen[key] = true
			tags = append(tags, name)
		}
	}
	return tags, len(tags) <= maxTagsPerNote
}

// validLinkID 校验关联对象的 objectId / parentId / scopeId：单行、不超过 512 字节；required 时不能为空。
func validLinkID(id string, required bool) bool {
	return (!required || id != "") && len(id) <= maxLinkIDBytes && singleLine(id)
}

func validSnapshot(name string) bool {
	return utf8.RuneCountInString(name) <= maxSnapshotRunes && singleLine(name)
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validID(id *string) bool {
	return id != nil && *id != "" && validOptionalID(*id)
}

func validOptionalID(id string) bool {
	return len(id) <= maxIDBytes && singleLine(id)
}

func validPosition(position *float64) bool {
	return position != nil && !math.IsNaN(*position) && !math.IsInf(*position, 0)
}

func validTitle(title string) bool {
	return utf8.RuneCountInString(title) <= maxTitleRunes && singleLine(title)
}

func validBody(body string) bool {
	return len(body) <= maxBodyBytes && utf8.ValidString(body) && !strings.ContainsRune(body, 0)
}

// folderName 去掉首尾空白后校验：非空、单行、不超过上限。
func folderName(raw *string) (string, bool) {
	if raw == nil {
		return "", false
	}
	name := strings.TrimSpace(*raw)
	if name == "" || utf8.RuneCountInString(name) > maxFolderNameRunes || !singleLine(name) {
		return "", false
	}
	return name, true
}

// singleLine 拒绝控制字符（Cc，含换行）与双向嵌入 / 覆盖 / 隔离字符（可伪装显示顺序）。
// ZWJ（组合 emoji）、ZWNJ（波斯文等）、软连字符、LRM/RLM 等其余格式字符是合法文本，保留。
func singleLine(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.Is(unicode.Cc, r) || isBidiControl(r) {
			return false
		}
	}
	return true
}

// isBidiControl 匹配 U+202A–U+202E（LRE/RLE/PDF/LRO/RLO）与 U+2066–U+2069（LRI/RLI/FSI/PDI）。
func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// decodeStrict 要求请求体是单个 JSON 对象：有界、合法 UTF-8、无重复键、无未知字段、无尾随内容。
// 空请求体按 {} 处理。
// 请求体大小由调用方（registerSized）先行检查。
func decodeStrict(raw string, out any) error {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	data := []byte(raw)
	if !utf8.Valid(data) {
		return errInvalidRequest
	}
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errInvalidRequest
	}
	if err := checkJSONShape(data); err != nil {
		return errInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return errInvalidRequest
	}
	return nil
}

// checkJSONShape 逐 token 遍历：限制嵌套深度、拒绝重复键，并确认只有一个顶层值。
func checkJSONShape(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func(depth int) error
	walk = func(depth int) error {
		if depth > maxJSONDepth {
			return errInvalidRequest
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errInvalidRequest
				}
				seen[name] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errInvalidRequest
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errInvalidRequest
	}
	return nil
}
