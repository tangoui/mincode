import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";
import {
  buildOverlay,
  findLocalePack,
  hasHiddenCharacter,
  isPackLocaleTag,
  localeCoverage,
  localizationPreviewSummary,
  parseLocalePackContributions,
  parsePluginTitlesAsset,
  translatableKeyCount,
} from "../src/domain/i18n/locale-pack.ts";
import { resolvePluginTitle } from "../src/domain/plugins/title.ts";
import { messagesEn } from "../src/i18n/locales/en.ts";
import { messagesZh } from "../src/i18n/locales/zh.ts";
import {
  PROTECTED_MESSAGE_KEYS,
  PROTECTED_MESSAGE_PREFIXES,
  isProtectedMessageKey,
} from "../src/i18n/protected-keys.ts";
import { currentLocaleOverlay, setLocaleOverlay, useVueI18n } from "../src/i18n/messages.ts";

const source = (path) => readFile(new URL(`../src/${path}`, import.meta.url), "utf8");
const assetKey = (n) => n.toString(16).padStart(64, "0");
const bidi = String.fromCodePoint(0x202e);
const zeroWidth = String.fromCodePoint(0x200b);

function contribution(pluginId, entries, issues = []) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "declarative",
    activeInWorkspace: true,
    point: { entries, issues },
  };
}

test("protected sentinels are never translatable", () => {
  for (const key of [
    "ai.agentic.allowOnce",
    "ai.agentic.reject",
    "ai.approvalMode.fullAccess",
    "plugins.aiGrants.allow",
    "terminal.pasteConfirmAction",
    "common.cancel",
    "settings.language",
    "pluginContrib.customEditors.remember",
    "plugins.aiConfig.title",
    "plugins.aiConfig.write",
    "plugins.aiConfig.mcpDisabled",
    "plugins.aiConfig.command",
    "plugins.capabilityAiConfig",
    // Shell extensions: confirmations, origin names, failures, button labels and the settings page header.
    "pluginContrib.shell.confirmTitle",
    "pluginContrib.shell.confirmWrite",
    "pluginContrib.shell.confirmDangerous",
    "pluginContrib.shell.originNotification",
    "pluginContrib.shell.originTray",
    "pluginContrib.shell.unavailable",
    "pluginContrib.shell.changed",
    "pluginContrib.settingsPages.header",
    "notifications.pluginActionsGroup",
    "notifications.pluginActionLabel",
    "pluginContrib.languageFeatures.codeLensConfirmTitle",
    "pluginContrib.languageFeatures.codeLensConfirmWrite",
    "pluginContrib.languageFeatures.codeLensConfirmDangerous",
    // Full trust: the warning, badge and lapse notice must not be softened by a language pack.
    "plugins.fullTrust.confirmBody",
    "plugins.fullTrust.confirmTitle",
    "plugins.fullTrust.hint",
    "plugins.fullTrust.badge",
    "plugins.fullTrust.badgeHelp",
    "plugins.fullTrust.previewHint",
    "plugins.fullTrust.lapsedNotice",
  ]) {
    assert.ok(Object.hasOwn(messagesEn, key), `${key} exists`);
    assert.equal(isProtectedMessageKey(key), true, key);
  }
  assert.equal(isProtectedMessageKey("settings.general"), false);
  assert.equal(isProtectedMessageKey("pluginContrib.localizations.group"), false);
  // Ordinary settings-page chrome stays translatable.
  assert.equal(isProtectedMessageKey("pluginContrib.settingsPages.retry"), false);
});

test("every protected prefix and key matches the catalogue", () => {
  const keys = Object.keys(messagesEn);
  for (const prefix of PROTECTED_MESSAGE_PREFIXES) {
    // terminalProfiles keys are added together with that point (Phase D T2).
    if (prefix === "pluginContrib.terminalProfiles.") continue;
    assert.ok(keys.some((key) => key.startsWith(prefix)), `prefix ${prefix} matches a key`);
  }
  for (const key of PROTECTED_MESSAGE_KEYS) assert.ok(Object.hasOwn(messagesEn, key), key);
});

test("zh and en catalogues have the same keys", () => {
  assert.deepEqual(Object.keys(messagesEn).sort(), Object.keys(messagesZh).sort());
});

