// Element Plus stylesheets for official plugin UI builds, derived from the components a page
// actually uses instead of a fixed list in the shared mount helper.
//
// `vue-app/src/plugin-ui/element-plus-styles.js` keeps the historical fixed list for third-party
// builds (the Vue + Go template vendors it unchanged). Official builds replace that module with
// the base stylesheet plus `element-plus/es/components/<name>/style/css.mjs` for every component
// imported (transitively) from the page entry; those style modules pull in the stylesheets of the
// components they depend on. After bundling, every Element Plus component that was rendered into
// the page is checked against the stylesheets that were bundled, so a missed stylesheet fails the
// build instead of shipping an unstyled control.
import { existsSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';

export const STYLE_MODULE_SUFFIX = '/src/plugin-ui/element-plus-styles.js';

const IMPORT_SPECIFIER = /(?:^|[\s;}])(?:import|export)\s*(?:[\w$*{}\s,]+?\s*from\s*)?["']([^"']+)["']|import\(\s*["']([^"']+)["']\s*\)/g;
const ELEMENT_NAMED_IMPORT = /(?:^|[\s;}])(?:import|export)\s*\{([^}]*)\}\s*from\s*["']element-plus["']/g;
const SOURCE_EXTENSIONS = ['', '.js', '.mjs', '.ts', '.vue', '/index.js', '/index.mjs', '/index.ts'];

const normalizeId = id => id.replaceAll('\\', '/').split('?')[0];

function componentsDir(root) {
  return resolve(root, 'node_modules/element-plus/es/components');
}

function hasStyle(root, name) {
  return /^[a-z][a-z0-9-]*$/.test(name) && existsSync(resolve(componentsDir(root), name, 'style/css.mjs'));
}

// ElInputNumber -> input-number, vLoading -> loading, ElLoadingDirective -> loading.
export function elementComponentName(root, exported) {
  const match = /^(?:El|v)([A-Z][A-Za-z0-9]*)$/.exec(exported.trim());
  if (!match) return undefined;
  const kebab = match[1].replace(/([a-z0-9])([A-Z])/g, '$1-$2').toLowerCase();
  for (const candidate of [kebab, kebab.replace(/-(?:directive|service)$/, '')]) {
    if (hasStyle(root, candidate)) return candidate;
  }
  return undefined;
}

function resolveSource(root, importer, specifier) {
  let base;
  if (specifier.startsWith('@mincode/plugin-ui/')) base = resolve(root, 'src/plugin-ui', specifier.slice('@mincode/plugin-ui/'.length));
  else if (specifier.startsWith('.')) base = resolve(dirname(importer), specifier);
  else return undefined;
  for (const extension of SOURCE_EXTENSIONS) {
    const candidate = base + extension;
    if (/\.(?:js|mjs|ts|vue)$/.test(candidate) && existsSync(candidate)) return candidate;
  }
  return undefined;
}

// Element Plus components named in imports reachable from entry (plugin sources and the shared
// plugin-ui modules; dependencies other than element-plus itself are not followed).
export async function scanElementComponents(root, entry) {
  const components = new Set();
  const seen = new Set();
  const queue = [resolve(entry)];
  while (queue.length) {
    const file = queue.pop();
    if (seen.has(file)) continue;
    seen.add(file);
    const source = await readFile(file, 'utf8');
    for (const match of source.matchAll(ELEMENT_NAMED_IMPORT)) {
      for (const part of match[1].split(',')) {
        const exported = part.trim().split(/\s+as\s+/)[0];
        const name = exported && elementComponentName(root, exported);
        if (name) components.add(name);
      }
    }
    for (const match of source.matchAll(IMPORT_SPECIFIER)) {
      const next = resolveSource(root, file, match[1] || match[2]);
      if (next && !seen.has(next)) queue.push(next);
    }
  }
  return [...components].sort();
}

export function elementStylesModule(components) {
  return ["import 'element-plus/theme-chalk/base.css';", ...components.map(name => `import 'element-plus/es/components/${name}/style/css.mjs';`), ''].join('\n');
}

// Theme-chalk stylesheets a component's style module imports, including its dependencies.
async function requiredStylesheets(root, name, cache) {
  if (cache.has(name)) return cache.get(name);
  const sheets = new Set();
  cache.set(name, sheets);
  const source = await readFile(resolve(componentsDir(root), name, 'style/css.mjs'), 'utf8');
  for (const match of source.matchAll(/import\s+["']([^"']+)["']/g)) {
    const specifier = match[1];
    const sheet = /theme-chalk\/([\w.-]+\.css)$/.exec(specifier);
    if (sheet) sheets.add(sheet[1]);
    const dependency = /^\.\.\/\.\.\/([a-z0-9-]+)\/style\/css\.mjs$/.exec(specifier);
    if (dependency) for (const nested of await requiredStylesheets(root, dependency[1], cache)) sheets.add(nested);
  }
  return sheets;
}

// Rollup/Vite plugin: serves the per-page style module and verifies the bundle afterwards.
// `state.missing` collects components whose stylesheets did not ship (empty when complete); only
// pages that import the shared style module (through mountPlugin) are checked.
export function elementPlusStylesPlugin({ root, components, state }) {
  return {
    name: 'mincode-plugin-ui-element-styles',
    enforce: 'pre',
    load(id) {
      if (!normalizeId(id).endsWith(STYLE_MODULE_SUFFIX)) return undefined;
      state.injected = true;
      return elementStylesModule(components);
    },
    async generateBundle(_options, bundle) {
      state.missing = [];
      // Pages that do not mount through the shared shell import their own stylesheets.
      if (!state.injected) return;
      const bundled = new Set();
      for (const id of this.getModuleIds()) {
        const sheet = /\/element-plus\/theme-chalk\/([\w.-]+\.css)$/.exec(normalizeId(id));
        if (sheet) bundled.add(sheet[1]);
      }
      const rendered = new Set();
      for (const output of Object.values(bundle)) {
        if (output.type !== 'chunk') continue;
        for (const [id, module] of Object.entries(output.modules)) {
          // A component's public index (withInstall) renders only when the component itself is
          // used, directly or by another component; shared hooks under src/ do not count.
          const component = /\/element-plus\/es\/components\/([a-z0-9-]+)\/index\.mjs$/.exec(normalizeId(id));
          if (component && module.renderedLength > 0 && hasStyle(root, component[1])) rendered.add(component[1]);
        }
      }
      const cache = new Map();
      for (const name of [...rendered].sort()) {
        const sheets = await requiredStylesheets(root, name, cache);
        if ([...sheets].some(sheet => !bundled.has(sheet))) state.missing.push(name);
      }
    },
  };
}
