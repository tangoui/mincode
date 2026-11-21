import assert from 'node:assert/strict';
import test from 'node:test';
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { deflateRawSync } from 'node:zlib';
import {
  latestReleaseDir,
  loadPlugin,
  main,
  missingPackages,
  overwritePlan,
  ossSaveName,
  packageTarget,
  parseUploadArgs,
  readZipEntry,
  releaseAction,
} from './upload-official-plugins.mjs';

// Minimal ZIP writer: deflated entries, like package-official-plugins.mjs.
function zip(files) {
  const local = [], central = [];
  let offset = 0;
  for (const [name, text] of Object.entries(files)) {
    const path = Buffer.from(name), data = Buffer.from(text), packed = deflateRawSync(data);
    const header = Buffer.alloc(30);
    header.writeUInt32LE(0x04034b50); header.writeUInt16LE(8, 8); header.writeUInt32LE(packed.length, 18);
    header.writeUInt32LE(data.length, 22); header.writeUInt16LE(path.length, 26);
    const entry = Buffer.alloc(46);
    entry.writeUInt32LE(0x02014b50); entry.writeUInt16LE(8, 10); entry.writeUInt32LE(packed.length, 20);
    entry.writeUInt32LE(data.length, 24); entry.writeUInt16LE(path.length, 28); entry.writeUInt32LE(offset, 42);
    local.push(header, path, packed); central.push(entry, path); offset += 30 + path.length + packed.length;
  }
  const directory = Buffer.concat(central), end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50); end.writeUInt16LE(central.length / 2, 8); end.writeUInt16LE(central.length / 2, 10);
  end.writeUInt32LE(directory.length, 12); end.writeUInt32LE(offset, 16);
  return Buffer.concat([...local, directory, end]);
}

const sha256 = (data) => createHash('sha256').update(data).digest('hex');
const manifest = { id: 'demo', name: 'Demo', version: '1.2.0', apiVersion: 2, description: 'A demo', icon: 'setting', category: 'productivity', tags: ['a', 'b'] };

/** A release set folder for `demo` with the four platform packages, as release-official-plugins.mjs writes it. */
function releaseSet(root, targets = ['darwin/arm64', 'darwin/amd64', 'windows/amd64', 'linux/amd64']) {
  const dir = join(root, 'plugin-release-20261030-1200', 'demo');
  mkdirSync(dir, { recursive: true });
  const sums = [];
  for (const target of targets) {
    const file = `demo-1.2.0-${target.replace('/', '-')}.min`;
    const data = zip({ 'plugin.json': JSON.stringify(manifest), 'ui/index.html': target });
    writeFileSync(join(dir, file), data);
    sums.push(`${sha256(data)}  ${file}`);
  }
  writeFileSync(join(dir, 'SHA256SUMS'), sums.join('\n') + '\n');
  writeFileSync(join(dir, 'README.md'), '# Demo\n');
  writeFileSync(join(dir, 'cover.png'), 'png');
  const source = join(root, 'official-plugins', 'demo');
  mkdirSync(source, { recursive: true });
  writeFileSync(join(source, 'CHANGELOG.md'), '# 更新日志\n\n## 未发布\n\n- next\n\n## 1.2.0\n\n- added\n');
  return { dir, source: join(root, 'official-plugins') };
}

