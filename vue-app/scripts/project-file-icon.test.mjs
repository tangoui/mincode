import assert from "node:assert/strict";
import test from "node:test";
import { resolveProjectFileIconKind } from "../src/domain/projects/file-icon.ts";

test("uses folder icons before inspecting the name", () => {
  assert.equal(resolveProjectFileIconKind("src.ts", true), "folder");
});

test("recognizes common project file types", () => {
  assert.equal(resolveProjectFileIconKind("App.vue"), "vue");
  assert.equal(resolveProjectFileIconKind("main.ts"), "typescript");
  assert.equal(resolveProjectFileIconKind("index.js"), "javascript");
  assert.equal(resolveProjectFileIconKind("styles.scss"), "style");
  assert.equal(resolveProjectFileIconKind("README.md"), "markdown");
  assert.equal(resolveProjectFileIconKind("schema.sql"), "database");
  assert.equal(resolveProjectFileIconKind("logo.svg"), "image");
});

test("recognizes extensionless and dotfile configuration names", () => {
  assert.equal(resolveProjectFileIconKind("Dockerfile.dev"), "terminal");
  assert.equal(resolveProjectFileIconKind(".env.local"), "config");
  assert.equal(resolveProjectFileIconKind("tsconfig.json"), "config");
  assert.equal(resolveProjectFileIconKind("package.json"), "archive");
});

test("falls back to a generic document icon", () => {
  assert.equal(resolveProjectFileIconKind("LICENSE"), "document");
  assert.equal(resolveProjectFileIconKind("unknown.custom"), "document");
});
