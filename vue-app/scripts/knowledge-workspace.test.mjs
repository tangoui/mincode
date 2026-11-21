import test from 'node:test';
import assert from 'node:assert/strict';
import { runKnowledgeBrowser, browserAvailable } from './knowledge-browser-test-utils.mjs';

// Removing an action binding, loosening its dirty/read-only guard, or misrouting
// the payload must fail these real built-page interactions (host I/O is a fixture).
test('knowledge document actions retain save favorite export history and trash flows', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(r => setTimeout(r, 360));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    const button = label => { const e = [...document.querySelectorAll('button')].find(e => e.textContent.trim() === label && e.getClientRects().length); if (!e) throw new Error('Missing button '+label+'; visible='+[...document.querySelectorAll('button')].filter(e => e.getClientRects().length).map(e => e.textContent.trim()).join('|')); return e; };
    const calls = action => window.kbHost.calls.filter(c => c.action === action);
    const input = (selector, value) => { const e = [...document.querySelectorAll(selector)].find(e => e.getClientRects().length); e.value = value; e.dispatchEvent(new Event('input', { bubbles: true })); };
    document.querySelector('.kb-document').click(); await wait();
    await window.kbEditor.waitReady(); window.kbEditor.set('## 本轮修改'); await wait();
    check(button('历史').disabled, 'dirty history disabled');
    button('导出').click(); await wait();
    check(button('保存为文件…').disabled, 'dirty export-file disabled');
    button('复制到剪贴板').click(); await wait();
    check(window.kbHost.calls.some(c => c.type === 'mincode.plugin.clipboard.write-text' && c.text === '## 本轮修改'), 'clipboard gets current unsaved body');
    button('保存').click(); await wait();
    check(calls('save').at(-1).payload.content === '## 本轮修改', 'save edited body');
    check(document.querySelector('.kb-save-status').textContent.includes('已保存'), 'actual saved status');
    document.querySelector('[aria-label="收藏"]').click(); await wait();
    check(window.kbHost.docs[0].favorite, 'favorite action applied');
    button('导出').click(); await wait(); button('保存为文件…').click(); await wait();
    check(calls('exportDocument').at(-1).payload.id === 'one', 'export correct persisted document');
    button('历史').click(); await wait();
    const restore = [...document.querySelectorAll('.el-dialog button')].find(e => e.textContent.trim() === '恢复' && e.getClientRects().length);
    check(Boolean(restore), 'history restore reachable'); restore.click(); await wait();
    check(window.kbEditor.value() === '# 恢复的正文', 'restored history reaches editor');
    check(document.querySelector('.kb-save-status').textContent.includes('已保存'), 'restored history is not marked unsaved');
    button('移入回收站').click(); await wait();
    check(window.kbHost.docs[0].deleted, 'trash correct document');
    const nav = [...document.querySelectorAll('.kb-nav-item')].find(e => e.textContent.includes('回收站')); nav.click(); await wait();
    document.querySelector('.kb-document').click(); await wait();
    check(!document.querySelector('.kb-writing'), 'trash read-only');
    check(document.querySelector('[data-testid="mode-edit"]').disabled, 'trash edit mode disabled');
    button('恢复文档').click(); await wait();
    check(!window.kbHost.docs[0].deleted, 'restore preserves document');
    [...document.querySelectorAll('.kb-nav-item')].find(e => e.textContent.includes('全部文章')).click(); await wait();
    document.querySelector('.kb-document').click(); await wait(); button('移入回收站').click(); await wait();
    nav.click(); await wait(); document.querySelector('.kb-document').click(); await wait();
    button('彻底删除').click(); await wait();
    check(calls('purge').length === 0, 'purge not performed before confirmation');
    [...document.querySelectorAll('.el-dialog button')].find(e => e.textContent.trim() === '彻底删除' && e.getClientRects().length).click(); await wait();
    check(!window.kbHost.docs.some(d => d.id === 'one'), 'confirmed purge correct record');
  });
  assert.equal(result, 'PASS');
});

