import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const viewSource = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8",
);
const styles = await readFile(
  new URL("../src/styles/workspaces/database/workspace.css", import.meta.url),
  "utf8",
);

test("database workspace detects dropped file type before saving and opening it", () => {
  assert.match(viewSource, /data-file-drop-target/);
  assert.match(viewSource, /pendingDatabaseFiles/);
  assert.match(viewSource, /invokeBackend<DetectedFileDatabase>\("db_detect_file_type"/);
  assert.match(viewSource, /await databaseStore\.saveConnection\(connection\)/);
  assert.match(viewSource, /await activateConnection\(connection\)/);
  assert.match(viewSource, /fileDatabaseConnectionName[\s\S]*?duckdb\|ddb\|db/);
});

test("database workspace provides native drag hover feedback", () => {
  assert.match(styles, /\.db-workspace\.file-drop-target-active::before/);
  assert.match(styles, /content:\s*attr\(data-file-drop-label\)/);
});
