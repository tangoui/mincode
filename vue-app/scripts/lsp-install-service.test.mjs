import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";

test("only successful LSP installations notify runtime listeners and cleanup removes them", async () => {
  const calls = [];
  let result = { output: "installed" };
  const key = "__mincodeLspInstallBridge";
  globalThis[key] = {
    invokeBackend: async (command, args) => { calls.push({ command, args }); return result; },
    isDesktopRuntime: () => true,
    listenAppEvent: () => () => {},
  };
  const url = new URL("../src/services/lsp-service.ts?install-test", import.meta.url).href;
  const loader = registerHooks({
    resolve(specifier, context, nextResolve) {
      if (context.parentURL === url && specifier === "@/services/desktop-bridge") {
        const exports = Object.keys(globalThis[key]).map(name => `export const ${name} = globalThis.${key}.${name};`).join("\n");
        return { url: `data:text/javascript,${encodeURIComponent(exports)}`, shortCircuit: true };
      }
      return nextResolve(specifier, context);
    },
  });
  let service;
  try { service = await import(url); }
  finally { loader.deregister(); delete globalThis[key]; }
  const events = [];
  const unsubscribe = service.onLspServerInstalled(async (id) => {
    await Promise.resolve();
    events.push(id);
  });
  const unsubscribeFailedRuntime = service.onLspServerInstalled(() => { throw new Error("handshake failed"); });
  assert.deepEqual(await service.installLspServer("vue-ts"), result);
  assert.deepEqual(events, ["vue-ts"]);
  assert.deepEqual(calls, [{ command: "project_lsp_install", args: { serverId: "vue-ts" } }]);
  result = { output: "npm failed", error: "installation failed" };
  assert.deepEqual(await service.installLspServer("vue-ts"), result);
  assert.deepEqual(events, ["vue-ts"]);
  unsubscribe();
  unsubscribeFailedRuntime();
  result = { output: "installed again" };
  await service.installLspServer("ts-ls");
  assert.deepEqual(events, ["vue-ts"]);
});
