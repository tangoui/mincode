import assert from "node:assert/strict";
import test from "node:test";
import {
  HTTP_RESOURCES_VERSION,
  mergeLegacyHttpResourceStates,
  restoreHttpResources,
} from "../src/domain/http/resources.ts";

function request(id, updatedAt, url) {
  return {
    id,
    name: id,
    method: "GET",
    url,
    params: [],
    headers: [],
    body: "",
    authType: "none",
    documentation: "",
    updatedAt,
  };
}

function collection(id, requests = [], children = []) {
  return { id, name: id, requests, children };
}

test("merges durable HTTP resources from independent and deleted workspace snapshots", () => {
  const merged = mergeLegacyHttpResourceStates([
    {
      environments: [{ id: "env-a", name: "A", vars: [] }],
      collections: [collection("collection-a", [request("request-a", 1, "https://a.test")])],
    },
    {
      environments: [{ id: "env-deleted", name: "Deleted workspace", vars: [] }],
      collections: [
        collection("collection-deleted", [
          request("request-deleted", 2, "https://deleted.test"),
        ]),
      ],
    },
  ]);

  assert.equal(merged.version, HTTP_RESOURCES_VERSION);
  assert.deepEqual(merged.environments, []);
  assert.deepEqual(merged.collections.map((item) => item.id), [
    "collection-a",
    "collection-deleted",
  ]);
  assert.deepEqual(merged.collections[0].environments.map((item) => item.id), ["env-a"]);
  assert.deepEqual(merged.collections[1].environments.map((item) => item.id), ["env-deleted"]);
});

test("keeps the newest request when legacy workspaces contain the same request id", () => {
  const merged = mergeLegacyHttpResourceStates([
    { collections: [collection("shared", [request("same", 10, "https://old.test")])] },
    { collections: [collection("shared", [request("same", 20, "https://new.test")])] },
  ]);

  assert.equal(merged.collections[0].requests.length, 1);
  assert.equal(merged.collections[0].requests[0].url, "https://new.test");
  assert.equal(merged.collections[0].requests[0].updatedAt, 20);
});

test("filters malformed environments and collections during restore", () => {
  const restored = restoreHttpResources({
    environments: [
      null,
      { id: "", name: "" },
      { id: "env-ok", name: " Valid ", vars: [{ id: "row", key: "token", value: "x" }] },
      { id: "env-ok", name: "Duplicate", vars: [] },
    ],
    collections: [
      null,
      { id: 42, name: "bad", requests: [] },
      { id: "collection-ok", name: "Valid", requests: [], children: [{ id: 7 }] },
    ],
  });

  assert.deepEqual(restored.environments, []);
  assert.deepEqual(restored.collections.map((item) => item.id), ["collection-ok"]);
  assert.equal(restored.collections[0].environments[0].name, "Valid");
  assert.equal(restored.collections[0].environments[0].vars[0].valueType, "string");
  assert.equal(restored.collections[0].activeEnvironmentId, "env-ok");
  assert.deepEqual(restored.collections[0].children, []);
});

test("copies legacy environments to every top-level collection and clears invalid request pins", () => {
  const restored = restoreHttpResources({
    environments: [
      { id: "dev", name: "Dev", vars: [] },
      { id: "prod", name: "Prod", vars: [] },
    ],
    collections: [
      collection("first", [request("valid", 1, "https://one.test")], [
        collection("child", [{ ...request("invalid", 1, "https://two.test"), environmentId: "missing" }]),
      ]),
      collection("second", [{ ...request("fixed", 1, "https://three.test"), environmentId: "prod" }]),
    ],
  });

  assert.deepEqual(restored.collections.map((item) => item.environments.map((env) => env.id)), [
    ["dev", "prod"],
    ["dev", "prod"],
  ]);
  assert.equal(restored.collections[0].children[0].environments, undefined);
  assert.equal(restored.collections[0].children[0].requests[0].environmentMode, "follow");
  assert.equal(restored.collections[0].children[0].requests[0].environmentId, undefined);
  assert.equal(restored.collections[1].requests[0].environmentMode, "fixed");
  assert.equal(restored.collections[1].requests[0].environmentId, "prod");
});

test("creates a default environment and falls back from invalid active environment", () => {
  const restored = restoreHttpResources({
    collections: [
      { ...collection("empty"), activeEnvironmentId: "missing" },
      {
        ...collection("configured"),
        environments: [{ id: "only", name: "Only", vars: [] }],
        activeEnvironmentId: "missing",
      },
    ],
  });

  assert.equal(restored.collections[0].environments.length, 1);
  assert.equal(restored.collections[0].activeEnvironmentId, restored.collections[0].environments[0].id);
  assert.equal(restored.collections[1].activeEnvironmentId, "only");
});
