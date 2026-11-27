import assert from "node:assert/strict";
import test from "node:test";
import * as uiTools from "../src/services/ai-tools/ui-tools.ts";

const selected = { kind: "database", id: "db-1" };
const tabs = [{ id: "tab-1", view: "database" }];
function fixture() {
  const actions = [];
  const signal = new AbortController().signal;
  const tools = uiTools.createUiCapabilities({
    listTabs: () => ({ activeView: "database", tabs }),
    getActive: async () => ({ view: "database", tab: tabs[0], resource: selected }),
    openView: async (view) => { actions.push(["view", view]); return { view, tabId: "tab-2" }; },
    openSettings: async () => { actions.push(["settings"]); },
    openResource: async (resource, passedSignal) => { actions.push(["resource", resource, passedSignal]); return { id: resource.id }; },
    closeTab: async (id) => { actions.push(["close", id]); },
    notify: (input) => { actions.push(["notify", input]); return "notification-1"; },
  });
  return { actions, signal, tools, run: (name, input = {}) => tools.find((item) => item.tool.name === `ui.${name}`).execute({ input, signal }) };
}

test("UI tools expose the planned names, risks, and required inputs", () => {
  const { tools } = fixture();
  assert.deepEqual(tools.map(({ tool, risk }) => [tool.name, risk, tool.inputSchema.required ?? []]), [
    ["ui.list_views", "read", []], ["ui.open_view", "write", ["view"]], ["ui.open_resource", "write", ["kind"]],
    ["ui.get_active", "read", []], ["ui.close_tab", "write", ["tabId"]], ["ui.notify", "write", ["title", "message", "level"]],
  ]);
});

test("unknown views, resource kinds, and missing resource identifiers fail without navigation", async () => {
  const { run, actions } = fixture();
  await assert.rejects(async () => run("open_view", { view: "missing" }), /missing/);
  await assert.rejects(async () => run("open_resource", { kind: "missing", id: "1" }), /missing/);
  await assert.rejects(async () => run("open_resource", { kind: "database" }), /id/);
  assert.deepEqual(actions, []);
});

test("ui.open_view opens settings through the dialog entry point", async () => {
  const { run, actions } = fixture();
  assert.deepEqual(await run("open_view", { view: "settings" }), { view: "settings" });
  assert.deepEqual(actions, [["settings"]]);
});

test("UI tools pass resource, tab, and notification arguments to their normal handlers", async () => {
  const { run, actions, signal } = fixture();
  assert.equal((await run("list_views")).activeView, "database");
  assert.ok((await run("list_views")).views.includes("notes"));
  assert.deepEqual((await run("get_active")).resource, selected);
  assert.deepEqual(await run("open_view", { view: "notes" }), { view: "notes", tabId: "tab-2" });
  for (const kind of ["database", "ssh", "sftp", "http_request", "note", "terminal"]) {
    assert.deepEqual(await run("open_resource", { kind, id: `${kind}-1` }), { id: `${kind}-1` });
  }
  assert.deepEqual(await run("close_tab", { tabId: "tab-1" }), { closed: true, tabId: "tab-1" });
  assert.deepEqual(await run("notify", { title: "Tests", message: "Finished", level: "success" }), { id: "notification-1" });
  assert.deepEqual(actions, [
    ["view", "notes"],
    ...["database", "ssh", "sftp", "http_request", "note", "terminal"].map((kind) => ["resource", { kind, id: `${kind}-1` }, signal]),
    ["close", "tab-1"],
    ["notify", { title: "Tests", message: "Finished", level: "success" }],
  ]);
});
