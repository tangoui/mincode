import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { browserCapabilities, browserCapabilityNames } from "../src/services/ai-tools/browser-tools.ts";

const browserResource = {
  id: "browser:active",
  name: "Browser",
  module: "browser",
  metadata: { tabId: "tab-1", url: "https://example.com" },
};

function capabilities(invoke = async () => null) {
  return browserCapabilities({ invoke });
}

function capability(name, invoke) {
  const found = capabilities(invoke).find((entry) => entry.tool.name === name);
  assert.ok(found, `missing capability ${name}`);
  return found;
}

test("browser.handle_dialog is a registered write tool", () => {
  assert.ok(browserCapabilityNames.includes("browser.handle_dialog"));
  const handle = capability("browser.handle_dialog");
  assert.equal(handle.mode, "write");
  assert.equal(handle.risk, "write");
  assert.equal(handle.target, "resource");
  assert.deepEqual(handle.tool.inputSchema.required, ["accept"]);
  assert.equal(handle.tool.inputSchema.properties.accept.type, "boolean");
  assert.equal(handle.tool.inputSchema.properties.promptText.type, "string");
});

test("every browser capability name is listed in browserCapabilityNames", () => {
  for (const entry of capabilities()) {
    if (entry.tool.name === "browser.open_tab") continue;
    assert.ok(browserCapabilityNames.includes(entry.tool.name), `browserCapabilityNames must list ${entry.tool.name}`);
  }
});

function dialogHandle(dialog, calls = []) {
  return capability("browser.handle_dialog", async (command, args) => {
    calls.push([command, args]);
    if (command === "embedded_browser_get_dialog") {
      if (dialog instanceof Error) throw dialog;
      return dialog;
    }
    return { dialogId: "d1", accepted: true };
  });
}

test("browser.handle_dialog previews the action on the pending confirm", async () => {
  const calls = [];
  const handle = dialogHandle({ id: "d1", tabId: "tab-1", kind: "confirm", message: "Delete your account?" }, calls);
  assert.equal(
    await handle.approvalPreview({ input: { accept: true }, resource: browserResource }),
    "Respond to page confirm \u201cDelete your account?\u201d \u2192 OK",
  );
  assert.equal(
    await handle.approvalPreview({ input: { accept: false }, resource: browserResource }),
    "Respond to page confirm \u201cDelete your account?\u201d \u2192 Cancel",
  );
  assert.deepEqual(calls[0], ["embedded_browser_get_dialog", { payload: { tabId: "tab-1" } }]);
});

test("browser.handle_dialog preview shortens long page text to one line", async () => {
  const message = "Are you sure you want to delete every project\nin this workspace? This cannot be undone at all, ever.";
  const handle = dialogHandle({ id: "d1", kind: "confirm", message });
  const flat = message.replace(/\s+/g, " ");
  assert.equal(
    await handle.approvalPreview({ input: { accept: true }, resource: browserResource }),
    `Respond to page confirm \u201c${flat.slice(0, 80)}\u2026\u201d \u2192 OK`,
  );
});

test("browser.handle_dialog preview names the prompt answer", async () => {
  const handle = dialogHandle({ id: "p1", kind: "prompt", message: "Your name?", defaultText: "guest" });
  assert.equal(
    await handle.approvalPreview({ input: { accept: true, promptText: "Ada" }, resource: browserResource }),
    "Respond to page prompt \u201cYour name?\u201d \u2192 OK with text \"Ada\"",
  );
  assert.equal(
    await handle.approvalPreview({ input: { accept: true }, resource: browserResource }),
    "Respond to page prompt \u201cYour name?\u201d \u2192 OK with text \"guest\"",
  );
  const long = "x".repeat(120);
  assert.equal(
    await handle.approvalPreview({ input: { accept: true, promptText: long }, resource: browserResource }),
    `Respond to page prompt \u201cYour name?\u201d \u2192 OK with text "${"x".repeat(80)}\u2026"`,
  );
  assert.equal(
    await handle.approvalPreview({ input: { accept: false, promptText: "Ada" }, resource: browserResource }),
    "Respond to page prompt \u201cYour name?\u201d \u2192 Cancel",
  );
});

