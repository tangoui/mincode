import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";
import { settingsIntegrations } from "./settings-integrations-module.mjs";

// external-sync.ts imports settings-integrations, which cannot run under Node
// as-is; hand it the stubbed copy and point the dependency-free modules at their files.
globalThis.__externalSyncSettingsIntegrations = settingsIntegrations;
const moduleUrl = (path) => new URL(path, import.meta.url).href;
const source = (await readFile(new URL("../src/domain/ai/external-sync.ts", import.meta.url), "utf8"))
  .replace(/from "@\/domain\/ai\/anthropic-auth"/g, `from "${moduleUrl("../src/domain/ai/anthropic-auth.ts")}"`)
  .replace(/from "@\/domain\/ai\/model-selection"/g, `from "${moduleUrl("../src/domain/ai/model-selection.ts")}"`)
  .replace(
    /import \{([^}]*)\} from "@\/domain\/ai\/settings-integrations";/,
    "const {$1} = globalThis.__externalSyncSettingsIntegrations;",
  );
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText;
const { applySync, parseSyncItems, previewSync, sha256Hex, syncKey, withPromptValues } = await import(
  `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`
);

const PLUGIN = "mincode.cc-switch-sync";
const SKILL_TEXT = "---\nname: review\ndescription: Review code\n---\nRead the diff first.";

function profileItem(overrides = {}, fields = {}) {
  return {
    kind: "profile",
    sourceId: "claude:a1",
    name: "gc2",
    sourceApp: "claude",
    ...overrides,
    profile: {
      baseUrl: "https://api.example.com",
      apiKey: "k",
      model: "m1",
      apiFormat: "anthropic",
      anthropicAuth: "bearer",
      commonModels: ["m1", "m2"],
      subagentModel: "m2",
      ...fields,
    },
  };
}

function skillItem(fields = {}) {
  return {
    kind: "skill",
    sourceId: "s1",
    name: "review",
    sourceApp: "skills",
    skill: { content: SKILL_TEXT, description: "Review code", hasExtraFiles: false, ...fields },
  };
}

function mcpItem(fields = {}) {
  return {
    kind: "mcp",
    sourceId: "fs",
    name: "filesystem",
    sourceApp: "mcp",
    mcp: {
      transport: "stdio",
      command: "npx",
      args: ["-y", "@mcp/fs", "/tmp/my dir"],
      url: "",
      env: { FS_TOKEN: "env-secret-value" },
      headers: {},
      ...fields,
    },
  };
}

const emptyLists = () => ({ aiProfiles: [], mcpServers: [], aiSkills: [] });

function idFactory() {
  let next = 0;
  return (prefix) => `${prefix}-${++next}`;
}

function addAll(items, lists = emptyLists(), secrets = {}) {
  const choices = items.map((item) => ({ key: syncKey(PLUGIN, item), action: "add" }));
  return applySync(PLUGIN, items, choices, lists, secrets, idFactory());
}

test("new item gets a syncSource and default name", () => {
  const item = profileItem();
  const key = syncKey(PLUGIN, item);
  assert.equal(key, `${PLUGIN}/profile/claude:a1`);

  const [entry] = previewSync(PLUGIN, [item], emptyLists(), {});
  assert.equal(entry.key, key);
  assert.equal(entry.status, "new");
  assert.equal(entry.kind, "profile");
  assert.equal(entry.name, "gc2");
  assert.equal(entry.sourceApp, "claude");
  assert.equal(entry.targetName, "gc2");
  assert.equal(entry.needsSecret, false);
  assert.equal(entry.secretChanged, false);
  assert.deepEqual(entry.changedFields, []);

  const result = applySync(PLUGIN, [item], [{ key, action: "add" }], emptyLists(), {}, idFactory());
  assert.equal(result.added, 1);
  assert.equal(result.overwritten, 0);
  assert.deepEqual(result.failed, []);
  const [profile] = result.lists.aiProfiles;
  assert.equal(profile.syncSource.sourceId, "claude:a1");
  assert.equal(profile.syncSource.pluginId, PLUGIN);
  assert.equal(profile.syncSource.kind, "profile");
  assert.match(profile.syncSource.fingerprint, /^[0-9a-f]{16}$/);
  assert.equal(profile.id, "profile-1");
  assert.equal(profile.name, "gc2");
  assert.equal(profile.provider, "custom");
  assert.equal(profile.apiKey, "k");
  assert.equal(profile.anthropicAuth, "bearer");
  assert.equal(profile.reasoningEffort, "default");
  assert.deepEqual(profile.commonModels, ["m1", "m2"]);
  assert.equal("contextWindow" in profile, false);
  assert.deepEqual(Object.keys(profile).sort(), [
    "anthropicAuth", "apiFormat", "apiKey", "baseUrl", "commonModels", "id", "model", "name",
    "provider", "reasoningEffort", "subagentModel", "syncSource",
  ]);
  assert.ok(result.secrets[key]);
  assert.notEqual(result.secrets[key], "k");
  assert.ok(!JSON.stringify(result.lists).includes(result.secrets[key]));
});

