import assert from 'node:assert/strict';
import test from 'node:test';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { pluginTestEnv } from './test-go-plugins.mjs';

test('business-card tests render with the canonical packaged fonts instead of the temporary Go executable directory', async () => {
  const backend = fileURLToPath(new URL('../../official-plugins/business-card/backend', import.meta.url));
  const fonts = fileURLToPath(new URL('../../official-plugins/business-card/lib/fonts', import.meta.url));
  assert.equal((await pluginTestEnv(backend)).MINCODE_TEST_FONTS, fonts);
});

test('plugin tests honor the release CGO marker for vet and test environments', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'mincode-test-env-'));
  try {
    assert.deepEqual(await pluginTestEnv(dir), { CGO_ENABLED: '0' });
    await writeFile(join(dir, 'cgo'), '# native library required\n');
    assert.deepEqual(await pluginTestEnv(dir), { CGO_ENABLED: '1' });
    assert.deepEqual(await pluginTestEnv(dir, true), { CGO_ENABLED: '1' });
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
