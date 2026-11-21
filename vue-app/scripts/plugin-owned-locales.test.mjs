import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { readFile, readdir } from 'node:fs/promises';
import { messages, normalizeLocale, pluginMessages } from '../src/plugin-ui/messages.mjs';
import { buildPayload } from '../src/plugin-ui/form-payload.mjs';
import { en, zh } from '../../official-plugins/etcd-manager/ui-src/locales.mjs';

const officialPlugins = new URL('../../official-plugins/', import.meta.url);
const pluginTemplates = new URL('../../tools/plugin-templates/', import.meta.url);
test('plugin dictionaries have matching keys and deterministic locale fallback', () => {
  for (const [a, b] of [[messages.en, messages.zh], [en, zh]]) {
    assert.deepEqual(Object.keys(a).sort(), Object.keys(b).sort());
    for (const value of [...Object.values(a), ...Object.values(b)]) assert.ok(typeof value === 'string' && value.trim());
  }
  for (const locale of ['zh', 'zh-CN', 'zh_TW', 'ZH-cn']) assert.equal(normalizeLocale(locale), 'zh');
  for (const locale of ['en', 'en-US', 'fr', '', undefined, {}]) assert.equal(normalizeLocale(locale), 'en');
  assert.equal(pluginMessages('en-US').clear, 'Clear results');
  assert.equal(pluginMessages('zh-CN').clear, '清空结果');
});
test('shared payload validation uses the selected plugin language', () => {
  const config = { fields: [{ id: 'root' }] };
  assert.throws(() => buildPayload(config, {}, '', 'en'), /absolute project path/);
  assert.throws(() => buildPayload(config, {}, '', 'zh'), /项目绝对路径/);
});
test('all official and template menu contributions own zh/en titles', async () => {
  for (const base of [officialPlugins, pluginTemplates]) {
    for (const entry of await readdir(base, { withFileTypes: true })) {
      if (!entry.isDirectory() || entry.name === 'node_modules') continue;
      const filename = existsSync(new URL(`${entry.name}/plugin.template.json`, base)) ? 'plugin.template.json' : 'plugin.json';
      const manifestURL = new URL(`${entry.name}/${filename}`, base);
      // 已移除官方插件可能留下被忽略的 bin/ui 产物；模板清单仍必须存在。
      if (base === officialPlugins && !existsSync(manifestURL)) continue;
      const manifest = JSON.parse(await readFile(manifestURL, 'utf8'));
      for (const item of [...(manifest.contributes?.commands ?? []), ...(manifest.contributes?.views ?? [])]) {
        assert.ok(item.titleLocalized?.zh?.trim(), `${entry.name}/${item.id}: missing zh`);
        assert.ok(item.titleLocalized?.en?.trim(), `${entry.name}/${item.id}: missing en`);
      }
    }
  }
});
test('shared UI reacts to host locale and provides Element Plus and result locales', async () => {
  const page = await readFile(new URL('../src/plugin-ui/OfficialPluginPage.vue', import.meta.url), 'utf8');
  assert.match(page, /locale\.value = normalizeLocale\(context\?\.locale\)/);
  assert.match(page, /provide\(pluginLocaleKey, locale\)/);
  assert.match(page, /computed\(\(\) => pluginMessages\(locale\.value\)\)/);
  assert.match(page, /<ElConfigProvider :locale="locale === 'zh' \? zhLocale : enLocale"/);
  const results = await readFile(new URL('../src/plugin-ui/PluginResults.vue', import.meta.url), 'utf8');
  assert.match(results, /inject\(pluginLocaleKey/);
  assert.match(results, /computed\(\(\) => pluginMessages\(locale\.value\)\)/);
});
test('plugin source does not import host i18n', async () => {
  async function inspect(directory) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const url = new URL(entry.name + (entry.isDirectory() ? '/' : ''), directory);
      if (entry.isDirectory()) await inspect(url);
      else if (/\.(?:vue|m?js|ts)$/.test(entry.name)) assert.doesNotMatch(await readFile(url, 'utf8'), /(?:from\s*|import\s*\()["'][^"']*(?:src\/i18n|@\/i18n)/, url.pathname);
    }
  }
  await inspect(new URL('../src/plugin-ui/', import.meta.url));
  for (const entry of await readdir(officialPlugins, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    // Declarative plugins (keymaps) ship data only and have no ui-src.
    const uiSource = new URL(`${entry.name}/ui-src/`, officialPlugins);
    if (!existsSync(uiSource)) continue;
    await inspect(uiSource);
  }
});
