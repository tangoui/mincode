import assert from 'node:assert/strict';
import test from 'node:test';
import { access, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const chrome = process.env.CHROME_BIN || '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge';
const available = await access(chrome).then(() => true, () => false);
const run = promisify(execFile);

// The plugin catalog toolbar: search on the left, the two filters and the install button in one row on the right.
test('plugin catalog filters and install button stay in one compact row', { skip: !available && 'set CHROME_BIN to a Chromium browser' }, async () => {
  // Element Plus styles load on demand with each component, i.e. after the workspace stylesheet.
  const elementCSS = await readFile(require.resolve('element-plus/dist/index.css'), 'utf8');
  const workspaceCSS = await readFile(new URL('../src/styles/workspaces/plugins/workspace.css', import.meta.url), 'utf8');
  // Same DOM Element Plus renders for PluginCatalogToolbar.vue (search field, two small selects, split install button).
  const select = label => `<div class="el-select el-select--small app-plugin-toolbar__filter"><div class="el-select__wrapper"><div class="el-select__selection"><span>${label}</span></div><div class="el-select__suffix">⌄</div></div></div>`;
  const toolbar = `<div class="app-plugin-toolbar"><div class="app-plugin-toolbar__row">
    <div class="app-plugin-toolbar__query"><div class="el-input app-search-field"><div class="el-input__wrapper"><input class="el-input__inner" placeholder="搜索插件，或输入插件目录、插件包路径或 HTTPS 地址后安装"></div></div>PREVIEW</div>
    <div class="app-plugin-toolbar__actions">${select('全部来源')}${select('全部状态')}
      <div class="el-dropdown app-plugin-toolbar__install"><div class="el-button-group"><button class="el-button el-button--primary el-button--small">安装插件</button><button class="el-button el-button--primary el-button--small el-dropdown__caret-button">⌄</button></div></div>
    </div></div></div>`;
  // Without and with an install source typed into the search box.
  const variants = [toolbar.replace('PREVIEW', ''), toolbar.replace('PREVIEW', '<button class="el-button el-button--primary el-button--small app-plugin-toolbar__preview">安装预检</button>')];
  const temp = await mkdtemp(join(tmpdir(), 'mincode-plugin-toolbar-'));
  try {
    const file = join(temp, 'layout.html');
    await writeFile(file, `<!doctype html><meta charset="utf-8"><style>${workspaceCSS}\n${elementCSS}</style><div id="fixture"></div><pre id="result"></pre><script>
const failures = [];
for (const [variant, markup] of ${JSON.stringify(variants)}.entries()) for (const width of [1800, 1200, 960]) {
  const fixture = document.querySelector('#fixture');
  fixture.style.width = width + 'px';
  fixture.innerHTML = markup;
  const box = selector => fixture.querySelector(selector).getBoundingClientRect();
  const filters = [...fixture.querySelectorAll('.app-plugin-toolbar__filter')].map(node => node.getBoundingClientRect());
  const install = box('.app-plugin-toolbar__install');
  const search = box('.app-search-field');
  const middle = rect => (rect.top + rect.bottom) / 2;
  for (const rect of [...filters, install]) {
    if (Math.abs(middle(rect) - middle(search)) > 2) failures.push(width + ': control not on the search row');
  }
  for (const rect of filters) if (rect.width > 160) failures.push(width + ': filter stretched to ' + Math.round(rect.width) + 'px');
  if (filters[0].right > filters[1].left || filters[1].right > install.left) failures.push(width + ': controls overlap or are out of order');
  if (search.right > filters[0].left) failures.push(width + ': search overlaps the filters');
  const preview = fixture.querySelector('.app-plugin-toolbar__preview');
  if (variant && (!preview || Math.abs(middle(preview.getBoundingClientRect()) - middle(search)) > 2 || preview.getBoundingClientRect().left < search.right)) failures.push(width + ': preview button not right after the search box');
}
document.querySelector('#result').textContent = JSON.stringify(failures);
</script>`);
    const { stdout } = await run(chrome, ['--headless', '--no-sandbox', '--disable-gpu', '--no-first-run', `--user-data-dir=${join(temp, 'profile')}`, '--virtual-time-budget=1000', '--dump-dom', `file://${file}`], { timeout: 30000, maxBuffer: 8 * 1024 * 1024 });
    const result = stdout.match(/<pre id="result">([^<]*)<\/pre>/);
    assert.ok(result, 'browser did not execute layout checks');
    assert.deepEqual(JSON.parse(result[1]), []);
  } finally {
    await rm(temp, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
  }
});
