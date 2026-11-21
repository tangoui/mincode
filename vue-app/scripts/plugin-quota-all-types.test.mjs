import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';

// Runs the plugin manager's real quota helpers: every installed plugin, data-only ones included, takes a slot.
const source = readFileSync(new URL('../src/views/PluginManagerWorkspaceView.vue', import.meta.url), 'utf8');
function extract(start, end) {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from);
  assert.ok(from >= 0 && to > from, `missing ${start}`);
  return source.slice(from, to);
}
const helpers = extract('function usesPluginQuota(', 'const pluginQuota') + extract('const previewQuotaMessage', 'const canConfirmInstall');
const compiled = ts.transpileModule(helpers, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;

function load({ plugins, preview, allowed = false }) {
  const context = vm.createContext({
    plugins: { value: plugins },
    preview: { value: preview },
    marketInstall: { value: undefined },
    pluginQuota: { value: { allowed, forbidden: false, limit: 6 } },
    quota: { effective: { plan: 'free' } },
    computed: getter => ({ get value() { return getter(); } }),
    t: (key, values) => (values ? `${key}:${values.limit}` : key),
  });
  // Top-level const bindings are not context properties, so expose the computed message explicitly.
  vm.runInContext(`${compiled}\nglobalThis.previewQuotaMessage = previewQuotaMessage;`, context);
  return context;
}

test('declarative plugins count toward the enabled-plugin quota; builtin ones do not', () => {
  const context = load({ plugins: [], preview: undefined });
  assert.equal(context.usesPluginQuota({ source: 'local', runtime: 'declarative' }), true);
  assert.equal(context.usesPluginQuota({ source: 'local', runtime: 'process' }), true);
  assert.equal(context.usesPluginQuota({ source: 'builtin', runtime: 'builtin' }), false);
});

test('installing a declarative package at the limit shows the quota message', () => {
  const manifest = { id: 'lang-x', runtime: { type: 'declarative' } };
  const context = load({ plugins: [], preview: { installed: false, manifest } });
  assert.equal(context.previewQuotaMessage.value, 'quota.pluginLimit:6');
});
