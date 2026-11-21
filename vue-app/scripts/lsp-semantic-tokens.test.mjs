import assert from "node:assert/strict";
import test from "node:test";
import {
  LSP_SEMANTIC_TOKEN_TYPES,
  MONACO_SEMANTIC_SCOPES,
  parseSemanticTokensProvider,
  remapSemanticTokens,
  semanticScopeMap,
} from "../src/domain/projects/lsp-semantic-tokens.ts";

test("provider capability requires a legend and full-document support", () => {
  assert.equal(parseSemanticTokensProvider(undefined), undefined);
  assert.equal(parseSemanticTokensProvider({ full: true }), undefined);
  assert.equal(parseSemanticTokensProvider({ legend: { tokenTypes: ["class"] }, range: true }), undefined);
  assert.deepEqual(parseSemanticTokensProvider({ legend: { tokenTypes: ["class", 3], tokenModifiers: ["static"] }, full: { delta: true } }), {
    legend: { tokenTypes: ["class", ""], tokenModifiers: ["static"] },
    full: true,
  });
});

test("every advertised LSP type maps onto a Monaco scope", () => {
  const map = semanticScopeMap(LSP_SEMANTIC_TOKEN_TYPES);
  for (const scope of map) assert.ok(scope >= 0 && scope < MONACO_SEMANTIC_SCOPES.length);
  const custom = semanticScopeMap(["class", "unknownCustom", "parameter"]);
  assert.deepEqual([...custom], [MONACO_SEMANTIC_SCOPES.indexOf("type"), -1, MONACO_SEMANTIC_SCOPES.indexOf("variable.parameter")]);
});

test("remap re-encodes relative positions when tokens are dropped", () => {
  const map = semanticScopeMap(["class", "unknownCustom", "function"]);
  const type = MONACO_SEMANTIC_SCOPES.indexOf("type");
  const fn = MONACO_SEMANTIC_SCOPES.indexOf("function");
  // line 0 col 2 class; line 0 col 10 unknown; line 0 col 20 function; line 2 col 4 unknown; line 2 col 8 class
  const data = [0, 2, 3, 0, 1, 0, 8, 4, 1, 0, 0, 10, 5, 2, 7, 2, 4, 1, 1, 0, 0, 4, 2, 0, 0];
  assert.deepEqual([...remapSemanticTokens(data, map)], [
    0, 2, 3, type, 0,
    0, 18, 5, fn, 0,
    2, 8, 2, type, 0,
  ]);
});

test("remap ignores out-of-legend types, zero lengths and trailing data", () => {
  const map = semanticScopeMap(["class"]);
  assert.deepEqual([...remapSemanticTokens([0, 1, 2, 9, 0, 1, 0, 0, 0, 0, 0, 3, 1, 0, 0, 7], map)], [1, 3, 1, 0, 0]);
  assert.equal(remapSemanticTokens([], map).length, 0);
});