test("browser.handle_dialog preview of an alert always reads OK", async () => {
  const handle = dialogHandle({ id: "a1", kind: "alert", message: "Saved" });
  assert.equal(
    await handle.approvalPreview({ input: { accept: false }, resource: browserResource }),
    "Respond to page alert \u201cSaved\u201d \u2192 OK",
  );
});

test("browser.handle_dialog preview falls back when no dialog is pending", async () => {
  assert.equal(
    await dialogHandle(null).approvalPreview({ input: { accept: false }, resource: browserResource }),
    "Respond to page dialog (none is open now)",
  );
  assert.equal(
    await dialogHandle(new Error("offline")).approvalPreview({ input: { accept: false }, resource: browserResource }),
    "Respond to page dialog",
  );
});

test("browser.handle_dialog executes through the AI-gated backend path", async () => {
  const calls = [];
  const handle = dialogHandle(null, calls);
  await handle.execute({ input: { accept: true, promptText: "Ada" }, resource: browserResource });
  assert.deepEqual(calls, [["embedded_browser_ai_execute", {
    payload: { toolName: "browser.handle_dialog", arguments: { accept: true, promptText: "Ada", tabId: "tab-1" } },
  }]]);
});

test("an approved handle_dialog call is bound to the dialog its preview showed", async () => {
  const calls = [];
  const handle = dialogHandle({ id: "d1", kind: "confirm", message: "Delete?" }, calls);
  const input = { accept: true, resourceId: "browser:active" };
  await handle.approvalPreview({ input, resource: browserResource });
  await handle.execute({ input, resource: browserResource });
  const executed = calls.find(([command]) => command === "embedded_browser_ai_execute");
  assert.equal(executed[1].payload.arguments.dialogId, "d1");
  // The approval parameters themselves are not rewritten.
  assert.equal("dialogId" in input, false);
});

test("an approval made while no dialog was open can never answer a later one", async () => {
  const calls = [];
  const handle = dialogHandle(null, calls);
  const input = { accept: true };
  await handle.approvalPreview({ input, resource: browserResource });
  await handle.execute({ input, resource: browserResource });
  const executed = calls.find(([command]) => command === "embedded_browser_ai_execute");
  const dialogId = executed[1].payload.arguments.dialogId;
  assert.equal(typeof dialogId, "string");
  assert.ok(dialogId.length > 0);
});

test("an AI-supplied dialogId is kept and previewed against the pending dialog", async () => {
  const calls = [];
  const handle = dialogHandle({ id: "d2", kind: "confirm", message: "Delete?" }, calls);
  const input = { accept: true, dialogId: "d1" };
  assert.equal(
    await handle.approvalPreview({ input, resource: browserResource }),
    "Respond to page dialog d1 (no longer open; the call will fail)",
  );
  await handle.execute({ input, resource: browserResource });
  const executed = calls.find(([command]) => command === "embedded_browser_ai_execute");
  assert.equal(executed[1].payload.arguments.dialogId, "d1");
});

test("browser.handle_dialog exposes the optional dialogId", () => {
  const handle = capability("browser.handle_dialog");
  assert.equal(handle.tool.inputSchema.properties.dialogId.type, "string");
  assert.deepEqual(handle.tool.inputSchema.required, ["accept"]);
});

test("browser.handle_dialog is exposed through the ACP MCP allowlist", async () => {
  const mcpTools = await readFile(new URL("../../backend/browser/mcp_tools.go", import.meta.url), "utf8");
  assert.match(mcpTools, /Name:\s*"browser\.handle_dialog"/);
  assert.doesNotMatch(mcpTools, /Name:\s*"browser\.evaluate"/);
  assert.doesNotMatch(mcpTools, /Name:\s*"browser\.computer_action"/);
});

