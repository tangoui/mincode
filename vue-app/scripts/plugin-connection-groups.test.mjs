import test from 'node:test';
import assert from 'node:assert/strict';
import {
  ENVIRONMENTS, environmentTagType, groupConnections, groupNames, withMeta, metaFromImport, metaAssignments,
} from '../src/plugin-ui/connection-groups.mjs';
import { messages } from '../src/plugin-ui/messages.mjs';

const profiles = [
  { id: 'p1', name: 'order-db', host: '10.0.0.1' },
  { id: 'p2', name: 'chat-main', host: '10.0.0.2' },
  { id: 'p3', name: 'chat-backup', host: '10.0.0.3' },
  { id: 'p4', name: 'scratch', host: 'localhost' },
  { id: 'p5', name: 'billing', host: '10.0.0.5' },
  { id: 'p6', name: 'lower-case', host: '10.0.0.6' },
];
const meta = {
  p1: { group: '订单', environment: 'prod' },
  p2: { group: 'IM', environment: 'dev' },
  p3: { group: 'IM', environment: 'test' },
  p5: { group: 'Billing', environment: 'staging' },
  p6: { group: 'im' },
  ghost: { group: 'Ghost', environment: 'prod' },
};
const names = groups => groups.map(g => g.name);
const ids = groups => groups.flatMap(g => g.items.map(i => i.profile.id));

test('environment codes and tag colors are fixed', () => {
  assert.deepEqual(ENVIRONMENTS, ['dev', 'test', 'staging', 'prod']);
  assert.equal(environmentTagType('dev'), 'info');
  assert.equal(environmentTagType('test'), 'primary');
  assert.equal(environmentTagType('staging'), 'warning');
  assert.equal(environmentTagType('prod'), 'danger');
  for (const env of ['', undefined, null, 'PROD', 'production', 'qa']) assert.equal(environmentTagType(env), '');
});

test('groups sort by name, ungrouped comes last, items keep their order and unknown IDs are ignored', () => {
  const groups = groupConnections(profiles, meta, { locale: 'en' });
  assert.deepEqual(names(groups), ['Billing', 'im', 'IM', '订单', '']);
  assert.equal(groups.at(-1).name, '');
  assert.deepEqual(groups.at(-1).items.map(i => i.profile.id), ['p4']);
  assert.deepEqual(groups.find(g => g.name === 'IM').items.map(i => i.profile.id), ['p2', 'p3']);
  assert.ok(!names(groups).includes('Ghost'), 'meta for a deleted connection is not a ghost group');
  assert.ok(!ids(groups).includes('ghost'));
  assert.deepEqual(ids(groups).sort(), profiles.map(p => p.id).sort(), 'each profile appears exactly once');
  const item = groups.find(g => g.name === '订单').items[0];
  assert.deepEqual(item, { profile: profiles[0], group: '订单', environment: 'prod' });
  assert.deepEqual(groups.at(-1).items[0], { profile: profiles[3], group: '', environment: '' });
});

test('"IM" and "im" are distinct groups', () => {
  const groups = groupConnections(profiles, meta);
  assert.deepEqual(groups.find(g => g.name === 'IM').items.map(i => i.profile.id), ['p2', 'p3']);
  assert.deepEqual(groups.find(g => g.name === 'im').items.map(i => i.profile.id), ['p6']);
});

test('zh collation sorts Chinese group names by pinyin and ungrouped is still last', () => {
  const list = [{ id: 'a', name: 'a' }, { id: 'b', name: 'b' }, { id: 'c', name: 'c' }, { id: 'd', name: 'd' }];
  const groups = groupConnections(list, { a: { group: '支付' }, b: { group: '订单' }, d: { group: '账户' } }, { locale: 'zh' });
  assert.deepEqual(names(groups), ['订单', '账户', '支付', '']);
});

test('missing or malformed meta yields a single ungrouped list', () => {
  for (const m of [undefined, null, {}, 'x']) {
    const groups = groupConnections(profiles, m);
    assert.deepEqual(names(groups), ['']);
    assert.deepEqual(groups[0].items.map(i => i.profile.id), profiles.map(p => p.id));
  }
  assert.deepEqual(groupConnections([], meta), []);
  assert.deepEqual(groupConnections(undefined, meta), []);
  const odd = groupConnections([{ id: 'x', name: 'x' }], { x: { group: 7, environment: 'qa' } });
  assert.deepEqual(odd[0].items[0], { profile: { id: 'x', name: 'x' }, group: '', environment: '' });
});

