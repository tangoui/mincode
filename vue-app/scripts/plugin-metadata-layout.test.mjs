import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const component = readFileSync(new URL('../src/components/plugins/PluginMetadata.vue', import.meta.url), 'utf8');
const css = readFileSync(new URL('../src/styles/workspaces/plugins/workspace.css', import.meta.url), 'utf8');

test('metadata reserves label width while long values remain wrappable', () => {
  assert.match(component, /<el-descriptions\s[^>]*:label-width="120"/);
  const labels = css.match(/\.app-plugin-metadata \.el-descriptions__label\s*\{([^}]+)\}/)?.[1];
  assert.ok(labels);
  assert.match(labels, /white-space:\s*nowrap/);
  assert.match(labels, /overflow-wrap:\s*normal/);
  assert.match(css, /\.app-plugin-metadata\s*\{[^}]*overflow-wrap:\s*anywhere/);
});
