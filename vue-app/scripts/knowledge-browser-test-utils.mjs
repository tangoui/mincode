import { access, readFile, mkdtemp, writeFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { spawn } from 'node:child_process';

export const chrome = process.env.CHROME_BIN || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
export const browserAvailable = await access(chrome).then(() => true, () => false);

// A stateful host boundary for the real built plugin. It never writes user data.
export function installKnowledgeHost({ locale, theme }) {
  location.hash = 'mincodeSession=test&nonce=test&generation=1';
  const initial = { id: 'one', library: 'default', title: '产品路线图', content: '# 产品规划\n\n当前季度目标。\n\n## 里程碑\n\n- 完成知识库工作台\n- 验证布局和操作', folder: '产品规划', tags: '产品,规划', source: 'roadmap.md', favorite: false, updated: '2026-11-06T10:30:00' };
  const docs = [initial, { ...initial, id: 'two', title: '接口说明', folder: '技术方案', tags: '技术', content: 'API 设计', source: '' }];
  const libraries = [{ id: 'default', title: '产品研发空间', description: '让知识有序沉淀' }, { id: 'other', title: '技术资料' }];
  const categories = ['产品规划', '技术方案'];
  const state = window.kbHost = { docs, libraries, calls: [], port: null };
  function invoke(action, p) {
    const doc = docs.find(d => d.id === p.id);
    const current = () => docs.filter(d => d.library === p.library && !d.deleted);
    if (action === 'libraries') return libraries.map(l => ({ ...l, count: docs.filter(d => d.library === l.id && !d.deleted).length }));
    if (action === 'categories') return categories.map(name => ({ name, count: current().filter(d => d.folder === name).length }));
    if (action === 'stats') return { documents: current().length, favorites: current().filter(d => d.favorite).length, folders: categories.length, tags: 3, bytes: 1024 };
    if (action === 'list' || action === 'search') {
      if (p.id) return { ...doc };
      return docs.filter(d => d.library === p.library && Boolean(d.deleted) === Boolean(p.deleted) && (p.folder === undefined || d.folder === p.folder) && (p.tags || []).every(t => d.tags.split(',').includes(t)) && (!p.query || (d.title + d.content + d.tags).includes(p.query))).map(d => ({ ...d, excerpt: d.content.slice(0, 50) }));
    }
    if (action === 'save') { const value = { ...p, id: p.id || 'new-' + docs.length, updated: '2026-11-06T11:00:00' }; if (doc) Object.assign(doc, value); else docs.push(value); return { ...value }; }
    if (action === 'favorite') { doc.favorite = p.favorite; return { favorite: p.favorite }; }
    if (action === 'trash' || action === 'restore') { doc.deleted = action === 'trash'; return { ...doc }; }
    if (action === 'purge') { docs.splice(docs.indexOf(doc), 1); return true; }
    if (action === 'history') return [{ id: 'rev-1', title: '旧标题', created: '2026-11-05T09:00:00', size: 80 }];
    if (action === 'restoreVersion') { doc.content = '# 恢复的正文'; return { ...doc }; }
    if (action === 'exportDocument') return { saved: true, filename: doc.title + '.md' };
    if (action === 'saveLibrary') { let item = libraries.find(l => l.id === p.id); if (item) Object.assign(item, p); else { item = { ...p, id: 'lib-new' }; libraries.push(item); } return item; }
    if (action === 'deleteLibrary') { libraries.splice(libraries.findIndex(l => l.id === p.id), 1); return true; }
    if (action === 'saveCategory') { if (p.previous) { const i = categories.indexOf(p.previous); categories[i] = p.name; docs.filter(d => d.folder === p.previous).forEach(d => { d.folder = p.name; }); } else categories.push(p.name); return true; }
    if (action === 'deleteCategory') { categories.splice(categories.indexOf(p.name), 1); docs.filter(d => d.folder === p.name).forEach(d => { d.folder = ''; }); return true; }
    if (action === 'importBatch') { const values = p.documents.map((d, i) => ({ ...d, id: 'import-' + docs.length + '-' + i, library: p.library, favorite: false, updated: '2026-11-06' })); docs.push(...values); return values; }
    if (action === 'ask') return { found: true, answer: '本季度优先完成工作台。 [1]', sources: [{ id: 'one', title: docs.find(d => d.id === 'one').title }] };
    throw new Error('Unimplemented fixture action ' + action);
  }
  addEventListener('message', ({ data }) => {
    if (data?.type !== 'mincode.plugin.ready') return;
    const channel = new MessageChannel(); state.port = channel.port1;
    channel.port1.onmessage = ({ data: request }) => {
      if (!request.id) return;
      state.calls.push(request);
      try {
        const result = request.type === 'mincode.plugin.clipboard.write-text' ? true : invoke(request.action, request.payload || {});
        channel.port1.postMessage({ type: 'mincode.host.response', id: request.id, ok: true, result });
      } catch (error) { channel.port1.postMessage({ type: 'mincode.host.response', id: request.id, ok: false, error: { message: error.message } }); }
    };
    postMessage({ type: 'mincode.host.connect', protocolVersion: 1, sessionId: 'test', nonce: 'test', generation: 1, features: { clipboardWrite: 1 }, context: { locale, theme } }, '*', [channel.port2]);
  });
}

export const knowledgeUi = new URL('../../official-plugins/knowledge-base/ui/', import.meta.url);

// Page helpers for the Markdown editor (CodeMirror inside a Shadow DOM). They type
// through CodeMirror transactions, the same path a user's keystrokes take.
export function installEditorHelpers() {
  const view = () => {
    const host = document.querySelector('[data-testid="markdown-editor"]');
    const node = host?.shadowRoot?.querySelector('.cm-content');
    return node?.cmView?.rootView?.view || node?.cmTile?.root?.view || null;
  };
  const hoverWaiters = new Map();
  let hoverId = 0;
  window.kbHoverDone = id => { hoverWaiters.get(id)?.(); hoverWaiters.delete(id); };
  window.kbHover = element => new Promise(resolve => {
    const box = element.getBoundingClientRect();
    const id = ++hoverId;
    hoverWaiters.set(id, () => setTimeout(resolve, 120));
    window.kbHoverRequest(JSON.stringify({ id, x: box.left + box.width / 2, y: box.top + box.height / 2 }));
  });
  window.kbEditor = {
    ready: () => Boolean(view()),
    value: () => view()?.state.doc.toString() ?? null,
    set(text) { const v = view(); v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: text }, userEvent: 'input.paste' }); },
    select(from, to = from) { const v = view(); v.focus(); v.dispatch({ selection: { anchor: from, head: to } }); },
    insert(text) { const v = view(); const { from, to } = v.state.selection.main; v.dispatch({ changes: { from, to, insert: text }, selection: { anchor: from + text.length }, userEvent: 'input.type' }); },
    selection: () => view()?.state.selection.main.head ?? null,
    hasFocus: () => Boolean(view()?.hasFocus),
    async waitReady(timeout = 6000) { const start = Date.now(); while (!view() && Date.now() - start < timeout) await new Promise(r => setTimeout(r, 50)); if (!view()) throw new Error('markdown editor did not load'); },
  };
}