test('knowledge search tags category library and palette routes remain available', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(r => setTimeout(r, 360));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    const button = label => { const e = [...document.querySelectorAll('button')].find(e => e.textContent.trim() === label && e.getClientRects().length); if (!e) throw new Error('Missing button '+label+'; visible='+[...document.querySelectorAll('button')].filter(e => e.getClientRects().length).map(e => e.textContent.trim()).join('|')); return e; };
    const input = (selector, value) => { const e = [...document.querySelectorAll(selector)].find(e => e.getClientRects().length); e.value = value; e.dispatchEvent(new Event('input', { bubbles: true })); };
    input('[data-testid="workspace-search"] input', '接口'); button('搜索').click(); await wait();
    check(document.querySelectorAll('.kb-document').length === 1 && document.querySelector('.kb-document').textContent.includes('接口说明'), 'search current library');
    input('[data-testid="workspace-search"] input', ''); button('搜索').click(); await wait();
    [...document.querySelectorAll('.kb-tags button')].find(e => e.textContent.includes('产品')).click(); await wait();
    [...document.querySelectorAll('.kb-tags button')].find(e => e.textContent.includes('规划')).click(); await wait();
    const search = window.kbHost.calls.filter(c => c.action === 'list' && c.payload.tags?.length === 2).at(-1);
    check(search.payload.tags.includes('产品') && search.payload.tags.includes('规划'), 'tag intersection forwarded');
    check(document.querySelectorAll('.kb-document').length === 1, 'intersection shows matching document');
    button('清除筛选').click(); await wait();
    document.querySelector('[aria-label="新建分类"]').click(); await wait();
    input('.el-dialog input', '验收分类'); await wait(); button('确定').click(); await wait();
    check([...document.querySelectorAll('.kb-category-row')].some(e => e.textContent.includes('验收分类')), 'new category shown');
    const row = [...document.querySelectorAll('.kb-category-row')].find(e => e.textContent.includes('验收分类'));
    row.querySelector('.kb-more').click(); await wait();
    [...document.querySelectorAll('.el-dropdown-menu__item')].find(e => e.textContent.trim() === '重命名' && e.getClientRects().length).click(); await wait();
    input('.el-dialog input', '已重命名'); await wait(); button('确定').click(); await wait();
    check([...document.querySelectorAll('.kb-category-row')].some(e => e.textContent.includes('已重命名')), 'rename shown');
    [...document.querySelectorAll('.kb-category-row')].find(e => e.textContent.includes('已重命名')).querySelector('.kb-more').click(); await wait();
    [...document.querySelectorAll('.el-dropdown-menu__item')].find(e => e.textContent.trim() === '删除分类' && e.getClientRects().length).click(); await wait();
    button('确定').click(); await wait();
    check(![...document.querySelectorAll('.kb-category-row')].some(e => e.textContent.includes('已重命名')), 'category removed');
    button('新建知识库').click(); await wait();
    input('.el-dialog input', '验收资料库'); await wait(); button('确定').click(); await wait();
    check(window.kbHost.libraries.some(l => l.title === '验收资料库'), 'new library action');
    button('设置').click(); await wait();
    button('删除空知识库').click(); await wait();
    check(!window.kbHost.libraries.some(l => l.title === '验收资料库'), 'delete empty library action');
    document.querySelector('.kb-document').click(); await wait();
    await window.kbEditor.waitReady(); window.kbEditor.set('未保存'); await wait();
    window.kbHost.port.postMessage({ type: 'mincode.host.open-request', request: { entry: 'knowledge-base.open', params: { docId: 'two' } } }); await wait();
    check(Boolean(button('继续编辑')), 'palette request obeys unsaved guard');
    button('放弃修改').click(); await wait();
    check(document.querySelector('.kb-title-input input').value === '接口说明', 'palette opens requested document');
  });
  assert.equal(result, 'PASS');
});