test('arguments, release folder and package names', () => {
  assert.deepEqual(parseUploadArgs(['--server', 'https://x.test/', '--no-publish', '-y', 'a', 'b']),
    { dir: '', server: 'https://x.test', user: '', ids: ['a', 'b'], syncInfo: false, existing: 'ask', publish: false, dryRun: false, yes: true });
  assert.equal(parseUploadArgs(['--overwrite']).existing, 'overwrite');
  assert.equal(parseUploadArgs(['--skip-existing']).existing, 'skip');
  assert.throws(() => parseUploadArgs(['--dir']), /needs a value/);
  assert.throws(() => parseUploadArgs(['--bogus']), /Unknown option/);
  assert.throws(() => parseUploadArgs(['--server', 'ftp://x']), /Invalid --server/);
  assert.equal(packageTarget('lang-de-0.1.1-darwin-arm64.min', 'lang-de', '0.1.1'), 'darwin/arm64');
  assert.equal(packageTarget('lang-de-0.1.1-linux-arm64.min', 'lang-de', '0.1.1'), '');
  assert.equal(packageTarget('lang-de-0.1.0-linux-amd64.min', 'lang-de', '0.1.1'), '');
  const root = mkdtempSync(join(tmpdir(), 'upload-plugins-'));
  try {
    assert.equal(latestReleaseDir(join(root, 'missing')), '');
    for (const name of ['plugin-release-20261029-2120', 'plugin-release-20261030-0944', 'plugins']) mkdirSync(join(root, name));
    assert.equal(latestReleaseDir(root), join(root, 'plugin-release-20261030-0944'));
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('reads a deflated file from a package ZIP', () => {
  const data = zip({ 'ui/a.js': 'x', 'plugin.json': '{"id":"demo"}' });
  assert.equal(readZipEntry(data, 'plugin.json').toString(), '{"id":"demo"}');
  assert.equal(readZipEntry(data, 'missing.json'), null);
  assert.throws(() => readZipEntry(Buffer.from('not a zip at all, definitely not'), 'plugin.json'), /Not a ZIP/);
});

test('loads a plugin folder and rejects packages that do not match SHA256SUMS', async () => {
  const root = mkdtempSync(join(tmpdir(), 'upload-plugins-'));
  try {
    const { dir, source } = releaseSet(root);
    const plugin = await loadPlugin(dir, 'demo', source);
    assert.equal(plugin.manifest.version, '1.2.0');
    assert.deepEqual(plugin.packages.map((item) => item.target).sort(), ['darwin/amd64', 'darwin/arm64', 'linux/amd64', 'windows/amd64']);
    assert.equal(plugin.notes, '- added');
    assert.equal(plugin.readme, '# Demo\n');
    assert.match(plugin.cover, /cover\.png$/);
    await assert.rejects(loadPlugin(dir, 'other', source), /不属于 other/);
    writeFileSync(join(dir, 'demo-1.2.0-linux-amd64.min'), 'changed');
    await assert.rejects(loadPlugin(dir, 'demo', source), /SHA256SUMS/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('decides what to do with the version online', () => {
  const detail = (release, owner = null) => ({ product: { owner_user_id: owner }, releases: release ? [{ version: '1.2.0', artifacts: [], ...release }] : [] });
  assert.equal(releaseAction(null, '1.2.0').step, 'new-plugin');
  assert.equal(releaseAction(detail(null), '1.2.0').step, 'new-version');
  assert.equal(releaseAction(detail({ status: 'published', review_status: 'approved' }), '1.2.0').step, 'exists');
  assert.equal(releaseAction(detail({ status: 'withdrawn', review_status: 'approved', published_at: 1 }), '1.2.0').step, 'exists');
  assert.equal(releaseAction(detail({ status: 'withdrawn', review_status: 'approved', published_at: null }), '1.2.0').step, 'error');
  assert.equal(releaseAction(detail({ status: 'approved', review_status: 'approved' }), '1.2.0').step, 'publish');
  assert.equal(releaseAction(detail({ status: 'draft', review_status: 'not_submitted', created_by_admin: true }), '1.2.0').step, 'upload');
  assert.equal(releaseAction(detail({ status: 'draft', review_status: 'not_submitted', created_by_admin: false }), '1.2.0').step, 'error');
  assert.equal(releaseAction(detail({ status: 'pending_review', review_status: 'pending' }), '1.2.0').step, 'error');
  assert.equal(releaseAction(detail(null, 7), '1.2.0').step, 'new-version');
});

test('only uploads missing packages and refuses a different one online', () => {
  const packages = [{ target: 'darwin/arm64', sha256: 'a' }, { target: 'linux/amd64', sha256: 'b' }];
  assert.deepEqual(missingPackages(undefined, packages), packages);
  assert.deepEqual(missingPackages({ artifacts: [{ os: 'darwin', arch: 'arm64', sha256: 'a' }] }, packages), [packages[1]]);
  assert.throws(() => missingPackages({ artifacts: [{ os: 'darwin', arch: 'arm64', sha256: 'z' }] }, packages), /与本地不同/);
  assert.throws(() => missingPackages({ artifacts: [{ os: 'windows', arch: 'amd64', sha256: 'a' }] }, packages), /本地没有/);
});

test('overwriting replaces only the packages that differ', () => {
  const packages = [{ target: 'darwin/arm64', sha256: 'a' }, { target: 'linux/amd64', sha256: 'b' }, { target: 'windows/amd64', sha256: 'c' }];
  const plan = overwritePlan({ artifacts: [
    { id: 1, os: 'darwin', arch: 'arm64', sha256: 'a' }, { id: 2, os: 'linux', arch: 'amd64', sha256: 'old' },
    { id: 3, os: 'darwin', arch: 'amd64', sha256: 'x' },
  ] }, packages);
  assert.deepEqual(plan.replace.map((row) => [row.artifact.id, row.item.target]), [[2, 'linux/amd64']]);
  assert.equal(plan.same, 1);
  assert.deepEqual(plan.missing, ['windows/amd64']);
  assert.deepEqual(plan.extra, ['darwin/amd64']);
});

test('OSS save names follow the BuildAdmin template', () => {
  const name = ossSaveName('/storage/{topic}/{year}{mon}{day}/{fileName}{fileSha1}{.suffix}', 'my cover image long name.png', 'abc', new Date(2026, 9, 3));
  assert.equal(name, 'storage/default/20261003/mycoverimageabc.png');
});

/** In-memory 插件管理 API: enough state to drive a whole upload. */
function fakeMarketplace() {
  const state = { products: [], releases: [], artifacts: [], licenses: [], calls: [], oss: [], statusChanges: [], productUpdates: [] };
  const detail = (product) => ({
    product,
    releases: state.releases.filter((row) => row.product_id === product.id)
      .map((row) => ({ ...row, artifacts: state.artifacts.filter((item) => item.release_id === row.id) })),
  });
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://x');
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const raw = Buffer.concat(chunks);
    let body = {};
    if (String(req.headers['content-type']).startsWith('application/json')) body = JSON.parse(raw.toString());
    else if (raw.length) body = Object.fromEntries((await new Request('http://x', { method: 'POST', headers: req.headers, body: raw }).formData()).entries());
    const action = url.pathname.replace('/admin/mincode/marketplacePlugin/', '');
    state.calls.push(action);
    const ok = (data) => res.end(JSON.stringify({ code: 1, msg: '', data }));
    if (url.pathname === '/oss') { state.oss.push(body.key); return res.end(); }
    if (req.headers.batoken !== 'secret') return res.end(JSON.stringify({ code: 409, msg: 'Please login first' }));
    if (url.pathname === '/admin/index/index') return ok({ siteConfig: { cdnUrl: 'https://cdn.example.com', upload: { mode: 'alioss', url: `http://127.0.0.1:${server.address().port}/oss`, saveName: '/storage/{fileSha1}{.suffix}', params: { policy: 'p' } } } });
    if (url.pathname === '/admin/Alioss/callback') return res.end(); // BuildAdmin replies with an empty 200
    if (action === 'index') return ok({ items: state.products.filter((row) => row.product_key.includes(url.searchParams.get('keyword'))), total: 0 });
    if (action === 'detail') return ok(detail(state.products.find((row) => row.id === Number(url.searchParams.get('product_id')))));
    if (action === 'draft') {
      let product = state.products.find((row) => row.product_key === body.product_key);
      if (product && (product.owner_user_id ?? null) !== (body.owner_user_id ?? null)) return res.end(JSON.stringify({ code: 0, msg: 'Plugin already exists with a different owner' }));
      if (product && product.name !== body.name) return res.end(JSON.stringify({ code: 0, msg: 'Product metadata does not match existing plugin' }));
      if (!product) state.products.push(product = { id: state.products.length + 1, product_key: body.product_key, name: body.name, description: body.description, cover_url: body.cover_url, category: body.category, tags: body.tags, readme: body.readme, owner_user_id: null });
      const release = { id: state.releases.length + 1, product_id: product.id, version: body.version, release_notes: body.release_notes, status: 'draft', review_status: 'not_submitted', created_by_admin: true };
      state.releases.push(release);
      return ok({ release_id: release.id, product_id: product.id });
    }
    if (action === 'updateProduct') {
      const product = state.products.find((row) => row.id === body.product_id);
      Object.assign(product, { name: body.name, description: body.description, cover_url: body.cover_url, category: body.category, tags: body.tags });
      if ('readme' in body) product.readme = body.readme;
      state.productUpdates.push(body);
      return ok({});
    }
    if (action === 'updateDraftNotes') { state.releases.find((row) => row.id === body.release_id).release_notes = body.release_notes; return ok({}); }
    if (action === 'setReleaseStatus') { state.statusChanges.push(body.status); state.releases.find((row) => row.id === body.release_id).status = body.status; return ok({}); }
    if (action === 'replaceArtifact') {
      const artifact = state.artifacts.find((row) => row.id === Number(body.artifact_id));
      if (state.releases.find((row) => row.id === artifact.release_id).status !== 'withdrawn') return res.end(JSON.stringify({ code: 0, msg: 'Withdraw or block the published version before replacing its package' }));
      artifact.sha256 = sha256(Buffer.from(await body.file.arrayBuffer()));
      return ok({});
    }
    if (action === 'freeLicenseEnsure') { state.licenses.push(Number(body.product_id)); return ok({}); }
    if (action === 'upload') {
      const data = Buffer.from(await body.file.arrayBuffer());
      state.artifacts.push({ id: state.artifacts.length + 1, release_id: Number(body.release_id), os: body.os, arch: body.arch, sha256: sha256(data) });
      return ok({});
    }
    if (action === 'approve') { Object.assign(state.releases.find((row) => row.id === body.release_id), { status: 'approved', review_status: 'approved' }); return ok({}); }
    if (action === 'publicationPreview') {
      const id = Number(url.searchParams.get('release_id'));
      return ok({ artifacts: state.artifacts.filter((row) => row.release_id === id).map((row) => ({ artifact_id: row.id, os: row.os, arch: row.arch, sha256: row.sha256 })) });
    }
    if (action === 'publish') {
      const confirmed = JSON.parse(body.confirmed_json);
      assert.equal(confirmed.length, state.artifacts.filter((row) => row.release_id === body.release_id).length);
      Object.assign(state.releases.find((row) => row.id === body.release_id), { status: 'published', published_at: 1 });
      return ok({});
    }
    res.end(JSON.stringify({ code: 0, msg: `unexpected ${url.pathname}` }));
  });
  return { state, server };
}

test('uploads a new plugin end to end, skips it when published and continues an approved release', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'upload-plugins-'));
  const { state, server } = fakeMarketplace();
  await new Promise((done) => server.listen(0, '127.0.0.1', done));
  const logs = [];
  t.mock.method(console, 'log', (...args) => logs.push(args.join(' ')));
  process.env.MINCODE_ADMIN_TOKEN = 'secret';
  try {
    const { dir } = releaseSet(root);
    const args = ['--dir', join(dir, '..'), '--server', `http://127.0.0.1:${server.address().port}`, '--yes'];
    await main(args);
    assert.equal(state.products.length, 1);
    assert.equal(state.products[0].cover_url, `https://cdn.example.com/${state.oss[0]}`);
    assert.equal(state.products[0].category, 'productivity');
    assert.deepEqual(state.products[0].tags, ['a', 'b']);
    assert.equal(state.products[0].readme, '# Demo\n');
    assert.deepEqual(state.licenses, [1]);
    assert.equal(state.artifacts.length, 4);
    assert.equal(state.releases[0].status, 'published');

    state.calls.length = 0;
    await main(args);
    assert.ok(!state.calls.includes('draft') && !state.calls.includes('upload'));
    assert.ok(logs.some((line) => line.includes('安装包一致，跳过')));

    // An interrupted run left an approved release: the next run only publishes it.
    state.releases[0].status = 'approved';
    state.calls.length = 0;
    await main(args);
    assert.deepEqual(state.calls.filter((call) => ['draft', 'upload', 'approve', 'publish'].includes(call)), ['publish']);
    assert.equal(state.releases[0].status, 'published');
  } finally {
    delete process.env.MINCODE_ADMIN_TOKEN;
    server.close();
    rmSync(root, { recursive: true, force: true });
  }
});

test('a new version keeps the online listing unless --sync-info is given', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'upload-plugins-'));
  const { state, server } = fakeMarketplace();
  await new Promise((done) => server.listen(0, '127.0.0.1', done));
  t.mock.method(console, 'log', () => {});
  process.env.MINCODE_ADMIN_TOKEN = 'secret';
  const existing = () => {
    state.products.splice(0, Infinity, { id: 1, product_key: 'demo', name: 'Old name', description: 'Old', cover_url: 'https://cdn.example.com/c.png', readme: 'old', owner_user_id: null });
    state.releases.splice(0, Infinity, { id: 1, product_id: 1, version: '1.1.0', status: 'published', review_status: 'approved', created_by_admin: true });
    state.artifacts.length = 0;
  };
  try {
    const { dir } = releaseSet(root);
    const args = ['--dir', join(dir, '..'), '--server', `http://127.0.0.1:${server.address().port}`, '--yes'];
    existing();
    await main(args);
    assert.equal(state.products[0].name, 'Old name');
    assert.equal(state.releases[1].version, '1.2.0');
    assert.equal(state.releases[1].status, 'published');
    assert.deepEqual(state.licenses, []);
    assert.deepEqual(state.oss, []);

    existing();
    await main([...args, '--sync-info']);
    assert.equal(state.products[0].name, 'Demo');
    assert.equal(state.products[0].readme, '# Demo\n');
    assert.equal(state.releases[1].status, 'published');

    // A member's listing gets its new version drafted for the same member.
    existing();
    state.products[0].owner_user_id = 7;
    await main(args);
    assert.equal(state.releases[1].status, 'published');
  } finally {
    delete process.env.MINCODE_ADMIN_TOKEN;
    server.close();
    rmSync(root, { recursive: true, force: true });
  }
});

