/**
 * 浏览器本地模式的本地项目：用 File System Access API（Chrome / Edge）实现九个 `local_fs_*` 命令，
 * 代替 backend/fs（localfs.go、localfs_search.go）。参数、返回形状、大小上限、搜索跳过的
 * 目录与结果上限都与 Go 一致；错误码沿用 Go 的文案（如 `path_is_directory`、`already_exists`）。
 *
 * 项目根是 `browser://<id>/<文件夹名>`：`showDirectoryPicker` 得到的目录句柄以 id 为键存进
 * IndexedDB，刷新后仍可取回，但浏览器会把权限重置为 `prompt`，必须由用户点击触发
 * `requestBrowserProjectPermission` 才能恢复。处理器在没有权限时抛出 `project.browser.permission`，
 * 句柄不存在时抛出 `project.browser.missing`，从不静默返回空结果。
 *
 * 路径一律是相对项目根、以 `/` 分隔的路径；空段、`.`、`..`、绝对路径、盘符与反斜杠在访问任何
 * 句柄之前就被拒绝。浏览器句柄本身无法越出所选文件夹，这里的校验让错误与 Go 端一样明确。
 *
 * 视图只能经 `desktop-bridge.ts` 的 `chooseNativeDirectory` 与 `stores/local-project.ts` 动态 `import()`
 * 本模块（`scripts/browser-runtime.test.mjs` 会检查），以免把它打进主包。
 */
import {
  BROWSER_PROJECT_SCHEME,
  supportsBrowserDirectoryPicker,
  type BrowserProjectAccess,
} from "@/domain/projects/browser-root";
import { openIdbDatabase, type IdbDatabase, type IdbSchema } from "./idb";
import { registerBrowserHandler, registerBrowserLocalFeature } from "./registry";

// ---------- File System Access API（TS 的 DOM 库尚未收录的部分） ----------

type HandlePermission = "granted" | "denied" | "prompt";
interface ReadWriteDescriptor {
  mode: "readwrite";
}

interface BrowserHandleBase {
  readonly name: string;
  isSameEntry(other: BrowserHandle): Promise<boolean>;
  queryPermission?(descriptor: ReadWriteDescriptor): Promise<HandlePermission>;
  requestPermission?(descriptor: ReadWriteDescriptor): Promise<HandlePermission>;
  /** Chrome：在同一父目录内改名；并非所有版本与目录都支持。 */
  move?(newName: string): Promise<void>;
}

interface BrowserWritable {
  write(data: BufferSource | Blob | string): Promise<void>;
  close(): Promise<void>;
  abort?(): Promise<void>;
}

interface BrowserFileHandle extends BrowserHandleBase {
  readonly kind: "file";
  getFile(): Promise<File>;
  createWritable(): Promise<BrowserWritable>;
}

interface BrowserDirectoryHandle extends BrowserHandleBase {
  readonly kind: "directory";
  getDirectoryHandle(name: string, options?: { create?: boolean }): Promise<BrowserDirectoryHandle>;
  getFileHandle(name: string, options?: { create?: boolean }): Promise<BrowserFileHandle>;
  removeEntry(name: string, options?: { recursive?: boolean }): Promise<void>;
  values(): AsyncIterable<BrowserHandle>;
}

type BrowserHandle = BrowserFileHandle | BrowserDirectoryHandle;

interface DirectoryPickerWindow {
  showDirectoryPicker(options: ReadWriteDescriptor): Promise<BrowserDirectoryHandle>;
}

// ---------- Go 端常量（localfs.go、localfs_search.go） ----------

const MAX_READ_BYTES = 5 * 1024 * 1024;
const MAX_WRITE_BYTES = 5 * 1024 * 1024;
const MAX_IMAGE_BYTES = 20 * 1024 * 1024;
const SEARCH_MAX_RESULTS = 300;
const SEARCH_MAX_FILE_BYTES = 1024 * 1024;
const SEARCH_PEEK_BYTES = 8192;
const SEARCH_LINE_TEXT_RUNES = 240;
/** searchSkipDirNames：不进入这些目录，但目录名本身仍可命中。 */
const SEARCH_SKIP_DIRS = new Set([".git", "node_modules", "target", "dist", "build", ".next", ".nuxt", ".output"]);

