import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/views/PluginManagerWorkspaceView.vue', import.meta.url), 'utf8');
const css = readFileSync(new URL('../src/styles/workspaces/plugins/workspace.css', import.meta.url), 'utf8');
const catalogSource = readFileSync(new URL('../src/domain/plugins/catalog.ts', import.meta.url), 'utf8');
const catalogCode = ts.transpileModule(
  catalogSource.replace(/^import[^;]+;\s*/gm, '').replace(/export /g, ''),
  { compilerOptions: { target: ts.ScriptTarget.ES2022 } },
).outputText;
const versionSource = readFileSync(new URL('../src/domain/plugins/market-version.ts', import.meta.url), 'utf8');
const compatibilitySource = readFileSync(new URL('../src/domain/plugins/market-compatibility.ts', import.meta.url), 'utf8');
const strip = text => ts.transpileModule(text.replace(/^import[^;]+;\s*/gm, '').replace(/export /g, ''), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
const catalog = vm.runInNewContext(`${strip(versionSource)}\n${strip(compatibilitySource)}\n${catalogCode}\n({ mergePluginCatalog, filterPluginCatalog, countPluginCategories, installSourceFromSearch, catalogUpgradeVersion, upgradablePluginIds });`);

const marketSource = readFileSync(new URL('../src/services/market-service.ts', import.meta.url), 'utf8');
const marketCode = ts.transpileModule(
  marketSource.replace(/^import[^;]+;\s*/gm, '').replace(/export /g, ''),
  { compilerOptions: { target: ts.ScriptTarget.ES2022 } },
).outputText;
const market = vm.runInNewContext(`${marketCode}\n({ parseMarketPage, listMarketPlugins });`, {
  PLUGIN_ICONS: ['box', 'calendar', 'monitor', 'terminal'],
  isPluginCategory: category => ['database', 'spreadsheet', 'web', 'api', 'network', 'development', 'productivity', 'other'].includes(category),
  pluginHomepage: value => /^https:\/\/[^/@]+/.test(value) ? value : undefined,
  invokeBackend: async (_command, args) => ({ items: args.page === 1 ? [{ product_key: 'test.plugin', name: 'Test', slug: 'test', description: '', homepage: '', category: 'other', tags: [], updated_at: 1 }] : [], page: args.page, limit: args.limit }),
});

test('market response validates shape and paginates without promising installs', async () => {
  const product = { product_key: 'test.plugin', name: 'Test', slug: 'test', description: '', homepage: '', category: 'other', tags: [], updated_at: 1 };
  assert.equal(market.parseMarketPage({ items: [product], page: 1, limit: 50 }, 1, 50)[0].icon, '');
  assert.equal(market.parseMarketPage({ items: [{ ...product, icon: 'monitor' }], page: 1, limit: 50 }, 1, 50)[0].icon, 'monitor');
  assert.equal(market.parseMarketPage({ items: [{ ...product, icon: 'calendar' }], page: 1, limit: 50 }, 1, 50)[0].icon, 'calendar');
  for (const invalid of [{ ...product, name: '' }, { ...product, category: 'fake' }, { ...product, homepage: 'file:///tmp' }, { ...product, tags: [1] }, { ...product, icon: '<svg/>' }]) {
    assert.throws(() => market.parseMarketPage({ items: [invalid], page: 1, limit: 50 }, 1, 50));
  }
  assert.throws(() => market.parseMarketPage({ items: [product], page: 2, limit: 50 }, 1, 50));
  const result = await market.listMarketPlugins(() => true);
  assert.equal(result.items.length, 1);
  assert.equal(result.truncated, false);
});

test('catalog merges installed official entries and filters without duplicates', () => {
  const plugins = [{ id: 'official', version: '1' }, { id: 'local' }];
  const official = [{ id: 'official', version: '2', installed: true }, { id: 'available' }];
  const entries = catalog.mergePluginCatalog(plugins, official);
  assert.equal(entries.length, 3);
  const merged = entries.find(entry => entry.id === 'official');
  assert.equal(merged.plugin.version, '1');
  assert.equal(merged.official.version, '2');
  assert.equal(merged.source, 'official');
  assert.equal(entries.find(entry => entry.id === 'local').source, 'local');
  const filter = { category: 'all', source: 'all', status: 'all', search: '' };
  assert.equal(catalog.filterPluginCatalog(entries, filter).length, 3);
  assert.equal(catalog.filterPluginCatalog(entries, { ...filter, status: 'installed' }).length, 2);
  const available = catalog.filterPluginCatalog(entries, { ...filter, status: 'available' });
  assert.equal(available.length, 1);
  assert.equal(available[0].id, 'available');
  const installedOnly = catalog.mergePluginCatalog(plugins, []);
  assert.equal(installedOnly.length, 2, 'installed plugins survive official catalog failure');
});

test('one catalog grid pages compact optional-cover cards', () => {
  assert.doesNotMatch(source, /class="plugin-manager-official"/);
  assert.match(source, /v-for="entry in pageEntries"/);
  assert.match(source, /v-model:current-page="page"/);
  assert.equal((source.match(/body-class="plugin-manager-card__body"/g) || []).length, 2, 'installed and display-only cloud cards share the compact body');
  assert.doesNotMatch(css, /--plugin-manager-card-height/);
  const coverStyle = css.match(/\.plugin-manager-cover \{([^}]+)\}/)[1];
  assert.match(coverStyle, /width: 100%;/);
  assert.match(coverStyle, /aspect-ratio: 2 \/ 1;/, '2:1 covers are shown whole at any card width');
  assert.doesNotMatch(coverStyle, /(^|[^-])height:/);
  assert.match(coverStyle, /object-fit: cover;/);
  assert.doesNotMatch(coverStyle, /padding:|max-height:|min-height:/);
  const covers = [...source.matchAll(/<img\b[^>]*class="plugin-manager-cover"[^>]*>/g)].map(([image]) => image);
  assert.equal(covers.length, 3, 'installed cards, cloud cards and cloud details each render an optional cover');
  for (const [image, binding, identity] of [
    [covers[0], 'plugin.cover', 'entry.id'],
    [covers[1], 'entry.cloud.cover_url', 'entry.id'],
    [covers[2], 'detail.cloud.cover_url', 'detail.id'],
  ]) {
    assert.ok(image.includes(`:src="${binding}"`), `cover source: ${binding}`);
    assert.ok(image.includes(`failedCovers[${identity}] !== ${binding}`), `hide failed cover: ${binding}`);
    assert.ok(image.includes(`@error="failedCovers[${identity}] = ${binding}"`), `remember failed cover: ${binding}`);
  }
});

