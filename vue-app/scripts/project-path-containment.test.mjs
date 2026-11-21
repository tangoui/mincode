import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import ts from "typescript";

const view = await readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8");

function extractFunction(name) {
  const source = view.match(new RegExp(`function ${name}\\([\\s\\S]*?\\n}\\n`))?.[0];
  assert.ok(source, `${name} must be present`);
  return source;
}

const helpers = ts.transpileModule(
  `${extractFunction("projectPathKey")}\n${extractFunction("isProjectPathWithin")}\nreturn { projectPathKey, isProjectPathWithin };`,
  { compilerOptions: { target: ts.ScriptTarget.ES2022 } }
).outputText;
const { projectPathKey, isProjectPathWithin } = new Function(helpers)();

test("project path containment accepts Windows backslash paths and drive-letter case differences", () => {
  assert.equal(isProjectPathWithin("C:\\work\\app", "C:\\work\\app"), true);
  assert.equal(isProjectPathWithin("C:\\work\\app\\packages\\api", "C:\\work\\app"), true);
  assert.equal(isProjectPathWithin("c:/work/app/packages/api", "C:\\work\\app\\"), true);
  assert.equal(isProjectPathWithin("C:\\Work\\App\\api", "c:/work/app"), true);
  assert.equal(isProjectPathWithin("\\\\server\\share\\repo", "//SERVER/share"), true);
  assert.equal(isProjectPathWithin("C:\\", "C:/"), true);
  assert.equal(isProjectPathWithin("C:\\api", "C:\\"), true);
  assert.equal(projectPathKey("C:\\Work\\App\\"), projectPathKey("c:/work/app"));
});

test("project path containment keeps segment boundaries and POSIX case sensitivity", () => {
  assert.equal(isProjectPathWithin("/work/app", "/work/app"), true);
  assert.equal(isProjectPathWithin("/work/app/api", "/work/app/"), true);
  assert.equal(isProjectPathWithin("/work/application", "/work/app"), false);
  assert.equal(isProjectPathWithin("C:\\work\\application", "C:\\work\\app"), false);
  assert.equal(isProjectPathWithin("/Work/App/api", "/work/app"), false);
  assert.equal(isProjectPathWithin("/work/app/../secret", "/work/app"), false);
  assert.equal(isProjectPathWithin("C:\\work\\app\\..\\secret", "C:\\work\\app"), false);
  assert.equal(isProjectPathWithin("/etc", "/"), true);
});

test("worktree inspector and AI bind-workspace use normalized project path containment", () => {
  const open = view.match(/async function openWorktreeInspector[\s\S]*?\n}\n/)?.[0];
  assert.ok(open);
  assert.match(open, /options\.filter\(\(entry\) => isProjectPathWithin\(entry\.path, root\)\)/);
  assert.match(open, /projectPathKey\(entry\.path\) === rootKey/);
  assert.doesNotMatch(open, /entry\.path\.startsWith\(`\$\{root\}\/`\)/);

  const bind = view.match(/case "bind-workspace": \{[\s\S]*?\n    \}\n/)?.[0];
  assert.ok(bind);
  assert.match(bind, /project\.projects\.some\(\(entry\) => isProjectPathWithin\(requested, entry\.root\)\)/);
  assert.doesNotMatch(bind, /requested\.startsWith\(/);
});
