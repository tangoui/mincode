import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const styles = await readFile(
  new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url),
  "utf8",
);

function rule(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return styles.match(new RegExp(`${escaped}\\s*\\{([\\s\\S]*?)\\}`))?.[1] ?? "";
}

test("lets Element Plus activity button content shrink for ellipsis", () => {
  const wrapper = rule(".ai-agent-event-group__item-row.el-button > span");
  const title = rule(".ai-agent-event-group__item-row strong");

  assert.match(wrapper, /overflow:\s*hidden/);
  assert.match(wrapper, /min-width:\s*0/);
  assert.match(title, /text-overflow:\s*ellipsis/);
  assert.match(title, /white-space:\s*nowrap/);
});

test("clips overflowing conversation and event titles", () => {
  assert.match(rule(".ai-conversation-item"), /overflow:\s*hidden/);
  assert.match(rule(".ai-conversation-item__content > span,\n.ai-tool-suggest__title"), /overflow:\s*hidden/);
  assert.match(rule(".ai-agent-event-card__title strong"), /min-width:\s*0/);
  assert.match(rule(".ai-agent-event-card__title strong"), /text-overflow:\s*ellipsis/);
});
