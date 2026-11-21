import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import {
  buildPluginHttpCollectionRegistry,
  httpCollectionPreviewSummary,
  isSafeTemplateUrl,
  isSensitiveHttpKey,
  looksLikeHttpCredential,
  templateRequestToDraft,
  templateToHttpCollection,
  uniqueCollectionName,
} from "../src/domain/http/plugin-collections.ts";
import { restoreHttpResources } from "../src/domain/http/resources.ts";

function contribution(entries, overrides = {}) {
  return {
    pluginId: "acme",
    pluginName: "Acme Tools",
    version: "1.0.0",
    source: "installed",
    runtime: "declarative",
    activeInWorkspace: false,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

function collection(mutate) {
  const value = {
    name: "Smoke checks",
    requests: [{
      name: "OpenAPI",
      method: "GET",
      url: "{{baseUrl}}/openapi.json",
      params: [{ key: "page", value: "1", enabled: false, description: "Page" }],
      headers: [{ key: "Accept", value: "application/json", enabled: true }, { key: "Authorization", value: "{{token}}", enabled: true }],
      body: "",
      bodyType: "none",
      documentation: "Read the spec.",
    }],
    children: [{
      name: "Health",
      requests: [{ name: "Ping", method: "POST", url: "https://api.example.com/ping", params: [], headers: [], body: "{\"ok\":true}", bodyType: "json" }],
      children: [],
    }],
    environments: [{ name: "Local", vars: [{ key: "baseUrl", value: "http://localhost:8080", enabled: true }, { key: "token", value: "", enabled: true }] }],
  };
  mutate?.(value);
  return value;
}

function entry(id, value = collection()) {
  return { id, title: `Title ${id}`, counts: { requests: 2, folders: 1, environments: 1 }, collection: value };
}

function counter() {
  let next = 0;
  return (prefix) => `${prefix}-${++next}`;
}

test("registry is global and re-validates the host snapshot", () => {
  const registry = buildPluginHttpCollectionRegistry([contribution([entry("smoke")])]);
  assert.equal(registry.issues.length, 0);
  assert.equal(registry.collections.length, 1);
  const [template] = registry.collections;
  assert.equal(template.key, "acme.smoke");
  assert.deepEqual(template.counts, { requests: 2, folders: 1, environments: 1 });
  assert.equal(template.collection.requests[0].headers[1].value, "{{token}}");
});

test("secrets and unsafe URLs are rejected by the frontend layer", () => {
  const cases = [
    (value) => { value.requests[0].headers[1].value = "Bearer abc"; },
    (value) => { value.requests[0].url = "https://user:pw@example.com/"; },
    (value) => { value.environments[0].vars[1].value = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig"; },
    (value) => { value.requests[0].body = "AKIAABCDEFGHIJKLMNOP"; },
    (value) => { value.requests[0].authType = "bearer"; },
  ];
  for (const mutate of cases) {
    const registry = buildPluginHttpCollectionRegistry([contribution([entry("bad", collection(mutate))])]);
    assert.equal(registry.collections.length, 0, JSON.stringify(collection(mutate)));
    assert.equal(registry.issues.length, 1);
    assert.equal(registry.issues[0].stage, "frontend");
  }
  assert.equal(isSensitiveHttpKey("X-Api-Key", true), true);
  assert.equal(isSensitiveHttpKey("client_secret", false), true);
  assert.equal(isSensitiveHttpKey("page", false), false);
  assert.equal(looksLikeHttpCredential(`ghp_${"a".repeat(36)}`), true);
  assert.equal(looksLikeHttpCredential("-----BEGIN PRIVATE KEY-----"), true);
  assert.equal(looksLikeHttpCredential("hello"), false);
  assert.equal(isSafeTemplateUrl("{{baseUrl}}/x"), true);
  assert.equal(isSafeTemplateUrl("file:///etc/passwd"), false);
});

test("import creates a fresh top-level collection in the stored shape", () => {
  const [template] = buildPluginHttpCollectionRegistry([contribution([entry("smoke")])]).collections;
  const ids = counter();
  const converted = templateToHttpCollection(template.collection, ids, ["Smoke checks"], 1000, "Default");
  assert.equal(converted.name, "Smoke checks (2)");
  assert.equal(converted.environments.length, 1);
  assert.equal(converted.activeEnvironmentId, converted.environments[0].id);
  const request = converted.requests[0];
  assert.equal(request.authType, "none");
  assert.equal(request.authToken, "");
  assert.equal(request.environmentMode, "follow");
  assert.equal(request.updatedAt, 1000);
  const encoded = JSON.stringify(converted);
  assert.equal(encoded.includes("acme"), false, "no link back to the plugin");

  const allIds = [];
  const walk = (folder) => {
    allIds.push(folder.id);
    folder.requests.forEach((item) => allIds.push(item.id, ...item.params.map((row) => row.id), ...item.headers.map((row) => row.id)));
    (folder.children ?? []).forEach(walk);
  };
  walk(converted);
  converted.environments.forEach((environment) => allIds.push(environment.id, ...environment.vars.map((row) => row.id)));
  assert.equal(new Set(allIds).size, allIds.length, "every id is fresh and unique");

  // A second import produces different ids and the storage round-trip keeps them.
  const again = templateToHttpCollection(template.collection, ids, [converted.name, "Smoke checks"], 2000);
  assert.notEqual(again.id, converted.id);
  assert.equal(again.name, "Smoke checks (3)");
  const restored = restoreHttpResources({ version: 1, collections: [converted, again] });
  assert.equal(restored.collections.length, 2);
  assert.equal(restored.collections[0].id, converted.id);
  assert.equal(restored.collections[0].requests[0].headers[1].value, "{{token}}");
  assert.equal(restored.collections[0].children[0].requests[0].body, "{\"ok\":true}");
  assert.equal(restored.collections[0].environments[0].name, "Local");
});

test("draft and helpers", () => {
  const [template] = buildPluginHttpCollectionRegistry([contribution([entry("smoke")])]).collections;
  const draft = templateRequestToDraft(template.collection.requests[0], counter());
  assert.equal(draft.method, "GET");
  assert.equal(draft.authType, "none");
  assert.equal(draft.params[0].enabled, false);
  assert.equal(uniqueCollectionName("A", []), "A");
  assert.equal(uniqueCollectionName("A", ["A", "A (2)"]), "A (3)");
  assert.deepEqual(httpCollectionPreviewSummary({ httpCollections: [{ title: "One" }, { title: "Two" }] }), { count: 2, titles: ["One", "Two"] });
  assert.equal(httpCollectionPreviewSummary({}), null);
});

test("HTTP workspace import and draft handlers never send", async () => {
  const source = await readFile(new URL("../src/views/HttpWorkspaceView.vue", import.meta.url), "utf8");
  for (const name of ["importPluginTemplate", "openTemplateDraft"]) {
    const start = source.indexOf(`function ${name}(`);
    assert.ok(start > 0, name);
    const body = source.slice(start, source.indexOf("\n}\n", start));
    assert.doesNotMatch(body, /\bsend\w*\(|http_send|invokeBackend/, name);
  }
  const consumer = await readFile(new URL("../src/services/contributions/index.ts", import.meta.url), "utf8");
  assert.match(consumer, /registerHttpCollectionContributionConsumer,/);
});