test('knowledge real file input import and AI source navigation still work', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(r => setTimeout(r, 360));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    const button = label => { const e = [...document.querySelectorAll('button')].find(e => e.textContent.trim() === label && e.getClientRects().length); if (!e) throw new Error('Missing button '+label+'; visible='+[...document.querySelectorAll('button')].filter(e => e.getClientRects().length).map(e => e.textContent.trim()).join('|')); return e; };
    const files = new DataTransfer(); files.items.add(new File(['# 导入正文'], '验收导入.md', { type: 'text/markdown' }));
    const input = document.querySelector('input[type=file]'); input.files = files.files; input.dispatchEvent(new Event('change', { bubbles: true })); await wait(); await wait();
    for (let i=0; i<40 && !window.kbHost.docs.some(d => d.title === '验收导入'); i++) await wait();
    check(document.querySelector('.kb-documents').textContent.includes('验收导入'), 'import reaches real list: '+JSON.stringify(window.kbHost.calls)+' '+document.querySelector('.kb').textContent);
    const imported = window.kbHost.docs.find(d => d.title === '验收导入');
    check(imported.content === '# 导入正文', 'import body preserved');
    button('✦ 问 AI').click(); await wait();
    const question = document.querySelector('.kb-ask-row input'); question.value = '产品目标'; question.dispatchEvent(new Event('input', { bubbles: true })); await wait();
    button('提问').click(); await wait(); await wait();
    check(document.querySelector('.kb-ask-answer').textContent.includes('本季度'), 'AI answer shown');
    check(window.kbHost.calls.some(c => c.action === 'ask' && c.payload.library === 'default' && c.payload.question === '产品目标'), 'AI request uses current library');
    document.querySelector('.kb-ask-sources button').click(); await wait();
    check(document.querySelector('.kb-title-input input').value === '产品路线图', 'AI source opens correct note');
  });
  assert.equal(result, 'PASS');
});

test('knowledge English narrow workspace has reachable panels without horizontal overflow', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(r => setTimeout(r, 360));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    document.querySelector('[data-testid="toggle-list"]').click(); await wait();
    document.querySelector('.kb-document').click(); await wait();
    document.querySelector('[data-testid="mode-preview"]').click(); await wait();
    check(!document.querySelector('.kb-writing'), 'English preview mode');
    const prop = [...document.querySelectorAll('button')].find(e => e.textContent.trim() === 'Document properties'); prop.click(); await wait();
    check(document.querySelectorAll('.kb-properties .el-select').length === 3, 'English properties reachable');
    document.querySelector('.kb-properties-header button').click(); await wait();
    check(document.documentElement.scrollWidth <= innerWidth, 'no page horizontal overflow');
    check(document.querySelector('.kb-editor').scrollWidth <= document.querySelector('.kb-editor').clientWidth, 'bounded editor');
    check(!document.querySelector('.kb').textContent.includes('editor.') && !document.querySelector('.kb').textContent.includes('nav.'), 'no untranslated visible keys');
  }, { locale: 'en-US', width: 600, theme: 'dark' });
  assert.equal(result, 'PASS');
});

test('knowledge long AI source titles stay inside narrow dialog and still open the source', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(resolve => setTimeout(resolve, 360));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    const button = label => [...document.querySelectorAll('button')].find(e => e.textContent.trim() === label && e.getClientRects().length);
    const title = 'LongTitle'.repeat(22);
    window.kbHost.docs[0].title = title;
    for (let i = 0; i < 40 && !(button('✦ 问 AI') && !button('✦ 问 AI').disabled); i++) await wait();
    button('✦ 问 AI').click(); await wait();
    const question = document.querySelector('.kb-ask-row input');
    question.value = '产品目标'; question.dispatchEvent(new Event('input', { bubbles: true })); await wait();
    button('提问').click(); await wait(); await wait();
    const source = document.querySelector('.kb-ask-sources button');
    const dialog = source.closest('.el-dialog').getBoundingClientRect();
    const bounds = source.getBoundingClientRect();
    check(bounds.right <= dialog.right && bounds.left >= dialog.left && bounds.right <= innerWidth, 'AI source must stay within dialog and viewport; width=' + bounds.width);
    check(source.title === title, 'full source title available as tooltip');
    source.click(); await wait();
    check(document.querySelector('.kb-title-input input').value === title, 'long source still opens correct document');
    check(window.kbHost.calls.some(c => c.action === 'list' && c.payload.id === 'one'), 'source ID preserved');
  }, { width: 600 });
  assert.equal(result, 'PASS');
});

