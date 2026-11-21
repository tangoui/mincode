import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import {
  collectScmProviders,
  parseScmCommitMessageResponse,
  SCM_COMMIT_MESSAGE_LIMITS,
  shouldApplyGeneratedMessage,
} from "../src/domain/plugins/scm-commit-message.ts";

const contract = JSON.parse(await readFile(new URL("../../plugins/api/contribution-points.json", import.meta.url), "utf8"));

function contribution(pluginId, entries, overrides = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

test("scm: limits mirror the contract", () => {
  const point = contract.points.scmCommitMessage;
  for (const [name, value] of Object.entries(SCM_COMMIT_MESSAGE_LIMITS)) {
    if (name === "maxEntries") assert.equal(value, point.maxEntries);
    else assert.equal(value, point.limits[name], name);
  }
});

test("scm: collect providers", () => {
  const providers = collectScmProviders([
    contribution("zeta.plugin", [{ id: "conventional", title: "Ticket", titleLocalized: { zh: "工单格式" }, action: "commit.draft" }]),
    contribution("alpha.plugin", [
      { id: "short", title: "Short", action: "draft" },
      // Past maxEntries: ignored.
      { id: "second", title: "Second", action: "draft" },
    ]),
    contribution("idle.plugin", [{ id: "idle", title: "Idle", action: "draft" }], { activeInWorkspace: false }),
    contribution("decl.plugin", [{ id: "decl", title: "Decl", action: "draft" }], { runtime: "declarative" }),
    contribution("bad.plugin", [{ id: "Bad", title: "Bad", action: "draft" }]),
    contribution("unsafe.plugin", [{ id: "unsafe", title: "a\u202eb", action: "draft" }]),
    contribution("noaction.plugin", [{ id: "noaction", title: "No action" }]),
    contribution("Invalid Plugin", [{ id: "x", title: "X", action: "draft" }]),
  ]);
  assert.deepEqual(providers, [
    { pluginId: "alpha.plugin", pluginName: "alpha.plugin name", id: "short", title: "Short", activeInWorkspace: true },
    { pluginId: "idle.plugin", pluginName: "idle.plugin name", id: "idle", title: "Idle", activeInWorkspace: false },
    { pluginId: "zeta.plugin", pluginName: "zeta.plugin name", id: "conventional", title: "Ticket", titleLocalized: { zh: "工单格式" }, activeInWorkspace: true },
  ]);
  assert.deepEqual(collectScmProviders([]), []);
});

test("scm: response parsing", () => {
  assert.deepEqual(parseScmCommitMessageResponse({ message: "feat: x\n\nbody", truncated: false, diffTruncated: true, omittedFiles: 2 }), {
    message: "feat: x\n\nbody",
    truncated: false,
    diffTruncated: true,
    omittedFiles: 2,
  });
  // CRLF normalized, unsafe characters removed, surrounding blanks trimmed.
  assert.deepEqual(parseScmCommitMessageResponse({ message: "  fix: y\r\nbody\u202e\u200b \n" }), {
    message: "fix: y\nbody",
    truncated: false,
    diffTruncated: false,
    omittedFiles: 0,
  });
  const long = parseScmCommitMessageResponse({ message: "中".repeat(SCM_COMMIT_MESSAGE_LIMITS.messageMaxRunes + 1) });
  assert.equal([...long.message].length, SCM_COMMIT_MESSAGE_LIMITS.messageMaxRunes);
  assert.equal(long.truncated, true);
  assert.equal(parseScmCommitMessageResponse({ message: "x", omittedFiles: -1 }).omittedFiles, 0);
  for (const value of [undefined, null, [], "feat", { message: 3 }, { message: null }, { message: "   " }, { message: "\u200b" }, {}]) {
    assert.equal(parseScmCommitMessageResponse(value), undefined, JSON.stringify(value));
  }
});

test("edited draft is not overwritten", () => {
  assert.equal(shouldApplyGeneratedMessage("a", "a"), true);
  assert.equal(shouldApplyGeneratedMessage("a", "ab"), false);
  assert.equal(shouldApplyGeneratedMessage("", ""), true);
  assert.equal(shouldApplyGeneratedMessage("", "wip"), false);
});