test("unchanged after apply", () => {
  const items = [profileItem(), mcpItem(), skillItem()];
  const { lists, secrets } = addAll(items);
  const entries = previewSync(PLUGIN, items, lists, secrets);
  assert.deepEqual(entries.map((entry) => entry.status), ["unchanged", "unchanged", "unchanged"]);
  for (const entry of entries) {
    assert.deepEqual(entry.changedFields, []);
    assert.equal(entry.needsSecret, false);
    assert.equal(entry.secretChanged, false);
  }
  assert.equal(entries[0].targetName, "gc2");
  assert.equal(entries[1].targetName, "filesystem");
});

test("source change is an update", () => {
  const { lists, secrets } = addAll([profileItem()]);
  const [entry] = previewSync(PLUGIN, [profileItem({}, { baseUrl: "https://new.example.com" })], lists, secrets);
  assert.equal(entry.status, "update");
  assert.ok(entry.changedFields.includes("baseUrl"));
  assert.ok(!entry.changedFields.includes("model"));
  assert.equal(entry.secretChanged, false);
  assert.equal(entry.needsSecret, false);
});

test("secret change is reported without the value", () => {
  const oldKey = "sk-old-secret-111";
  const newKey = "sk-new-secret-222";
  const { lists, secrets } = addAll([profileItem({}, { apiKey: oldKey })]);
  const entries = previewSync(PLUGIN, [profileItem({}, { apiKey: newKey })], lists, secrets);
  assert.equal(entries[0].status, "update");
  assert.equal(entries[0].secretChanged, true);
  assert.equal(entries[0].needsSecret, false);
  assert.deepEqual(entries[0].changedFields, []);
  const text = JSON.stringify(entries);
  assert.ok(!text.includes(oldKey));
  assert.ok(!text.includes(newKey));
});

test("local edit plus source change is a conflict", () => {
  const { lists, secrets } = addAll([profileItem()]);
  lists.aiProfiles[0].model = "x";
  const [conflict] = previewSync(PLUGIN, [profileItem({}, { baseUrl: "https://new.example.com" })], lists, secrets);
  assert.equal(conflict.status, "conflict");
  assert.ok(conflict.changedFields.includes("baseUrl"));
  assert.ok(conflict.changedFields.includes("model"));
  const [unchanged] = previewSync(PLUGIN, [profileItem()], lists, secrets);
  assert.equal(unchanged.status, "unchanged");
});

test("a local rename is not a MinCode edit", () => {
  const { lists, secrets } = addAll([profileItem(), skillItem()]);
  lists.aiProfiles[0].name = "my gateway";
  lists.aiSkills[0].name = "my-review";
  const entries = previewSync(
    PLUGIN,
    [profileItem({}, { model: "m2" }), skillItem({ description: "Review carefully" })],
    lists,
    secrets,
  );
  assert.deepEqual(entries.map((entry) => entry.status), ["update", "update"]);
  assert.equal(entries[0].targetName, "my gateway");
  const result = applySync(
    PLUGIN,
    [profileItem({}, { model: "m2" }), skillItem({ description: "Review carefully" })],
    entries.map((entry) => ({ key: entry.key, action: "overwrite" })),
    lists,
    secrets,
    idFactory(),
  );
  assert.equal(result.lists.aiProfiles[0].name, "my gateway");
  assert.equal(result.lists.aiProfiles[0].model, "m2");
  assert.equal(result.lists.aiSkills[0].name, "my-review");
});

test("missing local secret is an update, not a conflict", () => {
  const { lists } = addAll([profileItem()]);
  lists.aiProfiles[0].apiKey = "";
  const [entry] = previewSync(PLUGIN, [profileItem()], lists, {});
  assert.equal(entry.status, "update");
  assert.equal(entry.needsSecret, true);

  lists.aiProfiles[0].model = "x";
  const [filled] = previewSync(PLUGIN, [profileItem()], lists, {});
  assert.equal(filled.status, "update", "an unchanged source only fills in the key, so a MinCode edit is no conflict");
  assert.equal(filled.needsSecret, true);
  assert.deepEqual(filled.changedFields, [], "nothing but the key is written");

  const result = applySync(PLUGIN, [profileItem()], [{ key: entry.key, action: "overwrite" }], lists, {}, idFactory());
  assert.equal(result.overwritten, 1);
  assert.equal(result.lists.aiProfiles[0].apiKey, "k");
  // Source unchanged: only the key is filled in, the MinCode edit survives.
  assert.equal(result.lists.aiProfiles[0].model, "x");
  assert.ok(result.secrets[entry.key]);
  assert.equal(previewSync(PLUGIN, [profileItem()], result.lists, result.secrets)[0].status, "unchanged");

  const changed = profileItem({}, { baseUrl: "https://new.example.com" });
  const full = applySync(PLUGIN, [changed], [{ key: entry.key, action: "overwrite" }], lists, {}, idFactory());
  assert.equal(full.lists.aiProfiles[0].apiKey, "k");
  assert.equal(full.lists.aiProfiles[0].baseUrl, "https://new.example.com");
  assert.equal(full.lists.aiProfiles[0].model, "m1");
});

