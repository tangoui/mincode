import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const frame = read('../src/components/plugins/PluginViewFrame.vue');
const css = read('../src/styles/workspaces/plugins/workspace.css');
const backend = read('../../backend/plugin/ai_contract.go');

test('AI completion events include validated action risk', () => {
  assert.match(backend, /core\.Emit\("plugin:action-completed",[^\n]*"risk": action\.Risk/);
});
test('read-only completion does not mark the page stale; unknown risk stays conservative', () => {
  const condition = frame.match(/if \((event\.id === props\.pluginId && event\.risk !== "read")\) externalChange\.value = true/);
  assert.ok(condition);
  const shouldNotify = new Function('event', 'props', `return ${condition[1]}`);
  for (const risk of ['write', 'network', 'dangerous', undefined]) {
    assert.equal(shouldNotify({ id: 'kb', risk }, { pluginId: 'kb' }), true);
  }
  assert.equal(shouldNotify({ id: 'kb', risk: 'read' }, { pluginId: 'kb' }), false);
  assert.equal(shouldNotify({ id: 'other', risk: 'write' }, { pluginId: 'kb' }), false);
});
test('notice has intrinsic height and iframe takes the remaining space', () => {
  assert.match(frame, /ElAlert class="plugin-view-frame__notice"/);
  assert.match(css, /\.plugin-view-frame\s*\{[^}]*display: flex;[^}]*flex-direction: column;/);
  assert.match(css, /\.plugin-view-frame__notice\s*\{\s*flex: 0 0 auto;/);
  assert.match(css, /\.plugin-view-frame__iframe\s*\{[^}]*flex: 1 1 0;[^}]*min-height: 0;/);
  assert.match(frame, /async function startSession\(\)[\s\S]*?externalChange\.value = false;/);
});
