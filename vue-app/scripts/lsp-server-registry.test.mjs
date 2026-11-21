// 前端的 KNOWN_LSP_SERVER_IDS 是一份写死的白名单：sanitizeLspSettings 会把不在
// 表里的 id 静默丢掉。后端 registry.go 新增服务器时若忘了同步这里，表现是
// “点了启用没反应”——设置写进去又被过滤掉，没有任何报错。
// 加过 vue-ls 时就踩过一次，这条用例把两张表钉在一起。
import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import { KNOWN_LSP_SERVER_IDS, sanitizeLspSettings } from "../src/domain/projects/lsp.ts";

// Resolve from this module, not process.cwd(): the runner is also invoked from the repository/workspace root.
const registryPath = new URL("../../backend/lsp/registry.go", import.meta.url);

function backendServerIds() {
  const source = fs.readFileSync(registryPath, "utf8");
  const block = source.match(/var serverDefinitions = \[\]serverDefinition\{([\s\S]*?)\n\}/);
  assert.ok(block, "could not locate serverDefinitions in registry.go");
  return [...block[1].matchAll(/\bID:\s*"([^"]+)"/g)].map(([, id]) => id);
}

test("frontend allowlist matches the backend server registry", () => {
  const backend = backendServerIds();
  assert.ok(backend.length > 0, "registry.go must define at least one server");

  const frontend = new Set(KNOWN_LSP_SERVER_IDS);
  const missing = backend.filter((id) => !frontend.has(id));
  assert.deepEqual(
    missing,
    [],
    `registry.go defines ${missing.join(", ")} but KNOWN_LSP_SERVER_IDS omits them — enabling those servers would be silently discarded`,
  );

  const backendIds = new Set(backend);
  const stale = [...frontend].filter((id) => !backendIds.has(id));
  assert.deepEqual(stale, [], `KNOWN_LSP_SERVER_IDS lists ${stale.join(", ")} which no longer exist in registry.go`);
});

test("sanitize keeps every known server and drops unknown ids", () => {
  const kept = sanitizeLspSettings({ enabledServers: [...KNOWN_LSP_SERVER_IDS] });
  assert.deepEqual(kept.enabledServers, [...KNOWN_LSP_SERVER_IDS]);

  const filtered = sanitizeLspSettings({ enabledServers: ["vue-ls", "not-a-real-server"] });
  assert.deepEqual(filtered.enabledServers, ["vue-ls"]);
});
