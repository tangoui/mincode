import assert from "node:assert/strict";
import test from "node:test";
import {
  describeAiAgentEventActivity,
  formatAiAgentEventDetail,
} from "../src/domain/ai/agent-event-activity.ts";

function tool(overrides = {}) {
  return {
    kind: "tool",
    title: "Tool call",
    detail: "",
    status: "completed",
    ...overrides,
  };
}

test("describes a completed file read with expandable result detail", () => {
  const activity = describeAiAgentEventActivity(tool({
    toolName: "workspace.read_file",
    toolInput: '{"path":"vue-app/src/App.vue"}',
    detail: "<template />",
  }));

  assert.deepEqual(activity, {
    kind: "readFile",
    target: "vue-app/src/App.vue",
    expandable: true,
  });
});

test("uses saved tool input to retain search and command context after output replaces detail", () => {
  assert.deepEqual(describeAiAgentEventActivity(tool({
    toolName: "workspace.search_code",
    toolInput: '{"query":"eventAnchor"}',
    detail: '{"matches":["..."]}',
  })), {
    kind: "searchCode",
    target: "eventAnchor",
    expandable: true,
  });

  assert.deepEqual(describeAiAgentEventActivity(tool({
    toolName: "workspace.run_command",
    toolInput: '{"command":"git status --short"}',
    detail: " M vue-app/src/App.vue",
  })), {
    kind: "gitOperation",
    target: "git status --short",
    expandable: true,
  });
});

test("maps the conversation terminal command runner to the run-command activity", () => {
  assert.deepEqual(describeAiAgentEventActivity(tool({
    toolName: "terminal.run_command",
    toolInput: '{"command":"pnpm --dir vue-app lint"}',
    detail: '{"stdout":"","exitCode":0}',
  })), {
    kind: "runCommand",
    target: "pnpm --dir vue-app lint",
    expandable: true,
  });
});

test("falls back to legacy localized titles when persisted events have no tool name", () => {
  assert.deepEqual(describeAiAgentEventActivity(tool({
    title: "代码搜索",
    detail: '{"query":"AiAgentEventGroup"}',
  })), {
    kind: "searchCode",
    target: "AiAgentEventGroup",
    expandable: true,
  });
});

test("makes failed simple reads expandable and formats JSON only for opened detail", () => {
  assert.equal(describeAiAgentEventActivity(tool({
    toolName: "workspace.read_file",
    toolInput: '{"path":"missing.ts"}',
    detail: "No such file",
    status: "failed",
  })).expandable, true);
  assert.equal(formatAiAgentEventDetail('{"paths":["a.ts"]}'), '{\n  "paths": [\n    "a.ts"\n  ]\n}');
  assert.equal(formatAiAgentEventDetail('\n  command output  \n'), 'command output');
});

test("classifies commands into tests, checks, builds and Git operations", () => {
  const cases = [
    ["pnpm exec node scripts/config-sync.test.mjs", "runTest"],
    ["pnpm run typecheck", "codeCheck"],
    ["git push origin main", "gitOperation"],
  ];
  for (const [command, kind] of cases) {
    assert.equal(describeAiAgentEventActivity(tool({
      toolName: "workspace.run_command",
      toolInput: JSON.stringify({ command }),
    })).kind, kind);
  }
});

test("classifies product tools by domain and keeps a useful target", () => {
  assert.deepEqual(describeAiAgentEventActivity(tool({
    toolName: "database.open_table",
    toolInput: '{"table":"users"}',
    detail: '{"ok":true}',
  })), {
    kind: "databaseAction",
    target: "users",
    expandable: true,
  });
  assert.equal(describeAiAgentEventActivity(tool({
    toolName: "browser.navigate",
    toolInput: '{"url":"https://example.com"}',
  })).kind, "browserAction");
  assert.equal(describeAiAgentEventActivity(tool({
    toolName: "settings.call_mcp_tool",
    toolInput: '{"toolName":"search"}',
  })).kind, "connectorCall");
});

test("maps non-tool events to auditable activity summaries", () => {
  assert.deepEqual(describeAiAgentEventActivity({
    kind: "thought",
    title: "思考过程",
    detail: "检查调用链",
  }), {
    kind: "analysis",
    expandable: true,
  });
  assert.equal(describeAiAgentEventActivity({
    kind: "plan",
    title: "执行计划",
    detail: '{"steps":[]}',
  }).kind, "plan");
});

test("redacts sensitive command arguments from the compact target", () => {
  assert.equal(describeAiAgentEventActivity(tool({
    toolName: "terminal.run_command",
    toolInput: '{"command":"deploy --token=secret-value --password hunter2"}',
  })).target, "deploy --token=•••••• --password ••••••");
});