const IMAGE_MIME_TYPES: Readonly<Record<string, string>> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  jfif: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  bmp: "image/bmp",
  ico: "image/x-icon",
  avif: "image/avif",
  tif: "image/tiff",
  tiff: "image/tiff",
  svg: "image/svg+xml",
};

// ---------- 句柄存储 ----------

interface ProjectHandleRecord {
  id: string;
  name: string;
  handle: BrowserDirectoryHandle;
}

export const BROWSER_PROJECT_HANDLE_SCHEMA = { handles: "id" } as const satisfies IdbSchema;
const HANDLE_STORE = "handles";
const DATABASE_NAME = "mincode_browser_projects_v1";
const DATABASE_VERSION = 1;

let database: IdbDatabase | undefined;
/** 本标签页已取出的句柄；权限每次都重新查询。 */
const records = new Map<string, ProjectHandleRecord>();

function projectDatabase() {
  database ??= openIdbDatabase(DATABASE_NAME, DATABASE_VERSION, BROWSER_PROJECT_HANDLE_SCHEMA);
  return database;
}

/** 仅供测试：换成内存实现，并清空本标签页缓存（相当于刷新页面）。 */
export function __setBrowserProjectDatabaseForTests(replacement: IdbDatabase) {
  database = replacement;
  records.clear();
}

function isDirectoryRecord(value: unknown): value is ProjectHandleRecord {
  const record = value as Partial<ProjectHandleRecord> | undefined;
  return typeof record?.id === "string" && typeof record.name === "string" && record.handle?.kind === "directory";
}

async function loadRecord(id: string) {
  const cached = records.get(id);
  if (cached) return cached;
  const stored = await projectDatabase().transaction([HANDLE_STORE], "readonly", (tx) => tx.get<unknown>(HANDLE_STORE, id));
  if (!isDirectoryRecord(stored)) return undefined;
  records.set(id, stored);
  return stored;
}

/** 存下句柄；同一文件夹再次选择时沿用原 id，最近项目里不会出现两份。 */
async function rememberHandle(handle: BrowserDirectoryHandle) {
  const db = projectDatabase();
  const stored = await db.transaction([HANDLE_STORE], "readonly", (tx) => tx.getAll<unknown>(HANDLE_STORE));
  let id: string | undefined;
  // isSameEntry 不是 IndexedDB 请求，不能放在事务回调里 await（事务会提前提交）。
  for (const record of stored.filter(isDirectoryRecord)) {
    if (await record.handle.isSameEntry(handle).catch(() => false)) {
      id = record.id;
      break;
    }
  }
  const record: ProjectHandleRecord = { id: id ?? crypto.randomUUID(), name: handle.name, handle };
  await db.transaction([HANDLE_STORE], "readwrite", (tx) => tx.put(HANDLE_STORE, record));
  records.set(record.id, record);
  return record;
}

// ---------- 根与路径校验 ----------

interface BrowserProjectRoot {
  root: string;
  id: string;
  name: string;
}

const ROOT_PATTERN = /^browser:\/\/([A-Za-z0-9-]+)\/([^/\\]+)$/;

function parseRoot(value: unknown): BrowserProjectRoot | null {
  if (typeof value !== "string") return null;
  const root = value.trim();
  const match = ROOT_PATTERN.exec(root);
  if (!match || match[2] === "." || match[2] === "..") return null;
  return { root, id: match[1], name: match[2] };
}

function fail(code: string): never {
  throw new Error(code);
}

function requireRoot(value: unknown): BrowserProjectRoot {
  const root = parseRoot(value);
  if (!root) fail("project_root_invalid");
  return root;
}

