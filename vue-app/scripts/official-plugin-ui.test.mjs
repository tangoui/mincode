import test from 'node:test';
import assert from 'node:assert/strict';
import { access, readFile, readdir } from 'node:fs/promises';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
import { elementComponentName, elementStylesModule, scanElementComponents } from './plugin-ui-element-styles.mjs';

const officialPackagesRoot = new URL('../../official-plugins/', import.meta.url);
const officialUIPackages = [];
for (const entry of await readdir(officialPackagesRoot, { withFileTypes: true })) {
  if (!entry.isDirectory()) continue;
  try {
    await access(new URL(`${entry.name}/ui-src/entry.js`, officialPackagesRoot));
    officialUIPackages.push(entry.name);
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
}
officialUIPackages.sort();

// transport.js imports the self-contained theme-tokens.js helper; inline it so
// the transport runs as a classic script inside the vm sandbox.
const themeTokensSource = (await readFile(new URL('../src/plugin-ui/theme-tokens.js', import.meta.url), 'utf8')).replaceAll('export function', 'function');
const transportSource = (await readFile(new URL('../src/plugin-ui/transport.js', import.meta.url), 'utf8'));
assert.match(transportSource, /^import \{ applyThemeTokens \} from '\.\/theme-tokens\.js';$/m);
const source = themeTokensSource + transportSource.replace(/^import \{ applyThemeTokens \} from '\.\/theme-tokens\.js';$/m, '').replace('export function', 'function');
test('transport rejects foreign handshake and preserves host error', async () => {
  const listeners = new Map();
  const parent = { postMessage() {} };
  let ready = false;
  const delays = [];
  const context = vm.createContext({ URLSearchParams, location: { hash: '#mincodeSession=s&nonce=n&generation=1' }, parent, setTimeout: (_fn, delay) => { delays.push(delay); return delays.length; }, clearTimeout() {}, addEventListener: (type, fn) => listeners.set(type, fn), removeEventListener: type => listeners.delete(type) });
  vm.runInContext(source, context);
  const transport = context.createTransport({ onReady() { ready = true; }, onError() {}, applyContext() {} });
  const port = { start() {}, close() {}, postMessage(message) { this.request = message; } };
  const event = { source: {}, data: { type: 'mincode.host.connect', protocolVersion: 1, sessionId: 's', nonce: 'n', generation: 1, requestTimeoutMs: 30000 }, ports: [port] };
  listeners.get('message')(event);
  assert.equal(ready, false);
  listeners.get('message')({ ...event, source: parent });
  assert.equal(ready, true);
  const result = transport.call('compare', {});
  assert.equal(delays.at(-1), 35000);
  port.onmessage({ data: { type: 'mincode.host.response', id: port.request.id, ok: false, error: { message: '文件不存在' } } });
  await assert.rejects(result, /文件不存在/);
  transport.close();
});
test('transport exposes only negotiated clipboard write and trusted confirmation', async () => {
  const listeners = new Map(), sent = [];
  const parent = { postMessage() {} };
  const context = vm.createContext({ URLSearchParams, location: { hash: '#mincodeSession=s&nonce=n&generation=1' }, parent,
    setTimeout: () => 1, clearTimeout() {}, addEventListener: (type, fn) => listeners.set(type, fn), removeEventListener() {} });
  vm.runInContext(source, context);
  const transport = context.createTransport({ onReady() {}, onError() {}, applyContext() {} });
  const port = { start() {}, close() {}, postMessage(message) { sent.push(message); } };
  listeners.get('message')({ source: parent, data: { type: 'mincode.host.connect', protocolVersion: 1, sessionId: 's', nonce: 'n', generation: 1,
    features: { tasks: 1, clipboardWrite: 1, trustedConfirm: 1 }, context: { locale: 'en' } }, ports: [port] });
  assert.equal('readText' in transport.clipboard, false);
  await assert.rejects(transport.clipboard.writeText(42), error => error.code === 'invalid-request');
  await assert.rejects(transport.confirm({ intent: 'read', subject: 'inspect' }), error => error.code === 'invalid-request');
  const copy = transport.clipboard.writeText('secret', { sensitive: true });
  assert.deepEqual({ type: sent.at(-1).type, text: sent.at(-1).text, sensitive: sent.at(-1).sensitive },
    { type: 'mincode.plugin.clipboard.write-text', text: 'secret', sensitive: true });
  port.onmessage({ data: { type: 'mincode.host.response', id: sent.at(-1).id, ok: false, error: { code: 'cancelled', message: 'cancelled' } } });
  await assert.rejects(copy, error => error.cancelled === true);
  const confirmation = transport.confirm({ intent: 'dangerous', subject: 'delete item' });
  assert.deepEqual({ type: sent.at(-1).type, intent: sent.at(-1).intent, subject: sent.at(-1).subject },
    { type: 'mincode.plugin.confirm', intent: 'dangerous', subject: 'delete item' });
  port.onmessage({ data: { type: 'mincode.host.response', id: sent.at(-1).id, ok: true, result: false } });
  assert.equal(await confirmation, false);
  transport.close();
});

test('transport forwards host-read desktop drops only to pages that opt in', () => {
  const connect = onFilesDropped => {
    const listeners = new Map();
    const parent = { postMessage() {} };
    const context = vm.createContext({ URLSearchParams, location: { hash: '#mincodeSession=s&nonce=n&generation=1' }, parent,
      setTimeout: () => 1, clearTimeout() {}, addEventListener: (type, fn) => listeners.set(type, fn), removeEventListener() {} });
    vm.runInContext(source, context);
    const transport = context.createTransport({ onReady() {}, onError() {}, applyContext() {}, onFilesDropped });
    const port = { start() {}, close() {}, postMessage() {} };
    listeners.get('message')({ source: parent, data: { type: 'mincode.host.connect', protocolVersion: 1, sessionId: 's', nonce: 'n', generation: 1 }, ports: [port] });
    return { port, transport };
  };
  const drops = [];
  const { port, transport } = connect(files => drops.push(files));
  const files = [{ name: 'a.txt', size: 1, lastModified: 0, data: 'YQ==' }];
  port.onmessage({ data: { type: 'mincode.host.files-dropped', dropId: 1, files } });
  port.onmessage({ data: { type: 'mincode.host.files-dropped', dropId: 2, files: 'not-a-list' } });
  assert.deepEqual(drops, [files]);
  transport.close();
  const other = connect(undefined);
  assert.doesNotThrow(() => other.port.onmessage({ data: { type: 'mincode.host.files-dropped', dropId: 1, files } }));
  other.transport.close();
});

test('transport keeps the legacy timeout when an older host omits timeout negotiation', async () => {
  const listeners = new Map();
  const delays = [];
  const parent = { postMessage() {} };
  const context = vm.createContext({
    URLSearchParams,
    location: { hash: '#mincodeSession=s&nonce=n&generation=1' },
    parent,
    setTimeout: (_fn, delay) => { delays.push(delay); return delays.length; },
    clearTimeout() {},
    addEventListener: (type, fn) => listeners.set(type, fn),
    removeEventListener: type => listeners.delete(type),
  });
  vm.runInContext(source, context);
  const transport = context.createTransport({ onReady() {}, onError() {}, applyContext() {} });
  const port = { start() {}, close() {}, postMessage() {} };
  listeners.get('message')({
    source: parent,
    data: { type: 'mincode.host.connect', protocolVersion: 1, sessionId: 's', nonce: 'n', generation: 1 },
    ports: [port],
  });
  const request = transport.call('compare', {});
  assert.equal(delays.at(-1), 45000);
  const closed = assert.rejects(request, /插件页面已关闭/);
  transport.close();
  await closed;
});

test('official migrated sources and bundles use the host clipboard and trusted confirmation', async () => {
  for (const name of ['kafka-inspector', 'network-diagnostics', 'openapi-contract', 'certificate-manager']) {
    const files = await readdir(new URL(`${name}/ui-src/`, officialPackagesRoot));
    const vueSources = await Promise.all(files.filter(file => file.endsWith('.vue'))
      .map(file => readFile(new URL(`${name}/ui-src/${file}`, officialPackagesRoot), 'utf8')));
    assert.ok(vueSources.some(component => component.includes('transport.clipboard.writeText')), `${name}: missing host clipboard call`);
    const bundle = await readFile(new URL(`${name}/ui/app.js`, officialPackagesRoot), 'utf8');
    assert.doesNotMatch(bundle, /navigator\.clipboard/);
    assert.match(bundle, /mincode\.plugin\.clipboard\.write-text/);
  }
  const certificateSource = await readFile(new URL('certificate-manager/ui-src/CertificateManager.vue', officialPackagesRoot), 'utf8');
  assert.match(certificateSource, /transport\.confirm\(\{intent:'dangerous',subject\}\)/);
  const certificateBundle = await readFile(new URL('certificate-manager/ui/app.js', officialPackagesRoot), 'utf8');
  assert.match(certificateBundle, /mincode\.plugin\.confirm/);
});

// ui/app.js plus the ui/chunks/*.js it imports on demand.
async function pageScripts(name) {
  const base = new URL(`${name}/ui/`, officialPackagesRoot);
  const chunks = await readdir(new URL('chunks/', base)).catch(error => {
    if (error.code === 'ENOENT') return [];
    throw error;
  });
  const scripts = { 'app.js': await readFile(new URL('app.js', base), 'utf8') };
  for (const chunk of chunks.sort()) scripts[`chunks/${chunk}`] = await readFile(new URL(`chunks/${chunk}`, base), 'utf8');
  return scripts;
}

test('all official packages build independent UI without remote dependencies', async () => {
  const bundles = new Set();
  for (const name of officialUIPackages) {
    const base = new URL(`../../official-plugins/${name}/ui/`, import.meta.url);
    const scripts = await pageScripts(name);
    const js = scripts['app.js'];
    assert.ok(!bundles.has(js), `${name} must have its own bundle`);
    bundles.add(js);
    try {
      await access(new URL('config.js', base));
      assert.match(js, /from\s*"\.\/config\.js"/);
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
    for (const [file, code] of Object.entries(scripts)) {
      assert.doesNotMatch(code, /eval\(|new Function\(/, `${name}/ui/${file}`);
      assert.doesNotMatch(code, /process\.env\.NODE_ENV/, `${name}/ui/${file}`);
      if (file !== 'app.js') assert.ok(Object.values(scripts).some(other => other.includes(`./${file.slice('chunks/'.length)}`) || other.includes(`./${file}`)), `${name}/ui/${file} is not imported`);
    }
    const html = await readFile(new URL('index.html', base), 'utf8');
    assert.match(html, /id="app"/);
    assert.doesNotMatch(html, /https?:/);
  }
});

test('all official bundles include form-item layout and responsive workbench styles', async () => {
  for (const name of officialUIPackages) {
    const css = await readFile(new URL(`../../official-plugins/${name}/ui/style.css`, import.meta.url), 'utf8');
    assert.match(css, /\.el-button/);
    assert.match(css, /(?:max-width:|width\s*<=)\s*(?:720|860)px/);
    // Element Plus stylesheets follow the components a page renders, so form-item layout ships
    // exactly when ElFormItem is part of the bundle.
    const js = await readFile(new URL(`../../official-plugins/${name}/ui/app.js`, import.meta.url), 'utf8');
    if (/name:[`"']ElFormItem[`"']/.test(js)) {
      assert.match(css, /\.el-form-item__content/, name);
      assert.match(css, /\.el-form-item__label/, name);
    }
    if (css.includes('.official-plugin-page')) {
      assert.match(css, /\.plugin-field-wide/);
      const pageRule = css.match(/\.official-plugin-page\s*\{([^}]+)\}/)?.[1];
      assert.ok(pageRule, 'page layout rule must exist');
      assert.match(pageRule, /width:\s*100%/);
      assert.match(pageRule, /padding:\s*16px/);
      assert.doesNotMatch(pageRule, /max-width|margin:[^;]*auto/);
    }
  }
});

test('heavy page dependencies load as on-demand chunks', async () => {
  const lazy = { 'pdf-toolkit': /\/\*! pdf\.js/, 'ebook-reader': /\/\*! pdf\.js/, 'docker-manager': /xterm/i, 'kubernetes-manager': /xterm/i };
  for (const [name, marker] of Object.entries(lazy)) {
    const scripts = await pageScripts(name);
    const chunks = Object.keys(scripts).filter(file => file.startsWith('chunks/'));
    assert.ok(chunks.length, `${name}: no ui/chunks/`);
    assert.ok(chunks.some(file => marker.test(scripts[file])), `${name}: heavy dependency not in a chunk`);
    assert.match(scripts['app.js'], /import\(\s*["'`]\.\/chunks\//, `${name}: app.js must import its chunks on demand`);
  }
  // The license banner survives minification and stays out of the initial bundle.
  for (const name of ['pdf-toolkit', 'ebook-reader']) {
    const pdf = await pageScripts(name);
    assert.doesNotMatch(pdf['app.js'], /\/\*! pdf\.js/, `${name}: pdf.js must not be in the initial bundle`);
  }
  const pdf = await pageScripts('pdf-toolkit');
});

test('official pages use EL scrollbars with bounded columns and a shared page fallback', async () => {
  const source = await readFile(new URL('../src/plugin-ui/OfficialPluginPage.vue', import.meta.url), 'utf8');
  assert.match(source, /<ElScrollbar class="plugin-panel-scrollbar" always><aside class="plugin-parameters">/);
  assert.match(source, /<ElScrollbar class="plugin-panel-scrollbar" always><section class="plugin-result"/);
  assert.equal((source.match(/<ElScrollbar\b/g) || []).length, 2);
  assert.equal((source.match(/<\/ElScrollbar>/g) || []).length, 2);
  // Kafka 改用连接工作台后，侧栏由工作台提供，结果区仍包在 EL 滚动容器里。
  const kafka = await readFile(new URL('../../official-plugins/kafka-inspector/ui-src/KafkaWorkbench.vue', import.meta.url), 'utf8');
  assert.match(kafka, /<PluginConnectionWorkbench\b/);
  assert.match(kafka, /<ElScrollbar class="kafka-scroll">\s*<section v-if="session" class="kafka-result"/);
  const entry = await readFile(new URL('../src/plugin-ui/mount.js', import.meta.url), 'utf8');
  assert.match(entry, /h\(ElScrollbar, \{ class: 'plugin-page-scrollbar' \}/);
  const css = await readFile(new URL('../src/plugin-ui/page.css', import.meta.url), 'utf8');
  assert.match(css, /grid-template-rows: minmax\(0, 1fr\)/);
  assert.match(css, /\.official-plugin-page:not\(\.developer-workbench\)/);
  assert.match(css, /\.plugin-panel-scrollbar \{ height: auto; \}/);
});
test('native iframe scrollbars share thin WebKit and standard styling in shipped assets', async () => {
  const shared = await readFile(new URL('../src/plugin-ui/scrollbar.css', import.meta.url), 'utf8');
  assert.match(shared, /scrollbar-width: thin/);
  assert.match(shared, /\*::-webkit-scrollbar \{ width: 6px; height: 6px;/);
  assert.doesNotMatch(shared, /display:\s*none|overflow:\s*hidden/);
  const packages = new URL('../../official-plugins/', import.meta.url);
  assert.equal(await readFile(new URL('hbuilder-simulator/ui/scrollbar.css', packages), 'utf8'), shared);
  assert.match(await readFile(new URL('hbuilder-simulator/ui/index.html', packages), 'utf8'), /id="app"/);
  for (const name of officialUIPackages) {
    const css = await readFile(new URL(`${name}/ui/style.css`, packages), 'utf8');
    assert.match(css, /scrollbar-width:thin/);
    const rule = css.match(/(?:\*)?::-webkit-scrollbar\{([^}]+)\}/g)?.find(rule => rule.includes('width:6px') && rule.includes('height:6px'));
    assert.ok(rule, `${name}: missing thin WebKit scrollbar`);
  }
});

// Opening tags of a component; a `>` inside a quoted attribute (such as an arrow function) does not end the tag.
function openingTags(text, component) {
  const tags = [];
  for (let start = text.indexOf(`<${component}`); start >= 0; start = text.indexOf(`<${component}`, start + 1)) {
    let quote = '';
    for (let i = start; i < text.length; i++) {
      const char = text[i];
      if (quote) { if (char === quote) quote = ''; } else if (char === '"' || char === "'") quote = char;
      else if (char === '>') { tags.push(text.slice(start, i + 1)); break; }
    }
  }
  return tags;
}
test('connection workbench detail is bound to a per-profile function', async () => {
  // PluginConnectionWorkbench calls detail(profile) for every saved profile and the active one;
  // a string makes each sidebar row throw and render empty.
  const offenders = [];
  for (const name of officialUIPackages) {
    const dir = new URL(`${name}/ui-src/`, officialPackagesRoot);
    for (const file of (await readdir(dir)).filter(item => item.endsWith('.vue'))) {
      const text = await readFile(new URL(file, dir), 'utf8');
      for (const tag of openingTags(text, 'PluginConnectionWorkbench')) {
        const binding = /\s:detail="([^"]*)"/.exec(tag)?.[1];
        if (binding === undefined) continue;
        const reference = /^[A-Za-z_$][\w$]*$/.test(binding.trim());
        const arrow = /^\(?\s*[A-Za-z_$][\w$]*\s*\)?\s*=>/.test(binding.trim());
        if (!reference && !arrow) offenders.push(`${name}/${file}: :detail="${binding}"`);
      }
    }
  }
  assert.deepEqual(offenders, []);
});

test('all official UI sources avoid full Element Plus CSS and native business controls', async () => {
  assert.ok(officialUIPackages.length > 0);
  for (const name of officialUIPackages) {
    const entry = await readFile(new URL(`${name}/ui-src/entry.js`, officialPackagesRoot), 'utf8');
    assert.doesNotMatch(entry, /element-plus\/dist\/index\.css/, `${name}: full Element Plus CSS is forbidden`);
    const sourceFiles = await readdir(new URL(`${name}/ui-src/`, officialPackagesRoot));
    for (const file of sourceFiles.filter(file => file.endsWith('.vue'))) {
      const component = await readFile(new URL(`${name}/ui-src/${file}`, officialPackagesRoot), 'utf8');
      assert.doesNotMatch(component, /<(?:button|input|select|textarea|table|dialog)\b/i, `${name}/${file}: native business control is forbidden`);
    }
  }
});

test('HBuilder workbench uses Element Plus and keeps device actions and collapsed logs', async () => {
  const page = await readFile(new URL('../../official-plugins/hbuilder-simulator/ui-src/HBuilderWorkbench.vue', import.meta.url), 'utf8');
  for (const component of ['ElForm', 'ElFormItem', 'ElInput', 'ElButton', 'ElSelect', 'ElCollapseItem']) assert.ok(page.includes(`<${component}`));
  for (const action of ['inspect', 'openProject', 'startSimulator', 'stopSimulator', 'startH5', 'stopH5', 'readLog', 'openH5', 'androidScreenshot', 'androidLogcat', 'androidKey', 'androidInstall', 'androidLaunch', 'androidTap']) assert.ok(page.includes(`'${action}'`), action);
  assert.match(page, /const logs = ref\(\[\]\), deviceLogs = ref\(\[\]\)/);
  assert.match(page, /max-height: 220px/);
  assert.match(page, /max-width: 720px/);
  assert.doesNotMatch(page, /<button|<input|<select/);
});

test('knowledge base serializes reactive documents and exposes the compact editor workbench', async () => {
  const { toDocumentPayload } = await import('../../official-plugins/knowledge-base/ui-src/document-payload.mjs');
  const reactiveDocument = new Proxy({
    id: undefined,
    library: 'default',
    title: '测试笔记',
    content: '# 内容',
    folder: '开发/接口',
    tags: '测试',
    source: '',
    favorite: false,
    internalState: { dirty: true },
  }, {});
  assert.throws(() => structuredClone(reactiveDocument));
  const payload = toDocumentPayload(reactiveDocument);
  assert.doesNotThrow(() => structuredClone(payload));
  assert.deepEqual(Object.keys(payload), ['id', 'library', 'title', 'content', 'folder', 'tags', 'source', 'favorite']);

  const source = await readFile(new URL('../../official-plugins/knowledge-base/ui-src/KnowledgeWorkbench.vue', import.meta.url), 'utf8');
  assert.match(source, /transport\.call\('save', toDocumentPayload\(draft\.value\)\)/);
  assert.match(source, /class=\"kb-search\"/);
  const css = await readFile(new URL('../../official-plugins/knowledge-base/ui-src/workbench.css', import.meta.url), 'utf8');
  assert.match(css, /\.kb-search\s*\{[^}]*display:flex/);
  assert.match(css, /\.kb-search \.el-input\s*\{[^}]*min-width:0/);
  assert.doesNotMatch(source, /#append/);
  assert.match(source, /<ElConfigProvider :locale=\"elementLocale\">/);
  assert.match(source, /<ElForm label-position="top"[^>]*>/);
  assert.match(source, /class="kb-split"/);
  assert.match(source, /class=\"kb-preview\"/);
  assert.match(source, /FORBID_TAGS: \['img', 'iframe', 'video', 'audio'\]/);
  assert.match(source, /transport\.call\('save', toDocumentPayload\(draft\.value\)\)/);
  assert.match(css, /@media\(max-width:700px\)/);
});

// Dropdowns render outside the workbench; their component styles must ship in the plugin bundle.
test('knowledge base ships its own locale dictionaries and category dropdown styles', async () => {
  const base = '../../official-plugins/knowledge-base/';
  const entry = await readFile(new URL(base + 'ui-src/entry.js', import.meta.url), 'utf8');
  const locale = await readFile(new URL(base + 'ui-src/locales.mjs', import.meta.url), 'utf8');
  assert.match(locale, /messages\s*=\s*\{/);
  assert.match(locale, /'app.title': '知识库'/);
  assert.match(locale, /'app.title': 'Knowledge Base'/);
  assert.match(entry, /import 'element-plus\/theme-chalk\/el-dropdown\.css'/);
  const css = await readFile(new URL(base + 'ui/style.css', import.meta.url), 'utf8');
  assert.match(css, /\.el-dropdown-menu\s*\{[^}]*list-style:\s*none/);
  assert.match(css, /\.el-dropdown-menu__item\s*\{[^}]*display:\s*flex/);
  assert.match(css, /\.el-dropdown-menu__item--divided\s*\{[^}]*border-top:/);
});

test('shared mount keeps the fixed Element Plus set for other builds and official builds derive it', async () => {
  const vueApp = new URL('../', import.meta.url);
  const root = fileURLToPath(vueApp);
  const mount = await readFile(new URL('src/plugin-ui/mount.js', vueApp), 'utf8');
  assert.match(mount, /^import '\.\/element-plus-styles\.js';$/m);
  assert.doesNotMatch(mount, /theme-chalk/);
  const fixed = await readFile(new URL('src/plugin-ui/element-plus-styles.js', vueApp), 'utf8');
  assert.equal((fixed.match(/^import 'element-plus\/theme-chalk\/[\w-]+\.css';$/gm) || []).length, 21);
  assert.equal(elementComponentName(root, 'ElInputNumber'), 'input-number');
  assert.equal(elementComponentName(root, 'vLoading'), 'loading');
  assert.equal(elementComponentName(root, 'ElTableColumn'), 'table-column');
  assert.equal(elementComponentName(root, 'useLocale'), undefined);
  const todo = await scanElementComponents(root, fileURLToPath(new URL('../official-plugins/todo/ui-src/entry.js', vueApp)));
  for (const name of ['button', 'date-picker', 'dialog', 'scrollbar']) assert.ok(todo.includes(name), name);
  assert.ok(!todo.includes('table'), 'todo renders no table');
  assert.equal(elementStylesModule(['button']), "import 'element-plus/theme-chalk/base.css';\nimport 'element-plus/es/components/button/style/css.mjs';\n");
});