test("a missing secret does not hide a conflict", () => {
  const { lists } = addAll([profileItem(), mcpItem()]);
  lists.aiProfiles[0].apiKey = "";
  lists.mcpServers[0].env = { FS_TOKEN: "" };
  const changed = [profileItem({}, { baseUrl: "https://new.example.com" }), mcpItem({ command: "bunx" })];

  // Source changed, MinCode untouched: an update that also needs the key.
  const updates = previewSync(PLUGIN, changed, lists, {});
  assert.deepEqual(updates.map((entry) => [entry.status, entry.needsSecret]), [["update", true], ["update", true]]);
  assert.deepEqual(updates[0].changedFields, ["baseUrl"]);
  assert.deepEqual(updates[1].changedFields, ["command"]);

  // Source changed and MinCode edited: a conflict, still flagged as needing the key.
  lists.aiProfiles[0].model = "x";
  lists.mcpServers[0].args = "--local";
  const conflicts = previewSync(PLUGIN, changed, lists, {});
  assert.deepEqual(conflicts.map((entry) => [entry.status, entry.needsSecret]), [["conflict", true], ["conflict", true]]);
  assert.ok(conflicts[0].changedFields.includes("model"));
  assert.ok(conflicts[1].changedFields.includes("args"));

  // Overwriting the conflict (an explicit choice) writes the source in full, keys included.
  const result = applySync(
    PLUGIN,
    changed,
    conflicts.map((entry) => ({ key: entry.key, action: "overwrite" })),
    lists,
    {},
    idFactory(),
  );
  assert.equal(result.overwritten, 2);
  assert.equal(result.lists.aiProfiles[0].apiKey, "k");
  assert.equal(result.lists.aiProfiles[0].model, "m1");
  assert.equal(result.lists.aiProfiles[0].baseUrl, "https://new.example.com");
  assert.equal(result.lists.mcpServers[0].command, "bunx");
  assert.deepEqual(result.lists.mcpServers[0].env, { FS_TOKEN: "env-secret-value" });
});

test("an MCP fill-in fills only empty values and keeps local keys and values", () => {
  const source = mcpItem({
    env: { FS_TOKEN: "env-secret-value", FS_OTHER: "other-secret", FS_MISSING: "missing-secret", FS_PLAIN: "" },
    headers: { Authorization: "Bearer header-secret" },
  });
  const { lists } = addAll([source]);
  // As after config sync from another device, plus local edits: a filled value, an added key.
  lists.mcpServers[0].env = { FS_TOKEN: "", FS_OTHER: "local-other", FS_PLAIN: "", LOCAL_ONLY: "mine" };
  lists.mcpServers[0].headers = { Authorization: "", "X-Local": "local-header" };
  const [entry] = previewSync(PLUGIN, [source], lists, {});
  assert.equal(entry.status, "update");
  assert.equal(entry.needsSecret, true);
  assert.deepEqual(entry.changedFields, []);
  const result = applySync(PLUGIN, [source], [{ key: entry.key, action: "overwrite" }], lists, {}, idFactory());
  assert.equal(result.overwritten, 1);
  assert.deepEqual(result.lists.mcpServers[0].env, {
    FS_TOKEN: "env-secret-value",
    FS_OTHER: "local-other",
    FS_PLAIN: "",
    LOCAL_ONLY: "mine",
    FS_MISSING: "missing-secret",
  });
  assert.deepEqual(result.lists.mcpServers[0].headers, { Authorization: "Bearer header-secret", "X-Local": "local-header" });
  const [after] = previewSync(PLUGIN, [source], result.lists, result.secrets);
  assert.equal(after.needsSecret, false);
  assert.equal(after.status, "unchanged");
});

test("missing MCP secret values are filled in without touching other fields", () => {
  const { lists } = addAll([mcpItem()]);
  lists.mcpServers[0].env = { FS_TOKEN: "" };
  lists.mcpServers[0].command = "bunx";
  const key = syncKey(PLUGIN, mcpItem());
  const result = applySync(PLUGIN, [mcpItem()], [{ key, action: "overwrite" }], lists, {}, idFactory());
  assert.equal(result.overwritten, 1);
  assert.deepEqual(result.lists.mcpServers[0].env, { FS_TOKEN: "env-secret-value" });
  assert.equal(result.lists.mcpServers[0].command, "bunx");
  assert.ok(result.secrets[key]);
});

test("an overwrite with an unchanged secret keeps the local secret values", () => {
  const { lists, secrets } = addAll([profileItem(), mcpItem()]);
  lists.aiProfiles[0].apiKey = "local-edited-key";
  lists.mcpServers[0].env = { FS_TOKEN: "local-edited-token" };
  const changed = [
    profileItem({}, { baseUrl: "https://new.example.com" }),
    mcpItem({ env: { FS_TOKEN: "env-secret-value", FS_ROOT: "" }, command: "bunx" }),
  ];
  const entries = previewSync(PLUGIN, changed, lists, secrets);
  assert.deepEqual(entries.map((entry) => entry.status), ["update", "update"]);
  const result = applySync(
    PLUGIN,
    changed,
    entries.map((entry) => ({ key: entry.key, action: "overwrite" })),
    lists,
    secrets,
    idFactory(),
  );
  assert.equal(result.lists.aiProfiles[0].apiKey, "local-edited-key");
  assert.equal(result.lists.aiProfiles[0].baseUrl, "https://new.example.com");
  assert.equal(result.lists.mcpServers[0].command, "bunx");
  assert.deepEqual(result.lists.mcpServers[0].env, { FS_TOKEN: "local-edited-token", FS_ROOT: "" });
});

