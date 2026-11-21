import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  decodeEnvironmentPack,
  encodeEnvironmentPack,
  restoreEnvironmentPack,
} from "../src/domain/workspace/environment-pack.ts";

function request(id, environmentId) {
  return {
    id,
    name: id,
    method: "GET",
    url: "https://example.test/{{token}}",
    params: [],
    headers: [],
    body: "",
    authType: "none",
    documentation: "",
    environmentId,
    updatedAt: 1,
  };
}

function pack(httpCollections, httpEnvironments = { environments: [], activeEnvId: "" }) {
  return {
    version: 2,
    exportedAt: 1,
    appName: "MinCode",
    items: {
      sshConnections: [],
      dbConnections: [],
      httpCollections,
      httpEnvironments,
      localProjects: [],
    },
  };
}

test("HTTP 顶级集合环境模型可加密导出并完整往返", async () => {
  const source = pack([
    {
      id: "top",
      name: "Top",
      requests: [request("top-request", "env-b")],
      children: [
        {
          id: "child",
          name: "Child",
          requests: [request("child-request", "env-a")],
          children: [
            {
              id: "grandchild",
              name: "Grandchild",
              requests: [request("grandchild-request", "env-b")],
            },
          ],
        },
      ],
      environments: [
        { id: "env-a", name: "A", vars: [{ id: "row-a", key: "token", value: "a", enabled: true }] },
        { id: "env-b", name: "B", vars: [{ id: "row-b", key: "token", value: "b", enabled: true }] },
      ],
      activeEnvironmentId: "env-b",
    },
  ]);

  const restored = await decodeEnvironmentPack(
    await encodeEnvironmentPack(source, "password-123"),
    "password-123"
  );
  const collection = restored.items.httpCollections[0];

  assert.deepEqual(collection.environments, source.items.httpCollections[0].environments);
  assert.equal(collection.activeEnvironmentId, "env-b");
  assert.equal(collection.requests[0].environmentId, "env-b");
  assert.equal(collection.children[0].requests[0].environmentId, "env-a");
  assert.equal(collection.children[0].children[0].requests[0].environmentId, "env-b");
  assert.deepEqual(restored.items.httpEnvironments, { environments: [], activeEnvId: "" });
});

test("旧 httpEnvironments 迁移到每个缺少环境的顶级集合", () => {
  const restored = restoreEnvironmentPack(
    pack(
      [
        { id: "first", name: "First", requests: [request("one", "legacy-b")] },
        { id: "second", name: "Second", requests: [], children: [] },
      ],
      {
        environments: [
          { id: "legacy-a", name: "Legacy A", vars: [] },
          { id: "legacy-b", name: "Legacy B", vars: [] },
        ],
        activeEnvId: "legacy-b",
      }
    )
  );

  assert.deepEqual(
    restored.items.httpCollections.map((collection) => collection.environments?.map(({ id }) => id)),
    [["legacy-a", "legacy-b"], ["legacy-a", "legacy-b"]]
  );
  assert.deepEqual(
    restored.items.httpCollections.map((collection) => collection.activeEnvironmentId),
    ["legacy-b", "legacy-b"]
  );
  assert.equal(restored.items.httpCollections[0].requests[0].environmentId, "legacy-b");
  assert.notEqual(
    restored.items.httpCollections[0].environments,
    restored.items.httpCollections[1].environments
  );
  assert.deepEqual(restored.items.httpEnvironments, { environments: [], activeEnvId: "" });
});

test("旧全局环境不会覆盖已有顶级集合环境", () => {
  const restored = restoreEnvironmentPack(
    pack(
      [
        {
          id: "top",
          name: "Top",
          requests: [],
          environments: [{ id: "owned", name: "Owned", vars: [] }],
          activeEnvironmentId: "owned",
        },
      ],
      {
        environments: [{ id: "legacy", name: "Legacy", vars: [] }],
        activeEnvId: "legacy",
      }
    )
  );

  assert.deepEqual(restored.items.httpCollections[0].environments?.map(({ id }) => id), ["owned"]);
  assert.equal(restored.items.httpCollections[0].activeEnvironmentId, "owned");
});


const environmentPackDialog = await readFile(
  new URL("../src/components/settings/EnvironmentPackDialog.vue", import.meta.url),
  "utf8"
);

test("环境包提交失败会按新增 ID 补偿所有资源并持久化回滚结果", () => {
  assert.match(environmentPackDialog, /addedSshIds/);
  assert.match(environmentPackDialog, /addedDatabaseIds/);
  assert.match(environmentPackDialog, /addedHttpCollectionIds/);
  assert.match(environmentPackDialog, /addedProjectRoots/);
  assert.match(environmentPackDialog, /Promise\.allSettled/);
  assert.match(environmentPackDialog, /httpResourcesStore\.removeResources/);
  assert.match(environmentPackDialog, /projectStore\.flushPersistence\(\)/);
  assert.match(environmentPackDialog, /envImportRollbackFailed/);
});