test('a version already on the market is overwritten or skipped as chosen', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'upload-plugins-'));
  const { state, server } = fakeMarketplace();
  await new Promise((done) => server.listen(0, '127.0.0.1', done));
  t.mock.method(console, 'log', () => {});
  process.env.MINCODE_ADMIN_TOKEN = 'secret';
  try {
    const { dir } = releaseSet(root);
    const args = ['--dir', join(dir, '..'), '--server', `http://127.0.0.1:${server.address().port}`, '--yes'];
    state.products.push({ id: 1, product_key: 'demo', name: 'Demo', description: 'A demo', owner_user_id: null });
    state.releases.push({ id: 1, product_id: 1, version: '1.2.0', status: 'published', review_status: 'approved', published_at: 1, created_by_admin: true });
    const targets = [['darwin', 'arm64'], ['darwin', 'amd64'], ['windows', 'amd64'], ['linux', 'amd64']];
    targets.forEach(([os, arch], i) => state.artifacts.push({ id: i + 1, release_id: 1, os, arch, sha256: 'old' }));

    await assert.rejects(main(args), /--overwrite 或 --skip-existing/);
    await main([...args, '--skip-existing']);
    assert.ok(state.artifacts.every((row) => row.sha256 === 'old'));

    await main([...args, '--overwrite']);
    assert.ok(state.artifacts.every((row) => row.sha256 !== 'old'));
    assert.deepEqual(state.statusChanges, ['withdrawn', 'published']);
    assert.equal(state.releases.length, 1);
    assert.ok(!state.calls.includes('draft') && !state.calls.includes('upload'));
  } finally {
    delete process.env.MINCODE_ADMIN_TOKEN;
    server.close();
    rmSync(root, { recursive: true, force: true });
  }
});

