import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  MENU_HOST_CONTEXT_KEYS,
  MENU_LIMITS,
  MENU_VISIBLE_PLUGIN_ITEMS,
  MenuContextFieldTooLongError,
  MenuPayloadTooLargeError,
  MenuTargetUnavailableError,
  PLUGIN_MENU_IDS,
  buildMenuInvokePayload,
  contextMenuItems,
  editorHostContext,
  extnameOf,
  isPluginContextMenuId,
  isReservedMenuContextKey,
  menuContextValues,
  menuItemVisible,
  menuPassContext,
  normalizeMenuContext,
  monacoMenuPrecondition,
  parseMenuContributions,
  projectTreeHostContext,
  sortMenuItems,
  splitMenuOverflow,
  tabHostContext,
  terminalHostContext,
  truncateUtf8,
} from "../src/domain/plugins/menus.ts";
import {
  databaseTreeHostContext,
  gitChangeHostContext,
  gitCommitHostContext,
  httpRequestHostContext,
  menuHostContext,
  menuTargetPassContext,
  redisKeyHostContext,
  sanitizeMenuRequestUrl,
  sftpFileHostContext,
  sshConnectionHostContext,
} from "../src/domain/plugins/menu-targets.ts";
import {
  SETTINGS_PAGES_CONTRIBUTION_POINT,
  SETTINGS_PAGE_LOCALES_MAX,
  SETTINGS_PAGE_MAX_ENTRIES,
  SETTINGS_PAGE_TITLE_MAX_RUNES,
  parseSettingsPageEntries,
  settingsPagePreviewTitle,
} from "../src/domain/plugins/settings-pages.ts";

import { parseWhenExpression } from "../src/domain/commands/when.ts";

const repo = fileURLToPath(new URL("../../", import.meta.url));
const corpus = join(repo, "tools/verify/fixtures/contributions");

