import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import {
  CUSTOM_EDITOR_THEME_FIELDS,
  DEFAULT_CUSTOM_EDITOR_THEMES,
  EDITOR_THEME_COLORS,
  EDITOR_THEME_CUSTOM,
  EDITOR_THEME_FOLLOW,
  EDITOR_THEME_MAX_ENTRIES,
  EDITOR_THEME_REQUIRED_COLORS,
  EDITOR_THEME_RULES_MAX,
  EDITOR_THEMES_CONTRIBUTION_POINT,
  collectEditorThemes,
  customEditorThemeData,
  customEditorThemeFrom,
  customEditorThemeLowContrast,
  editorMonacoTheme,
  editorThemeContrastProblems,
  editorThemePreviewSummary,
  editorThemeSwatches,
  isPluginEditorThemeId,
  monacoEditorThemeName,
  normalizeCustomEditorTheme,
  normalizeEditorThemePreference,
  validateEditorTheme,
} from "../src/domain/themes/editor-themes.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");

const theme = (overrides = {}) => ({
  id: "acme.themes.night",
  title: "Night",
  kind: "dark",
  colors: { "editor.background": "#1E1E1E", "editor.foreground": "#d4d4d4", "editorCursor.foreground": "#fff", "editor.selectionBackground": "#264f7880" },
  rules: [
    { token: "comment", foreground: "6A9955", fontStyle: " italic " },
    { token: "keyword", foreground: "569cd6" },
    { token: "string", foreground: "ce9178" },
  ],
  ...overrides,
});

test("editor theme domain mirrors the generated contract", async () => {
  const contract = JSON.parse(await readSource("../src/domain/plugins/contribution-points.json")).points;
  const editor = contract.editorThemes;
  assert.equal(EDITOR_THEMES_CONTRIBUTION_POINT, "editorThemes");
  assert.equal(editor.status, "enabled");
  assert.deepEqual(editor.kinds, ["declarative", "process"]);
  assert.equal(editor.scope, "global");
  assert.equal(EDITOR_THEME_MAX_ENTRIES, editor.maxEntries);
  assert.equal(EDITOR_THEME_RULES_MAX, editor.limits.tokenRulesMax);
  assert.deepEqual([...EDITOR_THEME_REQUIRED_COLORS], editor.lists.requiredColors);
  assert.deepEqual([...EDITOR_THEME_COLORS], editor.monacoColors);
  // One Monaco color allowlist for UI themes and editor themes.
  assert.deepEqual(editor.monacoColors, contract.themes.monacoColors);
});

test("the preference is follow, custom or a plugin theme id", () => {
  assert.equal(normalizeEditorThemePreference(undefined), EDITOR_THEME_FOLLOW);
  assert.equal(normalizeEditorThemePreference("nonsense"), EDITOR_THEME_FOLLOW);
  assert.equal(normalizeEditorThemePreference(EDITOR_THEME_CUSTOM), EDITOR_THEME_CUSTOM);
  assert.equal(normalizeEditorThemePreference("editor-themes.dracula"), "editor-themes.dracula");
  assert.equal(isPluginEditorThemeId("acme.themes.night"), true);
  assert.equal(isPluginEditorThemeId("acme.Night"), false);
  assert.equal(isPluginEditorThemeId(".night"), false);
});

test("a valid theme is normalized", () => {
  const result = validateEditorTheme(theme(), "acme.themes");
  assert.equal(result.ok, true);
  assert.equal(result.theme.colors["editor.background"], "#1e1e1e");
  assert.equal(result.theme.colors["editorCursor.foreground"], "#ffffff");
  assert.deepEqual(result.theme.rules[0], { token: "comment", foreground: "6a9955", fontStyle: "italic" });
  const minimal = validateEditorTheme(theme({ rules: undefined, colors: { "editor.background": "#ffffff", "editor.foreground": "#1f2328" }, kind: "light" }), "acme.themes");
  assert.equal(minimal.ok, true);
  assert.deepEqual(minimal.theme.rules, []);
});

