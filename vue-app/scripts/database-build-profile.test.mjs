import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const dialogSource = await readFile(
  new URL("../src/components/database/DatabaseConnectionDialog.vue", import.meta.url),
  "utf8",
);
const capabilitySource = await readFile(
  new URL("../src/services/database-capabilities.ts", import.meta.url),
  "utf8",
);

for (const token of [
  "loadSupportedDatabaseTypes",
  "supportedDatabaseTypes",
  "availableCategories",
  'in availableCategories"',
]) {
  test(`database connection picker scopes options with ${token}`, () => {
    assert.match(dialogSource, new RegExp(token.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  });
}

test("database build capabilities come from the active backend", () => {
  assert.match(capabilitySource, /invokeBackend<DbType\[\]>\("db_supported_types"\)/);
  assert.match(capabilitySource, /\.catch\(\(\) => null\)/);
});
