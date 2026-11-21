// ai.config: a plugin page proposes AI settings (model profiles, connectors,
// skills) through mincode.plugin.ai-config.preview / .apply; the host frame
// compares them, shows its own confirmation and only then writes.
import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";
import { fileURLToPath } from "node:url";
import vm from "node:vm";
import {
  parsePluginUIHostRequest,
  PLUGIN_UI_MAX_AI_CONFIG_AUTH_SOURCES,
  PLUGIN_UI_MAX_AI_CONFIG_ITEMS,
  PLUGIN_UI_MAX_MESSAGE_BYTES,
} from "../src/domain/plugin-ui/protocol.ts";
import {
  AI_CONFIG_FIELD_NAMES,
  aiConfigReviewRows,
  visibleReviewText,
} from "../src/domain/plugin-ui/ai-config.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");
const PLUGIN = "mincode.cc-switch-sync";

const profile = {
  kind: "profile",
  sourceId: "p1",
  name: "Kimi",
  sourceApp: "claude",
  profile: { baseUrl: "https://api.example.test", apiKey: "sk-secret-value", model: "kimi-k2", apiFormat: "anthropic", commonModels: [] },
};
const mcp = {
  kind: "mcp",
  sourceId: "m1",
  name: "fetch",
  sourceApp: "claude",
  mcp: {
    transport: "stdio",
    command: "npx",
    args: ["-y", "@scope/fetch server", ""],
    url: "",
    env: { API_TOKEN: "env-secret-value", EMPTY: "" },
    headers: {},
  },
};
const remote = {
  kind: "mcp",
  sourceId: "m2",
  name: "remote",
  sourceApp: "codex",
  mcp: { transport: "http", command: "", args: [], url: "https://mcp.example.test/sse", env: {}, headers: { Authorization: "Bearer header-secret" } },
};
function entry(item, status, extra = {}) {
  return {
    key: `${PLUGIN}/${item.kind}/${item.sourceId}`,
    kind: item.kind,
    name: item.name,
    sourceApp: item.sourceApp,
    status,
    needsSecret: false,
    changedFields: [],
    secretChanged: false,
    targetName: item.name,
    notes: [],
    ...extra,
  };
}

test("protocol parses ai-config requests", () => {
  const preview = { type: "mincode.plugin.ai-config.preview", id: "r1", items: [profile, mcp] };
  assert.deepEqual(parsePluginUIHostRequest(preview), preview);
  const apply = {
    type: "mincode.plugin.ai-config.apply",
    id: "r2",
    items: [profile],
    choices: [{ key: `${PLUGIN}/profile/p1`, action: "add" }],
  };
  assert.deepEqual(parsePluginUIHostRequest(apply), apply);

  assert.equal(parsePluginUIHostRequest({ ...preview, items: "nope" }), undefined);
  assert.equal(parsePluginUIHostRequest({ ...preview, items: { 0: profile } }), undefined);
  assert.equal(parsePluginUIHostRequest({ ...preview, items: Array(PLUGIN_UI_MAX_AI_CONFIG_ITEMS + 1).fill({}) }), undefined);
  assert.equal(parsePluginUIHostRequest({ ...apply, items: undefined }), undefined);
  assert.equal(parsePluginUIHostRequest({ ...apply, choices: undefined }), undefined);
  assert.equal(parsePluginUIHostRequest({ ...apply, choices: [] }), undefined);
  assert.equal(parsePluginUIHostRequest({ ...apply, choices: [{ key: apply.choices[0].key, action: "delete" }] }), undefined);
  assert.equal(parsePluginUIHostRequest({ ...apply, choices: [{ key: "", action: "add" }] }), undefined);
  assert.equal(parsePluginUIHostRequest({ ...apply, choices: [{ key: 7, action: "add" }] }), undefined);
  assert.equal(parsePluginUIHostRequest({ ...apply, choices: [apply.choices[0], apply.choices[0]] }), undefined);
  const huge = { ...preview, items: [{ ...profile, name: "x".repeat(PLUGIN_UI_MAX_MESSAGE_BYTES) }] };
  assert.equal(parsePluginUIHostRequest(huge), undefined);
});

