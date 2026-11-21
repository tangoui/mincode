import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

async function importTypeScriptSource(path, transform) {
  const source = transform(await readFile(new URL(path, import.meta.url), "utf8"));
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}#${Math.random()}`);
}

globalThis.__secureSaveStrictMocks = { saved: [], stored: new Map(), failure: null };

// Mirrors secure-persistent-store: native failures only reject in strict mode.
const secureStoreMock = `
  const loadSecurePersistedValue = async (key) =>
    globalThis.__secureSaveStrictMocks.stored.get(key) ?? null;
  const saveSecurePersistedValue = async (key, value, _snapshot, options = {}) => {
    globalThis.__secureSaveStrictMocks.saved.push([key, value, options]);
    const failure = globalThis.__secureSaveStrictMocks.failure;
    if (failure) {
      if (options.strict) throw failure;
      return;
    }
    globalThis.__secureSaveStrictMocks.stored.set(key, value);
  };`;

function replaceSecureStore(source) {
  return source
    .replace(
      'import { readonly, shallowRef } from "vue";',
      "const shallowRef = (value) => ({ value }); const readonly = (ref) => ref;"
    )
    .replace(/import \{[^}]*\} from "@\/services\/secure-persistent-store";/, secureStoreMock);
}

const approvalPolicy = await importTypeScriptSource(
  "../src/services/ai-approval-policy.ts",
  (source) => replaceSecureStore(source.replace(
    /import \{[^}]*\} from "@\/domain\/ai\/approval-policy";/,
    `const AI_APPROVAL_MODES = ["ask", "agent", "full-access"];
     const DEFAULT_AI_APPROVAL_MODE = "ask";
     const normalizeAiApprovalMode = (value) =>
       AI_APPROVAL_MODES.includes(value) ? value : DEFAULT_AI_APPROVAL_MODE;`
  ))
);

const toolAuthorizationPersistence = await importTypeScriptSource(
  "../src/services/tool-authorization-persistence.ts",
  replaceSecureStore
);

const configSyncService = await importTypeScriptSource(
  "../src/services/config-sync-service.ts",
  (source) => replaceSecureStore(source)
    .replace(
      /import \{[^}]*\} from "@\/domain\/settings\/config-sync";/,
      "const normalizeConfigSyncRepository = (value) => ({ ...(value ?? {}) });"
    )
    .replace(
      'import { invokeBackend } from "@/services/desktop-bridge";',
      "const invokeBackend = async () => { throw new Error('invokeBackend must not be called'); };"
    )
);

function reset() {
  globalThis.__secureSaveStrictMocks.saved = [];
  globalThis.__secureSaveStrictMocks.failure = null;
}

test("approval mode tightening that fails to persist rejects and keeps the previous mode", async () => {
  reset();
  await approvalPolicy.saveAiApprovalMode("full-access");
  assert.equal(approvalPolicy.aiApprovalMode.value, "full-access");

  globalThis.__secureSaveStrictMocks.failure = new Error("seal failed");
  await assert.rejects(approvalPolicy.saveAiApprovalMode("ask"), /seal failed/);
  assert.equal(approvalPolicy.aiApprovalMode.value, "full-access");
  assert.deepEqual(globalThis.__secureSaveStrictMocks.saved.at(-1), [
    approvalPolicy.AI_APPROVAL_POLICY_STORAGE_KEY,
    { mode: "ask" },
    { strict: true },
  ]);
  assert.deepEqual(
    globalThis.__secureSaveStrictMocks.stored.get(approvalPolicy.AI_APPROVAL_POLICY_STORAGE_KEY),
    { mode: "full-access" }
  );

  globalThis.__secureSaveStrictMocks.failure = null;
  await approvalPolicy.saveAiApprovalMode("ask");
  assert.equal(approvalPolicy.aiApprovalMode.value, "ask");
});

test("revoking a global authorization that fails to persist rejects and keeps the grant", async () => {
  reset();
  const grant = {
    scope: "global",
    toolName: "shell.exec",
    resourceId: "workspace",
    parameterFingerprint: "a".repeat(64),
    risk: "write",
  };
  await toolAuthorizationPersistence.saveGlobalToolAuthorizations([grant]);
  assert.deepEqual(toolAuthorizationPersistence.globalToolAuthorizations.value, [grant]);

  globalThis.__secureSaveStrictMocks.failure = new Error("store failed");
  await assert.rejects(
    toolAuthorizationPersistence.revokeGlobalToolAuthorization(grant),
    /store failed/
  );
  assert.deepEqual(toolAuthorizationPersistence.globalToolAuthorizations.value, [grant]);
  assert.deepEqual(globalThis.__secureSaveStrictMocks.saved.at(-1)?.[2], { strict: true });
  await assert.rejects(toolAuthorizationPersistence.clearGlobalToolAuthorizations(), /store failed/);
  assert.deepEqual(toolAuthorizationPersistence.globalToolAuthorizations.value, [grant]);

  globalThis.__secureSaveStrictMocks.failure = null;
  await toolAuthorizationPersistence.clearGlobalToolAuthorizations();
  assert.deepEqual(toolAuthorizationPersistence.globalToolAuthorizations.value, []);
});

test("saving the config-sync repository rejects when secure persistence fails", async () => {
  reset();
  const repository = {
    remoteUrl: "https://example.com/config.git",
    branch: "main",
    authMode: "https-token",
    username: "me",
    token: "secret-token",
    encryptionPassword: "pw",
    rememberPassword: false,
  };

  globalThis.__secureSaveStrictMocks.failure = new Error("seal failed");
  await assert.rejects(configSyncService.saveConfigSyncRepository(repository), /seal failed/);
  const [key, value, options] = globalThis.__secureSaveStrictMocks.saved.at(-1);
  assert.equal(key, configSyncService.CONFIG_SYNC_REPOSITORY_KEY);
  assert.deepEqual(options, { strict: true });
  assert.equal(value.encryptionPassword, "", "unremembered password must not be persisted");
  assert.equal(globalThis.__secureSaveStrictMocks.stored.has(key), false);

  globalThis.__secureSaveStrictMocks.failure = null;
  await configSyncService.saveConfigSyncRepository(repository);
  assert.equal(globalThis.__secureSaveStrictMocks.stored.get(key)?.token, "secret-token");
});
