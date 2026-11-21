import "./ts-resolve.mjs";
import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";
import { computed } from "vue";

// Runtime language pack changes (enable, upgrade swap, disable): titles and
// Intl formatting re-render from the reactive overlay.
const { setLocaleOverlay, useVueI18n } = await import("../src/i18n/messages.ts");
const { localizedPluginTitle, pluginCommandTitle, setPluginCommandTitles, setPluginTitleOverlay, setPluginViewTitles } =
  await import("../src/services/plugin-titles.ts");
const { pluginThemeTitle } = await import("../src/domain/themes/plugin-theme.ts");
const { fileIconThemeTitle } = await import("../src/domain/projects/file-icon-theme.ts");
const { menuItemTitle } = await import("../src/domain/plugins/menus.ts");

const source = (path) => readFile(new URL(`../src/${path}`, import.meta.url), "utf8");

function applyPack(tag, titles) {
  setLocaleOverlay({ tag, pluginId: "pack", messages: new Map() });
  setPluginTitleOverlay(titles ? new Map(Object.entries(titles)) : null);
}

function removePack() {
  setLocaleOverlay(null);
  setPluginTitleOverlay(null);
}

test("titles follow a pack being enabled, upgraded and disabled", () => {
  const view = { pluginId: "acme", id: "board", title: "Board", titleLocalized: { zh: "看板", ja: "ボード" } };
  const other = { pluginId: "acme", id: "log", title: "Log", titleLocalized: { zh: "日志" } };
  const command = { pluginId: "acme", id: "sync", title: "Sync", titleLocalized: { zh: "同步" } };
  setPluginViewTitles([view, other]);
  setPluginCommandTitles([command]);
  const viewTitle = computed(() => localizedPluginTitle(view));
  const otherTitle = computed(() => localizedPluginTitle(other));
  const commandTitle = computed(() => pluginCommandTitle("plugin:acme:sync"));
  try {
    assert.equal(otherTitle.value, "日志");
    applyPack("ja", { acme: { views: { board: "パック", log: "ログ" }, commands: { sync: "同期" } } });
    assert.equal(viewTitle.value, "ボード", "the plugin's own titleLocalized wins over the pack");
    assert.equal(otherTitle.value, "ログ");
    assert.equal(commandTitle.value, "同期");
    // Upgrade: a new pluginTitles asset is swapped in.
    applyPack("ja", { acme: { views: { log: "ログ v2" } } });
    assert.equal(otherTitle.value, "ログ v2");
    assert.equal(commandTitle.value, "同步", "no pack title: the plugin's own base-language title");
    removePack();
    assert.equal(otherTitle.value, "日志");
    assert.equal(commandTitle.value, "同步");
  } finally {
    removePack();
    setPluginViewTitles([]);
    setPluginCommandTitles([]);
  }
});

test("Intl formatting follows the pack locale", () => {
  const { intlLocale } = useVueI18n();
  const formatted = computed(() => new Intl.NumberFormat(intlLocale.value).resolvedOptions().locale);
  const base = formatted.value;
  assert.equal(base, "zh-CN");
  applyPack("de-CH");
  try {
    assert.equal(formatted.value, "de-CH");
  } finally {
    removePack();
  }
  assert.equal(formatted.value, base);
});

test("theme, file icon theme and menu titles take the locale chain (own titles only)", () => {
  const { localeChain } = useVueI18n();
  const theme = { title: "Paper", titleLocalized: { zh: "纸", "zh-TW": "紙" } };
  const menu = { pluginId: "acme", command: "sync", title: "Sync", titleLocalized: { zh: "同步" } };
  applyPack("zh-TW", { acme: { commands: { sync: "同步化" } } });
  try {
    assert.equal(pluginThemeTitle(theme, localeChain.value), "紙");
    assert.equal(fileIconThemeTitle(theme, localeChain.value), "紙");
    assert.equal(menuItemTitle(menu, localeChain.value), "同步", "menus never use pack titles");
  } finally {
    removePack();
  }
  assert.equal(pluginThemeTitle(theme, localeChain.value), "纸");
  assert.equal(pluginThemeTitle(theme, "en"), "Paper");
});

test("UI formatting uses intlLocale, and command execution stays pack-free", async () => {
  const offenders = [];
  async function walk(dir) {
    for (const entry of await readdir(new URL(`../src/${dir}`, import.meta.url), { withFileTypes: true })) {
      const path = `${dir}/${entry.name}`;
      if (entry.isDirectory()) await walk(path);
      else if (/\.(vue|ts)$/.test(entry.name) && /\? "zh-CN" : "en-US"/.test(await source(path))) offenders.push(path);
    }
  }
  await walk("components");
  await walk("views");
  await walk("services");
  assert.deepEqual(offenders, []);
  const commands = await source("services/plugin-commands.ts");
  assert.match(commands, /watch\(localeChain,/);
  assert.match(commands, /resolvePluginTitle\(command, locale\)/);
  assert.doesNotMatch(commands, /services\/plugin-titles"|localizedPluginTitle\(|packTitle/);
});