test('searching "生产", "prod" or "Production" keeps only the production connection', () => {
  for (const query of ['生产', 'prod', 'PROD', 'Production', ' production ']) {
    for (const locale of ['zh', 'en']) {
      assert.deepEqual(ids(groupConnections(profiles, meta, { query, locale })), ['p1'], `${query}/${locale}`);
    }
  }
  assert.deepEqual(ids(groupConnections(profiles, meta, { query: '预发' })), ['p5']);
  assert.deepEqual(ids(groupConnections(profiles, meta, { query: 'staging' })), ['p5']);
  assert.deepEqual(ids(groupConnections(profiles, meta, { query: '开发' })), ['p2']);
  assert.deepEqual(ids(groupConnections(profiles, meta, { query: 'Development' })), ['p2']);
});

test('a matching group name keeps the whole group', () => {
  const groups = groupConnections(profiles, meta, { query: 'IM' });
  // Case-insensitive search: both "IM" and "im" groups match, and each is kept whole.
  assert.deepEqual(names(groups), ['IM', 'im'].sort((a, b) => a.localeCompare(b, 'zh-Hans-CN')));
  assert.deepEqual(groups.find(g => g.name === 'IM').items.map(i => i.profile.id), ['p2', 'p3']);
  const exact = groupConnections(profiles, meta, { query: '订单' });
  assert.deepEqual(names(exact), ['订单']);
  assert.deepEqual(ids(exact), ['p1']);
});

test('search matches connection names and the detail callback; empty groups are dropped', () => {
  assert.deepEqual(ids(groupConnections(profiles, meta, { query: 'backup' })), ['p3']);
  assert.deepEqual(names(groupConnections(profiles, meta, { query: 'backup' })), ['IM']);
  assert.deepEqual(ids(groupConnections(profiles, meta, { query: 'localhost' })), [], 'detail is not searched without a callback');
  const detail = p => p.host;
  assert.deepEqual(ids(groupConnections(profiles, meta, { query: 'LOCALHOST', detail })), ['p4']);
  assert.deepEqual(names(groupConnections(profiles, meta, { query: 'localhost', detail })), ['']);
  assert.deepEqual(groupConnections(profiles, meta, { query: 'nothing-matches' }), []);
  assert.equal(ids(groupConnections(profiles, meta, { query: '   ' })).length, profiles.length, 'blank query keeps everything');
  assert.deepEqual(ids(groupConnections(profiles, meta, { query: 'x', detail: () => undefined })), []);
});

test('temporary connections (empty id) are never placed in a group', () => {
  const list = [{ id: '', name: 'temp prod' }, ...profiles];
  const withTemp = { ...meta, '': { group: 'IM', environment: 'prod' } };
  const groups = groupConnections(list, withTemp);
  assert.ok(groups.every(g => g.items.every(i => i.profile.id !== '')));
  assert.deepEqual(ids(groupConnections(list, withTemp, { query: 'temp' })), []);
  assert.deepEqual(groupConnections([{ id: '', name: 'temp' }], {}), []);
});

test('groupNames returns unique sorted non-empty names', () => {
  assert.deepEqual(groupNames(meta), ['Billing', 'Ghost', 'IM', 'im', '订单'].sort((a, b) => a.localeCompare(b, 'zh-Hans-CN')));
  assert.deepEqual(groupNames({ a: { group: ' 支付 ' }, b: { group: '订单' }, c: { group: '' }, d: {}, e: null }), ['订单', '支付']);
  assert.deepEqual(groupNames(undefined), []);
});

test('withMeta adds only non-empty group/environment to exported items without mutating them', () => {
  const exported = [{ id: 'p1', name: 'order-db' }, { id: 'p6', name: 'lower-case' }, { id: 'p4', name: 'scratch' }, { name: 'no-id' }];
  const snapshot = structuredClone(exported);
  assert.deepEqual(withMeta(exported, meta), [
    { id: 'p1', name: 'order-db', group: '订单', environment: 'prod' },
    { id: 'p6', name: 'lower-case', group: 'im' },
    { id: 'p4', name: 'scratch' },
    { name: 'no-id' },
  ]);
  assert.deepEqual(exported, snapshot);
  assert.deepEqual(withMeta(exported, undefined), snapshot);
});

test('metaFromImport leaves old-format exports untouched', () => {
  const items = [{ name: 'a', host: 'h1' }, { name: 'b', host: 'h2' }];
  const result = metaFromImport(items);
  assert.deepEqual(result.pending, []);
  assert.deepEqual(result.items, items);
  result.items.forEach((item, i) => assert.equal(item, items[i]));
});

