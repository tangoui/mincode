#!/usr/bin/env node
// Vitest launcher that normalizes the Windows drive letter before Vitest starts.
//
// When the working directory uses a lowercase drive (for example `d:\code\...`,
// which some terminals and IDE agents produce), Node loads Vitest's runner from
// `file:///d:/...` while Vite resolves `import "vitest"` in setupFiles to
// `file:///D:/...`. ESM treats them as two module instances, so setupFiles fail
// with "Vitest failed to find the runner" and no test runs. Starting Vitest from
// a canonical upper-case path keeps every import on one instance.
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const upperDrive = (value) => (process.platform === "win32" ? value.replace(/^[a-z]:/, (drive) => drive.toUpperCase()) : value);

const appRoot = upperDrive(join(dirname(fileURLToPath(import.meta.url)), ".."));
const entry = join(appRoot, "node_modules", "vitest", "vitest.mjs");
const result = spawnSync(process.execPath, [entry, ...process.argv.slice(2)], {
  cwd: appRoot,
  stdio: "inherit",
});
if (result.error) {
  console.error(result.error);
  process.exit(1);
}
process.exit(result.status ?? 1);
