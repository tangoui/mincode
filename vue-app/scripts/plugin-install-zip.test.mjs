import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';
import { DECLARATIVE_CONTRIBUTION_POINTS, parsePluginInstallPreview } from '../src/domain/plugins/install-preview.ts';
import { messagesEn } from '../src/i18n/locales/en.ts';
import { messagesZh } from '../src/i18n/locales/zh.ts';

const source = readFileSync(new URL('../src/views/PluginManagerWorkspaceView.vue', import.meta.url), 'utf8');
const flow = source.slice(source.indexOf('async function chooseSourceZip()'), source.indexOf('async function uninstall('));
const compiled = ts.transpileModule(flow, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
// The backup-capability confirmation helpers sit just before toggle(); the install flow calls them.
const backupHelpers = source.slice(source.indexOf('async function confirmBackupCapability()'), source.indexOf('async function toggle('));
const backupCompiled = ts.transpileModule(backupHelpers, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
// Exercise the real picker bridge with only the native runtime mocked.
const bridgeSource = readFileSync(new URL('../src/services/desktop-bridge.ts', import.meta.url), 'utf8');
const pickerSource = bridgeSource.slice(bridgeSource.indexOf('function normalizeNativeDialogPath(')).replace(/^export /gm, '');
const pickerCompiled = ts.transpileModule(pickerSource, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
// The pickers call the desktop runtime's native dialog ops; dialog stands in for both open and save.
function nativePicker(dialog) {
  const context = vm.createContext({ Error, URL, nativeCall: (_op, options) => dialog(options) });
  vm.runInContext(pickerCompiled, context);
  return context;
}
const ref = value => ({ value });
const payload = (sourcePath = '/plugins/example.zip') => ({
  sourcePath,
  manifest: { id: 'example', name: 'Example', version: '1.0.0', apiVersion: 2, category: 'development', runtime: { type: 'process', entry: 'plugin.mjs' } },
  fileCount: 3, totalBytes: 4096, installed: false,
});
function harness(overrides = {}, selectedPath = '/plugins/example.min') {
  const calls = [];
  const context = {
    choosingSource: ref(false), installing: ref(false), previewLoading: ref(false), disposed: false, busyId: ref(''), officialBusy: ref(''),
    sourcePath: ref(''), preview: ref(undefined), previewPath: ref(''), previewVisible: ref(false), previewError: ref(''), previewGeneration: 0,
    marketInstall: ref(undefined),
    computed: getter => ({ get value() { return getter(); } }),
    isDesktopRuntime: () => true, t: key => key,
    cloudStatus: async () => ({ signedIn: true }),
    redirectToCloudSignIn: () => calls.push(['sign-in']),
    openCloudAccount: () => calls.push(['sign-in']),
    describePluginInstallError: error => ({ kind: 'unknown', detail: String(error) }),
    pluginErrorMessage: error => String(error),
    ElMessage: { warning: value => calls.push(['warning', value]), error: value => calls.push(['error', value]), success: value => calls.push(['success', value]), info: value => calls.push(['info', value]) },
    ElMessageBox: { confirm: async (...args) => { calls.push(['confirm', ...args]); } },
    plugins: ref([]), enabledPluginCount: { value: 0 }, pluginCanStart: () => true, quota: { admit: () => true, effective: { plan: 'free' } },
    pluginQuota: { value: { allowed: true, forbidden: false, limit: 0 } },
    usesPluginQuota: plugin => plugin.source !== 'builtin',
    paidHostBlocked: () => false,
    setPluginEnabled: async (id, enabled) => { calls.push(['enable', id, enabled]); },
    chooseNativeFile: async (title, filters) => { calls.push(['file', title, filters]); return selectedPath; },
    previewLocalPlugin: async path => { calls.push(['preview', path]); return payload(path.replace('/./', '/')); },
    previewRemotePlugin: async path => { calls.push(['preview-remote', path]); return payload(path); },
    previewMarketPluginInstall: async key => { calls.push(['preview-market', key]); return { preview: payload('/tmp/market/plugin.zip'), version: '1.0.0', sha256: 'a'.repeat(64), versionChange: 'install' }; },
    installLocalPlugin: async path => { calls.push(['install', path]); },
    // After an install the list contains the new, still disabled plugin.
    refresh: async () => { if (calls.some(call => call[0] === 'install')) context.plugins.value = [{ id: 'example', enabled: false, source: 'local', state: 'stopped' }]; },
    upgradeLocalPlugin: async (id, path) => { calls.push(['upgrade', id, path]); },
    ...overrides,
  };
  context.invalidatePreview = () => { context.previewGeneration++; context.preview.value = undefined; context.previewPath.value = ''; context.marketInstall.value = undefined; context.previewError.value = ''; context.previewLoading.value = false; };
  vm.createContext(context);
  vm.runInContext(backupCompiled, context);
  vm.runInContext(compiled, context);
  return { context, calls };
}

test('preview validates the original source path and preserves expanded counts', () => {
  for (const path of ['/plugins/example.min', '/plugins/example.zip', '/plugins/example']) {
    assert.deepEqual(parsePluginInstallPreview(payload(path)), payload(path));
  }
  for (const path of [undefined, null, 42, '', '  ']) {
    assert.throws(() => parsePluginInstallPreview({ ...payload(), sourcePath: path }), /Invalid plugin install preview/);
  }
});

test('preview keeps the global shortcuts an update would turn off', () => {
  const changed = { ...payload(), installed: true, globalShortcutsChanged: ['capture-region'] };
  assert.deepEqual(parsePluginInstallPreview(changed).globalShortcutsChanged, ['capture-region']);
  for (const value of ['capture-region', [1], {}]) {
    assert.throws(() => parsePluginInstallPreview({ ...changed, globalShortcutsChanged: value }), /Invalid plugin install preview/);
  }
});

test('preview keeps the data provider lines', () => {
  const lines = [{ point: 'sessionSources', id: 'team', title: 'Team Agent', titleLocalized: { zh: '团队 Agent' } }, { point: 'sessionSources', id: 'archive', title: 'Archive' }];
  assert.deepEqual(parsePluginInstallPreview({ ...payload(), dataProviders: lines }).dataProviders, lines);
  for (const value of ['sessionSources', [{}], [{ point: 'sessionSources', id: 'team' }], [{ ...lines[0], titleLocalized: { zh: 1 } }]]) {
    assert.throws(() => parsePluginInstallPreview({ ...payload(), dataProviders: value }), /Invalid plugin install preview/);
  }
});

test('preview accepts declarative keymap packages and rejects runtime fields on them', () => {
  const manifest = JSON.parse(readFileSync(new URL('../../official-plugins/keymaps/plugin.json', import.meta.url), 'utf8'));
  const declarative = { sourcePath: '/plugins/keymaps.zip', manifest, fileCount: 6, totalBytes: 41000, installed: false };
  assert.equal(parsePluginInstallPreview(declarative).manifest.runtime.type, 'declarative');
  const withRuntime = (runtime) => ({ ...declarative, manifest: { ...manifest, runtime: { type: 'declarative', ...runtime } } });
  for (const runtime of [{ entry: 'bin/x' }, { entrypoints: { darwin: 'bin/x' } }, { args: [] }, { activation: 'startup' }]) {
    assert.throws(() => parsePluginInstallPreview(withRuntime(runtime)), /Invalid plugin install preview/);
  }
  const keymaps = (list) => ({ ...declarative, manifest: { ...manifest, contributes: { keymaps: list } } });
  for (const list of [[], [{}], [{ path: 'keymaps/a.json', id: 'x' }], 'keymaps/a.json']) {
    assert.throws(() => parsePluginInstallPreview(keymaps(list)), /Invalid plugin install preview/);
  }
  assert.doesNotThrow(() => parsePluginInstallPreview(keymaps([{ id: 'keymaps.inline', rules: [] }])));
  assert.throws(() => parsePluginInstallPreview({ ...payload(), manifest: { ...payload().manifest, runtime: { type: 'wasm' } } }), /Invalid plugin install preview/);
});

test('preview accepts declarative packages for every data-only contribution point', () => {
  const contract = JSON.parse(readFileSync(new URL('../../plugins/api/contribution-points.json', import.meta.url), 'utf8'));
  const declarativePoints = Object.entries(contract.points)
    .filter(([, point]) => point.status === 'enabled' && point.kinds.includes('declarative'))
    .map(([key]) => key);
  assert.deepEqual([...DECLARATIVE_CONTRIBUTION_POINTS].sort(), declarativePoints.sort());

  const manifest = JSON.parse(readFileSync(new URL('../../official-plugins/themes/plugin.json', import.meta.url), 'utf8'));
  const themes = { sourcePath: '/plugins/themes', manifest, fileCount: 8, totalBytes: 20000, installed: false };
  assert.equal(parsePluginInstallPreview(themes).manifest.id, 'themes');
  const contributes = (value) => ({ ...themes, manifest: { ...manifest, contributes: value } });
  for (const value of [{}, { themes: [] }, { menus: { editor: [{ command: 'x' }] } }, { themes: [{ path: 'a.json' }], keymaps: [{}] }]) {
    assert.throws(() => parsePluginInstallPreview(contributes(value)), /Invalid plugin install preview/);
  }
  assert.doesNotThrow(() => parsePluginInstallPreview(contributes({ snippets: [{ path: 'snippets/go.json' }] })));
});

const packageFilters = [
  { DisplayName: 'plugins.packageFiles', Pattern: '*.min' },
  { DisplayName: 'plugins.legacyPackageFiles', Pattern: '*.zip' },
];
const dialogCall = calls => JSON.parse(JSON.stringify(calls.find(call => call[0] === 'file')));

for (const extension of ['min', 'zip']) {
  test(`${extension} picker offers MIN first and legacy ZIP, previews, and installs only after confirmation`, async () => {
    const path = `/plugins/example.${extension}`;
    const { context: c, calls } = harness({}, path);
    await c.chooseSourceZip();
    assert.equal(c.sourcePath.value, path);
    assert.deepEqual(dialogCall(calls), ['file', 'plugins.choosePackageTitle', packageFilters]);
    assert.deepEqual(calls[1], ['preview', path]);
    assert.equal(c.previewVisible.value, true);
    assert.equal(c.preview.value.sourcePath, path);
    assert.equal(calls.filter(call => call[0] === 'install' || call[0] === 'upgrade' || call[0] === 'enable').length, 0);
    assert.equal(c.choosingSource.value, false);
    await c.confirmInstall();
    assert.deepEqual(calls.find(call => call[0] === 'install'), ['install', path]);
    assert.match(source, /chooseNativeDirectory\(t\("plugins\.chooseDirectoryTitle"\)\);[\s\S]*?if \(accepted\) await install\(\);/);
  });

  test(`${extension} upgrade uses the same package filters and waits for explicit confirmation`, async () => {
    const path = `/plugins/example.${extension}`;
    let confirm;
    const { context: c, calls } = harness({
      ElMessageBox: { confirm: (...args) => { calls.push(['confirm', ...args]); return new Promise(resolve => { confirm = resolve; }); } },
    }, path);
    const pending = c.upgrade({ id: 'example', name: 'Example', version: '1.0.0' });
    await Promise.resolve();
    assert.deepEqual(dialogCall(calls), ['file', 'plugins.chooseUpgradePackageTitle', packageFilters]);
    const install = harness({}, path);
    await install.context.chooseSourceZip();
    assert.deepEqual(dialogCall(calls)[2], dialogCall(install.calls)[2]);
    assert.equal(calls.filter(call => call[0] === 'upgrade').length, 0);
    const confirmation = calls.find(call => call[0] === 'confirm');
    assert.equal(confirmation[1], 'plugins.upgradeConfirm');
    assert.equal(confirmation[2], 'plugins.upgradeTitle');
    assert.equal(confirmation[3].confirmButtonText, 'plugins.upgrade');
    confirm();
    await pending;
    assert.deepEqual(calls.find(call => call[0] === 'upgrade'), ['upgrade', 'example', path]);
    assert.deepEqual(calls.at(-1), ['success', 'plugins.upgradeSuccess']);
    assert.equal(c.busyId.value, '');
  });
}

test('upgrade cancellation never replaces the plugin package', async () => {
  for (const overrides of [
    { chooseNativeFile: async () => undefined },
    { chooseNativeFile: async () => null },
    { chooseNativeFile: nativePicker(async () => { throw new Error('Invalid dialog call: Dialog.OpenFile failed, error getting selection: cancelled by user'); }).chooseNativeFile },
    { ElMessageBox: { confirm: async () => { throw 'cancel'; } } },
  ]) {
    const { context: c, calls } = harness(overrides);
    await c.upgrade({ id: 'example', name: 'Example', version: '1.0.0' });
    assert.equal(calls.filter(call => call[0] === 'upgrade' || call[0] === 'error').length, 0);
    assert.equal(c.busyId.value, '');
  }
});

test('native picker cancellation is silent but real dialog failures still reject', async () => {
  const cancelled = nativePicker(async () => {
    throw new Error('Invalid dialog call: Dialog.OpenFile failed, error getting selection: cancelled by user');
  });
  assert.equal(await cancelled.chooseNativeFile('pick'), null);
  assert.equal(await cancelled.chooseNativeDirectory('pick'), null);
  assert.equal((await cancelled.chooseNativeFiles('pick')).length, 0);
  assert.equal(await cancelled.chooseNativeFileOrDirectory('pick'), null);
  assert.equal(await cancelled.chooseNativeSaveFile('save', 'x.min'), null);

  const failed = nativePicker(async () => {
    throw new Error('Invalid dialog call: Dialog.OpenFile failed, error getting selection: access denied');
  });
  await assert.rejects(() => failed.chooseNativeFile('pick'), /access denied/);
});

test('package and upgrade UI use matching semantic translations in both locales', () => {
  const keys = [
    'plugins.installPlugin', 'plugins.installFromDirectory', 'plugins.chooseDirectoryTitle', 'plugins.searchOrSource',
    'plugins.choosePackage', 'plugins.choosePackageTitle', 'plugins.packageFiles', 'plugins.legacyPackageFiles',
    'plugins.upgradeFromPackage', 'plugins.chooseUpgradePackageTitle', 'plugins.upgrade',
    'plugins.upgradeTitle', 'plugins.upgradeConfirm', 'plugins.upgradeSuccess',
  ];
  for (const key of keys) {
    assert.equal(typeof messagesEn[key], 'string', key);
    assert.equal(typeof messagesZh[key], 'string', key);
    assert.ok(messagesEn[key].length > 0 && messagesZh[key].length > 0, key);
    assert.deepEqual(messagesEn[key].match(/\{\w+\}/g), messagesZh[key].match(/\{\w+\}/g), key);
  }
  for (const messages of [messagesEn, messagesZh]) {
    assert.match(messages['plugins.packageFiles'], /\*\.min/);
    assert.match(messages['plugins.legacyPackageFiles'], /\*\.zip/);
    assert.match(messages['plugins.searchOrSource'], /\.min.*\.zip/);
    assert.match(messages['plugins.localInstallHint'], /\.min.*\.zip/);
    for (const key of ['plugins.choosePackage', 'plugins.choosePackageTitle', 'plugins.upgradeFromPackage', 'plugins.chooseUpgradePackageTitle']) {
      assert.doesNotMatch(messages[key], /ZIP/i, key);
    }
  }
  // The card action stays short ("Upgrade"); the full wording is its tooltip and accessible name.
  assert.match(source, /:aria-label="t\('plugins\.upgradeFromPackage'\)"[^\n]*@click="upgrade\(plugin\)">\{\{ t\('plugins\.upgrade'\) \}\}<\/el-button>/);
});

test('plugin data backups remain ZIP-only and keep their ZIP filename', () => {
  const backup = source.slice(source.indexOf('async function exportData('), source.indexOf('async function showLogs('));
  assert.match(backup, /`\$\{plugin.id\}-data-\$\{date\}\.zip`/);
  assert.equal((backup.match(/DisplayName: t\("plugins\.dataArchive"\), Pattern: "\*\.zip"/g) ?? []).length, 2);
  assert.doesNotMatch(backup, /\*\.min|plugins\.packageFiles|plugins\.legacyPackageFiles/);
});

test('cancel, dialog error, and unmount preserve the current source', async () => {
  for (const picker of [async () => undefined, async () => { throw new Error('Dialog failed'); }]) {
    const { context: c, calls } = harness({ chooseNativeFile: picker });
    c.sourcePath.value = '/existing';
    await c.chooseSourceZip();
    assert.equal(c.sourcePath.value, '/existing');
    assert.equal(c.choosingSource.value, false);
    assert.equal(c.previewVisible.value, false);
    assert.equal(calls.filter(call => call[0] === 'preview').length, 0);
  }
  let resolve;
  const { context: c } = harness({ chooseNativeFile: () => new Promise(done => { resolve = done; }) });
  const pending = c.chooseSourceZip();
  c.disposed = true;
  resolve('/new.zip');
  await pending;
  assert.equal(c.sourcePath.value, '');
});

test('browser preview never opens dialogs or invokes preview/install', async () => {
  const { context: c, calls } = harness({ isDesktopRuntime: () => false });
  c.sourcePath.value = '/plugins/example.zip';
  await c.chooseSourceZip();
  await c.install();
  c.preview.value = payload();
  c.previewPath.value = c.sourcePath.value;
  await c.confirmInstall();
  assert.equal(calls.length, 3);
  assert.ok(calls.every(call => call[0] === 'warning' && call[1] === 'common.desktopRequired'));
});

test('preview requires explicit confirmation and installs the normalized archive source', async () => {
  const { context: c, calls } = harness();
  c.sourcePath.value = ' /plugins/./example.zip ';
  await c.install();
  assert.equal(c.preview.value.sourcePath, '/plugins/example.zip');
  assert.equal(calls.filter(call => call[0] === 'install').length, 0);
  await c.confirmInstall();
  assert.deepEqual(calls.find(call => call[0] === 'install'), ['install', '/plugins/example.zip']);
  assert.match(source, /plugins.previewSource', \{ path: preview.sourcePath \}/);
});

test('busy operations cannot race source selection or preview', async () => {
  for (const flag of ['choosingSource', 'installing', 'previewLoading']) {
    const { context: c, calls } = harness();
    c.sourcePath.value = '/plugins/example.zip';
    c[flag].value = true;
    await c.chooseSourceZip();
    await c.install();
    assert.equal(calls.length, 0, flag);
  }
});

test('closed or changed previews cannot confirm stale installation', async () => {
  let resolve;
  const { context: c, calls } = harness({ previewLocalPlugin: () => new Promise(done => { resolve = done; }) });
  c.sourcePath.value = '/plugins/example.zip';
  const pending = c.install();
  c.previewVisible.value = false;
  c.invalidatePreview();
  resolve(payload());
  await pending;
  assert.equal(c.preview.value, undefined);
  await c.confirmInstall();
  c.preview.value = payload();
  c.previewPath.value = '/plugins/old.zip';
  await c.confirmInstall();
  c.previewPath.value = c.sourcePath.value;
  c.preview.value.installed = true;
  await c.confirmInstall();
  assert.equal(calls.filter(call => call[0] === 'install').length, 0);
});
