import assert from 'node:assert/strict';
import test from 'node:test';
import { access, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { compileStyle, parse } from 'vue/compiler-sfc';

const require = createRequire(import.meta.url);
const chrome = process.env.CHROME_BIN || '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge';
const available = await access(chrome).then(() => true, () => false);
const run = promisify(execFile);

test('database type cards keep logo, text and arrow inside the card at wide and narrow widths', { skip: !available && 'set CHROME_BIN to a Chromium browser' }, async () => {
  const source = await readFile(new URL('../src/components/database/DatabaseConnectionDialog.vue', import.meta.url), 'utf8');
  const { descriptor } = parse(source);
  const { code, errors } = compileStyle({ source: descriptor.styles[0].content, filename: 'DatabaseConnectionDialog.vue', id: 'data-v-picker', scoped: true });
  assert.deepEqual(errors, []);
  const elementCSS = await readFile(require.resolve('element-plus/dist/index.css'), 'utf8');
  const provider = '由 企业数据库驱动 / Enterprise Database Driver 提供 '.repeat(5);
  const cards = ['Exasol', 'SAP HANA', 'Vertica'].map(title => `<button data-v-picker type="button" class="db-type-card"><span data-v-picker class="db-type-card__logo db-type-card__logo--plugin">DB</span><span data-v-picker class="db-type-card__content"><b data-v-picker>${title}</b><small data-v-picker>${provider}</small></span><i data-v-picker class="db-type-card__arrow">›</i></button>`).join('');
  const temp = await mkdtemp(join(tmpdir(), 'mincode-picker-layout-'));
  try {
    const file = join(temp, 'layout.html');
    await writeFile(file, `<!doctype html><meta charset="utf-8"><style>${elementCSS}\n${code}</style><div id="fixture"></div><pre id="result"></pre><script>
const failures = [];
for (const theme of ['light', 'dark']) {
  document.documentElement.className = theme;
  for (const width of [960, 720, 480]) {
    const fixture = document.querySelector('#fixture');
    fixture.style.width = (width - 48) + 'px';
    fixture.innerHTML = ${JSON.stringify(`<div data-v-picker class="db-type-picker__grid">${cards}</div>`)};
    for (const card of fixture.querySelectorAll('.db-type-card')) {
      const outer = card.getBoundingClientRect();
      for (const selector of ['.db-type-card__logo', '.db-type-card__content', '.db-type-card__arrow']) {
        const inner = card.querySelector(selector).getBoundingClientRect();
        if (inner.left < outer.left || inner.right > outer.right + 1) failures.push(theme + '/' + width + '/' + selector);
      }
      // The arrow sits in the reserved right padding, never over the text.
      const content = card.querySelector('.db-type-card__content').getBoundingClientRect();
      const arrow = card.querySelector('.db-type-card__arrow').getBoundingClientRect();
      if (content.right > arrow.left + 1) failures.push(theme + '/' + width + '/text overlaps arrow');
      const text = card.querySelector('small');
      if (text.scrollWidth <= text.clientWidth) failures.push('long provider was not constrained');
    }
  }
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
