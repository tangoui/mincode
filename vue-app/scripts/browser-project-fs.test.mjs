import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";

// Browser-local projects: File System Access API replacement of the local_fs_* commands in
// backend/fs (localfs.go, localfs_search.go). The handlers run against an in-memory fake of
// FileSystemDirectoryHandle / FileSystemFileHandle and an in-memory stand-in for the IndexedDB wrapper.
// Argument and result shapes must stay those of the Go commands used by views/ProjectWorkspaceView.vue.

const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/dev-performance") {
      return { url: dataModule("export const measureDevAsync = (_name, run) => run();"), shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});

globalThis.__nativeDialogCalls = 0;
globalThis.window = {};
// Desktop mode: a stub window.tango runtime whose native file dialog returns /native/dir.
const desktopRuntime = {
  async call(method, request) {
    if (method === "mincode.native" && request.op === "dialog.open") {
      globalThis.__nativeDialogCalls += 1;
      return ["/native/dir"];
    }
    throw new Error(`unexpected ${method} ${JSON.stringify(request)}`);
  },
  emit() {},
  onRaw: () => () => {},
};

const projectFs = await import("../src/services/browser-runtime/project-fs.ts");
const {
  __setBrowserProjectDatabaseForTests,
  browserProjectPermission,
  openBrowserDirectory,
  requestBrowserProjectPermission,
} = projectFs;
const { browserLocalFeatures, getBrowserHandler } = await import("../src/services/browser-runtime/registry.ts");
const { chooseNativeDirectory } = await import("../src/services/desktop-bridge.ts");

// ---------- in-memory File System Access API ----------

/** Every handle method call, so tests can prove a rejected path never touched the folder. */
const accessLog = [];

function domError(name, message = name) {
  const error = new Error(message);
  error.name = name;
  return error;
}

/** The real API rejects these names with a TypeError; the handlers must reject them earlier. */
function checkName(name) {
  if (!name || name === "." || name === ".." || name.includes("/") || name.includes("\\")) {
    throw new TypeError(`Name is not allowed: ${JSON.stringify(name)}`);
  }
}

class FakeFileHandle {
  kind = "file";
  constructor(name, bytes, owner) {
    this.name = name;
    this.bytes = bytes;
    this.owner = owner;
  }
  async getFile() {
    accessLog.push(`getFile:${this.name}`);
    return new File([this.bytes], this.name);
  }
  async createWritable() {
    accessLog.push(`createWritable:${this.name}`);
    const chunks = [];
    return {
      write: async (data) => {
        chunks.push(typeof data === "string" ? new TextEncoder().encode(data) : new Uint8Array(await new Blob([data]).arrayBuffer()));
      },
      close: async () => {
        this.bytes = new Uint8Array(await new Blob(chunks).arrayBuffer());
      },
      abort: async () => undefined,
    };
  }
  async isSameEntry(other) {
    return other === this;
  }
}

class FakeDirectoryHandle {
  kind = "directory";
  constructor(name, permission = { state: "granted" }) {
    this.name = name;
    this.children = new Map();
    this.permission = permission;
    this.permissionRequests = [];
  }
  async queryPermission(descriptor) {
    accessLog.push(`queryPermission:${this.name}`);
    assert.deepEqual(descriptor, { mode: "readwrite" });
    return this.permission.state;
  }
  async requestPermission(descriptor) {
    this.permissionRequests.push(descriptor);
    if (this.permission.grantOnRequest) this.permission.state = "granted";
    return this.permission.state;
  }
  async isSameEntry(other) {
    return other === this;
  }
  async getDirectoryHandle(name, options = {}) {
    accessLog.push(`getDirectoryHandle:${name}`);
    checkName(name);
    const existing = this.children.get(name);
    if (existing) {
      if (existing.kind !== "directory") throw domError("TypeMismatchError");
      return existing;
    }
    if (!options.create) throw domError("NotFoundError");
    const created = new FakeDirectoryHandle(name, this.permission);
    this.children.set(name, created);
    return created;
  }
  async getFileHandle(name, options = {}) {
    accessLog.push(`getFileHandle:${name}`);
    checkName(name);
    const existing = this.children.get(name);
    if (existing) {
      if (existing.kind !== "file") throw domError("TypeMismatchError");
      return existing;
    }
    if (!options.create) throw domError("NotFoundError");
    const created = new FakeFileHandle(name, new Uint8Array(), this);
    this.children.set(name, created);
    return created;
  }
  async removeEntry(name, options = {}) {
    accessLog.push(`removeEntry:${name}`);
    checkName(name);
    const existing = this.children.get(name);
    if (!existing) throw domError("NotFoundError");
    if (existing.kind === "directory" && existing.children.size && !options.recursive) {
      throw domError("InvalidModificationError");
    }
    this.children.delete(name);
  }
  async *values() {
    accessLog.push(`values:${this.name}`);
    // Real directories enumerate in no particular order.
    for (const handle of [...this.children.values()].reverse()) yield handle;
  }
}

