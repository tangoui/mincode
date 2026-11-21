export interface ProjectFileEntry {
  name: string;
  path: string;
  isDir: boolean;
}

export interface ProjectTreeNode extends ProjectFileEntry {
  id: string;
  leaf: boolean;
}

export interface ProjectSearchResult {
  name: string;
  path: string;
  relativePath: string;
  isDir: boolean;
  kind: "name" | "content";
  line?: number;
  lineText?: string;
}

export interface ProjectEditorRevealPosition {
  line: number;
  column?: number;
}

/** `debug-source`: read-only text a debug adapter returned for a source outside the project (not a file). */
export type ProjectExternalDocumentKind = "text" | "image" | "pdf" | "binary" | "debug-source";

export interface ProjectEditorDocument {
  /** Runtime identity, preserved across workspace caches; never persisted. */
  sessionId?: number;
  path: string;
  name: string;
  content: string;
  savedContent: string;
  loaded: boolean;
  loading: boolean;
  errorKey?: ProjectDocumentErrorKey;
  errorDetail?: string;
  revealPosition?: ProjectEditorRevealPosition;
  external?: boolean;
  externalKind?: ProjectExternalDocumentKind;
  mimeType?: string;
  /** Bounded UTF-8 preview; remains a non-editable external document. */
  textPreviewAvailable?: boolean;
  size?: number;
  /** contributes.customEditors: open in this plugin editor instead of the built-in text editor. */
  editor?: ProjectDocumentEditor;
}

/** A plugin custom editor binding of a project document tab (key `<pluginId>.<editorId>`). */
export interface ProjectDocumentEditor {
  pluginId: string;
  editorId: string;
}

export type ProjectDocumentErrorKey = "project.readFailed";

export type ProjectEditorLineNumbers = "on" | "off" | "relative";

export interface MonacoPresentationPreferences {
  fontSize: number;
  tabSize: number;
  wordWrap: "off" | "on";
  minimap: boolean;
  lineNumbers: ProjectEditorLineNumbers;
  formatOnPaste?: boolean;
  formatOnType?: boolean;
  renderWhitespace?: "none" | "selection" | "all";
  indentationGuides?: boolean;
  bracketPairColorization?: boolean;
  stickyScroll?: boolean;
}

export interface ProjectEditorPreferences extends Required<MonacoPresentationPreferences> {
  autoSave: "off" | "afterDelay" | "onFocusChange" | "onWindowChange";
  autoSaveDelay: number;
  formatOnSave: boolean;
  trimTrailingWhitespace: boolean;
  insertFinalNewline: boolean;
}

export interface ProjectEditorViewState {
  lineNumber: number;
  column: number;
  selectionStartLineNumber: number;
  selectionStartColumn: number;
  positionLineNumber: number;
  positionColumn: number;
  scrollTop: number;
  scrollLeft: number;
}

export interface ProjectTerminalTab {
  id: string;
  name: string;
  cwd: string;
}

export interface ProjectBlameHunk {
  line_start: number;
  line_end: number;
  commit_hash: string;
  commit_hash_short: string;
  author: string;
  author_email: string;
  date: string;
  message: string;
}

export interface GitFileEntry {
  path: string;
  status: string;
  old_path?: string;
}

export interface GitProjectStatus {
  branch: string;
  upstream?: string;
  ahead: number;
  behind: number;
  staged: GitFileEntry[];
  unstaged: GitFileEntry[];
  untracked: GitFileEntry[];
}

const MARKDOWN_DOCUMENT_EXTENSIONS = new Set([
  "md",
  "markdown",
  "mdown",
  "mkdn",
  "mkd",
  "mdwn",
  "mdtxt",
]);

export function isMarkdownDocument(path: string) {
  const extension = path.split(".").pop()?.toLowerCase();
  return Boolean(extension && MARKDOWN_DOCUMENT_EXTENSIONS.has(extension));
}

const IMAGE_DOCUMENT_EXTENSIONS = new Set([
  "png",
  "jpg",
  "jpeg",
  "jfif",
  "gif",
  "webp",
  "bmp",
  "ico",
  "avif",
  "tif",
  "tiff",
  "svg",
]);

export function isImageDocument(path: string) {
  const extension = path.split(".").pop()?.toLowerCase();
  return Boolean(extension && IMAGE_DOCUMENT_EXTENSIONS.has(extension));
}