test('knowledge editor fields are padded, split stays side by side, and mode hover stays readable', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(resolve => setTimeout(resolve, 300));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    [...document.querySelectorAll('button')].find(e => e.textContent.trim() === '新建笔记').click(); await wait();
    const title = getComputedStyle(document.querySelector('.kb-title-input .el-input__wrapper'));
    await window.kbEditor.waitReady();
    check(parseFloat(title.paddingLeft) >= 10, 'title text has inner padding: ' + title.paddingLeft);
    const editorBox = document.querySelector('.kb-md-editor').getBoundingClientRect();
    check(editorBox.width > 200 && editorBox.height > 300, 'markdown editor occupies the writing column');
    const writing = document.querySelector('.kb-writing').getBoundingClientRect();
    const preview = document.querySelector('.kb-preview').getBoundingClientRect();
    check(preview.left >= writing.right && Math.abs(preview.top - writing.top) < 4, 'split shows preview beside editor at 1100px');
  }, { width: 1100 });
  assert.equal(result, 'PASS');
});


test('markdown editor formats with toolbar and shortcuts, saves once with Mod-S and refuses oversize text', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(resolve => setTimeout(resolve, 260));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    const saves = () => window.kbHost.calls.filter(c => c.action === 'save').length;
    const mac = /Mac/.test(navigator.platform);
    const key = (k, target) => target.dispatchEvent(new KeyboardEvent('keydown', { key: k, code: 'Key' + k.toUpperCase(), metaKey: mac, ctrlKey: !mac, bubbles: true, cancelable: true, composed: true }));
    document.querySelector('.kb-document').click(); await wait();
    await window.kbEditor.waitReady();
    check(window.kbEditor.value().startsWith('# 产品规划'), 'editor shows stored markdown');
    const host = document.querySelector('[data-testid="markdown-editor"]');
    check(Boolean(host.shadowRoot.querySelector('.cm-lineNumbers')), 'line numbers shown');
    check(Boolean(host.shadowRoot.querySelector('.cm-content [class*="ͼ"], .cm-content span[class]')), 'markdown highlighted');

    // Mod-S without changes must not save.
    key('s', document); await wait();
    check(saves() === 0, 'clean document does not save on Mod-S');

    window.kbEditor.set('abc'); await wait();
    window.kbEditor.select(1, 2);
    document.querySelector('[data-format="bold"]').click(); await wait();
    check(window.kbEditor.value() === 'a**b**c', 'toolbar bold wraps selection: ' + window.kbEditor.value());
    window.kbEditor.set('item'); await wait();
    window.kbEditor.select(0, 4);
    document.querySelector('[data-format="bulletList"]').click(); await wait();
    check(window.kbEditor.value() === '- item', 'toolbar list prefixes line');
    window.kbEditor.select(2, 6);
    key('i', host.shadowRoot.querySelector('.cm-content')); await wait();
    check(window.kbEditor.value() === '- *item*', 'Mod-I italicizes selection: ' + window.kbEditor.value());
    check(document.querySelector('.kb-save-status').textContent.includes('未保存'), 'edits mark unsaved');

    key('s', host.shadowRoot.querySelector('.cm-content')); await wait(); await wait();
    check(saves() === 1, 'Mod-S saves exactly once: ' + saves());
    check(window.kbHost.calls.filter(c => c.action === 'save').at(-1).payload.content === '- *item*', 'saved markdown is raw source');
    check(document.querySelector('.kb-save-status').textContent.includes('已保存'), 'saved state shown');

    window.kbEditor.set('x'.repeat(100001)); await wait();
    check(window.kbEditor.value() === '- *item*', 'oversize paste refused and content kept');
    check(document.body.textContent.includes('10 万字符上限'), 'limit message shown');
  });
  assert.equal(result, 'PASS');
});

