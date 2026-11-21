import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { existsSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, resolve } from 'node:path';
import { deflateRawSync, gunzipSync } from 'node:zlib';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { goBuildTags, goCgo, goCgoEnv, goPluginAssets, goPlugins, goTarget, packageGoPlugin } from './build-go-plugins.mjs';

const exec = promisify(execFile);
const manifest = JSON.parse(await readFile(new URL('../../official-plugins/etcd-manager/plugin.json', import.meta.url)));

test('Tango desktop builds use production tags and do not rebuild official plugins', async () => {
  const config = JSON.parse(await readFile(new URL('../../tan.json', import.meta.url), 'utf8'));
  assert.ok(config.desktopBuildTags.includes('production'));
  assert.ok(config.desktopBuildTags.includes('nocoredrivers'));
  assert.equal(config.frontend.build, 'pnpm build');
  assert.doesNotMatch(JSON.stringify(config.desktopHelpers), /build:plugin-(?:go|ui|packages)/);
});

test('Go target honors environment and rejects unsupported architectures', async () => {
  assert.deepEqual(await goTarget({ ...process.env, GOOS: 'windows', GOARCH: 'amd64' }), { GOOS: 'windows', GOARCH: 'amd64' });
  await assert.rejects(goTarget({ ...process.env, GOOS: 'linux', GOARCH: '386' }), /Unsupported/);
});

test('etcd uses direct binaries, scoped permissions and bounded aggregate payloads', () => {
  assert.equal(manifest.version, '0.1.0');
  assert.deepEqual(manifest.capabilities, ['commands', 'ui', 'network', 'storage', 'secrets']);
  assert.equal(manifest.runtime.entry, undefined);
  for (const platform of ['darwin', 'linux', 'windows']) assert.equal(manifest.runtime.entrypoints[platform], 'bin/mincode-etcd-manager.exe');
  const local = { diagnose: [], sessions: [] };
  // 连接工作台的分组、环境与侧栏偏好只读写插件存储，负载很小。
  const workbench = new Set(['profileMeta', 'setProfileMeta', 'renameProfileGroup', 'setConnectionPrefs']);
  for (const action of manifest.contributes.actions) {
    const expected = action.id in local ? local[action.id]
      : workbench.has(action.id) ? ['storage']
        : /ProfileSecrets$/.test(action.id) ? ['secrets']
          : /Profiles?$/.test(action.id) ? ['storage', 'secrets'] : ['network', 'storage', 'secrets'];
    assert.deepEqual(action.permissions, expected, action.id);
    assert.equal(action.maxPayloadBytes, action.id === 'diagnose' ? 1024 : workbench.has(action.id) ? 4096 : 262144, action.id);
  }
  const payload = { endpoints: ['https://example.test:2379'], username: 'u', password: 'p',
    tls: { caPEM: 'a'.repeat(65536), certPEM: 'b'.repeat(65536), keyPEM: 'c'.repeat(65536) } };
  assert.ok(Buffer.byteLength(JSON.stringify(payload)) < 262144);
  payload.tls.caPEM += 'a'.repeat(65536);
  assert.ok(Buffer.byteLength(JSON.stringify(payload)) > 262144, 'field maxima beyond the backend limits cannot fit the host aggregate limit');
});

