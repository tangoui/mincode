#!/usr/bin/env node
/**
 * Exports the host message catalogue for language pack authors
 * (contributes.localizations). Usage, from the repository root:
 *
 *   node --experimental-strip-types vue-app/scripts/export-host-messages.mjs --out <dir> [--locale <tag>]
 *
 * Writes `<dir>/host-messages.json`: every host key with its English and
 * Chinese base text, its `{placeholder}` list and whether it is protected
 * (never translatable; a pack value is ignored). With `--locale` it also
 * writes `<dir>/messages.<tag>.json`, a starter `messages` asset holding only
 * the translatable keys prefilled with English: translate the values and
 * delete keys you do not translate (they fall back to the base language).
 * Without `--out` the catalogue is printed to stdout.
 *
 * The protected-key rules, tag syntax and placeholder syntax come from the
 * same modules the runtime uses (i18n/protected-keys.ts and
 * domain/i18n/locale-pack.ts); nothing is duplicated here. On-demand author
 * tooling: its output is not committed.
 */
import "./ts-resolve.mjs";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const [{ messagesEn }, { messagesZh }, { isProtectedMessageKey }, { isPackLocaleTag, placeholderMultiset }] =
  await Promise.all([
    import("../src/i18n/locales/en.ts"),
    import("../src/i18n/locales/zh.ts"),
    import("../src/i18n/protected-keys.ts"),
    import("../src/domain/i18n/locale-pack.ts"),
  ]);

/** The host catalogue keyed by message key (English is the reference catalogue). */
export function exportHostMessages() {
  const keys = {};
  let protectedCount = 0;
  for (const key of Object.keys(messagesEn).sort()) {
    const isProtected = isProtectedMessageKey(key);
    if (isProtected) protectedCount++;
    keys[key] = {
      en: messagesEn[key],
      zh: Object.hasOwn(messagesZh, key) ? messagesZh[key] : null,
      placeholders: [...new Set(placeholderMultiset(messagesEn[key]))],
      protected: isProtected,
    };
  }
  const total = Object.keys(keys).length;
  return {
    description:
      "MinCode host messages for language pack authors. Translate only keys with protected=false; " +
      "keep every {placeholder}; protected keys always render in the user's base language.",
    counts: { total, translatable: total - protectedCount, protected: protectedCount },
    keys,
  };
}

/** A starter `messages` asset: translatable keys only, prefilled with English. */
export function messagesTemplate(catalogue) {
  const template = {};
  for (const [key, entry] of Object.entries(catalogue.keys)) {
    if (!entry.protected) template[key] = entry.en;
  }
  return template;
}

function parseArguments(argv) {
  const options = { out: undefined, locale: undefined };
  for (let index = 0; index < argv.length; index++) {
    const argument = argv[index];
    const value = argv[index + 1];
    if ((argument === "--out" || argument === "--locale") && value && !value.startsWith("--")) {
      options[argument.slice(2)] = value;
      index++;
    } else if (argument === "--help" || argument === "-h") {
      options.help = true;
    } else {
      throw new Error(`unknown or incomplete argument: ${argument}`);
    }
  }
  if (options.locale !== undefined && !isPackLocaleTag(options.locale)) {
    throw new Error(`--locale ${options.locale} is not a language pack tag (BCP 47 subset, not zh/zh-CN/en/en-US)`);
  }
  if (options.locale !== undefined && options.out === undefined) {
    throw new Error("--locale requires --out");
  }
  return options;
}

export async function main(argv) {
  const options = parseArguments(argv);
  if (options.help) {
    process.stdout.write("Usage: export-host-messages.mjs [--out <dir>] [--locale <tag>]\n");
    return;
  }
  const catalogue = exportHostMessages();
  const json = (value) => `${JSON.stringify(value, null, 2)}\n`;
  if (options.out === undefined) {
    process.stdout.write(json(catalogue));
    return;
  }
  const directory = resolve(options.out);
  await mkdir(directory, { recursive: true });
  await writeFile(resolve(directory, "host-messages.json"), json(catalogue));
  const written = ["host-messages.json"];
  if (options.locale) {
    const name = `messages.${options.locale}.json`;
    await writeFile(resolve(directory, name), json(messagesTemplate(catalogue)));
    written.push(name);
  }
  const { total, translatable, protected: protectedCount } = catalogue.counts;
  process.stderr.write(
    `Wrote ${written.join(", ")} to ${directory} (${total} keys, ${translatable} translatable, ${protectedCount} protected)\n`,
  );
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((error) => {
    process.stderr.write(`export-host-messages: ${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  });
}
