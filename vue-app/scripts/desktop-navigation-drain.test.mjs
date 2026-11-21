// Cold-start desktop navigation (services/plugin-uri-links.ts): targets queued by the Go desktop
// package before App.vue listened are drained once; plugin links go to the cold-start link flow,
// every other kind to App.vue's desktop:navigate handler (a mincode://settings link that launched
// the app must still open Settings).
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";

const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/dev-performance") {
      return { url: dataModule("export const measureDevAsync = (_name, run) => run();"), shortCircuit: true };
    }
    if (specifier === "@/services/plugin-uri-links-runtime") {
      return {
        url: dataModule("export const handleColdStartPluginLinks = (urls) => globalThis.__coldStartLinks.push(...urls); export const handlePluginUriLink = () => {};"),
        shortCircuit: true,
      };
    }
    return nextResolve(specifier, context);
  },
});

globalThis.window = globalThis;
globalThis.__coldStartLinks = [];
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

const { drainPendingDesktopNavigation } = await import("../src/services/plugin-uri-links.ts");

test("queued targets of every kind are delivered once; plugin links take the cold-start flow", async () => {
  queued = [
    { kind: "view", value: "settings", seq: 1 },
    { kind: "plugin-uri", value: "mincode://p.q/open?x=1", seq: 2 },
    { kind: "ai", seq: 3 },
    null,
    { kind: 7 },
  ];
  const delivered = [];
  await drainPendingDesktopNavigation((target) => delivered.push(target));
  assert.deepEqual(calls.at(-1), { method: "mincode.invoke", command: "desktop_take_pending_navigation" });
  assert.deepEqual(delivered, [{ kind: "view", value: "settings" }, { kind: "ai", value: undefined }]);
  assert.deepEqual(globalThis.__coldStartLinks, ["mincode://p.q/open?x=1"]);

  // The backend marks the frontend ready on the first drain: nothing is replayed.
  delivered.length = 0;
  await drainPendingDesktopNavigation((target) => delivered.push(target));
  assert.deepEqual(delivered, []);
});