export async function makeKnowledgePage(scenario, { locale = 'zh', theme = 'light' } = {}) {
  const css = await readFile(new URL('style.css', knowledgeUi), 'utf8');
  return `<!doctype html><html><head><meta charset="utf-8"><style>${css}\n*,*::before,*::after{transition:none!important;animation:none!important}</style></head><body><div id="app"></div><script>
  const report = value => { if (document.querySelector('#browser-result')) return; const e=document.createElement('pre'); e.id='browser-result'; e.textContent=value; document.body.append(e); };
  addEventListener('error', e=>report('ERROR: '+e.message)); addEventListener('unhandledrejection',e=>report('ERROR: '+(e.reason?.stack||e.reason)));
  (${installKnowledgeHost.toString()})(${JSON.stringify({ locale, theme })});
  (${installEditorHelpers.toString()})();
  // Start once the plugin has loaded documents (busy cleared), not after a fixed delay.
  const waitLoaded = async () => { const start = Date.now(); while (Date.now() - start < 15000) { if (document.querySelector('.kb-document') && !document.querySelector('.kb-global-status')?.textContent.includes('处理中') && !document.querySelector('.kb-global-status')?.textContent.includes('Processing')) return; await new Promise(r => setTimeout(r, 50)); } };
  setTimeout(async()=>{try{await waitLoaded();await (${scenario.toString()})();report('PASS')}catch(e){report(e.stack||e.message)}},300);
  </script><script type="module" src="${new URL('app.js', knowledgeUi).href}"></script></body></html>`;
}

