/**
 * Resolver hook for `node --test` so frontend tests can import the same
 * `@/…` and extensionless specifiers the Vite build resolves. Test-only: the
 * shipped bundle is resolved by Vite, not by Node.
 */
import { registerHooks } from "node:module";
import { statSync } from "node:fs";
import { fileURLToPath } from "node:url";

const sourceRoot = new URL("../src/", import.meta.url);
const CANDIDATE_SUFFIXES = ["", ".ts", ".mts", ".js", ".mjs", "/index.ts"];

function existingFile(base) {
  for (const suffix of CANDIDATE_SUFFIXES) {
    const candidate = new URL(base.href + suffix);
    // 只匹配文件：目录（如 CommonJS 依赖 require("./lib")）交给 Node 按 index.js 等规则解析。
    if (statSync(fileURLToPath(candidate), { throwIfNoEntry: false })?.isFile()) return candidate.href;
  }
  return null;
}

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.startsWith("@/")) {
      const url = existingFile(new URL(specifier.slice(2), sourceRoot));
      if (url) return { url, shortCircuit: true };
    } else if (specifier.startsWith("./") || specifier.startsWith("../")) {
      const url = existingFile(new URL(specifier, context.parentURL));
      if (url) return { url, shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});