/** Builds a folder from `{ "name": "text" | Uint8Array | { ...nested } }`. */
function folder(name, tree = {}, permission) {
  const dir = new FakeDirectoryHandle(name, permission);
  fill(dir, tree);
  return dir;
}

function fill(dir, tree) {
  for (const [name, value] of Object.entries(tree)) {
    if (typeof value === "string" || value instanceof Uint8Array) {
      const bytes = typeof value === "string" ? new TextEncoder().encode(value) : value;
      dir.children.set(name, new FakeFileHandle(name, bytes, dir));
    } else {
      const child = new FakeDirectoryHandle(name, dir.permission);
      fill(child, value);
      dir.children.set(name, child);
    }
  }
}

function fileText(dir, path) {
  let current = dir;
  const parts = path.split("/");
  for (const part of parts.slice(0, -1)) current = current.children.get(part);
  const file = current?.children.get(parts.at(-1));
  return file?.kind === "file" ? new TextDecoder().decode(file.bytes) : undefined;
}

/** In-memory stand-in for the IndexedDB wrapper (services/browser-runtime/idb.ts). */
function memoryDatabase() {
  const records = new Map();
  return {
    records,
    async transaction(stores, _mode, run) {
      assert.deepEqual(stores, ["handles"]);
      return run({
        get: async (store, key) => records.get(key),
        getAll: async () => [...records.values()],
        put: async (store, value) => {
          records.set(value.id, value);
        },
        delete: async (store, key) => {
          records.delete(key);
        },
      });
    },
  };
}

let database;
/** Picks `dir` through a fake showDirectoryPicker and returns its browser:// root. */
async function openFolder(dir) {
  globalThis.window.showDirectoryPicker = async (options) => {
    assert.deepEqual(options, { mode: "readwrite" });
    return dir;
  };
  return openBrowserDirectory();
}

function freshState() {
  database = memoryDatabase();
  __setBrowserProjectDatabaseForTests(database);
  globalThis.window = {};
  globalThis.tango = undefined;
  accessLog.length = 0;
}

const invoke = (command, args, signal) => getBrowserHandler(command)(args, signal);

// ---------- registration ----------

test("registers the nine local_fs commands and the project feature at import time", () => {
  for (const command of [
    "local_fs_list_dir",
    "local_fs_read_text_file",
    "local_fs_read_image_data_url",
    "local_fs_write_text_file",
    "local_fs_mkdir",
    "local_fs_rename",
    "local_fs_delete",
    "local_fs_file_info",
    "local_fs_search",
  ]) {
    assert.equal(typeof getBrowserHandler(command), "function", command);
  }
  const feature = browserLocalFeatures.find((entry) => entry.id === "project");
  assert.deepEqual(feature, {
    id: "project",
    titleKey: "browserLocal.feature.project.title",
    limitKey: "browserLocal.feature.project.limit",
  });
});

// ---------- opening folders ----------

test("openBrowserDirectory stores the handle under a new id and returns browser://<id>/<name>", async () => {
  freshState();
  const dir = folder("demo", { "a.txt": "A" });
  const root = await openFolder(dir);
  const match = /^browser:\/\/([A-Za-z0-9-]+)\/demo$/.exec(root);
  assert.ok(match, root);
  assert.equal(database.records.get(match[1]).handle, dir);
  assert.equal(database.records.get(match[1]).name, "demo");
  // Picking the same folder again reuses its id instead of adding a second recent project.
  assert.equal(await openFolder(dir), root);
  assert.equal(database.records.size, 1);
  // A different folder with the same name gets its own id.
  const other = await openFolder(folder("demo"));
  assert.notEqual(other, root);
  assert.equal(database.records.size, 2);
});

test("openBrowserDirectory returns null when the user cancels the picker", async () => {
  freshState();
  globalThis.window.showDirectoryPicker = async () => {
    throw domError("AbortError", "The user aborted a request.");
  };
  assert.equal(await openBrowserDirectory(), null);
  assert.equal(database.records.size, 0);
});

test("openBrowserDirectory without showDirectoryPicker reports that Chrome / Edge is needed", async () => {
  freshState();
  await assert.rejects(openBrowserDirectory(), /project\.browser\.unsupported/);
});

test("chooseNativeDirectory opens the browser picker only for local projects in browser mode", async () => {
  freshState();
  const dir = folder("picked");
  globalThis.window.showDirectoryPicker = async () => dir;
  const root = await chooseNativeDirectory("Choose", undefined, { browserProject: true });
  assert.match(root, /^browser:\/\/[A-Za-z0-9-]+\/picked$/);
  assert.equal(globalThis.__nativeDialogCalls, 0);
  // Other callers keep the native dialog path (which needs the desktop runtime);
  // on the desktop the project picker stays native too.
  await assert.rejects(chooseNativeDirectory("Choose"), /desktop runtime is unavailable/);
  globalThis.tango = desktopRuntime;
  assert.equal(await chooseNativeDirectory("Choose"), "/native/dir");
  assert.equal(await chooseNativeDirectory("Choose", undefined, { browserProject: true }), "/native/dir");
  assert.equal(globalThis.__nativeDialogCalls, 2);
});

