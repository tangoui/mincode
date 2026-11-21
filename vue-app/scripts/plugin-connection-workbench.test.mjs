import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const dir = new URL('../src/plugin-ui/', import.meta.url);
const read = name => readFile(new URL(name, dir), 'utf8');
const shell = await read('PluginConnectionWorkbench.vue').catch(() => '');
const fields = await read('PluginConnectionMetaFields.vue').catch(() => '');
const sync = await readFile(new URL('../../tools/plugin-sdk/sync-plugin-ui.mjs', import.meta.url), 'utf8');
const allowed = new Set(['vue', 'element-plus', '@element-plus/icons-vue', './connection-groups.mjs', './messages.mjs', './PluginConnectionMetaFields.vue']);
const script = source => source.match(/<script[^>]*>([\s\S]*?)<\/script>/)?.[1] ?? '';
const template = source => source.match(/<template>([\s\S]*)<\/template>/)?.[1] ?? '';

test('both components exist and import only vue, Element Plus and sibling shell helpers', () => {
  for (const [name, source] of [['PluginConnectionWorkbench.vue', shell], ['PluginConnectionMetaFields.vue', fields]]) {
    assert.ok(source, `${name} is missing`);
    const imports = [...source.matchAll(/(?:^|\n)\s*import\s+(?:type\s+)?(?:[^'"]+\s+from\s+)?['"]([^'"]+)['"]/g)].map(match => match[1]);
    assert.ok(imports.length, name);
    for (const path of imports) assert.ok(allowed.has(path), `${name}: ${path}`);
    assert.doesNotMatch(source, /localStorage|sessionStorage|navigator\.clipboard|innerHTML|v-html|eval\(|new Function|window\.open|fetch\(/, name);
    assert.doesNotMatch(template(source), /<(?:button|input|select|textarea|table|dialog|iframe|img|a)\b/i, name);
  }
  assert.doesNotMatch(fields, /PluginConnectionWorkbench/);
});

test('the shell declares the contract props, emits and slots', () => {
  const code = script(shell);
  for (const prop of ['title', 'subtitle', 'logo', 'listTitle', 'profiles', 'meta', 'prefs', 'activeId', 'showTemporary', 'connected', 'ready',
    'connectable', 'detail', 'statusTitle', 'busy', 'readOnly', 'locale', 'securityText']) {
    assert.match(code, new RegExp(`\\b${prop}\\??:`), `prop ${prop}`);
  }
  for (const event of ['select', 'connect', 'disconnect', 'new', 'edit', 'duplicate', 'delete', 'set-meta', 'rename-group', 'toggle-read-only', 'set-prefs']) {
    assert.match(code, new RegExp(`['"]?${event}['"]?: \\[`), `emit ${event}`);
  }
  for (const slot of ['tabs', 'top-actions', 'more', 'sidebar-footer', 'empty']) assert.match(shell, new RegExp(`<slot name="${slot}"`), `slot ${slot}`);
  assert.match(template(shell), /<slot(?: v-else)? \/>/, 'default slot');
});

test('the shell keeps the agreed interaction primitives and never stores anything itself', () => {
  assert.match(shell, /trigger="contextmenu"/);
  assert.match(shell, /@media \(max-width: 720px\)/);
  assert.match(shell, /ElDrawer/);
  assert.match(shell, /direction(?:="|: ')ltr/);
  assert.match(shell, /emit\('set-prefs'/);
  assert.match(shell, /groupConnections\(/);
  assert.match(shell, /environmentTagType\(/);
  assert.match(shell, /180/);
  assert.match(shell, /360/);
  assert.match(shell, /240/);
});

test('connect and disconnect buttons only render for connectable plugins', () => {
  const topbar = template(shell).match(/<header class="plugin-connection-workbench__topbar">[\s\S]*?<\/header>/)?.[0] ?? '';
  const buttons = [...topbar.matchAll(/<ElButton\b[^>]*@click="emit\('(connect|disconnect)'\)"[^>]*>/g)];
  assert.deepEqual([...new Set(buttons.map(match => match[1]))].sort(), ['connect', 'disconnect']);
  for (const [tag] of buttons) assert.match(tag, /v-(?:else-)?if="connectable/, tag);
  assert.match(template(shell), /v-if="connectable && !connected/, 'the not-connected card is gated on connectable');
});

test('the read-only lock is hidden when the plugin has no read-only mode', () => {
  assert.match(template(shell), /v-if="readOnly !== null && readOnly !== undefined"/);
  assert.match(shell, /emit\('toggle-read-only', !readOnly\)/);
});

test('meta fields edit group and environment with Element Plus controls', () => {
  assert.match(fields, /<ElSelect[^>]*filterable[^>]*allow-create/);
  assert.match(fields, /<ElRadioGroup/);
  assert.match(fields, /envNone/);
  assert.match(fields, /ENVIRONMENTS/);
  assert.match(fields, /'update:modelValue'/);
  for (const prop of ['modelValue', 'groups', 'locale', 'disabled']) assert.match(script(fields), new RegExp(`\\b${prop}\\??:`), prop);
});

test('the group field treats ungrouped as empty and normalises a cleared value to an empty string', () => {
  // Element Plus already counts '' as empty; overriding empty-values/value-on-clear hides the placeholder for ungrouped.
  assert.doesNotMatch(template(fields), /empty-values|value-on-clear/);
  assert.match(template(fields), /@update:model-value="update\(\{ group: cleanGroup\(\$event\) \}\)"/);
  const line = script(fields).match(/const cleanGroup = \(value: unknown\) => (.+);\n/)?.[1];
  assert.ok(line, 'cleanGroup');
  const cleanGroup = new Function('MAX_GROUP_RUNES', `return value => ${line};`)(40);
  for (const cleared of [undefined, null, '', '   ']) assert.equal(cleanGroup(cleared), '', String(cleared));
  assert.equal(cleanGroup('  IM 项目 '), 'IM 项目');
  assert.equal(cleanGroup('a\nb'), 'a b');
  assert.equal([...cleanGroup('组'.repeat(50))].length, 40);
});

test('the third-party template receives the shell files', () => {
  for (const name of ['PluginConnectionWorkbench.vue', 'PluginConnectionMetaFields.vue', 'connection-groups.mjs']) {
    assert.match(sync.match(/const FULL_REQUIRED = \[[^;]*\];/)?.[0] ?? '', new RegExp(`'${name.replace('.', '\\.')}'`), name);
  }
});

test('both components compile without unresolved template bindings', async () => {
  const compiler = await import(new URL('../node_modules/vue/compiler-sfc/index.mjs', import.meta.url).href);
  for (const [name, source] of [['PluginConnectionWorkbench.vue', shell], ['PluginConnectionMetaFields.vue', fields]]) {
    const { descriptor, errors } = compiler.parse(source, { filename: name });
    assert.deepEqual(errors, [], name);
    const compiled = compiler.compileScript(descriptor, { id: name, inlineTemplate: false });
    const result = compiler.compileTemplate({ source: descriptor.template.content, filename: name, id: name, compilerOptions: { bindingMetadata: compiled.bindings ?? {} } });
    assert.deepEqual(result.errors, [], name);
    const unresolved = [...new Set([...result.code.matchAll(/_ctx\.(\w+)/g)].map(match => match[1]))].filter(key => !['$slots', '$attrs', '$emit'].includes(key));
    assert.deepEqual(unresolved, [], name);
  }
});

test('the connection list is one ElTree with groups as parents and connections as leaves', () => {
  const view = template(shell);
  assert.match(script(shell), /import \{[^}]*\bElTree\b[^}]*\} from 'element-plus'/);
  const trees = [...view.matchAll(/<ElTree\b[^>]*>/g)].map(match => match[0]);
  assert.equal(trees.length, 1, 'exactly one ElTree');
  const [tree] = trees;
  for (const attr of ['node-key=', ':data=', 'highlight-current', ':current-node-key=', ':default-expanded-keys=', ':filter-node-method=',
    '@node-click=', '@node-expand=', '@node-collapse=', '@node-contextmenu=']) assert.ok(tree.includes(attr), attr);
  const slot = view.slice(view.indexOf('<ElTree'), view.indexOf('</ElTree>'));
  assert.match(slot, /<template #default="\{[^}]*\bdata\b[^}]*\}">/);
  assert.match(slot, /plugin-connection-workbench__dot/);
  assert.match(slot, /<ElTag\b[^>]*:type="environmentTagType\(/);
  assert.match(slot, /describe\(/);
  assert.match(slot, /connUngrouped/);
  // The hand-built rows, carets and their keyboard handlers are gone.
  assert.doesNotMatch(shell, /__row\b|__row-|__group-head|__caret|ArrowDown|ArrowRight/);
  assert.doesNotMatch(view, /role="button"/);
});

test('expansion, selection and search go through the tree but keep the prefs contract', () => {
  const code = script(shell);
  // Expanding and collapsing a group maps onto prefs.collapsed exactly like the old toggle.
  assert.match(code, /setPrefs\(\{ collapsed: \[\.\.\.next\] \}\)/);
  for (const handler of ['onNodeExpand', 'onNodeCollapse', 'onNodeClick', 'onNodeContextmenu', 'filterNode']) assert.match(code, new RegExp(`function ${handler}\\(`), handler);
  // Leaves select; groups only toggle.
  const click = code.slice(code.indexOf('function onNodeClick('), code.indexOf('\n}\n', code.indexOf('function onNodeClick(')));
  assert.match(click, /choose\(/);
  assert.match(click, /kind [!=]== 'group'/);
  // Filtering reuses groupConnections so matching stays identical (name, detail, group, environment names).
  const filter = code.slice(code.indexOf('function filterNode('), code.indexOf('\n}\n', code.indexOf('function filterNode(')));
  assert.match(filter, /visibleIds/);
  assert.match(code, /const visibleIds = computed\(\(\) => new Set\(groups\.value\.flatMap/);
  assert.match(code, /\.filter\(query\.value\)/);
});

test('one shared context menu opens at the pointer through virtual triggering', () => {
  const view = template(shell);
  const menus = [...view.matchAll(/<ElDropdown\b[^>]*trigger="contextmenu"[^>]*>/g)].map(match => match[0]);
  assert.equal(menus.length, 1, 'a single context-menu dropdown');
  assert.match(menus[0], /virtual-triggering/);
  assert.match(menus[0], /:virtual-ref="menuAnchor"/);
  assert.match(script(shell), /getBoundingClientRect: \(\) => menuRect\.value/);
  assert.match(script(shell), /handleOpen\(\)/);
  const menu = view.slice(view.indexOf(menus[0]), view.indexOf('</ElDropdown>', view.indexOf(menus[0])));
  for (const command of ['connect', 'disconnect', 'edit', 'duplicate', 'move', 'delete', 'rename', 'delete-group']) assert.match(menu, new RegExp(`command="${command}"`), command);
  // The temporary connection and the ungrouped bucket have no context menu.
  const open = script(shell).slice(script(shell).indexOf('function onNodeContextmenu('));
  assert.match(open.slice(0, open.indexOf('\n}\n')), /kind === 'temporary'/);
});

test('the not-connected state explains the plugin and leads to the right next step', async () => {
 const empty = template(shell).match(/<div v-if="connectable && !connected" class="plugin-connection-workbench__empty">([\s\S]*?)<slot name="empty" \/>/)?.[1] ?? '';
 assert.ok(empty, 'not-connected block');
 // An icon and a state title instead of only repeating the plugin name; the subtitle says what it does.
 assert.match(empty, /<ElIcon[^>]*class="plugin-connection-workbench__empty-icon"/);
 assert.match(empty, /copy\.connEmptyNoneTitle|emptyTitle/);
 assert.match(empty, /v-if="subtitle"[^>]*class="plugin-connection-workbench__empty-text"/);
 // With no saved connection the primary action creates one; otherwise it connects and lists recents.
 assert.match(script(shell), /const hasSaved = computed\(\(\) => props\.profiles\.length > 0\)/);
 assert.match(empty, /v-if="!hasSaved"[^>]*type="primary"[\s\S]*?@click="emit\('new'\)"/);
 assert.match(empty, /v-else[^>]*type="primary"[\s\S]*?@click="emit\('connect'\)"/);
 assert.doesNotMatch(shell, /\.plugin-connection-workbench__empty \{[^}]*border: 1px/);
 const { messages } = await import('../src/plugin-ui/messages.mjs');
 for (const key of ['connEmptyNoneTitle', 'connEmptyNoneHint', 'connEmptyHint']) {
  assert.ok(messages.zh[key] && messages.en[key], key);
 }
});

test('the environment radio group leaves room for Element Plus outline borders', () => {
 // Radio-button borders are outlines drawn outside the box; without the inset a scrolling
 // dialog body clips the left edge of the first (selected) option.
 assert.match(fields, /\.plugin-connection-meta__environment :deep\(\.el-radio-group\) \{[^}]*padding: 1px/);
});
