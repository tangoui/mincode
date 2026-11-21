import type {
  CreateNoteInput,
  ExportNotesInput,
  Note,
  NoteExportResult,
  NoteFolder,
  NoteImportReport,
  NoteImportSkipReason,
  NoteLink,
  NoteLinkEntry,
  NoteLinkInput,
  NoteLinkType,
  NoteSearchHit,
  NoteSummary,
  NoteTagCount,
  NoteTree,
  UpdateNoteInput,
} from "@/domain/notes/types";
import type { MessageKey } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime, nativeErrorMessage, type BackendCommand } from "@/services/desktop-bridge";

/** 单张图片上限，与后端 maxAssetBytes（20 << 20）一致。 */
export const NOTE_ASSET_MAX_BYTES = 20 * 1024 * 1024;

/** 笔记只在桌面端可用；浏览器预览直接拒绝，不伪造成功。 */
async function call<T>(command: BackendCommand, args: Record<string, unknown> = {}): Promise<T> {
  if (!isDesktopRuntime()) throw new Error("notes_desktop_only");
  return invokeBackend<T>(command, args);
}

export function getNoteTree() {
  return call<NoteTree>("notes_tree");
}

export function getNote(id: string) {
  return call<Note>("notes_get", { id });
}

export function createNote(input: CreateNoteInput = {}) {
  return call<Note>("notes_create", { ...input });
}

/** 只更新补丁中给出的字段，返回更新后的摘要。 */
export function updateNote(input: UpdateNoteInput) {
  return call<NoteSummary>("notes_update", { ...input });
}

export async function deleteNote(id: string) {
  await call<unknown>("notes_delete", { id });
}

/** folderId 为空串表示根目录。 */
export async function moveNote(id: string, folderId: string, position: number) {
  await call<unknown>("notes_move", { id, folderId, position });
}

/** parentId 为空串或省略表示根目录。 */
export function createNoteFolder(name: string, parentId = "") {
  return call<NoteFolder>("notes_folder_create", { parentId, name });
}

export async function renameNoteFolder(id: string, name: string) {
  await call<unknown>("notes_folder_rename", { id, name });
}

/** parentId 为空串表示根目录。 */
export async function moveNoteFolder(id: string, parentId: string, position: number) {
  await call<unknown>("notes_folder_move", { id, parentId, position });
}

export async function deleteNoteFolder(id: string) {
  await call<unknown>("notes_folder_delete", { id });
}

export function listNoteTags() {
  return call<NoteTagCount[]>("notes_tags_list");
}

/** 用给定标签整体替换笔记的标签（后端去空白、按不区分大小写去重）。 */
export function setNoteTags(id: string, tags: string[]) {
  return call<NoteSummary>("notes_tags_set", { id, tags });
}

export function addNoteLink(id: string, link: NoteLinkInput) {
  return call<NoteLink>("notes_links_add", { id, link: { ...link } });
}

export async function removeNoteLink(id: string, objectType: NoteLinkType, objectId: string, scopeId = "") {
  await call<unknown>("notes_links_remove", { id, objectType, objectId, scopeId });
}

/** scopeId 省略时匹配任意工作区；给出（含空串）时精确匹配。 */
export function listNotesForObject(objectType: NoteLinkType, objectId: string, scopeId?: string) {
  const args: Record<string, unknown> = { objectType, objectId };
  if (scopeId !== undefined) args.scopeId = scopeId;
  return call<NoteSummary[]>("notes_links_for_object", args);
}

/**
 * 每个对象关联的笔记数。返回的映射省略计数为 0 的对象，调用方必须读 `counts[id] ?? 0`。
 */
export function countNoteLinks(objectType: NoteLinkType, objectIds: string[]) {
  return call<Record<string, number>>("notes_links_counts", { objectType, objectIds });
}

export function listAllNoteLinks() {
  return call<NoteLinkEntry[]>("notes_links_all");
}

