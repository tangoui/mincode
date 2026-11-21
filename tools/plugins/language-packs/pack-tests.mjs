// Shared node:test suite for the official translated language packs
// (official-plugins/lang-<tag>). Each pack's tests/*.test.mjs calls
// languagePackTests; run them with `pnpm --dir vue-app test:official-plugin-data`.
//
// Checks a pack against the host's own rules: the per-message asset rules
// the Go host enforces (one violation rejects the whole pack) and the
// frontend overlay rules (vue-app/src/domain/i18n/locale-pack.ts). Keys the
// host removed or protected after translation are only dropped at runtime,
// so they are reported as diagnostics; update with tools/plugins/language-packs/sync.mjs.
import assert from "node:assert/strict";
import { readFile, stat } from "node:fs/promises";
import test from "node:test";
import { messagesEn } from "../../../vue-app/src/i18n/locales/en.ts";
import { buildOverlay, isPackLocaleTag, localizationPreviewSummary, translatableKeyCount } from "../../../vue-app/src/domain/i18n/locale-pack.ts";
import { isProtectedMessageKey } from "../../../vue-app/src/i18n/protected-keys.ts";
import { LIMITS, SPEC, hostAcceptsMessage } from "./rules.mjs";

// A release must translate nearly the whole catalogue; host copy added
// later falls back to the base language until the pack is updated.
const MIN_COVERAGE = 0.95;
// Multi-word messages left in English (product names and code-like
// examples legitimately stay); more than this means untranslated chunks.
const MAX_ENGLISH_SHARE = 0.03;

export function languagePackTests(pluginRoot, { locale }) {
  const manifestPromise = readFile(new URL("plugin.json", pluginRoot), "utf8").then(JSON.parse);
  const messagesPath = `l10n/host.${locale}.json`;

  async function readPack() {
    const bytes = await readFile(new URL(messagesPath, pluginRoot));
    assert.notEqual(bytes[0], 0xef, `${messagesPath} must not start with a byte order mark`);
    return { bytes, messages: JSON.parse(bytes.toString("utf8")) };
  }

  test(`${locale}: manifest is a declarative pack with one localization`, async () => {
    const manifest = await manifestPromise;
    assert.equal(manifest.id, `lang-${locale.toLowerCase()}`);
    assert.equal(manifest.apiVersion, 2);
    assert.equal(manifest.runtime.type, "declarative");
    assert.equal(manifest.capabilities, undefined);
    assert.deepEqual(Object.keys(manifest.contributes), ["localizations"]);
    const entries = manifest.contributes.localizations;
    assert.equal(entries.length, 1);
    const [entry] = entries;
    assert.equal(entry.locale, locale);
    assert.equal(entry.messages, messagesPath);
    assert.ok(isPackLocaleTag(entry.locale), entry.locale);
    assert.ok(!SPEC.lists.builtinLocales.includes(entry.locale), entry.locale);
    for (const name of [entry.languageName, entry.localizedLanguageName]) {
      assert.ok(name.trim() && [...name].length <= LIMITS.languageNameMaxRunes, name);
    }
    assert.ok(SPEC.assets.dirs.includes(entry.messages.split("/")[0]), entry.messages);
    assert.equal(localizationPreviewSummary(manifest.contributes).length, 1);
  });

  test(`${locale}: messages pass the Go host's asset rules`, async () => {
    const { bytes, messages } = await readPack();
    assert.ok(bytes.length <= SPEC.assets.maxBytes, `${messagesPath}: ${bytes.length} bytes`);
    assert.equal(typeof messages, "object");
    assert.ok(!Array.isArray(messages));
    const keys = Object.keys(messages);
    assert.ok(keys.length > 0 && keys.length <= LIMITS.messagesMaxKeys, `${keys.length} messages`);
    assert.deepEqual(keys, [...keys].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)), "keys are sorted");
    for (const key of keys) assert.ok(hostAcceptsMessage(key, messages[key]), key);
  });

  test(`${locale}: messages survive the overlay rules and cover the catalogue`, async (t) => {
    const { messages } = await readPack();
    const { messages: kept, dropped } = buildOverlay(messagesEn, messages);
    assert.deepEqual({ ...dropped, unknown: 0, protected: 0 }, { unknown: 0, protected: 0, placeholder: 0, url: 0, length: 0, newline: 0, hidden: 0 });
    const translatable = translatableKeyCount(messagesEn);
    assert.ok(kept.size >= translatable * MIN_COVERAGE, `${kept.size}/${translatable} translated`);
    const english = [...kept].filter(([key, value]) => value === messagesEn[key] && /\p{L}{3,}\s+\p{L}{3,}/u.test(value));
    assert.ok(english.length <= kept.size * MAX_ENGLISH_SHARE, `${english.length} multi-word messages are still English`);
    const stale = Object.keys(messages).filter((key) => typeof messagesEn[key] !== "string" || isProtectedMessageKey(key));
    if (stale.length) t.diagnostic(`${stale.length} keys are no longer translatable; run tools/plugins/language-packs/sync.mjs --prune`);
    const missing = translatable - kept.size;
    if (missing) t.diagnostic(`${missing} host messages are not translated yet and fall back to the base language`);
  });

  test(`${locale}: package members exist`, async () => {
    const manifest = await manifestPromise;
    for (const path of [messagesPath, manifest.cover, "README.md", "CHANGELOG.md"]) {
      assert.ok((await stat(new URL(path, pluginRoot))).isFile(), path);
    }
  });
}
