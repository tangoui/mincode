import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm, access } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { installEditorHelpers, renderPageFile } from './knowledge-browser-test-utils.mjs';


const chrome = process.env.CHROME_BIN || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const available = await access(chrome).then(() => true, () => false);
const ui = new URL('../../official-plugins/knowledge-base/ui/', import.meta.url);

// The layout page reports into <pre id="layout-result">; read it in real time.
const renderLayoutPage = (path, width) => renderPageFile(path, { width, resultSelector: '#layout-result' });

test('knowledge built UI preserves button layout, selection and bounded navigation', { skip: !available }, async () => {
  const temp = await mkdtemp(join(tmpdir(), 'mincode-kb-layout-'));
  try {
    const css = await readFile(new URL('style.css', ui), 'utf8');
    for (const width of [1440, 900, 600]) {
      for (const theme of ['light', 'dark']) {
        const html = `<!doctype html><html><head><meta charset="utf-8"><style>${css}
*, *::before, *::after { transition: none !important; animation: none !important; }</style></head><body><div id="app"></div><script>
(${installEditorHelpers.toString()})();
const report = message => { if (document.getElementById('layout-result')) return; const result = document.createElement('pre'); result.id = 'layout-result'; result.textContent = message; document.body.append(result); };
addEventListener('error', event => report('page error: ' + event.message));
addEventListener('unhandledrejection', event => report('unhandled rejection: ' + (event.reason?.stack || event.reason)));
location.hash = 'mincodeSession=test&nonce=test&generation=1';
const actionCalls = [];
const doc = { id: 'one', title: '很长的知识库笔记标题'.repeat(8), excerpt: '摘要内容'.repeat(30), updated: '2026-09-10', folder: '很长的目录'.repeat(15), tags: '标签一,标签二' };
addEventListener('message', event => {
  if (event.data?.type !== 'mincode.plugin.ready') return;
  const channel = new MessageChannel();
  channel.port1.onmessage = ({ data }) => {
    actionCalls.push({ action: data.action, payload: data.payload });
    const result = data.action === 'libraries' ? [{ id: 'default', title: '很长的知识库名称'.repeat(12), count: 8 }] : data.action === 'categories' ? [{ name: doc.folder, count: 8 }] : data.action === 'stats' ? { documents: 8, favorites: 1 } : data.payload.id ? { ...doc, content: '正文' } : Array.from({ length: 8 }, (_, i) => ({ ...doc, id: i ? 'doc' + i : 'one' }));
    channel.port1.postMessage({ type: 'mincode.host.response', id: data.id, ok: true, result });
  };
  postMessage({ type: 'mincode.host.connect', protocolVersion: 1, sessionId: 'test', nonce: 'test', generation: 1, context: { theme: '${theme}' } }, '*', [channel.port2]);
});
setTimeout(async () => {
  const pause = () => new Promise(resolve => setTimeout(resolve, 180));
  const failures = [];
  const check = (ok, message) => { if (!ok) failures.push(message); };
  const rect = element => element.getBoundingClientRect();
  const nav = [...document.querySelectorAll('.kb-nav-item')];
  check(nav.length >= 4, 'navigation loaded');
  check(Boolean(document.querySelector('[data-testid="knowledge-switcher"]')), 'top library switcher');
  check(Boolean(document.querySelector('[data-testid="workspace-search"]')), 'top search');
  check(Boolean(document.querySelector('[data-testid="new-document"]')), 'navigation new document');
  check(!document.querySelector('[data-testid="new-document"]').textContent.includes('＋'), 'new button has a single icon, not a duplicated plus');
  check(Boolean(document.querySelector('[data-testid="workspace-status"]')), 'truthful local status');
  if (innerWidth <= 700) {
    const toggle = document.querySelector('[data-testid="toggle-navigation"]');
    check(Boolean(toggle) && rect(toggle).width > 0, 'narrow navigation toggle visible');
  }
  check(nav.every(item => Math.abs(rect(item).left - rect(nav[0]).left) < 1), 'navigation aligned');
  check(nav.every(item => getComputedStyle(item).marginLeft === '0px'), 'no inherited button margin');
  check(getComputedStyle(nav[0]).backgroundColor !== 'rgba(0, 0, 0, 0)', 'active navigation background');
  document.querySelectorAll('.kb-nav .el-scrollbar__wrap,.kb-list .el-scrollbar__wrap').forEach(item => check(item.scrollWidth <= item.clientWidth, 'no horizontal overflow'));
  const card = document.querySelector('.kb-document');
  check(getComputedStyle(card.firstElementChild).display === 'block', 'document wrapper block');
  check(rect(card.querySelector('p')).top >= rect(card.querySelector('.kb-document-heading')).bottom, 'excerpt below title');
  check(rect(card.querySelector('small')).top >= rect(card.querySelector('p')).bottom, 'date below excerpt');
  const actions = document.querySelector('.kb-header-actions');
  check(rect(actions).width > 0, 'import and AI actions visible');
  if (innerWidth <= 700) { document.querySelector('[data-testid="toggle-list"]').click(); await pause(); }
  check(rect(card).width > 0 && rect(card).height > 0, 'document list reachable');
  card.click();
  setTimeout(async () => {
    check(card.classList.contains('selected'), 'document selection works');
    check(getComputedStyle(card).backgroundColor !== 'rgba(0, 0, 0, 0)', 'document selection background');
    const wait = () => new Promise(resolve => setTimeout(resolve, 350));
    const button = label => [...document.querySelectorAll('button')].find(item => item.textContent.trim() === label && item.getClientRects().length && getComputedStyle(item).visibility !== 'hidden');
    // The full draft workflow runs in the widest light and narrowest dark layouts; the
    // other combinations stop after layout and selection checks.
    if (!${(width === 1440 && theme === 'light') || (width === 600 && theme === 'dark')}) { report(failures.length ? failures.join('; ') : 'PASS'); return; }
    const discard = () => { const item = button('放弃修改'); check(Boolean(item) && Boolean(item.closest('.el-dialog')), 'unsaved changes dialog offers discard'); item?.click(); };
    const mode = value => document.querySelector('[data-testid="mode-' + value + '"]');
    check(Boolean(mode('split')), 'explicit split mode exists');
    if (!mode('split')) { report(failures.join('; ')); return; }
    mode('edit').click(); await wait();
    check(!document.querySelector('.kb-preview'), 'edit mode hides preview');
    await window.kbEditor.waitReady();
    window.kbEditor.set('未保存正文 ## draft'); await wait();
    mode('preview').click(); await wait();
    check(!document.querySelector('.kb-writing'), 'preview mode hides writing');
    check(document.querySelector('.kb-preview').textContent.includes('未保存正文 ## draft'), 'preview uses unsaved text');
    mode('split').click(); await wait();
    await window.kbEditor.waitReady(); check(window.kbEditor.value() === '未保存正文 ## draft', 'mode switch preserves draft');
    check(document.querySelector('.kb-save-status').textContent.includes('未保存'), 'mode switch preserves dirty');
    check(!actionCalls.some(item => item.action === 'save'), 'mode switch never saves');
    const propsBefore = window.kbEditor.value();
    button('文档属性').click(); await wait();
    button('文档属性').click(); await wait();
    check(window.kbEditor.value() === propsBefore, 'properties toggle preserves draft');
    button('取消编辑').click(); await wait(); discard(); await wait();

    if (innerWidth <= 700 && !document.querySelector('.kb').classList.contains('navigation-open')) { document.querySelector('[data-testid="toggle-navigation"]').click(); await wait(); }
    button('新建笔记').click();
    await wait();
    check(!nav[0].disabled && !card.disabled, 'draft does not disable navigation');
    if (!document.querySelector('.kb-properties')) { button('文档属性').click(); await wait(); }
    check(document.querySelectorAll('.kb-properties .el-select').length === 3, 'library, single category and multiple tags selectors rendered');
    check(Boolean(button('取消编辑')) && !button('取消编辑').disabled, 'cancel edit available');
    button('取消编辑').click();
    await wait();
    button('继续编辑').click();
    await wait();
    check(Boolean(document.querySelector('.kb-title-input')), 'cancel confirmation preserves draft');
    check(!nav[0].disabled, 'busy cleared after continue editing');
    if (document.querySelector('.kb-properties')) { document.querySelector('.kb-properties-header button').click(); await wait(); }
    if (innerWidth <= 700 && !document.querySelector('.kb').classList.contains('navigation-open')) { document.querySelector('[data-testid="toggle-navigation"]').click(); await wait(); }
    nav[0].click();
    await wait();
    discard();
    await wait();
    check(!document.querySelector('.kb-title-input'), 'confirmed navigation leaves draft');
    if (innerWidth <= 700 && !document.querySelector('.kb').classList.contains('navigation-open')) { document.querySelector('[data-testid="toggle-navigation"]').click(); await wait(); }
    button('新建笔记').click();
    await wait();
    button('取消编辑').click();
    await wait();
    discard();
    await wait();
    check(!document.querySelector('.kb-title-input'), 'cancel new draft clears editor');
    check(!nav[0].disabled, 'navigation unlocked after cancellation');
    report(failures.length ? failures.join('; ') : 'PASS');
  }, 300);
}, 1200);
</script><script type="module" src="${new URL('app.js', ui).href}"></script></body></html>`;
        const path = join(temp, `test-${width}-${theme}.html`);
        await writeFile(path, html);
        const result = await renderLayoutPage(path, width);
        assert.equal(result, 'PASS', `${width}/${theme}: ${result}`);
      }
    }
  } finally { await rm(temp, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 }); }
});
