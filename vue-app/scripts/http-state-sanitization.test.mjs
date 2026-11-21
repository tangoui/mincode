import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

async function importSanitizer() {
  const source = await readFile(
    new URL("../src/services/http-state-persistence.ts", import.meta.url),
    "utf8"
  );
  const testableSource = source
    .replace(/import \{ mergeLegacyHttpResourceStates,[\s\S]*?from "@\/domain\/http\/resources";/, "")
    .replace(/import \{ loadAllPersistedValues \} from "@\/services\/persistent-store";/, "")
    .replace(/import \{[\s\S]*?\} from "@\/services\/secure-persistent-store";/, "")
    .replace(/import \{ createWorkspaceScopedPersistence \} from "@\/services\/workspace-scoped-persistence";/, "")
    .replace(/export function createHttpStatePersistence<[\s\S]*$/m, "");
  const compiled = ts.transpileModule(testableSource, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return import(
    `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}#${randomUUID()}`
  );
}

const secretRequest = {
  id: "request",
  name: "Request",
  url: "https://secret.test",
  authToken: "token",
  authUsername: "username",
  authPassword: "password",
  apiKeyValue: "api-key",
  oauth2ClientSecret: "client-secret",
  oauth2AccessToken: "oauth-token",
  params: [{ id: "param", key: "secret", value: "param-value", enabled: true }],
  headers: [{ id: "header", key: "Authorization", value: "header-value", enabled: true }],
  formBody: [{ id: "file", key: "file", value: "/private/file", fileName: "file.txt", enabled: true }],
  body: "secret-body",
  binaryFilePath: "/private/binary",
  binaryFileName: "binary.bin",
  graphqlQuery: "secret-query",
  graphqlVariables: "secret-variables",
  documentation: "secret-docs",
};

test("browser HTTP snapshot redacts nested collection environments and request values", async () => {
  const { sanitizeHttpStateForBrowser } = await importSanitizer();
  const sanitized = sanitizeHttpStateForBrowser({
    environments: [{ id: "legacy", name: "Legacy", vars: [{ id: "legacy-row", key: "x", value: "legacy-secret" }] }],
    collections: [{
      id: "root",
      name: "Root",
      environments: [{ id: "env", name: "Env", vars: [{ id: "row", key: "token", value: "env-secret" }] }],
      requests: [secretRequest],
      children: [{ id: "child", name: "Child", requests: [secretRequest] }],
    }],
    tabs: [secretRequest],
    history: [{ id: "history", url: "https://history-secret.test" }],
  });

  assert.equal(sanitized.environments[0].vars[0].value, "");
  assert.equal(sanitized.collections[0].environments[0].vars[0].value, "");
  assert.equal(sanitized.collections[0].requests[0].url, "");
  assert.equal(sanitized.collections[0].requests[0].authToken, "");
  assert.equal(sanitized.collections[0].children[0].requests[0].body, "");
  assert.equal(sanitized.tabs[0].oauth2ClientSecret, "");
  assert.equal(sanitized.tabs[0].params[0].value, "");
  assert.equal(sanitized.tabs[0].formBody[0].fileName, "");
  assert.equal(sanitized.history[0].url, "");
});