// ---------- permission after reload ----------

test("permission: granted, prompt until the user re-authorizes, missing when the handle is gone", async () => {
  freshState();
  const permission = { state: "granted" };
  const dir = folder("proj", { "a.txt": "hello" }, permission);
  const root = await openFolder(dir);
  assert.equal(await browserProjectPermission(root), "granted");

  // After a reload Chrome reports "prompt" for a stored handle.
  permission.state = "prompt";
  __setBrowserProjectDatabaseForTests(database);
  assert.equal(await browserProjectPermission(root), "prompt");
  await assert.rejects(invoke("local_fs_read_text_file", { projectRoot: root, filePath: "a.txt" }), (error) => {
    assert.equal(error.message, "project.browser.permission");
    return true;
  });
  // Denied is reported as "prompt" too: the button is the only way back.
  permission.state = "denied";
  assert.equal(await browserProjectPermission(root), "prompt");

  permission.state = "prompt";
  permission.grantOnRequest = true;
  assert.equal(await requestBrowserProjectPermission(root), true);
  assert.deepEqual(dir.permissionRequests, [{ mode: "readwrite" }]);
  assert.equal(await browserProjectPermission(root), "granted");
  assert.equal(await invoke("local_fs_read_text_file", { projectRoot: root, filePath: "a.txt" }), "hello");

  // The user refuses.
  permission.state = "prompt";
  permission.grantOnRequest = false;
  assert.equal(await requestBrowserProjectPermission(root), false);
});

test("permission: a root whose handle is missing reports missing and never throws", async () => {
  freshState();
  const root = await openFolder(folder("proj"));
  const missingRoot = "browser://0000-unknown/proj";
  assert.equal(await browserProjectPermission(missingRoot), "missing");
  assert.equal(await requestBrowserProjectPermission(missingRoot), false);
  await assert.rejects(invoke("local_fs_list_dir", { projectRoot: missingRoot, dirPath: null }), /project\.browser\.missing/);

  // Same id but another folder name is not that project.
  const renamed = root.replace(/\/proj$/, "/other");
  assert.equal(await browserProjectPermission(renamed), "missing");

  // Site data cleared: the IndexedDB record is gone after a reload.
  database.records.clear();
  __setBrowserProjectDatabaseForTests(database);
  assert.equal(await browserProjectPermission(root), "missing");
  assert.equal(await requestBrowserProjectPermission(root), false);

  // Not a browser root at all (e.g. a desktop path).
  assert.equal(await browserProjectPermission("/Users/me/proj"), "missing");
  // IndexedDB unavailable: still no uncaught exception.
  __setBrowserProjectDatabaseForTests({
    transaction: async () => {
      throw new Error("IndexedDB is not available in this browser");
    },
  });
  assert.equal(await browserProjectPermission(root), "missing");
  assert.equal(await requestBrowserProjectPermission(root), false);
});

// ---------- path validation ----------

const badPaths = [
  "../x",
  "..",
  "a/../b",
  "a/..",
  "/",
  "/etc",
  "/etc/passwd",
  "..\\x",
  "a\\..\\b",
  "a\\b",
  "C:/Windows",
  "c:\\Windows",
  "browser://other/proj/a.txt",
  "file:///etc/passwd",
  42,
];

test("path validation: escapes and absolute paths are rejected before any handle access", async () => {
  freshState();
  const dir = folder("proj", { "a.txt": "A", src: { "b.txt": "B" } });
  const root = await openFolder(dir);
  const calls = (path) => [
    ["local_fs_list_dir", { projectRoot: root, dirPath: path }],
    ["local_fs_read_text_file", { projectRoot: root, filePath: path }],
    ["local_fs_read_image_data_url", { projectRoot: root, filePath: path }],
    ["local_fs_write_text_file", { projectRoot: root, filePath: path, content: "x" }],
    ["local_fs_mkdir", { projectRoot: root, dirPath: path }],
    ["local_fs_rename", { projectRoot: root, oldPath: path, newName: "z" }],
    ["local_fs_delete", { projectRoot: root, path }],
    ["local_fs_file_info", { projectRoot: root, filePath: path }],
    ["local_fs_search", { projectRoot: root, scopePath: path, query: "a", includeContent: true }],
  ];
  for (const path of badPaths) {
    for (const [command, args] of calls(path)) {
      accessLog.length = 0;
      await assert.rejects(invoke(command, args), (error) => {
        assert.match(error.message, /^path_(escapes_root|invalid)$/, `${command} ${JSON.stringify(path)}: ${error.message}`);
        return true;
      });
      assert.deepEqual(accessLog, [], `${command} ${JSON.stringify(path)} touched the folder`);
    }
  }
  assert.equal(fileText(dir, "a.txt"), "A");
  assert.deepEqual([...dir.children.keys()].sort(), ["a.txt", "src"]);
});

