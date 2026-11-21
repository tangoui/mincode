import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";
import {
  collectFileIconThemes,
  FILE_ICON_LIMITS,
  fileIconExtensionCandidates,
  fileIconPreviewSummary,
  fileIconThemeTitle,
  resolveFileIconDefinition,
  validateFileIconTheme,
} from "../src/domain/projects/file-icon-theme.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");
const key = (n) => n.toString(16).padStart(64, "0");

function entry(overrides = {}) {
  return {
    id: "acme.icons",
    title: "Acme",
    titleLocalized: { zh: "Acme 图标" },
    monochrome: false,
    icons: { file: key(1), ts: key(2), dts: key(3), folder: key(4), "folder-open": key(5), src: key(6), "src-open": key(7), root: key(8), "ts-light": key(9), makefile: key(10) },
    file: "file",
    folder: "folder",
    folderExpanded: "folder-open",
    rootFolder: "root",
    fileExtensions: { ts: "ts", "d.ts": "dts" },
    fileNames: { makefile: "makefile" },
    folderNames: { src: "src" },
    folderNamesExpanded: { src: "src-open" },
    light: { fileExtensions: { ts: "ts-light" } },
    ...overrides,
  };
}

function theme(overrides) {
  const result = validateFileIconTheme(entry(overrides), "acme");
  assert.equal(result.ok, true, result.error);
  return result.theme;
}

const definition = (t, target, light = false) => resolveFileIconDefinition(t, target, light)?.definition;

test("files resolve fileNames, then the longest compound extension, then file", () => {
  const t = theme();
  assert.equal(definition(t, { name: "Makefile" }), "makefile");
  assert.equal(definition(t, { name: "index.d.ts" }), "dts");
  assert.equal(definition(t, { name: "INDEX.TS" }), "ts");
  assert.equal(definition(t, { name: "README" }), "file");
  assert.equal(definition(t, { name: "a.unknown" }), "file");
  assert.deepEqual(fileIconExtensionCandidates("a.d.ts"), ["d.ts", "ts"]);
  assert.deepEqual(fileIconExtensionCandidates(".eslintrc"), ["eslintrc"]);
  assert.deepEqual(fileIconExtensionCandidates("trailing."), []);
  assert.equal(resolveFileIconDefinition(t, { name: "a.ts" }, false)?.key, key(2));
});

test("folders resolve root, names (expanded first), then folder defaults", () => {
  const t = theme();
  assert.equal(definition(t, { name: "src", isDir: true }), "src");
  assert.equal(definition(t, { name: "SRC", isDir: true, expanded: true }), "src-open");
  assert.equal(definition(t, { name: "lib", isDir: true }), "folder");
  assert.equal(definition(t, { name: "lib", isDir: true, expanded: true }), "folder-open");
  assert.equal(definition(t, { name: "proj", isDir: true, root: true }), "root");
  // rootFolderExpanded is unset: expanded roots fall back to rootFolder.
  assert.equal(definition(t, { name: "proj", isDir: true, root: true, expanded: true }), "root");
  const noFolder = theme({ folder: undefined, folderExpanded: undefined, rootFolder: undefined, folderNames: {}, folderNamesExpanded: {} });
  assert.equal(resolveFileIconDefinition(noFolder, { name: "lib", isDir: true }, false), undefined, "no folder icon means built-in fallback");
});

test("light overrides apply only in light mode and fall through to the base", () => {
  const t = theme();
  assert.equal(definition(t, { name: "a.ts" }, false), "ts");
  assert.equal(definition(t, { name: "a.ts" }, true), "ts-light");
  assert.equal(definition(t, { name: "a.d.ts" }, true), "dts", "longer base extension still wins over a shorter light one");
  assert.equal(definition(t, { name: "Makefile" }, true), "makefile");
  const lightFile = theme({ light: { file: "folder" } });
  assert.equal(definition(lightFile, { name: "x" }, true), "folder");
  assert.equal(definition(lightFile, { name: "x" }, false), "file");
});

test("prototype-shaped names never reach Object.prototype", () => {
  const t = theme({ fileNames: {} });
  assert.equal(definition(t, { name: "__proto__" }), "file");
  assert.equal(definition(t, { name: "constructor" }), "file");
  assert.equal(definition(t, { name: "x.constructor" }), "file");
});

