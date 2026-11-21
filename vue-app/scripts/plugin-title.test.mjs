import "./ts-resolve.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { filterCommandPaletteItems } from "../src/domain/commands/command-palette.ts";
import { computed } from "vue";
import { resolvePluginTitle } from "../src/domain/plugins/title.ts";
const { useVueI18n } = await import("../src/i18n/messages.ts");
const { localizedPluginTitle, pluginTabTitle, setPluginCommandTitles, setPluginViewTitles } = await import("../src/services/plugin-titles.ts");
const { registerCommand, useRegisteredCommands } = await import("../src/services/command-registry.ts");

test("palette open-tab entries resolve titles inside their reactive item mapping", () => {
  const source = readFileSync(new URL("../src/composables/use-command-palette.ts", import.meta.url), "utf8");
  const mapping = source.slice(source.indexOf("const tabItems:"), source.indexOf("const projectItems:"));
  assert.match(mapping, /title: pluginTabTitle\(tab\) \|\| t\(tab\.titleKey as MessageKey\)/);
});

const contribution = { title: "Default", titleLocalized: { zh: "中文", en: "English", "en-GB": "British" } };

test("plugin titles prefer exact locale, then base language, then default", () => {
  assert.equal(resolvePluginTitle(contribution, "en-GB"), "British");
  assert.equal(resolvePluginTitle(contribution, "en-US"), "English");
  assert.equal(resolvePluginTitle(contribution, "zh-Hant-TW"), "中文");
  assert.equal(resolvePluginTitle(contribution, "fr"), "Default");
  assert.equal(resolvePluginTitle({ title: "Legacy" }, "zh"), "Legacy");
});

test("blank, malformed and inherited translations never hide the default", () => {
  const titleLocalized = Object.assign(Object.create({ fr: "inherited" }), {
    "en-GB": "  ", en: " English ", zh: 42, de: null,
  });
  for (const locale of ["zh", "de", "fr", "constructor", "__proto__", ""]) {
    assert.equal(resolvePluginTitle({ title: "Default", titleLocalized }, locale), "Default");
  }
  assert.equal(resolvePluginTitle({ title: "Default", titleLocalized }, "en-GB"), "English");
});

test("host labels, registered commands and existing tabs react without re-registration or mutation", async () => {
  const { locale, setLocale } = useVueI18n();
  const view = { ...contribution, pluginId: "example", id: "main" };
  const tab = { view: "plugin-view", title: "中文", pluginView: { pluginId: "example", viewId: "main" } };
  const original = structuredClone(tab);
  setPluginViewTitles([view, { ...view, pluginId: "other", titleLocalized: { en: "Other" } }]);
  setPluginCommandTitles([{ ...view, id: "open" }]);
  const dispose = registerCommand({ id: "plugin:example:open", source: "plugin", title: "Captured title", execute() {} });
  const commands = useRegisteredCommands();
  const labels = computed(() => [localizedPluginTitle(view), pluginTabTitle(tab), commands.value[0].title]);
  const tabItems = computed(() => [{
    id: "tab:example", kind: "tab", title: pluginTabTitle(tab),
    action: { type: "select-tab", tabId: "example" },
  }]);
  const englishTabResults = computed(() => filterCommandPaletteItems(tabItems.value, "English"));
  try {
    setLocale("zh");
    assert.deepEqual(labels.value, ["中文", "中文", "中文"]);
    assert.equal(englishTabResults.value.length, 0);
    setLocale("en");
    for (let attempt = 0; locale.value !== "en" && attempt < 100; attempt++) {
      await new Promise(resolve => setTimeout(resolve, 5));
    }
    assert.equal(locale.value, "en");
    assert.deepEqual(labels.value, ["English", "English", "English"]);
    assert.equal(englishTabResults.value[0]?.title, "English");
    assert.equal(filterCommandPaletteItems(tabItems.value, "中文").length, 0);
    assert.deepEqual(tab, original);
    assert.equal(pluginTabTitle({ ...tab, title: "Custom name" }), "Custom name");
    setPluginViewTitles([]);
    assert.equal(pluginTabTitle(tab), "中文");
    assert.equal(pluginTabTitle({ view: "terminal", title: "My terminal" }), "My terminal");
    setPluginCommandTitles([]);
    assert.equal(commands.value[0].title, "Captured title");
  } finally {
    dispose();
    setPluginCommandTitles([]);
    setPluginViewTitles([]);
    setLocale("zh");
  }
});
