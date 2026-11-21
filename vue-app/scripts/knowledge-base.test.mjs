import test from 'node:test';
import assert from 'node:assert/strict';
import { after } from 'node:test';
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
// The plugin runs as a Go process; build it once and drive it over JSON-RPC with an in-memory fake host.
const binDir = mkdtempSync(join(tmpdir(), 'knowledge-base-test-'));
const bin = join(binDir, process.platform === 'win32' ? 'knowledge-base.exe' : 'knowledge-base');
execFileSync('go', ['build', '-o', bin, '.'], { cwd: fileURLToPath(new URL('../../official-plugins/knowledge-base/backend/', import.meta.url)), env: { ...process.env, GOWORK: 'off', CGO_ENABLED: '0' }, stdio: 'inherit' });
const children = [];
after(async () => {
  await Promise.all(children.map(child => new Promise((resolve, reject) => {
    if (child.exitCode !== null || child.signalCode !== null) return resolve();
    let deadline;
    const forceKill = setTimeout(() => {
      child.kill('SIGKILL');
      deadline = setTimeout(() => reject(new Error(`知识库测试进程未退出，保留临时目录：${binDir}`)), 3_000);
    }, 3_000);
    child.once('close', () => {
      clearTimeout(forceKill);
      clearTimeout(deadline);
      resolve();
    });
    child.kill();
  })));
  rmSync(binDir, { recursive: true, force: true });
});
function setup() {
  const store = new Map(); const files = new Map();
  const key = value => { if (value.includes(':')) throw new Error('plugin key contains forbidden colon'); return value; };
  const host = {
    'host.storage.get': p => store.has(key(p.key)) ? structuredClone(store.get(p.key)) : null,
    'host.storage.set': p => { store.set(key(p.key), structuredClone(p.value)); return null; },
    'host.storage.delete': p => { store.delete(key(p.key)); return null; },
    'host.storage.list': () => [...store.keys()].sort(),
    'host.storage.file.write': p => { const old = p.truncate ? Buffer.alloc(0) : (files.get(p.path) ?? Buffer.alloc(0)); files.set(p.path, Buffer.concat([old.subarray(0, p.offset), Buffer.from(p.data, 'base64')])); return true; },
    'host.storage.file.read': p => { const data = files.get(p.path); if (!data) return null; const end = Math.min(p.offset + p.maxBytes, data.length); return { data: data.subarray(p.offset, end).toString('base64'), offset: p.offset, nextOffset: end, eof: end >= data.length }; },
    'host.storage.file.delete': p => { files.delete(p.path); return null; },
  };
  const child = spawn(bin, [], { stdio: ['pipe', 'pipe', 'inherit'] }); children.push(child);
  const pending = new Map(); let next = 0;
  createInterface({ input: child.stdout }).on('line', line => {
    const msg = JSON.parse(line);
    if (msg.method) { let reply; try { reply = { result: host[msg.method](msg.params) }; } catch (error) { reply = { error: { code: -32000, message: error.message } }; } child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: msg.id, ...reply }) + '\n'); return; }
    const done = pending.get(msg.id); pending.delete(msg.id);
    if (msg.error) done.reject(new Error(msg.error.message)); else done.resolve(msg.result);
  });
  const invoke = (action, payload = {}) => new Promise((resolve, reject) => { const id = ++next; pending.set(id, { resolve, reject }); child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method: 'plugin.invoke', params: { action, payload: JSON.stringify(payload) } }) + '\n'); });
  return new Proxy({}, { get: (_, action) => payload => invoke(action, payload) });
}
test('knowledge libraries, documents and Chinese search', async () => {
  const a = setup();
  assert.equal((await a.libraries())[0].id, 'default');
  const lib = await a.saveLibrary({ title: '项目资料', project: '/example' });
  const doc = await a.save({ library: lib.id, title: '排查', content: '连接超时的处理方式', tags: '网络' });
  assert.equal((await a.search({ library: lib.id, query: '超时 网络' }))[0].id, doc.id);
  assert.equal((await a.search({ library: 'default', query: '超时' })).length, 0);
  assert.equal((await a.list({ library: lib.id }))[0].content, undefined);
  assert.equal((await a.list({ id: doc.id })).content, doc.content);
});
test('trash excludes results, restore retains content and prevents editing deleted documents', async () => {
  const a = setup(); const d = await a.save({ library: 'default', title: '笔记', content: '测试' });
  await a.trash({ id: d.id });
  assert.equal((await a.search({ library: 'default', query: '测试' })).length, 0);
  assert.equal((await a.list({ library: 'default', deleted: true })).length, 1);
  await assert.rejects(a.save(d), /恢复/);
  await a.restore({ id: d.id });
  assert.equal((await a.list({ id: d.id })).content, '测试');
});
test('reject invalid identifiers, oversized content, unknown libraries and empty titles', async () => {
  const a = setup();
  await assert.rejects(a.list({ id: '../bad' }), /标识/);
  await assert.rejects(a.save({ library: 'missing', title: 'ok', content: '' }), /不存在/);
  await assert.rejects(a.save({ library: 'default', title: ' ', content: '' }), /文本/);
  await assert.rejects(a.save({ library: 'default', title: 'ok', content: '中'.repeat(50000) }), /120 KB/);
});