test('continuing an earlier draft brings its release notes up to date', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'upload-plugins-'));
  const { state, server } = fakeMarketplace();
  await new Promise((done) => server.listen(0, '127.0.0.1', done));
  t.mock.method(console, 'log', () => {});
  process.env.MINCODE_ADMIN_TOKEN = 'secret';
  try {
    const { dir, source } = releaseSet(root);
    process.env.PLUGIN_PACKAGE_SOURCE = source;
    state.products.push({ id: 1, product_key: 'demo', name: 'Demo', description: 'A demo', owner_user_id: null });
    state.releases.push({ id: 1, product_id: 1, version: '1.2.0', release_notes: 'old', status: 'draft', review_status: 'not_submitted', created_by_admin: true });
    await main(['--dir', join(dir, '..'), '--server', `http://127.0.0.1:${server.address().port}`, '--yes']);
    assert.equal(state.releases[0].release_notes, '- added');
    // The plugin's only version is this draft: a first run stopped before the free license was made.
    assert.deepEqual(state.licenses, [1]);
    assert.equal(state.releases[0].status, 'published');
    assert.equal(state.artifacts.length, 4);
  } finally {
    delete process.env.MINCODE_ADMIN_TOKEN;
    delete process.env.PLUGIN_PACKAGE_SOURCE;
    server.close();
    rmSync(root, { recursive: true, force: true });
  }
});

