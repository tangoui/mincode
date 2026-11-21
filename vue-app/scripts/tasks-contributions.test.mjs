import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import {
  TASK_DETAIL_MAX_RUNES,
  TASK_DETECTED_MAX,
  TASK_GROUPS,
  TASK_MAX_ENTRIES,
  TASK_TITLE_MAX_RUNES,
  TASKS_CONTRIBUTION_POINT,
  groupPluginTasks,
  hasTaskContributions,
  isPluginTaskLaunchToken,
  parsePluginTaskKey,
  parsePluginTaskList,
  pluginTaskErrorCode,
  pluginTaskIdentity,
  pluginTaskPreviewSummary,
  pluginTaskTitle,
  restorePluginTaskRef,
} from "../src/domain/tasks/plugin-tasks.ts";
import { cloneTerminalPaneTree } from "../src/domain/terminal/types.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");

test("tasks domain mirrors the generated contribution contract", async () => {
  const contract = JSON.parse(await readSource("../src/domain/plugins/contribution-points.json")).points.tasks;
  assert.equal(TASKS_CONTRIBUTION_POINT, "tasks");
  assert.equal(contract.status, "enabled");
  assert.deepEqual(contract.kinds, ["process"]);
  assert.deepEqual(contract.requiresCapabilities, ["shell"]);
  assert.equal(TASK_MAX_ENTRIES, contract.maxEntries);
  assert.equal(TASK_TITLE_MAX_RUNES, contract.limits.titleMaxRunes);
  assert.equal(TASK_DETAIL_MAX_RUNES, contract.limits.detailMaxRunes);
  assert.equal(TASK_DETECTED_MAX, contract.limits.detectedTasksMax);
  assert.deepEqual([...TASK_GROUPS], contract.lists.groups);
  assert.deepEqual(contract.lists.variables, ["${projectRoot}", "${pluginRoot}"]);
});

test("task keys round-trip and reject malformed keys", () => {
  assert.deepEqual(parsePluginTaskKey("static:build"), { source: "static", id: "build" });
  assert.deepEqual(parsePluginTaskKey("detected:npm:script.test-unit"), {
    source: "detected",
    providerId: "npm",
    id: "script.test-unit",
  });
  for (const bad of ["", "build", "static:", "static:Build", "detected:npm", "detected::x", "detected:NPM:x", "other:x", 7]) {
    assert.equal(parsePluginTaskKey(bad), undefined, String(bad));
  }
  assert.equal(pluginTaskIdentity({ pluginId: "acme.tools", taskKey: "static:build" }), "acme.tools/static:build");
});

test("the task list parser drops malformed items, duplicates and unsafe text", () => {
  const list = parsePluginTaskList({
    projectRoot: "/work/app",
    tasks: [
      { pluginId: "acme.tools", pluginName: "Acme", key: "static:build", title: "Build\u202e", group: "build", command: "make", args: ["all"] },
      { pluginId: "acme.tools", key: "static:build", title: "Duplicate", command: "make" },
      { pluginId: "acme.tools", key: "detected:npm:test", title: "npm test", group: "weird", command: "npm", args: ["test"] },
      { pluginId: "bad id", key: "static:x", command: "x" },
      { pluginId: "acme.tools", key: "static:y", command: "a\nb" },
      { pluginId: "acme.tools", key: "static:z", command: "z", args: [1] },
    ],
    providers: [
      { pluginId: "acme.tools", providerId: "npm", title: "npm scripts", state: "ready", detectedAt: 1700000000000, dropped: 2 },
      { pluginId: "acme.tools", providerId: "npm", title: "dup" },
      { pluginId: "acme.tools", providerId: "Bad" },
    ],
  });
  assert.ok(list);
  assert.equal(list.projectRoot, "/work/app");
  assert.deepEqual(
    list.tasks.map((task) => [task.key, task.title, task.group, task.source]),
    [
      ["static:build", "Build", "build", "static"],
      ["detected:npm:test", "npm test", "other", "detected"],
    ],
  );
  assert.equal(list.tasks[1].providerId, "npm");
  assert.equal(list.tasks[1].pluginName, "acme.tools");
  assert.equal(list.providers.length, 1);
  assert.equal(list.providers[0].state, "ready");
  assert.equal(list.providers[0].dropped, 2);
  assert.equal(parsePluginTaskList(null), undefined);
  assert.equal(parsePluginTaskList({ tasks: [] }), undefined);
});

test("tasks are grouped in contract order with localized titles", () => {
  const list = parsePluginTaskList({
    projectRoot: "/p",
    tasks: [
      { pluginId: "a.b", key: "static:t2", title: "Zeta", group: "test", command: "x" },
      { pluginId: "a.b", key: "static:t1", title: "Alpha", group: "test", command: "x" },
      { pluginId: "a.b", key: "static:b", title: "Build", titleLocalized: { zh: "构建" }, group: "build", command: "x" },
    ],
  });
  const groups = groupPluginTasks(list.tasks, ["zh", "en"]);
  assert.deepEqual(groups.map((group) => group.group), ["build", "test"]);
  assert.deepEqual(groups[1].tasks.map((task) => task.title), ["Alpha", "Zeta"]);
  assert.equal(pluginTaskTitle(groups[0].tasks[0], ["zh", "en"]), "构建");
  assert.equal(pluginTaskTitle(groups[0].tasks[0], "en"), "Build");
});

