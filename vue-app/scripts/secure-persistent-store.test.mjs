import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

async function importSecureStore(runtime) {
  const source = await readFile(
    new URL("../src/services/secure-persistent-store.ts", import.meta.url),
    "utf8"
  );
  const testableSource = source
    .replace(
      /import \{[\s\S]*?\} from "@\/services\/persistent-store";/,
      `const enqueuePersistedWrite = (task) => task();
       const writesDesktopBrowserSnapshot = () => false;
       const clearDesktopBrowserSnapshot = () => undefined;
       const isPersistedWriteFrozen = () => false;
       const writeBrowserSnapshot = () => undefined;`
    )
    .replace(
      /import \{ invokeBackend, isDesktopRuntime \} from "@\/services\/desktop-bridge";/,
      "const { invokeBackend, isDesktopRuntime } = globalThis.__MINCODE_SECURE_STORE_TEST__;"
    );
  const compiled = ts.transpileModule(testableSource, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  globalThis.__MINCODE_SECURE_STORE_TEST__ = runtime;
  return import(
    `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}#${randomUUID()}`
  );
}

test("migrates a legacy plain value before returning it", async () => {
  const calls = [];
  const legacy = { apiKey: "secret" };
  const store = await importSecureStore({
    isDesktopRuntime: () => true,
    invokeBackend: async (command, args) => {
      calls.push([command, args]);
      if (command === "store_get") return legacy;
      if (command === "seal_object") return "enc:v2:cipher";
      if (command === "store_set") return undefined;
      throw new Error(`unexpected command: ${command}`);
    },
  });

  assert.deepEqual(await store.loadSecurePersistedValue("settings"), legacy);
  assert.deepEqual(calls, [
    ["store_get", { key: "settings" }],
    ["seal_object", { json: JSON.stringify(legacy) }],
    ["store_set", { key: "settings", value: "enc:v2:cipher" }],
  ]);
});

test("does not return a legacy plain value when encrypted write-back fails", async () => {
  const legacy = { token: "secret" };
  const store = await importSecureStore({
    isDesktopRuntime: () => true,
    invokeBackend: async (command) => {
      if (command === "store_get") return legacy;
      if (command === "seal_object") return "enc:v2:cipher";
      if (command === "store_set") throw new Error("disk full");
      throw new Error(`unexpected command: ${command}`);
    },
  });

  await assert.rejects(
    store.loadSecurePersistedValue("settings"),
    /disk full/
  );
});

test("reads an encrypted value without rewriting it", async () => {
  const calls = [];
  const store = await importSecureStore({
    isDesktopRuntime: () => true,
    invokeBackend: async (command, args) => {
      calls.push([command, args]);
      if (command === "store_get") return "enc:v2:cipher";
      if (command === "unseal_object") return JSON.stringify({ apiKey: "secret" });
      throw new Error(`unexpected command: ${command}`);
    },
  });

  assert.deepEqual(await store.loadSecurePersistedValue("settings"), { apiKey: "secret" });
  assert.deepEqual(calls.map(([command]) => command), ["store_get", "unseal_object"]);
});