test('classification, favorites, stats and batch import stay consistent', async () => {
  const a = setup();
  const imported = await a.importBatch({ library: 'default', documents: [
    { title: '接口规范', content: '鉴权和连接超时', folder: '开发/接口', tags: '后端,规范' },
    { title: '产品说明', content: '知识库使用方式', folder: '产品', tags: '说明' },
  ] });
  await a.favorite({ id: imported[0].id, favorite: true });
  const list = await a.list({ library: 'default' });
  assert.equal(list[0].id, imported[0].id);
  assert.equal(list[0].content, undefined);
  assert.match(list[0].excerpt, /鉴权/);
  assert.deepEqual(await a.stats({ library: 'default' }), { documents: 2, favorites: 1, folders: 2, tags: 3, bytes: Buffer.byteLength('鉴权和连接超时知识库使用方式') });
  assert.equal((await a.search({ library: 'default', query: '接口', folder: '开发/接口', tags: ['后端'] }))[0].id, imported[0].id);
});

test('editing creates bounded history and can restore a prior version', async () => {
  const a = setup();
  let doc = await a.save({ library: 'default', title: '版本', content: '第一版' });
  doc = await a.save({ ...doc, content: '第二版' });
  const versions = await a.history({ id: doc.id });
  assert.equal(versions.length, 1);
  assert.equal(versions[0].content, undefined);
  const restored = await a.restoreVersion({ id: doc.id, revision: versions[0].id });
  assert.equal(restored.content, '第一版');
  assert.equal((await a.history({ id: doc.id })).length, 2);
});

test('library deletion is safe and purge requires trash', async () => {
  const a = setup();
  const library = await a.saveLibrary({ title: '临时库', description: '待清理' });
  const doc = await a.save({ library: library.id, title: '资料', content: '正文' });
  await assert.rejects(a.deleteLibrary({ id: library.id }), /仍有文档/);
  await assert.rejects(a.purge({ id: doc.id }), /回收站/);
  await a.trash({ id: doc.id }); await a.purge({ id: doc.id });
  await a.deleteLibrary({ id: library.id });
  assert.equal((await a.libraries()).some(item => item.id === library.id), false);
  await assert.rejects(a.deleteLibrary({ id: 'default' }), /默认/);
});

test('categories preserve legacy folders, empty categories and library isolation', async () => {
  const a = setup();
  await a.save({ library: 'default', title: '旧文章', content: '', folder: '开发/接口' });
  await a.saveCategory({ library: 'default', name: ' 空分类 ' });
  assert.deepEqual(await a.categories({ library: 'default' }), [{ name: '开发/接口', count: 1 }, { name: '空分类', count: 0 }]);
  await assert.rejects(a.saveCategory({ library: 'default', name: '空分类' }), /已存在/);
  await assert.rejects(a.saveCategory({ library: 'default', name: ' ' }), /文本/);
  const lib = await a.saveLibrary({ title: '其他库' });
  assert.deepEqual(await a.categories({ library: lib.id }), []);
});

test('rename and delete category include trash, preserving content and tags', async () => {
  const a = setup();
  const doc = await a.save({ library: 'default', title: '笔记', content: '保留正文', folder: '旧分类', tags: 'Redis， 性能,Redis,, ' });
  assert.equal(doc.tags, 'Redis,性能');
  await a.trash({ id: doc.id });
  await a.saveCategory({ library: 'default', previous: '旧分类', name: '新分类' });
  assert.equal((await a.list({ id: doc.id })).folder, '新分类');
  await a.deleteCategory({ library: 'default', name: '新分类' });
  await a.restore({ id: doc.id });
  const restored = await a.list({ id: doc.id });
  assert.equal(restored.folder, '');
  assert.equal(restored.content, '保留正文');
  assert.equal(restored.tags, 'Redis,性能');
  assert.deepEqual(await a.categories({ library: 'default' }), []);
});

test('empty keyword search supports uncategorized and tag intersections', async () => {
  const a = setup();
  const doc = await a.save({ library: 'default', title: '未分类', content: '', tags: 'A,B' });
  await a.save({ library: 'default', title: '分类', content: '', folder: '技术', tags: 'A' });
  assert.equal((await a.search({ library: 'default' })).length, 2);
  assert.deepEqual((await a.search({ library: 'default', folder: '', tags: ['A', 'B'] })).map(d => d.id), [doc.id]);
  assert.equal((await a.search({ library: 'default', folder: '技术', tags: ['A', 'B'] })).length, 0);
  const updated = await a.save({ id: doc.id, library: 'default', title: '更新', content: '' });
  assert.equal(updated.tags, 'A,B');
});
