import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  SNIPPET_MAX_SUGGESTIONS,
  buildSnippetRegistry,
  parseSnippetBody,
  selectSnippetSuggestions,
  validateSnippetBody,
} from "../src/domain/editor/snippets.ts";
import {
  attachMonacoSnippets,
  monacoSnippetIssues,
  monacoSnippetLanguages,
  setMonacoSnippets,
} from "../src/services/monaco-snippets.ts";

const repo = fileURLToPath(new URL("../../", import.meta.url));
const corpus = join(repo, "tools/verify/fixtures/contributions");

test("snippet body syntax: supported forms", () => {
  for (const body of [
    "plain text",
    "console.log($1);$0",
    "${1}",
    "for (let ${1:i} = 0; ${1:i} < ${2:n}; ${1:i}++) {\n\t$0\n}",
    "${1:outer ${2:inner ${3:deepest}}}",
    "${1|public,private,protected|} ${2:name}",
    "${1|a\\,b,c\\|d|}",
    "$TM_SELECTED_TEXT ${TM_CURRENT_LINE} ${TM_CURRENT_WORD:word}",
    "$TM_LINE_INDEX $TM_LINE_NUMBER $TM_FILENAME ${TM_FILENAME_BASE}",
    "$CURRENT_YEAR-$CURRENT_MONTH-$CURRENT_DATE $CURRENT_HOUR:$CURRENT_MINUTE:$CURRENT_SECOND $CURRENT_SECONDS_UNIX",
    "$UUID $RANDOM $RANDOM_HEX",
    "$LINE_COMMENT x $BLOCK_COMMENT_START y $BLOCK_COMMENT_END",
    "cost: 5$ and \\$1 and \\} and \\\\",
    "${TM_SELECTED_TEXT:${1:fallback}}",
  ]) {
    assert.equal(validateSnippetBody(body), undefined, body);
  }
  assert.equal(parseSnippetBody("a ${1:b ${2:c}} ${3|x,y|} $TM_FILENAME ${UUID:id} \\$9"), "a b c x TM_FILENAME id $9");
});

test("snippet body syntax: rejected forms", () => {
  for (const [body, message] of [
    ["$CLIPBOARD", "CLIPBOARD"],
    ["${CLIPBOARD}", "CLIPBOARD"],
    ["${CLIPBOARD:x}", "CLIPBOARD"],
    ["${1/(.*)/${1:/upcase}/}", "regex transforms"],
    ["${TM_FILENAME/(.*)/$1/}", "regex transforms"],
    ["$TM_FILEPATH", "unsupported variable TM_FILEPATH"],
    ["$TM_DIRECTORY", "unsupported variable"],
    ["$WORKSPACE_NAME", "unsupported variable"],
    ["${UNKNOWN}", "unsupported variable UNKNOWN"],
    ["${1:unterminated", "unterminated placeholder"],
    ["${1|a,b", "unterminated choice"],
    ["${1|a,b|", 'expected "}"'],
    ["${||}", '"${" must be followed'],
    ["${", '"${" must be followed'],
    ["${1x}", "malformed tabstop"],
    ["${UUID x}", "malformed variable"],
    [`${"${1:".repeat(20)}x${"}".repeat(20)}`, "nest deeper"],
  ]) {
    const error = validateSnippetBody(body);
    assert.ok(error?.includes(message), `${body}: ${error}`);
  }
});

function contribution(pluginId, entries, issues = []) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "declarative",
    activeInWorkspace: true,
    point: { fingerprint: pluginId, entries, issues },
  };
}

function snippet(name, prefixes, body, description) {
  return { name, prefixes, body, ...(description ? { description } : {}) };
}

test("registry groups per language, skips bad snippets and reports issues", () => {
  const registry = buildSnippetRegistry([
    contribution("a", [
      { language: "typescript", path: "snippets/ts.json", snippets: [
        snippet("Log", ["log", "clog"], "console.log($1);", "Log it"),
        snippet("Paste", ["paste"], "$CLIPBOARD"),
        snippet("Bad prefix", ["a b"], "x"),
      ] },
      { language: "Bad Language", path: "snippets/x.json", snippets: [] },
      "garbage",
    ], [{ point: "snippets", entry: 3, id: "sql:snippets/missing.json", message: "missing" }]),
    contribution("b", [{ language: "typescript", path: "snippets/b.json", snippets: [snippet("If", ["if"], "if ($1) {\n\t$0\n}")] }]),
  ]);
  const ts = registry.byLanguage.get("typescript");
  assert.deepEqual(ts.map((item) => `${item.pluginId}:${item.prefix}`), ["a:log", "a:clog", "b:if"]);
  assert.equal(ts[0].pluginName, "a name");
  assert.equal(ts[0].preview, "console.log();");
  assert.equal(ts[0].description, "Log it");
  assert.deepEqual([...registry.byLanguage.keys()], ["typescript"]);
  const messages = registry.issues.map((issue) => `${issue.stage}:${issue.entry}:${issue.snippet ?? ""}:${issue.message.split(" (")[0]}`);
  assert.deepEqual(messages, [
    "host:3::missing",
    "frontend:0:Paste:unsupported snippet body: the CLIPBOARD variable is not allowed",
    "frontend:0:Bad prefix:malformed snippet",
    "frontend:1::malformed snippet file entry",
    "frontend:2::malformed snippet file entry",
  ]);
  assert.equal(registry.issues[0].file, "sql:snippets/missing.json");
});

