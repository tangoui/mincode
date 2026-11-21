import assert from "node:assert/strict";
import test from "node:test";
import {
  isSystemPackagePath,
  resolveLocalMarkdownTarget,
} from "../src/domain/markdown-local-paths.ts";

const projects = [
  { root: "/Volumes/work/min", name: "min" },
  { root: "/Volumes/work/min/packages/app", name: "app" },
];

test("relative markdown paths resolve inside the active project", () => {
  assert.deepEqual(resolveLocalMarkdownTarget("./README.md", projects, projects[0].root), {
    projectRoot: "/Volumes/work/min",
    relativePath: "README.md",
    absolutePath: "/Volumes/work/min/README.md",
  });
  assert.deepEqual(resolveLocalMarkdownTarget("app/bin/mincode.dmg", projects, projects[0].root), {
    projectRoot: "/Volumes/work/min",
    relativePath: "app/bin/mincode.dmg",
    absolutePath: "/Volumes/work/min/app/bin/mincode.dmg",
  });
});

test("absolute and file URL paths use the longest matching saved project root", () => {
  assert.deepEqual(
    resolveLocalMarkdownTarget("/Volumes/work/min/packages/app/main.ts", projects, projects[0].root),
    {
      projectRoot: "/Volumes/work/min/packages/app",
      relativePath: "main.ts",
      absolutePath: "/Volumes/work/min/packages/app/main.ts",
    },
  );
  assert.deepEqual(
    resolveLocalMarkdownTarget("file:///Volumes/work/min/docs/a%20b.md", projects, projects[0].root),
    {
      projectRoot: "/Volumes/work/min",
      relativePath: "docs/a b.md",
      absolutePath: "/Volumes/work/min/docs/a b.md",
    },
  );
});

test("paths outside saved projects and relative traversal are rejected", () => {
  assert.equal(resolveLocalMarkdownTarget("/etc/passwd", projects, projects[0].root), null);
  assert.equal(resolveLocalMarkdownTarget("/Volumes/work/min/../secret.txt", projects, projects[0].root), null);
  assert.equal(resolveLocalMarkdownTarget("../secret.txt", projects, projects[0].root), null);
  assert.equal(resolveLocalMarkdownTarget("README.md", projects, "/missing"), null);
});

test("system package directories are delegated to the file manager", () => {
  assert.equal(isSystemPackagePath("app/bin/mincode.app"), true);
  assert.equal(isSystemPackagePath("plugins/example.bundle/"), true);
  assert.equal(isSystemPackagePath("src/App.vue"), false);
  assert.equal(isSystemPackagePath("app/bin/mincode.dmg"), false);
});
