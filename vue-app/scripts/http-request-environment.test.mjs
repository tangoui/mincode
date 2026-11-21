import assert from "node:assert/strict";
import test from "node:test";
import {
  moveHttpCollection,
  moveSavedHttpRequest,
  resolveHttpRequestEnvironment,
} from "../src/domain/http/request-collection.ts";

function request(id, environmentId) {
  return {
    id,
    name: id,
    method: "GET",
    url: "https://example.test",
    params: [],
    headers: [],
    body: "",
    authType: "none",
    documentation: "",
    environmentMode: environmentId ? "fixed" : "follow",
    environmentId,
    updatedAt: 1,
  };
}

function environment(id) {
  return { id, name: id, vars: [] };
}

test("resolves fixed request environment before the collection active environment", () => {
  const collection = {
    id: "root",
    name: "Root",
    requests: [],
    environments: [environment("dev"), environment("prod")],
    activeEnvironmentId: "dev",
  };

  assert.equal(resolveHttpRequestEnvironment(collection, "prod", "fixed")?.id, "prod");
  assert.equal(resolveHttpRequestEnvironment(collection)?.id, "dev");
  assert.equal(resolveHttpRequestEnvironment(collection, "missing", "fixed")?.id, "dev");
});

test("moving a request across top-level collections clears an invalid fixed environment", () => {
  const source = {
    id: "source",
    name: "Source",
    requests: [request("request", "source-env")],
    environments: [environment("source-env")],
    activeEnvironmentId: "source-env",
  };
  const target = {
    id: "target",
    name: "Target",
    requests: [],
    environments: [environment("target-env")],
    activeEnvironmentId: "target-env",
  };

  assert.equal(moveSavedHttpRequest([source, target], "request", "target"), true);
  assert.equal(target.requests[0].environmentMode, "follow");
  assert.equal(target.requests[0].environmentId, undefined);
});

test("moving collections enforces environment ownership at the new hierarchy level", () => {
  const child = {
    id: "child",
    name: "Child",
    requests: [request("request", "source-env")],
    environments: [environment("source-env")],
    activeEnvironmentId: "source-env",
  };
  const target = {
    id: "target",
    name: "Target",
    requests: [],
    environments: [environment("target-env")],
    activeEnvironmentId: "target-env",
  };
  const collections = [child, target];

  assert.equal(moveHttpCollection(collections, "child", "target"), true);
  assert.equal(child.environments, undefined);
  assert.equal(child.activeEnvironmentId, undefined);
  assert.equal(child.requests[0].environmentId, "source-env");
  assert.equal(target.environments.some((item) => item.id === "source-env"), true);

  assert.equal(moveHttpCollection(collections, "child"), true);
  assert.equal(child.environments.length, 1);
  assert.equal(child.activeEnvironmentId, child.environments[0].id);
});