test('target package includes only binary, manifest, assets and built UI', { timeout: 300000 }, async () => {
  const temp = await mkdtemp(resolve(tmpdir(), 'mincode-go-package-test-'));
  try {
    const destination = resolve(temp, 'etcd-manager');
    // Through the command line: packaging loads build-plugin-ui.mjs, which imports the
    // entry module back, so a top-level await in the entry would never settle.
    const script = fileURLToPath(new URL('./build-go-plugins.mjs', import.meta.url));
    await exec(process.execPath, [script, '--package', 'etcd-manager', destination], { timeout: 280000 });
    assert.deepEqual((await readdir(destination)).sort(), ['assets', 'bin', 'plugin.json', 'ui']);
    const shipped = JSON.parse(await readFile(resolve(destination, 'plugin.json')));
    const target = await goTarget();
    assert.deepEqual(shipped.platforms, [target.GOOS]);
    const binary = resolve(destination, shipped.runtime.entry);
    const { stdout } = await exec('go', ['version', '-m', binary]);
    assert.match(stdout, new RegExp(`GOOS=${target.GOOS}`));
    assert.match(stdout, new RegExp(`GOARCH=${target.GOARCH}`));
    await assert.rejects(packageGoPlugin('etcd-manager', destination), /EEXIST/);
    assert.ok((await readFile(binary)).length > 0, 'existing output is preserved');
  } finally {
    await rm(temp, { recursive: true, force: true });
  }
});

test('Go plugins are discovered from official-plugins/*/backend/go.mod', async () => {
  const dirs = (await readdir(new URL('../../official-plugins/', import.meta.url), { withFileTypes: true })).filter(entry => entry.isDirectory()).map(entry => entry.name);
  assert.deepEqual(goPlugins, dirs.filter(name => existsSync(new URL(`../../official-plugins/${name}/backend/go.mod`, import.meta.url))).sort());
  for (const declarative of ['keymaps', 'themes']) assert.ok(!goPlugins.includes(declarative), declarative);
  assert.ok(goPlugins.includes('etcd-manager'));
});

