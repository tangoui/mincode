import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const overviewSource = await readFile(
  new URL("../src/components/database/DatabaseConnectionOverview.vue", import.meta.url),
  "utf8",
);
const workspaceSource = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8",
);

test("adds a fixed database connection overview tab", () => {
  assert.match(workspaceSource, /DATABASE_CONNECTION_OVERVIEW_TAB = "__connection-overview__"/);
  assert.match(workspaceSource, /<el-tab-pane :name="DATABASE_CONNECTION_OVERVIEW_TAB">/);
  assert.match(workspaceSource, /database\.connectionOverview/);
  assert.match(workspaceSource, /v-if="activeDatabaseConnectionTab === DATABASE_CONNECTION_OVERVIEW_TAB"/);
});

test("reuses explicit database connection actions without background probing", () => {
  assert.match(workspaceSource, /@connect="connectFromOverview"/);
  assert.match(workspaceSource, /@open="activateConnection"/);
  assert.match(workspaceSource, /async function connectFromOverview\(connection: DatabaseConnection\)/);
  assert.doesNotMatch(overviewSource, /setTimeout|testConnection|refreshSchema/);
  // The only timer is the one-minute clock for relative times; it never probes connections.
  assert.equal(overviewSource.match(/setInterval\(/g)?.length, 1);
  assert.match(overviewSource, /setInterval\(\(\) => \{\s*now\.value = Date\.now\(\);\s*\}, CLOCK_INTERVAL_MS\)/);
});

test("shows local connection metadata and existing session state", () => {
  assert.match(overviewSource, /connectedConnectionIds: string\[\]/);
  assert.match(overviewSource, /connectingConnectionIds: Record<string, boolean>/);
  assert.match(overviewSource, /connection\.lastConnectedAt/);
  // Tags stay searchable but the table has no tag column (design ③).
  assert.match(overviewSource, /\.\.\.\(connection\.tags \?\? \[\]\)/);
  assert.doesNotMatch(overviewSource, /connection\.tags\?\.\[0\]|db-connection-overview__tag\b/);
  assert.match(overviewSource, /props\.schemas\[connection\.id\]\?\.databases\.length/);
});

test("returns to overview when closing a connected database tab", () => {
  assert.match(
    workspaceSource,
    /function removeDatabaseConnectionTab[\s\S]*?selectedDatabaseConnectionTab\.value === connection\.id[\s\S]*?selectedDatabaseConnectionTab\.value = DATABASE_CONNECTION_OVERVIEW_TAB;[\s\S]*?disconnectConnection\(connection\)/,
  );
});

test("falls back to overview after the selected session expires", () => {
  assert.match(
    workspaceSource,
    /connectedConnections\.value\.some\(\(connection\) => connection\.id === selectedDatabaseConnectionTab\.value\)[\s\S]*?DATABASE_CONNECTION_OVERVIEW_TAB/,
  );
});

test("uses Element Plus scrollbar instead of native overview scrolling", () => {
  assert.match(overviewSource, /<el-scrollbar class="db-connection-overview__scroll">/);
  assert.match(overviewSource, /class="db-connection-overview__content"/);
  assert.doesNotMatch(overviewSource, /\.db-connection-overview \{[\s\S]*?overflow: auto;/);
});


test("lays the page out around quick connect with the shared overview pieces", () => {
  assert.match(overviewSource, /<ConnectionRecentStrip/);
  assert.match(overviewSource, /connection-overview__table/);
  assert.match(overviewSource, /database\.overviewSummary/);
  assert.doesNotMatch(overviewSource, /db-connection-overview__stat/);
  assert.match(overviewSource, /"import-config": \[\]/);
  assert.match(workspaceSource, /<DatabaseConnectionOverview[^>]*@import-config="importConnectionConfig"/);
});

test("keeps a narrow-window breakpoint for the page", () => {
  assert.match(overviewSource, /@media \(max-width: 680px\)/);
});

test("both overviews share the search keyboard and focus handling", async () => {
  const sshSource = await readFile(new URL("../src/components/ssh/SshServerOverview.vue", import.meta.url), "utf8");
  for (const source of [overviewSource, sshSource]) {
    assert.match(
      source,
      /import \{[^}]*\buseOverviewSearchKeyboard\b[^}]*\} from "@\/composables\/connections\/useOverviewSearchKeyboard"/,
    );
    assert.doesNotMatch(source, /function (dialogIsOpen|focusSearch|scrollHighlightIntoView)\(/);
  }
});

test("tree actions that open content on the right leave the connection overview", () => {
  assert.match(
    workspaceSource,
    /async function showConnectionContent\(connection: DatabaseConnection\)[\s\S]*?ensureConnectionActive\(connection\)[\s\S]*?selectedDatabaseConnectionTab\.value = connection\.id;/,
  );
  for (const fn of [
    "toggleDatabaseExpansion",
    "selectConnectionTable",
    "openExternalSqlFile",
    "openSchemaNewQuery",
    "openNewTableTemplate",
    "openTableDetailsFromTree",
    "openTableForInlineSearch",
  ]) {
    assert.match(
      workspaceSource,
      // 只在该函数体内匹配，不跨到下一个函数
      new RegExp(`async function ${fn}\\((?:(?!\\n(?:async )?function )[\\s\\S])*?await showConnectionContent\\(connection\\)`),
      fn,
    );
  }
  assert.match(workspaceSource, /case "new-query":\s*if \(await showConnectionContent\(connection\)\) newQuery\(\);/);
});

test("keeps the markup the desktop E2E database scenarios click", async () => {
  // src/services/e2e-database-overview.ts finds a saved connection's row by name and
  // clicks its action; the smoke only runs in a native build, so guard it here.
  const helper = await readFile(new URL("../src/services/e2e-database-overview.ts", import.meta.url), "utf8");
  for (const className of [
    "db-connection-overview__connections",
    "connection-overview__row",
    "connection-overview__name",
    "db-connection-overview__action",
  ]) {
    assert.match(helper, new RegExp(`\\.${className}\\b`), `helper uses .${className}`);
    assert.match(overviewSource, new RegExp(`class="[^"]*\\b${className}\\b`), `overview renders .${className}`);
  }
  for (const driver of ["e2e-database-split.ts", "e2e-database-object-toolbar.ts"]) {
    const source = await readFile(new URL(`../src/services/${driver}`, import.meta.url), "utf8");
    assert.doesNotMatch(source, /db-connection-card/, `${driver} must not use the removed card`);
    assert.match(source, /overviewConnectAction\(/, `${driver} uses the shared helper`);
  }
});
