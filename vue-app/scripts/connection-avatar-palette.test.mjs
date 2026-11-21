import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const css = await readFile(new URL("../src/styles/shared/connection-overview.css", import.meta.url), "utf8");

function rule(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^}]*)\\}`));
  assert.ok(match, `missing rule ${selector}`);
  return match[1];
}

test("连接头像用浅色底和同色深字，不再是深色实底白字", () => {
  const body = rule(".connection-avatar");
  assert.match(body, /background:\s*color-mix\(in srgb, var\(--connection-avatar-color, var\(--app-text-disabled\)\) 16%, var\(--app-surface\)\)/);
  assert.match(body, /color:\s*color-mix\(in srgb, var\(--connection-avatar-color, var\(--app-text-muted\)\) 78%, var\(--app-text\)\)/);
  assert.doesNotMatch(body, /color:\s*#fff/i);
});

test("深色主题下头像换成深底亮字", () => {
  const body = rule(':root[data-theme-kind="dark"] .connection-avatar');
  assert.match(body, /background:\s*color-mix\(in srgb, var\(--connection-avatar-color, var\(--app-text-disabled\)\) 26%, var\(--app-surface\)\)/);
  assert.match(body, /color:\s*color-mix\(in srgb, var\(--connection-avatar-color, var\(--app-text-muted\)\) 45%, #ffffff\)/);
});
