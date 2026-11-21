import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { promisify } from "node:util";

const exec = promisify(execFile);

for (const outDir of [undefined, "自定义 dist"]) {
  test(`打包预算检查支持空格和非 ASCII 目录名（${outDir ?? "默认输出"}）`, async () => {
    const root = await mkdtemp(join(tmpdir(), "mincode bundle-中文-"));
    const env = { ...process.env };
    delete env.VUE_OUTDIR;
    if (outDir !== undefined) env.VUE_OUTDIR = outDir;
    try {
      const scripts = join(root, "vue-app", "scripts");
      const dist = outDir === undefined ? join(root, "frontend", "dist") : join(root, "vue-app", outDir);
      const assets = join(dist, "assets");
      await mkdir(scripts, { recursive: true });
      await mkdir(assets, { recursive: true });
      const script = join(scripts, "check-bundle-budget.mjs");
      await writeFile(script, await readFile(new URL("./check-bundle-budget.mjs", import.meta.url)));
      await writeFile(join(dist, "index.html"), '<script src="./assets/index-test.js"></script><link href="./assets/index-test.css">');
      for (const name of ["index-test.js", "index-test.css", "editor.api-test.js", "standaloneReferenceSearch-test.js"]) {
        await writeFile(join(assets, name), "fixture");
      }
      const { stdout } = await exec(process.execPath, [script], { env });
      assert.match(stdout, /largest application chunk \(index-test.js\)/);
      assert.match(stdout, /Monaco editor API/);
      assert.match(stdout, /Monaco reference search/);
      await writeFile(join(assets, "index-test.js"), Buffer.alloc(193 * 1024));
      await assert.rejects(exec(process.execPath, [script], { env }), error => {
        assert.match(error.stderr, /Bundle budget exceeded/);
        assert.match(error.stderr, /entry JavaScript: 193\.0 KiB \/ 192\.0 KiB/);
        return true;
      });
    } finally {
      await rm(root, { recursive: true, force: true });
    }
});
}
