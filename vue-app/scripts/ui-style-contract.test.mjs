import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { contrastRatio, parseHexColor } from "@/domain/themes/editor-theme-data";

const source = async (path) => readFile(new URL(`../src/${path}`, import.meta.url), "utf8");
const foundation = await source("styles/foundation/foundation.css");
const controls = await source("styles/shared/element-plus-controls.css");
const filters = await source("styles/workspaces/database/filters.css");
const surfaces = await source("styles/shared/workspace-surfaces.css");
const tabs = await source("styles/shared/tab-strip.css");
const inputs = await source("styles/shared/theme-inputs.css");
const light = foundation.match(/^:root\s*\{([^}]+)\}/s)[1];
const color = (name) => parseHexColor(light.match(new RegExp(`--app-${name}:\\s*(#[0-9a-f]+);`))[1]);

test("light canvas and resting panels stay pure white", () => {
  for (const name of ["canvas", "surface", "sidebar", "muted"]) {
    assert.match(light, new RegExp(`--app-${name}: #ffffff;`));
  }
});

test("light body text remains readable", () => {
  for (const text of ["text", "text-muted"]) {
    assert.ok(contrastRatio(color(text), color("surface")) >= 4.5, text);
  }
});

test("light blue palette matches the original theme", () => {
  for (const [name, value] of [
    ["accent", "#5d87ff"],
    ["accent-rgb", "93, 135, 255"],
    ["accent-hover", "#7c9eff"],
    ["accent-soft", "#eef2ff"],
  ]) {
    assert.ok(light.includes(`--app-${name}: ${value};`), name);
  }
});

