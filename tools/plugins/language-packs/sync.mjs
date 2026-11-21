// Maintains the messages asset of a translated language pack plugin; to start
// a new pack from scratch use vue-app/scripts/export-host-messages.mjs.
// Run from the repository root:
//
//   node --experimental-strip-types --import ./vue-app/scripts/ts-resolve.mjs \
//     tools/plugins/language-packs/sync.mjs official-plugins/<id> [options]
//
// Without options it reports coverage against the host's English catalogue.
//   --missing <file>  write the untranslated messages as { key: English } for translators
//   --merge <file>    merge translated { key: text } into the pack (repeatable)
//   --prune           remove keys the host no longer has or now protects
//   --locale <tag>    pick the localization when the plugin declares several
// Messages that the host would drop (placeholders, line breaks, URLs, length,
// hidden characters) are never written; they are listed instead.
import { readFile, writeFile } from "node:fs/promises";
import { parseArgs } from "node:util";
import { resolve } from "node:path";
import { messagesEn } from "../../../vue-app/src/i18n/locales/en.ts";
import { buildOverlay, translatableKeyCount } from "../../../vue-app/src/domain/i18n/locale-pack.ts";
import { isProtectedMessageKey } from "../../../vue-app/src/i18n/protected-keys.ts";
import { hostAcceptsMessage, serializeMessages } from "./rules.mjs";

const { values, positionals } = parseArgs({
  allowPositionals: true,
  options: {
    missing: { type: "string" },
    merge: { type: "string", multiple: true },
    prune: { type: "boolean" },
    locale: { type: "string" },
  },
});
if (positionals.length !== 1) {
  console.error("usage: sync.mjs <plugin-dir> [--missing <file>] [--merge <file> ...] [--prune] [--locale <tag>]");
  process.exit(2);
}

const pluginDir = resolve(positionals[0]);
const manifest = JSON.parse(await readFile(`${pluginDir}/plugin.json`, "utf8"));
const entries = (manifest.contributes?.localizations ?? []).filter((entry) => typeof entry.messages === "string");
const entry = values.locale ? entries.find((item) => item.locale === values.locale) : entries.length === 1 ? entries[0] : undefined;
if (!entry) {
  console.error(`choose one of: ${entries.map((item) => item.locale).join(", ") || "(no messages assets)"} with --locale`);
  process.exit(2);
}

const assetPath = `${pluginDir}/${entry.messages}`;
const messages = JSON.parse(await readFile(assetPath, "utf8").catch(() => "{}"));
const rejected = [];

function accept(key, value) {
  const { messages: kept } = buildOverlay(messagesEn, { [key]: value });
  if (kept.has(key) && hostAcceptsMessage(key, value)) return true;
  rejected.push(key);
  return false;
}

let changed = false;
for (const file of values.merge ?? []) {
  const incoming = JSON.parse(await readFile(file, "utf8"));
  for (const [key, value] of Object.entries(incoming)) {
    if (!accept(key, value) || messages[key] === value) continue;
    messages[key] = value;
    changed = true;
  }
}

const stale = Object.keys(messages).filter((key) => typeof messagesEn[key] !== "string" || isProtectedMessageKey(key));
if (values.prune) {
  for (const key of stale) delete messages[key];
  changed ||= stale.length > 0;
}

const missing = Object.keys(messagesEn).filter((key) => !isProtectedMessageKey(key) && !(key in messages)).sort();
if (values.missing) {
  await writeFile(values.missing, serializeMessages(Object.fromEntries(missing.map((key) => [key, messagesEn[key]]))));
}
if (changed) await writeFile(assetPath, serializeMessages(messages));

const translatable = translatableKeyCount(messagesEn);
const translated = Object.keys(messages).length - (values.prune ? 0 : stale.length);
console.log(`${entry.locale}: ${translated}/${translatable} translated, ${missing.length} missing, ${values.prune ? 0 : stale.length} stale${changed ? `, wrote ${entry.messages}` : ""}`);
if (rejected.length) {
  console.log(`rejected ${rejected.length} message(s) the host would drop: ${rejected.join(", ")}`);
  process.exitCode = 1;
}
