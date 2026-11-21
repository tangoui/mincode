import test from 'node:test';
import assert from 'node:assert/strict';
import { pathFieldKind, relativeProjectPath } from '../src/plugin-ui/path-fields.mjs';
test('only supported path fields get pickers', () => {
  assert.equal(pathFieldKind({ id: 'root' }), 'directory');
  for (const id of ['proto', 'previous', 'current']) assert.equal(pathFieldKind({ id, pathKind: 'file' }), 'file');
  assert.equal(pathFieldKind({ id: 'target' }), undefined);
});
test('project relative paths support POSIX and Windows', () => {
  assert.equal(relativeProjectPath('/work/app/', '/work/app/proto/a.proto'), 'proto/a.proto');
  assert.equal(relativeProjectPath('C:\\Work\\App', 'c:\\work\\app\\a.proto'), 'a.proto');
  assert.equal(relativeProjectPath('/', '/a.proto'), 'a.proto');
});
test('outside and traversal selections are rejected', () => {
  for (const file of ['/work/app-other/a.proto', '/work/a.proto', '/work/app/../a.proto', '/work/app']) {
    assert.throws(() => relativeProjectPath('/work/app', file), /请选择/);
  }
});

test('project directory has a standalone picker wired to the native transport', async () => {
  const { readFile } = await import('node:fs/promises');
  const source = await readFile(new URL('../src/plugin-ui/OfficialPluginPage.vue', import.meta.url), 'utf8');
  assert.match(source, /<ElButton v-if="field.id === 'root'"[^>]*@click="selectPath\(field\)"[^>]*>\{\{ copy.directory \}\}<\/ElButton>/);
  assert.match(source, /await transport.pickPath\(pathFieldKind\(field\)\)/);
  assert.match(source, /if \(selected\) values\[field.id\] = field.id === 'root' \? selected/);
});
