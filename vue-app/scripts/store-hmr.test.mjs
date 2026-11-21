// Pinia 的 useStore 命中 `pinia._s` 缓存后就不再执行 setup，因此模块热更新后
// store 实例仍是旧的，闭包里握着的也还是旧函数。症状极具迷惑性：模板和依赖模块
// 都更新了，只有 store 里的逻辑没变，看起来像「改了代码没生效」。
// 排查 vue-ls 时就被这个假象误导过一次（模板已渲染新文案，但 sanitizeLspSettings
// 仍是旧版本，点「启用」照样被丢掉）。每个 store 都必须挂 acceptHMRUpdate。
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const storesDir = fileURLToPath(new URL("../src/stores/", import.meta.url));

function storeFiles() {
  return fs
    .readdirSync(storesDir)
    .filter((name) => name.endsWith(".ts") && !name.endsWith(".test.ts"))
    .map((name) => ({ name, source: fs.readFileSync(path.join(storesDir, name), "utf8") }))
    .filter(({ source }) => source.includes("defineStore("));
}

test("every Pinia store accepts HMR updates", () => {
  const files = storeFiles();
  assert.ok(files.length > 0, "expected to find at least one store");

  const missing = files
    .filter(({ source }) => !/import\.meta\.hot\.accept\(\s*acceptHMRUpdate\(/.test(source))
    .map(({ name }) => name);

  assert.deepEqual(
    missing,
    [],
    `${missing.join(", ")} call defineStore but never acceptHMRUpdate — edits to these stores ` +
      `(or to anything they close over) will not take effect until a full reload, which reads as ` +
      `"my fix did nothing"`,
  );
});

// acceptHMRUpdate 必须收到该文件真正导出的那个 useXxxStore：传错名字不会报错，
// 但热更新会静默失效，退化成本来要修的那个 bug。
test("each store hands acceptHMRUpdate its own exported store", () => {
  for (const { name, source } of storeFiles()) {
    const exported = source.match(/export const (use\w+)\s*=\s*defineStore/);
    assert.ok(exported, `${name}: could not find an exported defineStore binding`);

    const accepted = source.match(/acceptHMRUpdate\(\s*(\w+)\s*,/);
    assert.ok(accepted, `${name}: acceptHMRUpdate call not found`);
    assert.equal(
      accepted[1],
      exported[1],
      `${name}: acceptHMRUpdate got ${accepted[1]} but the file exports ${exported[1]}`,
    );
  }
});
