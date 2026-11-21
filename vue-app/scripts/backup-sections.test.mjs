// Backup section mapping: the TS table must match the Go one; both read the
// shared expectations in backend/backup/testdata/sections.json.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  BACKUP_CORE_SECTIONS,
  BACKUP_SECTION_AI,
  BACKUP_SECTION_DATABASE,
  BACKUP_SECTION_SETTINGS,
  BACKUP_SECTION_SSH,
  BACKUP_SECTION_WORKSPACES,
  pluginSectionId,
  sectionForKey,
} from "../src/domain/backup/sections.ts";

const tableURL = new URL("../../backend/backup/testdata/sections.json", import.meta.url);

test("sectionForKey matches shared table", async () => {
  const table = JSON.parse(await readFile(tableURL, "utf8"));
  const entries = Object.entries(table);
  assert.ok(entries.length >= 14, `table has ${entries.length} keys`);
  for (const [key, want] of entries) {
    assert.equal(sectionForKey(key), want, key);
  }
});

test("core sections keep display order", () => {
  assert.deepEqual(BACKUP_CORE_SECTIONS, [
    BACKUP_SECTION_SETTINGS,
    BACKUP_SECTION_SSH,
    BACKUP_SECTION_DATABASE,
    BACKUP_SECTION_WORKSPACES,
    BACKUP_SECTION_AI,
  ]);
  assert.deepEqual(BACKUP_CORE_SECTIONS, ["settings", "ssh", "database", "workspaces", "ai"]);
});

test("pluginSectionId prefixes the plugin id and rejects empty ids", () => {
  assert.equal(pluginSectionId("http-tools"), "plugin:http-tools");
  assert.throws(() => pluginSectionId(""));
});