export const DEFAULT_EDITOR_PREFERENCES: ProjectEditorPreferences = {
  fontSize: 13,
  tabSize: 2,
  wordWrap: "on",
  minimap: false,
  lineNumbers: "on",
  autoSave: "off",
  autoSaveDelay: 3000,
  formatOnSave: false,
  formatOnPaste: true,
  formatOnType: false,
  trimTrailingWhitespace: false,
  insertFinalNewline: false,
  renderWhitespace: "selection",
  indentationGuides: true,
  bracketPairColorization: true,
  stickyScroll: true,
};

export function projectBasename(path: string) {
  const normalized = path.replace(/\\/g, "/").replace(/\/$/, "");
  return normalized.split("/").pop() || normalized;
}

export function projectDirname(path: string) {
  const slashNormalized = path.replace(/\\/g, "/");
  const normalized = slashNormalized.replace(/\/+$/g, "");
  if (!normalized) return slashNormalized.startsWith("/") ? "/" : "";

  const separator = normalized.lastIndexOf("/");
  if (separator < 0) return "";
  // Preserve the Unix filesystem root. Git and native filesystem calls require
  // absolute paths to stay absolute; dropping this slash turns `/Volumes/...`
  // into a process-relative path such as `Volumes/...`.
  if (separator === 0) return "/";
  return normalized.slice(0, separator);
}

export function joinProjectPath(parent: string, name: string) {
  return [parent.replace(/\\/g, "/").replace(/\/+$/g, ""), name.trim()].filter(Boolean).join("/");
}

export function toProjectTreeNode(entry: ProjectFileEntry): ProjectTreeNode {
  return { ...entry, id: entry.path, leaf: !entry.isDir };
}

export function sortProjectEntries(entries: ProjectFileEntry[]) {
  return [...entries].sort(
    (a, b) => Number(b.isDir) - Number(a.isDir) || a.name.localeCompare(b.name)
  );
}

/** The auto-save delay default before it became 3 s. */
const LEGACY_AUTO_SAVE_DELAY = 1000;

/**
 * Stored preferences always carry autoSaveDelay, so the old 1 s default was saved even by users who never turned
 * on "after delay"; those get the current default. A delay chosen while using "after delay" is kept.
 */
export function upgradeStoredEditorPreferences(
  value: Partial<ProjectEditorPreferences> | undefined
): Partial<ProjectEditorPreferences> | undefined {
  if (value?.autoSave === "afterDelay" || value?.autoSaveDelay !== LEGACY_AUTO_SAVE_DELAY) return value;
  const { autoSaveDelay: _legacy, ...rest } = value;
  return rest;
}

export function sanitizeEditorPreferences(
  value: Partial<ProjectEditorPreferences> | undefined
): ProjectEditorPreferences {
  return {
    fontSize:
      Number.isInteger(value?.fontSize) &&
      Number(value?.fontSize) >= 10 &&
      Number(value?.fontSize) <= 28
        ? Number(value?.fontSize)
        : DEFAULT_EDITOR_PREFERENCES.fontSize,
    tabSize:
      value?.tabSize === 2 || value?.tabSize === 4
        ? value.tabSize
        : DEFAULT_EDITOR_PREFERENCES.tabSize,
    autoSave: value?.autoSave === "afterDelay" || value?.autoSave === "onFocusChange" || value?.autoSave === "onWindowChange"
      ? value.autoSave : "off",
    autoSaveDelay: Number.isInteger(value?.autoSaveDelay) && Number(value?.autoSaveDelay) >= 500 && Number(value?.autoSaveDelay) <= 10000
      ? Number(value?.autoSaveDelay) : DEFAULT_EDITOR_PREFERENCES.autoSaveDelay,
    formatOnSave: value?.formatOnSave === true,
    formatOnPaste: typeof value?.formatOnPaste === "boolean" ? value.formatOnPaste : true,
    formatOnType: value?.formatOnType === true,
    trimTrailingWhitespace: value?.trimTrailingWhitespace === true,
    insertFinalNewline: value?.insertFinalNewline === true,
    renderWhitespace: value?.renderWhitespace === "none" || value?.renderWhitespace === "all" ? value.renderWhitespace : "selection",
    indentationGuides: typeof value?.indentationGuides === "boolean" ? value.indentationGuides : true,
    bracketPairColorization: typeof value?.bracketPairColorization === "boolean" ? value.bracketPairColorization : true,
    stickyScroll: typeof value?.stickyScroll === "boolean" ? value.stickyScroll : true,
    wordWrap: value?.wordWrap === "off" ? "off" : "on",
    minimap: value?.minimap === true,
    lineNumbers:
      value?.lineNumbers === "off" || value?.lineNumbers === "relative" ? value.lineNumbers : "on",
  };
}
