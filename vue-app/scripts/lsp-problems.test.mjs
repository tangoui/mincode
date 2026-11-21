import assert from "node:assert/strict";
import test from "node:test";
import {
  MAX_PROBLEM_FILES,
  MAX_PROBLEMS_PER_FILE,
  clearServerProblems,
  createProblemRegistry,
  fileUriPath,
  problemPath,
  problemsFor,
  setProblems,
  summarizeProblems,
} from "../src/domain/projects/lsp-problems.ts";

const at = (line, character = 0) => ({ start: { line, character }, end: { line, character: character + 1 } });

test("problems merge servers per file, count severities and sort errors first", () => {
  const registry = createProblemRegistry();
  assert.equal(setProblems(registry, "/repo", "gopls", "file:///repo/b.go", [
    { range: at(9), message: "warn", severity: 2 },
    { range: at(2, 4), message: "err", severity: 1, source: "compiler", code: 7 },
  ]), true);
  setProblems(registry, "/repo", "golangci", "file:///repo/b.go", [{ range: at(1), message: "hint", severity: 4 }]);
  setProblems(registry, "/repo", "gopls", "file:///repo/a.go", [{ range: at(0), message: "no severity" }]);
  setProblems(registry, "/other", "gopls", "file:///other/x.go", [{ range: at(0), message: "elsewhere", severity: 1 }]);

  const summary = summarizeProblems(registry, "/repo");
  assert.deepEqual([summary.errors, summary.warnings, summary.others, summary.truncated], [1, 2, 1, false]);
  assert.deepEqual(summary.files.map((file) => file.path), ["b.go", "a.go"]);
  assert.deepEqual(summary.files[0].entries.map((entry) => [entry.severity, entry.line, entry.serverId]), [
    [1, 2, "gopls"], [2, 9, "gopls"], [4, 1, "golangci"],
  ]);
  assert.deepEqual(summary.files[0].entries[0], {
    serverId: "gopls", severity: 1, message: "err", source: "compiler", code: "7", line: 2, character: 4,
  });
  // A missing severity counts as a warning, like the editor markers.
  assert.equal(summary.files[1].warnings, 1);
});

test("empty reports and stopped servers remove entries without touching other servers or roots", () => {
  const registry = createProblemRegistry();
  setProblems(registry, "/repo", "a", "file:///repo/x.ts", [{ range: at(0), message: "a" }]);
  setProblems(registry, "/repo", "b", "file:///repo/x.ts", [{ range: at(0), message: "b" }]);
  assert.equal(setProblems(registry, "/repo", "c", "file:///repo/x.ts", []), false);
  assert.equal(setProblems(registry, "/repo", "a", "file:///repo/x.ts", []), true);
  assert.deepEqual(problemsFor(registry, "/repo", "a", "file:///repo/x.ts"), []);
  assert.equal(problemsFor(registry, "/repo", "b", "file:///repo/x.ts").length, 1);
  assert.equal(clearServerProblems(registry, "/repo", "b"), true);
  assert.equal(clearServerProblems(registry, "/repo", "b"), false);
  assert.equal(registry.roots.size, 0);
});

test("files and diagnostics per file are bounded and truncation is reported", () => {
  const registry = createProblemRegistry();
  const many = Array.from({ length: MAX_PROBLEMS_PER_FILE + 10 }, (_, line) => ({ range: at(line), message: "x", severity: 1 }));
  setProblems(registry, "/repo", "s", "file:///repo/big.ts", many);
  assert.equal(problemsFor(registry, "/repo", "s", "file:///repo/big.ts").length, MAX_PROBLEMS_PER_FILE);
  for (let index = 1; index < MAX_PROBLEM_FILES; index++) {
    setProblems(registry, "/repo", "s", `file:///repo/${index}.ts`, [{ range: at(0), message: "x" }]);
  }
  assert.equal(setProblems(registry, "/repo", "s", "file:///repo/overflow.ts", [{ range: at(0), message: "x" }]), false);
  // An existing file can still be updated at the cap.
  assert.equal(setProblems(registry, "/repo", "t", "file:///repo/1.ts", [{ range: at(0), message: "y" }]), true);
  const summary = summarizeProblems(registry, "/repo");
  assert.equal(summary.files.length, MAX_PROBLEM_FILES);
  assert.equal(summary.truncated, true);
});

test("display paths are root-relative, decoded and handle Windows drives", () => {
  assert.equal(problemPath("file:///repo/src/a%20b.ts", "/repo"), "src/a b.ts");
  assert.equal(problemPath("file:///repo-other/a.ts", "/repo"), "/repo-other/a.ts");
  assert.equal(problemPath("file:///c%3A/Repo/src/x.ts", "C:\\repo"), "src/x.ts");
  assert.equal(problemPath("not a uri", "/repo"), "not a uri");
});

test("fileUriPath decodes file URIs and rejects other schemes", () => {
  assert.equal(fileUriPath("file:///repo/src/a%20b.go"), "/repo/src/a b.go");
  assert.equal(fileUriPath("file:///usr/local/go/src/fmt/print.go"), "/usr/local/go/src/fmt/print.go");
  assert.equal(fileUriPath("file:///c%3A/Repo/main.go"), "c:/Repo/main.go");
  assert.equal(fileUriPath("file:///C:/Repo/%E4%B8%AD%E6%96%87.go"), "C:/Repo/中文.go");
  assert.equal(fileUriPath("https://example.com/a.go"), undefined);
  assert.equal(fileUriPath("inmemory://model/1"), undefined);
  assert.equal(fileUriPath("not a uri"), undefined);
});
