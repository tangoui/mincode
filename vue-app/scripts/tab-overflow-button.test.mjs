import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const foundation = await readFile(new URL("../src/styles/foundation/foundation.css", import.meta.url), "utf8");
const redis = await readFile(new URL("../src/styles/workspaces/database/redis-workspace.css", import.meta.url), "utf8");

// Element Plus's own .el-button height (same specificity) used to win, leaving a 32px
// button pinned to the top of the tab bar with the dots and hover fill off-centre.
test("tab overflow button fills the tab bar height and centres its dots", () => {
  assert.match(foundation, /\.app-top-tabs > \.el-dropdown \{[^}]*align-self: stretch;[^}]*align-items: stretch;/);
  assert.match(
    foundation,
    /\.app-top-tabs \.el-button\.app-tabs-overflow \{[^}]*height: 100%;[^}]*margin: 0;[^}]*padding: 0;[^}]*border-radius: 0;/,
  );
  assert.doesNotMatch(redis, /\.app-tabs-overflow/, "tab bar styles live with the tab bar, not the Redis workspace");
});

// .app-inline-rename draws an accent border on the root; on an el-input that stacks
// on the wrapper's own outline (double border in the "more tabs" rename field).
test("inline rename el-inputs keep only Element Plus's own outline", () => {
  assert.match(foundation, /\.el-input\.app-inline-rename \{[^}]*border: 0;[^}]*background: transparent;/);
});