test("suggestion selection matches at word boundaries, orders and caps", () => {
  const items = ["log", "sel*", "one", "logger"].map((prefix, index) => ({
    language: "typescript", prefix, name: prefix, body: prefix, preview: prefix, pluginId: "p", pluginName: "P", entry: index,
  }));
  const pick = (typed) => selectSnippetSuggestions(items, typed).map(({ item, replace }) => `${item.prefix}/${replace}`);
  assert.deepEqual(pick("console.lo"), ["log/2", "logger/2"]);
  assert.deepEqual(pick("foo"), []);
  assert.deepEqual(pick("sel*"), ["sel*/4"]);
  assert.deepEqual(pick("x=se"), ["sel*/2"]);
  assert.deepEqual(pick(""), ["log/0", "sel*/0", "one/0", "logger/0"]);
  const many = Array.from({ length: 500 }, (_, index) => ({ ...items[0], prefix: `p${index}`, entry: index }));
  const capped = selectSnippetSuggestions(many, "p");
  assert.equal(capped.length, SNIPPET_MAX_SUGGESTIONS);
  assert.equal(capped[0].item.prefix, "p0");
});

function stubMonaco({ languages = ["typescript", "sql", "plaintext"] } = {}) {
  const registered = [];
  const editors = [];
  const monaco = {
    editor: {
      EditorOption: { readOnly: 91 },
      getEditors: () => editors,
    },
    languages: {
      CompletionItemKind: { Snippet: 27 },
      CompletionItemInsertTextRule: { InsertAsSnippet: 4 },
      getLanguages: () => languages.map((id) => ({ id })),
      registerCompletionItemProvider(language, provider) {
        const record = { language, provider, disposed: false };
        registered.push(record);
        return { dispose: () => { record.disposed = true; } };
      },
    },
  };
  return { monaco, registered, editors };
}

function model(line) {
  return { getLineContent: () => line };
}

function editorFor(target, { readOnly = false, focused = false } = {}) {
  return { getModel: () => target, getOption: (option) => (option === 91 ? readOnly : undefined), hasTextFocus: () => focused };
}

function registryOf(contributions) {
  return buildSnippetRegistry(contributions).byLanguage;
}

