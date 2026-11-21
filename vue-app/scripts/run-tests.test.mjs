import test from "node:test";
import assert from "node:assert/strict";
import { copyFile, mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, sep } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";
import { buildTestArgs, testConcurrency } from "./run-tests.mjs";

// TAP 报告器输出 "# tests 1"，Node 23+ 默认的 spec 报告器输出 "ℹ tests 1"。
const testsSummary = /^(?:# |ℹ )tests 1\b/m;
const runnerURL = new URL("./run-tests.mjs", import.meta.url);
const resolverURL = new URL("./ts-resolve.mjs", import.meta.url);
const run = (args) => {
  const env = { ...process.env };
  // 独立测试进程不能继承父测试 runner 的内部 IPC 上下文。
  delete env.NODE_TEST_CONTEXT;
  return spawnSync(process.execPath, args, { encoding: "utf8", timeout: 30000, env });
};

async function fixture(t) {
  const directory = await mkdtemp(join(tmpdir(), "mincode test-entry # "));
  t.after(() => rm(directory, { recursive: true, force: true }));
  await writeFile(join(directory, "fixture.ts"), "export const value: number = 42;\n");
  const testFile = join(directory, "smoke.test.mjs");
  await writeFile(testFile, [
    'import test from "node:test";',
    'import assert from "node:assert/strict";',
    'import { value } from "./fixture";',
    'test("preload-smoke-only", () => assert.equal(value, 42));',
    "",
  ].join("\n"));
  return { directory, testFile };
}

test("测试文件并发可配置且拒绝无效预算", () => {
  assert.equal(testConcurrency("1"), 1);
  assert.equal(testConcurrency("4"), 4);
  for (const invalid of ["", "0", "-1", "1.5", "NaN", "Infinity", "2x", "9007199254740992"]) {
    assert.throws(() => testConcurrency(invalid), /positive safe integer/);
  }
});

test("参数构造保留文件路径，preload 使用 file URL", () => {
  const testFiles = [fileURLToPath(new URL("./run-tests.test.mjs", import.meta.url))];
  assert.deepEqual(buildTestArgs(testFiles), [
    "--experimental-strip-types", "--import", resolverURL.href, "--test", ...testFiles,
  ]);
  assert.equal(new URL(buildTestArgs(testFiles)[2]).protocol, "file:");
});

test("Windows 盘符和特殊字符不会变成 ESM 的盘符协议", () => {
  const windowsResolver = new URL("file:///C:/checkout%20space/%23tools/ts-resolve.mjs");
  const testFiles = ["C:\\checkout space\\test.test.mjs"];
  const args = buildTestArgs(testFiles, windowsResolver);
  assert.equal(args[2], windowsResolver.href);
  assert.equal(new URL(args[2]).protocol, "file:");
  assert.equal(args[4], testFiles[0]);
  assert.deepEqual(testFiles, ["C:\\checkout space\\test.test.mjs"]);
});

test("真实 Node preload 可解析无扩展名 TypeScript，且只运行专项 fixture", async (t) => {
  const { testFile } = await fixture(t);
  const result = run(buildTestArgs([testFile]));
  assert.equal(result.error, undefined);
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stdout, /preload-smoke-only/);
  assert.match(result.stdout, testsSummary);
  assert.doesNotMatch(result.stderr, /ERR_UNSUPPORTED_ESM_URL_SCHEME/);
});

test("main 仅发现指定目录的测试并返回真实子进程结果", async (t) => {
  const { directory, testFile } = await fixture(t);
  await writeFile(join(directory, "ignored.mjs"), 'throw new Error("不可执行的非测试文件");\n');
  const directoryURL = pathToFileURL(directory + sep).href;
  const args = ["--input-type=module", "--eval", [
    `const { main } = await import(${JSON.stringify(runnerURL.href)});`,
    `process.exitCode = await main(new URL(${JSON.stringify(directoryURL)}));`,
  ].join("\n")];
  const passed = run(args);
  assert.equal(passed.error, undefined);
  assert.equal(passed.status, 0, passed.stderr + passed.stdout);
  assert.match(passed.stdout, testsSummary);
  await writeFile(testFile, 'import test from "node:test"; test("expected-failure", () => { throw new Error("fixture failure"); });\n');
  const failed = run(args);
  assert.equal(failed.error, undefined);
  assert.equal(failed.status, 1, failed.stderr + failed.stdout);
  assert.match(failed.stdout, /fixture failure/);
});

test("经 junction/符号链接目录运行入口时仍会真正执行测试", async (t) => {
  const { directory } = await fixture(t);
  for (const name of ["run-tests.mjs", "ts-resolve.mjs"]) {
    await copyFile(fileURLToPath(new URL(`./${name}`, import.meta.url)), join(directory, name));
  }
  const linkRoot = await mkdtemp(join(tmpdir(), "mincode-test-link-"));
  t.after(() => rm(linkRoot, { recursive: true, force: true }));
  const linked = join(linkRoot, "linked");
  await symlink(directory, linked, process.platform === "win32" ? "junction" : "dir");
  const result = run([join(linked, "run-tests.mjs")]);
  assert.equal(result.error, undefined);
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stdout, /preload-smoke-only/);
});

test("相对路径指向目录时交还 Node 解析，不把目录当文件读取", async (t) => {
  const { directory, testFile } = await fixture(t);
  // happy-dom 等 CommonJS 依赖会 require("./目录")，由 Node 解析到目录下的 index.js。
  await mkdir(join(directory, "lib"));
  await writeFile(join(directory, "lib", "index.js"), "module.exports = { value: 7 };\n");
  await writeFile(join(directory, "entry.cjs"), 'module.exports = require("./lib");\n');
  await writeFile(testFile, [
    'import test from "node:test";',
    'import assert from "node:assert/strict";',
    'import { createRequire } from "node:module";',
    'test("preload-smoke-only", () => assert.equal(createRequire(import.meta.url)("./entry.cjs").value, 7));',
    "",
  ].join("\n"));
  const result = run(buildTestArgs([testFile], resolverURL));
  assert.equal(result.error, undefined);
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stdout, testsSummary);
});
