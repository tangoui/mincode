import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const objectBrowserSource = await readFile(
  new URL("../src/components/database/DatabaseObjectBrowser.vue", import.meta.url),
  "utf8",
);
const ddlViewerSource = await readFile(
  new URL("../src/components/ui/ReadonlyCodeViewer.vue", import.meta.url),
  "utf8",
);
const workspaceSource = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8",
);

test("keeps the virtual object table mounted when opening and closing details", () => {
  assert.match(objectBrowserSource, /<AppSplitPane[\s\S]*?:collapsed="!selectedObject"/);
  assert.doesNotMatch(objectBrowserSource, /<AppSplitPane\s+v-if="selectedObject"/);
  assert.equal((objectBrowserSource.match(/<DatabaseObjectTable\b/g) ?? []).length, 1);
  assert.match(objectBrowserSource, /<ReadonlyCodeViewer\s+v-if="details\?\.ddl"/);
  assert.doesNotMatch(ddlViewerSource, /new ResizeObserver/);
  assert.match(ddlViewerSource, /automaticLayout: true/);
});

test("offers databases from the active connection in the overview toolbar", () => {
  assert.match(objectBrowserSource, /databases: string\[\]/);
  assert.match(objectBrowserSource, /v-for="databaseName in databases"/);
  assert.match(objectBrowserSource, /@change="emit\('switchDatabase', \$event\)"/);
  assert.match(objectBrowserSource, /:aria-label="t\('database\.switchDatabase'\)"/);
});

test("uses the same per-connection schema snapshot as the database tree", () => {
  assert.match(
    workspaceSource,
    /const overviewSchema = computed\(\(\) => \{[\s\S]*?activeConnection\.value\?\.id;[\s\S]*?databaseStore\.schemas\[connectionId\]/,
  );
  assert.match(
    workspaceSource,
    /overviewSchema\.value\?\.databases\.map\(\(database\) => database\.name\)/,
  );
  assert.match(
    workspaceSource,
    /overviewSchema\.value\?\.databases\.find\(/,
  );
});

test("routes overview database changes through the existing overview transition", () => {
  assert.match(workspaceSource, /:databases="overviewDatabases"/);
  assert.match(workspaceSource, /@switch-database="switchOverviewDatabase"/);
  assert.match(
    workspaceSource,
    /function switchOverviewDatabase\(database: string\)[\s\S]*?overviewDatabases\.value\.includes\(database\)[\s\S]*?openDatabaseOverview\(connection, database, false\);/,
  );
  assert.match(
    workspaceSource,
    /function openDatabaseOverview\([\s\S]*?overviewSelectedObjectName\.value = "";[\s\S]*?overviewDetails\.value = undefined;/,
  );
});