test("review rows show what is written, never secret values", () => {
  const items = [profile, mcp, remote];
  const entries = [
    entry(profile, "new", { targetName: "Kimi (2)", notes: ["possible-duplicate"] }),
    entry(mcp, "update", { secretChanged: true }),
    entry(remote, "unchanged"),
  ];
  const choices = [
    { key: entries[0].key, action: "add" },
    { key: entries[1].key, action: "overwrite" },
    { key: entries[2].key, action: "overwrite" },
  ];
  const rows = aiConfigReviewRows(items, entries, choices);
  assert.deepEqual(rows.map((row) => [row.kind, row.action, row.targetName]), [
    ["profile", "add", "Kimi (2)"],
    ["mcp", "overwrite", "fetch"],
  ], "an unchanged item cannot be overwritten, so it is not listed");
  const [profileRow, mcpRow] = rows;
  assert.equal(profileRow.apiKey, "set");
  assert.equal(profileRow.anthropicAuth, "x-api-key", "a missing auth mode is written as x-api-key");
  assert.equal(profileRow.baseUrl, "https://api.example.test");
  assert.equal(profileRow.duplicateName, true);
  assert.equal(mcpRow.commandLine, "npx -y '@scope/fetch server' ''");
  assert.deepEqual(mcpRow.envKeys, ["API_TOKEN", "EMPTY"]);
  assert.deepEqual(mcpRow.headerKeys, []);
  const serialized = JSON.stringify(rows);
  for (const secret of ["sk-secret-value", "env-secret-value", "header-secret"]) assert.ok(!serialized.includes(secret), secret);

  const remoteRows = aiConfigReviewRows([remote], [entry(remote, "new")], [{ key: entries[2].key, action: "add" }]);
  assert.equal(remoteRows[0].url, "https://mcp.example.test/sse");
  assert.deepEqual(remoteRows[0].headerKeys, ["Authorization"]);
  assert.ok(!JSON.stringify(remoteRows).includes("header-secret"));

  const bearer = { ...profile, profile: { ...profile.profile, anthropicAuth: "bearer" } };
  assert.equal(aiConfigReviewRows([bearer], [entry(bearer, "new")], [{ key: entries[0].key, action: "add" }])[0].anthropicAuth, "bearer");
  const openai = { ...profile, profile: { ...profile.profile, apiFormat: "openai" } };
  assert.equal(aiConfigReviewRows([openai], [entry(openai, "new")], [{ key: entries[0].key, action: "add" }])[0].anthropicAuth, undefined);
  const keep = aiConfigReviewRows([profile], [entry(profile, "update")], [{ key: entries[0].key, action: "overwrite" }]);
  assert.equal(keep[0].apiKey, "keep");
  const rotated = aiConfigReviewRows([profile], [entry(profile, "update", { needsSecret: true })], [{ key: entries[0].key, action: "overwrite" }]);
  assert.equal(rotated[0].apiKey, "update");
  assert.deepEqual(aiConfigReviewRows([profile], [entry(profile, "new")], [{ key: entries[0].key, action: "overwrite" }]), []);
});

test("overwrite rows list the fields that change and whether secrets change", () => {
  const key = (item) => `${PLUGIN}/${item.kind}/${item.sourceId}`;
  const overwrite = (item, extra) =>
    aiConfigReviewRows([item], [entry(item, "update", extra)], [{ key: key(item), action: "overwrite" }])[0];

  const moved = overwrite(profile, { changedFields: ["baseUrl", "model"] });
  assert.deepEqual(moved.changedFields, ["baseUrl", "model"]);
  assert.equal(moved.secretsOnly, false);
  assert.equal(moved.apiKey, "keep");
  const rotated = overwrite(profile, { changedFields: [], secretChanged: true });
  assert.deepEqual(rotated.changedFields, []);
  assert.equal(rotated.apiKey, "update", "a rotated key is announced even when no other field changes");

  assert.equal(overwrite(mcp, { secretChanged: true }).secretValues, "update");
  assert.equal(overwrite(mcp, {}).secretValues, "keep");
  assert.equal(overwrite(remote, { secretChanged: true }).secretValues, "update");
  const plain = { ...mcp, mcp: { ...mcp.mcp, env: { EMPTY: "" } } };
  assert.equal(overwrite(plain, {}).secretValues, "none", "no values at the source, nothing to show");
  const added = aiConfigReviewRows([mcp], [entry(mcp, "new")], [{ key: key(mcp), action: "add" }])[0];
  assert.equal(added.secretValues, "set");
  assert.deepEqual(added.changedFields, [], "an add has no changed fields");

  for (const name of ["provider", "baseUrl", "model", "apiFormat", "anthropicAuth", "commonModels", "subagentModel",
    "reasoningEffort", "contextWindow", "transport", "command", "args", "url", "env", "headers"]) {
    assert.ok(AI_CONFIG_FIELD_NAMES.includes(name), name);
  }
});

