// contributes.themes: token parity with the shared contract, frontend
// validation and contrast floors, CSSOM apply/remove, missing-plugin
// fallback, the startup cache, Monaco/xterm mappings, the official themes
// package, the iframe token applier, and the data-theme wiring rule.
import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";
import {
  THEME_CONTRAST_FLOORS,
  THEME_CSS_PROPERTIES,
  THEME_LIMITS,
  THEME_MONACO_COLORS,
  THEME_OPTIONAL_TOKENS,
  THEME_REQUIRED_TOKENS,
  THEME_TERMINAL_KEYS,
} from "@/domain/themes/tokens";
import {
  collectPluginThemes,
  contrastRatio,
  monacoPluginThemeName,
  parseHexColor,
  pluginMonacoTheme,
  pluginTerminalTheme,
  pluginThemeCssProperties,
  pluginThemeTitle,
  validatePluginTheme,
} from "@/domain/themes/plugin-theme";

const repo = new URL("../../", import.meta.url);
const contract = JSON.parse(await readFile(new URL("plugins/api/contribution-points.json", repo), "utf8"));

function colors(overrides = {}) {
  return {
    canvas: "#f2f3f5", surface: "#ffffff", sidebar: "#ffffff", border: "#e5e6eb", "border-strong": "#c9cdd4",
    muted: "#f7f8fa", hover: "#f2f3f5", accent: "#3a64d8", "accent-hover": "#5d87ff", "accent-soft": "#eef2ff",
    text: "#1d2129", "text-regular": "#4e5969", "text-muted": "#6b7480", "text-disabled": "#c9cdd4", "text-on-accent": "#ffffff",
    ...overrides,
  };
}

function theme(id = "demo.paper", extra = {}) {
  return { id, title: "Paper", titleLocalized: { zh: "纸" }, kind: "light", colors: colors(), ...extra };
}

// ---------------------------------------------------------------------------
// Contract parity and pure domain

test("theme tokens and allowlists mirror the shared contribution contract", () => {
  const point = contract.points.themes;
  assert.equal(point.status, "enabled");
  assert.deepEqual([...THEME_REQUIRED_TOKENS], point.tokens.required);
  assert.deepEqual([...THEME_OPTIONAL_TOKENS], point.tokens.optional);
  assert.deepEqual([...THEME_MONACO_COLORS], point.monacoColors);
  assert.deepEqual([...THEME_TERMINAL_KEYS], point.terminalKeys);
  assert.equal(THEME_LIMITS.tokenRulesMax, point.limits.tokenRulesMax);
  assert.ok(THEME_CSS_PROPERTIES.includes("--app-accent-rgb"));
  assert.ok(THEME_CSS_PROPERTIES.every((name) => /^--app-[a-z0-9-]+$/.test(name)));
});

test("every themable token is a foundation.css token", async () => {
  const foundation = await readFile(new URL("../src/styles/foundation/foundation.css", import.meta.url), "utf8");
  for (const property of THEME_CSS_PROPERTIES) assert.ok(foundation.includes(`${property}:`), `${property} missing in foundation.css`);
});

