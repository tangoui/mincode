import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const view = await readFile(
  new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url),
  "utf8",
);
const styles = await readFile(
  new URL("../src/styles/workspaces/settings/dialog.css", import.meta.url),
  "utf8",
);

// Registry results (MCP search and templates) render as market-style
// cards since c6f9c9be; each card owns a footer action row built from Element Plus buttons.
function registryCards() {
  return ["mcpSearchResults", "builtinMcpTemplates"].map((source) => {
    const start = view.indexOf(`v-for="entry in ${source}"`);
    assert.ok(start >= 0, `missing registry result list ${source}`);
    const end = view.indexOf("</article>", start);
    assert.ok(end > start, `missing registry card end for ${source}`);
    return { source, card: view.slice(start, end) };
  });
}

function cssRule(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return styles.match(new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^}]*)\\}`))?.[1] ?? "";
}

test("AI 注册表结果使用 Element Plus 横向操作区", () => {
  for (const { source, card } of registryCards()) {
    assert.match(
      card,
      /<div class="settings-ai-entry-card__actions">[\s\S]*?<el-button[\s\S]*?<\/div>/,
      `${source} cards must expose an Element Plus action row`,
    );
    const actions = card.slice(card.indexOf("settings-ai-entry-card__actions"));
    assert.doesNotMatch(actions, /<button\b/, `${source} actions must use el-button`);
  }

  const actionRule = cssRule(".settings-ai-entry-card__actions");
  assert.match(actionRule, /display:\s*flex;/);
  assert.match(actionRule, /align-items:\s*center;/);
  assert.doesNotMatch(actionRule, /flex-direction:\s*column/);
  const footRule = cssRule(".settings-ai-entry-card__foot");
  assert.match(footRule, /display:\s*flex;/);
  assert.doesNotMatch(footRule, /flex-direction:\s*column/);
});

test("注册表内容纵排规则不再误伤操作区", () => {
  for (const { source, card } of registryCards()) {
    assert.match(
      card,
      /class="settings-ai-entry-card__title">[\s\S]*?<strong[\s\S]*?<small/,
      `${source} cards must keep name/id inside the title column`,
    );
    const title = card.indexOf("settings-ai-entry-card__title");
    const actions = card.indexOf("settings-ai-entry-card__actions");
    assert.ok(title >= 0 && actions > title, `${source} action row must sit outside the title column`);
  }

  // Only the explicit title column stacks vertically; no broad child selector may
  // turn the card's action row into a column.
  assert.match(cssRule(".settings-ai-entry-card__title"), /flex-direction:\s*column;/);
  assert.doesNotMatch(styles, /\.settings-ai-entry-card\s*>\s*(?:div|footer)\b/);
  assert.doesNotMatch(styles, /\.settings-ai-registry-list article > div,/);
  assert.doesNotMatch(styles, /\.settings-ai-card-grid article > div\b/);
});
