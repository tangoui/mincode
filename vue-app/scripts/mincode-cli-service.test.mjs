import "./ts-resolve.mjs";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";

// The service only needs the bridge for its async calls; normalization is pure.
const desktopStub = `export const invokeBackend = async () => { throw new Error("not used"); };`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    const resolved = nextResolve(specifier, context);
    if (resolved.url.endsWith("/src/services/desktop-bridge.ts")) {
      return { url: `data:text/javascript,${encodeURIComponent(desktopStub)}`, shortCircuit: true };
    }
    return resolved;
  },
});

const { normalizeMincodeCliStatus } = await import("../src/services/mincode-cli-service.ts");

test("normalizeMincodeCliStatus keeps the PATH hint directory", () => {
  const status = normalizeMincodeCliStatus({
    state: "missing",
    installable: true,
    linkPath: "/h/.local/bin/mincode",
    pathHint: "/h/.local/bin",
  });
  assert.equal(status.pathHint, "/h/.local/bin");
  assert.equal(status.linkPath, "/h/.local/bin/mincode");
});

test("normalizeMincodeCliStatus defaults a missing or invalid PATH hint to empty", () => {
  assert.equal(normalizeMincodeCliStatus({ state: "installed" }).pathHint, "");
  assert.equal(normalizeMincodeCliStatus({ state: "missing", pathHint: 42 }).pathHint, "");
});