/** 空白搜索词返回 []；limit 省略时用后端默认值。 */
export function searchNotes(query: string, limit?: number) {
  const args: Record<string, unknown> = { query };
  if (limit !== undefined) args.limit = limit;
  return call<NoteSearchHit[]>("notes_search", args);
}

function bytesToBase64(bytes: Uint8Array) {
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

/** 保存一张图片附件，返回正文引用 `/__notes-assets/<sha256>.<ext>`。超过 20 MB 时不调用后端直接拒绝。 */
export async function putNoteAsset(file: File): Promise<string> {
  if (!isDesktopRuntime()) throw new Error("notes_desktop_only");
  if (file.size > NOTE_ASSET_MAX_BYTES) throw new Error("note_asset_too_large");
  const dataBase64 = bytesToBase64(new Uint8Array(await file.arrayBuffer()));
  const result = await call<{ ref: string }>("notes_asset_put", { fileName: file.name, dataBase64 });
  return result.ref;
}

/** paths 为 .md / .markdown 绝对路径；folderId 为空串或省略表示导入到根目录。 */
export function importNoteFiles(paths: string[], folderId = "") {
  return call<NoteImportReport>("notes_import_files", { paths, folderId });
}

/** 把目录（绝对路径）按层级导入为文件夹与笔记；folderId 为空串或省略表示导入到根目录。 */
export function importNoteFolder(path: string, folderId = "") {
  return call<NoteImportReport>("notes_import_folder", { path, folderId });
}

export function exportNotes(input: ExportNotesInput) {
  return call<NoteExportResult>("notes_export", { ...input });
}

type Translate = (key: MessageKey, params?: Record<string, string | number>) => string;

const NOTE_ERROR_KEYS: Readonly<Record<string, MessageKey>> = {
  note_asset_too_large: "notes.error.note_asset_too_large",
  note_asset_type: "notes.error.note_asset_type",
  note_export_dest: "notes.error.note_export_dest",
  note_export_empty: "notes.error.note_export_empty",
  note_folder_cycle: "notes.error.note_folder_cycle",
  note_folder_not_found: "notes.error.note_folder_not_found",
  note_import_path: "notes.error.note_import_path",
  note_invalid_request: "notes.error.note_invalid_request",
  note_link_type: "notes.error.note_link_type",
  note_not_found: "notes.error.note_not_found",
  note_request_too_large: "notes.error.note_request_too_large",
  notes_desktop_only: "notes.error.notes_desktop_only",
};

/** 把笔记命令的稳定错误码映射为本地化文案；其他错误（内部错误可能带原文）用通用文案附原文。 */
export function noteErrorMessage(error: unknown, t: Translate): string {
  const message = nativeErrorMessage(error);
  const key = Object.prototype.hasOwnProperty.call(NOTE_ERROR_KEYS, message) ? NOTE_ERROR_KEYS[message] : undefined;
  return key ? t(key) : t("notes.error.unknown", { message });
}

const NOTE_IMPORT_SKIP_KEYS: Readonly<Record<NoteImportSkipReason, MessageKey>> = {
  outside_root: "notes.import.skip.outside_root",
  symlink: "notes.import.skip.symlink",
  asset_type: "notes.import.skip.asset_type",
  asset_too_large: "notes.import.skip.asset_too_large",
  missing: "notes.import.skip.missing",
  too_large: "notes.import.skip.too_large",
  read_error: "notes.import.skip.read_error",
};

/** 导入跳过原因对应的文案键；后端新增、前端未知的原因回退到通用文案。 */
export function noteImportSkipReasonKey(reason: string): MessageKey {
  return Object.prototype.hasOwnProperty.call(NOTE_IMPORT_SKIP_KEYS, reason)
    ? NOTE_IMPORT_SKIP_KEYS[reason as NoteImportSkipReason]
    : "notes.import.skip.unknown";
}