// Real time CDP is intentional: File.text() and MessagePort deliveries must not
// race Chromium's virtual clock (which can exhaust its budget before file I/O).
export async function runKnowledgeBrowser(scenario, options = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'mincode-kb-browser-'));
  try {
    const file = join(dir, 'page.html'); await writeFile(file, await makeKnowledgePage(scenario, options));
    return await renderPageFile(file, options);
  } finally {
    await rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  }
}

// Opens a local page in a real-time headless browser and returns the text of
// its result element (default #browser-result) once the page reports it.
export async function renderPageFile(file, options = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'mincode-kb-profile-'));
  const selector = options.resultSelector || '#browser-result';
  let child, socket;
  try {
    child = spawn(chrome, ['--headless', '--no-sandbox', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--allow-file-access-from-files', '--remote-debugging-port=0', `--user-data-dir=${join(dir, 'profile')}`, `--window-size=${options.width || 1440},1000`, 'about:blank']);
    const endpoint = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('Browser CDP startup timeout')), 15000);
      let err = '';
      child.stderr.on('data', data => { err += data; const match = err.match(/DevTools listening on (ws:\/\/[^\s]+)/); if (match) { clearTimeout(timer); resolve(match[1]); } });
      child.on('error', error => { clearTimeout(timer); reject(error); });
      child.on('exit', code => { clearTimeout(timer); reject(new Error('Browser exited ' + code + ': ' + err.slice(-800))); });
    });
    socket = new WebSocket(endpoint);
    await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, { once: true }); socket.addEventListener('error', reject, { once: true }); });
    let next = 0;
    const pending = new Map();
    socket.addEventListener('message', event => { const message = JSON.parse(event.data); const entry = pending.get(message.id); if (entry) { pending.delete(message.id); clearTimeout(entry.timer); if (message.error) entry.reject(new Error(JSON.stringify(message.error))); else entry.resolve(message.result); } });
    const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => { const id = ++next; const timer = setTimeout(() => { pending.delete(id); reject(new Error('CDP timed out: ' + method)); }, 35000); pending.set(id, { resolve, reject, timer }); socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) })); });
    const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
    const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
    await send('Emulation.setDeviceMetricsOverride', { width: options.width || 1440, height: 1000, deviceScaleFactor: 1, mobile: false }, sessionId);
    await send('Page.navigate', { url: `file://${file}` }, sessionId);
    await send('Runtime.addBinding', { name: 'kbHoverRequest' }, sessionId);
    socket.addEventListener('message', async event => {
      const message = JSON.parse(event.data);
      if (message.method !== 'Runtime.bindingCalled' || message.params?.name !== 'kbHoverRequest') return;
      const { id, x, y } = JSON.parse(message.params.payload);
      await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y }, sessionId);
      await send('Runtime.evaluate', { expression: `window.kbHoverDone?.(${JSON.stringify(id)})` }, sessionId);
    });
    const result = await send('Runtime.evaluate', { expression: `new Promise(resolve => { const start=Date.now(); const poll=()=>{const e=document.querySelector(${JSON.stringify(selector)});if(e)resolve(e.textContent);else if(Date.now()-start>27000)resolve('Scenario timeout');else setTimeout(poll,50)};poll()})`, awaitPromise: true, returnByValue: true }, sessionId);
    if (options.screenshotPath) {
      const screenshot = await send('Page.captureScreenshot', { format: 'png' }, sessionId);
      await writeFile(options.screenshotPath, Buffer.from(screenshot.data, 'base64'));
    }
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  } finally {
    socket?.close();
    if (child && child.exitCode === null && child.signalCode === null) {
      await new Promise(resolve => { const timer = setTimeout(() => { child.kill('SIGKILL'); resolve(); }, 3000); child.once('close', () => { clearTimeout(timer); resolve(); }); child.kill('SIGTERM'); });
    }
    await rm(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  }
}