test("overwrite keeps MinCode-only fields", () => {
  const first = addAll([profileItem(), skillItem()]);
  const lists = first.lists;
  lists.aiProfiles[0].maxOutputTokens = 1234;
  lists.aiProfiles[0].failoverModels = ["backup"];
  lists.aiSkills[0].enabled = false;
  const profileId = lists.aiProfiles[0].id;
  const skillId = lists.aiSkills[0].id;
  const changed = [
    profileItem({}, { baseUrl: "https://new.example.com", contextWindow: 200000 }),
    skillItem({ content: `${SKILL_TEXT}\nThen the tests.` }),
  ];
  const entries = previewSync(PLUGIN, changed, lists, first.secrets);
  assert.deepEqual(entries.map((entry) => entry.status), ["update", "update"]);
  const result = applySync(
    PLUGIN,
    changed,
    entries.map((entry) => ({ key: entry.key, action: "overwrite" })),
    lists,
    first.secrets,
    idFactory(),
  );
  assert.equal(result.overwritten, 2);
  assert.equal(result.added, 0);
  const [profile] = result.lists.aiProfiles;
  const [skill] = result.lists.aiSkills;
  assert.equal(profile.id, profileId);
  assert.equal(profile.maxOutputTokens, 1234);
  assert.deepEqual(profile.failoverModels, ["backup"]);
  assert.equal(profile.baseUrl, "https://new.example.com");
  assert.equal(profile.contextWindow, 200000);
  assert.equal(skill.id, skillId);
  assert.equal(skill.enabled, false);
  assert.match(skill.content, /Then the tests\.$/);
  assert.deepEqual(
    previewSync(PLUGIN, changed, result.lists, result.secrets).map((entry) => entry.status),
    ["unchanged", "unchanged"],
  );
});

test("manual profile with the same name is never overwritten", () => {
  const manual = { id: "manual", name: "gc2", baseUrl: "https://api.example.com", apiKey: "mine" };
  const lists = { ...emptyLists(), aiProfiles: [manual] };
  const before = structuredClone(lists);
  const [entry] = previewSync(PLUGIN, [profileItem()], lists, {});
  assert.equal(entry.status, "new");
  assert.equal(entry.targetName, "gc2 (2)");
  assert.ok(entry.notes.includes("possible-duplicate"));

  const result = addAll([profileItem()], lists);
  assert.deepEqual(lists, before);
  assert.deepEqual(result.lists.aiProfiles[0], manual);
  assert.equal(result.lists.aiProfiles[1].name, "gc2 (2)");

  const taken = { ...emptyLists(), aiProfiles: [manual, { id: "other", name: "GC2 (2)" }] };
  assert.equal(addAll([profileItem()], taken).lists.aiProfiles[2].name, "gc2 (3)");
});

test("same names from different apps get an app prefix", () => {
  const claude = profileItem({ sourceId: "claude:p", name: "gpt6", sourceApp: "claude" });
  const codex = profileItem(
    { sourceId: "codex:p", name: "gpt6", sourceApp: "codex" },
    { apiFormat: "openai-responses", anthropicAuth: undefined, reasoningEffort: "high" },
  );
  delete codex.profile.anthropicAuth;
  const solo = profileItem({ sourceId: "claude:q", name: "solo" });
  const { lists } = addAll([claude, codex, solo]);
  assert.deepEqual(lists.aiProfiles.map((profile) => profile.name), ["Claude Code · gpt6", "Codex · gpt6", "solo"]);
  assert.equal(lists.aiProfiles[1].anthropicAuth, "x-api-key");
  assert.equal(lists.aiProfiles[1].reasoningEffort, "high");
});

test("mcp servers are added disabled with quoted args and secret-free fingerprints", () => {
  const item = mcpItem();
  const key = syncKey(PLUGIN, item);
  const { lists, secrets } = addAll([item]);
  const [server] = lists.mcpServers;
  assert.deepEqual(server, {
    id: "filesystem",
    transport: "stdio",
    command: "npx",
    args: '-y @mcp/fs "/tmp/my dir"',
    url: "",
    env: { FS_TOKEN: "env-secret-value" },
    headers: {},
    enabled: false,
    syncSource: server.syncSource,
  });
  assert.ok(secrets[key]);
  assert.ok(!JSON.stringify(server.syncSource).includes("env-secret-value"));

  const [valueChanged] = previewSync(PLUGIN, [mcpItem({ env: { FS_TOKEN: "rotated-value" } })], lists, secrets);
  assert.equal(valueChanged.status, "update");
  assert.equal(valueChanged.secretChanged, true);
  assert.deepEqual(valueChanged.changedFields, []);
  assert.ok(!JSON.stringify(valueChanged).includes("rotated-value"));

  const [keyAdded] = previewSync(
    PLUGIN,
    [mcpItem({ env: { FS_TOKEN: "env-secret-value", FS_ROOT: "/tmp" } })],
    lists,
    secrets,
  );
  assert.equal(keyAdded.status, "update");
  assert.deepEqual(keyAdded.changedFields, ["env"]);

  const blanked = structuredClone(lists);
  blanked.mcpServers[0].env = { FS_TOKEN: "" };
  const [needsSecret] = previewSync(PLUGIN, [item], blanked, secrets);
  assert.equal(needsSecret.status, "update");
  assert.equal(needsSecret.needsSecret, true);

  const taken = { ...emptyLists(), mcpServers: [{ id: "filesystem", transport: "stdio", command: "x", args: "", url: "", enabled: true, env: {}, headers: {} }] };
  assert.equal(addAll([item], taken).lists.mcpServers[1].id, "filesystem (2)");
  assert.equal(addAll([mcpItem({ args: ["--root", "", "a b"] })]).lists.mcpServers[0].args, '--root  "a b"');
});