test("mcp rows list prompts and whether each is filled", () => {
  const prompted = {
    ...remote,
    sourceId: "m3",
    mcp: {
      ...remote.mcp,
      env: { TOKEN: "" },
      headers: { Authorization: "" },
      prompts: [
        { target: "env", name: "TOKEN", label: "API \u202etoken", secret: true },
        { target: "header", name: "Authorization", label: "PAT", secret: false, valuePrefix: "Bearer " },
      ],
    },
  };
  const key = `${PLUGIN}/mcp/m3`;
  const local = {
    id: "remote",
    env: { TOKEN: "saved" },
    headers: { Authorization: "  " },
    syncSource: { pluginId: PLUGIN, kind: "mcp", sourceId: "m3", fingerprint: "x" },
  };
  const other = { id: "other", env: { TOKEN: "x" }, headers: { Authorization: "x" }, syncSource: { pluginId: "other.plugin", kind: "mcp", sourceId: "m3", fingerprint: "x" } };
  const [row] = aiConfigReviewRows([prompted], [entry(prompted, "update")], [{ key, action: "overwrite" }], [other, local]);
  assert.deepEqual(row.prompts, [
    { target: "env", name: "TOKEN", label: "API \\u{202e}token", secret: true, filled: true },
    { target: "header", name: "Authorization", label: "PAT", secret: false, filled: false },
  ]);
  const [added] = aiConfigReviewRows([prompted], [entry(prompted, "new")], [{ key, action: "add" }], [other]);
  assert.deepEqual(added.prompts.map((prompt) => prompt.filled), [false, false], "another plugin's object never counts");
  const [plain] = aiConfigReviewRows([mcp], [entry(mcp, "new")], [{ key: `${PLUGIN}/mcp/m1`, action: "add" }]);
  assert.deepEqual(plain.prompts, []);
});


test("a secret fill-in row says only the keys are filled", () => {
  const key = (item) => `${PLUGIN}/${item.kind}/${item.sourceId}`;
  const fill = (item, extra = {}) =>
    aiConfigReviewRows([item], [entry(item, "update", { needsSecret: true, ...extra })], [{ key: key(item), action: "overwrite" }])[0];
  assert.equal(fill(profile).secretsOnly, true);
  assert.equal(fill(mcp).secretsOnly, true);
  assert.equal(fill(profile, { changedFields: ["baseUrl"] }).secretsOnly, false, "a source change is written in full");
  assert.equal(fill(profile, { status: "conflict" }).secretsOnly, false);
  const add = aiConfigReviewRows([profile], [entry(profile, "new", { needsSecret: true })], [{ key: key(profile), action: "add" }])[0];
  assert.equal(add.secretsOnly, false);
});

test("the dialog shows changed fields and hides the written-field list for fill-ins", async () => {
  const dialog = await readSource("../src/components/plugins/PluginAiConfigConfirm.vue");
  assert.match(dialog, /v-if="row\.secretsOnly"[^>]*>\{\{ t\(`plugins\.aiConfig\.secretsOnly\.\$\{row\.kind\}`\) \}\}/);
  assert.match(dialog, /<dl v-else class="app-plugin-ai-config__spec">/, "fill-in rows list no base URL, model or command");
  assert.match(dialog, /row\.secretsOnly \? t\("plugins\.aiConfig\.action\.fill"\)/);
  assert.match(dialog, /v-if="row\.changedFields\.length"/);
  assert.match(dialog, /t\("plugins\.aiConfig\.changedFields"\)/);
  assert.match(dialog, /t\(`plugins\.aiConfig\.secretValues\.\$\{row\.secretValues\}`\)/);
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  for (const name of AI_CONFIG_FIELD_NAMES) {
    assert.ok(zh.includes(`"plugins.aiConfig.field.${name}"`), name);
    assert.ok(en.includes(`"plugins.aiConfig.field.${name}"`), name);
  }
  for (const name of ["secretsOnly.profile", "secretsOnly.mcp", "action.fill", "changedFields", "secretValuesLabel",
    "secretValues.set", "secretValues.update", "secretValues.keep"]) {
    assert.ok(zh.includes(`"plugins.aiConfig.${name}"`), name);
    assert.ok(en.includes(`"plugins.aiConfig.${name}"`), name);
  }
});

