import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { detectProjectEditorLanguage } from "../src/domain/projects/editor-language.ts";

const definitionsRoot = fileURLToPath(new URL("../node_modules/monaco-editor/esm/vs/languages/definitions/", import.meta.url));
const registerAllPath = path.join(definitionsRoot, "register.all.js");
const runtimePath = fileURLToPath(new URL("../src/services/monaco-runtime.js", import.meta.url));

function stringValue(node) {
  if (!node) return undefined;
  return ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) ? node.text : undefined;
}

function propertyValue(object, name) {
  for (const property of object.properties) {
    if (!ts.isPropertyAssignment(property)) continue;
    const propertyName = stringValue(property.name) ?? (ts.isIdentifier(property.name) ? property.name.text : undefined);
    if (propertyName === name) return property.initializer;
  }
  return undefined;
}

function stringArray(node) {
  if (!node || !ts.isArrayLiteralExpression(node)) return undefined;
  return node.elements.map(stringValue).filter((value) => value !== undefined);
}

function parseLanguageDefinitions(filePath) {
  const source = ts.createSourceFile(
    filePath,
    fs.readFileSync(filePath, "utf8"),
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.JS
  );
  const definitions = [];

  function visit(node) {
    if (ts.isCallExpression(node) && node.arguments.length > 0) {
      const callee = node.expression;
      const isRegistration =
        (ts.isIdentifier(callee) && callee.text === "registerLanguage") ||
        (ts.isPropertyAccessExpression(callee) && callee.name.text === "register");
      const argument = node.arguments[0];
      if (isRegistration && ts.isObjectLiteralExpression(argument)) {
        const id = stringValue(propertyValue(argument, "id"));
        if (id) {
          definitions.push({
            id,
            extensions: stringArray(propertyValue(argument, "extensions")),
            filenames: stringArray(propertyValue(argument, "filenames")),
            filenamePatterns: stringArray(propertyValue(argument, "filenamePatterns")),
            firstLine: stringValue(propertyValue(argument, "firstLine")),
          });
        }
      }
    }
    ts.forEachChild(node, visit);
  }

  visit(source);
  return definitions;
}