test('markdown editor follows document switches without leaking undo or dirty state', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(resolve => setTimeout(resolve, 260));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    const docs = () => [...document.querySelectorAll('.kb-document')];
    docs()[0].click(); await wait(); await window.kbEditor.waitReady();
    docs()[1].click(); await wait();
    check(window.kbEditor.value() === 'API 设计', 'second document content loaded');
    check(document.querySelector('.kb-save-status').textContent.includes('已保存'), 'switching is not an edit');
    const content = document.querySelector('[data-testid="markdown-editor"]').shadowRoot.querySelector('.cm-content');
    content.focus();
    const mac = /Mac/.test(navigator.platform);
    content.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', code: 'KeyZ', metaKey: mac, ctrlKey: !mac, bubbles: true, cancelable: true, composed: true })); await wait();
    check(window.kbEditor.value() === 'API 设计', 'undo cannot bring back the previous document');
    check(document.querySelector('.kb-save-status').textContent.includes('已保存'), 'undo after switch leaves document clean');
    document.querySelector('[data-testid="mode-preview"]').click(); await wait();
    check(!document.querySelector('[data-testid="markdown-editor"]'), 'preview mode has no editor');
    document.querySelector('[data-testid="mode-edit"]').click(); await wait(); await window.kbEditor.waitReady();
    check(window.kbEditor.value() === 'API 设计', 'editor remounts with current draft');
  });
  assert.equal(result, 'PASS');
});

test('markdown editor follows dark theme and narrow windows', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(resolve => setTimeout(resolve, 260));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    document.querySelector('[data-testid="toggle-list"]').click(); await wait();
    document.querySelector('.kb-document').click(); await wait(); await window.kbEditor.waitReady();
    const toolbar = document.querySelector('.kb-md-toolbar').getBoundingClientRect();
    check(toolbar.right <= innerWidth && toolbar.left >= 0, 'toolbar fits narrow window');
    check(document.documentElement.scrollWidth <= innerWidth, 'no horizontal page overflow');
    const editor = document.querySelector('[data-testid="markdown-editor"]').shadowRoot.querySelector('.cm-editor');
    const bg = getComputedStyle(editor).backgroundColor;
    check(bg !== 'rgb(255, 255, 255)', 'dark theme editor is not white: ' + bg);
  }, { width: 600, theme: 'dark' });
  assert.equal(result, 'PASS');
});


test('markdown editor keeps undo history, cursor and focus across search and save', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(resolve => setTimeout(resolve, 300));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    const mac = /Mac/.test(navigator.platform);
    const content = () => document.querySelector('[data-testid="markdown-editor"]').shadowRoot.querySelector('.cm-content');
    const key = k => content().dispatchEvent(new KeyboardEvent('keydown', { key: k, code: 'Key' + k.toUpperCase(), metaKey: mac, ctrlKey: !mac, bubbles: true, cancelable: true, composed: true }));
    document.querySelector('.kb-document').click(); await wait(); await window.kbEditor.waitReady();
    const original = window.kbEditor.value();
    window.kbEditor.select(0); window.kbEditor.insert('hello '); await wait();
    window.kbEditor.select(3);
    [...document.querySelectorAll('button')].find(e => e.textContent.trim() === '搜索').click(); await wait(); await wait();
    check(window.kbEditor.selection() === 3, 'cursor kept after search: ' + window.kbEditor.selection());
    key('z'); await wait();
    check(window.kbEditor.value() === original, 'undo still works after search: ' + JSON.stringify(window.kbEditor.value().slice(0, 20)));
    window.kbEditor.select(0); window.kbEditor.insert('saved '); await wait();
    window.kbEditor.select(4);
    key('s'); await wait(); await wait();
    check(document.querySelector('.kb-save-status').textContent.includes('已保存'), 'save completed');
    check(window.kbEditor.selection() === 4, 'cursor kept after save: ' + window.kbEditor.selection());
    check(window.kbEditor.hasFocus(), 'focus kept after save');
    key('z'); await wait();
    check(window.kbEditor.value() === original, 'undo after save reaches pre-save edit');
    check(document.querySelector('.kb-save-status').textContent.includes('未保存'), 'undoing a saved edit marks unsaved');
  });
  assert.equal(result, 'PASS');
});

