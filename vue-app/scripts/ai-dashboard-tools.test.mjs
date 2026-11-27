import assert from "node:assert/strict";
import test from "node:test";
import * as dashboardTools from "../src/services/ai-tools/dashboard-tools.ts";

const overview = {
  quickActions: [{ view: "terminal", title: "Terminal" }],
  connections: { ssh: [{ id: "ssh-1", name: "Build", favorite: true }], database: [] },
  recentSessions: [{ id: "codex:1", provider: "Codex", title: "Tests", lastAt: 50 }],
  counts: { ssh: 1, database: 0, connectedDatabases: 0, openTabs: 2 },
};
const widgets = [
  { id: "recent_sessions", title: "Recent sessions", kind: "data" },
  { id: "plugin-a/status", pluginId: "plugin-a", title: "Status", kind: "data" },
  { id: "plugin-b/view", pluginId: "plugin-b", title: "View", kind: "view" },
];
const data = { items: [{ label: "State", value: "Ready", severity: "ok" }] };

function fixture() {
  const calls = [];
  const signal = new AbortController().signal;
  const tools = dashboardTools.createDashboardCapabilities({
    overview: async () => overview,
    widgets: async () => widgets,
    widgetData: async (id, passedSignal) => {
      calls.push([id, passedSignal]);
      if (id === "plugin-a/status") return data;
      if (id === "recent_sessions") return overview.recentSessions;
      throw new Error(`Unknown widget: ${id}`);
    },
  });
  return { tools, calls, signal, run: (name, input = {}) => tools.find((item) => item.tool.name === `dashboard.${name}`).execute({ input, signal }) };
}

test("dashboard advertises its three read-only capabilities", () => {
  const { tools } = fixture();
  assert.deepEqual(tools.map(({ tool, risk }) => [tool.name, risk, tool.inputSchema.required ?? []]), [
    ["dashboard.get_overview", "read", []],
    ["dashboard.list_widgets", "read", []],
    ["dashboard.get_widget_data", "read", ["id"]],
  ]);
});

test("dashboard uses the overview and includes contributed widgets", async () => {
  const { run } = fixture();
  assert.deepEqual(await run("get_overview"), overview);
  assert.deepEqual(await run("list_widgets"), widgets);
});

test("dashboard forwards widget IDs and cancellation and surfaces missing widgets", async () => {
  const { run, calls, signal } = fixture();
  assert.deepEqual(await run("get_widget_data", { id: "plugin-a/status" }), data);
  assert.deepEqual(await run("get_widget_data", { id: "recent_sessions" }), overview.recentSessions);
  await assert.rejects(async () => run("get_widget_data", { id: "missing" }), /missing/);
  await assert.rejects(async () => run("get_widget_data", { id: " " }), /id/);
  assert.deepEqual(calls, [["plugin-a/status", signal], ["recent_sessions", signal], ["missing", signal]]);
});