test("monaco provider: one per language, disposed when empty, read-only suppression, sort and cap", () => {
  const { monaco, registered, editors } = stubMonaco();
  const detach = attachMonacoSnippets(monaco);
  assert.equal(registered.length, 0);

  const many = Array.from({ length: 300 }, (_, index) => snippet(`S${index}`, [`s${index}`], `select ${index};`));
  setMonacoSnippets(registryOf([
    contribution("a", [
      { language: "typescript", path: "snippets/ts.json", snippets: [snippet("Log", ["log"], "console.log(${1:value});$0", "Log it")] },
      { language: "sql", path: "snippets/sql.json", snippets: many },
      { language: "cobol", path: "snippets/cobol.json", snippets: [snippet("Hi", ["hi"], "DISPLAY 'HI'.")] },
    ]),
    contribution("b", [{ language: "typescript", path: "snippets/b.json", snippets: [snippet("Lg", ["lg"], "x")] }]),
  ]));
  assert.deepEqual(monacoSnippetLanguages(), ["sql", "typescript"]);
  assert.equal(registered.length, 2);
  assert.deepEqual(monacoSnippetIssues().map((issue) => `${issue.pluginId}:${issue.entry}:${issue.stage}:${issue.message}`), [
    'a:2:frontend:unknown Monaco language "cobol"',
  ]);

  const ts = registered.find((record) => record.language === "typescript");
  const tsModel = model("  console.lo");
  editors.push(editorFor(tsModel));
  const result = ts.provider.provideCompletionItems(tsModel, { lineNumber: 3, column: 13 });
  assert.equal(result.suggestions.length, 1);
  const [item] = result.suggestions;
  assert.equal(item.label, "log");
  assert.equal(item.kind, 27);
  assert.equal(item.insertTextRules, 4);
  assert.equal(item.insertText, "console.log(${1:value});$0");
  assert.equal(item.sortText, "~");
  assert.equal(item.detail, "Log · a name");
  assert.equal(item.documentation, "Log it\n\nconsole.log(value);");
  assert.deepEqual(item.range, { startLineNumber: 3, startColumn: 11, endLineNumber: 3, endColumn: 13 });

  // A read-only editor (focused, or every editor of the model) gets nothing.
  editors.length = 0;
  editors.push(editorFor(tsModel, { readOnly: true }));
  assert.equal(ts.provider.provideCompletionItems(tsModel, { lineNumber: 1, column: 13 }).suggestions.length, 0);
  editors.push(editorFor(tsModel, { readOnly: false, focused: true }));
  assert.equal(ts.provider.provideCompletionItems(tsModel, { lineNumber: 1, column: 13 }).suggestions.length, 1);
  editors.push(editorFor(tsModel, { readOnly: true, focused: true }));
  editors.splice(1, 1);
  assert.equal(ts.provider.provideCompletionItems(tsModel, { lineNumber: 1, column: 13 }).suggestions.length, 0);

  const sql = registered.find((record) => record.language === "sql");
  const sqlResult = sql.provider.provideCompletionItems(model("s"), { lineNumber: 1, column: 2 });
  assert.equal(sqlResult.suggestions.length, SNIPPET_MAX_SUGGESTIONS);
  assert.equal(sqlResult.incomplete, true);
  assert.ok(sqlResult.suggestions.every((suggestion) => suggestion.sortText === "~"));

  // Replacing data keeps existing providers; a language without snippets loses its provider.
  setMonacoSnippets(registryOf([
    contribution("a", [{ language: "typescript", path: "snippets/ts.json", snippets: [snippet("Other", ["other"], "x")] }]),
  ]));
  assert.equal(registered.length, 2);
  assert.equal(sql.disposed, true);
  assert.equal(ts.disposed, false);
  assert.deepEqual(monacoSnippetLanguages(), ["typescript"]);
  assert.deepEqual(monacoSnippetIssues(), []);
  editors.length = 0;
  assert.equal(ts.provider.provideCompletionItems(model("oth"), { lineNumber: 1, column: 4 }).suggestions[0].label, "other");

  setMonacoSnippets(new Map());
  assert.equal(ts.disposed, true);
  assert.deepEqual(monacoSnippetLanguages(), []);

  setMonacoSnippets(registryOf([
    contribution("a", [{ language: "plaintext", path: "snippets/p.json", snippets: [snippet("P", ["p"], "x")] }]),
  ]));
  assert.deepEqual(monacoSnippetLanguages(), ["plaintext"]);
  detach();
  assert.equal(registered.at(-1).disposed, true);
  assert.deepEqual(monacoSnippetLanguages(), []);
  setMonacoSnippets(new Map());
});

// The host normalizes snippet files (prefix → prefixes, body/description
// arrays joined with "\n"); mirror that to feed corpus files to the frontend stage.
async function corpusContribution(name) {
  const dir = join(corpus, name);
  const manifest = JSON.parse(await readFile(join(dir, "plugin.json"), "utf8"));
  const entries = [];
  for (const entry of manifest.contributes.snippets) {
    const file = JSON.parse(await readFile(join(dir, entry.path), "utf8"));
    const join2 = (value) => (Array.isArray(value) ? value.join("\n") : value);
    entries.push({
      language: entry.language,
      path: entry.path,
      snippets: Object.entries(file).map(([snippetName, item]) => ({
        name: snippetName,
        prefixes: Array.isArray(item.prefix) ? item.prefix : [item.prefix],
        body: join2(item.body),
        ...(item.description ? { description: join2(item.description) } : {}),
      })),
    });
  }
  return contribution(manifest.id, entries);
}

test("fixture corpus: frontend layer for snippets", async () => {
  const expectations = JSON.parse(await readFile(join(corpus, "expectations.json"), "utf8"));
  let checked = 0;
  for (const [name, expected] of Object.entries(expectations.cases)) {
    if (!name.includes("/snippets-")) continue;
    const valid = name.startsWith("valid/");
    if (!valid && !(expected.rejectedBy ?? []).includes("frontend")) continue;
    const registry = buildSnippetRegistry([await corpusContribution(name)]);
    if (valid) {
      assert.deepEqual(registry.issues, [], name);
    } else {
      assert.ok(registry.issues.some((issue) => issue.stage === "frontend"), `${name}: frontend accepted`);
    }
    checked += 1;
  }
  assert.ok(checked >= 3);
});
