import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { parse } from 'vue/compiler-sfc';

// Both plugin cards (installed and cloud) open with the cover, edge to edge above the title, like marketplace cards.
const source = readFileSync(new URL('../src/views/PluginManagerWorkspaceView.vue', import.meta.url), 'utf8');
const { descriptor } = parse(source);
const root = descriptor.template.ast;

function walk(node, visit) {
  visit(node);
  for (const child of node.children ?? []) walk(child, visit);
}
const hasClass = (node, name) => node.props?.some(prop => prop.name === 'class' && prop.value?.content.split(/\s+/).includes(name));

const elements = node => (node.children ?? []).filter(child => child.type === 1);
const css = readFileSync(new URL('../src/styles/workspaces/plugins/workspace.css', import.meta.url), 'utf8');

test('plugin cards open with the cover above the title', () => {
  const cards = [];
  walk(root, node => { if (node.tag === 'el-card' && hasClass(node, 'plugin-manager-card')) cards.push(node); });
  assert.equal(cards.length, 2, 'installed and cloud cards');
  for (const card of cards) {
    const children = elements(card);
    assert.equal(children[0]?.tag, 'img', 'cover is the first element of the card');
    assert.ok(hasClass(children[0], 'plugin-manager-cover'));
    assert.ok(hasClass(children[1], 'plugin-manager-card__scroll'), 'title and details follow the cover');
  }
  // The card cover cancels the 12px body padding so it reaches the card edges.
  assert.match(css, /\.plugin-manager-card__body > \.plugin-manager-cover \{[^}]*width: calc\(100% \+ 24px\);[^}]*max-width: none;[^}]*margin: -12px -12px 12px;/);
  assert.match(css, /\.plugin-manager-card__body \{[^}]*padding: 12px;/);
});

test('card bodies clip edge-to-edge covers instead of creating native scrollbars', () => {
  const rule = css.match(/\.plugin-manager-card__body \{([^}]*)\}/)?.[1] ?? '';
  assert.match(rule, /overflow: hidden;/);
});

test('card secondary actions are left aligned', () => {
  const rule = css.match(/\.plugin-manager-secondary-actions \{([^}]*)\}/)?.[1] ?? '';
  assert.match(rule, /justify-content: flex-start;/);
  assert.doesNotMatch(rule, /margin-left: auto/);
});
