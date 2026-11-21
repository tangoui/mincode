// Opt-in real-browser regression: CHROME_PATH may point to Chrome or Edge.
// Run from vue-app: node scripts/plugin-control-geometry.e2e.mjs
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const browser = process.env.CHROME_PATH ?? '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge';
const directory = await mkdtemp(join(tmpdir(), 'mincode-control-geometry-'));
const styles = process.env.GEOMETRY_CSS
  ? [await readFile(resolve(process.env.GEOMETRY_CSS), 'utf8')]
  : await Promise.all([
  'node_modules/element-plus/dist/index.css',
  'src/styles/foundation/foundation.css',
  'src/styles/shared/element-plus-controls.css',
  'src/styles/shared/compact-density.css',
  'src/styles/shared/theme-inputs.css',
].map(path => readFile(join(root, path), 'utf8')));
const script = async path => (await readFile(join(root, path), 'utf8')).replaceAll('</script', '<\\/script');
const screenshot = process.env.GEOMETRY_SCREENSHOT ? resolve(process.env.GEOMETRY_SCREENSHOT) : join(directory, 'controls.png');
const html = `<!doctype html><meta charset="utf-8">
${styles.map(css => `<style>${css}</style>`).join('\n')}
<style>body{padding:24px;margin:0}h2{font-size:16px}.row{display:flex;align-items:center;gap:10px;margin:14px 0}.row>.label{width:100px}.row>.el-input{width:320px}.row>.el-select{width:180px}pre{font-size:12px}</style>
<div id="app"></div><pre id="result">pending</pre>
<script>${await script('node_modules/vue/dist/vue.global.prod.js')}</script>
<script>${await script('node_modules/element-plus/dist/index.full.min.js')}</script>
<script>
const {h,createApp,nextTick}=Vue;
const cases=[['default',28,{}],['small',24,{size:'small'}],['large',32,{size:'large'}],['disabled',28,{disabled:true}],['suffix',28,{clearable:true,showPassword:true}]];
createApp({render(){return h('main',{},[
 h('h2',{},'插件控件高度与圆角回归'),
 ...cases.map(([name,height,props])=>h('div',{class:'row',id:name},[
  h('span',{class:'label'},name),
  h(ElementPlus.ElInput,{...props,modelValue:name==='suffix'?'demo':'',placeholder:'搜索标题、说明或正文'}),
  h(ElementPlus.ElSelect,{...props,modelValue:'all'},()=>[h(ElementPlus.ElOption,{label:'全部分类',value:'all'})]),
  h(ElementPlus.ElButton,props,()=> '重新加载')
 ]))
]);}}).mount('#app');
nextTick().then(()=>{
 const rows=cases.map(([name,expected])=>{
  const row=document.getElementById(name);
  const measure=selector=>{const e=row.querySelector(selector),r=e.getBoundingClientRect(),c=getComputedStyle(e);return {height:r.height,top:r.top,radius:c.borderTopLeftRadius}};
  return {name,expected,input:measure('.el-input__wrapper'),select:measure('.el-select__wrapper'),button:measure('.el-button')};
 });
 document.getElementById('result').textContent=JSON.stringify(rows);
 document.title='geometry-ready';
});
</script>`;
const page = join(directory, 'controls.html');
await writeFile(page, html);
const dom = execFileSync(browser, [
  '--headless=new', '--disable-gpu',
  ...(process.env.CHROME_NO_SANDBOX === '1' ? ['--no-sandbox'] : []),
  `--user-data-dir=${join(directory, 'profile')}`,
  '--no-first-run', '--no-default-browser-check', '--window-size=1080,520',
  '--virtual-time-budget=2000', `--screenshot=${screenshot}`, '--dump-dom', pathToFileURL(page).href,
], { encoding: 'utf8', timeout: 45_000, maxBuffer: 20 * 1024 * 1024, stdio: ['ignore', 'pipe', 'pipe'] });
const match = dom.match(/<pre id="result">([^<]+)<\/pre>/);
assert.ok(match && match[1] !== 'pending', 'real Vue controls must mount before measuring');
const rows = JSON.parse(match[1].replaceAll('&quot;', '"').replaceAll('&amp;', '&'));
if (process.env.GEOMETRY_RESULT) await writeFile(resolve(process.env.GEOMETRY_RESULT), JSON.stringify(rows, null, 2) + '\n');
process.stdout.write(JSON.stringify(rows, null, 2) + '\n');
for (const row of rows) {
  for (const control of ['input', 'select', 'button']) {
    assert.equal(row[control].height, row.expected, `${row.name} ${control} height`);
    assert.equal(row[control].top, row.input.top, `${row.name} ${control} top alignment`);
    const radius = row.name === 'small' && control === 'button' ? '5px' : '6px';
    assert.equal(row[control].radius, radius, `${row.name} ${control} radius`);
  }
}
process.stdout.write(`PASS: ${rows.length} real-component rows; screenshot ${screenshot}\n`);