test("global geometry belongs to foundation, not database filters", () => {
  for (const [name, size] of [["radius-sm", 4], ["radius", 8], ["radius-lg", 12]]) {
    assert.match(foundation, new RegExp(`--app-${name}: ${size}px;`));
    assert.equal((foundation.match(new RegExp(`--app-${name}:`, "g")) ?? []).length, 1);
    assert.doesNotMatch(filters, new RegExp(`--app-${name}:`));
  }
  assert.doesNotMatch(filters, /:root|\.app-frame\s*\{/);
  assert.match(foundation, /--el-border-radius-base: var\(--app-radius-sm\);/);
  assert.match(controls, /--app-control-radius: 6px;/);
  assert.match(controls, /--el-button-border-radius: var\(--app-control-radius\);/);
  assert.match(inputs, /border-radius: var\(--app-control-radius\);/);
});

test("physical input borders are included in the shared control height", async () => {
  assert.match(inputs, /\.el-input\s*\{\s*--el-input-inner-height: calc\(var\(--el-input-height, var\(--el-component-size\)\) - 4px\);/);
  const density = await source("styles/shared/compact-density.css");
  for (const [suffix, token] of [["", ""], ["--small", "-small"], ["--large", "-large"]]) {
    const rule = density.match(new RegExp(`\\.el-button${suffix}\\s*\\{([^}]+)\\}`))[1];
    assert.ok(rule.includes(`height: var(--el-component-size${token});`));
  }
  assert.match(density, /\.el-select--large \.el-select__wrapper\s*\{[^}]*min-height: var\(--el-component-size-large\);/s);
});

test("the native text-field rule leaves Element Plus inner inputs to their wrappers", () => {
  // Every #app native-input rule must skip Element's inner inputs, or the id
  // outranks Element's `border: none` and e.g. a date range shows two boxes.
  const rules = [...inputs.matchAll(/#app\s*:where\([^{]*\{/g)].map(match => match[0]);
  assert.equal(rules.length, 5);
  for (const rule of rules) {
    for (const cls of ["el-input__inner", "el-select__input", "el-textarea__inner", "el-range-input", "el-input-tag__input", "el-input-otp__input-field"]) {
      assert.match(rule, new RegExp(`:not\\(\\s*\\.${cls}\\s*\\)`), cls);
    }
  }
});

test("main workspaces stay on surface while navigation uses sidebar", () => {
  assert.match(surfaces, /\.settings-workspace\s*\{\s*background: var\(--app-surface\);/);
  assert.match(foundation, /\.app-activity-bar\s*\{[^}]*background: var\(--app-sidebar\);/s);
});

test("solid primary state rules exclude variants and disabled controls", () => {
  const rules = [...controls.matchAll(/([^{}]+)\{([^{}]*)\}/g)];
  const primary = rules.filter((rule) => rule[1].includes(".el-button--primary:not("));
  assert.equal(primary.length, 4);
  for (const [, selector] of primary) {
    assert.match(selector, /:not\(\.is-plain, \.is-link, \.is-text, \.is-disabled, :disabled\)/);
  }
  assert.match(primary.find((rule) => rule[1].includes(":hover"))[2], /var\(--app-accent-hover\)/);
  assert.match(primary.find((rule) => rule[1].includes(":active"))[2], /var\(--el-color-primary-dark-2\)/);
  assert.doesNotMatch(tabs, /\.el-button--primary/);
});

test("default-button rules do not match the size class shared by typed buttons", () => {
  // `.el-button--default` is also Element Plus's default size class, so primary buttons carry it too;
  // a rule keyed on it paints their hover text in --app-accent-hover, the same color as their hover fill.
  assert.doesNotMatch(controls, /\.el-button--default/);
});

test("shared buttons retain visible keyboard focus and panels avoid elevation shadows", () => {
  assert.match(controls, /\.el-button:focus-visible\s*\{[^}]*outline: 2px solid/s);
  assert.doesNotMatch(controls, /outline: none/);
  for (const [, value] of controls.matchAll(/box-shadow:\s*([^;]+);/g)) assert.equal(value, "none");
});

test("outlined filters use surface fill and brand edges", () => {
  assert.match(controls, /--el-radio-button-checked-bg-color: var\(--app-surface\);/);
  assert.match(controls, /--el-radio-button-checked-text-color: var\(--app-accent\);/);
  assert.match(controls, /--el-radio-button-checked-border-color: var\(--app-accent\);/);
  assert.match(controls, /:not\(:disabled\) \+ \.el-radio-button__inner\s*\{[^}]*outline-color: var\(--app-accent\);/s);
});

test("plain tags remain semantic and neutral tags exclude dark and hit variants", () => {
  assert.match(controls, /--el-tag-bg-color: color-mix\(in srgb, var\(--el-tag-text-color\) 6%, var\(--app-surface\)\);/);
  assert.match(controls, /--el-tag-border-color: color-mix\(in srgb, var\(--el-tag-text-color\) 24%, var\(--app-surface\)\);/);
  assert.match(controls, /\.el-tag\.el-tag--info:not\(\.el-tag--dark, \.is-hit\)/);
  assert.match(controls, /--el-tag-border-radius: var\(--app-radius-sm\);/);
});

test("segmented controls use neutral tracks and surface selections without workspace repainting", async () => {
  assert.match(controls, /--app-segmented-bg: var\(--app-hover\);/);
  assert.match(controls, /--app-segmented-selected-bg: var\(--app-surface\);/);
  assert.match(controls, /--app-segmented-selected-color: var\(--app-text\);/);
  assert.match(controls, /:root \.el-segmented\s*\{[^}]*--el-segmented-item-selected-bg-color: var\(--app-segmented-selected-bg\);/s);
  for (const path of [
    "styles/workspaces/http/workspace.css",
    "styles/workspaces/settings/dialog.css",
    "styles/workspaces/database/filters.css",
    "styles/workspaces/database/object-browser.css",
    "styles/workspaces/database/result-grid.css",
    "styles/shared/git-sftp-workspace-refinement.css",
    "styles/workspaces/sessions/workspace.css",
    "styles/workspaces/ssh/connection-form.css",
  ]) {
    const css = await source(path);
    assert.doesNotMatch(css, /--el-segmented-item-selected-(?:bg-color|color):/, path);
  }
  const http = await source("styles/workspaces/http/workspace.css");
  assert.match(http, /\.http-code-dialog__languages \.el-segmented\.is-inactive\s*\{[^}]*--app-segmented-selected-bg: transparent;/s);
  assert.match(http, /--app-segmented-selection-border: transparent;/);
});

test("neutral input hover and table surfaces stay separate from selection feedback", () => {
  assert.match(inputs, /--app-input-bg-hover: var\(--app-surface\);/);
  assert.match(inputs, /--app-input-border: var\(--app-border-strong\);/);
  assert.match(controls, /--el-table-header-bg-color: var\(--app-hover\);/);
  assert.match(controls, /--el-table-row-hover-bg-color: var\(--app-hover\);/);
  assert.match(controls, /--el-table-current-row-bg-color: var\(--app-accent-soft\);/);
});

test("drawer headers do not keep Element Plus's 32px gap above the body", () => {
  const header = controls.match(/\.el-drawer__header\s*\{([^}]+)\}/);
  assert.ok(header, "shared drawer header rule");
  assert.match(header[1], /margin-bottom: 0;/);
  assert.match(header[1], /border-bottom: 1px solid var\(--app-border\);/);
  assert.match(controls, /\.el-drawer__body\s*\{[^}]*padding-top: 16px;/s);
});

test("filled primary buttons use the theme's on-accent text colour", () => {
  const rule = controls.match(/\.el-button--primary:not\(\.is-plain, \.is-link, \.is-text, \.is-disabled, :disabled\)\s*\{([^}]+)\}/);
  assert.ok(rule, "filled primary rule");
  for (const variable of ["--el-button-text-color", "--el-button-hover-text-color", "--el-button-active-text-color"]) {
    assert.match(rule[1], new RegExp(`${variable}: var\\(--app-text-on-accent\\);`), variable);
  }
  // Dark themes switch to a light accent with dark on-accent text: keep it readable.
  for (const block of foundation.matchAll(/--app-accent:\s*(#[0-9a-f]{6});[^}]*?--app-text-on-accent:\s*(#[0-9a-f]{6});/gs)) {
    if (block[2] === "#ffffff") continue; // the light theme keeps white text on its own accent
    assert.ok(contrastRatio(parseHexColor(block[1]), parseHexColor(block[2])) >= 4.5, `${block[1]} on ${block[2]}`);
  }
});
