import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const panelSource = await readFile(
  new URL("../src/components/projects/ProjectGitPanel.vue", import.meta.url),
  "utf8",
);
const zhLocale = await readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8");
const enLocale = await readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8");

test("unstaged and untracked group headers expose group-level discard actions", () => {
  // Each non-staged group header needs its own destructive action, guarded by a
  // confirmation dialog, and scoped to a single backend command.
  assert.match(panelSource, /async function discardUnstagedAll\(\)/);
  assert.match(panelSource, /async function cleanUntrackedAll\(\)/);
  assert.match(panelSource, /confirmAction\(\s*t\("git\.discardUnstagedAllConfirm"/);
  assert.match(panelSource, /confirmAction\(\s*t\("git\.cleanUntrackedAllConfirm"/);
  assert.match(panelSource, /runGit\("git_discard_unstaged"\)/);
  assert.match(panelSource, /runGit\("git_clean_untracked"\)/);

  // The header action buttons must be wired to the two group handlers.
  assert.match(panelSource, /@click="[^"]*discardUnstagedAll\(\)[^"]*"/);
  assert.match(panelSource, /@click="[^"]*cleanUntrackedAll\(\)[^"]*"/);
});

test("group discard copy exists in both locales", () => {
  for (const locale of [zhLocale, enLocale]) {
    assert.match(locale, /"git\.discardUnstagedAll":/);
    assert.match(locale, /"git\.discardUnstagedAllConfirm":/);
    assert.match(locale, /"git\.cleanUntrackedAll":/);
    assert.match(locale, /"git\.cleanUntrackedAllConfirm":/);
  }
});
