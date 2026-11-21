import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { parseSshHistoryData } from '../src/domain/ssh/command-history.ts';
const service = await readFile(new URL('../src/services/ssh-command-history-service.ts', import.meta.url), 'utf8');
const domain = await readFile(new URL('../src/domain/ssh/command-history.ts', import.meta.url), 'utf8');
const terminal = await readFile(new URL('../src/components/terminal/SshTerminal.vue', import.meta.url), 'utf8');
const panel = await readFile(new URL('../src/components/ssh/SshCommandHistory.vue', import.meta.url), 'utf8');
test('history fill uses paste without execution and rejects controls', () => {
 const fill = terminal.slice(terminal.indexOf('function fillHistoryCommand'), terminal.indexOf('\n}', terminal.indexOf('function fillHistoryCommand')) + 2);
 assert.match(fill, /status.value !== "ready"/);
 assert.match(fill, /\\p\{Cc\}/);
 assert.match(fill, /predictiveEcho\.paste\(command\)/);
 assert.doesNotMatch(fill, /writeShellInput|\\r|\\n/);
});
test('history lists saved entries on mount and confirms clearing', () => {
 assert.match(panel, /onMounted\(\(\) => \{ void call\(\{ action: "list" \}\)/);
 assert.match(panel, /ElMessageBox.confirm\(t\("ssh.history.clearConfirm"\)/);
 assert.doesNotMatch(panel + service + domain, /action: "import"|ssh_history_import|ssh_history_start|startLive|liveError|\blive\b|setInterval|clearInterval|refreshTimer|action: "start"/);
 assert.doesNotMatch(panel, /invokeBackend|writeShellInput/);
 assert.match(service, /ssh_history_\$\{action\}/);
 assert.match(panel, /profileId: props.profile.id/);
 assert.match(panel, /username: props.profile.username/);
 assert.match(terminal, /<SshCommandHistory/);
});

test('save toggle persists through the update request and guards busy/offline', () => {
 assert.match(panel, /async function toggleSave\(value: boolean\)/);
 assert.match(panel, /await call\(\{ action: "update", operation: "setEnabled", enabled: Boolean\(value\) \}\)/);
 assert.doesNotMatch(panel.slice(panel.indexOf('<template>')), /setEnabled/);
 assert.match(panel, /if \(busy.value \|\| !desktop\) return false/);
 assert.match(panel, /:model-value="enabled"/);
 assert.match(panel, /@change="toggleSave"/);
});

test('saved entries and disabled preference remain compatible without transient fields', () => {
 const entry = { id: 'existing-id', command: 'ls -la', favorite: true, importedAt: '2026-01-01T00:00:00Z' };
 const saved = { entries: [entry], enabled: false };
 assert.deepEqual(parseSshHistoryData({ ...saved, live: true, liveError: 'obsolete' }), saved);
 assert.deepEqual(parseSshHistoryData({ entries: [], enabled: true }), { entries: [], enabled: true });
 for (const value of [null, {}, { entries: [], enabled: 'false' }, { entries: [{ ...entry, command: 3 }], enabled: true }]) {
  assert.throws(() => parseSshHistoryData(value), /Invalid SSH history/);
 }
});

test('both locales explain local-only capture without remote reads or hooks', async () => {
 for (const locale of ['en', 'zh']) {
  const source = await readFile(new URL(`../src/i18n/locales/${locale}.ts`, import.meta.url), 'utf8');
  const help = source.split('\n').find(line => line.includes('"ssh.history.help"'));
  assert.match(help, locale === 'en' ? /saved locally automatically/ : /自动保存到本机/);
  assert.match(help, locale === 'en' ? /no remote files are read/ : /不读取远端文件/);
  assert.match(help, locale === 'en' ? /no recording hooks or scripts are injected/ : /不注入记录钩子或脚本/);
  assert.match(help, locale === 'en' ? /5,000 entries are retained/ : /最多保留 5,000 条/);
 }
});

test('history toolbar uses an accessible icon within the compact button width', () => {
 const start = terminal.indexOf('<el-tooltip :content="t(\'ssh.history.button\')"');
 assert.ok(start >= 0);
 const button = terminal.slice(start, terminal.indexOf('</el-tooltip>', start));
 assert.match(button, /:aria-label="t\('ssh\.history\.button'\)"/);
 assert.match(button, /native-type="button"/);
 assert.match(button, /:aria-expanded="commandHistoryOpen"/);
 assert.match(button, /@click.stop="commandHistoryOpen = true"/);
 assert.match(button, /<el-icon><Clock \/><\/el-icon>/);
 assert.doesNotMatch(button, />\{\{ t\('ssh\.history\.button'\) \}\}<\/el-button>/);
 assert.match(terminal, /<el-drawer v-model="commandHistoryOpen" :title="t\('ssh\.history\.title'\)"/);
 assert.doesNotMatch(terminal, /历史命令/);
});
