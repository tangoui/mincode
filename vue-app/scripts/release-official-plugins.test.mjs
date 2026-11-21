import assert from 'node:assert/strict';
import test from 'node:test';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  PACKAGE_LIMITS,
  RELEASE_TARGETS,
  cgoToolchain,
  countFiles,
  packageBudget,
  marketCategory,
  parseReleaseArgs,
  releaseNotes,
  uploadInfo,
} from './release-official-plugins.mjs';

const root = new URL('../../', import.meta.url);
const catalogSource = readFileSync(new URL('vue-app/src/domain/plugins/catalog.ts', root), 'utf8');
const marketCategories = JSON.parse(catalogSource.match(/export const pluginCategories = (\[[\s\S]*?\]) as const/)[1].replace(/,\s*\]/, ']'));

test('release targets are the four marketplace platforms', () => {
  assert.deepEqual(RELEASE_TARGETS, ['darwin/arm64', 'darwin/amd64', 'windows/amd64', 'linux/amd64']);
  assert.deepEqual(parseReleaseArgs([]), { targets: RELEASE_TARGETS, ids: [] });
  assert.deepEqual(parseReleaseArgs(['--targets', 'linux/amd64', 'calendar']), { targets: ['linux/amd64'], ids: ['calendar'] });
  assert.throws(() => parseReleaseArgs(['--targets', 'linux/arm64']), /Unsupported release target/);
});

test('cgo plugins get a cross toolchain per target or a clear reason why not', () => {
  const host = { os: 'darwin', arch: 'arm64' };
  const has = (commands) => (name) => commands.includes(name);
  assert.deepEqual(cgoToolchain({ os: 'darwin', arch: 'arm64' }, host, has([])), { env: {} });
  assert.deepEqual(cgoToolchain({ os: 'darwin', arch: 'amd64' }, host, has(['clang'])),
    { env: { CC: 'clang -arch x86_64', CXX: 'clang++ -arch x86_64' } });
  assert.deepEqual(cgoToolchain({ os: 'windows', arch: 'amd64' }, host, has(['x86_64-w64-mingw32-gcc'])),
    { env: { CC: 'x86_64-w64-mingw32-gcc', CXX: 'x86_64-w64-mingw32-g++' } });
  assert.deepEqual(cgoToolchain({ os: 'linux', arch: 'amd64' }, host, has(['docker'])), { container: true });
  assert.match(cgoToolchain({ os: 'windows', arch: 'amd64' }, host, has([])).error, /x86_64-w64-mingw32-gcc/);
  assert.match(cgoToolchain({ os: 'linux', arch: 'amd64' }, host, has([])).error, /docker/);
  assert.deepEqual(cgoToolchain({ os: 'linux', arch: 'amd64' }, { os: 'linux', arch: 'amd64' }, has([])), { env: {} });
});

test('release notes are the CHANGELOG section of the released version only', () => {
  const changelog = '# 更新日志\n\n## 未发布\n\n- next\n\n## 0.2.0 - 2026-10-19\n\n- added\n\n### 修复\n\n- fixed\n\n## 0.1.0\n\n- first\n';
  assert.equal(releaseNotes(changelog, '0.2.0'), '- added\n\n### 修复\n\n- fixed');
  assert.equal(releaseNotes(changelog, '0.1.0'), '- first');
  assert.equal(releaseNotes(changelog, '0.3.0'), '');
});

test('the marketplace category map only names official plugins and known categories', () => {
  const { categories } = JSON.parse(readFileSync(new URL('vue-app/scripts/official-plugin-market-categories.json', root), 'utf8'));
  assert.ok(Object.keys(categories).length > 0);
  for (const [id, category] of Object.entries(categories)) {
    assert.ok(existsSync(new URL(`official-plugins/${id}/plugin.json`, root)), id);
    assert.ok(marketCategories.includes(category), `${id}: ${category}`);
  }
  assert.equal(marketCategory('lang-de', { category: 'productivity' }), 'language');
  assert.equal(marketCategory('calendar', { category: 'productivity' }), 'productivity');
});