/**
 * 相对项目根的路径 → 路径段。`null`/`undefined`/空串表示项目根；同 Go 的 `filepath.Clean("/" + sub)`，
 * `.` 与空段（`a/`、`a//b`、`./a`、`a/./b`）被忽略，因此 `.` 也是项目根。
 * 与 Go 不同，越界写法（`..`、开头 `/`、反斜杠、盘符、绝对 URL）不会被「拉回」根内，而是直接拒绝。
 */
function relativeSegments(value: unknown): string[] {
  if (value === null || value === undefined) return [];
  if (typeof value !== "string") fail("path_invalid");
  const path = value.trim();
  if (!path) return [];
  if (path.startsWith("/") || path.includes("\\") || /^[A-Za-z]:(?:\/|$)/.test(path)) fail("path_escapes_root");
  // 绝对 URL（`browser://…`、`file:///…`）不是相对路径；不能因忽略空段而被当成根内的 `browser:/…`。
  if (/^[A-Za-z][A-Za-z0-9+.-]*:\/\//.test(path)) fail("path_escapes_root");
  const segments = path.split("/");
  if (segments.includes("..")) fail("path_escapes_root");
  return segments.filter((segment) => segment !== "" && segment !== ".");
}

function errorName(error: unknown) {
  const name = (error as { name?: unknown } | null)?.name;
  return typeof name === "string" ? name : "";
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

async function permissionOf(handle: BrowserDirectoryHandle): Promise<"granted" | "prompt"> {
  if (typeof handle.queryPermission !== "function") return "granted";
  return (await handle.queryPermission({ mode: "readwrite" })) === "granted" ? "granted" : "prompt";
}

/** 取回项目根句柄；只在参数全部校验通过后调用。 */
async function openRoot(root: BrowserProjectRoot) {
  const record = await loadRecord(root.id);
  if (!record || record.name !== root.name) fail("project.browser.missing");
  if ((await permissionOf(record.handle)) !== "granted") fail("project.browser.permission");
  return record.handle;
}

// ---------- 对外：打开文件夹与授权 ----------

/**
 * 让用户选择一个文件夹（必须在用户点击中调用），返回 `browser://<id>/<文件夹名>`；取消时返回 null。
 * 浏览器不支持时抛出 `project.browser.unsupported`。
 */
export async function openBrowserDirectory(): Promise<string | null> {
  if (!supportsBrowserDirectoryPicker()) fail("project.browser.unsupported");
  let handle: BrowserDirectoryHandle;
  try {
    handle = await (window as unknown as DirectoryPickerWindow).showDirectoryPicker({ mode: "readwrite" });
  } catch (error) {
    if (errorName(error) === "AbortError") return null;
    throw error;
  }
  const record = await rememberHandle(handle);
  return `${BROWSER_PROJECT_SCHEME}${record.id}/${record.name}`;
}

/** 项目根当前的访问状态；从不抛错（读不到句柄一律视为 `missing`）。 */
export async function browserProjectPermission(root: string): Promise<BrowserProjectAccess> {
  const parsed = parseRoot(root);
  if (!parsed) return "missing";
  try {
    const record = await loadRecord(parsed.id);
    if (!record || record.name !== parsed.name) return "missing";
    return await permissionOf(record.handle);
  } catch {
    return "missing";
  }
}

/** 请求读写权限；浏览器要求由用户点击触发。授权成功返回 true，从不抛错。 */
export async function requestBrowserProjectPermission(root: string): Promise<boolean> {
  const parsed = parseRoot(root);
  if (!parsed) return false;
  try {
    const record = await loadRecord(parsed.id);
    if (!record || record.name !== parsed.name) return false;
    if (typeof record.handle.requestPermission !== "function") return (await permissionOf(record.handle)) === "granted";
    return (await record.handle.requestPermission({ mode: "readwrite" })) === "granted";
  } catch {
    return false;
  }
}

// ---------- 目录遍历 ----------

async function directoryAt(root: BrowserDirectoryHandle, segments: readonly string[], create = false) {
  let current = root;
  for (const segment of segments) current = await current.getDirectoryHandle(segment, { create });
  return current;
}

/** 找不到 → `path_resolve_failed`（Go 的 EvalSymlinks 失败），中途是文件 → `path_is_not_directory`。 */
function resolveFailure(error: unknown): never {
  const name = errorName(error);
  if (name === "NotFoundError") fail(`path_resolve_failed: ${errorText(error)}`);
  if (name === "TypeMismatchError") fail("path_is_not_directory");
  throw error;
}

async function childEntry(parent: BrowserDirectoryHandle, name: string): Promise<BrowserHandle | undefined> {
  try {
    return await parent.getFileHandle(name);
  } catch (error) {
    if (errorName(error) === "NotFoundError") return undefined;
    if (errorName(error) !== "TypeMismatchError") throw error;
  }
  return parent.getDirectoryHandle(name);
}

function splitLast(segments: readonly string[]) {
  return { parentSegments: segments.slice(0, -1), name: segments[segments.length - 1] };
}

/** 已存在的条目（文件或目录）；空路径即项目根。 */
async function entryAt(root: BrowserDirectoryHandle, segments: readonly string[]): Promise<BrowserHandle> {
  if (!segments.length) return root;
  const { parentSegments, name } = splitLast(segments);
  const parent = await directoryAt(root, parentSegments).catch(resolveFailure);
  const entry = await childEntry(parent, name).catch(resolveFailure);
  if (!entry) fail(`path_resolve_failed: ${name} not found`);
  return entry;
}

async function sortedChildren(directory: BrowserDirectoryHandle) {
  const entries: BrowserHandle[] = [];
  for await (const entry of directory.values()) entries.push(entry);
  // os.ReadDir 按文件名排序。
  return entries.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

async function writeFile(handle: BrowserFileHandle, data: BufferSource | Blob) {
  const writable = await handle.createWritable();
  try {
    await writable.write(data);
    await writable.close();
  } catch (error) {
    await writable.abort?.().catch(() => undefined);
    fail(`write: ${errorText(error)}`);
  }
}

async function copyEntry(source: BrowserHandle, destination: BrowserDirectoryHandle, name: string): Promise<void> {
  if (source.kind === "file") {
    await writeFile(await destination.getFileHandle(name, { create: true }), await source.getFile());
    return;
  }
  const target = await destination.getDirectoryHandle(name, { create: true });
  for await (const child of source.values()) await copyEntry(child, target, child.name);
}

/** 优先用 `move()` 原地改名；不支持时复制后删除原条目，复制失败则清理半成品。 */
async function renameEntry(parent: BrowserDirectoryHandle, entry: BrowserHandle, oldName: string, newName: string) {
  if (typeof entry.move === "function") {
    try {
      await entry.move(newName);
      return;
    } catch (error) {
      if (await childEntry(parent, newName).catch(() => undefined)) fail(`rename: ${errorText(error)}`);
    }
  }
  try {
    await copyEntry(entry, parent, newName);
  } catch (error) {
    await parent.removeEntry(newName, { recursive: true }).catch(() => undefined);
    fail(`rename: ${errorText(error)}`);
  }
  await parent.removeEntry(oldName, { recursive: true });
}

// ---------- 内容辅助 ----------

const ascii = (text: string) => Array.from(text, (char) => char.charCodeAt(0));

/** fingerprintBinaryMagic 的魔数表：[前缀, 最短长度]。 */
const BINARY_MAGIC: ReadonlyArray<readonly [readonly number[], number]> = [
  [[0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a], 0],
  [[0xff, 0xd8, 0xff], 0],
  [ascii("GIF87a"), 0],
  [ascii("GIF89a"), 0],
  [ascii("RIFF"), 12],
  [ascii("BM"), 14],
  [[0, 0, 1, 0], 0],
  [[0, 0, 2, 0], 0],
  [[0x50, 0x4b, 0x03, 0x04], 0],
  [[0x50, 0x4b, 0x05, 0x06], 0],
  [[0x50, 0x4b, 0x07, 0x08], 0],
  [[0x1f, 0x8b], 0],
  [[0x28, 0xb5, 0x2f, 0xfd], 0],
  [[...ascii("Rar!"), 0x1a, 0x07, 0x00], 0],
  [[...ascii("Rar!"), 0x1a, 0x07, 0x01], 0],
  [[0x37, 0x7a, 0xbc, 0xaf, 0x27, 0x1c], 0],
  [ascii("%PDF-"), 0],
  [[0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1], 0],
  [ascii("MZ"), 0],
  [[0x7f, ...ascii("ELF")], 0],
  [[0xfe, 0xed, 0xfa, 0xce], 0],
  [[0xce, 0xfa, 0xed, 0xfe], 0],
  [[0xfe, 0xed, 0xfa, 0xcf], 0],
  [[0xcf, 0xfa, 0xed, 0xfe], 0],
  [[0xca, 0xfe, 0xba, 0xbe], 0],
  [[0x00, ...ascii("asm")], 0],
  [[...ascii("SQLite format 3"), 0], 0],
  [ascii("OggS"), 0],
  [ascii("fLaC"), 0],
  [ascii("ID3"), 0],
  [[0x1a, 0x45, 0xdf, 0xa3], 0],
];

/** sampleLooksLikeBinaryForPreview：含 NUL 或已知魔数视为二进制；UTF-8 / UTF-16 BOM 放行。 */
function looksBinary(sample: Uint8Array) {
  let bytes = sample;
  if (bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) bytes = bytes.subarray(3);
  if (!bytes.length) return false;
  if (bytes.length >= 2 && ((bytes[0] === 0xff && bytes[1] === 0xfe) || (bytes[0] === 0xfe && bytes[1] === 0xff))) {
    return false;
  }
  if (bytes.includes(0)) return true;
  if (bytes.length >= 2 && bytes[0] === 0xff && (bytes[1] & 0xe0) === 0xe0) return true;
  return BINARY_MAGIC.some(([prefix, minLength]) =>
    bytes.length >= Math.max(prefix.length, minLength) && prefix.every((byte, index) => bytes[index] === byte));
}

function truncateRunes(text: string, max: number) {
  if (text.length <= max) return text;
  const runes = Array.from(text);
  return runes.length <= max ? text : runes.slice(0, max).join("");
}

/** 首个包含 query（已小写）的行；超过 1 MB 或二进制文件不搜索内容。 */
async function firstContentMatch(handle: BrowserFileHandle, query: string) {
  const file = await handle.getFile();
  if (file.size > SEARCH_MAX_FILE_BYTES) return undefined;
  const bytes = new Uint8Array(await file.arrayBuffer());
  if (looksBinary(bytes.subarray(0, SEARCH_PEEK_BYTES))) return undefined;
  const lines = new TextDecoder().decode(bytes).split("\n");
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index].endsWith("\r") ? lines[index].slice(0, -1) : lines[index];
    if (line.toLowerCase().includes(query)) return { line: index + 1, text: line };
  }
  return undefined;
}

function base64(bytes: Uint8Array) {
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

function imageMime(name: string) {
  const dot = name.lastIndexOf(".");
  return dot < 0 ? undefined : IMAGE_MIME_TYPES[name.slice(dot + 1).toLowerCase()];
}

interface SearchResult {
  name: string;
  path: string;
  relativePath: string;
  isDir: boolean;
  kind: "name" | "content";
  line: number | null;
  lineText: string | null;
}

// ---------- 命令处理器 ----------

registerBrowserHandler("local_fs_list_dir", async (args) => {
  const root = requireRoot(args.projectRoot);
  const segments = relativeSegments(args.dirPath);
  const directory = await directoryAt(await openRoot(root), segments).catch((error: unknown) => {
    // 目录不存在时返回空列表（如 .mincode/rules 尚未创建），同 Go。
    if (errorName(error) === "NotFoundError") return undefined;
    if (errorName(error) === "TypeMismatchError") fail("read_dir: path_is_not_directory");
    throw error;
  });
  if (!directory) return [];
  const prefix = segments.length ? `${segments.join("/")}/` : "";
  return (await sortedChildren(directory)).map((entry) => ({
    name: entry.name,
    path: `${prefix}${entry.name}`,
    isDir: entry.kind === "directory",
  }));
});

registerBrowserHandler("local_fs_file_info", async (args) => {
  const root = requireRoot(args.projectRoot);
  const segments = relativeSegments(args.filePath);
  const entry = await entryAt(await openRoot(root), segments);
  if (entry.kind === "directory") return { isFile: false, size: 0 };
  return { isFile: true, size: (await entry.getFile()).size };
});

registerBrowserHandler("local_fs_read_text_file", async (args) => {
  const root = requireRoot(args.projectRoot);
  const segments = relativeSegments(args.filePath);
  const entry = await entryAt(await openRoot(root), segments);
  if (entry.kind === "directory") fail("path_is_directory");
  const file = await entry.getFile();
  if (file.size > MAX_READ_BYTES) fail(`file_too_large: ${file.size} bytes (max ${MAX_READ_BYTES})`);
  // 同 Go 的 string(data)：保留 BOM，非法 UTF-8 替换为 U+FFFD。
  return new TextDecoder("utf-8", { ignoreBOM: true }).decode(await file.arrayBuffer());
});

registerBrowserHandler("local_fs_read_image_data_url", async (args) => {
  const root = requireRoot(args.projectRoot);
  const segments = relativeSegments(args.filePath);
  const entry = await entryAt(await openRoot(root), segments);
  if (entry.kind !== "file") fail("not_a_file");
  const mime = imageMime(entry.name);
  if (!mime) fail("unsupported_image_type");
  const file = await entry.getFile();
  if (file.size > MAX_IMAGE_BYTES) fail(`image_too_large: ${file.size} bytes (max ${MAX_IMAGE_BYTES})`);
  return `data:${mime};base64,${base64(new Uint8Array(await file.arrayBuffer()))}`;
});

registerBrowserHandler("local_fs_write_text_file", async (args) => {
  const root = requireRoot(args.projectRoot);
  const segments = relativeSegments(args.filePath);
  const content = typeof args.content === "string" ? args.content : "";
  const createOnly = args.createOnly === true;
  const projectRoot = await openRoot(root);
  if (!segments.length) fail(createOnly ? "already_exists" : "path_is_directory");
  const { parentSegments, name } = splitLast(segments);
  const existingParent = await directoryAt(projectRoot, parentSegments).catch((error: unknown) => {
    if (errorName(error) === "NotFoundError") return undefined;
    if (errorName(error) === "TypeMismatchError") fail("create_dir_all: path_is_not_directory");
    throw error;
  });
  if (existingParent) {
    const existing = await childEntry(existingParent, name);
    if (existing && createOnly) fail("already_exists");
    if (existing?.kind === "directory") fail("path_is_directory");
  }
  const bytes = new TextEncoder().encode(content);
  if (bytes.length > MAX_WRITE_BYTES) fail(`content_too_large: ${bytes.length} bytes (max ${MAX_WRITE_BYTES})`);
  const parent = existingParent ?? await directoryAt(projectRoot, parentSegments, true).catch((error: unknown) => {
    fail(`create_dir_all: ${errorText(error)}`);
  });
  await writeFile(await parent.getFileHandle(name, { create: true }), bytes);
  return undefined;
});

registerBrowserHandler("local_fs_mkdir", async (args) => {
  const root = requireRoot(args.projectRoot);
  const segments = relativeSegments(args.dirPath);
  await directoryAt(await openRoot(root), segments, true).catch((error: unknown) => {
    if (errorName(error) === "TypeMismatchError") fail("path_is_not_directory");
    fail(`create_dir_all: ${errorText(error)}`);
  });
  return undefined;
});

registerBrowserHandler("local_fs_rename", async (args) => {
  const newName = typeof args.newName === "string" ? args.newName.trim() : "";
  if (!newName || /[\\/]/.test(newName) || newName === "." || newName === "..") fail("invalid_name");
  const root = requireRoot(args.projectRoot);
  const segments = relativeSegments(args.oldPath);
  const projectRoot = await openRoot(root);
  if (!segments.length) fail("cannot_rename_project_root");
  const { parentSegments, name } = splitLast(segments);
  const parent = await directoryAt(projectRoot, parentSegments).catch(resolveFailure);
  const entry = await childEntry(parent, name).catch(resolveFailure);
  if (!entry) fail(`path_resolve_failed: ${name} not found`);
  if (await childEntry(parent, newName)) fail("already_exists");
  await renameEntry(parent, entry, name, newName);
  return undefined;
});

registerBrowserHandler("local_fs_delete", async (args) => {
  const root = requireRoot(args.projectRoot);
  const segments = relativeSegments(args.path);
  const projectRoot = await openRoot(root);
  if (!segments.length) fail("cannot_delete_project_root");
  const { parentSegments, name } = splitLast(segments);
  const parent = await directoryAt(projectRoot, parentSegments).catch(resolveFailure);
  if (!(await childEntry(parent, name).catch(resolveFailure))) fail(`path_resolve_failed: ${name} not found`);
  await parent.removeEntry(name, { recursive: true }).catch((error: unknown) => fail(`delete: ${errorText(error)}`));
  return undefined;
});

registerBrowserHandler("local_fs_search", async (args, signal) => {
  const query = (typeof args.query === "string" ? args.query : "").trim().toLowerCase();
  if (!query) return [];
  const root = requireRoot(args.projectRoot);
  const scope = relativeSegments(args.scopePath);
  const includeContent = args.includeContent === true;
  const scopeDirectory = await directoryAt(await openRoot(root), scope).catch((error: unknown) => {
    if (errorName(error) === "TypeMismatchError") fail("scope_not_a_directory");
    return resolveFailure(error);
  });

  const results: SearchResult[] = [];
  const result = (relativePath: string, name: string, isDir: boolean): SearchResult => ({
    name, path: `${root.root}/${relativePath}`, relativePath, isDir, kind: "name", line: null, lineText: null,
  });
  // 与 Go 相同的深度优先顺序：后入栈的目录先遍历。
  const stack = [{ directory: scopeDirectory, path: scope.join("/") }];
  while (stack.length && results.length < SEARCH_MAX_RESULTS) {
    signal?.throwIfAborted();
    const { directory, path } = stack.pop()!;
    const entries = await sortedChildren(directory).catch(() => undefined);
    if (!entries) continue;
    for (const entry of entries) {
      if (results.length >= SEARCH_MAX_RESULTS) break;
      const relativePath = path ? `${path}/${entry.name}` : entry.name;
      const nameMatches = entry.name.toLowerCase().includes(query);
      if (entry.kind === "directory") {
        if (nameMatches) results.push(result(relativePath, entry.name, true));
        if (!SEARCH_SKIP_DIRS.has(entry.name)) stack.push({ directory: entry, path: relativePath });
        continue;
      }
      if (nameMatches) {
        results.push(result(relativePath, entry.name, false));
        if (results.length >= SEARCH_MAX_RESULTS) break;
      }
      if (!includeContent) continue;
      signal?.throwIfAborted();
      const match = await firstContentMatch(entry, query).catch(() => undefined);
      if (!match) continue;
      results.push({
        ...result(relativePath, entry.name, false),
        kind: "content",
        line: match.line,
        lineText: truncateRunes(match.text.trim(), SEARCH_LINE_TEXT_RUNES),
      });
    }
  }
  return results;
});

registerBrowserLocalFeature({
  id: "project",
  titleKey: "browserLocal.feature.project.title",
  limitKey: "browserLocal.feature.project.limit",
});