function loadBundledLanguageDefinitions() {
  const registerAllSource = fs.readFileSync(registerAllPath, "utf8");
  const registrationFiles = [...registerAllSource.matchAll(/import ['"](.+?\/register\.js)['"];?/g)].map(
    ([, relativePath]) => path.resolve(definitionsRoot, relativePath)
  );
  const definitions = registrationFiles.flatMap(parseLanguageDefinitions);
  const jsonRegistration = fileURLToPath(new URL("../node_modules/monaco-editor/esm/vs/languages/features/json/register.js", import.meta.url));
  return {
    // monaco-runtime 自己补注册了 Monaco 不带的语言（如 .vue），一并纳入，
    // 否则检测结果会和运行时不一致。
    definitions: [
      ...definitions,
      ...parseLanguageDefinitions(jsonRegistration),
      ...parseLanguageDefinitions(runtimePath),
    ],
    registrationFiles,
  };
}

const { definitions, registrationFiles } = loadBundledLanguageDefinitions();

function detect(filePath, firstLine = "") {
  return detectProjectEditorLanguage(filePath, definitions, firstLine);
}

test("registers every language definition bundled with Monaco", () => {
  const runtimeSource = fs.readFileSync(runtimePath, "utf8");
  assert.match(runtimeSource, /languages\/definitions\/register\.all\.js/);
  assert.equal(registrationFiles.length, fs.readdirSync(definitionsRoot).filter((entry) => {
    return fs.existsSync(path.join(definitionsRoot, entry, "register.js"));
  }).length);
  assert.ok(definitions.length >= registrationFiles.length);
});

test("recognizes every extension and exact filename declared by bundled languages", () => {
  const extensions = new Set(definitions.flatMap((definition) => definition.extensions ?? []));
  const filenames = new Set(definitions.flatMap((definition) => definition.filenames ?? []));

  for (const extension of extensions) {
    assert.notEqual(detect(`/workspace/example${extension}`), "plaintext", extension);
  }
  for (const filename of filenames) {
    assert.notEqual(detect(`/workspace/${filename}`), "plaintext", filename);
  }
});

test("detects representative programming, data, infrastructure, and template languages", () => {
  const cases = {
    "/Volumes/LHD/code/sass/Test.php": "php",
    "src/main.CPP": "cpp",
    "src/report.abap": "abap",
    "infra/main.bicep": "bicep",
    "scripts/build.coffee": "coffeescript",
    "server/application.exs": "elixir",
    "server/library.fsx": "fsharp",
    "science/model.jl": "julia",
    "legacy/unit.pas": "pascal",
    "data/query.pq": "powerquery",
    "docs/index.rst": "restructuredtext",
    "hardware/core.sv": "systemverilog",
    "shaders/main.wgsl": "wgsl",
    "api/main.tsp": "typespec",
    "views/page.html.liquid": "liquid",
    "contracts/Token.sol": "sol",
    "proto/service.proto": "proto",
  };
  for (const [filePath, language] of Object.entries(cases)) {
    assert.equal(detect(filePath), language, filePath);
  }
});

test("preserves MinCode aliases beyond Monaco's built-in associations", () => {
  assert.equal(detect("config/settings.jsonc"), "json");
  assert.equal(detect("styles/theme.sass"), "scss");
  assert.equal(detect("scripts/start.zsh"), "shell");
  assert.equal(detect("deploy/Dockerfile.dev"), "dockerfile");
  assert.equal(detect("deploy/Containerfile"), "dockerfile");
  assert.equal(detect("/workspace/.env.dev"), "ini");
  assert.equal(detect("C:\\workspace\\.zshrc"), "shell");
});

// .vue 曾映射到 html，于是 html-ls 把 <script lang="ts"> 当内联 JavaScript 校验，
// `import type` 被误报成 TS5097，满屏红波浪线。SFC 只能交给懂它的 Volar。
test("routes .vue to its own language id, never to html", () => {
  assert.equal(detect("components/App.vue"), "vue");

  const runtimeSource = fs.readFileSync(runtimePath, "utf8");
  assert.match(runtimeSource, /register\(\{[^}]*id:\s*["']vue["']/s, "monaco-runtime must register a vue language");
});

test("uses registered first-line rules for extensionless scripts", () => {
  assert.equal(detect("scripts/run", "#!/usr/bin/env node"), "javascript");
  assert.equal(detect("scripts/analyze", "#!/usr/bin/python3"), "python");
  assert.equal(detect("README"), "plaintext");
});

test("plugin compound suffix patterns beat a shorter built-in extension only", async () => {
  const { detectCompoundSuffixLanguage } = await import("../src/domain/projects/editor-language.ts");
  const builtIns = [
    { id: "php", extensions: [".php"] },
    { id: "ini", extensions: [".conf"] },
    { id: "twig", extensions: [".html.twig"] },
    { id: "dockerfile", filenames: ["Dockerfile"] },
  ];
  const plugins = [
    { id: "blade", filenamePatterns: ["*.blade.php"] },
    { id: "html-twig", filenamePatterns: ["*.html.twig"] },
    { id: "broad", filenamePatterns: ["*.php", "**/views/*.php", "*.b?.php"] },
  ];
  assert.equal(detectCompoundSuffixLanguage("resources/views/welcome.blade.php", plugins, builtIns), "blade");
  assert.equal(detectCompoundSuffixLanguage("C:\\app\\Welcome.BLADE.PHP", plugins, builtIns), "blade");
  // Not a compound suffix, or not longer than what a built-in already claims.
  assert.equal(detectCompoundSuffixLanguage("app/User.php", plugins, builtIns), undefined);
  assert.equal(detectCompoundSuffixLanguage("resources/views/home.php", plugins, builtIns), undefined);
  assert.equal(detectCompoundSuffixLanguage("page.html.twig", plugins, builtIns), undefined);
  // The suffix alone is not a file with that suffix.
  assert.equal(detectCompoundSuffixLanguage(".blade.php", plugins, builtIns), undefined);
  assert.equal(detectCompoundSuffixLanguage("x.blade.php", [], builtIns), undefined);
});