test("browser.get_downloads is a registered read tool", () => {
  assert.ok(browserCapabilityNames.includes("browser.get_downloads"));
  const downloads = capability("browser.get_downloads");
  assert.equal(downloads.mode, "read");
  assert.equal(downloads.risk, "read");
  assert.equal(downloads.target, "resource");
});

test("browser.get_downloads executes through the AI-gated backend path", async () => {
  const calls = [];
  const downloads = capability("browser.get_downloads", async (command, args) => {
    calls.push([command, args]);
    return [{ id: "download-1", tabId: "tab-1", filename: "report.csv", state: "completed" }];
  });
  const result = await downloads.execute({ input: {}, resource: browserResource });
  assert.equal(result[0].filename, "report.csv");
  assert.deepEqual(calls, [["embedded_browser_ai_execute", {
    payload: { toolName: "browser.get_downloads", arguments: { tabId: "tab-1" } },
  }]]);
});

test("browser.get_downloads is exposed through the ACP MCP allowlist", async () => {
  const mcpTools = await readFile(new URL("../../backend/browser/mcp_tools.go", import.meta.url), "utf8");
  assert.match(mcpTools, /Name:\s*"browser\.get_downloads"/);
});

const semanticTools = {
  "browser.go_back": { mode: "write", risk: "network" },
  "browser.go_forward": { mode: "write", risk: "network" },
  "browser.select_option": { mode: "write", risk: "write" },
  "browser.set_checked": { mode: "write", risk: "write" },
  "browser.hover": { mode: "write", risk: "write" },
  "browser.scroll": { mode: "read", risk: "read" },
};

test("history, select, check, hover and scroll tools use the planned risk levels", () => {
  for (const [name, expected] of Object.entries(semanticTools)) {
    assert.ok(browserCapabilityNames.includes(name), `browserCapabilityNames must list ${name}`);
    const entry = capability(name);
    assert.equal(entry.target, "resource", name);
    assert.equal(entry.mode, expected.mode, `${name} mode`);
    assert.equal(entry.risk, expected.risk, `${name} risk`);
  }
});

test("history, select, check, hover and scroll tools are in the ACP MCP allowlist", async () => {
  const mcpTools = await readFile(new URL("../../backend/browser/mcp_tools.go", import.meta.url), "utf8");
  for (const name of Object.keys(semanticTools)) {
    assert.match(mcpTools, new RegExp(`Name:\\s*"${name.replace(".", "\\.")}"`), `${name} must be in mcp_tools.go`);
  }
});

test("semantic browser tools preview what they will do", async () => {
  const preview = (name, input) => capability(name).approvalPreview({ input, resource: browserResource });
  assert.equal(await preview("browser.go_back", {}), "Go back in the browser");
  assert.equal(await preview("browser.go_forward", {}), "Go forward in the browser");
  assert.equal(await preview("browser.select_option", { snapshotId: "s", ref: "e3", labels: ["Apple", "Pear"] }), "Select \"Apple, Pear\" in browser element e3");
  assert.equal(await preview("browser.select_option", { snapshotId: "s", ref: "e3", values: ["a"] }), "Select \"a\" in browser element e3");
  assert.equal(await preview("browser.set_checked", { snapshotId: "s", ref: "e4", checked: true }), "Check browser element e4");
  assert.equal(await preview("browser.set_checked", { snapshotId: "s", ref: "e4", checked: false }), "Uncheck browser element e4");
  assert.equal(await preview("browser.hover", { snapshotId: "s", ref: "e5" }), "Hover browser element e5");
});