test('a listing without a cover gets the release cover and keeps the rest of its details', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'upload-plugins-'));
  const { state, server } = fakeMarketplace();
  await new Promise((done) => server.listen(0, '127.0.0.1', done));
  t.mock.method(console, 'log', () => {});
  process.env.MINCODE_ADMIN_TOKEN = 'secret';
  try {
    const { dir } = releaseSet(root);
    const args = ['--dir', join(dir, '..'), '--server', `http://127.0.0.1:${server.address().port}`, '--yes'];
    // Already published without a cover, as a draft from the official-plugins SQL would end up.
    state.products.push({ id: 1, product_key: 'demo', name: 'Demo', description: 'A demo', cover_url: '', category: 'database', tags: ['x'], readme: 'online', owner_user_id: null });
    state.releases.push({ id: 1, product_id: 1, version: '1.2.0', status: 'published', review_status: 'approved', published_at: 1, created_by_admin: true });
    await main(args);
    assert.equal(state.products[0].cover_url, `https://cdn.example.com/${state.oss[0]}`);
    assert.equal(state.products[0].category, 'database');
    assert.deepEqual(state.products[0].tags, ['x']);
    assert.equal(state.products[0].readme, 'online');
    assert.equal('readme' in state.productUpdates[0], false);

    // With a cover online nothing is uploaded again.
    await main(args);
    assert.equal(state.oss.length, 1);
  } finally {
    delete process.env.MINCODE_ADMIN_TOKEN;
    server.close();
    rmSync(root, { recursive: true, force: true });
  }
});
