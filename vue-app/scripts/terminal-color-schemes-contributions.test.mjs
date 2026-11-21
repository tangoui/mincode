import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import {
  TERMINAL_COLOR_SCHEME_ALPHA_COLORS,
  TERMINAL_COLOR_SCHEME_LOCALES_MAX,
  TERMINAL_COLOR_SCHEME_MAX_ENTRIES,
  TERMINAL_COLOR_SCHEME_OPTIONAL_COLORS,
  TERMINAL_COLOR_SCHEME_REQUIRED_COLORS,
  TERMINAL_COLOR_SCHEME_TITLE_MAX_RUNES,
  TERMINAL_COLOR_SCHEMES_CONTRIBUTION_POINT,
  collectTerminalColorSchemes,
  isPluginTerminalColorSchemeId,
  terminalColorSchemeContrastProblem,
  terminalColorSchemePalette,
  terminalColorSchemePreviewSummary,
  terminalColorSchemeTitle,
  validateTerminalColorScheme,
} from "../src/domain/themes/terminal-color-schemes.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");

const DARK_COLORS = {
  foreground: "#D4D4D4",
  background: "#1e1e1e",
  cursor: "#ffffff",
  selectionBackground: "#264f78",
  black: "#000000",
  red: "#f14c4c",
  green: "#23d18b",
  yellow: "#f5f543",
  blue: "#3b8eea",
  magenta: "#d670d6",
  cyan: "#29b8db",
  white: "#e5e5e5",
  brightBlack: "#666666",
  brightRed: "#f14c4c",
  brightGreen: "#23d18b",
  brightYellow: "#f5f543",
  brightBlue: "#3b8eea",
  brightMagenta: "#d670d6",
  brightCyan: "#29b8db",
  brightWhite: "#ffffff",
};

const scheme = (overrides = {}) => ({ id: "acme.themes.night", title: "Night", kind: "dark", colors: { ...DARK_COLORS }, ...overrides });

test("terminal colour scheme domain mirrors the generated contract", async () => {
  const contract = JSON.parse(await readSource("../src/domain/plugins/contribution-points.json")).points.terminalColorSchemes;
  assert.equal(TERMINAL_COLOR_SCHEMES_CONTRIBUTION_POINT, "terminalColorSchemes");
  assert.equal(contract.status, "enabled");
  assert.deepEqual(contract.kinds, ["declarative", "process"]);
  assert.equal(contract.scope, "global");
  assert.equal(TERMINAL_COLOR_SCHEME_MAX_ENTRIES, contract.maxEntries);
  assert.equal(TERMINAL_COLOR_SCHEME_TITLE_MAX_RUNES, contract.limits.titleMaxRunes);
  assert.equal(TERMINAL_COLOR_SCHEME_LOCALES_MAX, contract.limits.localesMax);
  assert.deepEqual([...TERMINAL_COLOR_SCHEME_REQUIRED_COLORS], contract.lists.requiredColors);
  assert.deepEqual([...TERMINAL_COLOR_SCHEME_OPTIONAL_COLORS], contract.lists.optionalColors);
  assert.deepEqual([...TERMINAL_COLOR_SCHEME_ALPHA_COLORS], contract.lists.alphaColors);
});

test("plugin scheme ids are <pluginId>.<name> split on the last dot", () => {
  assert.equal(isPluginTerminalColorSchemeId("acme.themes.night"), true);
  assert.equal(isPluginTerminalColorSchemeId("acme.night-2"), true);
  for (const bad of ["default", "dark", ".night", "acme.", "acme.Night", "acme.night_x", "bad id.night", 3, undefined]) {
    assert.equal(isPluginTerminalColorSchemeId(bad), false, String(bad));
  }
});

test("a valid scheme is normalized and its palette gets defaults", () => {
  const result = validateTerminalColorScheme(scheme({ titleLocalized: { zh: "夜间" } }), "acme.themes");
  assert.equal(result.ok, true, result.error);
  assert.equal(result.scheme.colors.foreground, "#d4d4d4");
  assert.equal(terminalColorSchemeTitle(result.scheme, ["zh", "en"]), "夜间");
  const palette = terminalColorSchemePalette(result.scheme);
  assert.equal(palette.cursorAccent, "#1e1e1e");
  assert.equal(palette.selectionForeground, "#d4d4d4");
  assert.equal(palette.brightWhite, "#ffffff");
});