test("semantic browser tool schemas", () => {
  const schema = (name) => capability(name).tool.inputSchema;
  assert.deepEqual(schema("browser.select_option").required, ["snapshotId", "ref"]);
  assert.equal(schema("browser.select_option").properties.values.type, "array");
  assert.equal(schema("browser.select_option").properties.labels.type, "array");
  assert.deepEqual(schema("browser.set_checked").required, ["snapshotId", "ref", "checked"]);
  assert.equal(schema("browser.set_checked").properties.checked.type, "boolean");
  assert.deepEqual(schema("browser.hover").required, ["snapshotId", "ref"]);
  assert.deepEqual(schema("browser.scroll").properties.direction.enum, ["up", "down", "left", "right"]);
  assert.ok(schema("browser.scroll").properties.containerRef);
  assert.equal(schema("browser.scroll").properties.amount.type, "number");
  assert.ok(!JSON.stringify(schema("browser.scroll")).includes("anyOf"), "browser.scroll schema must not use anyOf");
  assert.match(capability("browser.hover").tool.description, /browser\.get_page_snapshot/);
  assert.ok(!schema("browser.scroll").required?.length);
  assert.equal(schema("browser.go_back").properties.timeoutMs.type, "number");
});

test("browser.scroll executes through the AI-gated backend path", async () => {
  const calls = [];
  const scroll = capability("browser.scroll", async (command, args) => {
    calls.push([command, args]);
    return { scrollX: 0, scrollY: 800, atStart: false, atEnd: false };
  });
  await scroll.execute({ input: { direction: "down" }, resource: browserResource });
  assert.deepEqual(calls, [["embedded_browser_ai_execute", {
    payload: { toolName: "browser.scroll", arguments: { direction: "down", tabId: "tab-1" } },
  }]]);
});

function tabsDependency(calls = []) {
  const tabs = [
    { tabId: "tab-1", title: "Shop", url: "https://shop.test:8443/cart?token=abc", foreground: true, target: true },
    { tabId: "tab-3", title: "Login", url: "https://login.test/", foreground: false, target: false, openerTabId: "tab-1" },
  ];
  return {
    listSharedTabs() {
      calls.push(["list"]);
      return { tabs, unsharedCount: 1 };
    },
    async switchAiTarget(tabId) {
      calls.push(["switch", tabId]);
      if (!tabs.some((tab) => tab.tabId === tabId)) {
        throw new Error(JSON.stringify({ code: "browser_tab_not_shared", message: "not shared", details: { tabId } }));
      }
      return { ...tabs.find((tab) => tab.tabId === tabId), foreground: true, target: true };
    },
  };
}

function tabCapability(name, browserTabs, invoke = async () => { throw new Error("must not call the backend"); }) {
  const found = browserCapabilities({ invoke, browserTabs }).find((entry) => entry.tool.name === name);
  assert.ok(found, `missing capability ${name}`);
  return found;
}

test("browser.list_tabs and browser.switch_tab use the planned risk levels", () => {
  const list = tabCapability("browser.list_tabs", tabsDependency());
  assert.ok(browserCapabilityNames.includes("browser.list_tabs"));
  assert.equal(list.mode, "read");
  assert.equal(list.risk, "read");
  assert.equal(list.target, "none");
  const switchTab = tabCapability("browser.switch_tab", tabsDependency());
  assert.ok(browserCapabilityNames.includes("browser.switch_tab"));
  assert.equal(switchTab.mode, "write");
  assert.equal(switchTab.risk, "write");
  assert.equal(switchTab.target, "none");
  assert.deepEqual(switchTab.tool.inputSchema.required, ["tabId"]);
  assert.match(switchTab.tool.description, /browser\.list_tabs/);
});

test("browser.list_tabs reads the shared tabs locally without a backend call", async () => {
  const calls = [];
  const result = await tabCapability("browser.list_tabs", tabsDependency(calls)).execute({ input: {} });
  assert.deepEqual(calls, [["list"]]);
  assert.equal(result.unsharedCount, 1);
  assert.deepEqual(result.tabs.map((tab) => tab.tabId), ["tab-1", "tab-3"]);
});

