import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = (path) => readFile(new URL(path, import.meta.url), "utf8");

test("database navigator starts directly with connection groups", async () => {
  const component = await source("../src/components/database/sidebar/DatabaseConnectionSidebar.vue");
  assert.doesNotMatch(component, /class="db-navigator-section-title"[\s\S]*?database\.dataSources/);
  assert.match(component, /v-for="group in connectionGroups"/);
});

const cssRule = (css, selector) => {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return [...css.matchAll(new RegExp(`(?:^|\\n|\\})\\s*${escaped}\\s*\\{([^}]*)\\}`, "g"))].map((match) => match[1]);
};

test("connection groups are flat titles with a colored bar instead of tinted boxes", async () => {
  const [databaseCss, sshCss, sshView] = await Promise.all([
    source("../src/styles/workspaces/database/navigator.css"),
    source("../src/styles/workspaces/ssh/workspace.css"),
    source("../src/views/SshWorkspaceView.vue"),
  ]);
  const headerRules = cssRule(databaseCss, ".db-connection-group__header");
  assert.ok(headerRules.length > 0, "group header rule exists");
  for (const body of headerRules) {
    for (const [, value] of body.matchAll(/(?:^|;)\s*(?:background|border):\s*([^;]+)/g)) {
      assert.match(value.trim(), /^(?:0|none|transparent)$/, `group header keeps a flat ${value}`);
    }
  }
  assert.ok(cssRule(databaseCss, ".db-connection-group__bar").length > 0, "group color bar rule exists");
  for (const body of cssRule(databaseCss, ".db-connection-group__children")) {
    assert.doesNotMatch(body, /border-left:\s*[^;]*solid/);
  }
  assert.doesNotMatch(sshCss, /\.db-connection-group__(?:header|title|children)[^{]*\{[^}]*--app-success/);
  assert.match(sshView, /class="db-connection-group__bar"[^>]*avatarColor\(group\.key \|\| ['"]ungrouped['"]\)/);
  assert.match(sshView, /<small>\{\{ group\.profiles\.length \}\}<\/small>/);
});

test("database sidebar groups use the same colored bar, count and trailing chevron as SSH", async () => {
  const component = await source("../src/components/database/sidebar/DatabaseConnectionSidebar.vue");
  const header = component.match(/<div class="db-connection-group__header">[\s\S]*?<\/button>/)?.[0] ?? "";
  assert.match(header, /class="db-connection-group__bar"[^>]*avatarColor\(group\.key \|\| ['"]ungrouped['"]\)/);
  assert.match(header, /<small>\{\{ group\.connections\.length \}\}<\/small>\s*<AppChevron/);
  assert.doesNotMatch(header, /<Folder \/>|db-connection-group__folder/);
  assert.match(header, /@dragstart="onConnectionGroupDragStart\(group\.key, \$event\)"/);
  assert.match(component, /@command="emit\('rename-group', group\.name\)"/);
  assert.match(component, /import \{ avatarColor \} from "@\/domain\/connections\/overview"/);
});

test("navigator toolbar uses compact borderless icon buttons beside a title", async () => {
  const [databaseCss, sshView] = await Promise.all([
    source("../src/styles/workspaces/database/navigator.css"),
    source("../src/views/SshWorkspaceView.vue"),
  ]);
  const [button] = cssRule(databaseCss, ".db-explorer__toolbar .db-square-action.el-button");
  assert.ok(button, "toolbar button rule exists");
  assert.match(button, /width:\s*26px/);
  assert.match(button, /height:\s*26px/);
  assert.match(button, /border:\s*0/);
  assert.match(sshView, /<strong class="db-explorer__title">\{\{ t\("ssh\.manager\.title"\) \}\}<\/strong>/);
});

test("SSH sidebar shows export and batch command inline, mirrors them in the more menu and shows a footer", async () => {
  const sshView = await source("../src/views/SshWorkspaceView.vue");
  const toolbar = sshView.match(/<div class="db-explorer__toolbar">[\s\S]*?\n      <\/div>/)?.[0] ?? "";
  const more = toolbar.match(/<el-dropdown class="db-explorer__more"[\s\S]*?<\/el-dropdown>/)?.[0] ?? "";
  const inline = toolbar.replace(more, "");
  // Every action is inline by default; export and batch command are the secondary ones that fold when narrow.
  assert.match(inline, /class="db-square-action db-explorer__secondary"[^>]*:disabled="!connections\.profiles\.length"[^>]*@click="exportProfiles"/);
  assert.match(inline, /class="db-square-action db-explorer__secondary"[^>]*:disabled="!batchEligibleProfiles\.length"[^>]*@click="openBatchCommand"/);
  assert.match(inline, /@click="knownHostsVisible = true"/);
  assert.match(inline, /@click="chooseImport"[\s\S]*?configSyncVisible = true/);
  assert.match(inline, /class="db-square-action is-primary"[^>]*@click="newConnection"/);
  // The overflow menu carries the same secondary actions with the same disabled conditions.
  assert.match(more, /:disabled="!connections\.profiles\.length" @click="exportProfiles"/);
  assert.match(more, /:disabled="!batchEligibleProfiles\.length" @click="openBatchCommand"/);
  assert.match(sshView, /<footer class="db-explorer__footer">[\s\S]*?ssh\.manager\.footerSummary[\s\S]*?configSyncVisible = true[\s\S]*?<\/footer>/);
  assert.match(sshView, /id="ssh-profile-import"/);
  assert.match(sshView, /<ConnectionAvatar[\s\S]*?:seed="profile\.id"[\s\S]*?:size="26"/);
});

test("navigator toolbar folds secondary actions into the more menu only when the sidebar is narrow", async () => {
  const databaseCss = await source("../src/styles/workspaces/database/navigator.css");
  const [toolbar] = cssRule(databaseCss, ".db-explorer__toolbar");
  assert.match(toolbar, /container:\s*db-explorer-toolbar\s*\/\s*inline-size/);
  const [moreDefault] = cssRule(databaseCss, ".db-explorer__toolbar > .db-explorer__more");
  assert.match(moreDefault, /display:\s*none/);
  const query = databaseCss.match(/@container db-explorer-toolbar \(max-width: \d+px\) \{([\s\S]*?)\n\}/)?.[1] ?? "";
  assert.ok(query, "toolbar container query exists");
  assert.match(query, /\.db-explorer__toolbar > \.db-explorer__secondary\.el-button\s*\{[^}]*display:\s*none/);
  assert.match(query, /\.db-explorer__toolbar > \.db-explorer__more\s*\{[^}]*display:\s*inline-flex/);
});

test("the primary new button keeps the soft accent style", async () => {
  const databaseCss = await source("../src/styles/workspaces/database/navigator.css");
  const [primary] = cssRule(databaseCss, ".db-explorer__toolbar .db-square-action.is-primary.el-button");
  assert.ok(primary, "primary toolbar button rule exists");
  assert.match(primary, /border:\s*1px solid color-mix\(in srgb, var\(--app-accent\) 35%, var\(--app-surface\)\)/);
  assert.match(primary, /background:\s*var\(--app-accent-soft\)/);
  assert.match(primary, /color:\s*var\(--app-accent\)/);
  assert.doesNotMatch(databaseCss, /is-primary[^{]*\{[^}]*--app-text-on-accent/);
  assert.doesNotMatch(databaseCss, /is-primary[^{]*\{[^}]*background:\s*var\(--app-accent\)\s*;/);
});

test("SSH sidebar reuses the database navigator layout instead of its own", async () => {
  const [sshView, sshCss] = await Promise.all([
    source("../src/views/SshWorkspaceView.vue"),
    source("../src/styles/workspaces/ssh/workspace.css"),
  ]);
  for (const cls of ["db-explorer__toolbar", "db-square-action", "db-search-row", "db-connection-filter-panel",
    "db-tree-scroll", "db-connection-navigator", "db-connection-group__title", "db-connection-group__children",
    "db-connection-entry", "db-connection-node__main", "db-connection-node__meta", "db-connection-node__status",
    "db-connection-node__menu", "db-explorer__more", "db-explorer__footer"]) {
    assert.match(sshView, new RegExp(`class="[^"]*\\b${cls}\\b`), cls);
  }
  assert.match(sshView, /<ConnectionAvatar\b/);
  assert.doesNotMatch(sshView, /ssh-connection-card|ssh-manager-toolbar|ssh-manager-search/);
  assert.doesNotMatch(sshCss, /\.ssh-connection-card|\.ssh-manager-toolbar|\.ssh-manager-search|\.ssh-connection-group__/);
});
