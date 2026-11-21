import assert from "node:assert/strict";
import test from "node:test";
import { registerHooks } from "node:module";

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/dev-performance") {
      const source = "export const measureDevAsync = (_label, run) => run();";
      return { url: "data:text/javascript," + encodeURIComponent(source), shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});

globalThis.window = globalThis;
let queued = [];
const calls = [];
globalThis.tango = {
  async call(method, request) {
    calls.push({ method, command: request?.command });
    const targets = queued;
    queued = [];
    return targets;
  },
  stream() {},
  socket() {},
  emit() {},
  onRaw: () => () => {},
};

const { drainPendingDesktopNavigation } = await import("../src/services/desktop-navigation.ts");

test("queued desktop targets are delivered once", async () => {
  queued = [
    { kind: "view", value: "settings", seq: 1 },
    { kind: "ai", seq: 3 },
    null,
    { kind: 7 },
  ];
  const delivered = [];
  await drainPendingDesktopNavigation((target) => delivered.push(target));
  assert.deepEqual(calls.at(-1), { method: "mincode.invoke", command: "desktop_take_pending_navigation" });
  assert.deepEqual(delivered, [{ kind: "view", value: "settings" }, { kind: "ai", value: undefined }]);

  // The backend marks the frontend ready on the first drain: nothing is replayed.
  delivered.length = 0;
  await drainPendingDesktopNavigation((target) => delivered.push(target));
  assert.deepEqual(delivered, []);
});
