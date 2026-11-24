import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const styles = await readFile(
  new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url),
  "utf8",
);
const timelineStyles = await readFile(new URL("../src/styles/workspaces/ai/timeline.css", import.meta.url), "utf8");

function rule(selector, source = styles) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return source.match(new RegExp(`${escaped}\\s*\\{([\\s\\S]*?)\\}`))?.[1] ?? "";
}

test("lets timeline activity targets shrink for ellipsis", () => {
  const wrapper = rule(".ai-timeline__title", timelineStyles);
  const target = rule(".ai-timeline__title code", timelineStyles);

  assert.match(wrapper, /min-width:\s*0/);
  assert.match(target, /overflow:\s*hidden/);
  assert.match(target, /text-overflow:\s*ellipsis/);
  assert.match(target, /white-space:\s*nowrap/);
});

test("clips overflowing conversation titles", () => {
  assert.match(rule(".ai-conversation-item"), /overflow:\s*hidden/);
  assert.match(rule(".ai-conversation-item__content > span,\n.ai-tool-suggest__title"), /overflow:\s*hidden/);
});