test('metaFromImport strips group/environment and keeps valid values as pending', () => {
  const items = [
    { name: 'a', host: 'h1', group: ' IM ', environment: 'prod' },
    { name: 'b', host: 'h2', group: '', environment: 'dev' },
    { name: 'c', host: 'h3', group: 'x'.repeat(41), environment: 'qa' },
    { name: 'd', host: 'h4', group: 'line\nbreak' },
    { name: 'e', host: 'h5' },
    { name: 'f', group: 'G', environment: 'staging' },
  ];
  const snapshot = structuredClone(items);
  const { pending, items: cleaned } = metaFromImport(items);
  assert.deepEqual(pending, [
    { name: 'a', group: 'IM', environment: 'prod' },
    { name: 'b', group: '', environment: 'dev' },
    { name: 'f', group: 'G', environment: 'staging' },
  ]);
  assert.deepEqual(cleaned, [
    { name: 'a', host: 'h1' }, { name: 'b', host: 'h2' }, { name: 'c', host: 'h3' }, { name: 'd', host: 'h4' }, { name: 'e', host: 'h5' }, { name: 'f' },
  ]);
  assert.deepEqual(items, snapshot, 'input is not mutated');
  assert.deepEqual(metaFromImport(undefined), { pending: [], items: undefined });
});

test('metaAssignments maps pending meta by name and skips names that do not match', () => {
  const pending = [
    { name: 'a', group: 'IM', environment: 'prod' },
    { name: 'skipped-duplicate', group: 'G', environment: 'dev' },
    { name: ' b ', group: '', environment: 'test' },
  ];
  const created = [{ id: 'n1', name: 'a' }, { id: 'n2', name: 'b' }, { id: 'n3', name: 'other' }];
  assert.deepEqual(metaAssignments(pending, created), [
    { id: 'n1', group: 'IM', environment: 'prod' },
    { id: 'n2', group: '', environment: 'test' },
  ]);
  assert.deepEqual(metaAssignments([], created), []);
  assert.deepEqual(metaAssignments(pending, []), []);
  assert.deepEqual(metaAssignments(undefined, undefined), []);
});

test('metaAssignments never guesses when a name is ambiguous', () => {
  const pending = [{ name: 'dup', group: 'A' }, { name: 'dup', group: 'B' }, { name: 'x', group: 'X' }];
  assert.deepEqual(metaAssignments(pending, [{ id: '1', name: 'dup' }, { id: '2', name: 'x' }, { id: '3', name: 'x' }]), []);
  assert.deepEqual(metaAssignments([{ name: 't', group: 'T' }], [{ id: '', name: 't' }]), [], 'temporary profiles get nothing');
  assert.deepEqual(metaAssignments([{ name: 'A', group: 'T' }], [{ id: '1', name: 'a' }]), [], 'names match exactly');
});

const newKeys = ['connSearch', 'connNew', 'connUngrouped', 'connTemporary', 'connSecurity', 'connMore', 'connConnect', 'connDisconnect',
  'connEdit', 'connDuplicate', 'connMoveTo', 'connDelete', 'groupRename', 'groupDelete', 'groupNameLabel', 'groupNamePlaceholder',
  'envLabel', 'envNone', 'env_dev', 'env_test', 'env_staging', 'env_prod', 'connEmptyTitle', 'connRecent', 'connCollapse', 'connExpand'];

test('zh/en shell messages contain every connection key with matching placeholders', () => {
  const placeholders = text => [...text.matchAll(/\{(\w+)\}/g)].map(m => m[1]).sort();
  for (const key of newKeys) {
    for (const locale of ['zh', 'en']) assert.ok(messages[locale][key]?.trim(), `${locale}.${key}`);
    assert.deepEqual(placeholders(messages.zh[key]), placeholders(messages.en[key]), key);
  }
  assert.deepEqual(placeholders(messages.zh.connEmptyTitle), ['name']);
  assert.deepEqual(['dev', 'test', 'staging', 'prod'].map(e => messages.zh[`env_${e}`]), ['开发', '测试', '预发', '生产']);
  assert.deepEqual(['dev', 'test', 'staging', 'prod'].map(e => messages.en[`env_${e}`]), ['Development', 'Test', 'Staging', 'Production']);
  assert.equal(messages.zh.connUngrouped, '未分组');
  assert.equal(messages.zh.connTemporary, '临时连接');
  assert.equal(messages.zh.groupNameLabel, '项目分组');
  assert.equal(messages.zh.envNone, '不标');
});

test('the helper module is self-contained', async () => {
  const { readFile } = await import('node:fs/promises');
  const source = await readFile(new URL('../src/plugin-ui/connection-groups.mjs', import.meta.url), 'utf8');
  for (const [, spec] of source.matchAll(/from\s+['"]([^'"]+)['"]/g)) assert.match(spec, /^\.\/[\w-]+\.mjs$/, spec);
  assert.doesNotMatch(source, /localStorage|sessionStorage/);
});
