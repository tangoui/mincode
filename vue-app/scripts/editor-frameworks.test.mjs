import assert from 'node:assert/strict';
import test from 'node:test';
import { existsSync } from 'node:fs';
const url = new URL('../src/domain/editor/frameworks.ts', import.meta.url);
const domain = existsSync(url) ? await import(url.href) : {};
const match = (...args) => {
  assert.equal(typeof domain.matchFramework, 'function', '缺少通用框架匹配');
  return domain.matchFramework(...args);
};
const rule = { id: 'demo', languages: ['typescript'], match: { all: [{ file: 'pages.json' }], any: [{ file: 'App.vue' }, { file: 'package.json', field: ['dependencies', 'demo-framework'] }], exclude: [{ file: 'manifest.json', field: ['vueVersion'], equals: '2' }] }, completions: [] };
test('通用规则要求全部标志与至少一个可选标志，不猜测普通项目', () => {
  assert.equal(match(rule, new Map([['pages.json', '{}']])), false);
  assert.equal(match(rule, new Map([['pages.json', '{}'], ['App.vue', '']])), true);
  assert.equal(match(rule, new Map([['App.vue', '']])), false);
});
test('JSONC 字段匹配支持依赖且排除旧版，不执行代码或正则', () => {
  const files = new Map([['pages.json', '{}'], ['package.json', '{// comment\n"dependencies":{"demo-framework":"1",},}']]);
  assert.equal(match(rule, files), true);
  files.set('manifest.json', '{"vueVersion":"2"}');
  assert.equal(match(rule, files), false);
});
test('最近项目边界阻止外层框架污染嵌套普通应用', () => {
  assert.equal(typeof domain.nearestFrameworkRoot, 'function');
  const roots = [{ root: '/repo', pluginId: 'demo', framework: rule }];
  assert.equal(domain.nearestFrameworkRoot('/repo/a.ts', roots, ['/repo', '/repo/plain'])?.root, '/repo');
  assert.equal(domain.nearestFrameworkRoot('/repo/plain/a.ts', roots, ['/repo', '/repo/plain']), undefined);
  assert.equal(domain.nearestFrameworkRoot('/repository/a.ts', roots, ['/repo']), undefined);
});
test('前端边界拒绝越界路径和任意代码字段', () => {
  assert.equal(typeof domain.parseEditorFramework, 'function');
  assert.ok(domain.parseEditorFramework(rule));
  assert.equal(domain.parseEditorFramework({ ...rule, match: { ...rule.match, all: [{ file: '../secret' }] } }), undefined);
  assert.equal(domain.parseEditorFramework({ ...rule, execute: 'code' }), undefined);
});
test('补全按前缀过滤并限制数量，悬停采用纯文本元数据', () => {
  assert.equal(typeof domain.frameworkSuggestions, 'function');
  const entry = { ...rule, completions: [{ label: 'demo.request', insertText: 'request', context: 'demo.', detail: 'request(options)', documentation: '请求数据' }] };
  assert.equal(domain.frameworkSuggestions(entry, 'demo.re')[0].label, 'demo.request');
  assert.deepEqual(domain.frameworkSuggestions(entry, 'other.re'), []);
});
test('补全上下文必须从完整标识符开始，不匹配更长标识符或属性链的一部分', () => {
  const entry = { ...rule, completions: [{ label: 'uni.request', insertText: 'request', context: 'uni.', detail: 'uni.request(options)', documentation: '请求' }] };
  const labels = (before) => domain.frameworkSuggestions(entry, before).map((item) => item.label);
  for (const before of ['uni.re', '  uni.re', 'await uni.re', '(uni.re', 'a = uni.', 'x;uni.re', 'myuni.request(); uni.re']) {
    assert.deepEqual(labels(before), ['uni.request'], before);
  }
  for (const before of ['myuni.re', 'obj.uni.re', '_uni.re', '$uni.re', '1uni.re', 'ünuni.re', 'uni.request(); myuni.re']) {
    assert.deepEqual(labels(before), [], before);
  }
  const tag = { ...rule, completions: [{ label: '<view', insertText: 'view', context: '<', detail: '', documentation: '' }] };
  assert.equal(domain.frameworkSuggestions(tag, 'a<vi')[0]?.label, '<view');
});