test("an mcp overwrite keeps the enabled flag the user set", () => {
  const item = mcpItem();
  const key = syncKey(PLUGIN, item);
  const { lists, secrets } = addAll([item]);
  lists.mcpServers[0].enabled = true;
  const changed = mcpItem({ command: "bunx" });
  const [entry] = previewSync(PLUGIN, [changed], lists, secrets);
  assert.equal(entry.status, "update");
  const result = applySync(PLUGIN, [changed], [{ key, action: "overwrite" }], lists, secrets, idFactory());
  assert.equal(result.overwritten, 1);
  assert.equal(result.lists.mcpServers[0].command, "bunx");
  assert.equal(result.lists.mcpServers[0].enabled, true);
});

test("skills are added enabled and note extra files", () => {
  const item = skillItem({ hasExtraFiles: true, docsUrl: "https://example.com/review" });
  const [entry] = previewSync(PLUGIN, [item], emptyLists(), {});
  assert.ok(entry.notes.includes("extra-files"));
  const { lists, secrets } = addAll([item]);
  const [skill] = lists.aiSkills;
  assert.equal(skill.id, "skill-1");
  assert.equal(skill.name, "review");
  assert.equal(skill.enabled, true);
  assert.equal(skill.content, SKILL_TEXT);
  assert.equal(skill.description, "Review code");
  assert.equal(skill.docsUrl, "https://example.com/review");
  assert.deepEqual(secrets, {});
});

test("invalid choices are reported", () => {
  const lists = emptyLists();
  const before = structuredClone(lists);
  const key = syncKey(PLUGIN, profileItem());
  const result = applySync(PLUGIN, [profileItem()], [{ key, action: "overwrite" }], lists, {}, idFactory());
  assert.equal(result.failed.length, 1);
  assert.equal(result.failed[0].key, key);
  assert.equal(result.failed[0].reason, "invalid-choice");
  assert.equal(result.added, 0);
  assert.equal(result.overwritten, 0);
  assert.deepEqual(result.lists, before);
  assert.deepEqual(lists, before);

  const applied = addAll([profileItem()]);
  const again = applySync(
    PLUGIN,
    [profileItem()],
    [{ key, action: "add" }, { key, action: "overwrite" }, { key: `${PLUGIN}/profile/missing`, action: "add" }],
    applied.lists,
    applied.secrets,
    idFactory(),
  );
  assert.deepEqual(again.failed.map((failure) => failure.reason), ["invalid-choice", "invalid-choice", "invalid-choice"]);
  assert.deepEqual(again.lists, applied.lists);
});

test("parseSyncItems rejects bad input", () => {
  const valid = [profileItem(), mcpItem(), skillItem({ docsUrl: "https://example.com" })];
  valid[0].profile.contextWindow = 200000;
  valid[0].profile.reasoningEffort = "high";
  assert.deepEqual(parseSyncItems(structuredClone(valid)), valid);
  assert.deepEqual(parseSyncItems([]), []);

  const missingSourceId = profileItem();
  delete missingSourceId.sourceId;
  const bad = [
    missingSourceId,
    { ...profileItem(), kind: "prompt" },
    mcpItem({ args: ["-y", 3] }),
    mcpItem({ transport: "sse", url: "https://mcp.example.com" }),
    profileItem({}, { apiFormat: "gemini" }),
    profileItem({}, { apiKey: "" }),
    profileItem({}, { anthropicAuth: "basic" }),
    mcpItem({ env: { FS_TOKEN: 1 } }),
    skillItem({ content: "   " }),
  ];
  for (const item of bad) assert.equal(parseSyncItems([item]), undefined, JSON.stringify(item));
  assert.equal(parseSyncItems(undefined), undefined);
  assert.equal(parseSyncItems({}), undefined);
  assert.equal(parseSyncItems([profileItem(), profileItem()]), undefined);
});

test("deleted source items leave settings untouched", () => {
  const first = profileItem();
  const second = profileItem({ sourceId: "claude:a2", name: "second" });
  const { lists, secrets } = addAll([first, second]);
  const before = structuredClone(lists);
  const entries = previewSync(PLUGIN, [first], lists, secrets);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].key, syncKey(PLUGIN, first));
  assert.deepEqual(lists, before);
  const result = applySync(PLUGIN, [first], [], lists, secrets, idFactory());
  assert.deepEqual(result.lists, before);
  assert.deepEqual(result.secrets, secrets);
});

