import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

import { pluginLocaleBoundary } from './build-plugin-ui.mjs';

test('plugin builds reject host locale modules but allow package-owned data', () => {
  const boundary = pluginLocaleBoundary();
  assert.throws(() => boundary.load('/repo/vue-app/src/i18n/locales/en.ts'), /plugin-owned locales/);
  assert.throws(() => boundary.load('C:\\repo\\vue-app\\src\\i18n\\locales\\zh.ts'), /plugin-owned locales/);
  assert.doesNotThrow(() => boundary.load('/repo/plugins/etcd-manager/ui-src/locales.mjs'));
});

const exec = promisify(execFile);
const root = fileURLToPath(new URL('../../', import.meta.url));
const script = new URL('./build-plugin-ui.mjs', import.meta.url).href;

test('plugin UI bytes are stable across working directories and repeated builds', { timeout: 120000 }, async () => {
  const temp = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-ui-cwd-test-'));
  try {
    let expected;
    for (const [index, cwd] of [root, resolve(root, 'vue-app'), root].entries()) {
      const outputPackages = resolve(temp, String(index));
      await exec(process.execPath, ['--input-type=module', '-e', `
        import { buildPluginUI } from ${JSON.stringify(script)};
        await buildPluginUI(['etcd-manager'], { outputPackages: ${JSON.stringify(outputPackages)} });
      `], { cwd, timeout: 35000, maxBuffer: 4 * 1024 * 1024 });
      const files = ['app.js', 'style.css', 'index.html'];
      assert.deepEqual((await readdir(resolve(outputPackages, 'etcd-manager/ui'))).sort(), [...files].sort(), 'host public assets must not enter plugin output');
      const actual = await Promise.all(files.map(file => readFile(resolve(outputPackages, 'etcd-manager/ui', file))));
      if (expected) {
        for (const [i, file] of files.entries()) {
          assert.ok(actual[i].equals(expected[i]), `${file} differs on build ${index + 1} (cwd: ${cwd})`);
        }
      } else {
        expected = actual;
      }
    }
  } finally {
    await rm(temp, { recursive: true, force: true });
  }
});
