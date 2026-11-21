import assert from "node:assert/strict";
import test from "node:test";
import { globToRegExp, matchesFilenamePattern } from "../src/domain/files/filename-glob.ts";

test("globToRegExp: * stays in one segment, ** crosses segments, ? is one character", () => {
  assert.ok(globToRegExp("*.drawio").test("diagram.drawio"));
  assert.ok(!globToRegExp("*.drawio").test("docs/diagram.drawio"));
  assert.ok(globToRegExp("**/*.drawio").test("docs/a/diagram.drawio"));
  assert.ok(globToRegExp("file?.txt").test("file1.txt"));
  assert.ok(!globToRegExp("file?.txt").test("file/.txt"));
  assert.ok(globToRegExp("*.DRAWIO").test("x.drawio"), "case-insensitive");
});

test("globToRegExp escapes regex metacharacters literally", () => {
  assert.ok(globToRegExp("a+b(c).[x]{1}|$^").test("a+b(c).[x]{1}|$^"));
  assert.ok(!globToRegExp("a.b").test("axb"));
});

test("matchesFilenamePattern tests the basename unless the pattern has a slash", () => {
  assert.ok(matchesFilenamePattern("*.proto", "src/api/x.proto", "x.proto"));
  assert.ok(!matchesFilenamePattern("src/*.proto", "lib/x.proto", "x.proto"));
  assert.ok(matchesFilenamePattern("src/*.proto", "src/x.proto", "x.proto"));
  assert.ok(matchesFilenamePattern("src\\*.proto", "src/x.proto", "x.proto"), "backslashes are normalized");
});