test("sha256Hex matches node:crypto across block boundaries and UTF-8", () => {
  const samples = ["", "abc", "a".repeat(55), "a".repeat(56), "a".repeat(64), "a".repeat(1000), "密钥 · ключ · 🔑"];
  for (const text of samples) {
    assert.equal(sha256Hex(text), createHash("sha256").update(text, "utf8").digest("hex"), text);
  }
});

test("a cleared MCP secret is reported as a secret change", () => {
  const { lists, secrets } = addAll([mcpItem()]);
  const cleared = mcpItem({ env: { FS_TOKEN: "" } });
  const [entry] = previewSync(PLUGIN, [cleared], lists, secrets);
  assert.equal(entry.status, "update");
  assert.equal(entry.secretChanged, true);
  assert.equal(entry.needsSecret, false);
  const key = syncKey(PLUGIN, cleared);
  const result = applySync(PLUGIN, [cleared], [{ key, action: "overwrite" }], lists, secrets, idFactory());
  assert.equal(key in result.secrets, false);
  assert.deepEqual(result.lists.mcpServers[0].env, { FS_TOKEN: "" });
  assert.equal(previewSync(PLUGIN, [cleared], result.lists, result.secrets)[0].status, "unchanged");
});

test("a reviewed write refuses entries that changed since the confirmation", () => {
  const { lists, secrets } = addAll([profileItem()]);
  const changed = profileItem({}, { baseUrl: "https://new.example.com" });
  const [shown] = previewSync(PLUGIN, [changed], lists, secrets);
  assert.equal(shown.status, "update");
  // The user edits the profile after the dialog opened: update → conflict.
  lists.aiProfiles[0].model = "edited";
  const choice = { key: shown.key, action: "overwrite" };
  const refused = applySync(PLUGIN, [changed], [choice], lists, secrets, idFactory(), [shown]);
  assert.equal(refused.overwritten, 0);
  assert.deepEqual(refused.failed, [{ key: shown.key, reason: "changed-since-review" }]);
  assert.equal(refused.lists.aiProfiles[0].baseUrl, "https://api.example.com");
  assert.equal(refused.lists.aiProfiles[0].model, "edited");
  // Without a review the same choice is still a valid overwrite of the conflict.
  assert.equal(applySync(PLUGIN, [changed], [choice], lists, secrets, idFactory()).overwritten, 1);

  // An add that is no longer new is refused as changed, not as an invalid choice.
  const fresh = profileItem({ sourceId: "claude:b2", name: "other" });
  const [newEntry] = previewSync(PLUGIN, [fresh], lists, secrets);
  const added = addAll([fresh], lists, secrets);
  const late = applySync(PLUGIN, [fresh], [{ key: newEntry.key, action: "add" }], added.lists, added.secrets, idFactory(), [newEntry]);
  assert.deepEqual(late.failed, [{ key: newEntry.key, reason: "changed-since-review" }]);
  assert.equal(late.lists.aiProfiles.length, added.lists.aiProfiles.length);

  // The fields the dialog listed as changing are part of what was reviewed.
  const conflicted = profileItem({}, { baseUrl: "https://newer.example.com" });
  const [shownConflict] = previewSync(PLUGIN, [conflicted], lists, secrets);
  assert.equal(shownConflict.status, "conflict");
  lists.aiProfiles[0].subagentModel = "edited-too";
  const widened = applySync(PLUGIN, [conflicted], [{ key: shownConflict.key, action: "overwrite" }], lists, secrets, idFactory(), [shownConflict]);
  assert.deepEqual(widened.failed, [{ key: shownConflict.key, reason: "changed-since-review" }]);

  // A choice that was not part of the review is never written.
  const unreviewed = applySync(PLUGIN, [fresh], [{ key: newEntry.key, action: "add" }], lists, secrets, idFactory(), []);
  assert.deepEqual(unreviewed.failed, [{ key: newEntry.key, reason: "not-reviewed" }]);
  assert.equal(unreviewed.added, 0);
});

test("an add is written under the name the preview showed", () => {
  const first = profileItem({ sourceId: "claude:x1", name: "Kimi" });
  const second = profileItem({ sourceId: "claude:x2", name: "Kimi" });
  const entries = previewSync(PLUGIN, [first, second], emptyLists(), {});
  assert.deepEqual(entries.map((entry) => entry.targetName), ["Kimi", "Kimi (2)"]);
  const result = applySync(PLUGIN, [first, second], [{ key: entries[1].key, action: "add" }], emptyLists(), {}, idFactory(), [entries[1]]);
  assert.equal(result.added, 1);
  assert.equal(result.lists.aiProfiles[0].name, "Kimi (2)");
});

// Connector market (ai-market): required values the host asks for, and a sign-in marker.
function promptedItem(fields = {}) {
  return mcpItem({
    env: { FS_TOKEN: "", FS_PLAIN: "plain-value" },
    headers: { Authorization: "" },
    prompts: [
      { target: "env", name: "FS_TOKEN", label: "Token", secret: true },
      { target: "header", name: "Authorization", label: "Personal access token", secret: true, valuePrefix: "Bearer " },
    ],
    ...fields,
  });
}

