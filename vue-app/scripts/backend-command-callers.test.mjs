import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { test } from "node:test";
import { parse } from "vue/compiler-sfc";
import ts from "typescript";
import { BACKEND_COMMANDS } from "../src/generated/backend-commands.ts";

const sourceRoot = new URL("../src/", import.meta.url);

const documentedExceptions = new Set([
  // ssh-command-history.test.mjs covers the typed `ssh_history_${action}` service.
  "ssh_history_list",
  "ssh_history_update",
  // The diagnostic readout pairs with stream_diagnostics_report for inspecting IPC failures.
  "stream_diagnostics_get",
  // The Agent control server dispatches these directly through the Go command registry.
  "terminal_control_list",
  "terminal_control_send",
  "terminal_control_set_metadata",
]);

async function* sourceFiles(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (entry.isDirectory() && !["generated", "browser-runtime"].includes(entry.name)) {
      yield* sourceFiles(new URL(`${entry.name}/`, directory));
    } else if (entry.isFile() && /\.(?:[cm]?[jt]s|vue)$/.test(entry.name) && !entry.name.endsWith(".d.ts")) {
      yield new URL(entry.name, directory);
    }
  }
}

function collectStringLiterals(filename, source, literals) {
  const tree = ts.createSourceFile(filename, source, ts.ScriptTarget.Latest, false);
  function visit(node) {
    if (ts.isStringLiteralLike(node)) literals.add(node.text);
    ts.forEachChild(node, visit);
  }
  visit(tree);
}

test("each registered backend command has a frontend caller or a documented exception", async () => {
  const literals = new Set();
  for await (const file of sourceFiles(sourceRoot)) {
    const source = await readFile(file, "utf8");
    if (file.pathname.endsWith(".vue")) {
      const { descriptor } = parse(source, { filename: file.pathname });
      for (const script of [descriptor.script, descriptor.scriptSetup]) {
        if (script) collectStringLiterals(`${file.pathname}.${script.lang ?? "js"}`, script.content, literals);
      }
    } else {
      collectStringLiterals(file.pathname, source, literals);
    }
  }

  const missing = BACKEND_COMMANDS.filter((command) => !literals.has(command) && !documentedExceptions.has(command));
  assert.deepEqual(missing, [], `Backend commands without frontend references: ${missing.join(", ")}`);
});
