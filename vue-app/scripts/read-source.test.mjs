import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { readSource, readSourceSync } from "./read-source.mjs";

test("源码读取对 LF 和 CRLF 提供相同内容且保留字符串转义", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mincode-read-source-"));
  try {
    const source = 'function example() {\n  return "\\r\\n";\n}\n';
    for (const newline of ["\n", "\r\n"]) {
      const path = join(directory, "source.ts");
      await writeFile(path, source.replaceAll("\n", newline));
      assert.equal(await readSource(path), source);
      assert.equal(readSourceSync(path), source);
    }
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("源码读取不会隐藏缺失文件错误", async () => {
  const path = new URL("./missing-source-fixture.ts", import.meta.url);
  await assert.rejects(readSource(path), { code: "ENOENT" });
  assert.throws(() => readSourceSync(path), { code: "ENOENT" });
});
