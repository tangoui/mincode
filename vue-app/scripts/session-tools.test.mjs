import assert from "node:assert/strict";
import test from "node:test";

import {
  BUILTIN_SESSION_TOOLS,
  buildSessionToolNewCommand,
  buildSessionToolResumeCommand,
  builtinRootsPayload,
  createCustomSessionTool,
  createDefaultSessionToolPrefs,
  customToolBackendConfig,
  enabledSessionToolIds,
  envPrefix,
  moveSessionTool,
  normalizeSessionToolPrefs,
  processNameFromTemplate,
  quoteShellArgument,
  renderCommandTemplate,
  sessionToolClassSuffix,
  sessionToolEnv,
  slugifyCustomToolId,
} from "../src/domain/ai/session-tools.ts";
import { SESSION_PROVIDER_IDS } from "../src/domain/ai/session-deep-link.ts";

test("built-in tools cover every deep-link provider in the default order", () => {
  assert.deepEqual(BUILTIN_SESSION_TOOLS.map((tool) => tool.id), [...SESSION_PROVIDER_IDS]);
  assert.deepEqual(createDefaultSessionToolPrefs().order, [...SESSION_PROVIDER_IDS]);
});

test("default commands match the CLIs' resume and new-session syntax", () => {
  const prefs = createDefaultSessionToolPrefs();
  const expected = {
    claude: "claude --resume abc",
    pi: "pi --session abc",
    codex: "codex resume abc",
    gemini: "gemini --resume abc",
    opencode: "opencode --session abc",
    kimi: "kimi --session abc",
    qodercn: "qodercn --resume abc",
    qoder: "qoder --resume abc",
    grok: "grok --resume abc",
  };
  for (const [id, command] of Object.entries(expected)) {
    assert.equal(buildSessionToolResumeCommand(prefs, id, "abc"), command);
    assert.equal(buildSessionToolNewCommand(prefs, id), id);
  }
});

test("templates quote substituted values and fall back to defaults when blank", () => {
  assert.equal(quoteShellArgument("a'b"), "'a'\\''b'");
  assert.equal(renderCommandTemplate("x --resume {id} --cwd {cwd}", { id: "a b", cwd: "/tmp/p" }), "x --resume 'a b' --cwd /tmp/p");
  assert.equal(renderCommandTemplate("x {id}", {}), "x");
  assert.equal(renderCommandTemplate("x $(id) {id}", { id: "$(rm -rf ~)" }), "x $(id) '$(rm -rf ~)'");

  const prefs = createDefaultSessionToolPrefs();
  prefs.builtins.claude.resumeTemplate = "claude --dangerously-skip-permissions --resume {id}";
  prefs.builtins.claude.newTemplate = "claude --model opus";
  assert.equal(
    buildSessionToolResumeCommand(prefs, "claude", "s1"),
    "claude --dangerously-skip-permissions --resume s1",
  );
  assert.equal(buildSessionToolNewCommand(prefs, "claude"), "claude --model opus");
  assert.equal(buildSessionToolResumeCommand(prefs, "codex", "s1"), "codex resume s1");
});

test("custom tools resume with the new-session template when no resume template is set", () => {
  const prefs = createDefaultSessionToolPrefs();
  const tool = createCustomSessionTool("My Agent");
  tool.newTemplate = "my-agent";
  prefs.custom.push(tool);
  assert.equal(tool.id, "custom:my-agent");
  assert.equal(buildSessionToolResumeCommand(prefs, tool.id, "s1"), "my-agent");
  tool.resumeTemplate = "my-agent --continue {id}";
  assert.equal(buildSessionToolResumeCommand(prefs, tool.id, "s1"), "my-agent --continue s1");
  assert.equal(buildSessionToolNewCommand(prefs, "custom:missing"), "");
});