test("prompts must name an empty env or header of the item", () => {
  const valid = promptedItem();
  assert.deepEqual(parseSyncItems([structuredClone(valid)]), [valid], "valid prompts are kept as given");
  const prompt = (extra) => promptedItem({ prompts: [{ target: "env", name: "FS_TOKEN", label: "Token", secret: true, ...extra }] });
  const bad = [
    prompt({ name: "NOPE" }),
    prompt({ name: "FS_PLAIN" }),
    prompt({ target: "args" }),
    prompt({ target: "header" }),
    prompt({ label: "x".repeat(201) }),
    prompt({ label: 3 }),
    prompt({ secret: "yes" }),
    prompt({ valuePrefix: "x".repeat(33) }),
    promptedItem({ prompts: "FS_TOKEN" }),
    promptedItem({ auth: "basic" }),
  ];
  for (const item of bad) assert.equal(parseSyncItems([item]), undefined, JSON.stringify(item.mcp.prompts ?? item.mcp.auth));
  assert.ok(parseSyncItems([prompt({ label: "x".repeat(200), valuePrefix: "x".repeat(32) })]));
  assert.deepEqual(parseSyncItems([mcpItem({ auth: "oauth" })])[0].mcp.auth, "oauth");
});

test("auth is written only when present and fingerprinted", () => {
  const signIn = mcpItem({ auth: "oauth" });
  const { lists: withAuth } = addAll([signIn]);
  assert.equal(withAuth.mcpServers[0].auth, "oauth");
  const { lists, secrets } = addAll([mcpItem()]);
  assert.equal("auth" in lists.mcpServers[0], false);

  // CC Switch objects synced before this field existed keep their fingerprint.
  assert.equal(lists.mcpServers[0].syncSource.fingerprint, "5ea18284f0a68732");
  const [kept] = previewSync(PLUGIN, [mcpItem()], lists, secrets);
  assert.equal(kept.status, "unchanged");

  const [added] = previewSync(PLUGIN, [signIn], lists, secrets);
  assert.equal(added.status, "update");
  assert.deepEqual(added.changedFields, ["auth"]);
  const result = applySync(PLUGIN, [signIn], [{ key: added.key, action: "overwrite" }], lists, secrets, idFactory());
  assert.equal(result.lists.mcpServers[0].auth, "oauth");
  assert.equal(previewSync(PLUGIN, [signIn], result.lists, result.secrets)[0].status, "unchanged");
});

test("a source that drops auth clears the local marker", () => {
  const signIn = mcpItem({ auth: "oauth" });
  const { lists, secrets } = addAll([signIn]);
  const [entry] = previewSync(PLUGIN, [mcpItem()], lists, secrets);
  assert.equal(entry.status, "update");
  assert.deepEqual(entry.changedFields, ["auth"]);
  const result = applySync(PLUGIN, [mcpItem()], [{ key: entry.key, action: "overwrite" }], lists, secrets, idFactory(), [entry]);
  assert.equal(result.overwritten, 1);
  assert.equal("auth" in result.lists.mcpServers[0], false);
  assert.equal(result.lists.mcpServers[0].syncSource.fingerprint, "5ea18284f0a68732");
  assert.equal(previewSync(PLUGIN, [mcpItem()], result.lists, result.secrets)[0].status, "unchanged");
});

test("prompted values already set locally are kept on overwrite", () => {
  const item = promptedItem({ headers: {}, prompts: [{ target: "env", name: "FS_TOKEN", label: "Token", secret: true }] });
  const key = syncKey(PLUGIN, item);
  const filled = withPromptValues([item], PLUGIN, { [key]: { "env:FS_TOKEN": "k1" } });
  const first = applySync(PLUGIN, filled, [{ key, action: "add" }], emptyLists(), {}, idFactory());
  assert.equal(first.lists.mcpServers[0].env.FS_TOKEN, "k1");

  const bumped = { ...item, mcp: { ...item.mcp, args: ["-y", "@mcp/fs@2.0.0", "/tmp/my dir"] } };
  const [entry] = previewSync(PLUGIN, [bumped], first.lists, first.secrets);
  assert.equal(entry.status, "update");
  assert.deepEqual(entry.changedFields, ["args"]);
  assert.equal(entry.needsSecret, false);
  assert.equal(entry.secretChanged, false);
  const result = applySync(PLUGIN, [bumped], [{ key, action: "overwrite" }], first.lists, first.secrets, idFactory(), [entry]);
  assert.deepEqual(result.failed, []);
  assert.equal(result.lists.mcpServers[0].env.FS_TOKEN, "k1");
  assert.equal(result.lists.mcpServers[0].env.FS_PLAIN, "plain-value");
});