test("review text makes hidden characters visible", () => {
  assert.equal(visibleReviewText("rm\u202e -rf"), "rm\\u{202e} -rf");
  assert.equal(visibleReviewText("a\u200bb\u0007"), "a\\u{200b}b\\u{7}");
  assert.equal(visibleReviewText("line\nnext\tx"), "line\\u{a}next\\u{9}x");
  assert.equal(visibleReviewText("line\nnext\tx", { multiline: true }), "line\nnext\tx");
  const rows = aiConfigReviewRows(
    [{ ...mcp, mcp: { ...mcp.mcp, command: "safe\u202e" } }],
    [entry(mcp, "new")],
    [{ key: `${PLUGIN}/mcp/m1`, action: "add" }],
  );
  assert.ok(rows[0].commandLine.startsWith("'safe\\u{202e}' "), rows[0].commandLine);
});

test("frame gates ai-config on the capability", async () => {
  const frame = await readSource("../src/components/plugins/PluginViewFrame.vue");
  const gate = frame.match(/function aiConfigAllowed\([^)]*\)[^{]*\{([\s\S]*?)\n\}/);
  assert.ok(gate, "aiConfigAllowed exists");
  assert.match(gate[1], /capabilities\.includes\("ai\.config"\)/);
  assert.match(frame, /\.\.\.\(aiConfigAllowed\(session\) \? \{ aiConfig: 2 as const \} : \{\}\)/);
  assert.equal(frame.match(/aiConfig: \d/g)?.length, 1, "aiConfig is offered only in the gated spread");
  const blocked = frame.match(/const WARM_BLOCKED_HOST_REQUESTS = new Set<string>\(\[([\s\S]*?)\]\)/);
  assert.ok(blocked?.[1].includes('"mincode.plugin.ai-config.apply"'));
  const handler = frame.match(/async function handleAiConfigRequest\([\s\S]*?\n\}/)?.[0] ?? "";
  assert.match(handler, /if \(!aiConfigAllowed\(session\)\) \{\s*rejectRequest\(request\.id, "not-allowed"/);
});

test("apply always shows the host confirmation", async () => {
  const frame = await readSource("../src/components/plugins/PluginViewFrame.vue");
  const handler = frame.match(/async function handleAiConfigRequest\([\s\S]*?\n\}/)?.[0] ?? "";
  const confirm = handler.indexOf("await requestAiConfigConfirm(");
  const cancelled = handler.indexOf('rejectRequest(request.id, "cancelled"');
  const apply = handler.indexOf("applyExternalSync(");
  assert.ok(confirm > 0 && cancelled > confirm && apply > cancelled, "confirm, then reject on cancel, then apply");
  assert.match(handler.slice(confirm, apply), /if \(!confirmed\) \{\s*rejectRequest\(request\.id, "cancelled"/);
  assert.match(handler, /parseSyncItems\(request\.items\)/);
  assert.equal(frame.match(/applyExternalSync\(/g)?.length, 1, "no other path writes");
  // The dialog is host-owned and closes (as a cancel) when the session ends.
  assert.match(frame, /<PluginAiConfigConfirm\b/);
  assert.match(frame, /function dismissAiConfigConfirm\(\)/);
  const dialog = await readSource("../src/components/plugins/PluginAiConfigConfirm.vue");
  assert.match(dialog, /<ElDialog\b/);
  assert.ok(!/v-html/.test(dialog), "plugin text is never rendered as HTML");
  assert.match(dialog, /t\("plugins\.aiConfig\.title", \{ plugin: visibleReviewText\(props\.pluginName\) \}\)/);
  assert.match(dialog, /<dt>\{\{ t\("plugins\.aiConfig\.apiKeyLabel"\) \}\}<\/dt>/);
  assert.match(dialog, /row\.anthropicAuth/);
});

test("the write uses only the reviewed choices, one review per plugin", async () => {
  const frame = await readSource("../src/components/plugins/PluginViewFrame.vue");
  const handler = frame.match(/async function handleAiConfigRequest\([\s\S]*?\n\}/)?.[0] ?? "";
  const apply = handler.slice(handler.indexOf("// Every choice must name"));
  const claim = apply.indexOf("sync.claimExternalSyncReview(session.pluginId)");
  const flush = apply.indexOf("await sync.flushPendingSettingsEdits()");
  const preview = apply.indexOf("sync.previewExternalSync(session.pluginId, items)");
  const confirm = apply.indexOf("await requestAiConfigConfirm(");
  assert.ok(claim > 0 && flush > claim && preview > flush && confirm > preview, "claim, flush, preview, then confirm");
  assert.match(apply, /if \(!release\) \{\s*rejectRequest\(request\.id, "busy"/);
  assert.match(apply, /\} finally \{\s*release\(\);\s*\}/);
  assert.match(apply, /const reviewedChoices = rows\.map\(\(row\) => \(\{ key: row\.key, action: row\.action \}\)\)/);
  // Values typed into the dialog fill only the prompted keys; the reviewed entries stay the ones shown.
  assert.match(apply, /const \{ confirmed, values \} = await requestAiConfigConfirm\(pluginName, rows\)/);
  assert.match(apply, /const filled = withPromptValues\(items, session\.pluginId, values\)/);
  assert.match(apply, /sync\.applyExternalSync\(session\.pluginId, filled, reviewedChoices, reviewed\)/);
  assert.ok(!/safePost\([^\n]*values/.test(apply), "typed values are never posted to the page");
  assert.match(frame, /@confirm="settleAiConfigConfirm\(true, \$event\)"/);
  assert.match(apply, /reason: "invalid-choice"/);
  assert.ok(!/request\.choices\)/.test(apply.slice(confirm)), "page choices never reach the write");
});

async function loadTransport(features) {
  const themeImport = /^import \{ applyThemeTokens \} from '\.\/theme-tokens\.js';$/m;
  const themeTokens = (await readSource("../src/plugin-ui/theme-tokens.js")).replaceAll("export function", "function");
  const transport = await readSource("../src/plugin-ui/transport.js");
  const source = themeTokens + transport.replace(themeImport, "").replace("export function", "function");
  const listeners = new Map();
  const parent = { postMessage() {} };
  const context = vm.createContext({
    URLSearchParams, ArrayBuffer, Uint8Array,
    location: { hash: "#mincodeSession=s&nonce=n&generation=1" },
    parent, setTimeout, clearTimeout,
    addEventListener: (type, listener) => listeners.set(type, listener),
    removeEventListener: (type) => listeners.delete(type),
  });
  vm.runInContext(source, context);
  const api = context.createTransport({ onReady() {}, onError() {}, applyContext() {} });
  const sent = [];
  // Like a real MessagePort: the message is structured-cloned, so proxies (Vue reactive state) throw.
  const port = { start() {}, close() {}, postMessage(message) { sent.push(structuredClone(message)); } };
  listeners.get("message")({
    source: parent,
    data: { type: "mincode.host.connect", protocolVersion: 1, sessionId: "s", nonce: "n", generation: 1, requestTimeoutMs: 30000, features },
    ports: [port],
  });
  return { api, port, sent };
}

test("transport sends aiConfig items from reactive page state", async () => {
  const on = await loadTransport({ tasks: 1, aiConfig: 1 });
  const reactiveItems = new Proxy([new Proxy({ ...profile }, {})], {});
  const reactiveChoices = new Proxy([new Proxy({ key: "k", action: "add" }, {})], {});
  const preview = on.api.aiConfig.preview(reactiveItems);
  const apply = on.api.aiConfig.apply(reactiveItems, reactiveChoices);
  assert.equal(on.sent.length, 2);
  assert.deepEqual(on.sent[0].items, [profile]);
  assert.deepEqual(on.sent[1].choices, [{ key: "k", action: "add" }]);
  // Settle both requests so their timeouts do not keep the test process alive.
  for (const message of on.sent) on.port.onmessage({ data: { type: "mincode.host.response", id: message.id, ok: true, result: {} } });
  await Promise.all([preview, apply]);
});

test("transport exposes aiConfig", async () => {
  const off = await loadTransport({ tasks: 1 });
  assert.equal(off.api.aiConfig.supported(), false);
  await assert.rejects(off.api.aiConfig.preview([profile]), (error) => error.code === "not-allowed");
  await assert.rejects(off.api.aiConfig.apply([profile], [{ key: "k", action: "add" }]), (error) => error.code === "not-allowed");
  assert.equal(off.sent.length, 0);

  const on = await loadTransport({ tasks: 1, aiConfig: 1 });
  assert.equal(on.api.aiConfig.supported(), true);
  const preview = on.api.aiConfig.preview([profile]);
  assert.equal(on.sent[0].type, "mincode.plugin.ai-config.preview");
  assert.deepEqual(on.sent[0].items, [profile]);
  on.port.onmessage({ data: { type: "mincode.host.response", id: on.sent[0].id, ok: true, result: { entries: [] } } });
  assert.deepEqual(await preview, { entries: [] });
  const apply = on.api.aiConfig.apply([profile], [{ key: "k", action: "add" }]);
  assert.equal(on.sent[1].type, "mincode.plugin.ai-config.apply");
  assert.deepEqual(on.sent[1].choices, [{ key: "k", action: "add" }]);
  on.port.onmessage({ data: { type: "mincode.host.response", id: on.sent[1].id, ok: false, error: { code: "cancelled", message: "no" } } });
  await assert.rejects(apply, (error) => error.code === "cancelled");
  await assert.rejects(on.api.aiConfig.preview("nope"), (error) => error.code === "invalid-request");
  await assert.rejects(on.api.aiConfig.apply([profile], "nope"), (error) => error.code === "invalid-request");
  assert.equal(on.sent.length, 2);

  const types = await readSource("../src/plugin-ui/transport.d.ts");
  assert.match(types, /aiConfig: \{/);
  assert.match(types, /preview\(items: AiConfigItem\[\]\): Promise<\{ entries: AiConfigPreviewEntry\[\] \}>/);
  assert.match(types, /apply\(items: AiConfigItem\[\], choices: AiConfigChoice\[\]\): Promise<AiConfigApplyResult>/);
});

test("login request parses only kind mcp and a non-empty sourceId", () => {
  const login = { type: "mincode.plugin.ai-config.login", id: "r1", kind: "mcp", sourceId: "github" };
  assert.deepEqual(parsePluginUIHostRequest(login), login);
  // Unknown members are dropped; only the four known ones reach the frame.
  assert.deepEqual(parsePluginUIHostRequest({ ...login, token: "x", url: "https://evil.test" }), login);
  for (const kind of ["skill", "profile", "", undefined, 1]) {
    assert.equal(parsePluginUIHostRequest({ ...login, kind }), undefined, String(kind));
  }
  for (const sourceId of ["", "   ", undefined, 7, null, ["github"], "x".repeat(513)]) {
    assert.equal(parsePluginUIHostRequest({ ...login, sourceId }), undefined, String(sourceId));
  }
  assert.deepEqual(parsePluginUIHostRequest({ ...login, sourceId: "x".repeat(512) })?.sourceId, "x".repeat(512));
});

const loginServers = [
  { id: "github", transport: "http", url: "https://api.githubcopilot.com/mcp/", enabled: false, auth: "oauth",
    syncSource: { pluginId: "mincode.other", kind: "mcp", sourceId: "github", fingerprint: "f0" } },
  { id: "github-2", transport: "http", url: "https://api.githubcopilot.com/mcp/x", enabled: false,
    syncSource: { pluginId: PLUGIN, kind: "mcp", sourceId: "github", fingerprint: "f1" } },
  { id: "local", transport: "stdio", command: "npx", url: "", enabled: false,
    syncSource: { pluginId: PLUGIN, kind: "mcp", sourceId: "local", fingerprint: "f2" } },
  { id: "skill-like", transport: "http", url: "https://x.test/mcp", enabled: false,
    syncSource: { pluginId: PLUGIN, kind: "skill", sourceId: "s1", fingerprint: "f3" } },
  { id: "hand-made", transport: "http", url: "https://hand.test/mcp", enabled: true },
];

function fakeLogin(outcome, error) {
  const calls = { signIn: [], marked: [], approved: [] };
  return {
    calls,
    deps: {
      async approve(target) {
        calls.approved.push(target);
        return true;
      },
      async signIn(target, onError) {
        calls.signIn.push(target);
        if (outcome === "failed") onError(error);
        return outcome;
      },
      async markOauth(target) {
        calls.marked.push(target);
      },
    },
  };
}





test("hosts advertise aiConfig level 2", async () => {
  const protocol = await readSource("../src/domain/plugin-ui/protocol.ts");
  assert.match(protocol, /aiConfig\?: 1 \| 2/);
  const frame = await readSource("../src/components/plugins/PluginViewFrame.vue");
  assert.match(frame, /\.\.\.\(aiConfigAllowed\(session\) \? \{ aiConfig: 2 as const \} : \{\}\)/);

  const none = await loadTransport({ tasks: 1 });
  assert.equal(none.api.aiConfig.level(), 0);
  assert.equal(none.api.aiConfig.supported(), false);
  await assert.rejects(none.api.aiConfig.login("github"), (error) => error.code === "not-allowed");

  const old = await loadTransport({ tasks: 1, aiConfig: 1 });
  assert.equal(old.api.aiConfig.level(), 1);
  assert.equal(old.api.aiConfig.supported(), true);
  await assert.rejects(old.api.aiConfig.login("github"), (error) => error.code === "not-allowed");
  assert.equal(old.sent.length, 0);

  const bogus = await loadTransport({ tasks: 1, aiConfig: "2" });
  assert.equal(bogus.api.aiConfig.level(), 0);

  const current = await loadTransport({ tasks: 1, aiConfig: 2 });
  assert.equal(current.api.aiConfig.level(), 2);
  assert.equal(current.api.aiConfig.supported(), true);
  const login = current.api.aiConfig.login("github");
  assert.deepEqual({ ...current.sent[0], id: undefined }, { type: "mincode.plugin.ai-config.login", kind: "mcp", sourceId: "github", id: undefined });
  current.port.onmessage({ data: { type: "mincode.host.response", id: current.sent[0].id, ok: true, result: { status: "signed-in" } } });
  assert.deepEqual(await login, { status: "signed-in" });
  for (const sourceId of ["", "  ", 7, undefined]) {
    await assert.rejects(current.api.aiConfig.login(sourceId), (error) => error.code === "invalid-request");
  }
  assert.equal(current.sent.length, 1);

  const types = await readSource("../src/plugin-ui/transport.d.ts");
  assert.match(types, /level\(\): number;/);
  assert.match(types, /login\(sourceId: string\): Promise<AiConfigLoginResult>/);
  assert.match(types, /export interface AiConfigLoginResult \{\s*status: 'signed-in' \| 'cancelled' \| 'failed';\s*reason\?: string;\s*\}/);
});

test("auth-status request parses 1-200 unique non-empty sourceIds", () => {
  const request = { type: "mincode.plugin.ai-config.auth-status", id: "a1", sourceIds: ["github", "linear"] };
  assert.deepEqual(parsePluginUIHostRequest(request), request);
  // Unknown members are dropped.
  assert.deepEqual(parsePluginUIHostRequest({ ...request, token: "x", kind: "mcp" }), request);
  const max = Array.from({ length: PLUGIN_UI_MAX_AI_CONFIG_AUTH_SOURCES }, (_, index) => `s${index}`);
  assert.equal(PLUGIN_UI_MAX_AI_CONFIG_AUTH_SOURCES, 200);
  assert.deepEqual(parsePluginUIHostRequest({ ...request, sourceIds: max })?.sourceIds, max);
  assert.deepEqual(parsePluginUIHostRequest({ ...request, sourceIds: ["x".repeat(512)] })?.sourceIds, ["x".repeat(512)]);
  for (const sourceIds of [
    [], undefined, "github", [...max, "one-more"], ["github", "github"], ["github", ""], ["   "], ["x".repeat(513)], [7], [null], [["github"]],
  ]) {
    assert.equal(parsePluginUIHostRequest({ ...request, sourceIds }), undefined, JSON.stringify(sourceIds)?.slice(0, 40));
  }
});

function fakeStatuses(answers) {
  const asked = [];
  return {
    asked,
    async status(target) {
      asked.push(target);
      const answer = answers[target.id];
      if (answer instanceof Error) throw answer;
      return answer;
    },
  };
}





test("transport asks auth status at level 2", async () => {
  const none = await loadTransport({ tasks: 1 });
  await assert.rejects(none.api.aiConfig.authStatus(["github"]), (error) => error.code === "not-allowed");
  const old = await loadTransport({ tasks: 1, aiConfig: 1 });
  await assert.rejects(old.api.aiConfig.authStatus(["github"]), (error) => error.code === "not-allowed");
  assert.equal(old.sent.length, 0);

  const current = await loadTransport({ tasks: 1, aiConfig: 2 });
  const reactiveIds = new Proxy(["github", "linear"], {});
  const asked = current.api.aiConfig.authStatus(reactiveIds);
  assert.deepEqual({ ...current.sent[0], id: undefined }, { type: "mincode.plugin.ai-config.auth-status", sourceIds: ["github", "linear"], id: undefined });
  current.port.onmessage({ data: { type: "mincode.host.response", id: current.sent[0].id, ok: true, result: { statuses: { github: "signed-in", linear: "not-installed" } } } });
  assert.deepEqual(await asked, { statuses: { github: "signed-in", linear: "not-installed" } });
  for (const sourceIds of [[], "github", ["github", ""], ["  "], [7], undefined]) {
    await assert.rejects(current.api.aiConfig.authStatus(sourceIds), (error) => error.code === "invalid-request");
  }
  assert.equal(current.sent.length, 1);

  const types = await readSource("../src/plugin-ui/transport.d.ts");
  assert.match(types, /authStatus\(sourceIds: string\[\]\): Promise<\{ statuses: Record<string, AiConfigAuthStatus> \}>/);
  assert.match(types, /export type AiConfigAuthStatus = 'signed-in' \| 'signed-out' \| 'needs-login' \| 'not-installed';/);
});

async function sourceFiles(directory) {
  const files = [];
  for (const item of await readdir(directory, { withFileTypes: true })) {
    const path = new URL(item.name + (item.isDirectory() ? "/" : ""), directory);
    if (item.isDirectory()) files.push(...(await sourceFiles(path)));
    else if (/\.(ts|vue|js)$/.test(item.name)) files.push(path);
  }
  return files;
}

test("ai-config is never reachable from AI tools", async () => {
  const toolFiles = await sourceFiles(new URL("../src/services/ai-tools/", import.meta.url));
  assert.ok(toolFiles.length > 0);
  for (const file of toolFiles) {
    const source = await readFile(file, "utf8");
    for (const needle of ["ai-config", "ai-external-sync", "applyExternalSync", "previewExternalSync", "external-sync"]) {
      assert.ok(!source.includes(needle), `${fileURLToPath(file)} must not reference ${needle}`);
    }
  }
});

test("ai.config is part of the capability vocabulary and disclosed", async () => {
  const api = await readSource("../../plugins/api/api.go");
  assert.match(api, /CapabilityAIConfig Capability = "ai\.config"/);
  const schema = JSON.parse(await readSource("../../tools/plugin-devkit/manifest.schema.json"));
  assert.ok(schema.properties.capabilities.items.enum.includes("ai.config"));
  const tones = await readSource("../src/domain/plugins/capability-tone.ts");
  assert.match(tones, /"ai\.config": "orange"/);
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  assert.ok(zh.includes("可向 AI 设置提交模型配置、连接器和技能，写入前需你确认"));
  assert.ok(en.includes("Can propose model profiles, connectors and skills to AI settings; you confirm before anything is written"));
  const keyPattern = /"(plugins\.capabilityAiConfig|plugins\.aiConfig\.[A-Za-z.-]+)"/g;
  const zhKeys = [...zh.matchAll(keyPattern)].map((match) => match[1]).sort();
  const enKeys = [...en.matchAll(keyPattern)].map((match) => match[1]).sort();
  assert.ok(zhKeys.length > 5);
  assert.deepEqual(zhKeys, enKeys);
  const manager = await readSource("../src/views/PluginManagerWorkspaceView.vue");
  assert.match(manager, /capabilities\?\.includes\('ai\.config'\)[^\n]*plugins\.capabilityAiConfig/);
  assert.match(manager, /plugin\.capabilities\.includes\("ai\.config"\) \? t\("plugins\.capabilityAiConfig"\)/);
});
