import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { inflateRawSync } from 'node:zlib';
import { once } from 'node:events';
import { lstat, mkdir, mkdtemp, readFile, readdir, readlink, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { goTarget } from './build-go-plugins.mjs';

const script = fileURLToPath(new URL('./package-official-plugins.mjs', import.meta.url));
const { GOOS: platform, GOARCH: arch } = await goTarget();

async function packagePlugins(output, { source, names = ['draw'], succeeds = true, preload, build = false } = {}) {
  const child = spawn(process.execPath, [...(preload ? ['--import', pathToFileURL(preload).href] : []), script, ...(build ? [] : ['--no-build']), ...names], {
    env: { ...process.env, PLUGIN_PACKAGE_OUT: output, ...(source ? { PLUGIN_PACKAGE_SOURCE: source } : {}) },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stdout = '';
  let stderr = '';
  child.stdout.on('data', chunk => { stdout += chunk; });
  child.stderr.on('data', chunk => { stderr += chunk; });
  const [code] = await once(child, 'close');
  if (succeeds) assert.equal(code, 0, stderr || stdout);
  else assert.notEqual(code, 0, 'packaging unexpectedly succeeded');
  return stderr || stdout;
}

async function walk(root, relative = '') {
  const files = [];
  for (const entry of await readdir(join(root, relative), { withFileTypes: true })) {
    const name = relative ? `${relative}/${entry.name}` : entry.name;
    const info = await lstat(join(root, name));
    assert.equal(info.isSymbolicLink(), false, `runtime directory contains symlink: ${name}`);
    if (entry.isDirectory()) files.push(...await walk(root, name));
    else files.push(name);
  }
  return files.sort();
}

test('official packaging emits a directly installable Draw runtime directory', async () => {
  const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-test-'));
  try {
    const staleDirectory = join(output, `draw-0.2.0-${platform}-${arch}`);
    const userDirectory = join(output, `backup-${platform}-${arch}`);
    const fakeDirectory = join(output, `other-0.2.0-${platform}-${arch}`);
    await mkdir(staleDirectory, { recursive: true });
    await writeFile(join(staleDirectory, 'plugin.json'), '{}');
    await mkdir(userDirectory, { recursive: true });
    await writeFile(join(userDirectory, 'plugin.json'), '{}');
    await mkdir(fakeDirectory);
    await writeFile(join(fakeDirectory, 'keep'), 'untouched');
    await writeFile(join(output, `catalog-${platform}-${arch}.json`), JSON.stringify({
      schemaVersion: 1,
      packages: [
        { id: 'draw', version: '0.2.0', directory: basename(staleDirectory), file: `${basename(staleDirectory)}.zip` },
        { id: 'other', version: '0.2.0', directory: basename(fakeDirectory), file: `${basename(fakeDirectory)}.zip` },
        { id: 'draw', version: '0.2.0', directory: basename(userDirectory), file: `${basename(userDirectory)}.zip` },
      ],
    }));

    await packagePlugins(output);
    await assert.rejects(lstat(staleDirectory), error => error.code === 'ENOENT');
    assert.equal((await lstat(userDirectory)).isDirectory(), true);
    assert.equal(await readFile(join(fakeDirectory, 'keep'), 'utf8'), 'untouched');
    const catalogFile = (await readdir(output)).find(name => name.startsWith('catalog-'));
    assert.ok(catalogFile, 'missing package catalog');
    const catalog = JSON.parse(await readFile(join(output, catalogFile), 'utf8'));
    const draw = catalog.packages.find(item => item.id === 'draw');
    assert.ok(draw, 'missing Draw package');
    assert.ok(catalog.packages.some(item => item.id === 'other'), 'unselected catalog entry lost');

    assert.equal(draw.file, `${draw.directory}.min`);
    const archive = await readFile(join(output, draw.file));
    assert.equal(archive.readUInt32LE(0), 0x04034b50, '.min must contain ZIP bytes');
    assert.equal(archive.readUInt32LE(archive.length - 22), 0x06054b50);
    assert.equal(createHash('sha256').update(archive).digest('hex'), draw.sha256);
    assert.equal(archive.length, draw.bytes);
    const directory = join(output, draw.directory);
    const files = await walk(directory);
    assert.ok(files.includes('plugin.json'));
    assert.ok(files.includes('bin/mincode-draw.exe'));
    assert.equal(files.includes('plugin.mjs'), false);
    assert.ok(files.includes('ui/index.html'));
    assert.equal(files.some(name => name === 'node_modules' || name.startsWith('node_modules/')), false);
    assert.equal(files.some(name => name === 'src' || name.startsWith('src/')), false);
  } finally {
    await rm(output, { recursive: true, force: true });
  }
});

async function fixture(output, id = 'example') {
  const source = join(output, 'source');
  const plugin = join(source, id);
  await mkdir(plugin, { recursive: true });
  await writeFile(join(plugin, 'plugin.json'), JSON.stringify({ id, version: '1.0.0', platforms: [platform], runtime: { entry: 'plugin.mjs' } }));
  await writeFile(join(plugin, 'plugin.mjs'), 'export default null;');
  return { source, plugin, base: `${id}-1.0.0-${platform}-${arch}` };
}

test('symlinks in manifest, runtime, bin, UI, and nested assets fail without publishing', async t => {
  const cases = [
    ['plugin.json', async ({ plugin, outside }) => {
      await writeFile(outside, await readFile(join(plugin, 'plugin.json')));
      await rm(join(plugin, 'plugin.json'));
      await symlink(outside, join(plugin, 'plugin.json'));
    }],
    ['plugin.mjs', async ({ plugin, outside }) => {
      await rm(join(plugin, 'plugin.mjs'));
      await symlink(outside, join(plugin, 'plugin.mjs'));
    }],
    ['bin/runtime', async ({ plugin, outside }) => {
      await writeFile(join(plugin, 'plugin.json'), JSON.stringify({ id: 'example', version: '1.0.0', platforms: [platform], runtime: { entry: 'bin/runtime' } }));
      await rm(join(plugin, 'plugin.mjs'));
      await mkdir(join(plugin, 'bin'));
      await symlink(outside, join(plugin, 'bin', 'runtime'));
    }],
    ['bin', async ({ plugin, outside }) => {
      await mkdir(outside);
      await symlink(outside, join(plugin, 'bin'), 'dir');
    }],
    ['ui', async ({ plugin, outside }) => {
      await mkdir(outside);
      await writeFile(join(outside, 'index.html'), 'external');
      await symlink(outside, join(plugin, 'ui'), 'dir');
    }],
    ['ui/index.html', async ({ plugin, outside }) => {
      await mkdir(join(plugin, 'ui'));
      await symlink(outside, join(plugin, 'ui', 'index.html'));
    }],
    ['assets/nested', async ({ plugin, outside }) => {
      await mkdir(join(plugin, 'assets'));
      await mkdir(outside);
      await writeFile(join(outside, 'payload'), 'external');
      await symlink(outside, join(plugin, 'assets', 'nested'), 'dir');
    }],
  ];
  for (const [name, setup] of cases) {
    await t.test(name, async () => {
      const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-symlink-'));
      try {
        const fixturePaths = await fixture(output);
        if (!['bin', 'ui', 'assets/nested'].includes(name)) await writeFile(join(output, 'outside'), 'external');
        await setup({ ...fixturePaths, outside: join(output, 'outside') });
        assert.match(await packagePlugins(output, { source: fixturePaths.source, names: ['example'], succeeds: false }), /symlink rejected/i);
        assert.deepEqual((await readdir(output)).sort(), ['outside', 'source']);
      } finally { await rm(output, { recursive: true, force: true }); }
    });
  }
});

test('selected plugin directory cannot link outside the package source', async () => {
  const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-root-symlink-'));
  try {
    const { source, plugin } = await fixture(output);
    const external = join(output, 'external');
    await mkdir(external);
    await rm(plugin, { recursive: true });
    await symlink(external, plugin, 'dir');
    assert.match(await packagePlugins(output, { source, names: ['example'], succeeds: false }), /symlink rejected/i);
    assert.deepEqual((await readdir(output)).sort(), ['external', 'source']);
  } finally { await rm(output, { recursive: true, force: true }); }
});

test('a non-regular runtime or UI entry is rejected before reading it', async t => {
  if (process.platform === 'win32') return t.skip('mkfifo is unavailable on Windows');
  const { spawnSync } = await import('node:child_process');
  for (const name of ['plugin.mjs', 'ui/index.html']) {
    await t.test(name, async () => {
      const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-fifo-'));
      try {
        const { source, plugin } = await fixture(output);
        const path = join(plugin, name);
        if (name.startsWith('ui/')) await mkdir(join(plugin, 'ui'));
        else await rm(path);
        const result = spawnSync('mkfifo', [path]);
        assert.equal(result.status, 0, result.stderr.toString());
        assert.match(await packagePlugins(output, { source, names: ['example'], succeeds: false }), /Non-regular file rejected/);
        assert.deepEqual(await readdir(output), ['source']);
      } finally { await rm(output, { recursive: true, force: true }); }
    });
  }
});

test('failed packaging retains old package and catalog; successful replacement removes only its own stale paths', async t => {
  for (const extension of ['zip', 'min']) await t.test(extension, async () => {
    const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-rollback-'));
    try {
      const { source, plugin, base } = await fixture(output);
      const old = `example-0.9.0-${platform}-${arch}`;
      const catalogName = `catalog-${platform}-${arch}.json`;
      await mkdir(join(output, old));
      await writeFile(join(output, old, 'keep'), 'old');
      await writeFile(join(output, `${old}.${extension}`), 'old archive');
      const unowned = [`${old}.${extension === 'zip' ? 'min' : 'zip'}`, `user-${platform}-${arch}.${extension}`, `${old}.${extension}.backup`, `${old}-user.MIN`];
      for (const name of unowned) await writeFile(join(output, name), 'user file');
      await writeFile(join(output, catalogName), JSON.stringify({ schemaVersion: 1, packages: [
        { id: 'example', version: '0.9.0', directory: old, file: `${old}.${extension}` },
        { id: 'example', version: '0.9.0', directory: old, file: unowned[1] },
        { id: 'example', version: '0.9.0', directory: old, file: unowned[2] },
        { id: 'example', version: '0.9.0', directory: old, file: unowned[3] },
      ] }));
      const before = await readFile(join(output, catalogName), 'utf8');
      await rm(join(plugin, 'plugin.mjs'));
      assert.match(await packagePlugins(output, { source, names: ['example'], succeeds: false }), /Missing runtime/);
      assert.equal(await readFile(join(output, catalogName), 'utf8'), before);
      assert.equal(await readFile(join(output, `${old}.${extension}`), 'utf8'), 'old archive');
      for (const name of unowned) assert.equal(await readFile(join(output, name), 'utf8'), 'user file');
      assert.equal(await readFile(join(output, old, 'keep'), 'utf8'), 'old');
      await writeFile(join(plugin, 'plugin.mjs'), 'export default null;');
      const preload = join(output, 'fail-publication.mjs');
      await writeFile(preload, `
        import fs from 'node:fs/promises';
        import { syncBuiltinESMExports } from 'node:module';
        import { basename, dirname } from 'node:path';
        const rename = fs.rename;
        fs.rename = async (from, to) => {
          if (basename(dirname(from)).startsWith('.package-stage-') && to === ${JSON.stringify(join(output, catalogName))}) {
            throw Error('Injected catalog publication failure');
          }
          return rename(from, to);
        };
        syncBuiltinESMExports();
      `);
      assert.match(await packagePlugins(output, { source, names: ['example'], succeeds: false, preload }), /Injected catalog publication failure/);
      assert.equal(await readFile(join(output, catalogName), 'utf8'), before);
      assert.equal(await readFile(join(output, `${old}.${extension}`), 'utf8'), 'old archive');
      assert.equal(await readFile(join(output, old, 'keep'), 'utf8'), 'old');
      for (const name of unowned) assert.equal(await readFile(join(output, name), 'utf8'), 'user file');
      await assert.rejects(lstat(join(output, `${base}.min`)), error => error.code === 'ENOENT');
      await assert.rejects(lstat(join(output, base)), error => error.code === 'ENOENT');
      assert.equal((await readdir(output)).some(name => name.startsWith('.package-stage-')), false);
      await packagePlugins(output, { source, names: ['example'] });
      await assert.rejects(lstat(join(output, old)), error => error.code === 'ENOENT');
      await assert.rejects(lstat(join(output, `${old}.${extension}`)), error => error.code === 'ENOENT');
      for (const name of unowned) assert.equal(await readFile(join(output, name), 'utf8'), 'user file');
      assert.equal((await lstat(join(output, base))).isDirectory(), true);
      const catalog = JSON.parse(await readFile(join(output, catalogName), 'utf8'));
      assert.equal(catalog.packages[0].file, `${base}.min`);
      const archive = await readFile(join(output, `${base}.min`));
      let offset = 0;
      const files = [];
      while (archive.readUInt32LE(offset) === 0x04034b50) {
        assert.equal(archive.readUInt16LE(offset + 8), 8);
        const size = archive.readUInt32LE(offset + 18), length = archive.readUInt16LE(offset + 26), extra = archive.readUInt16LE(offset + 28);
        const name = archive.subarray(offset + 30, offset + 30 + length).toString('utf8');
        const start = offset + 30 + length + extra;
        assert.deepEqual(inflateRawSync(archive.subarray(start, start + size)), await readFile(join(output, base, name)));
        files.push(name);
        offset = start + size;
      }
      assert.equal(archive.readUInt32LE(offset), 0x02014b50);
      assert.equal(archive.readUInt32LE(archive.length - 22), 0x06054b50);
      assert.deepEqual(files.sort(), ['plugin.json', 'plugin.mjs']);
      const beforeReplacement = Buffer.from(archive);
      await packagePlugins(output, { source, names: ['example'] });
      assert.deepEqual(await readFile(join(output, `${base}.min`)), beforeReplacement);
      for (const name of unowned) assert.equal(await readFile(join(output, name), 'utf8'), 'user file');
    } finally { await rm(output, { recursive: true, force: true }); }
  });
});

test('dangling output symlinks are backed up and restored after catalog publication fails', async () => {
  const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-dangling-output-'));
  try {
    const { source, base } = await fixture(output);
    const catalogName = `catalog-${platform}-${arch}.json`;
    const before = JSON.stringify({ schemaVersion: 1, packages: [{ id: 'example', version: '1.0.0', directory: base, file: `${base}.min` }] });
    await writeFile(join(output, catalogName), before);
    for (const name of [base, `${base}.min`]) await symlink(`missing-${name}`, join(output, name));
    const preload = join(output, 'fail-publication.mjs');
    await writeFile(preload, `
      import fs from 'node:fs/promises';
      import { syncBuiltinESMExports } from 'node:module';
      import { basename, dirname } from 'node:path';
      const rename = fs.rename;
      fs.rename = async (from, to) => {
        if (basename(dirname(from)).startsWith('.package-stage-') && to === ${JSON.stringify(join(output, catalogName))}) {
          throw Error('Injected catalog publication failure');
        }
        return rename(from, to);
      };
      syncBuiltinESMExports();
    `);
    assert.match(await packagePlugins(output, { source, names: ['example'], succeeds: false, preload }), /Injected catalog publication failure/);
    assert.equal(await readFile(join(output, catalogName), 'utf8'), before);
    for (const name of [base, `${base}.min`]) {
      assert.equal((await lstat(join(output, name))).isSymbolicLink(), true);
      assert.equal(await readlink(join(output, name)), `missing-${name}`);
    }
    assert.equal((await readdir(output)).some(name => name.startsWith('.package-stage-')), false);
    await packagePlugins(output, { source, names: ['example'] });
    assert.equal((await lstat(join(output, base))).isDirectory(), true);
    assert.equal((await lstat(join(output, `${base}.min`))).isFile(), true);
    assert.equal(await readFile(join(output, base, 'plugin.mjs'), 'utf8'), 'export default null;');
  } finally { await rm(output, { recursive: true, force: true }); }
});

test('non-ENOENT output lstat and backup rename errors retain old outputs and catalog', async t => {
  for (const operation of ['lstat', 'rename']) for (const code of ['EACCES', 'EIO']) await t.test(`${operation} ${code}`, async () => {
    const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-backup-error-'));
    try {
      const { source, base } = await fixture(output);
      const catalogName = `catalog-${platform}-${arch}.json`;
      const before = JSON.stringify({ schemaVersion: 1, packages: [{ id: 'example', version: '1.0.0', directory: base, file: `${base}.min` }] });
      await mkdir(join(output, base));
      await writeFile(join(output, base, 'keep'), 'old directory');
      await writeFile(join(output, `${base}.min`), 'old archive');
      await writeFile(join(output, catalogName), before);
      const preload = join(output, 'fail-backup.mjs');
      await writeFile(preload, `
        import fs from 'node:fs/promises';
        import { syncBuiltinESMExports } from 'node:module';
        import { basename, dirname } from 'node:path';
        const operation = ${JSON.stringify(operation)};
        const original = fs[operation];
        fs[operation] = async (...args) => {
          if (args[0] === ${JSON.stringify(join(output, catalogName))} && (operation === 'lstat' || basename(dirname(args[1])) === 'backup')) {
            throw Object.assign(Error('Injected ' + operation + ' ${code}'), { code: ${JSON.stringify(code)} });
          }
          return original(...args);
        };
        syncBuiltinESMExports();
      `);
      assert.match(await packagePlugins(output, { source, names: ['example'], succeeds: false, preload }), new RegExp(`Injected ${operation} ${code}`));
      assert.equal(await readFile(join(output, base, 'keep'), 'utf8'), 'old directory');
      assert.deepEqual(await readdir(join(output, base)), ['keep']);
      assert.equal(await readFile(join(output, `${base}.min`), 'utf8'), 'old archive');
      assert.equal(await readFile(join(output, catalogName), 'utf8'), before);
      assert.deepEqual((await readdir(output)).sort(), [base, `${base}.min`, catalogName, 'fail-backup.mjs', 'source'].sort());
    } finally { await rm(output, { recursive: true, force: true }); }
  });
});

test('backup rename ENOENT after a successful output lstat still permits publication', async () => {
  const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-backup-race-'));
  try {
    const { source, base } = await fixture(output);
    const catalogName = `catalog-${platform}-${arch}.json`;
    await mkdir(join(output, base));
    await writeFile(join(output, base, 'keep'), 'old directory');
    await writeFile(join(output, `${base}.min`), 'old archive');
    await writeFile(join(output, catalogName), JSON.stringify({ schemaVersion: 1, packages: [{ id: 'example', version: '1.0.0', directory: base, file: `${base}.min` }] }));
    const preload = join(output, 'remove-after-lstat.mjs');
    const marker = join(output, 'rename-error');
    await writeFile(preload, `
      import assert from 'node:assert/strict';
      import fs from 'node:fs/promises';
      import { syncBuiltinESMExports } from 'node:module';
      import { basename, dirname } from 'node:path';
      const lstat = fs.lstat;
      const rename = fs.rename;
      const target = ${JSON.stringify(join(output, base))};
      fs.lstat = async (...args) => {
        const info = await lstat(...args);
        if (args[0] === target) await fs.rm(target, { recursive: true });
        return info;
      };
      fs.rename = async (from, to) => {
        if (from === target && basename(dirname(to)) === 'backup') {
          await assert.rejects(lstat(from), error => error.code === 'ENOENT');
          await fs.writeFile(${JSON.stringify(marker)}, 'ENOENT');
          throw Object.assign(Error('Injected backup rename ENOENT after removal'), { code: 'ENOENT' });
        }
        return rename(from, to);
      };
      syncBuiltinESMExports();
    `);
    await packagePlugins(output, { source, names: ['example'], preload });
    assert.equal(await readFile(marker, 'utf8'), 'ENOENT');
    assert.deepEqual((await readdir(join(output, base))).sort(), ['plugin.json', 'plugin.mjs']);
    assert.equal(await readFile(join(output, base, 'plugin.mjs'), 'utf8'), 'export default null;');
    const catalog = JSON.parse(await readFile(join(output, catalogName), 'utf8'));
    assert.equal(catalog.packages[0].directory, base);
    assert.equal(catalog.packages[0].file, `${base}.min`);
    const archive = await readFile(join(output, `${base}.min`));
    assert.equal(createHash('sha256').update(archive).digest('hex'), catalog.packages[0].sha256);
    assert.equal((await readdir(output)).some(name => name.startsWith('.package-stage-')), false);
  } finally { await rm(output, { recursive: true, force: true }); }
});

test('runtime lib/ and models/ ship; build inputs such as scripts/ and backend/ do not', async () => {
  const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-members-'));
  try {
    const { source, plugin, base } = await fixture(output);
    for (const [path, data] of [['lib/libruntime.so', 'so'], ['models/model.onnx', 'onnx'], ['scripts/fetch-assets.mjs', 'x'], ['backend/go.mod', 'x'], ['README.md', 'x']]) {
      await mkdir(join(plugin, path, '..'), { recursive: true });
      await writeFile(join(plugin, path), data);
    }
    await packagePlugins(output, { source, names: ['example'] });
    assert.deepEqual(await walk(join(output, base)), ['lib/libruntime.so', 'models/model.onnx', 'plugin.json', 'plugin.mjs']);
  } finally { await rm(output, { recursive: true, force: true }); }
});

test('host file-count and 64 MiB bounds fail before publishing', async () => {
  const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-bounds-'));
  try {
    const { source, plugin } = await fixture(output);
    for (let index = 0; index < 1999; index++) await writeFile(join(plugin, `file-${index}.mjs`), 'x');
    assert.match(await packagePlugins(output, { source, names: ['example'], succeeds: false }), /2000 entry limit/);
    assert.deepEqual(await readdir(output), ['source']);
    for (let index = 0; index < 1999; index++) await rm(join(plugin, `file-${index}.mjs`));
    await mkdir(join(plugin, 'assets'));
    await writeFile(join(plugin, 'assets', 'large'), Buffer.alloc(64 * 1024 * 1024));
    assert.match(await packagePlugins(output, { source, names: ['example'], succeeds: false }), /64 MiB limit/);
    assert.deepEqual(await readdir(output), ['source']);
  } finally { await rm(output, { recursive: true, force: true }); }
});

test('ZIP over 64 MiB is rejected even when unpacked bytes fit', async () => {
  const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-zip-bound-'));
  try {
    const { source, plugin } = await fixture(output);
    await mkdir(join(plugin, 'assets'));
    const available = 64 * 1024 * 1024
      - (await readFile(join(plugin, 'plugin.json'))).length
      - (await readFile(join(plugin, 'plugin.mjs'))).length;
    await writeFile(join(plugin, 'assets', 'random'), randomBytes(available));
    assert.match(await packagePlugins(output, { source, names: ['example'], succeeds: false }), /ZIP exceeds host 64 MiB limit/);
    assert.deepEqual(await readdir(output), ['source']);
  } finally { await rm(output, { recursive: true, force: true }); }
});

test('packaging rebuilds stale official UI output before zipping', { timeout: 300_000 }, async () => {
  const output = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-package-rebuild-'));
  const generated = fileURLToPath(new URL('../../official-plugins/ebook-reader/ui/app.js', import.meta.url));
  const original = await readFile(generated).catch(() => null);
  try {
    await mkdir(resolve(generated, '..'), { recursive: true });
    await writeFile(generated, 'stale build output');
    await packagePlugins(output, { names: ['ebook-reader'], build: true });
    const rebuilt = await readFile(generated, 'utf8');
    assert.notEqual(rebuilt, 'stale build output');
    const catalogFile = (await readdir(output)).find(name => name.startsWith('catalog-'));
    const entry = JSON.parse(await readFile(join(output, catalogFile), 'utf8')).packages.find(item => item.id === 'ebook-reader');
    assert.equal(await readFile(join(output, entry.directory, 'ui', 'app.js'), 'utf8'), rebuilt);
  } finally {
    if (original && await readFile(generated, 'utf8').catch(() => '') === 'stale build output') await writeFile(generated, original);
    await rm(output, { recursive: true, force: true });
  }
});