test("path validation: \".\" and empty segments are normalised like Go's filepath.Clean(\"/\" + sub)", async () => {
  freshState();
  const dir = folder("proj", { "a.txt": "A", src: { "b.txt": "B" } });
  const root = await openFolder(dir);
  const rootListing = await invoke("local_fs_list_dir", { projectRoot: root, dirPath: "" });
  for (const path of [".", "./", " . ", ".//."]) {
    assert.deepEqual(await invoke("local_fs_file_info", { projectRoot: root, filePath: path }), { isFile: false, size: 0 }, path);
    assert.deepEqual(await invoke("local_fs_list_dir", { projectRoot: root, dirPath: path }), rootListing, path);
    // Root-only operations still refuse the root, as Go does for "." after Clean.
    await assert.rejects(invoke("local_fs_delete", { projectRoot: root, path }), /^Error: cannot_delete_project_root$/);
    await assert.rejects(invoke("local_fs_rename", { projectRoot: root, oldPath: path, newName: "z" }), /^Error: cannot_rename_project_root$/);
    await assert.rejects(invoke("local_fs_write_text_file", { projectRoot: root, filePath: path, content: "x" }), /^Error: path_is_directory$/);
    await assert.rejects(invoke("local_fs_write_text_file", { projectRoot: root, filePath: path, content: "x", createOnly: true }), /^Error: already_exists$/);
    await assert.rejects(invoke("local_fs_read_text_file", { projectRoot: root, filePath: path }), /^Error: path_is_directory$/);
    assert.equal(await invoke("local_fs_mkdir", { projectRoot: root, dirPath: path }), undefined);
  }
  for (const path of ["./src/b.txt", "src/./b.txt", "src//b.txt", "./src//./b.txt"]) {
    assert.equal(await invoke("local_fs_read_text_file", { projectRoot: root, filePath: path }), "B", path);
  }
  for (const path of ["src/", "./src", "src//", "src/."]) {
    assert.deepEqual(await invoke("local_fs_list_dir", { projectRoot: root, dirPath: path }), [
      { name: "b.txt", path: "src/b.txt", isDir: false },
    ], path);
  }
  await invoke("local_fs_write_text_file", { projectRoot: root, filePath: "./new//c.txt", content: "C" });
  assert.equal(fileText(dir, "new/c.txt"), "C");
  const search = await invoke("local_fs_search", { projectRoot: root, scopePath: ".", query: "b.txt" });
  assert.deepEqual(search.map((item) => item.relativePath), ["src/b.txt"]);
  assert.deepEqual([...dir.children.keys()].sort(), ["a.txt", "new", "src"]);
});

test("path validation: project roots must be exactly browser://<id>/<name>", async () => {
  freshState();
  const root = await openFolder(folder("proj", { "a.txt": "A" }));
  for (const projectRoot of [
    "",
    "/Users/me/proj",
    "C:\\proj",
    "browser://",
    "browser:///proj",
    `${root}/a.txt`,
    `${root}/..`,
    root.replace("browser://", "browser:/"),
    root.replace("browser://", "file://"),
    "browser://../proj",
    undefined,
  ]) {
    accessLog.length = 0;
    await assert.rejects(invoke("local_fs_read_text_file", { projectRoot, filePath: "a.txt" }), (error) => {
      assert.match(error.message, /^project_root_invalid$/, `${projectRoot}: ${error.message}`);
      return true;
    });
    assert.deepEqual(accessLog, [], `${projectRoot} touched the folder`);
  }
  // Rename's new name is a single entry name.
  for (const newName of ["", " ", ".", "..", "a/b", "a\\b", "../x"]) {
    await assert.rejects(invoke("local_fs_rename", { projectRoot: root, oldPath: "a.txt", newName }), /^Error: invalid_name$/);
  }
});

// ---------- list / read / write ----------

