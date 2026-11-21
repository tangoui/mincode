import { realpathSync } from "node:fs";
import { readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";
import { availableParallelism } from "node:os";

// Several test files spawn Go/Vite builds of their own. Bound file concurrency
// instead of allowing CPU-count-based defaults to oversubscribe those builds.
export function testConcurrency(value = process.env.MINCODE_TEST_CONCURRENCY) {
  if (value === undefined) return Math.max(1, Math.min(4, availableParallelism() - 1));
  if (!/^[1-9]\d*$/.test(value) || !Number.isSafeInteger(Number(value))) {
    throw new Error("MINCODE_TEST_CONCURRENCY must be a positive safe integer");
  }
  return Number(value);
}

const scriptsDirectory = new URL("./", import.meta.url);

export function buildTestArgs(testFiles, resolverURL = new URL("./ts-resolve.mjs", scriptsDirectory)) {
  return [
    "--experimental-strip-types",
    "--import",
    resolverURL.href,
    "--test",
    ...testFiles,
  ];
}

export async function main(directory = scriptsDirectory) {
  const testFiles = (await readdir(directory, { withFileTypes: true }))
    .filter((entry) => entry.isFile() && entry.name.endsWith(".test.mjs"))
    .map((entry) => fileURLToPath(new URL(entry.name, directory)))
    .sort();

  if (testFiles.length === 0) {
    throw new Error("No frontend test files found in vue-app/scripts");
  }

  const child = spawn(process.execPath, [
    `--test-concurrency=${testConcurrency()}`,
    ...buildTestArgs(testFiles),
  ], { stdio: "inherit" });
  const exitCode = await new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code) => resolve(code));
  });
  return exitCode ?? 1;
}

// import.meta.url 已解析符号链接/junction，argv[1] 未解析；两侧都取真实路径再比较，
// 否则经链接目录运行时会静默跳过全部测试并以 0 退出。
function isEntryPoint() {
  if (!process.argv[1]) return false;
  try {
    return realpathSync.native(process.argv[1]) === realpathSync.native(fileURLToPath(import.meta.url));
  } catch {
    return false;
  }
}

if (isEntryPoint()) {
  process.exitCode = await main();
}