test("buildOverlay keeps only safe, known, unprotected translations", () => {
  const catalogue = {
    "a.plain": "Open",
    "a.params": "Open {name} in {place}",
    "a.url": "See https://example.com",
    "a.multi": "Line one\nLine two",
    "common.cancel": "Cancel",
    "a.short": "Hi",
  };
  const { messages, dropped } = buildOverlay(catalogue, {
    "a.plain": "Abrir",
    "a.params": "Abrir {place} {name}",
    "a.multi": "Linha um\nLinha dois",
    "a.url": "Veja https://example.org",
    "missing.key": "x",
    "common.cancel": "Cancelar",
    "a.short": `Oi${bidi}`,
  });
  assert.deepEqual([...messages.keys()].sort(), ["a.multi", "a.params", "a.plain", "a.url"]);
  assert.equal(dropped.unknown, 1);
  assert.equal(dropped.protected, 1);
  assert.equal(dropped.hidden, 1);

  const second = buildOverlay(catalogue, {
    "a.params": "Abrir {name}",
    "a.plain": "Abrir https://evil.example",
    "a.short": "Oi\nOi",
    "a.url": "x".repeat(4 * catalogue["a.url"].length + 65),
    "a.multi": 42,
  });
  assert.equal(second.messages.size, 0);
  assert.deepEqual(
    { ...second.dropped },
    { unknown: 1, protected: 0, placeholder: 1, url: 1, length: 1, newline: 1, hidden: 0 },
  );
  assert.equal(buildOverlay(catalogue, ["not", "an", "object"]).messages.size, 0);
});

test("hidden characters, tags and coverage", () => {
  assert.equal(hasHiddenCharacter(`a${zeroWidth}b`, true), true);
  assert.equal(hasHiddenCharacter("a\nb", true), false);
  assert.equal(hasHiddenCharacter("a\nb", false), true);
  assert.equal(isPackLocaleTag("pt-BR"), true);
  assert.equal(isPackLocaleTag("zh-Hant-TW"), true);
  assert.equal(isPackLocaleTag("en-US"), false);
  assert.equal(isPackLocaleTag("ZH"), false);
  assert.equal(isPackLocaleTag("not a tag"), false);
  const catalogue = { "common.cancel": "Cancel", "a.b": "B", "a.c": "C" };
  assert.equal(translatableKeyCount(catalogue), 2);
  assert.equal(localeCoverage(1, 2), 50);
  assert.equal(localeCoverage(0, 0), 0);
});

test("parseLocalePackContributions re-checks entries", () => {
  const { packs, issues } = parseLocalePackContributions([
    contribution("b.pack", [
      { locale: "pt-BR", languageName: "Portuguese", localizedLanguageName: "Portugues", messages: { asset: assetKey(1), count: 3 } },
      { locale: "pt-BR", languageName: "Portuguese", localizedLanguageName: "Portugues", messages: { asset: assetKey(2), count: 1 } },
      { locale: "en", languageName: "English", localizedLanguageName: "English", messages: { asset: assetKey(3), count: 1 } },
      { locale: "fr", languageName: "French", localizedLanguageName: "Francais" },
      { locale: "de", languageName: `German${bidi}`, localizedLanguageName: "Deutsch", messages: { asset: assetKey(4), count: 1 } },
    ]),
    contribution("a.pack", [
      { locale: "pt-BR", languageName: "Portuguese", localizedLanguageName: "Portugues", pluginTitles: { asset: assetKey(5), plugins: ["x.y", "../bad"] } },
    ]),
  ]);
  assert.deepEqual(packs.map((pack) => `${pack.pluginId}/${pack.locale}`), ["a.pack/pt-BR", "b.pack/pt-BR"]);
  assert.deepEqual(packs[0].pluginTitles.plugins, ["x.y"]);
  assert.equal(packs[1].messages.count, 3);
  assert.equal(issues.length, 4);
  assert.equal(findLocalePack(packs, { tag: "pt-BR" })?.pluginId, "a.pack");
  assert.equal(findLocalePack(packs, { tag: "pt-BR", pluginId: "b.pack" })?.pluginId, "b.pack");
  assert.equal(findLocalePack(packs, { tag: "pt-BR", pluginId: "gone" }), undefined);
  assert.equal(findLocalePack(packs, null), undefined);
});