test("validation rejects foreign ids, non-hex values, fonts and unknown keys", () => {
  assert.equal(validatePluginTheme(theme(), "demo").ok, true);
  const cases = [
    [theme("other.paper"), 'must start with "demo."'],
    [theme("demo."), "must start with"],
    [theme(undefined, { kind: "sepia" }), "kind"],
    [theme(undefined, { colors: colors({ surface: "white" }) }), "hex color"],
    [theme(undefined, { colors: colors({ surface: "#fff;background:url(x)" }) }), "hex color"],
    [theme(undefined, { colors: { ...colors(), font: "#ffffff" } }), 'unsupported key "font"'],
    [theme(undefined, { fontFamily: "serif" }), "fontFamily"],
    [theme(undefined, { editor: { colors: { "editor.fontSize": "#ffffff" } } }), "editor.fontSize"],
    [theme(undefined, { editor: { rules: [{ token: "a", fontFamily: "x" }] } }), "fontFamily"],
    [theme(undefined, { editor: { rules: [{ token: "a", foreground: "#ffffff" }] } }), "without '#'"],
    [theme(undefined, { editor: { rules: [{ token: "a", fontStyle: "bold bold" }] } }), "fontStyle"],
    [theme(undefined, { terminal: { fontSize: "#ffffff" } }), "fontSize"],
  ];
  for (const [raw, message] of cases) {
    const result = validatePluginTheme(raw, "demo");
    assert.equal(result.ok, false, JSON.stringify(raw).slice(0, 80));
    assert.match(result.error, new RegExp(message.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  const missing = colors();
  delete missing.text;
  assert.match(validatePluginTheme(theme(undefined, { colors: missing }), "demo").error, /missing required token "text"/);
});

test("contrast floors match WCAG math and the Go validator", () => {
  assert.ok(Math.abs(contrastRatio(parseHexColor("#000"), parseHexColor("#fff")) - 21) < 0.01);
  assert.ok(Math.abs(contrastRatio(parseHexColor("#767676"), parseHexColor("#ffffff")) - 4.54) < 0.01);
  assert.deepEqual(THEME_CONTRAST_FLOORS.map((floor) => floor.min), [4.5, 3, 3, 3]);
  assert.match(validatePluginTheme(theme(undefined, { colors: colors({ text: "#999999" }) }), "demo").error, /contrast of text on surface/);
  assert.match(validatePluginTheme(theme(undefined, { colors: colors({ text: "#1d212940" }) }), "demo").error, /contrast of text on surface/);
  assert.match(validatePluginTheme(theme(undefined, { colors: colors({ "text-on-accent": "#7090ee" }) }), "demo").error, /text-on-accent on accent/);
  assert.match(validatePluginTheme(theme(undefined, { colors: colors({ danger: "#ffb0b0" }) }), "demo").error, /danger on surface/);
});

test("collectPluginThemes keeps host issues, skips invalid and duplicate entries, caps per plugin", () => {
  const many = Array.from({ length: THEME_LIMITS.maxThemesPerPlugin + 2 }, (_, index) => theme(`demo.t${index}`));
  const result = collectPluginThemes([
    { pluginId: "demo", pluginName: "Demo", point: { entries: [theme("demo.a"), theme("demo.a"), theme("x.b"), ...many], issues: [{ entry: 9, id: "demo.z", message: "host rejected" }] } },
  ]);
  assert.equal(result.themes.length, THEME_LIMITS.maxThemesPerPlugin);
  assert.equal(result.themes[0].pluginName, "Demo");
  assert.deepEqual(result.issues.map((issue) => issue.stage), ["host", "frontend", "frontend", "frontend", "frontend", "frontend"]);
  assert.match(result.issues[1].message, /duplicate/);
});

test("CSS, Monaco and xterm mappings only carry validated colors", () => {
  const { theme: valid } = validatePluginTheme(theme(undefined, {
    kind: "light",
    editor: { colors: { "editor.background": "#fafafa" }, rules: [{ token: "comment", foreground: "6b7480", fontStyle: "italic" }] },
    terminal: { background: "#fff", red: "#c9362b" },
  }), "demo");
  const css = pluginThemeCssProperties(valid);
  assert.equal(css["--app-surface"], "#ffffff");
  assert.equal(css["--app-accent-rgb"], "58, 100, 216");
  assert.ok(Object.keys(css).every((name) => THEME_CSS_PROPERTIES.includes(name)));

  const monaco = pluginMonacoTheme(valid);
  assert.equal(monaco.base, "vs");
  assert.equal(monaco.inherit, true);
  assert.equal(monaco.colors["editor.background"], "#fafafa");
  assert.equal(monaco.colors["editor.foreground"], "#1d2129");
  assert.deepEqual(monaco.rules, [{ token: "comment", foreground: "6b7480", fontStyle: "italic" }]);
  assert.match(monacoPluginThemeName("demo.paper"), /^mincode-plugin-[0-9a-f]{8}$/);
  assert.equal(monacoPluginThemeName("demo.paper"), monacoPluginThemeName("demo.paper"));
  assert.notEqual(monacoPluginThemeName("demo.paper"), monacoPluginThemeName("demo.night"));

  const xterm = pluginTerminalTheme(valid, { red: "#000000", blue: "#0000ff" });
  assert.equal(xterm.background, "#fff");
  assert.equal(xterm.red, "#c9362b");
  assert.equal(xterm.blue, "#0000ff");
  assert.equal(xterm.cursor, "#3a64d8");
  assert.equal(pluginThemeTitle(valid, "zh"), "纸");
  assert.equal(pluginThemeTitle(valid, "en"), "Paper");
});

test("official themes package validates in the frontend stage", async () => {
  const root = new URL("official-plugins/themes/", repo);
  const manifest = JSON.parse(await readFile(new URL("plugin.json", root), "utf8"));
  const entries = await Promise.all(manifest.contributes.themes.map(async (reference) => JSON.parse(await readFile(new URL(reference.path, root), "utf8"))));
  const result = collectPluginThemes([{ pluginId: manifest.id, pluginName: manifest.name, point: { entries, issues: [] } }]);
  assert.deepEqual(result.issues, []);
  assert.equal(result.themes.length, manifest.contributes.themes.length);
  assert.equal(result.themes.length, 16);
  assert.ok(result.themes.some((item) => item.kind === "dark") && result.themes.some((item) => item.kind === "light"));
});

// ---------------------------------------------------------------------------
// services/ui-theme.ts with a stub DOM

function createLocalStorage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
  };
}

function createDom() {
  const properties = new Map();
  const classes = new Set();
  const events = [];
  const root = {
    dataset: {},
    style: {
      colorScheme: "",
      setProperty: (name, value) => properties.set(name, value),
      removeProperty: (name) => properties.delete(name),
    },
    classList: { toggle: (name, on) => (on ? classes.add(name) : classes.delete(name)) },
  };
  globalThis.document = { documentElement: root };
  globalThis.window = {
    localStorage: createLocalStorage(),
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    dispatchEvent: (event) => events.push(event),
  };
  return { root, properties, classes, events };
}

async function loadUiTheme() {
  const source = (await readFile(new URL("../src/services/ui-theme.ts", import.meta.url), "utf8"))
    .replace(/import \{ hydrateSettingsSnapshot \} from "@\/services\/settings-persistence";/, "const hydrateSettingsSnapshot = async () => ({});")
    .replace(
      /import \{ registerDesktopBrowserSnapshotKeys, writePersistedBrowserSnapshot \} from "@\/services\/persistent-store";/,
      "const registerDesktopBrowserSnapshotKeys = () => {}; const writePersistedBrowserSnapshot = (key, value) => window.localStorage.setItem(key, JSON.stringify(value));",
    )
    // No editor theme setting: editors follow the UI theme.
    .replace(/import \{ editorThemeOverride \} from "@\/services\/editor-theme-override";/, "const editorThemeOverride = () => undefined;");
  assert.ok(!source.includes("@/services/"), "ui-theme.ts gained a service import the test does not stub");
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText;
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}#${Math.random()}`);
}

