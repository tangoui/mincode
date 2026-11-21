import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import {
  fullTrustRisk,
  parseAuthorizeReply,
  parseFullTrustAudit,
  parseFullTrustList,
  parseFullTrustStatus,
  fullTrustDisplayText,
} from "../src/domain/plugins/full-trust.ts";

const root = new URL("../", import.meta.url);
const source = (path) => readFile(new URL(`src/${path}`, root), "utf8");

/** Body of `async function <name>(` up to the next top-level function. */
function functionBody(text, name) {
  const start = text.indexOf(`async function ${name}(`);
  assert.notEqual(start, -1, `${name} not found`);
  const next = text.indexOf("\nasync function ", start + 1);
  const nextSync = text.indexOf("\nfunction ", start + 1);
  const ends = [next, nextSync].filter((index) => index !== -1);
  return text.slice(start, ends.length ? Math.min(...ends) : undefined);
}

const subject = { pluginId: "trust.demo", name: "Demo", version: "1.0.0", capabilities: ["plugins"], fingerprint: "ab".repeat(32), eligible: true };

test("full-trust: risk normalization", () => {
  assert.equal(fullTrustRisk("write"), "write");
  assert.equal(fullTrustRisk("network"), "network");
  assert.equal(fullTrustRisk("read"), "dangerous");
  assert.equal(fullTrustRisk(undefined), "dangerous");
  assert.equal(fullTrustRisk("x"), "dangerous");
  assert.equal(fullTrustRisk("dangerous"), "dangerous");
});

test("full-trust: parsers", () => {
  const status = { pluginId: "trust.demo", trusted: true, grantedAt: 5, legacyDangerous: false, subject };
  assert.deepEqual(parseFullTrustStatus(status), status);
  assert.equal(parseFullTrustStatus({ ...status, subject: { ...subject, fingerprint: undefined } }), undefined);
  // The cheap get (no package hash) and ineligible plugins carry an empty fingerprint.
  const cheap = { ...status, trusted: false, grantedAt: undefined, subject: { ...subject, fingerprint: "", eligible: false, reason: "disabled" } };
  assert.equal(parseFullTrustStatus(cheap)?.subject.fingerprint, "");
  assert.equal(parseFullTrustStatus(cheap)?.subject.reason, "disabled");
  assert.equal(parseFullTrustStatus(null), undefined);
  assert.equal(parseFullTrustStatus({ ...status, lapsed: "package_changed", trusted: false }).lapsed, "package_changed");

  const good = { at: 1, lastAt: 2, count: 3, kind: "skipped", surface: "viewAction", risk: "write", subject: "run" };
  const audit = parseFullTrustAudit({ entries: [good, { ...good, kind: "other" }, { ...good, count: -1 }, null, { kind: "granted", at: 4, lastAt: 4, count: 1 }] });
  assert.deepEqual(audit, [good, { kind: "granted", at: 4, lastAt: 4, count: 1 }]);
  assert.equal(parseFullTrustAudit({ entries: Array.from({ length: 250 }, () => good) }).length, 200);
  assert.deepEqual(parseFullTrustAudit(undefined), []);

  assert.deepEqual(parseFullTrustList({ plugins: [{ pluginId: "a", name: "A", version: "1", grantedAt: 9 }, { pluginId: 3 }] }), [{ pluginId: "a", name: "A", version: "1", grantedAt: 9 }]);
  assert.deepEqual(parseFullTrustList(null), []);

  assert.deepEqual(parseAuthorizeReply(null), { allowed: false });
  assert.deepEqual(parseAuthorizeReply({ allowed: "yes" }), { allowed: false });
  assert.deepEqual(parseAuthorizeReply({ allowed: true }), { allowed: true });
  assert.deepEqual(parseAuthorizeReply({ allowed: false, lapsed: "upgraded" }), { allowed: false, lapsed: "upgraded" });
  // A lapse always means "ask": a reply claiming both is not trusted.
  assert.deepEqual(parseAuthorizeReply({ allowed: true, lapsed: "upgraded" }), { allowed: false, lapsed: "upgraded" });
});

test("full-trust: frame authorizes before confirming", async () => {
  const frame = await source("components/plugins/PluginViewFrame.vue");
  const body = functionBody(frame, "confirmAction");
  const authorize = body.indexOf('authorizeWithoutPrompt(pluginId, "viewAction", action.risk, action.id)');
  assert.notEqual(authorize, -1);
  assert.ok(authorize < body.indexOf("ElMessageBox.confirm"));
  assert.ok(body.indexOf("requiresPluginApproval(action.risk)") < authorize);
  assert.doesNotMatch(frame, /getPluginDangerousActionTrust/);
});

