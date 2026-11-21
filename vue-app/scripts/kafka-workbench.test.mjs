import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc';

const uiDir = new URL('../../official-plugins/kafka-inspector/ui-src/', import.meta.url);
const vueFiles = (await readdir(uiDir)).filter(name => name.endsWith('.vue'));
const sources = Object.fromEntries(await Promise.all(vueFiles.map(async name => [name, await readFile(new URL(name, uiDir), 'utf8')])));
const source = sources['KafkaWorkbench.vue'];
const locales = await readFile(new URL('locales.mjs', uiDir), 'utf8');
const { en, zh } = await import(new URL('locales.mjs', uiDir));
const { filterMessages, parseImport, exportProfiles } = await import(new URL('model.mjs', uiDir));
const view = text => ({ data: text, encoding: 'utf8', size: Buffer.byteLength(text) });
const b64view = text => ({ data: Buffer.from(text).toString('base64'), encoding: 'base64', size: Buffer.byteLength(text) });

test('Kafka components compile with bindings and templates', () => {
  assert.ok(source, 'KafkaWorkbench.vue exists');
  for (const [name, text] of Object.entries(sources)) {
    const { descriptor, errors } = parse(text);
    assert.deepEqual(errors, [], name);
    const script = compileScript(descriptor, { id: `kafka-${name}` });
    const result = compileTemplate({ source: descriptor.template.content, filename: name, id: `kafka-${name}`, compilerOptions: { bindingMetadata: script.bindings } });
    assert.deepEqual(result.errors, [], name);
  }
});

test('local message filtering searches decoded keys, values and headers', () => {
  const rows = [
    { offset: 1, key: null, value: view('{"event":"Created"}'), headers: [{ key: 'trace', value: b64view('abc') }] },
    { offset: 2, key: view('second'), value: null, headers: [] },
  ];
  assert.equal(filterMessages(rows, 'created').length, 1);
  assert.equal(filterMessages(rows, 'ABC').length, 1);
  assert.equal(filterMessages(rows, 'second')[0], rows[1]);
  assert.equal(filterMessages(rows, '').length, 2);
  assert.equal(filterMessages(rows, 'missing').length, 0);
});

test('dedicated workbench is routed only to Kafka and uses escaped display', async () => {
  const mount = await readFile(new URL('../src/plugin-ui/mount.js', import.meta.url), 'utf8');
  assert.match(await readFile(new URL('entry.js', uiDir), 'utf8'), /KafkaWorkbench\.vue/);
  assert.doesNotMatch(mount, /KafkaWorkbench|config.id/);
  for (const [name, text] of Object.entries(sources)) {
    assert.doesNotMatch(text, /v-html|localStorage|sessionStorage|navigator\.clipboard/, name);
    assert.doesNotMatch(text, /<select\b|<option\b/, name);
  }
  assert.match(locales, /仅搜索本次已读取结果/);
});

test('zh and en locales define the same keys', () => {
  assert.deepEqual(Object.keys(zh).sort(), Object.keys(en).sort());
});

test('profile import/export never carries secrets', () => {
  const parsed = parseImport(JSON.stringify({ profiles: [{ name: 'a', brokers: 'b1:9092, b2:9092', password: 'x', username: 'u' }] }));
  assert.deepEqual(parsed.profiles[0].brokers, ['b1:9092', 'b2:9092']);
  assert.equal(parsed.profiles[0].password, undefined);
  assert.equal(parsed.strippedSecrets, 1);
  assert.doesNotMatch(exportProfiles([{ name: 'a', brokers: ['b:9092'], password: 'x' }]), /password/);
});

test('iframe has one outer scroll owner and bounded desktop Kafka panels', async () => {
  const css = await readFile(new URL('../src/plugin-ui/page.css', import.meta.url), 'utf8');
  assert.match(css, /html, body \{ overflow: hidden; \}/);
  assert.match(css, /#app \{ overflow: hidden; \}/);
  assert.match(css, /\.plugin-page-scrollbar \{ height: 100%; \}/);
  const mount = await readFile(new URL('../src/plugin-ui/mount.js', import.meta.url), 'utf8');
  assert.match(mount, /h\(ElScrollbar/);
  assert.match(css, /\.plugin-workbench \{ flex: 1; min-height: 0; grid-template-rows: minmax\(0, 1fr\); align-items: stretch;/);
  assert.match(css, /@media \(min-width: 721px\)/);
});

test('Kafka surfaces advertised.listeners diagnostics and keeps writes out of read-only mode', () => {
  assert.match(sources['ClusterPanel.vue'], /diagnostics/);
  assert.match(locales, /advertised\.listeners/);
  assert.match(source, /readOnly/);
  assert.match(sources['MessagesPanel.vue'], /readOnly \|\| !!busy/);
});