test("local_fs_list_dir lists entries sorted by name with relative paths and isDir", async () => {
  freshState();
  const root = await openFolder(folder("proj", {
    "b.txt": "",
    "a.txt": "",
    src: { "z.ts": "", lib: {} },
    ".git": {},
  }));
  assert.deepEqual(await invoke("local_fs_list_dir", { projectRoot: root, dirPath: null }), [
    { name: ".git", path: ".git", isDir: true },
    { name: "a.txt", path: "a.txt", isDir: false },
    { name: "b.txt", path: "b.txt", isDir: false },
    { name: "src", path: "src", isDir: true },
  ]);
  assert.deepEqual(await invoke("local_fs_list_dir", { projectRoot: root, dirPath: "src" }), [
    { name: "lib", path: "src/lib", isDir: true },
    { name: "z.ts", path: "src/z.ts", isDir: false },
  ]);
  assert.deepEqual(await invoke("local_fs_list_dir", { projectRoot: root }), await invoke("local_fs_list_dir", { projectRoot: root, dirPath: "" }));
  // A directory that does not exist yet (e.g. .mincode/rules) lists as empty.
  assert.deepEqual(await invoke("local_fs_list_dir", { projectRoot: root, dirPath: ".mincode/rules" }), []);
  await assert.rejects(invoke("local_fs_list_dir", { projectRoot: root, dirPath: "a.txt" }), /path_is_not_directory/);
});

test("local_fs_read_text_file / local_fs_write_text_file round-trip text like Go", async () => {
  freshState();
  const dir = folder("proj", { "a.txt": "héllo\n", src: {} });
  const root = await openFolder(dir);
  assert.equal(await invoke("local_fs_read_text_file", { projectRoot: root, filePath: "a.txt" }), "héllo\n");
  assert.equal(await invoke("local_fs_write_text_file", { projectRoot: root, filePath: "a.txt", content: "changed" }), undefined);
  assert.equal(fileText(dir, "a.txt"), "changed");
  // Missing parent directories are created (os.MkdirAll).
  await invoke("local_fs_write_text_file", { projectRoot: root, filePath: "new/deep/c.md", content: "# c" });
  assert.equal(fileText(dir, "new/deep/c.md"), "# c");
  // createOnly never overwrites a file or a directory.
  await assert.rejects(invoke("local_fs_write_text_file", { projectRoot: root, filePath: "a.txt", content: "x", createOnly: true }), /already_exists/);
  await assert.rejects(invoke("local_fs_write_text_file", { projectRoot: root, filePath: "src", content: "x", createOnly: true }), /already_exists/);
  await invoke("local_fs_write_text_file", { projectRoot: root, filePath: "fresh.txt", content: "new", createOnly: true });
  assert.equal(fileText(dir, "fresh.txt"), "new");
  assert.equal(fileText(dir, "a.txt"), "changed");
  await assert.rejects(invoke("local_fs_write_text_file", { projectRoot: root, filePath: "src", content: "x" }), /path_is_directory/);
  await assert.rejects(invoke("local_fs_read_text_file", { projectRoot: root, filePath: "src" }), /path_is_directory/);
  await assert.rejects(invoke("local_fs_read_text_file", { projectRoot: root, filePath: "nope.txt" }), /path_resolve_failed/);
});

test("local_fs_read_text_file / local_fs_write_text_file keep Go's 5 MB limits", async () => {
  freshState();
  const big = new Uint8Array(5 * 1024 * 1024 + 1).fill(97);
  const dir = folder("proj", { "big.txt": big });
  const root = await openFolder(dir);
  await assert.rejects(invoke("local_fs_read_text_file", { projectRoot: root, filePath: "big.txt" }), /^Error: file_too_large: 5242881 bytes \(max 5242880\)$/);
  // The limit counts UTF-8 bytes, not UTF-16 code units.
  const content = "é".repeat(2.5 * 1024 * 1024 + 1);
  assert.ok(content.length <= 5 * 1024 * 1024);
  await assert.rejects(
    invoke("local_fs_write_text_file", { projectRoot: root, filePath: "out.txt", content }),
    /^Error: content_too_large: 5242882 bytes \(max 5242880\)$/,
  );
  await assert.rejects(invoke("local_fs_write_text_file", { projectRoot: root, filePath: "out.txt", content: "a".repeat(5 * 1024 * 1024 + 1) }), /content_too_large/);
  assert.equal(dir.children.has("out.txt"), false);
});

test("local_fs_read_image_data_url returns a data URL with the extension's MIME type", async () => {
  freshState();
  const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 1, 2, 255]);
  const root = await openFolder(folder("proj", { img: { "logo.PNG": png, "x.svg": "<svg/>" }, "a.txt": "A" }));
  assert.equal(
    await invoke("local_fs_read_image_data_url", { projectRoot: root, filePath: "img/logo.PNG" }),
    `data:image/png;base64,${Buffer.from(png).toString("base64")}`,
  );
  assert.equal(
    await invoke("local_fs_read_image_data_url", { projectRoot: root, filePath: "img/x.svg" }),
    `data:image/svg+xml;base64,${Buffer.from("<svg/>").toString("base64")}`,
  );
  await assert.rejects(invoke("local_fs_read_image_data_url", { projectRoot: root, filePath: "a.txt" }), /unsupported_image_type/);
  await assert.rejects(invoke("local_fs_read_image_data_url", { projectRoot: root, filePath: "img" }), /not_a_file/);
});