test('editor header is a single compact row and the title field is compact', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(resolve => setTimeout(resolve, 300));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    document.querySelector('.kb-document').click(); await wait(); await window.kbEditor.waitReady();
    check(!document.querySelector('.kb-modebar'), 'modes live in the same row as breadcrumb and actions');
    const toolbar = document.querySelector('.kb-toolbar').getBoundingClientRect();
    check(toolbar.height <= 48, 'single header row stays compact: ' + toolbar.height);
    for (const selector of ['.kb-breadcrumb', '.kb-modes', '.kb-inline']) {
      const box = document.querySelector('.kb-toolbar ' + selector).getBoundingClientRect();
      check(Math.abs((box.top + box.bottom) / 2 - (toolbar.top + toolbar.bottom) / 2) < 6, selector + ' sits on the header row');
    }
    const editorTop = document.querySelector('.kb-writing').getBoundingClientRect().top;
    check(editorTop - toolbar.bottom <= 26, 'writing area starts close to the header: ' + (editorTop - toolbar.bottom));
    const title = document.querySelector('.kb-title-input .el-input__wrapper').getBoundingClientRect();
    check(title.height <= 38, 'title field is compact: ' + title.height);
    check(parseFloat(getComputedStyle(document.querySelector('.kb-title-input input')).fontSize) <= 17, 'title font is compact');
    const props = [...document.querySelectorAll('.kb-toolbar button')].find(e => e.textContent.trim() === '文档属性');
    check(Boolean(props), 'properties toggle stays reachable in the header');
  }, { width: 1440 });
  assert.equal(result, 'PASS');
});

test('compact editor header wraps without overflow in narrow windows', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(resolve => setTimeout(resolve, 300));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    document.querySelector('[data-testid="toggle-list"]').click(); await wait();
    document.querySelector('.kb-document').click(); await wait();
    const toolbar = document.querySelector('.kb-toolbar');
    check(toolbar.scrollWidth <= toolbar.clientWidth + 1, 'header has no horizontal overflow');
    for (const e of toolbar.querySelectorAll('button')) { const b = e.getBoundingClientRect(); check(b.right <= innerWidth && b.left >= 0, 'header button visible: ' + e.textContent.trim()); }
    check(document.documentElement.scrollWidth <= innerWidth, 'no page overflow');
  }, { width: 600 });
  assert.equal(result, 'PASS');
});


test('mode buttons keep readable colors under a real mouse hover', { skip: !browserAvailable }, async () => {
  const result = await runKnowledgeBrowser(async () => {
    const wait = () => new Promise(resolve => setTimeout(resolve, 300));
    const check = (ok, label) => { if (!ok) throw new Error(label); };
    document.querySelector('.kb-document').click(); await wait();
    const parse = value => (value.match(/[\d.]+/g) || []).slice(0, 3).map(Number);
    const luminance = rgb => { const c = rgb.map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }); return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]; };
    const contrast = (a, b) => { const [x, y] = [luminance(a), luminance(b)].sort((m, n) => n - m); return (x + 0.05) / (y + 0.05); };
    for (const mode of ['edit', 'split', 'preview']) {
      const button = document.querySelector('[data-testid="mode-' + mode + '"]');
      button.click(); await wait();
      await window.kbHover(button);
      check(button.matches(':hover'), mode + ' is really hovered');
      const style = getComputedStyle(button);
      const bg = parse(style.backgroundColor), fg = parse(style.color);
      check(contrast(fg, bg) >= 3, mode + ' selected hover text contrast too low: ' + style.color + ' on ' + style.backgroundColor);
      check(Math.abs(bg[2] - bg[0]) < 40, mode + ' selected hover must stay a neutral chip, not a blue fill: ' + style.backgroundColor);
      const other = document.querySelector('[data-testid="mode-' + (mode === 'edit' ? 'split' : 'edit') + '"]');
      await window.kbHover(other);
      const os = getComputedStyle(other);
      check(contrast(parse(os.color), parse(os.backgroundColor)) >= 3, mode + ' inactive hover text contrast too low: ' + os.color + ' on ' + os.backgroundColor);
    }
  }, { width: 1440 });
  assert.equal(result, 'PASS');
});
