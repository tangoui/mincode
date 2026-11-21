import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";
import { ACP_BUILTIN_AGENTS, parseAcpAdapterId, pluginAcpAdapterId } from "../src/domain/ai/acp-agents.ts";
import {
  acpAgentErrorCode,
  acpAgentPlatform,
  collectAcpAgents,
  isAcpAgentUnavailableError,
  isAcpAgentUntrustedError,
  validateAcpAgentEntry,
} from "../src/domain/plugins/acp-agents.ts";
import { createAcpAgentsApply } from "../src/services/contributions/acp-agents-state.ts";

const DEFAULT_BUILTIN = ACP_BUILTIN_AGENTS[0].id;

function entry(overrides = {}) {
  return {
    id: "helper",
    title: "Helper",
    command: "${pluginRoot}/bin/agent",
    args: ["--acp"],
    env: { TOKEN_FILE: "x" },
    platforms: ["darwin", "linux", "windows"],
    ...overrides,
  };
}

function contribution(entries, overrides = {}) {
  return {
    pluginId: "acme.tools",
    pluginName: "Acme Tools",
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

test("parseAcpAdapterId reads built-in, custom and plugin ids", () => {
  assert.deepEqual(parseAcpAdapterId("codex"), { kind: "builtin", id: "codex" });
  assert.deepEqual(parseAcpAdapterId("custom:agent-1"), { kind: "custom", id: "agent-1" });
  assert.deepEqual(parseAcpAdapterId("plugin:acme.tools/helper"), { kind: "plugin", pluginId: "acme.tools", agentId: "helper" });
  const id = pluginAcpAdapterId("acme.tools", "helper");
  assert.equal(id, "plugin:acme.tools/helper");
  assert.deepEqual(parseAcpAdapterId(id), { kind: "plugin", pluginId: "acme.tools", agentId: "helper" });
});

test("parseAcpAdapterId falls back to the default built-in agent for anything else", () => {
  for (const bad of [
    "",
    "  ",
    "unknown-agent",
    "custom:",
    "plugin:",
    "plugin:acme.tools",
    "plugin:acme.tools/",
    "plugin:Acme/helper",
    "plugin:acme.tools/Helper",
    "plugin:acme.tools/a/b",
    "plugin:../x/helper",
    undefined,
    null,
    42,
  ]) {
    assert.deepEqual(parseAcpAdapterId(bad), { kind: "builtin", id: DEFAULT_BUILTIN }, String(bad));
  }
});

test("validateAcpAgentEntry keeps display fields only and rejects a missing id or title", () => {
  const ok = validateAcpAgentEntry(entry({
    description: " Writes code ",
    titleLocalized: { "zh-CN": " 助手 " },
    descriptionLocalized: { zh: "写代码" },
  }));
  assert.equal(ok.ok, true);
  assert.deepEqual(ok.agent, {
    agentId: "helper",
    title: "Helper",
    titleLocalized: { "zh-CN": "助手" },
    description: "Writes code",
    descriptionLocalized: { zh: "写代码" },
    platforms: ["darwin", "linux", "windows"],
  });
  assert.equal("titleLocalized" in validateAcpAgentEntry(entry({ titleLocalized: {} })).agent, false);
  for (const field of ["command", "args", "env"]) assert.equal(field in ok.agent, false, field);
  assert.equal("platforms" in validateAcpAgentEntry(entry({ platforms: undefined })).agent, false);

  for (const [bad, message] of [
    [null, /object/],
    [entry({ id: undefined }), /id/],
    [entry({ id: "" }), /id/],
    [entry({ id: "Bad" }), /id/],
    [entry({ title: undefined }), /title/],
    [entry({ title: "  " }), /title/],
    [entry({ title: "a\u202eb" }), /title/],
    [entry({ title: "x".repeat(65) }), /title/],
    [entry({ description: "x".repeat(201) }), /description/],
    [entry({ titleLocalized: { "not a locale": "x" } }), /titleLocalized/],
    [entry({ titleLocalized: { zh: "" } }), /titleLocalized/],
    [entry({ titleLocalized: ["x"] }), /titleLocalized/],
    [entry({ descriptionLocalized: { zh: "x".repeat(201) } }), /descriptionLocalized/],
    [entry({ descriptionLocalized: Object.fromEntries(Array.from({ length: 17 }, (_, i) => [`x${String.fromCharCode(97 + i)}`, "x"])) }), /descriptionLocalized/],
    [entry({ platforms: [] }), /platforms/],
    [entry({ platforms: ["beos"] }), /platforms/],
    [entry({ platforms: ["linux", "linux"] }), /platforms/],
  ]) {
    const result = validateAcpAgentEntry(bad);
    assert.equal(result.ok, false);
    assert.match(result.error, message);
  }
});

test("collectAcpAgents lists active process plugins for this platform, ordered and deduplicated", () => {
  const agents = collectAcpAgents([
    contribution([entry({ id: "zeta" }), entry({ id: "alpha" }), entry({ id: "alpha", title: "Again" }), entry({ id: "Bad" })]),
    contribution([entry({ id: "mac-only", platforms: ["darwin"] }), entry({ id: "any", platforms: undefined })], { pluginId: "beta.agents", pluginName: "Beta" }),
    contribution([entry()], { pluginId: "declarative.only", runtime: "declarative" }),
    contribution([entry()], { pluginId: "other.workspace", activeInWorkspace: false }),
  ], "linux");
  assert.deepEqual(agents.map((agent) => `${agent.pluginId}/${agent.agentId}`), ["acme.tools/alpha", "acme.tools/zeta", "beta.agents/any"]);
  assert.equal(agents[0].title, "Helper", "the first entry with an id wins");
  assert.equal(agents[0].pluginName, "Acme Tools");
  assert.deepEqual(collectAcpAgents([contribution([entry({ id: "mac-only", platforms: ["darwin"] })])], "darwin").map((agent) => agent.agentId), ["mac-only"]);
});

test("acpAgentPlatform maps navigator platforms", () => {
  assert.equal(acpAgentPlatform("MacIntel"), "darwin");
  assert.equal(acpAgentPlatform("Win32"), "windows");
  assert.equal(acpAgentPlatform("Linux x86_64"), "linux");
});

function consumer(platform = "linux") {
  const target = { agents: { value: [] }, ready: { value: false } };
  const errors = [];
  let gate = Promise.resolve();
  const apply = createAcpAgentsApply(async () => {
    await gate;
    return (contributions) => collectAcpAgents(contributions, platform);
  }, (message) => errors.push(message), target);
  // What plugin-contributions does on each snapshot: dispose the last apply, then apply, in one tick.
  let dispose;
  const snapshot = (contributions) => {
    dispose?.();
    dispose = apply(contributions);
  };
  const ids = () => target.agents.value.map((agent) => `${agent.pluginId}/${agent.agentId}`);
  const hold = () => {
    let release;
    gate = new Promise((resolve) => {
      release = resolve;
    });
    return () => release();
  };
  return { target, errors, snapshot, stop: () => dispose?.(), ids, hold };
}

const both = [
  contribution([entry(), entry({ id: "mac-only", platforms: ["darwin"] })]),
  contribution([entry({ id: "other" })], { pluginId: "beta.agents", pluginName: "Beta" }),
];

test("the consumer drops a plugin's agents when it is disabled and never lists another platform's", async () => {
  const c = consumer("linux");
  c.snapshot(both);
  await flush();
  assert.deepEqual(c.ids(), ["acme.tools/helper", "beta.agents/other"]);

  // beta.agents disabled: the next snapshot no longer carries it.
  c.snapshot([both[0]]);
  await flush();
  assert.deepEqual(c.ids(), ["acme.tools/helper"]);

  // acme.tools disabled too: no acpAgents left in the snapshot.
  c.snapshot([]);
  assert.deepEqual(c.ids(), []);
  assert.equal(c.target.ready.value, true, "an empty snapshot is a known empty list");
  await flush();
  assert.deepEqual(c.ids(), []);
  assert.deepEqual(c.errors, []);
});

test("readiness: not ready before the first snapshot, and a refresh never shows a transient empty list", async () => {
  const c = consumer("linux");
  assert.equal(c.target.ready.value, false);
  assert.deepEqual(c.ids(), []);
  c.snapshot(both);
  assert.equal(c.target.ready.value, false, "still loading the first snapshot");
  await flush();
  assert.equal(c.target.ready.value, true);

  // A refresh keeps the previous list (and readiness) until the new snapshot lands.
  const release = c.hold();
  c.snapshot([both[1]]);
  assert.equal(c.target.ready.value, true);
  assert.deepEqual(c.ids(), ["acme.tools/helper", "beta.agents/other"]);
  await flush();
  assert.equal(c.target.ready.value, true);
  assert.deepEqual(c.ids(), ["acme.tools/helper", "beta.agents/other"], "the dispose of a refresh does not clear");
  release();
  await flush();
  assert.deepEqual(c.ids(), ["beta.agents/other"]);

  // Stopping the sync (dispose with no apply after it) clears and resets readiness.
  c.stop();
  await flush();
  assert.deepEqual(c.ids(), []);
  assert.equal(c.target.ready.value, false);
});

test("a superseded snapshot never applies and a load failure is reported", async () => {
  const c = consumer("linux");
  c.snapshot(both);
  c.snapshot([both[1]]);
  await flush();
  assert.deepEqual(c.ids(), ["beta.agents/other"]);
  const target = { agents: { value: [] }, ready: { value: false } };
  const errors = [];
  const apply = createAcpAgentsApply(async () => {
    throw new Error("offline");
  }, (message) => errors.push(message), target);
  apply(both);
  await flush();
  assert.deepEqual(errors, ["load consumer: offline"]);
  assert.equal(target.ready.value, false);
});

test("host error codes for plugin agents", () => {
  assert.equal(acpAgentErrorCode(new Error("untrusted: ACP agent \"helper\" has not been confirmed")), "untrusted");
  assert.equal(acpAgentErrorCode("unavailable: plugin ACP agents are not available"), "unavailable");
  assert.equal(acpAgentErrorCode(new Error("boom")), "");
  assert.equal(isAcpAgentUntrustedError(new Error("untrusted: changed and must be confirmed again")), true);
  assert.equal(isAcpAgentUntrustedError(new Error("unavailable: gone")), false);
  assert.equal(isAcpAgentUnavailableError(new Error("unavailable: plugin \"x\" no longer contributes ACP agent \"y\"")), true);
  assert.equal(isAcpAgentUnavailableError(undefined), false);
});

test("the consumer is registered lazily and the service names agents by reference only", async () => {
  const read = (path) => readFile(new URL(path, import.meta.url), "utf8");
  const index = await read("../src/services/contributions/index.ts");
  assert.match(index, /registerAcpAgentContributionConsumer/);
  const consumer = await read("../src/services/contributions/acp-agents.ts");
  assert.match(consumer, /createAcpAgentsApply\(async \(\) => \{\n\s+const \{ acpAgentPlatform, collectAcpAgents \} = await import\("@\/domain\/plugins\/acp-agents"\)/);
  const state = await read("../src/services/contributions/acp-agents-state.ts");
  assert.doesNotMatch(state, /^import (?!type )[^\n]*from "@\/domain\//m, "the state module imports domain types only");
  assert.match(consumer, /point: "acpAgents"/);
  assert.doesNotMatch(consumer, /^import (?!type )[^\n]*from "@\/domain\/plugins\/acp-agents"/m, "the domain module stays out of the entry chunk (type imports only)");
  const service = await read("../src/services/plugin-acp-agent-service.ts");
  assert.match(service, /"plugin_host_acp_agent_prepare"/);
  assert.match(service, /"plugin_host_acp_agent_trust"/);
  assert.match(service, /"acp_launch_plugin"/);
  assert.doesNotMatch(service, /"acp_launch"/);
  assert.doesNotMatch(service, /\bcommand\s*:/);
});

async function sourceFiles(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const url = new URL(`${entry.name}${entry.isDirectory() ? "/" : ""}`, directory);
    if (entry.isDirectory()) files.push(...(await sourceFiles(url)));
    else if (/\.(ts|vue)$/.test(entry.name)) files.push(url);
  }
  return files;
}

test("AI tools cannot start or confirm a plugin agent: only the user-driven start flow reaches the host calls", async () => {
  const src = new URL("../src/", import.meta.url);
  const commandUsers = new Set();
  const serviceUsers = new Set();
  for (const file of await sourceFiles(src)) {
    const relative = file.href.slice(src.href.length);
    const text = await readFile(file, "utf8");
    if (/["'`](acp_launch_plugin|plugin_host_acp_agent_(prepare|trust))["'`]/.test(text)) commandUsers.add(relative);
    if (/from "@\/services\/plugin-acp-agent-service"/.test(text) && /\b(trustAcpAgent|launchPluginAcpAgent)\b/.test(text)) {
      serviceUsers.add(relative);
    }
  }
  // The generated list only names the commands for the typed bridge.
  assert.deepEqual([...commandUsers].sort(), ["generated/backend-commands.ts", "services/plugin-acp-agent-service.ts"]);
  assert.deepEqual([...serviceUsers], ["composables/use-acp-agent-start.ts"]);
  const drawer = await readFile(new URL("components/ai/AiAssistantDrawer.vue", src), "utf8");
  // The drawer starts agents only from startAgent (the user's start button or send).
  assert.equal((drawer.match(/startConversationAgent\(/g) ?? []).length, 1);
});

test("the drawer loads or creates a session only after the agent start returned an adapter", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  const start = drawer.indexOf("async function startAgent(");
  const end = drawer.indexOf("\nasync function ", start + 1);
  assert.ok(start > 0 && end > start);
  const body = drawer.slice(start, end);
  // acp_load_session / acp_new_session exist only in startAgent ...
  assert.equal(drawer.split('"acp_load_session"').length - 1, 1);
  assert.equal(drawer.split('"acp_new_session"').length - 1, 1);
  assert.ok(body.includes('"acp_load_session"') && body.includes('"acp_new_session"'));
  // ... after the guard: an unavailable or mismatched agent returns undefined
  // (use-acp-agent-start.spec.ts) and nothing is loaded or created.
  const guard = body.indexOf("if (!adapterId) return;");
  assert.ok(guard > body.indexOf("await startConversationAgent("));
  assert.ok(guard < body.indexOf('"acp_load_session"'));
  assert.ok(guard < body.indexOf('"acp_new_session"'));
  // The command launch happens only through the start flow's callback.
  assert.equal(drawer.split('invokeBackend("acp_launch"').length - 1, 1);
  assert.ok(body.indexOf('invokeBackend("acp_launch"') > body.indexOf("launchCommandAgent: async"));
  // A conversation whose agent did not start keeps its session id but is not sent to.
  assert.match(drawer, /const target = await startAgent\(conversationId, false, request\);\n\s+const conversation = assistant\.conversationById\(conversationId\);\n\s+if \(!target \|\| !conversation\)/);
});