test("local_fs_file_info reports isFile and size", async () => {
  freshState();
  const root = await openFolder(folder("proj", { "a.txt": "12345", src: {} }));
  assert.deepEqual(await invoke("local_fs_file_info", { projectRoot: root, filePath: "a.txt" }), { isFile: true, size: 5 });
  assert.deepEqual(await invoke("local_fs_file_info", { projectRoot: root, filePath: "src" }), { isFile: false, size: 0 });
  assert.deepEqual(await invoke("local_fs_file_info", { projectRoot: root, filePath: "" }), { isFile: false, size: 0 });
  // The AI drawer checks a conversation's project with filePath "." (Go treats it as the root).
  assert.deepEqual(await invoke("local_fs_file_info", { projectRoot: root, filePath: "." }), { isFile: false, size: 0 });
  await assert.rejects(invoke("local_fs_file_info", { projectRoot: root, filePath: "missing.txt" }), /path_resolve_failed/);
});

// ---------- mkdir / rename / delete ----------

test("local_fs_mkdir creates nested directories and is a no-op for existing ones", async () => {
  freshState();
  const dir = folder("proj", { "a.txt": "A" });
  const root = await openFolder(dir);
  assert.equal(await invoke("local_fs_mkdir", { projectRoot: root, dirPath: "x/y/z" }), undefined);
  assert.equal(dir.children.get("x").children.get("y").children.get("z").kind, "directory");
  await invoke("local_fs_mkdir", { projectRoot: root, dirPath: "x/y" });
  await assert.rejects(invoke("local_fs_mkdir", { projectRoot: root, dirPath: "a.txt" }), /path_is_not_directory/);
});

test("local_fs_rename renames files and directories in place (with and without handle.move)", async () => {
  for (const withMove of [false, true]) {
    freshState();
    const dir = folder("proj", { "a.txt": "A", src: { "b.ts": "B", nested: { "c.ts": "C" } }, taken: "" });
    if (withMove) {
      // Chrome's FileSystemHandle.move(newName) renames within the same parent.
      const addMove = (parent) => {
        for (const child of parent.children.values()) {
          child.move = async (newName) => {
            accessLog.push(`move:${child.name}`);
            parent.children.delete(child.name);
            child.name = newName;
            parent.children.set(newName, child);
          };
          if (child.kind === "directory") addMove(child);
        }
      };
      addMove(dir);
    }
    const root = await openFolder(dir);
    assert.equal(await invoke("local_fs_rename", { projectRoot: root, oldPath: "a.txt", newName: "renamed.txt" }), undefined);
    assert.equal(fileText(dir, "renamed.txt"), "A");
    assert.equal(dir.children.has("a.txt"), false);
    await invoke("local_fs_rename", { projectRoot: root, oldPath: "src", newName: "lib" });
    assert.equal(fileText(dir, "lib/b.ts"), "B");
    assert.equal(fileText(dir, "lib/nested/c.ts"), "C");
    assert.equal(dir.children.has("src"), false);
    assert.equal(accessLog.some((entry) => entry.startsWith("move:")), withMove);

    await assert.rejects(invoke("local_fs_rename", { projectRoot: root, oldPath: "renamed.txt", newName: "taken" }), /already_exists/);
    await assert.rejects(invoke("local_fs_rename", { projectRoot: root, oldPath: "renamed.txt", newName: "lib" }), /already_exists/);
    await assert.rejects(invoke("local_fs_rename", { projectRoot: root, oldPath: "", newName: "x" }), /cannot_rename_project_root/);
    await assert.rejects(invoke("local_fs_rename", { projectRoot: root, oldPath: "missing", newName: "x" }), /path_resolve_failed/);
  }
});

test("local_fs_delete removes files and whole directories but never the project root", async () => {
  freshState();
  const dir = folder("proj", { "a.txt": "A", src: { deep: { "b.ts": "B" } } });
  const root = await openFolder(dir);
  assert.equal(await invoke("local_fs_delete", { projectRoot: root, path: "a.txt" }), undefined);
  await invoke("local_fs_delete", { projectRoot: root, path: "src" });
  assert.deepEqual([...dir.children.keys()], []);
  await assert.rejects(invoke("local_fs_delete", { projectRoot: root, path: "" }), /cannot_delete_project_root/);
  await assert.rejects(invoke("local_fs_delete", { projectRoot: root, path: "missing" }), /path_resolve_failed/);
});

// ---------- search ----------

