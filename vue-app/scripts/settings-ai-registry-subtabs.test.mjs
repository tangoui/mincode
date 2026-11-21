import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const viewSource = await readFile(
  new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url),
  "utf8",
);
const zhLocale = await readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8");
const enLocale = await readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8");

function panelSource(startMarker, endMarker) {
  const start = viewSource.indexOf(startMarker);
  assert.ok(start >= 0, `missing panel marker ${startMarker}`);
  const end = viewSource.indexOf(endMarker, start);
  assert.ok(end > start, `missing panel end marker ${endMarker}`);
  return viewSource.slice(start, end);
}

test("connector panel keeps installed servers and discovery in separate sub tabs", () => {
  const panel = panelSource("aiTab === 'mcp'", "aiTab === 'skills'");

  assert.match(panel, /v-model="mcpSubTab"/);
  const installedGuard = panel.indexOf(`v-if="mcpSubTab === 'installed'"`);
  assert.notEqual(installedGuard, -1, "missing installed sub tab branch");
  const discoverBranch = panel.indexOf("<template v-else>", installedGuard);
  assert.notEqual(discoverBranch, -1, "missing discovery sub tab branch");

  const installedList = panel.indexOf("settings-ai-installed");
  assert.ok(
    installedList > installedGuard && installedList < discoverBranch,
    "installed server list must render only in the installed sub tab",
  );
  const searchCard = panel.indexOf("settings.mcp.searchInstall");
  assert.ok(
    searchCard > discoverBranch,
    "registry search must render only in the discovery sub tab",
  );
});

test("skills panel keeps installed skills and discovery in separate sub tabs", () => {
  const panel = panelSource("aiTab === 'skills'", "aiTab === 'rules'");

  assert.match(panel, /v-model="skillSubTab"/);
  const installedGuard = panel.indexOf(`v-if="skillSubTab === 'installed'"`);
  assert.notEqual(installedGuard, -1, "missing installed sub tab branch");
  const discoverBranch = panel.indexOf("<template v-else>", installedGuard);
  assert.notEqual(discoverBranch, -1, "missing discovery sub tab branch");

  // Installed skills render as a card grid since c6f9c9be.
  const installedList = panel.indexOf(`v-for="skill in settings.aiSkills"`);
  assert.ok(
    installedList > installedGuard && panel.lastIndexOf("settings-ai-card-grid", installedList) > installedGuard,
    "installed skill cards must be inside the installed sub tab",
  );
  assert.ok(
    installedList > installedGuard && installedList < discoverBranch,
    "installed skill list must render only in the installed sub tab",
  );
  const searchLine = panel.indexOf("settings.skills.searchPlaceholder");
  assert.ok(
    searchLine > discoverBranch,
    "skill registry search must render only in the discovery sub tab",
  );
});

test("installed/discover sub tab labels exist in both locales", () => {
  for (const locale of [zhLocale, enLocale]) {
    assert.match(locale, /"settings\.ai\.subtabInstalled":/);
    assert.match(locale, /"settings\.ai\.subtabDiscover":/);
  }
});
