import assert from 'node:assert/strict';
import test from 'node:test';
import { existsSync } from 'node:fs';
const url = new URL('../src/services/editor-framework-registry.ts', import.meta.url);
const runtime = existsSync(url) ? await import(url.href) : {};
const framework = { id: 'demo', languages: ['typescript'], match: { all: [{ file: 'pages.json' }], any: [], exclude: [] }, completions: [] };
test('注册框架插件时默认拒绝未匹配文件，普通插件不受影响', () => {
  assert.equal(typeof runtime.setEditorFrameworks, 'function');
  runtime.setEditorFrameworks([{ pluginId: 'demo', framework }]);
  assert.equal(runtime.frameworkPluginAllowed('demo', '/repo/a.ts'), false);
  assert.equal(runtime.frameworkPluginAllowed('other', '/repo/a.ts'), true);
  runtime.setFrameworkRoots([{ pluginId: 'demo', framework, root: '/repo' }], ['/repo']);
  assert.equal(runtime.frameworkPluginAllowed('demo', '/repo/a.ts'), true);
  runtime.setEditorFrameworks([]);
  assert.equal(runtime.frameworkPluginAllowed('demo', '/repo/a.ts'), true);
});