function contribution(entries, overrides = {}) {
  return {
    pluginId: "acme",
    pluginName: "Acme",
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

function item(menu, command, extra = {}) {
  return { menu, command, title: command, order: 0, ...extra };
}

test("menus: ids and limits mirror the contract", async () => {
  const contract = JSON.parse(await readFile(join(repo, "plugins/api/contribution-points.json"), "utf8"));
  const point = contract.points.menus;
  assert.equal(point.status, "enabled");
  assert.equal(point.sinceHost, "0.1.0");
  assert.deepEqual([...PLUGIN_MENU_IDS], point.keys);
  assert.deepEqual({ ...MENU_LIMITS }, point.limits);
  // Every rendered menu has a host key list; commandPalette and tray have none.
  assert.deepEqual(
    Object.keys(MENU_HOST_CONTEXT_KEYS).sort(),
    point.keys.filter((key) => key !== "commandPalette" && key !== "tray").sort(),
  );
});

test("menus: host context keys of the new placements are exactly the contract ones", () => {
  assert.deepEqual(
    Object.fromEntries(
      [
        "git/changes/context",
        "git/commit/context",
        "database/tree/context",
        "ssh/connection/context",
        "sftp/file/context",
        "redis/key/context",
        "http/request/context",
      ].map((menu) => [menu, [...MENU_HOST_CONTEXT_KEYS[menu]]]),
    ),
    {
      "git/changes/context": ["git.group", "git.status", "git.extname"],
      "git/commit/context": ["git.isMerge", "git.hasRefs"],
      "database/tree/context": ["database.kind", "database.objectType", "database.tableKind", "database.connected"],
      "ssh/connection/context": ["ssh.status", "ssh.authType", "ssh.hasJumpHost"],
      "sftp/file/context": ["sftp.kind", "sftp.extname", "sftp.protocol"],
      "redis/key/context": ["redis.type", "redis.hasTtl"],
      "http/request/context": ["http.method", "http.scheme"],
    },
  );
  for (const key of ["git.x", "ssh.x", "sftp.x", "redis.x", "http.x"]) assert.equal(isReservedMenuContextKey(key), true, key);
  for (const key of ["github.ready", "httpx.on", "sshd.up", "sftpgo.x", "redisx.y", "gitlab.z"]) {
    assert.equal(isReservedMenuContextKey(key), false, key);
  }
});

test("menus: parsing keeps valid items and reports invalid ones", () => {
  const { items, issues } = parseMenuContributions([
    contribution([
      item("editor/context", "acme.format", { when: 'editor.languageId == "json"', group: "navigation", order: 5, passContext: true }),
      item("commandPalette", "acme.format", { hidden: true }),
      item("editor/context", "acme.format"),
      item("explorer/context", "acme.x"),
      item("terminal/context", "acme.y", { when: 'editor.kind == "text"' }),
      item("commandPalette", "acme.z", { when: "foo" }),
      item("commandPalette", "acme.p", { passContext: true }),
      item("tab/x", "acme.q"),
      item("terminal/context", "acme.h", { hidden: true }),
      item("terminal/context", "acme.o", { order: 1001 }),
      item("terminal/context", "acme.g", { group: "Bad Group" }),
      item("terminal/context", "acme.t", { title: "  " }),
      item("terminal/context", "acme.w", { when: "a ||" }),
      "garbage",
    ]),
    contribution([item("editor/context", "other.run")], { pluginId: "declarative", runtime: "declarative" }),
    contribution([item("editor/context", "other.run")], { pluginId: "inactive", activeInWorkspace: false }),
  ]);
  assert.deepEqual(
    items.map((entry) => [entry.menu, entry.command]),
    [
      ["editor/context", "acme.format"],
      ["commandPalette", "acme.format"],
    ],
  );
  assert.equal(items[0].passContext, true);
  assert.equal(items[0].group, "navigation");
  assert.equal(items[0].whenClauses.length, 1);
  assert.equal(items[1].hidden, true);
  assert.equal(issues.length, 12);
  assert.ok(issues.some((issue) => issue.message === "duplicate menu item"));
  assert.ok(issues.some((issue) => /terminal\/context does not provide/.test(issue.message)));
  assert.ok(issues.every((issue) => issue.pluginId === "acme"));
});

test("menus: valid fixture parses without issues", async () => {
  const manifest = JSON.parse(await readFile(join(corpus, "valid/menus-process/plugin.json"), "utf8"));
  const entries = Object.entries(manifest.contributes.menus).flatMap(([menu, list]) =>
    // The host copies title/titleLocalized from the referenced command (contrib_menus.go).
    list.map((entry) => ({
      order: 0,
      ...entry,
      menu,
      title: manifest.contributes.commands.find((command) => command.id === entry.command).title,
    })),
  );
  const { items, issues } = parseMenuContributions([contribution(entries)]);
  assert.deepEqual(issues, []);
  assert.equal(items.length, entries.length);
});

test("menus: host context keys and reserved prefixes", () => {
  assert.deepEqual(projectTreeHostContext({ projectName: "p", relativePath: "src/App.VUE", kind: "file", isProjectRoot: false }), {
    "resource.kind": "file",
    "resource.extname": ".vue",
    "resource.isProjectRoot": false,
  });
  assert.equal(projectTreeHostContext({ projectName: "p", relativePath: "a.b", kind: "directory", isProjectRoot: false })["resource.extname"], "");
  assert.equal(extnameOf(".gitignore"), "");
  assert.equal(extnameOf("dir.d/file"), "");
  assert.deepEqual(editorHostContext({ kind: "sql", languageId: "sql", readonly: false, selection: "x", database: { kind: "mysql" } }), {
    "editor.kind": "sql",
    "editor.languageId": "sql",
    "editor.hasSelection": true,
    "editor.readonly": false,
    "database.kind": "mysql",
  });
  assert.deepEqual(terminalHostContext({ kind: "docker" }), { "terminal.kind": "docker", "terminal.selection": false });
  assert.deepEqual(tabHostContext({ view: "terminal", pinned: true }), { "tab.view": "terminal", "tab.pinned": true });

  for (const key of ["editor.kind", "resource.x", "tab.view", "terminal.kind", "database.kind", "platform"]) {
    assert.equal(isReservedMenuContextKey(key), true, key);
  }
  assert.equal(isReservedMenuContextKey("acme.mode"), false);

  // Plugin keys cannot spoof host keys; keys of other menus are not exposed.
  const values = menuContextValues(
    "terminal/context",
    { "terminal.kind": "ssh", "terminal.selection": true, "editor.kind": "sql" },
    { "terminal.kind": "local", platform: "win", mode: "on" },
    "mac",
  );
  assert.deepEqual(values, { mode: "on", "terminal.kind": "ssh", "terminal.selection": true, platform: "mac" });
  for (const [menu, keys] of Object.entries(MENU_HOST_CONTEXT_KEYS)) {
    assert.ok(keys.every((key) => isReservedMenuContextKey(key)), menu);
  }
});

test("menus: when keys allow camelCase only after the first dot", () => {
  for (const key of ["editor.languageId", "resource.isProjectRoot", "acme.mode", "a..b", "x-y_z.q"]) {
    assert.doesNotThrow(() => parseWhenExpression(key), key);
  }
  for (const key of ["sideBarVisible", "Editor.kind", "a" + ".b".repeat(32)]) {
    assert.throws(() => parseWhenExpression(key), /context key/, key);
  }
});

test("menus: when evaluation against the menu context", () => {
  const [entry] = parseMenuContributions([
    contribution([item("project/tree/context", "acme.open", { when: 'resource.kind == "file" && resource.extname == ".json" && !resource.isProjectRoot && platform != "win"' })]),
  ]).items;
  const host = projectTreeHostContext({ projectName: "p", relativePath: "a.json", kind: "file", isProjectRoot: false });
  assert.equal(menuItemVisible(entry, menuContextValues("project/tree/context", host, {}, "mac")), true);
  assert.equal(menuItemVisible(entry, menuContextValues("project/tree/context", host, {}, "win")), false);
  const dir = projectTreeHostContext({ projectName: "p", relativePath: "a.json", kind: "directory", isProjectRoot: false });
  assert.equal(menuItemVisible(entry, menuContextValues("project/tree/context", dir, {}, "mac")), false);
});

test("menus: sorting and overflow", () => {
  const entries = [
    { group: "z", order: 0, pluginName: "A", title: "a" },
    { group: "", order: 5, pluginName: "B", title: "b" },
    { group: "", order: 5, pluginName: "A", title: "c" },
    { group: "", order: 1, pluginName: "Z", title: "d" },
    { group: "", order: 5, pluginName: "A", title: "a" },
  ];
  assert.deepEqual(sortMenuItems(entries, (entry) => entry.title).map((entry) => `${entry.group}/${entry.pluginName}/${entry.title}`), [
    "/Z/d",
    "/A/a",
    "/A/c",
    "/B/b",
    "z/A/a",
  ]);
  const many = Array.from({ length: MENU_VISIBLE_PLUGIN_ITEMS + 3 }, (_, index) => index);
  const split = splitMenuOverflow(many);
  assert.equal(split.visible.length, MENU_VISIBLE_PLUGIN_ITEMS);
  assert.deepEqual(split.overflow, [12, 13, 14]);
  assert.deepEqual(splitMenuOverflow([1, 2]), { visible: [1, 2], overflow: [] });
});

test("menus: passContext objects per menu", () => {
  assert.deepEqual(
    menuPassContext("project/tree/context", { projectName: "p", relativePath: "src", kind: "directory", isProjectRoot: false, path: "/w/p/src" }),
    { projectName: "p", relativePath: "src", kind: "directory", isProjectRoot: false, path: "/w/p/src" },
  );
  assert.deepEqual(
    menuPassContext("sql/editor/context", { kind: "sql", languageId: "sql", readonly: false, selection: "select 1", database: { kind: "mysql", name: "app" } }),
    { languageId: "sql", selection: "select 1", database: { kind: "mysql", name: "app" } },
  );
  // The plain editor menu never forwards database details.
  assert.deepEqual(menuPassContext("editor/context", { kind: "text", languageId: "ts", readonly: true, database: { kind: "mysql" } }), {
    languageId: "ts",
  });
  assert.deepEqual(menuPassContext("terminal/context", { kind: "ssh", selection: "" }), { kind: "ssh" });
  assert.deepEqual(menuPassContext("workbench/tab/context", { view: "terminal", pinned: true }), { view: "terminal" });
});

test("menus: payload truncation and size limit", () => {
  assert.deepEqual(truncateUtf8("héllo", 2), { text: "h", truncated: true });
  assert.deepEqual(truncateUtf8("abc", 3), { text: "abc", truncated: false });
  assert.equal(truncateUtf8("你好", 4).text, "你");

  const small = JSON.parse(buildMenuInvokePayload("terminal/context", { kind: "local", selection: "ls" }, "{}"));
  assert.deepEqual(small, { menu: "terminal/context", context: { kind: "local", selection: "ls" }, payload: "{}" });

  const long = "界".repeat(20000); // 60000 bytes
  const cut = JSON.parse(buildMenuInvokePayload("editor/context", { languageId: "text", selection: long }, ""));
  assert.equal(cut.context.truncated, true);
  assert.ok(new TextEncoder().encode(cut.context.selection).length <= MENU_LIMITS.selectionMaxBytes);

  assert.throws(
    () => buildMenuInvokePayload("editor/context", { languageId: "text", selection: "x" }, "y".repeat(MENU_LIMITS.passContextMaxBytes)),
    MenuPayloadTooLargeError,
  );
});

test("menus: Monaco precondition translation", () => {
  const parse = (menu, when) => parseMenuContributions([contribution([item(menu, "acme.run", when ? { when } : {})])]).items[0];
  const keys = new Map();
  const pluginKey = (key) => {
    if (!keys.has(key)) keys.set(key, `mincodePluginCtx${keys.size}`);
    return keys.get(key);
  };
  assert.equal(monacoMenuPrecondition(parse("editor/context"), "mac", pluginKey), "mincodeEditorKind != 'sql'");
  assert.equal(
    monacoMenuPrecondition(parse("sql/editor/context", 'database.kind == "mysql" && editor.hasSelection'), "mac", pluginKey),
    "mincodeEditorKind == 'sql' && mincodeDatabaseKind == 'mysql' && editorHasSelection",
  );
  assert.equal(
    monacoMenuPrecondition(parse("editor/context", '!editor.languageId == "json" && !editor.readonly && acme.mode != "off"'), "mac", pluginKey),
    "mincodeEditorKind != 'sql' && editorLangId != 'json' && !editorReadonly && mincodePluginCtx0 != 'off'",
  );
  assert.equal(monacoMenuPrecondition(parse("editor/context", 'platform == "win"'), "mac", pluginKey), null);
  assert.equal(monacoMenuPrecondition(parse("editor/context", 'platform == "mac"'), "mac", pluginKey), "mincodeEditorKind != 'sql'");
  assert.throws(() => monacoMenuPrecondition(parse("editor/context", 'acme.mode == "it\'s"'), "mac", pluginKey), /cannot be used/);
  assert.throws(() => monacoMenuPrecondition(parse("terminal/context"), "mac", pluginKey), /not a Monaco menu/);
});

test("menus: consumers load Monaco lazily (type-only imports)", async () => {
  const files = [
    "src/services/contributions/menus.ts",
    "src/services/monaco-menus.ts",
    "src/components/plugins/PluginMenuItems.vue",
    "src/domain/plugins/menus.ts",
  ];
  for (const file of files) {
    const source = await readFile(fileURLToPath(new URL(`../${file}`, import.meta.url)), "utf8");
    const eager = [...source.matchAll(/^\s*import\s+(?!type\s)[^;]*?from\s+["']([^"']+)["']/gm)]
      .map((match) => match[1])
      .filter((specifier) => /monaco-runtime|monaco-editor|monaco-keymap/.test(specifier));
    assert.deepEqual(eager, [], file);
    assert.doesNotMatch(source, /import\(\s*["']@\/services\/monaco-runtime/, file);
  }
});

// ---------------------------------------------------------------------------
// New placements (git, database tree, ssh, sftp, redis, http).

const gitChange = {
  projectName: "app",
  repositoryPath: "/w/app",
  relativePath: "src/Main.GO",
  oldRelativePath: "src/old.go",
  status: "renamed",
  group: "staged",
  path: "/w/app/src/Main.GO",
};
const gitCommit = {
  projectName: "app",
  repositoryPath: "/w/app",
  hash: "0123456789abcdef0123456789abcdef01234567",
  shortHash: "0123456",
  subject: "feat: add menus\n\nLong body with details",
  author: "Ada Lovelace",
  time: 1767225600,
  parents: ["aaa", "bbb"],
  refs: ["HEAD -> main", "tag: v1.0.0"],
};
const databaseTable = {
  connection: { id: "c1", name: "Prod", kind: "mysql" },
  connected: true,
  objectType: "table",
  database: "shop",
  table: { name: "orders", kind: "VIEW" },
};
const sshConnection = {
  connectionId: "s1",
  name: "web-1",
  group: "prod",
  host: "10.0.0.5",
  port: 2222,
  username: "deploy",
  status: "ready",
  authType: "key",
  hasJumpHost: true,
};
const sftpFile = {
  connection: { id: "s1", name: "web-1", protocol: "sftp", host: "10.0.0.5", port: 22, username: "deploy" },
  path: "/var/log/App.LOG",
  name: "App.LOG",
  kind: "file",
  size: 1024,
};
const redisKey = { connection: { id: "r1", name: "Cache" }, dbIndex: 2, key: "session:abc", type: "HASH", ttl: 60 };
const httpRequest = {
  collection: { id: "col1", name: "API" },
  request: { id: "req1", name: "List users", method: "post", url: "https://u:p@api.example.com/v1/users?api_key=secret#frag" },
};

test("menus: host context values per new placement", () => {
  assert.deepEqual(gitChangeHostContext(gitChange), { "git.group": "staged", "git.status": "renamed", "git.extname": ".go" });
  assert.equal(gitChangeHostContext({ ...gitChange, relativePath: "Makefile" })["git.extname"], "");
  assert.equal(gitChangeHostContext({ ...gitChange, relativePath: "vendor.d/", group: "untracked", status: "untracked" })["git.extname"], "");

  assert.deepEqual(gitCommitHostContext(gitCommit), { "git.isMerge": true, "git.hasRefs": true });
  assert.deepEqual(gitCommitHostContext({ ...gitCommit, parents: ["aaa"], refs: [] }), { "git.isMerge": false, "git.hasRefs": false });

  assert.deepEqual(databaseTreeHostContext(databaseTable), {
    "database.kind": "mysql",
    "database.objectType": "table",
    "database.tableKind": "view",
    "database.connected": true,
  });
  assert.deepEqual(
    databaseTreeHostContext({ connection: { id: "c1", name: "Prod", kind: "postgres" }, connected: false, objectType: "connection" }),
    { "database.kind": "postgres", "database.objectType": "connection", "database.tableKind": "", "database.connected": false },
  );

  assert.deepEqual(sshConnectionHostContext(sshConnection), { "ssh.status": "ready", "ssh.authType": "key", "ssh.hasJumpHost": true });

  assert.deepEqual(sftpFileHostContext(sftpFile), { "sftp.kind": "file", "sftp.extname": ".log", "sftp.protocol": "sftp" });
  assert.equal(sftpFileHostContext({ ...sftpFile, kind: "directory", path: "/srv/a.d", name: "a.d" })["sftp.extname"], "");

  assert.deepEqual(redisKeyHostContext(redisKey), { "redis.type": "hash", "redis.hasTtl": true });
  assert.equal(redisKeyHostContext({ ...redisKey, ttl: -1 })["redis.hasTtl"], false);
  assert.equal(redisKeyHostContext({ ...redisKey, ttl: 0 })["redis.hasTtl"], true);

  assert.deepEqual(httpRequestHostContext(httpRequest), { "http.method": "POST", "http.scheme": "https" });
  assert.deepEqual(httpRequestHostContext({ ...httpRequest, request: { ...httpRequest.request, url: "{{base}}/users" } }), {
    "http.method": "POST",
    "http.scheme": "",
  });

  // menuHostContext dispatches to the same functions, including the existing menus.
  assert.deepEqual(menuHostContext("git/changes/context", gitChange), gitChangeHostContext(gitChange));
  assert.deepEqual(menuHostContext("git/commit/context", gitCommit), gitCommitHostContext(gitCommit));
  assert.deepEqual(menuHostContext("database/tree/context", databaseTable), databaseTreeHostContext(databaseTable));
  assert.deepEqual(menuHostContext("ssh/connection/context", sshConnection), sshConnectionHostContext(sshConnection));
  assert.deepEqual(menuHostContext("sftp/file/context", sftpFile), sftpFileHostContext(sftpFile));
  assert.deepEqual(menuHostContext("redis/key/context", redisKey), redisKeyHostContext(redisKey));
  assert.deepEqual(menuHostContext("http/request/context", httpRequest), httpRequestHostContext(httpRequest));
  assert.deepEqual(menuHostContext("terminal/context", { kind: "ssh", selection: "x" }), { "terminal.kind": "ssh", "terminal.selection": true });
  assert.deepEqual(menuHostContext("workbench/tab/context", { view: "git", pinned: false }), { "tab.view": "git", "tab.pinned": false });

  // Every host context only uses the keys its menu declares.
  for (const [menu, target] of [
    ["git/changes/context", gitChange],
    ["git/commit/context", gitCommit],
    ["database/tree/context", databaseTable],
    ["ssh/connection/context", sshConnection],
    ["sftp/file/context", sftpFile],
    ["redis/key/context", redisKey],
    ["http/request/context", httpRequest],
  ]) {
    assert.deepEqual(Object.keys(menuHostContext(menu, target)).sort(), [...MENU_HOST_CONTEXT_KEYS[menu]].sort(), menu);
  }
});

/** Recursive key shape of a value: objects become sorted key -> shape maps, everything else its type. */
function shape(value) {
  if (Array.isArray(value)) return value.map(shape);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, shape(value[key])]));
  }
  return typeof value;
}

test("menus: passContext whitelists per placement", () => {
  // Inputs carry every sensitive field a caller might accidentally spread in;
  // every sensitive value contains the marker S3CRET.
  const secrets = {
    password: "S3CRET-password",
    passphrase: "S3CRET-passphrase",
    keyFile: "/home/u/.ssh/S3CRET-id_ed25519",
    privateKey: "S3CRET-BEGIN",
    jumpHost: { host: "S3CRET-bastion", password: "S3CRET-pw" },
    socksProxy: { host: "S3CRET-proxy", password: "S3CRET-pw" },
    postConnectCommands: ["S3CRET sudo -i"],
    description: "S3CRET notes",
    tunnel: { host: "S3CRET-tunnel", password: "S3CRET-pw" },
    filePath: "/data/S3CRET.sqlite",
    token: "S3CRET-token",
    options: { sslmode: "S3CRET" },
    value: "S3CRET redis value",
    headers: [{ key: "Authorization", value: "Bearer S3CRET" }],
    authToken: "S3CRET-bearer",
    body: "{\"S3CRET\":1}",
    email: "S3CRET@example.com",
    authorEmail: "S3CRET@example.com",
    totp: "S3CRET-123456",
    connectionString: "mysql://root:S3CRET@h/db",
  };
  const withSecrets = (target) => ({ ...secrets, ...target });

  const git = menuTargetPassContext("git/changes/context", withSecrets(gitChange));
  assert.deepEqual(git, {
    projectName: "app",
    repositoryPath: "/w/app",
    relativePath: "src/Main.GO",
    oldRelativePath: "src/old.go",
    status: "renamed",
    group: "staged",
    path: "/w/app/src/Main.GO",
  });
  assert.deepEqual(Object.keys(menuTargetPassContext("git/changes/context", { ...gitChange, oldRelativePath: "" })).sort(), [
    "group",
    "path",
    "projectName",
    "relativePath",
    "repositoryPath",
    "status",
  ]);

  const commit = menuTargetPassContext("git/commit/context", withSecrets({ ...gitCommit, author: "Ada Lovelace <ada@example.com>" }));
  assert.deepEqual(commit, {
    projectName: "app",
    repositoryPath: "/w/app",
    hash: gitCommit.hash,
    shortHash: "0123456",
    subject: "feat: add menus",
    author: "Ada Lovelace",
    time: 1767225600,
    parents: ["aaa", "bbb"],
    refs: ["HEAD -> main", "tag: v1.0.0"],
  });
  // The arrays are copies, not the caller's arrays.
  assert.notEqual(commit.parents, gitCommit.parents);
  // A bare email (no name) never becomes the author.
  for (const author of ["ada@example.com", "<ada@example.com>", " ada@example.com ", "ada.lovelace+git@example.com"]) {
    assert.equal(menuTargetPassContext("git/commit/context", { ...gitCommit, author }).author, "", author);
  }
  assert.equal(menuTargetPassContext("git/commit/context", { ...gitCommit, author: "Ada <ada@example.com>" }).author, "Ada");

  const database = menuTargetPassContext(
    "database/tree/context",
    withSecrets({
      ...databaseTable,
      connection: { ...databaseTable.connection, ...secrets, host: "db.internal", port: 3306, username: "root", database: "shop" },
      table: { ...databaseTable.table, ...secrets },
    }),
  );
  assert.deepEqual(shape(database), {
    connection: { id: "string", kind: "string", name: "string" },
    database: "string",
    objectType: "string",
    table: { kind: "string", name: "string" },
  });
  assert.deepEqual(database, {
    connection: { id: "c1", name: "Prod", kind: "mysql" },
    objectType: "table",
    database: "shop",
    table: { name: "orders", kind: "view" },
  });
  assert.deepEqual(
    menuTargetPassContext("database/tree/context", { connection: { id: "c1", name: "Prod", kind: "redis" }, connected: true, objectType: "connection" }),
    { connection: { id: "c1", name: "Prod", kind: "redis" }, objectType: "connection" },
  );

  const ssh = menuTargetPassContext("ssh/connection/context", withSecrets(sshConnection));
  assert.deepEqual(ssh, { connectionId: "s1", name: "web-1", group: "prod", host: "10.0.0.5", port: 2222, username: "deploy" });
  assert.deepEqual(Object.keys(menuTargetPassContext("ssh/connection/context", { ...sshConnection, group: "" })).sort(), [
    "connectionId",
    "host",
    "name",
    "port",
    "username",
  ]);

  const sftp = menuTargetPassContext("sftp/file/context", withSecrets({ ...sftpFile, connection: { ...sftpFile.connection, ...secrets } }));
  assert.deepEqual(sftp, {
    connection: { id: "s1", name: "web-1", protocol: "sftp", host: "10.0.0.5", port: 22, username: "deploy" },
    path: "/var/log/App.LOG",
    name: "App.LOG",
    kind: "file",
    size: 1024,
  });
  assert.equal("size" in menuTargetPassContext("sftp/file/context", { ...sftpFile, kind: "directory", size: undefined }), false);

  const redis = menuTargetPassContext(
    "redis/key/context",
    withSecrets({ ...redisKey, connection: { ...redisKey.connection, ...secrets, host: "h", port: 6379, username: "acl" } }),
  );
  assert.deepEqual(redis, { connection: { id: "r1", name: "Cache" }, dbIndex: 2, key: "session:abc", type: "hash", ttl: 60 });

  const http = menuTargetPassContext(
    "http/request/context",
    withSecrets({
      collection: { ...httpRequest.collection, ...secrets, variables: [{ key: "token", value: "S3CRET" }] },
      request: {
        ...httpRequest.request,
        ...secrets,
        params: [{ key: "a", value: "S3CRET" }],
        auth: { type: "bearer", token: "S3CRET" },
        cookies: "sid=S3CRET",
        docs: "S3CRET docs",
      },
      environment: { token: "S3CRET" },
    }),
  );
  assert.deepEqual(http, {
    collection: { id: "col1", name: "API" },
    request: { id: "req1", name: "List users", method: "POST", url: "https://api.example.com/v1/users", redacted: true },
  });
  const redacted = menuTargetPassContext("http/request/context", {
    ...httpRequest,
    request: { ...httpRequest.request, url: "https://hooks.example.com/services/T0/B0/AbCdEf1234567890XyZ12345" },
  });
  assert.deepEqual(redacted.request, {
    id: "req1",
    name: "List users",
    method: "POST",
    url: "https://hooks.example.com/services/T0/B0/{redacted}",
    redacted: true,
  });

  // No sensitive value survives anywhere (URL userinfo and query included).
  for (const context of [git, commit, database, ssh, sftp, redis, http]) {
    const text = JSON.stringify(context);
    assert.equal(text.includes("S3CRET"), false, text);
    assert.equal(text.includes("@example.com"), false, text);
    assert.equal(text.includes("api_key"), false, text);
  }
});

test("menus: sanitizeMenuRequestUrl", () => {
  const cases = [
    // [input, url, scheme, redacted]
    ["https://u:p@hooks.example.com/services/T0/B0/AbCdEf1234567890XyZ12345?token=x#f", "https://hooks.example.com/services/T0/B0/{redacted}", "https", true],
    ["http://api.example.com/v1/users?page=2", "http://api.example.com/v1/users", "http", false],
    ["https://api.example.com/v1/users#section?x=1", "https://api.example.com/v1/users", "https", false],
    ["HTTPS://API.example.com/A", "HTTPS://API.example.com/A", "https", false],
    // userinfo up to the last @ of the authority, including @ in the password; removing it sets redacted.
    ["https://user:p@ss@host.example.com:8443/x", "https://host.example.com:8443/x", "https", true],
    ["https://token@host.example.com", "https://host.example.com", "https", true],
    // @ in the path is not userinfo.
    ["https://host.example.com/users/@me", "https://host.example.com/users/@me", "https", false],
    // Telegram style bot<id>:<token> segment.
    ["https://api.telegram.org/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/sendMessage", "https://api.telegram.org/{redacted}/sendMessage", "https", true],
    // UUIDs are long credential-shaped segments too; the loss is accepted.
    ["https://api.example.com/orders/123e4567-e89b-12d3-a456-426614174000/items", "https://api.example.com/orders/{redacted}/items", "https", true],
    // Short secrets and long letter-only or digit-only segments are kept (documented heuristic limits).
    ["https://api.example.com/k/abc123/v", "https://api.example.com/k/abc123/v", "https", false],
    ["https://api.example.com/AbCdEf1234567890XyZ1234", "https://api.example.com/AbCdEf1234567890XyZ1234", "https", false],
    ["https://api.example.com/averyveryverylongresourcename", "https://api.example.com/averyveryverylongresourcename", "https", false],
    ["https://api.example.com/123456789012345678901234567890", "https://api.example.com/123456789012345678901234567890", "https", false],
    // %XX counts as one token character (never decoded): percent-encoded tokens are caught.
    ["https://api.example.com/AbCdEf1234567890XyZ1234%21", "https://api.example.com/{redacted}", "https", true],
    ["https://api.example.com/t/abc%2Bdef%2F1234567890abcdefg", "https://api.example.com/t/{redacted}", "https", true],
    ["https://api.example.com/t/abc%2Bdef%2F12", "https://api.example.com/t/abc%2Bdef%2F12", "https", false],
    // Base64 alphabet (+ and =) is part of the token charset.
    ["https://api.example.com/t/QWxhZGRpbjpvcGVuIHNlc2FtZQ+abc12", "https://api.example.com/t/{redacted}", "https", true],
    // A malformed % keeps the segment out of the token alphabet.
    ["https://api.example.com/100%off-AbCdEf1234567890XyZ", "https://api.example.com/100%off-AbCdEf1234567890XyZ", "https", false],
    // Matrix / path parameters are cut at the first ; of each segment.
    ["https://api.example.com/app;jsessionid=AbC123/x", "https://api.example.com/app/x", "https", false],
    ["https://api.example.com/a;v=1;sig=xyz/b", "https://api.example.com/a/b", "https", false],
    // A segment carrying key=value is replaced as a whole.
    ["https://api.example.com/api/key=abc/x", "https://api.example.com/api/{redacted}/x", "https", true],
    ["https://api.example.com/token=", "https://api.example.com/{redacted}", "https", true],
    // Backslashes are path separators (WHATWG, http/https): they never hide userinfo.
    ["https://u:p\\w@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https://host.example.com\\a\\b", "https://host.example.com/a/b", "https", false],
    // Unencoded / in the password or username: the userinfo runs to the last @ followed by something host-like.
    ["https://u:pa/ss@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https://u:pa/s/s@host.example.com/x", "https://host.example.com/x", "https", true],
    ["u:pa/ss@example.com/x", "example.com/x", "", true],
    // ...also when the text between : and / is numeric, empty, or there is no : at all.
    ["https://u:12/34@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https://u:/abc@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https://us/er@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https://u:12/34@localhost:8080/x", "https://localhost:8080/x", "https", true],
    // Unencoded ? or # in the password: userinfo is found before the query and fragment are cut.
    ["https://u:pa?ss@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https://u:pa#ss@host.example.com/x?y", "https://host.example.com/x", "https", true],
    ["https://u:p?a#s/s@host.example.com/x", "https://host.example.com/x", "https", true],
    // ...while an @ in the query or fragment of a well-formed host is not userinfo.
    ["https://host.example.com?email=a@b.com", "https://host.example.com", "https", false],
    ["https://host.example.com/x#a@b.com", "https://host.example.com/x", "https", false],
    // Any number of slashes after an http(s)/ws scheme starts the authority (WHATWG special schemes).
    ["https:/u:p@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https:///u:p@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https:\\\\\\u:p@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https:\\/\\u:p@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https:u:p@host.example.com/x", "https://host.example.com/x", "https", true],
    ["https:host.example.com/x", "https://host.example.com/x", "https", false],
    ["///u:p@host.example.com/x", "//host.example.com/x", "", true],
    // A well-formed host keeps @ in its path.
    ["https://host.example.com:8443/users/@me", "https://host.example.com:8443/users/@me", "https", false],
    ["https://host.example.com/users/@me.json", "https://host.example.com/users/@me.json", "https", false],
    ["https://localhost:3000/@scope/pkg.js", "https://localhost:3000/@scope/pkg.js", "https", false],
    ["{{host}}:{{port}}/users/@me", "{{host}}:{{port}}/users/@me", "", false],
    ["{{host}}/users/@a.b", "{{host}}/users/@a.b", "", false],
    // Known residual: a dot-less host with a numeric port reads like `user:12/34@`. A later @ followed by
    // something host-like wins (drop), otherwise the host is kept.
    ["https://myhost:8443/users/@me", "https://myhost:8443/users/@me", "https", false],
    ["https://myhost:8443/users/@me.json", "https://me.json", "https", true],
    // An authority with an invalid port and no recognizable host is replaced as a whole.
    ["https://host:abc/x", "https://{redacted}/x", "https", true],
    ["https://u:pa/ss/x", "https://{redacted}/ss/x", "https", true],
    // IPv6 literals with port and userinfo.
    ["https://u:p@[2001:db8::1]:8443/x?y", "https://[2001:db8::1]:8443/x", "https", true],
    ["http://u@[::1]", "http://[::1]", "http", true],
    ["http://u:1/2@[::1]:80/x", "http://[::1]:80/x", "http", true],
    ["http://[::1]/AbCdEf1234567890XyZ12345", "http://[::1]/{redacted}", "http", true],
    ["http://[fe80::1%25en0]:80/users/@me", "http://[fe80::1%25en0]:80/users/@me", "http", false],
    // Variables stay as typed and are never expanded.
    ["{{baseUrl}}/users", "{{baseUrl}}/users", "", false],
    ["{{baseUrl}}/users/{{userId}}?token={{token}}", "{{baseUrl}}/users/{{userId}}", "", false],
    ["https://{{host}}/v1/{{AbCdEf1234567890XyZ12345}}", "https://{{host}}/v1/{{AbCdEf1234567890XyZ12345}}", "https", false],
    ["{{user}}:{{pass}}@{{host}}/x", "{{host}}/x", "", true],
    // IDN hosts are kept as typed (no punycode, no decoding); userinfo is still removed.
    ["https://用户:密码@例子.测试/路径/AbCdEf1234567890XyZ12345?q=1", "https://例子.测试/路径/{redacted}", "https", true],
    ["https://bücher.example/straße", "https://bücher.example/straße", "https", false],
    // Scheme-less, protocol-relative and other schemes.
    ["u:p@example.com/x", "example.com/x", "", true],
    ["localhost:8080/x", "localhost:8080/x", "", false],
    ["//u:p@example.com/x?y", "//example.com/x", "", true],
    ["/relative/AbCdEf1234567890XyZ12345", "/relative/{redacted}", "", true],
    ["/users/@me.json", "/users/@me.json", "", false],
    ["ws://u:p@example.com/socket", "ws://example.com/socket", "", true],
    ["  https://example.com/a  ", "https://example.com/a", "https", false],
    ["", "", "", false],
  ];
  for (const [input, url, scheme, redacted] of cases) {
    assert.deepEqual(sanitizeMenuRequestUrl(input), { url, scheme, redacted }, input);
  }
  // Never any query, fragment or userinfo left.
  for (const [input] of cases) {
    const { url } = sanitizeMenuRequestUrl(input);
    assert.doesNotMatch(url, /[?#]/, input);
    assert.doesNotMatch(url, /^[^/]*(\/\/)?[^/]*@/, input);
    assert.equal(url.includes("\\"), false, input);
    assert.doesNotMatch(url, /[;=]/, input);
  }
  // Length is checked when the payload is built, in UTF-8 bytes (like httpCollections).
  const long = `https://example.com/${"a".repeat(MENU_LIMITS.urlMaxLength)}`;
  assert.equal(sanitizeMenuRequestUrl(long).url, long);
  assert.throws(
    () =>
      buildMenuInvokePayload(
        "http/request/context",
        menuTargetPassContext("http/request/context", { ...httpRequest, request: { ...httpRequest.request, url: long } }),
        "",
      ),
    (error) => error instanceof MenuContextFieldTooLongError && error.field === "url" && error.limit === MENU_LIMITS.urlMaxLength,
  );
  // The query does not count: a long query string is cut before the check.
  const longQuery = `https://example.com/x?${"q".repeat(5000)}`;
  assert.doesNotThrow(() =>
    buildMenuInvokePayload(
      "http/request/context",
      menuTargetPassContext("http/request/context", { ...httpRequest, request: { ...httpRequest.request, url: longQuery } }),
      "",
    ),
  );
});

test("menus: field limits", () => {
  // Commit subject: truncated on a code point boundary, flagged.
  const subject = "提".repeat(400); // 1200 bytes
  const commit = JSON.parse(buildMenuInvokePayload("git/commit/context", menuTargetPassContext("git/commit/context", { ...gitCommit, subject }), "")).context;
  assert.equal(commit.truncated, true);
  assert.equal(commit.subject, "提".repeat(341)); // 1023 bytes: the 342nd character would cross 1024
  assert.ok(new TextEncoder().encode(commit.subject).length <= MENU_LIMITS.commitSubjectMaxBytes);
  const short = JSON.parse(buildMenuInvokePayload("git/commit/context", menuTargetPassContext("git/commit/context", gitCommit), "")).context;
  assert.equal("truncated" in short, false);
  const exact = "a".repeat(MENU_LIMITS.commitSubjectMaxBytes);
  assert.equal(JSON.parse(buildMenuInvokePayload("git/commit/context", { subject: exact }, "")).context.truncated, undefined);

  // Remote path: refused above the byte limit, never cut; multi-byte counted in bytes.
  const okPath = "/" + "é".repeat(2047); // 4095 bytes (é is 2 bytes)
  assert.doesNotThrow(() => buildMenuInvokePayload("sftp/file/context", menuTargetPassContext("sftp/file/context", { ...sftpFile, path: okPath }), ""));
  const exactPath = okPath + "a"; // 4096 bytes
  assert.equal(JSON.parse(buildMenuInvokePayload("sftp/file/context", { path: exactPath }, "")).context.path, exactPath);
  assert.throws(
    () => buildMenuInvokePayload("sftp/file/context", { path: okPath + "é" }, ""), // 4097 bytes in 2049 characters
    (error) => error instanceof MenuContextFieldTooLongError && error.field === "path" && error.limit === MENU_LIMITS.remotePathMaxBytes,
  );

  // Redis key: refused, never cut.
  assert.doesNotThrow(() => buildMenuInvokePayload("redis/key/context", { key: "k".repeat(MENU_LIMITS.redisKeyMaxBytes) }, ""));
  assert.throws(
    () => buildMenuInvokePayload("redis/key/context", { key: "键".repeat(1366) }, ""), // 4098 bytes
    (error) => error instanceof MenuContextFieldTooLongError && error.field === "key" && error.limit === MENU_LIMITS.redisKeyMaxBytes,
  );

  // URL limit in bytes: 694 characters but 2062 bytes is refused.
  const idn = "https://例子.测试/" + "路".repeat(680);
  assert.throws(
    () => buildMenuInvokePayload("http/request/context", { request: { url: idn } }, ""),
    (error) => error instanceof MenuContextFieldTooLongError && error.field === "url",
  );

  // Limits only apply to their own placement.
  assert.doesNotThrow(() => buildMenuInvokePayload("project/tree/context", { path: "/" + "a".repeat(5000) }, ""));

  // normalizeMenuContext is the same check, usable before the command runs; it is idempotent.
  assert.throws(() => normalizeMenuContext("redis/key/context", { key: "k".repeat(5000) }), MenuContextFieldTooLongError);
  const normalized = normalizeMenuContext("git/commit/context", { subject });
  assert.deepEqual(normalizeMenuContext("git/commit/context", normalized), normalized);

  const error = new MenuContextFieldTooLongError("key", 4096);
  assert.ok(error instanceof Error);
  assert.equal(error.name, "MenuContextFieldTooLongError");
});

test("menus: plugin keys under new reserved prefixes are dropped", () => {
  const values = menuContextValues(
    "redis/key/context",
    redisKeyHostContext({ ...redisKey, type: "string", ttl: -1 }),
    { "redis.type": "hash", "redis.hasTtl": true, "git.status": "modified", "ssh.status": "ready", "sftp.kind": "file", "http.method": "GET", mode: "on" },
    "mac",
  );
  assert.deepEqual(values, { mode: "on", "redis.type": "string", "redis.hasTtl": false, platform: "mac" });

  const [entry] = parseMenuContributions([contribution([item("git/changes/context", "acme.run", { when: 'git.status == "modified"' })])]).items;
  // The plugin's own git.status cannot make the item visible.
  const added = gitChangeHostContext({ ...gitChange, status: "added" });
  assert.equal(menuItemVisible(entry, menuContextValues("git/changes/context", added, { "git.status": "modified" }, "mac")), false);
  const modified = gitChangeHostContext({ ...gitChange, status: "modified" });
  assert.equal(menuItemVisible(entry, menuContextValues("git/changes/context", modified, {}, "mac")), true);
});

test("menus: new menus reject host keys of other placements", () => {
  const { items, issues } = parseMenuContributions([
    contribution([
      item("ssh/connection/context", "acme.a", { when: 'git.status == "modified"' }),
      item("git/changes/context", "acme.b", { when: "git.isMerge" }),
      item("git/commit/context", "acme.c", { when: 'git.group == "staged"' }),
      item("database/tree/context", "acme.d", { when: "editor.hasSelection" }),
      item("sql/editor/context", "acme.e", { when: 'database.objectType == "table"' }),
      item("sftp/file/context", "acme.f", { when: 'resource.kind == "file"' }),
      item("redis/key/context", "acme.g", { when: 'database.kind == "redis"' }),
      item("http/request/context", "acme.h", { when: 'ssh.status == "ready"' }),
      item("project/tree/context", "acme.i", { when: 'http.method == "GET"' }),
      // Valid: own host keys, platform and plugin keys.
      item("http/request/context", "acme.ok", { when: 'http.method == "GET" && http.scheme == "https" && platform == "mac" && acme.mode', passContext: true }),
      item("database/tree/context", "acme.ok", {
        when: 'database.kind == "mysql" && database.objectType == "table" && database.tableKind == "view" && database.connected',
      }),
    ]),
  ]);
  assert.deepEqual(
    items.map((entry) => `${entry.menu} ${entry.command}`),
    ["http/request/context acme.ok", "database/tree/context acme.ok"],
  );
  assert.equal(issues.length, 9);
  assert.ok(issues.every((issue) => /does not provide/.test(issue.message)), JSON.stringify(issues));
});

test("menus: valid menus-more fixture parses without issues", async () => {
  const manifest = JSON.parse(await readFile(join(corpus, "valid/menus-more-process/plugin.json"), "utf8"));
  const entries = Object.entries(manifest.contributes.menus).flatMap(([menu, list]) =>
    list.map((entry) => ({
      order: 0,
      ...entry,
      menu,
      title: manifest.contributes.commands.find((command) => command.id === entry.command).title,
    })),
  );
  const { items, issues } = parseMenuContributions([contribution(entries)]);
  assert.deepEqual(issues, []);
  assert.equal(items.length, 7);
});

test("menus: usePluginMenuTarget builds host values and passContext from the open target", async () => {
  const { ref } = await import("vue");
  const { usePluginMenuTarget } = await import("../src/composables/use-plugin-menu-target.ts");
  const open = ref(null);
  const { host, context } = usePluginMenuTarget("redis/key/context", () => open.value);
  assert.equal(host.value, null);
  // A passContext item never sends an empty context: without a target it refuses to run.
  assert.throws(() => context(), MenuTargetUnavailableError);
  open.value = { ...redisKey, value: "S3CRET value" };
  assert.deepEqual(host.value, { "redis.type": "hash", "redis.hasTtl": true });
  assert.deepEqual(context(), { connection: { id: "r1", name: "Cache" }, dbIndex: 2, key: "session:abc", type: "hash", ttl: 60 });
  open.value = null;
  assert.equal(host.value, null);
  assert.throws(() => context(), MenuTargetUnavailableError);
});

test("menus: placement builders live outside menus.ts and load only through the shared composable", async () => {
  const source = await readFile(fileURLToPath(new URL("../src/domain/plugins/menus.ts", import.meta.url)), "utf8");
  for (const name of ["sanitizeMenuRequestUrl", "gitChangeHostContext", "menuHostContext", "menuTargetPassContext", "redisKeyHostContext"]) {
    assert.doesNotMatch(source, new RegExp(`function ${name}\\b`), name);
  }
  const { readdir } = await import("node:fs/promises");
  const srcRoot = fileURLToPath(new URL("../src/", import.meta.url));
  // Type-only imports are erased; value imports, re-exports and dynamic imports (alias or relative
  // specifier, with or without extension) pull the module into a chunk.
  const valueImport =
    /(?:import|export)\s+(?!type\s)[^;]*?from\s+["'][^"']*\/menu-targets(?:\.ts)?["']|import\s*\(\s*["'][^"']*\/menu-targets(?:\.ts)?["']/;
  for (const sample of [
    'import { menuHostContext } from "@/domain/plugins/menu-targets";',
    'import { menuHostContext } from "./menu-targets";',
    "import { menuHostContext } from '../../domain/plugins/menu-targets.ts';",
    'import {\n  menuHostContext,\n} from "../plugins/menu-targets";',
    'export { sanitizeMenuRequestUrl } from "./menu-targets";',
    'const mod = await import("./menu-targets");',
  ]) {
    assert.match(sample, valueImport, sample);
  }
  for (const sample of ['import type { GitChangeMenuTarget } from "./menu-targets";', 'import { x } from "./menu-targets-extra";']) {
    assert.doesNotMatch(sample, valueImport, sample);
  }
  const importers = [];
  const serviceSurfaceImporters = [];
  // service-surfaces.ts reuses the passContext builders and the URL sanitizer; it stays out of the
  // entry chunk too: the modules the app shell loads (services.ts, plugin-services.ts) import it type-only.
  const serviceSurfacesImport =
    /(?:import|export)\s+(?!type\s)[^;]*?from\s+["'][^"']*\/service-surfaces(?:\.ts)?["']|import\s*\(\s*["'][^"']*\/service-surfaces(?:\.ts)?["']/;
  for (const entry of await readdir(srcRoot, { recursive: true, withFileTypes: true })) {
    if (!entry.isFile() || !/\.(ts|vue)$/.test(entry.name)) continue;
    const file = join(entry.parentPath ?? entry.path, entry.name);
    const text = await readFile(file, "utf8");
    if (!file.endsWith("menu-targets.ts") && valueImport.test(text)) importers.push(file.slice(srcRoot.length));
    if (serviceSurfacesImport.test(text)) serviceSurfaceImporters.push(file.slice(srcRoot.length));
  }
  assert.deepEqual(importers.sort(), ["composables/use-plugin-menu-target.ts", "domain/plugins/service-surfaces.ts"]);
  for (const shell of ["domain/plugins/services.ts", "services/plugin-services.ts", "App.vue", "main.ts"]) {
    assert.ok(!serviceSurfaceImporters.includes(shell), shell);
  }
});

test("menus: the entry chunk's static import graph never reaches the menu target builders or the service menu", async () => {
  const { dirname, resolve } = await import("node:path");
  const { statSync } = await import("node:fs");
  const srcRoot = fileURLToPath(new URL("../src/", import.meta.url));
  // Static value imports and re-exports; `import type` / `export type` are erased and import() is a separate chunk.
  const staticImport = /(?:^|[\n;])\s*(?:import|export)\s+(?!type[\s{])(?:[^'"`;]*?\sfrom\s*)?["']([^"']+)["']/g;
  const resolveSpecifier = (specifier, from) => {
    let base;
    if (specifier.startsWith("@/")) base = join(srcRoot, specifier.slice(2));
    else if (specifier.startsWith(".")) base = resolve(dirname(from), specifier);
    else return null;
    for (const suffix of ["", ".ts", ".vue", ".js", ".mjs", "/index.ts"]) {
      const candidate = base + suffix;
      if (statSync(candidate, { throwIfNoEntry: false })?.isFile()) return candidate;
    }
    return null;
  };
  const seen = new Set();
  const queue = [join(srcRoot, "main.ts")];
  while (queue.length) {
    const file = queue.pop();
    if (seen.has(file)) continue;
    seen.add(file);
    if (!/\.(ts|vue|js|mjs)$/.test(file)) continue;
    let text = await readFile(file, "utf8");
    if (file.endsWith(".vue")) text = [...text.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)].map((match) => match[1]).join("\n");
    for (const match of text.matchAll(staticImport)) {
      const target = resolveSpecifier(match[1], file);
      if (target && !seen.has(target)) queue.push(target);
    }
  }
  const reached = [...seen].map((file) => file.slice(srcRoot.length));
  assert.ok(reached.includes("App.vue") && reached.length > 50, `entry graph too small (${reached.length})`);
  for (const lazy of [
    "domain/plugins/menu-targets.ts",
    "domain/plugins/service-surfaces.ts",
    "components/plugins/PluginServiceMenuItems.vue",
    // settingsPages: the consumer keeps only the raw contributions in the entry; parsing and the page section load with Settings.
    "domain/plugins/settings-pages.ts",
    "services/contributions/settings-pages-runtime.ts",
    "components/settings/PluginSettingsPageSection.vue",
    "components/plugins/PluginViewFrame.vue",
  ]) {
    assert.ok(!reached.includes(lazy), `${lazy} is statically reachable from main.ts`);
  }
});

test("menus: tray is a menu id but never a context menu", async () => {
  const contract = JSON.parse(await readFile(join(repo, "vue-app/src/domain/plugins/contribution-points.json"), "utf8"));
  const limits = contract.points.menus.limits;
  assert.ok(PLUGIN_MENU_IDS.includes("tray"));
  assert.equal(PLUGIN_MENU_IDS.indexOf("tray"), PLUGIN_MENU_IDS.indexOf("commandPalette") - 1);
  assert.equal(isPluginContextMenuId("tray"), false);
  assert.equal(isPluginContextMenuId("commandPalette"), false);
  assert.equal(isPluginContextMenuId("editor/context"), true);
  assert.equal(isPluginContextMenuId(7), false);
  assert.equal("tray" in MENU_HOST_CONTEXT_KEYS, false);
  for (const name of ["trayMaxPerPlugin", "trayPluginsMax", "trayLabelMaxRunes"]) assert.equal(MENU_LIMITS[name], limits[name], name);
  assert.deepEqual([MENU_LIMITS.trayMaxPerPlugin, MENU_LIMITS.trayPluginsMax, MENU_LIMITS.trayLabelMaxRunes], [4, 8, 48]);

  const { items, issues } = parseMenuContributions([
    contribution([
      item("tray", "acme.a", { group: "g", order: 3 }),
      item("tray", "acme.b"),
      item("tray", "acme.w", { when: "acme.ready" }),
      item("tray", "acme.p", { passContext: true }),
      item("tray", "acme.h", { hidden: true }),
    ]),
  ]);
  assert.deepEqual(
    items.map((entry) => [entry.menu, entry.command]),
    [
      ["tray", "acme.a"],
      ["tray", "acme.b"],
    ],
  );
  assert.equal(issues.length, 3);
  assert.ok(issues.every((issue) => /tray item acme\.[wph] accepts only command, group and order/.test(issue.message)));
  const tooMany = parseMenuContributions([contribution(["a", "b", "c", "d", "e"].map((id) => item("tray", `acme.${id}`)))]);
  assert.equal(tooMany.items.length, 4);
  assert.deepEqual(
    tooMany.issues.map((issue) => issue.message),
    ["menu tray allows at most 4 items"],
  );
});

test("menus: tray entries never render in app menus", async () => {
  const { items } = parseMenuContributions([
    contribution([
      item("tray", "acme.a"),
      item("editor/context", "acme.b"),
      item("terminal/context", "acme.c"),
      item("commandPalette", "acme.d"),
    ]),
  ]);
  assert.equal(items.length, 4);
  for (const menu of PLUGIN_MENU_IDS) {
    const shown = contextMenuItems(items, menu);
    assert.ok(shown.every((entry) => entry.menu === menu && entry.menu !== "tray" && entry.menu !== "commandPalette"), menu);
  }
  assert.deepEqual(contextMenuItems(items, "tray"), []);
  assert.deepEqual(contextMenuItems(items, "commandPalette"), []);
  assert.deepEqual(contextMenuItems(items, "editor/context").map((entry) => entry.command), ["acme.b"]);
  // The host menu resolver selects its candidates through contextMenuItems only.
  const consumer = await readFile(join(repo, "vue-app/src/services/contributions/menus.ts"), "utf8");
  assert.match(consumer, /contextMenuItems\(items\.value, menu\)/);
  assert.doesNotMatch(consumer, /item\.menu === menu/);
});

test("settingsPages: parse snapshot entries", async () => {
  const contract = JSON.parse(await readFile(join(repo, "vue-app/src/domain/plugins/contribution-points.json"), "utf8"));
  const point = contract.points.settingsPages;
  assert.equal(SETTINGS_PAGES_CONTRIBUTION_POINT, "settingsPages");
  assert.equal(SETTINGS_PAGE_MAX_ENTRIES, point.maxEntries);
  assert.equal(SETTINGS_PAGE_TITLE_MAX_RUNES, point.limits.titleMaxRunes);
  assert.equal(SETTINGS_PAGE_LOCALES_MAX, point.limits.localesMax);

  const pages = parseSettingsPageEntries(
    contribution([
      { id: "main", view: "settings", title: "Rules", titleLocalized: { zh: "规则", bad: 7 } },
      { id: "nov", title: "No view" },
    ]),
  );
  assert.deepEqual(pages, [
    {
      pluginId: "acme",
      pluginName: "Acme",
      id: "main",
      view: "settings",
      title: "Rules",
      titleLocalized: { zh: "规则" },
      activeInWorkspace: true,
    },
  ]);
  // A missing title falls back to "" (the caller then uses the view title).
  const untitled = parseSettingsPageEntries(contribution([{ id: "main", view: "settings" }], { activeInWorkspace: false }));
  assert.deepEqual(untitled, [{ pluginId: "acme", pluginName: "Acme", id: "main", view: "settings", title: "", activeInWorkspace: false }]);
  // Only the first entry counts (maxEntries 1); bad ids, views, titles and declarative plugins are dropped.
  assert.equal(parseSettingsPageEntries(contribution([{ id: "a", view: "one" }, { id: "b", view: "two" }])).length, 1);
  assert.deepEqual(parseSettingsPageEntries(contribution([{ id: "main", title: "No view" }])), []);
  assert.deepEqual(parseSettingsPageEntries(contribution([{ id: "", view: "settings" }])), []);
  assert.deepEqual(parseSettingsPageEntries(contribution([{ id: "main", view: "bad view" }])), []);
  assert.deepEqual(parseSettingsPageEntries(contribution([{ id: "main", view: "settings", title: "x".repeat(49) }])), []);
  assert.deepEqual(parseSettingsPageEntries(contribution([{ id: "main", view: "settings" }], { runtime: "declarative" })), []);
  assert.deepEqual(parseSettingsPageEntries(contribution(["garbage", null])), []);
  // A 60-character view title reaches the snapshot cut to 48 runes by the host
  // (settingsPageInheritedTitle), so the page is kept, not silently dropped.
  const viewTitle = "a".repeat(59) + "規";
  assert.equal(parseSettingsPageEntries(contribution([{ id: "main", view: "settings", title: viewTitle }])).length, 0);
  const inherited = parseSettingsPageEntries(
    contribution([{ id: "main", view: "settings", title: viewTitle.slice(0, 48), titleLocalized: { zh: "规".repeat(48) } }]),
  );
  assert.equal(inherited.length, 1);
  assert.equal(inherited[0].title, "a".repeat(48));
  assert.deepEqual(inherited[0].titleLocalized, { zh: "规".repeat(48) });
});

test("settingsPages: install preview names the page title or the view title", () => {
  const contributes = {
    views: [{ id: "settings", title: "Proxy settings", titleLocalized: { zh: "代理设置" } }],
    settingsPages: [{ id: "main", view: "settings", title: "Rules", titleLocalized: { zh: "规则" } }],
  };
  assert.equal(settingsPagePreviewTitle(contributes, ["zh"]), "规则");
  assert.equal(settingsPagePreviewTitle(contributes, ["en"]), "Rules");
  // Without its own title the page uses the view title; without the view, its id.
  assert.equal(settingsPagePreviewTitle({ ...contributes, settingsPages: [{ id: "main", view: "settings" }] }, ["zh"]), "代理设置");
  assert.equal(settingsPagePreviewTitle({ settingsPages: [{ id: "main", view: "gone" }] }, ["en"]), "gone");
  assert.equal(settingsPagePreviewTitle({}, ["en"]), undefined);
  assert.equal(settingsPagePreviewTitle(undefined, ["en"]), undefined);
  assert.equal(settingsPagePreviewTitle({ settingsPages: ["x", { id: "main" }] }, ["en"]), undefined);
  // The same title checks as the snapshot parser: unsafe text or more than 48 runes is not shown.
  const view = { views: [{ id: "settings", title: "View" }] };
  assert.equal(settingsPagePreviewTitle({ ...view, settingsPages: [{ id: "main", view: "settings", title: "x".repeat(49) }] }, ["en"]), undefined);
  assert.equal(settingsPagePreviewTitle({ ...view, settingsPages: [{ id: "main", view: "settings", title: "Rules‮" }] }, ["en"]), undefined);
  const badLocalized = { id: "main", view: "settings", title: "Rules", titleLocalized: { zh: "规".repeat(49), ja: "a⁦b" } };
  assert.equal(settingsPagePreviewTitle({ ...view, settingsPages: [badLocalized] }, ["zh"]), "Rules");
  assert.equal(settingsPagePreviewTitle({ ...view, settingsPages: [badLocalized] }, ["ja"]), "Rules");
  const manyLocales = Object.fromEntries(Array.from({ length: 17 }, (_, index) => [`l${index}`, `T${index}`]));
  assert.equal(settingsPagePreviewTitle({ ...view, settingsPages: [{ id: "main", view: "settings", title: "Rules", titleLocalized: manyLocales }] }, ["l16"]), "Rules");
  // An inherited view title is cut to 48 runes and loses unsafe characters, like the host.
  const longView = { views: [{ id: "settings", title: `${"v".repeat(60)}`, titleLocalized: { zh: "‮代理设置" } }] };
  assert.equal(settingsPagePreviewTitle({ ...longView, settingsPages: [{ id: "main", view: "settings" }] }, ["en"]), "v".repeat(48));
  assert.equal(settingsPagePreviewTitle({ ...longView, settingsPages: [{ id: "main", view: "settings" }] }, ["zh"]), "代理设置");
});