test("a value filled in the confirmation passes the reviewed check", () => {
  const item = promptedItem();
  const key = syncKey(PLUGIN, item);
  const first = addAll([item]);
  assert.equal(first.lists.mcpServers[0].env.FS_TOKEN, "");
  const bumped = { ...item, mcp: { ...item.mcp, args: ["-y", "@mcp/fs@2.0.0", "/tmp/my dir"] } };
  const entries = previewSync(PLUGIN, [bumped], first.lists, first.secrets);
  assert.equal(entries[0].status, "update");
  const filled = withPromptValues([bumped], PLUGIN, { [key]: { "env:FS_TOKEN": "new-token", "header:Authorization": "pat" } });
  const result = applySync(PLUGIN, filled, [{ key, action: "overwrite" }], first.lists, first.secrets, idFactory(), entries);
  assert.deepEqual(result.failed, []);
  assert.equal(result.overwritten, 1);
  assert.equal(result.lists.mcpServers[0].env.FS_TOKEN, "new-token");
  assert.equal(result.lists.mcpServers[0].headers.Authorization, "Bearer pat");

  // A secret rotation elsewhere in the item still never clears a prompted value.
  const rotated = { ...bumped, mcp: { ...bumped.mcp, env: { FS_TOKEN: "", FS_PLAIN: "rotated" } } };
  const [rotation] = previewSync(PLUGIN, [rotated], result.lists, result.secrets);
  assert.equal(rotation.secretChanged, true);
  const after = applySync(PLUGIN, [rotated], [{ key, action: "overwrite" }], result.lists, result.secrets, idFactory(), [rotation]);
  assert.deepEqual(after.lists.mcpServers[0].env, { FS_TOKEN: "new-token", FS_PLAIN: "rotated" });
  assert.equal(after.lists.mcpServers[0].headers.Authorization, "Bearer pat");
});

test("a fill-in of missing secrets keeps prompted values and needs no prompt hash", () => {
  const item = promptedItem();
  const key = syncKey(PLUGIN, item);
  const first = applySync(
    PLUGIN,
    withPromptValues([item], PLUGIN, { [key]: { "env:FS_TOKEN": "t1" } }),
    [{ key, action: "add" }],
    emptyLists(),
    {},
    idFactory(),
  );
  // As after config sync from another device: the plain secret is missing, the prompted one is empty.
  first.lists.mcpServers[0].env = { FS_TOKEN: "", FS_PLAIN: "" };
  const [entry] = previewSync(PLUGIN, [item], first.lists, first.secrets);
  assert.equal(entry.status, "update");
  assert.equal(entry.needsSecret, true);
  const filled = withPromptValues([item], PLUGIN, { [key]: { "env:FS_TOKEN": "t2" } });
  assert.deepEqual(previewSync(PLUGIN, filled, first.lists, first.secrets), [entry], "filled prompts do not change the entry");
  const result = applySync(PLUGIN, filled, [{ key, action: "overwrite" }], first.lists, first.secrets, idFactory(), [entry]);
  assert.deepEqual(result.lists.mcpServers[0].env, { FS_TOKEN: "t2", FS_PLAIN: "plain-value" });
});

test("withPromptValues fills only prompted names and adds the prefix", () => {
  const item = promptedItem({
    prompts: [
      { target: "env", name: "FS_TOKEN", label: "Token", secret: false },
      { target: "header", name: "Authorization", label: "PAT", secret: true, valuePrefix: "Bearer " },
    ],
  });
  const key = syncKey(PLUGIN, item);
  const original = structuredClone(item);
  const [filled] = withPromptValues([item], PLUGIN, {
    [key]: { "env:FS_TOKEN": "visible", "env:FS_PLAIN": "ignored", "env:OTHER": "ignored", "header:Authorization": "abc" },
  });
  assert.deepEqual(filled.mcp.env, { FS_TOKEN: "visible", FS_PLAIN: "plain-value" });
  assert.deepEqual(filled.mcp.headers, { Authorization: "Bearer abc" });
  assert.deepEqual(item, original, "the given items are not changed");
  const [again] = withPromptValues([item], PLUGIN, { [key]: { "header:Authorization": "Bearer abc" } });
  assert.equal(again.mcp.headers.Authorization, "Bearer abc", "an existing prefix is not repeated");
  const [lower] = withPromptValues([item], PLUGIN, { [key]: { "header:Authorization": "bearer abc" } });
  assert.equal(lower.mcp.headers.Authorization, "bearer abc", "the prefix is recognised in any case");
  const [bare] = withPromptValues([item], PLUGIN, { [key]: { "header:Authorization": "Bearer" } });
  assert.equal(bare.mcp.headers.Authorization, "Bearer", "the bare prefix is not doubled");
  const [empty] = withPromptValues([item], PLUGIN, { [key]: { "env:FS_TOKEN": "  " } });
  assert.equal(empty.mcp.env.FS_TOKEN, "");
  const [other] = withPromptValues([item], "other.plugin", { [key]: { "env:FS_TOKEN": "x" } });
  assert.equal(other.mcp.env.FS_TOKEN, "", "values are looked up by this plugin's sync key");
  const [plain] = withPromptValues([mcpItem()], PLUGIN, { [syncKey(PLUGIN, mcpItem())]: { "env:FS_TOKEN": "x" } });
  assert.equal(plain.mcp.env.FS_TOKEN, "env-secret-value", "items without prompts are left alone");
});