test('upload info lists the admin form fields in order and flags the manifest category', () => {
  const text = uploadInfo({
    id: 'lang-de',
    manifest: { name: 'German', version: '0.1.0', description: 'Deutsch', icon: 'flag', tags: ['de', 'locale'], category: 'productivity' },
    category: 'language', categoryLabel: '语言包', cover: 'cover.png', readme: true, notes: '- first',
    packages: [{ label: 'Linux x64', file: 'lang-de-0.1.0-linux-amd64.min', bytes: 1200, sha256: 'a'.repeat(64) }],
  });
  const fields = [...text.matchAll(/^\| (插件 ID|名称|版本|主页|图标|分类|标签|封面图|详情说明) \|/gm)].map((m) => m[1]);
  assert.deepEqual(fields, ['插件 ID', '名称', '版本', '主页', '图标', '分类', '标签', '封面图', '详情说明']);
  assert.match(text, /语言包（`language`）；插件包内仍为 `productivity`/);
  assert.match(text, /```text\nde,locale\n```/);
  assert.match(text, /lang-de-0\.1\.0-linux-amd64\.min/);
});

test('the release fails a package over the install budget and warns near it', () => {
  const MiB = 1024 * 1024;
  // ocr 0.1.0 as released on 2026-10-28: darwin/amd64 is the largest target.
  assert.deepEqual(packageBudget({ file: 'ocr-0.1.0-linux-amd64.min', unpackedBytes: 57965040, files: 11 }), {});
  assert.match(packageBudget({ file: 'ocr-0.1.0-darwin-amd64.min', unpackedBytes: 63473064, files: 11 }).warning, /ocr-0\.1\.0-darwin-amd64\.min: 60\.53 MiB uncompressed, only 3\.47 MiB below/);
  assert.deepEqual(packageBudget({ file: 'x.min', unpackedBytes: PACKAGE_LIMITS.bytes, files: PACKAGE_LIMITS.files }).error, undefined);
  assert.match(packageBudget({ file: 'ocr-0.2.0-darwin-amd64.min', unpackedBytes: 64 * MiB + 1, files: 11 }).error, /ocr-0\.2\.0-darwin-amd64\.min: 64\.00 MiB uncompressed exceeds the 64\.00 MiB install limit/);
  assert.match(packageBudget({ file: 'x.min', unpackedBytes: MiB, files: 2001 }).error, /2001 ZIP entries \(files and folders\) exceed the 2000 install limit/);
  assert.match(packageBudget({ file: 'x.min', unpackedBytes: undefined, files: 3 }).error, /unknown uncompressed size/);
});

test('the entry count includes folders, as the host ZIP check does', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'release-count-'));
  try {
    // plugin.json, bin/, bin/a.exe, models/, models/x/, models/x/m.onnx, empty/
    writeFileSync(join(dir, 'plugin.json'), '{}');
    mkdirSync(join(dir, 'bin'));
    writeFileSync(join(dir, 'bin', 'a.exe'), '');
    mkdirSync(join(dir, 'models', 'x'), { recursive: true });
    writeFileSync(join(dir, 'models', 'x', 'm.onnx'), '');
    mkdirSync(join(dir, 'empty'));
    assert.equal(await countFiles(dir), 7);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('the release checks every package budget before copying it', () => {
  const source = readFileSync(new URL('vue-app/scripts/release-official-plugins.mjs', root), 'utf8');
  const check = source.indexOf('packageBudget({ file: item.file');
  assert.ok(check > 0 && check < source.indexOf('await copyFile(resolve(staging, tag, item.file)'));
});

test('the container-built linux/amd64 cgo binary gets its per-target assets too', () => {
  const source = readFileSync(new URL('./release-official-plugins.mjs', import.meta.url), 'utf8');
  const container = source.slice(source.indexOf('async function containerBuild'), source.indexOf('async function main'));
  assert.match(container, /await rename\(temp, entry\);[\s\S]*goPluginAssets\(id\)[\s\S]*prepareAssets\(\{ GOOS: 'linux', GOARCH: 'amd64' \}, resolve\(pluginsRoot, id\)\)/);
});

test('a release leaves bin/ built for this machine, even after a failed target', async () => {
  const source = readFileSync(new URL('./release-official-plugins.mjs', import.meta.url), 'utf8');
  assert.match(source, /\} finally \{\s*await restoreHostBinaries\(\[\.\.\.pureIds, \.\.\.cgoIds\]\);/);
  assert.match(source, /delete env\.GOOS;\s*delete env\.GOARCH;/);
});

// Shared with the native CLI: version escaping, ASCII word boundary and headings.
test('release notes agree with the native publish CLI cases', () => {
  const cases = JSON.parse(readFileSync(new URL('../../tools/mincode-cli/testdata/release-notes-cases.json', import.meta.url), 'utf8'));
  for (const { changelog, version, want } of cases) assert.equal(releaseNotes(changelog, version), want);
});