test("browser.switch_tab previews the destination host and switches locally", async () => {
  const calls = [];
  const switchTab = tabCapability("browser.switch_tab", tabsDependency(calls));
  assert.equal(
    await switchTab.approvalPreview({ input: { tabId: "tab-3" } }),
    "Switch the AI browser tab to login.test",
  );
  assert.equal(
    await switchTab.approvalPreview({ input: { tabId: "tab-1" } }),
    "Switch the AI browser tab to shop.test:8443",
  );
  // An unshared tab's host is never looked up or shown.
  assert.equal(
    await switchTab.approvalPreview({ input: { tabId: "tab-2" } }),
    "Switch the AI browser tab to tab-2 (not shared; the call will fail)",
  );
  const result = await switchTab.execute({ input: { tabId: "tab-3" } });
  assert.equal(result.tabId, "tab-3");
  assert.equal(result.target, true);
  assert.deepEqual(calls.at(-1), ["switch", "tab-3"]);
  await assert.rejects(switchTab.execute({ input: { tabId: "tab-2" } }), /browser_tab_not_shared/);
});

test("tab tools fail clearly when the browser workspace is not available", async () => {
  await assert.rejects(tabCapability("browser.list_tabs", undefined).execute({ input: {} }), /browser workspace/i);
});

test("browser.list_tabs and browser.switch_tab are in the ACP MCP allowlist", async () => {
  const mcpTools = await readFile(new URL("../../backend/browser/mcp_tools.go", import.meta.url), "utf8");
  assert.match(mcpTools, /Name:\s*"browser\.list_tabs"/);
  assert.match(mcpTools, /Name:\s*"browser\.switch_tab"/);
});

test("browser.handle_dialog asks for the dialogId and points at browser.switch_tab", async () => {
  const description = capability("browser.handle_dialog").tool.description;
  for (const pattern of [/dialogId/, /dialog_pending/, /browser\.switch_tab/]) {
    assert.match(description, pattern);
  }
  assert.match(capability("browser.handle_dialog").tool.inputSchema.properties.dialogId.description, /dialog_pending/);
  const mcpTools = await readFile(new URL("../../backend/browser/mcp_tools.go", import.meta.url), "utf8");
  const goHandle = mcpTools.slice(mcpTools.indexOf('Name:        "browser.handle_dialog"'));
  assert.match(goHandle.slice(0, goHandle.indexOf("InputSchema")), /browser\.switch_tab/);
});

test("browser.extract_content is a registered read tool in the ACP MCP allowlist", async () => {
  assert.ok(browserCapabilityNames.includes("browser.extract_content"));
  const extract = capability("browser.extract_content");
  assert.equal(extract.mode, "read");
  assert.equal(extract.risk, "read");
  assert.equal(extract.target, "resource");
  const schema = extract.tool.inputSchema;
  assert.deepEqual(schema.properties.format.enum, ["markdown", "text"]);
  assert.equal(schema.properties.maxChars.minimum, 1000);
  assert.equal(schema.properties.maxChars.maximum, 100000);
  assert.ok(schema.properties.snapshotId && schema.properties.ref);
  assert.ok(!schema.required?.length);
  assert.match(extract.tool.description, /untrusted/);
  const mcpTools = await readFile(new URL("../../backend/browser/mcp_tools.go", import.meta.url), "utf8");
  assert.match(mcpTools, /Name:\s*"browser\.extract_content"/);
  const goDescription = mcpTools.match(/const extractContentDescription = "([^"]+)"/)?.[1];
  assert.equal(goDescription, extract.tool.description, "the Vue and MCP descriptions must match");
});

test("browser.extract_content sanitizes the page text it returns", async () => {
  const calls = [];
  const extract = capability("browser.extract_content", async (command, args) => {
    calls.push([command, args]);
    return { title: "Docs", url: "https://x.test/", content: "Use api_key=sk-live-123456789012345678901234 here", truncated: false };
  });
  const result = await extract.execute({ input: { format: "text", maxChars: 5000 }, resource: browserResource });
  assert.deepEqual(calls, [["embedded_browser_ai_execute", {
    payload: { toolName: "browser.extract_content", arguments: { format: "text", maxChars: 5000, tabId: "tab-1" } },
  }]]);
  assert.doesNotMatch(result.content, /sk-live-123456789012345678901234/);
  assert.equal(result.title, "Docs");
});