test('backend/build-tags adds validated Go build tags', async () => {
  const dir = await mkdtemp(resolve(tmpdir(), 'mincode-build-tags-'));
  try {
    assert.deepEqual(await goBuildTags(dir), []);
    await writeFile(resolve(dir, 'build-tags'), 'nodynamic, netgo\n');
    assert.deepEqual(await goBuildTags(dir), ['-tags=nodynamic,netgo']);
    await writeFile(resolve(dir, 'build-tags'), 'nodynamic -ldflags=x\n');
    await assert.rejects(goBuildTags(dir), /Invalid Go build tag/);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
  assert.deepEqual(await goBuildTags(fileURLToPath(new URL('../../official-plugins/image-toolkit/backend', import.meta.url))), ['-tags=nodynamic']);
});

test('backend/cgo opts a plugin into CGO_ENABLED=1 for the host target only', async () => {
  const dir = await mkdtemp(resolve(tmpdir(), 'mincode-cgo-'));
  const { stdout } = await exec('go', ['env', '-json', 'GOHOSTOS', 'GOHOSTARCH']);
  const { GOHOSTOS, GOHOSTARCH } = JSON.parse(stdout);
  const host = { GOOS: GOHOSTOS, GOARCH: GOHOSTARCH };
  const cross = { GOOS: GOHOSTOS === 'linux' ? 'windows' : 'linux', GOARCH: 'amd64' };
  const env = { ...process.env };
  delete env.CC;
  try {
    // No marker: pure Go, whatever the target.
    assert.equal(await goCgo(dir), null);
    assert.deepEqual(await goCgoEnv(dir, host, env), { CGO_ENABLED: '0' });
    assert.deepEqual(await goCgoEnv(dir, cross, env), { CGO_ENABLED: '0' });

    await writeFile(resolve(dir, 'cgo'), '# links libfoo\n');
    assert.deepEqual(await goCgo(dir), { targets: [] });
    assert.deepEqual(await goCgoEnv(dir, host, env), { CGO_ENABLED: '1' });
    // A cross target without a C cross toolchain fails before go build runs.
    await assert.rejects(goCgoEnv(dir, cross, env), /needs cgo: cross-building .* requires a C cross toolchain/);
    assert.deepEqual(await goCgoEnv(dir, cross, { ...env, CC: 'cross-cc' }), { CGO_ENABLED: '1' });

    // Declared targets bound where the C dependency exists at all.
    await writeFile(resolve(dir, 'cgo'), `# comment\n${GOHOSTOS}/${GOHOSTARCH}\n`);
    assert.deepEqual(await goCgoEnv(dir, host, env), { CGO_ENABLED: '1' });
    await assert.rejects(goCgoEnv(dir, { GOOS: 'windows', GOARCH: GOHOSTOS === 'windows' && GOHOSTARCH === 'arm64' ? 'amd64' : 'arm64' }, { ...env, CC: 'cross-cc' }), /supports only/);
    // Windows cgo builds link the MinGW runtime statically: a target machine has no
    // libwinpthread-1.dll or libgcc DLLs, and the plugin would not start without them.
    await writeFile(resolve(dir, 'cgo'), '# links libfoo\n');
    const windows = { GOOS: 'windows', GOARCH: 'amd64' };
    assert.deepEqual(await goCgoEnv(dir, windows, { ...env, CC: 'x86_64-w64-mingw32-gcc' }), { CGO_ENABLED: '1', CGO_LDFLAGS: '-static' });
    assert.deepEqual(await goCgoEnv(dir, windows, { ...env, CC: 'x86_64-w64-mingw32-gcc', CGO_LDFLAGS: '-L/opt/lib' }), { CGO_ENABLED: '1', CGO_LDFLAGS: '-L/opt/lib -static' });
    await writeFile(resolve(dir, 'cgo'), 'plan9/386\n');
    await assert.rejects(goCgo(dir), /Invalid cgo target/);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
  // The official cgo plugins are the DuckDB and YashanDB drivers; neither C
  // dependency ships a windows/arm64 build.
  const official = [];
  for (const name of goPlugins) {
    if (await goCgo(fileURLToPath(new URL(`../../official-plugins/${name}/backend`, import.meta.url)))) official.push(name);
  }
  assert.deepEqual(official.sort(), ['duckdb-driver', 'ocr', 'yashandb-driver']);
  for (const name of official) {
    const cgo = await goCgo(fileURLToPath(new URL(`../../official-plugins/${name}/backend`, import.meta.url)));
    // ONNX Runtime 1.23.2 (ocr) has CPU builds for the four marketplace targets only.
    const targets = name === 'ocr' ? ['darwin/amd64', 'darwin/arm64', 'linux/amd64', 'windows/amd64']
      : ['darwin/amd64', 'darwin/arm64', 'linux/amd64', 'linux/arm64', 'windows/amd64'];
    assert.deepEqual(cgo.targets, targets, name);
  }
});

test('scripts/fetch-assets.mjs is the optional per-target asset hook', async () => {
  const root = await mkdtemp(resolve(tmpdir(), 'mincode-go-assets-'));
  try {
    await mkdir(resolve(root, 'plain'), { recursive: true });
    assert.equal(await goPluginAssets('plain', root), null);
    const hook = resolve(root, 'hooked', 'scripts', 'fetch-assets.mjs');
    await mkdir(dirname(hook), { recursive: true });
    await writeFile(hook, "export const assetDirs = ['lib', 'models'];\nexport async function prepareAssets() {}\n");
    assert.deepEqual((await goPluginAssets('hooked', root)).assetDirs, ['lib', 'models']);
    // A hook owns only the generated lib/ and models/: never bin/, ui/, a committed
    // directory such as assets/, or a path outside the package.
    for (const [name, dirs] of [['claims-bin', "['bin']"], ['claims-ui', "['ui']"], ['claims-assets', "['lib', 'assets']"], ['escapes', "['../x']"], ['empty', '[]'], ['duplicate', "['lib', 'lib']"]]) {
      const bad = resolve(root, name, 'scripts', 'fetch-assets.mjs');
      await mkdir(dirname(bad), { recursive: true });
      await writeFile(bad, `export const assetDirs = ${dirs};\nexport async function prepareAssets() {}\n`);
      await assert.rejects(goPluginAssets(name, root), /assetDirs limited to lib, models/, name);
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
  // Every build runs the hook after the binary, and --package ships what it placed.
  const source = await readFile(new URL('./build-go-plugins.mjs', import.meta.url), 'utf8');
  assert.match(source, /await rename\(resolve\(temp, 'plugin'\), resolve\(output, entry\)\);\s*const assets = await goPluginAssets\(name\);\s*if \(assets\) await assets\.prepareAssets\(target, output, \{ env \}\);/);
  assert.match(source, /for \(const dir of \(await goPluginAssets\(name\)\)\?\.assetDirs \?\? \[\]\)/);
  assert.equal(goPlugins.filter(name => existsSync(new URL(`../../official-plugins/${name}/scripts/fetch-assets.mjs`, import.meta.url))).join(), 'business-card,java-debug,jdbc-bridge,ocr');
});

test('ocr assets: pinned downloads are verified, cached and never kept on mismatch', async () => {
  const assets = await import('../../official-plugins/ocr/scripts/fetch-assets.mjs');
  assert.deepEqual(assets.assetDirs, ['lib', 'models']);
  assert.deepEqual(Object.keys(assets.TARGETS).sort(), ['darwin/amd64', 'darwin/arm64', 'linux/amd64', 'windows/amd64']);
  for (const [target, spec] of Object.entries(assets.TARGETS)) {
    assert.ok(assets.DOWNLOADS[spec.archive], target);
    assert.ok(spec.members.some(member => member.to === spec.library && /^[0-9a-f]{64}$/.test(member.sha256)), target);
    for (const file of [...spec.members, ...(spec.vcRuntime?.files ?? [])]) assert.match(file.to, /^lib\/[^/]+$/, target);
  }
  // onnxruntime.dll imports exactly these non-system DLLs; they ship next to it.
  assert.deepEqual(assets.TARGETS['windows/amd64'].vcRuntime.files.map(file => file.to).sort(),
    ['lib/msvcp140.dll', 'lib/msvcp140_1.dll', 'lib/vcruntime140.dll', 'lib/vcruntime140_1.dll']);
  for (const pin of Object.values(assets.DOWNLOADS)) {
    assert.match(pin.url, /^https:\/\//);
    assert.match(pin.sha256, /^[0-9a-f]{64}$/);
  }
  await assert.rejects(assets.prepareAssets({ GOOS: 'linux', GOARCH: 'arm64' }, tmpdir()), /no ONNX Runtime build for linux\/arm64/);

  const cache = await mkdtemp(resolve(tmpdir(), 'mincode-ocr-cache-'));
  const env = { ...process.env, MINCODE_PLUGIN_ASSETS_CACHE: cache };
  const name = 'PP-OCRv6_det_tiny.onnx';
  const pin = assets.DOWNLOADS[name];
  let fetches = 0;
  const fake = body => async () => { fetches++; return new Response(body); };
  try {
    await assert.rejects(assets.download(name, { env, fetchImpl: fake(Buffer.alloc(pin.bytes)) }), /SHA-256 mismatch/);
    assert.equal(fetches, 1, 'a digest mismatch is not retried');
    assert.deepEqual(await readdir(resolve(cache, 'ocr')), [], 'a rejected download leaves nothing behind');
    // A body larger than the pinned size is abandoned while streaming, and not retried.
    let pulled = 0, mostPulled = 0;
    const endless = async () => {
      fetches++;
      pulled = 0;
      return new Response(new ReadableStream({ pull(controller) { pulled += 65536; mostPulled = Math.max(mostPulled, pulled); controller.enqueue(new Uint8Array(65536)); } }));
    };
    await assert.rejects(assets.download(name, { env, fetchImpl: endless, retryDelayMs: 1 }), /exceeds the pinned/);
    assert.equal(fetches, 2, 'an over-size body is not retried');
    assert.ok(mostPulled < pin.bytes + 8 * 65536, `stops reading near the pinned size (read ${mostPulled})`);
    // A file whose digest matches is used without fetching; this one does not match, so it is replaced.
    await writeFile(resolve(cache, 'ocr', name), Buffer.alloc(pin.bytes));
    await assert.rejects(assets.download(name, { env, fetchImpl: async () => new Response(null, { status: 404 }), retryDelayMs: 1 }), /HTTP 404/);
    assert.equal(existsSync(resolve(cache, 'ocr', name)), false);
    let calls = 0;
    const flaky = async () => { calls++; if (calls === 1) throw new TypeError('fetch failed'); return new Response(Buffer.alloc(pin.bytes, 7)); };
    // The synthetic body cannot match the pin, so this checks that a network error is retried.
    await assert.rejects(assets.download(name, { env, fetchImpl: flaky, retryDelayMs: 1 }), /SHA-256 mismatch/);
    assert.equal(calls, 2, 'a network error is retried');
    // A verified cached file is returned without any fetch (uses the developer cache when present).
    const verified = await readFile(resolve(assets.cacheDir({ ...process.env, MINCODE_PLUGIN_ASSETS_CACHE: '' }), name)).catch(() => null);
    if (verified) {
      await writeFile(resolve(cache, 'ocr', name), verified);
      const data = await assets.download(name, { env, fetchImpl: async () => { throw new Error('must not fetch'); } });
      assert.deepEqual(data, verified);
    }
  } finally {
    await rm(cache, { recursive: true, force: true });
  }
});

test('ocr assets: the tar and ZIP readers return exact member bytes', async () => {
  const assets = await import('../../official-plugins/ocr/scripts/fetch-assets.mjs');
  const dir = await mkdtemp(resolve(tmpdir(), 'mincode-ocr-archive-'));
  try {
    const member = randomBytes(70000);
    await mkdir(resolve(dir, 'pkg-1.0', 'lib'), { recursive: true });
    await writeFile(resolve(dir, 'pkg-1.0', 'lib', 'libx.so.1.0'), member);
    await exec('tar', ['-czf', resolve(dir, 'a.tgz'), '-C', dir, 'pkg-1.0']);
    const files = assets.readTar(gunzipSync(await readFile(resolve(dir, 'a.tgz'))));
    assert.deepEqual(files.get('pkg-1.0/lib/libx.so.1.0'), member);

    // Stored and deflated members.
    const entries = [['a/stored.bin', member, 0], ['a/deflated.bin', member, 8]];
    const local = [], central = [];
    let offset = 0;
    for (const [path, data, method] of entries) {
      const name = Buffer.from(path), body = method ? deflateRawSync(data) : data;
      const header = Buffer.alloc(30);
      header.writeUInt32LE(0x04034b50); header.writeUInt16LE(method, 8); header.writeUInt32LE(body.length, 18); header.writeUInt32LE(data.length, 22); header.writeUInt16LE(name.length, 26);
      const record = Buffer.alloc(46);
      record.writeUInt32LE(0x02014b50); record.writeUInt16LE(method, 10); record.writeUInt32LE(body.length, 20); record.writeUInt32LE(data.length, 24); record.writeUInt16LE(name.length, 28); record.writeUInt32LE(offset, 42);
      local.push(header, name, body); central.push(record, name);
      offset += 30 + name.length + body.length;
    }
    const directory = Buffer.concat(central), end = Buffer.alloc(22);
    end.writeUInt32LE(0x06054b50); end.writeUInt16LE(entries.length, 10); end.writeUInt32LE(directory.length, 12); end.writeUInt32LE(offset, 16);
    const zip = Buffer.concat([...local, directory, end]);
    assert.deepEqual(assets.readZipMember(zip, 'a/stored.bin'), member);
    assert.deepEqual(assets.readZipMember(zip, 'a/deflated.bin'), member);
    assert.throws(() => assets.readZipMember(zip, 'a/missing.bin'), /not found/);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