test("theme validation rejects wrong ids, colors, rules and fields", () => {
  const cases = [
    [theme({ id: "other.night" }), "id must be"],
    [theme({ kind: "hc" }), "kind"],
    [theme({ colors: { "editor.background": "#000000" } }), "missing required color"],
    [theme({ colors: { ...theme().colors, "terminal.background": "#000000" } }), "unsupported key"],
    [theme({ colors: { ...theme().colors, "editor.foreground": "white" } }), "hex"],
    [theme({ colors: { ...theme().colors, "editor.background": "#1e1e1e80" } }), "opaque"],
    [theme({ rules: [{ token: "comment", foreground: "#6a9955" }] }), "without '#'"],
    [theme({ rules: [{ token: "comment", fontStyle: "oblique" }] }), "fontStyle"],
    [theme({ rules: [{ token: "comment", fontFamily: "x" }] }), "unknown field"],
    [theme({ fontFamily: "x" }), "unknown field"],
    [theme({ title: "a\u0007" }), "title"],
  ];
  for (const [raw, message] of cases) {
    const result = validateEditorTheme(raw, "acme.themes");
    assert.equal(result.ok, false, message);
    assert.match(result.error, new RegExp(message.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
});

test("contrast floors match the host", () => {
  const low = (colors, rules = []) => editorThemeContrastProblems({ colors: { "editor.background": "#1e1e1e", "editor.foreground": "#d4d4d4", ...colors }, rules });
  assert.deepEqual(low({}), []);
  assert.match(low({ "editor.foreground": "#3a3a3a" })[0], /editor.foreground on editor.background/);
  assert.match(low({ "editorCursor.foreground": "#252525" })[0], /editorCursor/);
  assert.match(low({ "editor.selectionBackground": "#d4d4d4" })[0], /selectionBackground/);
  assert.match(low({}, [{ token: "comment", foreground: "2a2a2a" }])[0], /rules\[0\]/);
  assert.match(low({}, [{ token: "string", foreground: "d4d4d4", background: "cccccc" }])[0], /its background/);
  const result = validateEditorTheme(theme({ rules: [{ token: "comment", foreground: "2a2a2a" }] }), "acme.themes");
  assert.equal(result.ok, false);
  assert.match(result.error, /contrast/);
});

test("collecting themes keeps host issues, skips invalid and duplicate entries and sorts dark before light", () => {
  const result = collectEditorThemes([
    {
      pluginId: "acme.themes",
      pluginName: "Acme",
      point: {
        entries: [theme(), theme({ id: "acme.themes.paper", title: "Paper", kind: "light", colors: { "editor.background": "#ffffff", "editor.foreground": "#1f2328" }, rules: [] }), theme(), theme({ id: "bad" })],
        issues: [{ entry: 4, id: "acme.themes.x", message: "host said no" }],
      },
    },
  ]);
  assert.deepEqual(result.themes.map((item) => item.id), ["acme.themes.night", "acme.themes.paper"]);
  assert.equal(result.themes[0].pluginName, "Acme");
  assert.deepEqual(result.issues.map((issue) => [issue.stage, issue.entry]), [["host", 4], ["frontend", 2], ["frontend", 3]]);
});

test("Monaco data fills the gutter, minimap and widgets from the editor background", () => {
  const data = editorMonacoTheme({ kind: "light", colors: { "editor.background": "#fafafa", "editor.foreground": "#222222" }, rules: [{ token: "comment", foreground: "777777" }] });
  assert.equal(data.base, "vs");
  assert.equal(data.inherit, true);
  assert.equal(data.colors["editorGutter.background"], "#fafafa");
  assert.equal(data.colors["minimap.background"], "#fafafa");
  assert.deepEqual(data.rules, [{ token: "comment", foreground: "777777" }]);
  assert.match(monacoEditorThemeName("acme.themes.night"), /^mincode-editor-[0-9a-f]{8}$/);
  assert.notEqual(monacoEditorThemeName("a.b"), monacoEditorThemeName("a.c"));
  assert.deepEqual(editorThemeSwatches(validateEditorTheme(theme(), "acme.themes").theme), ["#1e1e1e", "#d4d4d4", "#569cd6", "#ce9178"]);
});

test("the custom theme is normalized, converted and warns on low contrast", () => {
  assert.deepEqual(normalizeCustomEditorTheme(undefined), DEFAULT_CUSTOM_EDITOR_THEMES.dark);
  assert.equal(normalizeCustomEditorTheme({ kind: "light" }).colors.background, "#ffffff");
  const custom = normalizeCustomEditorTheme({
    kind: "dark",
    colors: { background: "#ABC", keyword: "red", selection: "#11223344", comment: "#11223344" },
  });
  assert.equal(custom.colors.background, "#aabbcc");
  assert.equal(custom.colors.keyword, DEFAULT_CUSTOM_EDITOR_THEMES.dark.colors.keyword, "named colors are ignored");
  assert.equal(custom.colors.selection, "#11223344", "selection may be translucent");
  assert.equal(custom.colors.comment, DEFAULT_CUSTOM_EDITOR_THEMES.dark.colors.comment, "syntax colors must be opaque");
  const data = customEditorThemeData(DEFAULT_CUSTOM_EDITOR_THEMES.dark);
  assert.equal(data.colors["editor.background"], DEFAULT_CUSTOM_EDITOR_THEMES.dark.colors.background);
  assert.ok(data.rules.some((rule) => rule.token === "comment" && rule.fontStyle === "italic"));
  assert.ok(Object.keys(data.colors).every((key) => EDITOR_THEME_COLORS.has(key)));
  // Both default custom themes pass the plugin contrast floors.
  for (const kind of ["dark", "light"]) {
    assert.deepEqual(customEditorThemeLowContrast(DEFAULT_CUSTOM_EDITOR_THEMES[kind]), [], kind);
    assert.deepEqual(editorThemeContrastProblems(customEditorThemeData(DEFAULT_CUSTOM_EDITOR_THEMES[kind])), [], kind);
  }
  const dim = { ...DEFAULT_CUSTOM_EDITOR_THEMES.dark, colors: { ...DEFAULT_CUSTOM_EDITOR_THEMES.dark.colors, comment: "#2a2b2e" } };
  assert.deepEqual(customEditorThemeLowContrast(dim).map((item) => item.field), ["comment"]);
  // Starting from a plugin theme copies its colors and token colors.
  const from = customEditorThemeFrom(validateEditorTheme(theme(), "acme.themes").theme);
  assert.equal(from.colors.background, "#1e1e1e");
  assert.equal(from.colors.keyword, "#569cd6");
  assert.equal(from.colors.function, DEFAULT_CUSTOM_EDITOR_THEMES.dark.colors.function);
  assert.equal(CUSTOM_EDITOR_THEME_FIELDS.length, 17);
});

test("the install preview lists inline titles and referenced files", () => {
  assert.deepEqual(editorThemePreviewSummary({ editorThemes: [{ title: "Night" }, { path: "editor-themes/paper.json" }, 3] }), ["Night", "editor-themes/paper.json"]);
  assert.deepEqual(editorThemePreviewSummary(undefined), []);
});

test("the setting, runtime and consumer are wired", async () => {
  const [index, main, uiTheme, runtime, row, panel, view, manager, zh, en] = await Promise.all([
    readSource("../src/services/contributions/index.ts"),
    readSource("../src/main.ts"),
    readSource("../src/services/ui-theme.ts"),
    readSource("../src/services/editor-theme.ts"),
    readSource("../src/components/settings/EditorThemeSettingRow.vue"),
    readSource("../src/components/settings/GeneralSettingsPanel.vue"),
    readSource("../src/views/SettingsWorkspaceView.vue"),
    readSource("../src/views/PluginManagerWorkspaceView.vue"),
    readSource("../src/i18n/locales/zh.ts"),
    readSource("../src/i18n/locales/en.ts"),
  ]);
  assert.match(index, /registerThemeContributionConsumer,[\s\S]*registerEditorThemeContributionConsumer,/);
  assert.match(main, /initializeEditorTheme\(\)/);
  // The editor theme setting wins over the UI theme in the one place editors resolve their theme.
  assert.match(uiTheme, /export function resolveMonacoEditorTheme[\s\S]*?const override = editorThemeOverride\(\);\s*if \(override\) return override;/);
  assert.match(runtime, /registerMonacoContributionHook/);
  assert.doesNotMatch(runtime, /import\("monaco-editor"\)|from "monaco-editor"/);
  assert.match(row, /EDITOR_THEME_FOLLOW[\s\S]*EDITOR_THEME_CUSTOM/);
  assert.match(panel, /<EditorThemeSettingRow/);
  assert.match(view, /editorColorTheme: normalizeEditorThemePreference\(raw\.editorColorTheme\)/);
  assert.match(view, /applyEditorThemePreference\(theme, custom\)/);
  assert.match(manager, /pluginContrib\.editorThemes\.previewSummary/);
  const keys = (source) => [...source.matchAll(/"((?:settings\.editorTheme|pluginContrib\.editorThemes)[^"]*)":/g)].map((match) => match[1]).sort();
  assert.deepEqual(keys(zh), keys(en));
  for (const field of CUSTOM_EDITOR_THEME_FIELDS) assert.ok(keys(zh).includes(`settings.editorTheme.field.${field}`), field);
});

test("the official editor-themes plugin ships at least ten themes of both kinds", async () => {
  const root = new URL("../../official-plugins/editor-themes/", import.meta.url);
  const manifest = JSON.parse(await readFile(new URL("plugin.json", root), "utf8"));
  assert.equal(manifest.runtime.type, "declarative");
  const entries = manifest.contributes.editorThemes;
  assert.ok(entries.length >= 10 && entries.length <= EDITOR_THEME_MAX_ENTRIES, `${entries.length} themes`);
  const files = new Set(await readdir(new URL("editor-themes/", root)));
  const raws = await Promise.all(
    entries.map(async (entry) => {
      assert.ok(files.has(entry.path.replace("editor-themes/", "")), entry.path);
      return JSON.parse(await readFile(new URL(entry.path, root), "utf8"));
    }),
  );
  const result = collectEditorThemes([{ pluginId: manifest.id, pluginName: manifest.name, point: { entries: raws, issues: [] } }]);
  assert.deepEqual(result.issues, []);
  assert.equal(result.themes.length, entries.length);
  assert.ok(result.themes.some((item) => item.kind === "dark") && result.themes.some((item) => item.kind === "light"));
  for (const item of result.themes) assert.ok(item.titleLocalized?.zh, `${item.id} zh title`);
});
