/**
 * 内置笔记的前端类型，字段名与可空性与 Go 后端 `backend/notes/types.go` 的 JSON 形状一致。
 * 时间字段一律为 Unix 毫秒；空串 ID（folderId / parentId）表示根目录。
 */

/** 可关联的 MinCode 对象类型：SSH 连接、项目、数据库连接、AI 对话、HTTP 已保存请求。 */
export type NoteLinkType = "ssh" | "project" | "database" | "ai" | "http";

export interface NoteFolder {
  id: string;
  /** 空串表示根目录。 */
  parentId: string;
  name: string;
  position: number;
  createdAt: number;
  updatedAt: number;
}

/** 树与列表使用的笔记摘要（不含正文）。 */
export interface NoteSummary {
  id: string;
  /** 空串表示根目录。 */
  folderId: string;
  title: string;
  favorite: boolean;
  position: number;
  updatedAt: number;
  /** 始终为数组。 */
  tags: string[];
}

/** 笔记与 MinCode 对象的关联；快照字段在对象被删除后仍用于显示。 */
export interface NoteLink {
  objectType: NoteLinkType;
  objectId: string;
  parentId: string;
  scopeId: string;
  nameSnapshot: string;
  scopeNameSnapshot: string;
  createdAt: number;
}

/** notes_links_add 的关联参数：createdAt 由后端填写，除 objectType / objectId 外均可省略（按空串处理）。 */
export interface NoteLinkInput {
  objectType: NoteLinkType;
  objectId: string;
  parentId?: string;
  scopeId?: string;
  nameSnapshot?: string;
  scopeNameSnapshot?: string;
}

export interface Note extends NoteSummary {
  body: string;
  createdAt: number;
  /** 始终为数组。 */
  links: NoteLink[];
}

/** 笔记页左栏的全部文件夹与笔记摘要，各自按 position 升序。 */
export interface NoteTree {
  folders: NoteFolder[];
  notes: NoteSummary[];
}

/** 标签及引用它的笔记数（notes_tags_list）。 */
export interface NoteTagCount {
  name: string;
  count: number;
}

/** 一条关联及其所属笔记（notes_links_all）。 */
export interface NoteLinkEntry {
  link: NoteLink;
  noteId: string;
  noteTitle: string;
}

/** 搜索命中；snippet 为正文命中处前后各 40 个字符（只命中标题时取正文开头）。 */
export interface NoteSearchHit {
  note: NoteSummary;
  snippet: string;
}

/** 导入时跳过某个文件的原因（与后端 markdown_io.go 的 skip* 常量一致）。 */
export type NoteImportSkipReason =
  | "outside_root"
  | "symlink"
  | "asset_type"
  | "asset_too_large"
  | "missing"
  | "too_large"
  | "read_error";

export interface NoteImportSkippedItem {
  path: string;
  /** 通常是 NoteImportSkipReason；保留 string 以容忍后端新增原因。 */
  reason: NoteImportSkipReason | (string & {});
}

/** notes_import_files / notes_import_folder 的结果。 */
export interface NoteImportReport {
  notes: number;
  /** 写入的图片数（同一图片文件只计一次）。 */
  assets: number;
  /** 始终为数组；最多保留前若干项，全部数量见 skippedTotal。 */
  skipped: NoteImportSkippedItem[];
  skippedTotal: number;
  /** 文件夹导入达到条目或笔记上限而提前停止。 */
  truncated: boolean;
}

/** notes_export 的结果；两者始终为数组。 */
export interface NoteExportResult {
  /** 写出的 .md 绝对路径（按写入顺序）。 */
  written: string[];
  /** 附件目录中找不到、因而保留原引用的图片引用（/__notes-assets/<文件名>）。 */
  missingAssets: string[];
}

export interface CreateNoteInput {
  folderId?: string;
  title?: string;
  body?: string;
}

/** notes_update 的补丁：省略的字段保持不变。 */
export interface UpdateNoteInput {
  id: string;
  title?: string;
  body?: string;
  favorite?: boolean;
}

export interface ExportNotesInput {
  noteIds?: string[];
  folderId?: string;
  /** 已存在的绝对目录。 */
  destDir: string;
}