function record(id = "demo.night", kind = "dark", pluginId = id.split(".")[0]) {
  const dark = colors({ canvas: "#101218", surface: "#181b22", sidebar: "#14161c", text: "#e8eaf0", "text-muted": "#9aa1ad", "accent-soft": "#223055" });
  const { theme: valid } = validatePluginTheme(theme(id, { kind, colors: kind === "dark" ? dark : colors() }), pluginId);
  assert.ok(valid, "fixture theme must validate");
  return { ...valid, pluginId, pluginName: "Demo" };
}

test("applying a plugin theme writes tokens via CSSOM and switching removes them", async () => {
  const dom = createDom();
  const ui = await loadUiTheme();
  ui.setAvailablePluginThemes([record()]);
  ui.applyUiTheme("demo.night");
  assert.equal(dom.root.dataset.theme, "plugin");
  assert.equal(dom.root.dataset.themeKind, "dark");
  assert.equal(dom.root.dataset.themePreference, "demo.night");
  assert.equal(dom.root.style.colorScheme, "dark");
  assert.ok(dom.classes.has("dark"));
  assert.equal(dom.properties.get("--app-surface"), "#181b22");
  assert.ok([...dom.properties.keys()].every((name) => THEME_CSS_PROPERTIES.includes(name)));
  assert.equal(ui.resolveMonacoEditorTheme("demo.night"), monacoPluginThemeName("demo.night"));
  assert.equal(ui.currentPluginThemeTokens()["--app-surface"], "#181b22");
  const changed = dom.events.filter((event) => event.type === ui.UI_THEME_CHANGED_EVENT).at(-1);
  assert.equal(changed.detail, "demo.night");

  ui.applyUiTheme("light");
  assert.equal(dom.properties.size, 0);
  assert.equal(dom.root.dataset.theme, "light");
  assert.equal(dom.root.dataset.themeKind, "light");
  assert.ok(!dom.classes.has("dark"));
  assert.equal(ui.currentPluginThemeTokens(), undefined);
});

test("a missing plugin theme keeps the id and falls back by stored kind", async () => {
  const dom = createDom();
  window.localStorage.setItem("app_settings_v2", JSON.stringify({ uiTheme: "gone.night", uiThemeKind: "dark" }));
  const ui = await loadUiTheme();
  const stored = ui.readStoredUiTheme();
  assert.equal(stored, "gone.night");
  ui.setAvailablePluginThemes([]);
  ui.applyUiTheme(stored);
  assert.equal(dom.root.dataset.theme, "dark");
  assert.equal(dom.root.dataset.themePreference, "gone.night");
  assert.equal(ui.unavailablePluginThemeId(), "gone.night");
  assert.equal(ui.resolveMonacoEditorTheme("gone.night"), "vs-dark");
  assert.equal(dom.properties.size, 0);
  assert.ok(dom.events.some((event) => event.type === ui.PLUGIN_THEMES_CHANGED_EVENT));

  // The plugin arrives: the same preference now applies the theme.
  ui.setAvailablePluginThemes([record("gone.night")]);
  assert.equal(dom.root.dataset.theme, "plugin");
  assert.equal(ui.unavailablePluginThemeId(), undefined);
  assert.equal(ui.normalizeUiTheme("not a theme"), "light");
});

