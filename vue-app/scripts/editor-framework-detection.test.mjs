import assert from 'node:assert/strict';
import test from 'node:test';
import { existsSync } from 'node:fs';
const url = new URL('../src/domain/editor/framework-detection.ts', import.meta.url);
const domain = existsSync(url) ? await import(url.href) : {};
const framework = { id: 'demo', languages: ['typescript'], match: { all: [{ file: 'pages.json' }], any: [], exclude: [] }, completions: [] };
test('只读取已打开文件的有界祖先，嵌套 package.json 成为隔离边界', async () => {
  assert.equal(typeof domain.detectFrameworkRoots, 'function');
  const calls = [];
  const result = await domain.detectFrameworkRoots([{ pluginId: 'demo', framework }], ['/repo'], ['/repo/plain/a.ts'], async (root, file) => {
    calls.push(file);
    return ({ 'pages.json': '{}', 'package.json': '{}', 'plain/package.json': '{}' })[file];
  });
  assert.ok(result.roots.some((item) => item.root === '/repo'));
  assert.ok(result.boundaries.includes('/repo/plain'));
  assert.ok(calls.length < 20);
});
test('Windows 分隔符和盘符大小写不影响嵌套项目检测', async () => {
  const result = await domain.detectFrameworkRoots([{ pluginId: 'demo', framework }], ['C:\\repo'], ['c:\\repo\\app\\main.ts'], async (_root, file) => file === 'app/pages.json' ? '{}' : undefined);
  assert.ok(result.roots.some((item) => item.root.toLowerCase().replaceAll('\\', '/') === 'c:/repo/app'));
});
test('多个项目共同遵守 128 个候选目录上限', async () => {
  const projects = Array.from({ length: 12 }, (_, index) => `/repo${index}`);
  const paths = projects.flatMap((root) => Array.from({ length: 9 }, (_, index) => `${root}/app${index}/deep/main.ts`));
  const candidates = new Set();
  await domain.detectFrameworkRoots([{ pluginId: 'demo', framework }], projects, paths, async (root, file) => {
    candidates.add(`${root}/${file.slice(0, file.lastIndexOf('/') + 1)}`);
    return undefined;
  });
  assert.ok(candidates.size <= 128, `实际检查 ${candidates.size} 个候选目录`);
});
test('读取错误拒绝检测而不把错误伪装为普通项目', async () => {
  assert.equal(typeof domain.detectFrameworkRoots, 'function');
  await assert.rejects(() => domain.detectFrameworkRoots([{ pluginId: 'demo', framework }], ['/repo'], ['/repo/a.ts'], async () => { throw new Error('denied'); }), /denied/);
});
