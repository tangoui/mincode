import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync, existsSync } from 'node:fs';
const source = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
test('通用框架 consumer 注册且重型 runtime 延迟加载', () => {
  assert.match(source('../src/services/contributions/index.ts'), /registerEditorFrameworkContributionConsumer/);
  const url = new URL('../src/services/contributions/editor-frameworks.ts', import.meta.url);
  assert.ok(existsSync(url));
  assert.match(readFileSync(url, 'utf8'), /lazyContributionApply/);
});
test('普通代码编辑不重复扫描项目或清空补全匹配', () => {
  const runtime = source('../src/services/contributions/editor-frameworks-runtime.ts');
  assert.match(runtime, /markerNames\.has/);
  assert.match(runtime, /if \(markerNames\.has[\s\S]*?model\.onDidChangeContent\(refresh\)/);
});
test('片段和 Schema 使用同一通用项目过滤且监听刷新', () => {
  assert.match(source('../src/services/monaco-snippets.ts'), /frameworkPluginAllowed/);
  assert.match(source('../src/services/monaco-json-schemas.ts'), /frameworkPluginAllowed/);
  assert.match(source('../src/services/monaco-json-schemas.ts'), /frameworkRegistryChanged/);
});