test("parsePluginTitlesAsset skips the pack's own plugin and unsafe titles", () => {
  const titles = parsePluginTitlesAsset({
    "pack.self": { commands: { run: "Executar" } },
    "other.plugin": { commands: { run: "Executar", bad: `x${bidi}` }, views: { main: "Principal", "": "x" } },
    "../evil": { views: { main: "x" } },
    "empty.plugin": { commands: {} },
  }, "pack.self");
  assert.deepEqual([...titles.keys()], ["other.plugin"]);
  assert.deepEqual({ ...titles.get("other.plugin").commands }, { run: "Executar" });
  assert.deepEqual({ ...titles.get("other.plugin").views }, { main: "Principal" });
});

test("plugin title chain: own titleLocalized wins over a pack", () => {
  const own = { title: "Run", titleLocalized: { "pt-BR": "Rodar", zh: "运行", en: "Run it" } };
  assert.equal(resolvePluginTitle(own, ["pt-BR", "en"], "Executar"), "Rodar");
  const partial = { title: "Run", titleLocalized: { zh: "运行", en: "Run it" } };
  assert.equal(resolvePluginTitle(partial, ["pt-BR", "en"], "Executar"), "Executar");
  assert.equal(resolvePluginTitle(partial, ["pt-BR", "en"]), "Run it");
  // zh-TW must not take the plugin's zh (base) title before the pack's.
  assert.equal(resolvePluginTitle(partial, ["zh-TW", "zh"], "執行"), "執行");
  assert.equal(resolvePluginTitle({ title: "Run", titleLocalized: { pt: "Correr" } }, ["pt-BR", "en"], "Executar"), "Correr");
  assert.equal(resolvePluginTitle(partial, "zh"), "运行");
  assert.equal(resolvePluginTitle({ title: "Run" }, ["pt-BR", "en"], "  "), "Run");
});

test("overlay applies, filters protected keys and is removed", () => {
  const { t, localeTag, localeChain, intlLocale } = useVueI18n();
  const base = t("settings.general");
  setLocaleOverlay({
    tag: "pt-BR",
    pluginId: "pack",
    messages: new Map([["settings.general", "Geral"], ["common.cancel", "Cancelar"]]),
  });
  assert.equal(t("settings.general"), "Geral");
  assert.equal(t("common.cancel"), messagesZh["common.cancel"]);
  assert.equal(currentLocaleOverlay()?.messages.has("common.cancel"), false);
  assert.equal(localeTag.value, "pt-BR");
  assert.deepEqual(localeChain.value, ["pt-BR", "zh"]);
  assert.equal(intlLocale.value, "pt-BR");
  setLocaleOverlay(null);
  assert.equal(t("settings.general"), base);
  assert.deepEqual(localeChain.value, ["zh"]);
});

test("install preview lists declared languages", () => {
  assert.deepEqual(localizationPreviewSummary({
    localizations: [
      { locale: "pt-BR", languageName: "Portuguese", localizedLanguageName: "Portugues" },
      { locale: "fr" },
      "junk",
    ],
  }), ["Portugues (Portuguese, pt-BR)", "fr"]);
  assert.deepEqual(localizationPreviewSummary(undefined), []);
});

test("语言包 consumer 延迟加载 runtime，避免进入启动入口包", async () => {
  const text = await source("services/contributions/localizations.ts");
  assert.doesNotMatch(text, /import\s*\{[^}]*setLocalePacks[^}]*\}\s*from/);
  assert.match(text, /import\("@\/services\/locale-pack-loader"\)/);
  assert.match(text, /lazyContributionApply/);
});

test("AI settings tool clears the language pack on a locale change", async () => {
  const text = await source("services/ai-tools/settings-tools.ts");
  assert.match(text, /clearLocalePackSelection\(\{ persist: true \}\)/);
});

test("confirmations use the pack-free plugin title", async () => {
  const titles = await source("services/plugin-titles.ts");
  assert.match(titles, /Never use this for execution confirmations/);
  const commands = await source("services/plugin-commands.ts");
  assert.doesNotMatch(commands, /localizedPluginTitle|pluginCommandTitle/);
});

test("host text is never rendered as HTML from t()", async () => {
  const offenders = [];
  async function walk(dir) {
    for (const entry of await readdir(new URL(`../src/${dir}`, import.meta.url), { withFileTypes: true })) {
      const path = `${dir}/${entry.name}`;
      if (entry.isDirectory()) await walk(path);
      else if (entry.name.endsWith(".vue")) {
        const text = await source(path);
        if (/v-html="[^"]*\bt\(/.test(text)) offenders.push(path);
      }
    }
  }
  await walk("components");
  await walk("views");
  assert.deepEqual(offenders, []);
});