test("local_fs_search finds names and first content lines, skipping Go's generated directories", async () => {
  freshState();
  const binary = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, ...new TextEncoder().encode("needle")]);
  const root = await openFolder(folder("proj", {
    "README.md": "intro\n  Find the NEEDLE here  \r\nneedle again\n",
    "needle.txt": "nothing",
    src: { "main.ts": "const x = 1;\nconsole.log('needle');\n", "logo.png": binary },
    node_modules: { "needle-pkg": { "index.js": "needle", "modules.js": "" } },
    ".git": { HEAD: "needle" },
    dist: { "out.js": "needle" },
    "big.txt": `${"a".repeat(1024 * 1024)}needle`,
  }));

  const results = await invoke("local_fs_search", { projectRoot: root, scopePath: null, query: "  NeEdLe ", includeContent: true });
  const byKey = new Map(results.map((row) => [`${row.kind}:${row.relativePath}`, row]));
  assert.deepEqual(byKey.get("content:README.md"), {
    name: "README.md",
    path: `${root}/README.md`,
    relativePath: "README.md",
    isDir: false,
    kind: "content",
    line: 2,
    lineText: "Find the NEEDLE here",
  });
  assert.deepEqual(byKey.get("name:needle.txt"), {
    name: "needle.txt",
    path: `${root}/needle.txt`,
    relativePath: "needle.txt",
    isDir: false,
    kind: "name",
    line: null,
    lineText: null,
  });
  assert.equal(byKey.get("content:src/main.ts").line, 2);
  // Skipped directories are not descended into.
  assert.equal(byKey.has("name:node_modules/needle-pkg"), false);
  assert.equal(byKey.has("content:node_modules/needle-pkg/index.js"), false);
  assert.equal(byKey.has("content:.git/HEAD"), false);
  assert.equal(byKey.has("content:dist/out.js"), false);
  // Binary files and files over 1 MB are not searched for content.
  assert.equal(byKey.has("content:src/logo.png"), false);
  assert.equal(byKey.has("content:big.txt"), false);
  assert.equal(results.length, 3);
  // ...but a skipped directory's own name still matches.
  const skipped = await invoke("local_fs_search", { projectRoot: root, scopePath: null, query: "modules", includeContent: false });
  assert.deepEqual(skipped.map((row) => [row.relativePath, row.isDir]), [["node_modules", true]]);

  const namesOnly = await invoke("local_fs_search", { projectRoot: root, scopePath: null, query: "needle", includeContent: false });
  assert.deepEqual(namesOnly.map((row) => `${row.kind}:${row.relativePath}`).sort(), ["name:needle.txt"]);
  const scoped = await invoke("local_fs_search", { projectRoot: root, scopePath: "src", query: "needle", includeContent: true });
  assert.deepEqual(scoped.map((row) => `${row.kind}:${row.relativePath}`), ["content:src/main.ts"]);
  assert.deepEqual(await invoke("local_fs_search", { projectRoot: root, scopePath: null, query: "   ", includeContent: true }), []);
  await assert.rejects(invoke("local_fs_search", { projectRoot: root, scopePath: "README.md", query: "x" }), /scope_not_a_directory/);
});

test("local_fs_search caps results at 300 like Go and honours the abort signal", async () => {
  freshState();
  const tree = {};
  for (let index = 0; index < 400; index += 1) tree[`match-${String(index).padStart(3, "0")}.txt`] = "";
  const root = await openFolder(folder("proj", tree));
  const results = await invoke("local_fs_search", { projectRoot: root, scopePath: null, query: "match", includeContent: false });
  assert.equal(results.length, 300);
  assert.equal(results[0].name, "match-000.txt");

  const controller = new AbortController();
  controller.abort(new Error("stopped"));
  await assert.rejects(invoke("local_fs_search", { projectRoot: root, scopePath: null, query: "match" }, controller.signal), /stopped/);
});

// ---------- project page: access banner across several remembered roots ----------

const { reactive, nextTick } = await import("vue");
const { useBrowserProjectAccess, isBrowserProjectAccessError } = await import("../src/composables/use-browser-project-access.ts");

/** The slice of stores/local-project.ts the composable uses, backed by the real permission calls. */
function accessStore(projectRoot, rememberedRoots = [projectRoot]) {
  const store = reactive({
    projectRoot,
    projects: rememberedRoots.map((root) => ({ root, name: root.split("/").pop() })),
    browserRootAccess: {},
    async refreshBrowserRootAccess(root) {
      const access = await browserProjectPermission(root);
      store.browserRootAccess = { ...store.browserRootAccess, [root]: access };
      return access;
    },
    async requestBrowserRootAccess(root) {
      const granted = await requestBrowserProjectPermission(root);
      if (granted) store.browserRootAccess = { ...store.browserRootAccess, [root]: "granted" };
      else await store.refreshBrowserRootAccess(root);
      return granted;
    },
  });
  return store;
}