test("full-trust: host UI bridge never authorizes", async () => {
  // Never-skipped confirmations: host.terminal.open, host.project.open,
  // process launches, uriHandlers links, external opens, ai.config, grants and
  // plugin-requested confirms.
  for (const path of [
    "services/plugin-host-ui.ts",
    "services/plugin-launch-confirm.ts",
    "services/plugin-uri-links-runtime.ts",
    "services/plugin-associations-runtime.ts",
    "components/plugins/PluginHostUiLayer.vue",
    "components/plugins/PluginAiConfigConfirm.vue",
  ]) {
    const text = await source(path);
    assert.doesNotMatch(text, /plugin-full-trust/, path);
    assert.doesNotMatch(text, /authorizeWithoutPrompt/, path);
    assert.doesNotMatch(text, /plugin_full_trust/, path);
  }
  // Plugin-requested confirms inside the page frame stay prompts: only confirmAction authorizes.
  const frame = await source("components/plugins/PluginViewFrame.vue");
  assert.equal(frame.match(/authorizeWithoutPrompt\(/g)?.length, 1);
});

test("full-trust: the service only asks the host", async () => {
  const service = await source("services/plugin-full-trust.ts");
  assert.match(service, /if \(!isDesktopRuntime\(\)\) return false;/);
  assert.match(service, /invokeBackend<unknown>\("plugin_full_trust_authorize", \{ pluginId, surface, risk: fullTrustRisk\(risk\), subject \}\)/);
  const pluginService = await source("services/plugin-service.ts");
  assert.doesNotMatch(pluginService, /DangerousActionTrust|plugin_host_dangerous_action_trust/);
});

test("full-trust: stays out of the entry chain", async () => {
  for (const path of [
    "main.ts",
    "App.vue",
    "services/plugin-host-ui.ts",
    "services/plugin-service.ts",
    "services/plugin-commands.ts",
    "services/contributions/index.ts",
  ]) {
    const text = await source(path);
    assert.doesNotMatch(text, /from "@\/services\/plugin-full-trust"/, path);
    assert.doesNotMatch(text, /from "@\/domain\/plugins\/full-trust"/, path);
  }
});

/** Every .ts / .vue file under src/, relative to src/. */
async function sourceFiles(dir = "") {
  const { readdir } = await import("node:fs/promises");
  const out = [];
  for (const entry of await readdir(new URL(`src/${dir}`, root), { withFileTypes: true })) {
    const rel = dir ? `${dir}/${entry.name}` : entry.name;
    if (entry.isDirectory()) out.push(...await sourceFiles(rel));
    else if (/\.(ts|vue)$/.test(entry.name)) out.push(rel);
  }
  return out;
}

test("full-trust: no AI tool or plugin bridge reaches full trust", async () => {
  const files = (await sourceFiles("services/ai-tools")).concat("services/ai-native-tools.ts", "components/plugins/PluginViewFrame.vue");
  for (const path of files) {
    const text = await source(path);
    // The frame may only ask whether to skip a confirmation (authorizeWithoutPrompt).
    assert.doesNotMatch(text, /plugin_full_trust_(set|get|list|audit)/, path);
    assert.doesNotMatch(text, /\bsetFullTrust\b|\bgetFullTrust\b|\bclearFullTrustAudit\b/, path);
    if (!path.endsWith("PluginViewFrame.vue")) assert.doesNotMatch(text, /plugin-full-trust|plugin_full_trust/, path);
  }
  // The page bridge has no request type that could touch trust.
  const frame = await source("components/plugins/PluginViewFrame.vue");
  assert.doesNotMatch(frame, /"mincode\.plugin\.[\w.-]*trust/i);
});

test("full-trust: only the known confirmation surfaces may authorize", async () => {
  // Any new caller must be one of the design's skippable confirmations (spec §一);
  // add it here only together with the design change.
  const allowed = new Set([
    "services/plugin-full-trust.ts",
    "components/plugins/PluginViewFrame.vue",
    "components/projects/ProjectPluginTestsPanel.vue",
    "services/plugin-shell-commands.ts",
    "services/plugin-code-lens-commands.ts",
  ]);
  const callers = [];
  for (const path of await sourceFiles()) {
    if (path === "generated/backend-commands.ts") continue; // the command name catalogue, not a caller
    const text = await source(path);
    if (/authorizeWithoutPrompt|plugin_full_trust_authorize/.test(text)) callers.push(path);
  }
  for (const path of callers) assert.ok(allowed.has(path), `${path} may not authorize full trust`);
  assert.ok(callers.length >= allowed.size - 1, "allowlisted callers still authorize");
});

test("full-trust: manager loads the UI lazily", async () => {
  const manager = await source("views/PluginManagerWorkspaceView.vue");
  // The audit dialog is opened from inside the other two (see below).
  for (const name of ["PluginFullTrust", "PluginFullTrustSummary"]) {
    assert.match(manager, new RegExp(`const ${name} = defineAsyncComponent\\(\\(\\) => import\\("@/components/plugins/${name}\\.vue"\\)\\)`), name);
    assert.doesNotMatch(manager, new RegExp(`import ${name} from`), name);
  }
  assert.doesNotMatch(manager, /from "@\/services\/plugin-full-trust"/);
  assert.match(manager, /import\("@\/services\/plugin-full-trust"\)/);
  // Also on disabled plugins, so legacy trust can be revoked there (granting stays blocked: not eligible).
  assert.match(manager, /<PluginFullTrust v-if="plugin\.source === 'local' && plugin\.runtime !== 'declarative'"/);
  assert.match(manager, /<el-tag (?:disable-transitions )?v-if="plugin\.fullTrust" type="danger" effect="plain" size="small" :title="t\('plugins\.fullTrust\.badgeHelp'\)">\{\{ t\('plugins\.fullTrust\.badge'\) \}\}<\/el-tag>/);
  assert.match(manager, /<PluginFullTrustSummary v-if="plugins\.some\(\(p\) => p\.fullTrust\)"/);
  for (const name of ["PluginFullTrust", "PluginFullTrustSummary"]) {
    const text = await source(`components/plugins/${name}.vue`);
    assert.match(text, /const PluginFullTrustAudit = defineAsyncComponent\(\(\) => import\("@\/components\/plugins\/PluginFullTrustAudit\.vue"\)\)/, name);
  }
});

test("full-trust: plugin text is only interpolated", async () => {
  for (const name of ["PluginFullTrust", "PluginFullTrustAudit", "PluginFullTrustSummary"]) {
    const text = await source(`components/plugins/${name}.vue`);
    assert.doesNotMatch(text, /v-html|innerHTML|dangerouslyUseHTMLString/, name);
  }
});

test("full-trust: a regrant shows a later lapse notice again", async () => {
  const service = await source("services/plugin-full-trust.ts");
  assert.match(functionBody(service, "setFullTrust"), /lapseNoticeShown\.delete\(pluginId\)/);
  assert.match(service, /if \(trusted\) lapseNoticeShown\.delete\(raw\.pluginId\)/);
});

test("full-trust: plugin text loses format characters and is isolated", async () => {
  assert.equal(fullTrustDisplayText("Backup\u202E"), "Backup");
  assert.equal(fullTrustDisplayText("\u2066a\u200B\u2069 \n\n b\u0007c\uFEFF"), "a b c");
  assert.doesNotMatch(fullTrustDisplayText("x\u202Dy\u061Cz\u200Fw"), /\p{Cf}/u);
  const audit = await source("components/plugins/PluginFullTrustAudit.vue");
  assert.match(audit, /<bdi class="plugin-full-trust__value">\{\{ fullTrustDisplayText\(row\.subject \?\? ""\) \}\}<\/bdi>/);
  assert.match(audit, /<bdi class="plugin-full-trust__value">\{\{ fullTrustDisplayText\(row\.counterpart \?\? ""\) \}\}<\/bdi>/);
  const summary = await source("components/plugins/PluginFullTrustSummary.vue");
  assert.match(summary, /<bdi class="plugin-full-trust__value">\{\{ clean\(item\.name\) \}\}<\/bdi>/);
  assert.match(summary, /<bdi class="plugin-full-trust__value">\{\{ clean\(item\.version\) \}\}<\/bdi>/);
  const css = await source("styles/workspaces/plugins/plugin-full-trust.css");
  assert.match(css, /\.plugin-full-trust__value \{\s*unicode-bidi: isolate;/);
});