test("normalization drops unknown ids, dedupes order, and appends missing tools", () => {
  const prefs = normalizeSessionToolPrefs({
    order: ["codex", "custom:aider", "unknown", "codex", 7],
    builtins: { codex: { enabled: false, dir: " ~/codex ", resumeTemplate: 1 } },
    custom: [
      { id: "custom:aider", name: "Aider", mark: "AIDER", format: "json", fields: { sessionId: "id", extra: "x" } },
      { id: "custom:aider", name: "Aider Copy" },
      { id: "custom:bad id", name: "" },
      { id: "not-custom", name: "Other Tool", enabled: false },
    ],
  });
  assert.deepEqual(prefs.custom.map((tool) => tool.id), ["custom:aider", "custom:aider-copy", "custom:other-tool"]);
  assert.equal(prefs.custom[0].mark, "AID");
  assert.equal(prefs.custom[0].format, "json");
  assert.equal(prefs.custom[0].fields.sessionId, "id");
  assert.equal("extra" in prefs.custom[0].fields, false);
  assert.equal(prefs.builtins.codex.enabled, false);
  assert.equal(prefs.builtins.codex.dir, "~/codex");
  assert.equal(prefs.builtins.codex.resumeTemplate, "");
  assert.deepEqual(prefs.order.slice(0, 3), ["codex", "custom:aider", "claude"]);
  assert.equal(prefs.order.length, SESSION_PROVIDER_IDS.length + 3);
  assert.equal(new Set(prefs.order).size, prefs.order.length);
  assert.deepEqual(
    enabledSessionToolIds(prefs).filter((id) => !SESSION_PROVIDER_IDS.includes(id)),
    ["custom:aider", "custom:aider-copy"],
  );
  assert.equal(enabledSessionToolIds(prefs).includes("codex"), false);
  assert.deepEqual(normalizeSessionToolPrefs(null), createDefaultSessionToolPrefs());
});

test("ordering, slugs, and CSS suffixes stay stable", () => {
  assert.deepEqual(moveSessionTool(["a", "b", "c"], "c", -1), ["a", "c", "b"]);
  assert.deepEqual(moveSessionTool(["a", "b", "c"], "a", -1), ["a", "b", "c"]);
  assert.equal(slugifyCustomToolId("中文工具"), "custom:tool");
  assert.equal(slugifyCustomToolId("Aider", ["custom:aider", "custom:aider-2"]), "custom:aider-3");
  assert.equal(sessionToolClassSuffix("claude"), "claude");
  assert.equal(sessionToolClassSuffix("custom:aider"), "custom");
});

test("directory overrides become backend roots and CLI environment variables", () => {
  const prefs = createDefaultSessionToolPrefs();
  prefs.builtins.codex.dir = "~/work/codex";
  prefs.builtins.gemini.dir = "/data/gemini";
  assert.deepEqual(builtinRootsPayload(prefs), { codex: "~/work/codex", gemini: "/data/gemini" });

  const resolved = { codex: "/Users/me/work/codex", gemini: "/data/gemini" };
  assert.deepEqual(sessionToolEnv("codex", resolved), { CODEX_HOME: "/Users/me/work/codex" });
  assert.deepEqual(sessionToolEnv("gemini", resolved), {});
  assert.deepEqual(sessionToolEnv("claude", resolved), {});
  assert.deepEqual(sessionToolEnv("custom:x", resolved), {});
  assert.equal(envPrefix({ CODEX_HOME: "/a b" }), "CODEX_HOME='/a b' ");
  assert.equal(envPrefix({ "bad-name": "/x" }), "");
});

test("custom backend config derives a safe process name from the templates", () => {
  assert.equal(processNameFromTemplate("FOO=1 env /usr/local/bin/aider --yes"), "aider");
  assert.equal(processNameFromTemplate("'my tool' run"), "");
  assert.equal(processNameFromTemplate(""), "");

  const tool = createCustomSessionTool("Aider");
  tool.dir = "~/.aider/sessions";
  tool.resumeTemplate = "npx aider --resume {id}";
  assert.deepEqual(customToolBackendConfig(tool), {
    dir: "~/.aider/sessions",
    glob: "",
    format: "jsonl",
    fields: tool.fields,
    userRoles: "",
    assistantRoles: "",
    processName: "npx",
  });
  tool.resumeTemplate = "";
  assert.equal("processName" in customToolBackendConfig(tool), false);
});