test("scheme validation rejects wrong ids, colours and fields", () => {
  const invalid = (raw, pluginId = "acme.themes") => validateTerminalColorScheme(raw, pluginId);
  assert.match(invalid(scheme({ id: "other.night" })).error, /id must be/);
  assert.match(invalid(scheme({ id: "acme.themes.Night" })).error, /id must be/);
  assert.match(invalid(scheme({ extra: 1 })).error, /unknown field/);
  assert.match(invalid(scheme({ kind: "dim" })).error, /kind/);
  const { red: _red, ...missing } = DARK_COLORS;
  assert.match(invalid(scheme({ colors: missing })).error, /missing required color "red"/);
  assert.match(invalid(scheme({ colors: { ...DARK_COLORS, purple: "#800080" } })).error, /unknown color/);
  assert.match(invalid(scheme({ colors: { ...DARK_COLORS, red: "red" } })).error, /hex color/);
  assert.match(invalid(scheme({ colors: { ...DARK_COLORS, foreground: "#d4d4d4ff" } })).error, /hex color/);
  // Alpha is allowed only for the selection colours.
  assert.equal(invalid(scheme({ colors: { ...DARK_COLORS, selectionBackground: "#264f78cc" } })).ok, true);
});

test("contrast floors are enforced", () => {
  assert.equal(terminalColorSchemeContrastProblem("dark", DARK_COLORS), undefined);
  assert.match(terminalColorSchemeContrastProblem("dark", { ...DARK_COLORS, foreground: "#333333" }), /foreground on background/);
  assert.match(terminalColorSchemeContrastProblem("dark", { ...DARK_COLORS, cursor: "#2a2a2a" }), /cursor/);
  assert.match(terminalColorSchemeContrastProblem("dark", { ...DARK_COLORS, blue: "#101060" }), /blue/);
  assert.match(terminalColorSchemeContrastProblem("dark", { ...DARK_COLORS, brightBlack: "#222222" }), /brightBlack/);
  // brightBlack is only checked for dark schemes; black/white never are.
  assert.equal(
    terminalColorSchemeContrastProblem("light", { ...DARK_COLORS, brightBlack: "#222222", black: "#1e1e1e" }),
    undefined,
  );
  assert.match(
    terminalColorSchemeContrastProblem("dark", { ...DARK_COLORS, selectionBackground: "#d0d0d0" }),
    /selectionBackground/,
  );
});

test("collecting schemes keeps host issues, skips invalid and duplicate entries and sorts by title", () => {
  const base = { pluginName: "Acme themes", version: "1.0.0", source: "local", runtime: "declarative", activeInWorkspace: true };
  const { schemes, issues } = collectTerminalColorSchemes([
    {
      ...base,
      pluginId: "acme.themes",
      point: {
        entries: [scheme({ id: "acme.themes.zulu", title: "Zulu" }), scheme(), scheme(), scheme({ id: "acme.themes.bad", colors: {} })],
        issues: [{ entry: 4, message: "file not found" }],
      },
    },
  ]);
  assert.deepEqual(schemes.map((item) => item.id), ["acme.themes.night", "acme.themes.zulu"]);
  assert.equal(schemes[0].pluginName, "Acme themes");
  assert.deepEqual(
    issues.map((issue) => [issue.stage, issue.entry]),
    [
      ["host", 4],
      ["frontend", 2],
      ["frontend", 3],
    ],
  );
});

test("the install preview lists inline titles and referenced files", () => {
  assert.deepEqual(
    terminalColorSchemePreviewSummary({
      terminalColorSchemes: [scheme(), { path: "terminal-schemes/day.json" }, 7],
    }),
    ["Night", "terminal-schemes/day.json"],
  );
  assert.deepEqual(terminalColorSchemePreviewSummary(undefined), []);
});

test("a stored scheme whose plugin is gone falls back to default without being rewritten", async () => {
  const theme = await readSource("../src/services/terminal-theme.ts");
  assert.match(theme, /isPluginTerminalColorSchemeId\(value\) \? value : "default"/);
  const panel = await readSource("../src/components/settings/TerminalSettingsPanel.vue");
  assert.match(panel, /pluginContrib\.terminalColorSchemes\.unavailable/);
});
