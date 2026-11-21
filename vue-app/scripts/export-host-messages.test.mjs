import "./ts-resolve.mjs";
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const { exportHostMessages, messagesTemplate } = await import("./export-host-messages.mjs");
const { messagesEn } = await import("../src/i18n/locales/en.ts");
const { isProtectedMessageKey } = await import("../src/i18n/protected-keys.ts");
const { buildOverlay } = await import("../src/domain/i18n/locale-pack.ts");

const script = fileURLToPath(new URL("./export-host-messages.mjs", import.meta.url));
const run = promisify(execFile);

test("catalogue covers every host key and marks protected keys like the runtime", () => {
  const catalogue = exportHostMessages();
  assert.deepEqual(Object.keys(catalogue.keys).sort(), Object.keys(messagesEn).sort());
  for (const [key, entry] of Object.entries(catalogue.keys)) {
    assert.equal(entry.protected, isProtectedMessageKey(key), key);
    assert.equal(entry.en, messagesEn[key]);
    assert.equal(typeof entry.zh, "string", `${key} has zh text`);
  }
  assert.equal(catalogue.keys["common.cancel"].protected, true);
  assert.equal(catalogue.keys["ai.agentic.allowOnce"].protected, true);
  assert.equal(catalogue.keys["settings.general"].protected, false);
  const { total, translatable, protected: protectedCount } = catalogue.counts;
  assert.equal(total, translatable + protectedCount);
  assert.ok(protectedCount > 0 && translatable > 0);
  const withPlaceholder = Object.values(catalogue.keys).find((entry) => entry.placeholders.length > 0);
  assert.ok(withPlaceholder, "some key has placeholders");
  for (const name of withPlaceholder.placeholders) assert.ok(withPlaceholder.en.includes(name));
});

test("the template has only translatable keys and passes buildOverlay unchanged", () => {
  const template = messagesTemplate(exportHostMessages());
  assert.equal(Object.keys(template).some(isProtectedMessageKey), false);
  const overlay = buildOverlay(messagesEn, template);
  assert.equal(overlay.messages.size, Object.keys(template).length);
});

test("CLI writes the catalogue and a pack template, and rejects built-in tags", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mincode-host-messages-"));
  try {
    await run(process.execPath, [script, "--out", directory, "--locale", "pt-BR"]);
    const catalogue = JSON.parse(await readFile(join(directory, "host-messages.json"), "utf8"));
    const template = JSON.parse(await readFile(join(directory, "messages.pt-BR.json"), "utf8"));
    assert.equal(catalogue.counts.translatable, Object.keys(template).length);
    await assert.rejects(run(process.execPath, [script, "--out", directory, "--locale", "zh-CN"]));
    await assert.rejects(run(process.execPath, [script, "--locale", "ja"]));
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
