import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";

const css = await readFile(new URL("../src/plugin-ui/page.css", import.meta.url), "utf8");
const packages = new URL("../../official-plugins/", import.meta.url);

function rule(selector) {
  const start = css.indexOf(`${selector} {`);
  assert.ok(start >= 0, selector);
  return css.slice(start + selector.length + 2, css.indexOf("}", start));
}

test("shared plugin header is one compact row without the letter logo", () => {
  assert.match(rule(".plugin-page-header"), /min-height:\s*44px/);
  assert.match(rule(".plugin-page-header__logo"), /display:\s*none/);
  assert.match(rule(".plugin-page-header__desc"), /text-overflow:\s*ellipsis/);
  assert.doesNotMatch(css, /box-shadow:\s*0 6px 14px/);
});

test("shared plugin controls use the desktop 28px scale", () => {
  assert.match(css, /\.official-plugin-page \{ --el-component-size: 28px;/);
  assert.doesNotMatch(css, /min-height:\s*3[4-6]px/);
});

// 单个主操作控件可以放大（如 openapi-contract 的请求地址栏），但不能改整页的控件尺寸。
const localControlSizes = { "openapi-contract": ".oa-endpoint-select" };

test("official plugins do not override the shared control scale or base font", async () => {
  const offenders = [];
  for (const entry of await readdir(packages, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const source = await readFile(new URL(`${entry.name}/ui-src/style.css`, packages), "utf8").catch(() => "");
    for (const [, selector, body] of source.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      if (/--el-component-size:\s*(?:29|3\d)px/.test(body) && localControlSizes[entry.name] !== selector.trim()) offenders.push(`${entry.name}: control size`);
    }
    if (/--el-font-size-base:\s*14px/.test(source)) offenders.push(`${entry.name}: base font`);
  }
  assert.deepEqual(offenders, []);
});


test("connection picker keeps an explicit intrinsic width before wrapping on narrow screens", async () => {
  const source = await readFile(new URL("../src/plugin-ui/PluginConnectionBar.vue", import.meta.url), "utf8");
  assert.match(source, /\.plugin-connection-bar__select \{ flex: 0 0 auto; width: 220px;/);
  assert.match(source, /@media \(max-width: 720px\)[^]*flex: 1 1 160px; width: auto;/);
});

test("connection workbench keeps a 44px one-row top bar and folds the list into a drawer on narrow pages", async () => {
  const source = await readFile(new URL("../src/plugin-ui/PluginConnectionWorkbench.vue", import.meta.url), "utf8");
  assert.match(source, /\.plugin-connection-workbench \{ --el-component-size: 28px;/);
  assert.match(source, /\.plugin-connection-workbench__topbar \{[^}]*min-height: 44px;/);
  assert.match(source, /@media \(max-width: 720px\) \{\s*\.plugin-connection-workbench__side, \.plugin-connection-workbench__resizer \{ display: none; \}/);
  assert.match(source, /window\.matchMedia\('\(max-width: 720px\)'\)/);
});

test("plugin dialogs fit the viewport: header and footer stay visible and the body scrolls", () => {
  const dialog = rule(".el-dialog:not(.is-fullscreen)");
  assert.match(dialog, /display:\s*flex;\s*flex-direction:\s*column/);
  assert.match(dialog, /max-height:\s*calc\(100vh - var\(--el-dialog-margin-top, 15vh\) - 16px\)/);
  assert.doesNotMatch(dialog, /width/, "dialog width is left to Element Plus and the plugin");
  assert.match(rule(".el-dialog.is-align-center:not(.is-fullscreen)"), /max-height:\s*calc\(100vh - 32px\)/);
  assert.match(rule(".el-dialog:not(.is-fullscreen) > :is(.el-dialog__header, .el-dialog__footer)"), /flex:\s*none/);
  const body = rule(".el-dialog:not(.is-fullscreen) > .el-dialog__body");
  assert.match(body, /min-height:\s*0/);
  assert.match(body, /overflow:\s*auto/);
});