test("the startup cache paints the last plugin theme before the first snapshot", async () => {
  createDom();
  let ui = await loadUiTheme();
  ui.setAvailablePluginThemes([record()]);
  ui.applyUiTheme("demo.night");
  const cache = window.localStorage.getItem(ui.PLUGIN_THEME_CACHE_KEY);
  assert.ok(cache);

  // Fresh module (next launch), same storage.
  const storage = window.localStorage;
  const dom = createDom();
  window.localStorage = storage;
  ui = await loadUiTheme();
  ui.applyUiTheme("demo.night");
  assert.equal(dom.root.dataset.theme, "plugin");
  assert.equal(dom.properties.get("--app-surface"), "#181b22");

  // A tampered cache is ignored.
  storage.setItem(ui.PLUGIN_THEME_CACHE_KEY, JSON.stringify({ version: 1, theme: { ...JSON.parse(cache).theme, colors: { surface: "url(x)" } } }));
  const next = createDom();
  window.localStorage = storage;
  ui = await loadUiTheme();
  ui.applyUiTheme("demo.night");
  assert.equal(next.root.dataset.theme, "light");
  assert.equal(next.properties.size, 0);
});

// ---------------------------------------------------------------------------
// Plugin iframe tokens and wiring rules

test("plugin iframes re-validate theme tokens and apply them through CSSOM", async () => {
  const { applyThemeTokens, validThemeTokens } = await import("../src/plugin-ui/theme-tokens.js");
  assert.deepEqual(validThemeTokens({ "--app-surface": "#fff", "--app-accent-rgb": "1, 2, 3", "--app-text": "red", "--x": "#fff", "--app-bad;": "#fff" }), {
    "--app-surface": "#fff",
    "--app-accent-rgb": "1, 2, 3",
  });
  const properties = new Map();
  const root = { style: { setProperty: (name, value) => properties.set(name, value), removeProperty: (name) => properties.delete(name) } };
  applyThemeTokens({ "--app-surface": "#111111", "--app-text": "#eeeeee" }, root);
  assert.equal(properties.size, 2);
  applyThemeTokens(undefined, root);
  assert.equal(properties.size, 0);
  const transport = await readFile(new URL("../src/plugin-ui/transport.js", import.meta.url), "utf8");
  assert.match(transport, /applyThemeTokens\(value\?\.themeTokens\)/);
});

test("host context sends a plugin theme as its kind plus tokens", async () => {
  const source = await readFile(new URL("../src/services/plugin-ui-context.ts", import.meta.url), "utf8");
  assert.match(source, /dataset\.theme === "plugin" \? themeKind/);
  assert.match(source, /currentPluginThemeTokens\(\)/);
});

async function styleFiles(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const url = new URL(entry.name + (entry.isDirectory() ? "/" : ""), directory);
    if (entry.isDirectory()) files.push(...(await styleFiles(url)));
    else if (/\.(css|vue)$/.test(entry.name)) files.push(url);
  }
  return files;
}

test("kind-dependent CSS outside foundation.css selects data-theme-kind, not data-theme", async () => {
  const src = new URL("../src/", import.meta.url);
  const offenders = [];
  for (const file of await styleFiles(src)) {
    if (file.pathname.endsWith("/styles/foundation/foundation.css")) continue;
    const text = await readFile(file, "utf8");
    if (/data-theme="/.test(text)) offenders.push(file.pathname.slice(src.pathname.length));
  }
  assert.deepEqual(offenders, [], "use :root[data-theme-kind=\"dark\"] so plugin themes are covered");
});

test("Monaco and terminal consumers resolve plugin themes", async () => {
  const monaco = await readFile(new URL("../src/services/monaco-themes.ts", import.meta.url), "utf8");
  assert.match(monaco, /registerMonacoContributionHook/);
  assert.match(monaco, /defineTheme\(name, data\)/);
  const terminal = await readFile(new URL("../src/services/terminal-theme.ts", import.meta.url), "utf8");
  assert.match(terminal, /pluginTerminalTheme\(plugin/);
  const index = await readFile(new URL("../src/services/contributions/index.ts", import.meta.url), "utf8");
  assert.match(index, /registerThemeContributionConsumer/);
});
