import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

async function importRunnerService() {
  const source = await readFile(
    new URL("../src/services/http-collection-runner-service.ts", import.meta.url),
    "utf8"
  );
  const testableSource = source
    .replace(
      /import \{[\s\S]*?\} from "@\/domain\/http\/collection-runner";/,
      `const evaluateHttpCollectionResponse = (request, response) => ({
         requestId: request.id,
         requestName: request.name,
         method: request.method,
         url: request.url,
         passed: true,
         elapsedMs: response.elapsedMs,
       });
       const summarizeHttpCollectionRun = (results, durationMs) => ({
         results,
         total: results.length,
         passed: results.length,
         failed: 0,
         durationMs,
       });`
    )
    .replace(
      'import { buildNativeHttpRequestPayload } from "@/domain/http/native-request";',
      `const buildNativeHttpRequestPayload = (request, expand) => ({
         url: expand(request.url),
         authToken: expand(request.authToken),
       });`
    )
    .replace(
      /import \{[\s\S]*?\} from "@\/domain\/http\/request-collection";/,
      `const resolveHttpRequestEnvironment = (collection, environmentId, environmentMode) => {
         const environments = collection.environments ?? [];
         return (environmentMode === "fixed"
           ? environments.find((environment) => environment.id === environmentId)
           : undefined)
           ?? environments.find((environment) => environment.id === collection.activeEnvironmentId)
           ?? environments[0];
       };`
    )
    .replace(
      'import { invokeBackend, isDesktopRuntime } from "./desktop-bridge";',
      `const payloads = [];
       export const __payloads = payloads;
       const isDesktopRuntime = () => true;
       const invokeBackend = async (_command, args) => {
         payloads.push(args.payload);
         return { elapsedMs: 1 };
       };`
    );
  const compiled = ts.transpileModule(testableSource, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return import(
    `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}#${randomUUID()}`
  );
}

function variable(id, key, value, enabled = true) {
  return { id, key, value, enabled };
}

function request(id, environmentId, authToken = "Bearer {{token}}") {
  return {
    id,
    name: id,
    method: "GET",
    url: "https://{{host}}/resource",
    params: [],
    headers: [],
    body: "",
    authType: "bearer",
    authToken,
    documentation: "",
    environmentMode: environmentId ? "fixed" : "follow",
    environmentId,
    updatedAt: 1,
  };
}

test("collection runner resolves each request's fixed environment or the collection active environment", async () => {
  const runner = await importRunnerService();
  const collection = {
    id: "collection",
    name: "Collection",
    activeEnvironmentId: "active",
    environments: [
      {
        id: "active",
        name: "Active",
        vars: [variable("active-host", "host", "active.test"), variable("active-token", "token", "active-token")],
      },
      {
        id: "fixed",
        name: "Fixed",
        vars: [variable("fixed-host", "host", "fixed.test"), variable("fixed-token", "token", "fixed-token")],
      },
    ],
    requests: [request("fixed-request", "fixed"), request("active-request")],
  };

  await runner.runHttpCollection(collection);

  assert.deepEqual(runner.__payloads, [
    { url: "https://fixed.test/resource", authToken: "Bearer fixed-token" },
    { url: "https://active.test/resource", authToken: "Bearer active-token" },
  ]);
});

test("collection runner falls back to the active environment when a stale fixed id is encountered", async () => {
  const runner = await importRunnerService();
  await runner.runHttpCollection({
    id: "collection",
    name: "Collection",
    activeEnvironmentId: "active",
    environments: [{ id: "active", name: "Active", vars: [variable("host", "host", "active.test")] }],
    requests: [request("missing-environment", "missing", "saved-token")],
  });

  assert.deepEqual(runner.__payloads, [
    { url: "https://active.test/resource", authToken: "saved-token" },
  ]);
});