test("host error codes are recognized only as prefixes", () => {
  assert.equal(pluginTaskErrorCode(new Error("not_found: task session \"x\" is not running")), "not_found");
  assert.equal(pluginTaskErrorCode("Error: changed: fingerprint differs"), "changed");
  assert.equal(pluginTaskErrorCode("unavailable: plugin disabled"), "unavailable");
  assert.equal(pluginTaskErrorCode("something else happened"), undefined);
  assert.equal(pluginTaskErrorCode("notforbidden:x"), undefined);
});

test("only active process plugins with entries make tasks available", () => {
  const base = { pluginName: "P", version: "1.0.0", source: "local" };
  assert.equal(hasTaskContributions([]), false);
  assert.equal(
    hasTaskContributions([{ ...base, pluginId: "a", runtime: "declarative", activeInWorkspace: true, point: { entries: [{}] } }]),
    false,
  );
  assert.equal(
    hasTaskContributions([{ ...base, pluginId: "a", runtime: "process", activeInWorkspace: false, point: { entries: [{}] } }]),
    false,
  );
  assert.equal(
    hasTaskContributions([{ ...base, pluginId: "a", runtime: "process", activeInWorkspace: true, point: { entries: [] } }]),
    false,
  );
  assert.equal(
    hasTaskContributions([{ ...base, pluginId: "a", runtime: "process", activeInWorkspace: true, point: { entries: [{}] } }]),
    true,
  );
});

test("restored task panes never carry a launch token, so they only re-attach", () => {
  const restored = restorePluginTaskRef({
    pluginId: "acme.tools",
    taskKey: "static:build",
    projectRoot: "/work/app",
    title: "Build",
    launchToken: "0123456789abcdef",
  });
  assert.deepEqual(restored, { pluginId: "acme.tools", taskKey: "static:build", projectRoot: "/work/app", title: "Build" });
  assert.equal(restorePluginTaskRef({ pluginId: "acme.tools", taskKey: "bad", projectRoot: "/p" }), undefined);
  assert.equal(restorePluginTaskRef({ pluginId: "acme.tools", taskKey: "static:x", projectRoot: "" }), undefined);
  assert.equal(restorePluginTaskRef({ pluginId: "acme.tools", taskKey: "static:x", projectRoot: "/p\n" }), undefined);
  assert.equal(restorePluginTaskRef({ pluginId: "acme.tools", taskKey: "static:x", projectRoot: "/p" }).title, "static:x");
  assert.equal(isPluginTaskLaunchToken("0123456789abcdef"), true);
  assert.equal(isPluginTaskLaunchToken("short"), false);

  let next = 0;
  const cloned = cloneTerminalPaneTree(
    {
      id: "terminal-1",
      type: "local",
      shellType: "system",
      task: { pluginId: "acme.tools", taskKey: "static:build", projectRoot: "/p", title: "Build", launchToken: "0123456789abcdef" },
    },
    "terminal-1",
    (prefix) => `${prefix}-clone-${++next}`,
  );
  assert.deepEqual(cloned.root.task, { pluginId: "acme.tools", taskKey: "static:build", projectRoot: "/p", title: "Build" });
});

test("the install preview lists static command lines and providers", () => {
  const preview = pluginTaskPreviewSummary({
    tasks: [
      { id: "build", kind: "static", title: "Build", command: "make", args: ["all"] },
      { id: "scripts", kind: "provider", title: "npm scripts", action: "detect-scripts" },
      { id: "Bad", kind: "static", title: "x", command: "x" },
      { id: "noop", kind: "static", title: "No command" },
    ],
  });
  assert.deepEqual(preview, {
    tasks: [{ title: "Build", command: "make", args: ["all"] }],
    providers: ["npm scripts"],
  });
  assert.deepEqual(pluginTaskPreviewSummary(undefined), { tasks: [], providers: [] });
});

test("task panes only start a process with a one-shot token or an explicit rerun", async () => {
  const terminal = await readSource("../src/components/terminal/LocalTerminal.vue");
  assert.match(terminal, /consumePluginTaskLaunchToken\(task\.launchToken\)/);
  assert.match(terminal, /attachOnly: !fresh/);
  const service = await readSource("../src/services/plugin-task-service.ts");
  assert.match(service, /const pendingLaunchTokens = new Set<string>\(\)/);
  assert.match(service, /confirmPluginLaunch\(launch, \{ changed: launch\.previouslyTrusted \}\)/);
  const store = await readSource("../src/stores/terminal-workspace.ts");
  assert.match(store, /task: restorePluginTaskRef\(value\.task\)/);
});