test("after a reload a second remembered root that lost permission shows the banner instead of an empty folder", async () => {
  freshState();
  const permissionA = { state: "granted" };
  const permissionB = { state: "granted" };
  const rootA = await openFolder(folder("alpha", { "a.txt": "A" }, permissionA));
  const rootB = await openFolder(folder("beta", { "b.txt": "B" }, permissionB));
  // Reload: the handles come back from IndexedDB; Chrome re-grants only the folder the user re-authorized.
  __setBrowserProjectDatabaseForTests(database);
  permissionB.state = "prompt";

  const store = accessStore(rootA, [rootA, rootB]);
  const access = useBrowserProjectAccess(store);
  assert.equal(await access.check(rootA), true);
  assert.equal(access.banner.value, null);

  // Expanding root B in the tree (B is not the current project) fails with the permission error...
  const failure = await invoke("local_fs_list_dir", { projectRoot: rootB, dirPath: null }).catch((error) => error);
  assert.equal(isBrowserProjectAccessError(failure), true);
  // ...which must surface the banner for B rather than resolving an empty folder silently.
  assert.equal(access.noteFailure(rootB, failure), true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  await nextTick();
  assert.deepEqual(access.banner.value, { root: rootB, name: "beta", access: "prompt" });

  // Clicking root B activates it: the view's activateProject re-checks access; banner stays on B.
  store.projectRoot = rootB;
  assert.equal(await access.check(rootB), false);
  assert.deepEqual(access.banner.value, { root: rootB, name: "beta", access: "prompt" });

  // The user clicks Re-authorize.
  permissionB.grantOnRequest = true;
  assert.equal(await access.reauthorize(rootB), true);
  assert.equal(access.banner.value, null);
  assert.equal(await invoke("local_fs_read_text_file", { projectRoot: rootB, filePath: "b.txt" }), "B");

  // Other failures are not access problems and leave the banner alone.
  assert.equal(access.noteFailure(rootA, new Error("path_resolve_failed: x not found")), false);
  assert.equal(access.banner.value, null);

  // A root whose handle is gone asks to choose the folder again.
  database.records.clear();
  __setBrowserProjectDatabaseForTests(database);
  store.projectRoot = rootA;
  assert.equal(await access.check(rootA), false);
  assert.deepEqual(access.banner.value, { root: rootA, name: "alpha", access: "missing" });
  // A typed disk path cannot be opened in the browser.
  assert.equal(access.isNativePathInBrowser("/Users/me/proj"), true);
  assert.equal(access.isNativePathInBrowser(rootA), false);
});

test("a removed browser project never comes back through the re-authorize banner", async () => {
  freshState();
  const permissionB = { state: "granted" };
  const rootA = await openFolder(folder("alpha", { "a.txt": "A" }));
  const rootB = await openFolder(folder("beta", { "b.txt": "B" }, permissionB));
  __setBrowserProjectDatabaseForTests(database);
  permissionB.state = "prompt";

  const store = accessStore(rootA, [rootA, rootB]);
  const access = useBrowserProjectAccess(store);
  assert.equal(await access.check(rootB), false);
  assert.deepEqual(access.banner.value, { root: rootB, name: "beta", access: "prompt" });

  // The user removes project B from the list (stores/local-project.ts removeProject).
  store.projects = store.projects.filter((entry) => entry.root !== rootB);
  await nextTick();
  assert.equal(access.banner.value, null);
  // A late failure report for the removed root does not bring the banner back either.
  access.noteFailure(rootB, new Error("project.browser.permission"));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(access.banner.value, null);
});

test("desktop: the access composable is inert", async () => {
  freshState();
  globalThis.tango = desktopRuntime;
  let refreshes = 0;
  const store = reactive({
    projectRoot: "browser://x/proj",
    projects: [{ root: "browser://x/proj", name: "proj" }],
    browserRootAccess: { "browser://x/proj": "prompt" },
    async refreshBrowserRootAccess() {
      refreshes += 1;
      return "prompt";
    },
    async requestBrowserRootAccess() {
      return false;
    },
  });
  const access = useBrowserProjectAccess(store);
  assert.equal(await access.check("browser://x/proj"), true);
  assert.equal(access.noteFailure("browser://x/proj", new Error("project.browser.permission")), false);
  assert.equal(access.banner.value, null);
  assert.equal(access.isNativePathInBrowser("/Users/me/proj"), false);
  assert.equal(access.pickerUnsupported, false);
  assert.equal(refreshes, 0);
});

test("ProjectWorkspaceView re-checks access when a root is activated and when its tree fails to load", async () => {
  const { readFile } = await import("node:fs/promises");
  const view = await readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8");
  const between = (start, end) => view.slice(view.indexOf(start), view.indexOf(end, view.indexOf(start)));
  assert.match(between("async function activateProject", "\n}\n"), /browserAccess\.check\(root\)/);
  assert.match(between("const loadTree: LoadFunction", "\n};\n"), /catch \(error\) \{\s*browserAccess\.noteFailure\(root, error\);/);
  assert.match(between("const syncChildren = async", "// 异步读取期间"), /browserAccess\.noteFailure\(data\.projectRoot, error\)/);
  assert.match(between("async function openProject", "\n}\n"), /browserAccess\.check\(normalized\)/);
  assert.match(between("async function openProject", "\n}\n"), /browserAccess\.noteFailure\(normalized, error\)/);
});
