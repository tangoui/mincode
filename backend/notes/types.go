// Package notes 实现 MinCode 内置笔记：多层文件夹、Markdown 笔记、标签、对象关联与图片附件。
//
// 笔记是全局数据，不按工作区隔离，存放在共享应用数据库（backend/storage）。
// 时间字段一律为 Unix 毫秒。
package notes

import "errors"

// 稳定英文错误码：命令失败时作为错误文本原样返回，前端映射为中英文文案。
const (
	ErrInvalidRequest  = "note_invalid_request"
	ErrRequestTooLarge = "note_request_too_large"
	ErrNoteNotFound    = "note_not_found"
	ErrFolderNotFound  = "note_folder_not_found"
	ErrFolderCycle     = "note_folder_cycle"
	ErrLinkType        = "note_link_type"
	ErrAssetTooLarge   = "note_asset_too_large"
	ErrAssetType       = "note_asset_type"
	ErrImportPath      = "note_import_path"
	ErrExportDest      = "note_export_dest"
	ErrExportEmpty     = "note_export_empty"
)

var (
	errInvalidRequest  = errors.New(ErrInvalidRequest)
	errRequestTooLarge = errors.New(ErrRequestTooLarge)
	errNoteNotFound    = errors.New(ErrNoteNotFound)
	errFolderNotFound  = errors.New(ErrFolderNotFound)
	errFolderCycle     = errors.New(ErrFolderCycle)
	errLinkType        = errors.New(ErrLinkType)
	errAssetTooLarge   = errors.New(ErrAssetTooLarge)
	errAssetType       = errors.New(ErrAssetType)
	errImportPath      = errors.New(ErrImportPath)
	errExportDest      = errors.New(ErrExportDest)
	errExportEmpty     = errors.New(ErrExportEmpty)
)

// Folder 是笔记文件夹；ParentID 为空表示根目录。
type Folder struct {
	ID        string  `json:"id"`
	ParentID  string  `json:"parentId"`
	Name      string  `json:"name"`
	Position  float64 `json:"position"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
}

// NoteSummary 是树与列表使用的笔记摘要（不含正文）；FolderID 为空表示根目录。
// Tags 始终编码为数组，不为 null。
type NoteSummary struct {
	ID        string   `json:"id"`
	FolderID  string   `json:"folderId"`
	Title     string   `json:"title"`
	Favorite  bool     `json:"favorite"`
	Position  float64  `json:"position"`
	UpdatedAt int64    `json:"updatedAt"`
	Tags      []string `json:"tags"`
}

// Note 是完整笔记。Links 始终编码为数组，不为 null。
type Note struct {
	NoteSummary
	Body      string `json:"body"`
	CreatedAt int64  `json:"createdAt"`
	Links     []Link `json:"links"`
}

// Link 是笔记与 MinCode 对象（SSH 连接、项目、数据库连接、AI 对话、HTTP 请求）的关联。
// 快照字段在对象被删除后仍用于显示。
type Link struct {
	ObjectType        string `json:"objectType"`
	ObjectID          string `json:"objectId"`
	ParentID          string `json:"parentId"`
	ScopeID           string `json:"scopeId"`
	NameSnapshot      string `json:"nameSnapshot"`
	ScopeNameSnapshot string `json:"scopeNameSnapshot"`
	CreatedAt         int64  `json:"createdAt"`
}

// Tree 是笔记页左栏需要的全部文件夹与笔记摘要，各自按 position 升序。
type Tree struct {
	Folders []Folder      `json:"folders"`
	Notes   []NoteSummary `json:"notes"`
}

// TagCount 是标签及引用它的笔记数（notes_tags_list）。
type TagCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// LinkEntry 是一条关联及其所属笔记（notes_links_all，笔记页「关联」分组用）。
type LinkEntry struct {
	Link      Link   `json:"link"`
	NoteID    string `json:"noteId"`
	NoteTitle string `json:"noteTitle"`
}

// SearchResult 是一条搜索命中；Snippet 为正文命中处前后各 40 个字符（只命中标题时取正文开头）。
type SearchResult struct {
	Note    NoteSummary `json:"note"`
	Snippet string      `json:"snippet"`
}

// SkippedItem 是导入时跳过的一个 Markdown 文件或图片；Reason 取值见 markdown_io.go 的 skip* 常量。
type SkippedItem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ImportReport 是 notes_import_files / notes_import_folder 的结果：导入的笔记数、写入的图片数
// （同一图片文件只计一次）与跳过项（按遇到的顺序）。Skipped 始终编码为数组。
// 文件夹导入达到条目或笔记上限而提前停止时 Truncated 为 true；Skipped 最多保留前若干项，
// SkippedTotal 是全部跳过项的数量。
type ImportReport struct {
	Notes        int           `json:"notes"`
	Assets       int           `json:"assets"`
	Skipped      []SkippedItem `json:"skipped"`
	SkippedTotal int           `json:"skippedTotal"`
	Truncated    bool          `json:"truncated"`
}

// ExportResult 是 notes_export 的结果：写出的 .md 绝对路径（按写入顺序），以及附件目录中找不到、
// 因而保留原引用的图片引用（/__notes-assets/<文件名>）。两者始终编码为数组。
type ExportResult struct {
	Written       []string `json:"written"`
	MissingAssets []string `json:"missingAssets"`
}