test("revalidation rejects malformed snapshot entries", () => {
  const bad = [
    [{ id: "other.icons" }, "id"],
    [{ id: "acme." }, "id"],
    [{ title: "" }, "title"],
    [{ title: "a\u202eb" }, "title"],
    [{ icons: {} }, "icons"],
    [{ icons: { file: "../x" } }, "asset key"],
    [{ icons: { "bad id": key(1) }, file: "bad id" }, "asset key"],
    [{ file: "missing" }, "unknown"],
    [{ fileNames: { x: "missing" } }, "unknown"],
    [{ fileExtensions: { ".ts": "ts" } }, "invalid"],
    [{ folderNames: { "a/b": "folder" } }, "invalid"],
    [{ folderNames: { Src: "folder", src: "folder" } }, "repeats"],
    [{ light: "dark" }, "light"],
    [{ light: { folder: "nope" } }, "light.folder"],
  ];
  for (const [overrides, message] of bad) {
    const result = validateFileIconTheme(entry(overrides), "acme");
    assert.equal(result.ok, false, JSON.stringify(overrides));
    assert.match(result.error, new RegExp(message.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  const withoutFile = entry();
  delete withoutFile.file;
  assert.equal(validateFileIconTheme(withoutFile, "acme").ok, false);
  assert.equal(validateFileIconTheme(null, "acme").ok, false);
});

test("collect keeps host issues, drops duplicates and caps themes per plugin", () => {
  const entries = Array.from({ length: FILE_ICON_LIMITS.themesPerPlugin + 1 }, (_, index) => entry({ id: `acme.t${index}` }));
  entries.push(entry({ id: "acme.t0" }), { bogus: true });
  const { themes, issues } = collectFileIconThemes([
    { pluginId: "acme", pluginName: "Acme", point: { entries, issues: [{ entry: 9, id: "acme.skipped", message: "svg: <script> is not allowed" }] } },
  ]);
  assert.equal(themes.length, FILE_ICON_LIMITS.themesPerPlugin);
  assert.equal(themes[0].pluginName, "Acme");
  assert.deepEqual(issues.map((issue) => issue.stage), ["host", "frontend", "frontend", "frontend"]);
});

test("collect accepts six themes and rejects the seventh without dropping Compact or Outline", () => {
  const names = ["color", "mono", "material", "classic", "compact", "outline", "extra"];
  const entries = names.map((name) => entry({ id: `acme.${name}` }));
  const { themes, issues } = collectFileIconThemes([
    { pluginId: "acme", pluginName: "Acme", point: { entries, issues: [] } },
  ]);
  assert.deepEqual(themes.map((item) => item.id), names.slice(0, 6).map((name) => `acme.${name}`));
  assert.equal(issues.length, 1);
  assert.equal(issues[0].stage, "frontend");
});

test("titles and install preview", () => {
  const t = theme();
  assert.equal(fileIconThemeTitle(t, "zh-CN"), "Acme 图标");
  assert.equal(fileIconThemeTitle(t, "en"), "Acme");
  assert.deepEqual(fileIconPreviewSummary({ fileIcons: [{ title: "Acme" }, { path: "fileicons/b.json" }] }), { count: 2, titles: ["Acme", "fileicons/b.json"] });
  assert.equal(fileIconPreviewSummary({}), null);
});

test("limits mirror the contract", async () => {
  const contract = JSON.parse(await readSource("../src/domain/plugins/contribution-points.json"));
  const point = contract.points.fileIcons;
  assert.equal(point.status, "enabled");
  assert.equal(FILE_ICON_LIMITS.themesPerPlugin, point.maxEntries);
  for (const name of ["titleMaxRunes", "iconsPerTheme", "mappingsMax", "keyMaxLength"]) {
    assert.equal(FILE_ICON_LIMITS[name], point.limits[name], name);
  }
});

test("icons render only in image context", async () => {
  const component = await readSource("../src/components/projects/ProjectFileIcon.vue");
  for (const forbidden of [/v-html/, /innerHTML/, /outerHTML/, /<object\b/, /<embed\b/, /<iframe\b/, /insertAdjacentHTML/, /DOMParser/]) {
    assert.doesNotMatch(component, forbidden);
  }
  assert.match(component, /<img[\s\S]*?:src="pluginIconURL"/);
  assert.match(component, /maskImage/);
  const store = await readSource("../src/stores/file-icon-theme.ts");
  assert.match(store, /startsWith\("blob:"\)/, "only blob: URLs reach the renderer");
  const consumer = await readSource("../src/services/contributions/file-icons.ts");
  assert.match(consumer, /export function applyFileIconThemePreference\(themeId: string\): void/);
  assert.match(consumer, /data-theme-kind/);
  const index = await readSource("../src/services/contributions/index.ts");
  assert.match(index, /\/\/ fileIcons \(Phase D\)\n\s*registerFileIconContributionConsumer,/);
});