test('cloud cards install only through the verified market preview of a published version', () => {
  assert.match(source, /v-if="!entry.plugin && entry.cloud"/);
  assert.match(source, /v-if="!entry.cloud.latest_published_version" :title="t\('plugins.marketNotInstallable'\)"/);
  const card = source.split('v-if="!entry.plugin && entry.cloud"')[1].split('</el-card>')[0];
  assert.doesNotMatch(card, /@click="(?:install\(|upgrade|toggle|uninstall|confirmInstall)/);
  assert.match(card, /v-if="entry.cloud.latest_published_version"[\s\S]*?@click="installFromMarket\(entry.cloud.product_key\)"/);
  assert.match(source, /if \(market\?\.upgrade\) await upgradeLocalPlugin\(market.key, path\);\s*else await installLocalPlugin\(path\);/);
  assert.match(source, /listMarketPlugins/);
});

test('market install preview must match the requested plugin and published version', async () => {
  const preview = { sourcePath: '/tmp/x/plugin.zip', manifest: { id: 'todo', version: '0.1.9' }, fileCount: 1, totalBytes: 1, installed: false };
  const load = response => vm.runInNewContext(`${marketCode}\n({ previewMarketPluginInstall });`, {
    isPluginCategory: () => true, pluginHomepage: () => undefined, PLUGIN_ICONS: [],
    parsePluginInstallPreview: value => value,
    invokeBackend: async () => response,
  }).previewMarketPluginInstall;
  const valid = { preview, version: '0.1.9', sha256: 'a'.repeat(64), versionChange: 'install' };
  assert.equal((await load(valid)('todo')).version, '0.1.9');
  await assert.rejects(load(valid)('draw'));
  await assert.rejects(load({ ...valid, version: '0.2.0' })('todo'));
  await assert.rejects(load({ ...valid, sha256: 'bad' })('todo'));
  await assert.rejects(load({ ...valid, versionChange: 'downgrade' })('todo'));
});

test('market version comparison follows semantic-version precedence', () => {
  const versionSource = readFileSync(new URL('../src/domain/plugins/market-version.ts', import.meta.url), 'utf8');
  const versionCode = ts.transpileModule(versionSource.replace(/export /g, ''), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
  const { comparePluginVersions, isNewerPluginVersion } = vm.runInNewContext(`${versionCode}\n({ comparePluginVersions, isNewerPluginVersion });`);
  assert.equal(isNewerPluginVersion('0.10.0', '0.9.9'), true);
  assert.equal(isNewerPluginVersion('0.1.4', '0.1.4'), false);
  assert.equal(isNewerPluginVersion('1.0.0-beta.2', '1.0.0'), false);
  assert.equal(isNewerPluginVersion(null, '1.0.0'), false);
  assert.equal(comparePluginVersions('1.0.0-alpha', '1.0.0-alpha.1'), -1);
  assert.equal(comparePluginVersions('1.0.0-alpha.10', '1.0.0-alpha.9'), 1);
  assert.equal(comparePluginVersions('1.0.0-rc.1', '1.0.0-beta'), 1);
  assert.equal(comparePluginVersions('1.0.0+build.5', '1.0.0'), 0);
  assert.equal(comparePluginVersions('v1.0.0', '1.0.0'), undefined);
});

test('catalog header wraps from workspace width instead of the full window', () => {
  assert.match(css, /@container workbench \(max-width: 1100px\) \{[\s\S]*?\.plugin-manager-catalog-header \{[\s\S]*?grid-template-columns: 1fr;/);
  assert.doesNotMatch(css, /@media \(max-width: 1100px\) \{[\s\S]*?\.plugin-manager-catalog-header/);
});

test('cover normalization rejects remote, vector and oversized payloads', () => {
  const service = readFileSync(new URL('../src/services/plugin-service.ts', import.meta.url), 'utf8');
  const body = service.split('export function normalizePluginCover(value: unknown): string | undefined {')[1].split('\n}')[0];
  const normalize = value => vm.runInNewContext(`(() => {${body}})()`, { value });
  assert.equal(normalize('data:image/png;base64,YQ=='), 'data:image/png;base64,YQ==');
  for (const value of [undefined, 'https://example.com/cover.png', 'file:///tmp/cover.png', 'data:image/svg+xml;base64,YQ==', 'data:image/png;base64,' + 'A'.repeat(700000)]) {
    assert.equal(normalize(value), undefined);
  }
});

test('catalog renders plugin icons through the shared safe icon resolver', () => {
  assert.match(source, /pluginViewIcon\(plugin.icon \|\| entry.official\?\.icon\)/);
  assert.match(source, /pluginViewIcon\(entry.cloud.icon\)/);
});

test('the catalog search box doubles as the install source field', () => {
  for (const path of ['/Users/me/plugins/demo', '~/plugins/demo.min', './demo', '../demo.zip', 'C:\\plugins\\demo.min', 'D:/plugins/demo', '\\\\server\\share\\demo.min', 'demo.min', 'Demo.ZIP', 'https://example.com/demo.min']) {
    assert.equal(catalog.installSourceFromSearch(`  ${path}  `), path, path);
  }
  for (const text of ['', '   ', 'redis', 'etcd manager', 'http://example.com/demo.min', 'https://', 'minify', 'a/b']) {
    assert.equal(catalog.installSourceFromSearch(text), '', text);
  }
  // An install source is not a search query, so it does not filter the catalog.
  const entries = catalog.mergePluginCatalog([{ id: 'local', name: 'Local' }], []);
  assert.equal(catalog.filterPluginCatalog(entries, { category: 'all', source: 'all', status: 'all', search: '/tmp/demo.min' }).length, 1);
  assert.equal(catalog.filterPluginCatalog(entries, { category: 'all', source: 'all', status: 'all', search: 'nomatch' }).length, 0);
});

test('market entries carry download and like counts, defaulting bad values to zero', () => {
  const product = { product_key: 'test.plugin', name: 'Test', slug: 'test', description: '', homepage: '', category: 'other', tags: [], updated_at: 1 };
  const [counted] = market.parseMarketPage({ items: [{ ...product, download_count: 1234, like_count: 56 }], page: 1, limit: 50 }, 1, 50);
  assert.equal(counted.download_count, 1234);
  assert.equal(counted.like_count, 56);
  for (const bad of [{}, { download_count: -1, like_count: -2 }, { download_count: '9', like_count: 1.5 }, { download_count: null }]) {
    const [entry] = market.parseMarketPage({ items: [{ ...product, ...bad }], page: 1, limit: 50 }, 1, 50);
    assert.equal(entry.download_count, 0);
    assert.equal(entry.like_count, 0);
  }
});


test('catalog status filter lists installed plugins that the market can upgrade', () => {
  const cloud = (key, latest, extra = {}) => ({ product_key: key, name: key, category: 'other', tags: [], latest_published_version: latest, host_compatibility: 'compatible', compatible_version: latest, ...extra });
  const plugins = [
    { id: 'old', version: '0.1.0', source: 'local' },
    { id: 'current', version: '0.2.0', source: 'local' },
    { id: 'newer-local', version: '1.0.0', source: 'local' },
    { id: 'blocked', version: '0.1.0', source: 'local' },
    { id: 'older-offered', version: '0.1.0', source: 'local' },
    { id: 'builtin', version: '0.1.0', source: 'builtin' },
    { id: 'no-release', version: '0.1.0', source: 'local' },
  ];
  const market = [
    cloud('old', '0.2.0'),
    cloud('current', '0.2.0'),
    cloud('newer-local', '0.9.0'),
    // Latest needs a newer MinCode and no compatible release is newer than the installed one.
    cloud('blocked', '0.3.0', { host_compatibility: 'host_too_old', compatible_version: null }),
    // Latest needs a newer MinCode, but an older compatible release is still an upgrade.
    cloud('older-offered', '0.5.0', { host_compatibility: 'host_too_old', compatible_version: '0.4.0' }),
    cloud('builtin', '0.2.0'),
    cloud('no-release', null),
    cloud('market-only', '1.0.0'),
  ];
  const entries = catalog.mergePluginCatalog(plugins, [], market);
  const filter = { category: 'all', source: 'all', status: 'upgradable', search: '' };
  const ids = [...catalog.filterPluginCatalog(entries, filter).map(entry => entry.id)].sort();
  assert.equal(ids.join(','), 'old,older-offered');
  assert.equal(catalog.catalogUpgradeVersion(entries.find(entry => entry.id === 'old')), '0.2.0');
  assert.equal(catalog.catalogUpgradeVersion(entries.find(entry => entry.id === 'older-offered')), '0.4.0');
  assert.equal(catalog.catalogUpgradeVersion(entries.find(entry => entry.id === 'current')), null);
  assert.equal(catalog.catalogUpgradeVersion(entries.find(entry => entry.id === 'market-only')), null);
  // Upgradable entries remain installed, so the installed filter still includes them.
  assert.ok(catalog.filterPluginCatalog(entries, { ...filter, status: 'installed' }).some(entry => entry.id === 'old'));
  assert.equal(catalog.countPluginCategories(entries, filter).all, 2);
  assert.equal([...catalog.upgradablePluginIds(entries)].sort().join(','), 'old,older-offered');
});

test('the status dropdown offers an upgradable option and cards use the same upgrade rule', () => {
  const toolbar = readFileSync(new URL('../src/components/plugins/PluginCatalogToolbar.vue', import.meta.url), 'utf8');
  assert.match(toolbar, /\['all', 'installed', 'upgradable', 'available'\]/);
  const zh = readFileSync(new URL('../src/i18n/locales/zh.ts', import.meta.url), 'utf8');
  const en = readFileSync(new URL('../src/i18n/locales/en.ts', import.meta.url), 'utf8');
  assert.match(zh, /"plugins\.catalogStatus\.upgradable": "可升级"/);
  assert.match(en, /"plugins\.catalogStatus\.upgradable": "Upgradable"/);
  assert.match(source, /catalogUpgradeVersion\(entry\)/);
});
