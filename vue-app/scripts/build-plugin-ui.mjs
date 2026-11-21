import { build } from 'vite';
import { buildPluginRuntime } from './build-plugin-runtime.mjs';
import { buildGoPlugins, goPlugins } from './build-go-plugins.mjs';
import { buildRustPlugins } from './build-rust-plugins.mjs';
import vue from '@vitejs/plugin-vue';
import { access, cp, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { elementPlusStylesPlugin, scanElementComponents } from './plugin-ui-element-styles.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const packages = resolve(root, '../official-plugins');
const generatedFiles = ['app.js', 'style.css'];
const chunkDir = 'chunks';

const generatedIndex = '<!doctype html><html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="style.css"><title>MinCode Plugin</title></head><body><div id="app"></div><script type="module" src="app.js"></script></body></html>\n';

// Relative, '/'-separated paths of every file below dir ([] when it is missing).
async function listFiles(dir) {
  const entries = await readdir(dir, { recursive: true, withFileTypes: true }).catch(error => {
    if (error.code === 'ENOENT') return [];
    throw error;
  });
  return entries.filter(entry => entry.isFile())
    .map(entry => relative(dir, resolve(entry.parentPath, entry.name)).split(sep).join('/'))
    .sort();
}

export function pluginLocaleBoundary() {
  return {
    name: 'plugin-owned-locales',
    load(id) {
      if (id.replaceAll('\\', '/').includes('/src/i18n/')) {
        throw new Error('Plugin UI must bundle plugin-owned locales, not host i18n: ' + id);
      }
    },
  };
}

async function discoverPluginUIs() {
  const available = [];
  for (const entry of await readdir(packages, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    try {
      await access(resolve(packages, entry.name, 'ui-src/entry.js'));
      available.push(entry.name);
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
  }
  return available.sort();
}

// Returns the plugin UIs to build plus the requested Go plugins that have no UI
// (for example database drivers), which only need their backend binary.
async function selectedPluginUIs(requested) {
  const available = await discoverPluginUIs();
  const headless = goPlugins.filter(name => !available.includes(name));
  const names = requested.length ? [...new Set(requested)] : [...available, ...headless];
  for (const name of names) {
    if (!available.includes(name) && !headless.includes(name)) throw new Error(`Unknown plugin UI: ${name}`);
  }
  return { names: names.filter(name => available.includes(name)), headless: names.filter(name => headless.includes(name)) };
}

// One Vite library build of a plugin page into outDir. Element Plus stylesheets follow the
// components the page uses; a component that rendered without its stylesheet is returned in
// `missing` so the caller can rebuild with it (see plugin-ui-element-styles.mjs).
async function buildPageBundle(entry, outDir, components) {
  const state = { missing: [] };
  await build({
    configFile: false,
    root,
    // Plugins own their assets; never copy the desktop app's public files.
    publicDir: false,
    plugins: [pluginLocaleBoundary(), elementPlusStylesPlugin({ root, components, state }), vue()],
    resolve: {
      alias: [
        { find: '@mincode/plugin-ui', replacement: resolve(root, 'src/plugin-ui') },
        ...['vue', 'element-plus', '@element-plus/icons-vue', 'marked', 'dompurify', 'yaml', 'smol-toml', '@xterm/xterm', '@xterm/addon-fit', '@codemirror/state', '@codemirror/view', '@codemirror/commands', '@codemirror/language', '@codemirror/legacy-modes', '@lezer/highlight'].map(dependency => ({
          find: dependency,
          replacement: resolve(root, 'node_modules', dependency),
        })),
      ],
      dedupe: ['vue'],
    },
    define: { 'process.env.NODE_ENV': JSON.stringify('production') },
    build: {
      outDir,
      emptyOutDir: true,
      lib: {
        entry,
        formats: ['es'],
        fileName: () => 'app.js',
        cssFileName: 'style',
      },
      // Vite's root does not set Rolldown's cwd, which affects emitted module identifiers.
      rolldownOptions: {
        cwd: root,
        external: ['./config.js'],
        // The page bundle is final (nothing re-bundles it), so whitespace can go too; library
        // mode keeps it by default. License banners (/*! … */, @license) stay in place.
        // Dynamic import() targets load on demand from ui/chunks/.
        output: {
          minify: true,
          comments: { legal: true },
          chunkFileNames: `${chunkDir}/[name]-[hash].js`,
        },
      },
    },
  });
  return state.missing;
}

// Builds one plugin page (the Vite library build of entry) into uiDir: app.js, style.css,
// chunks/ and, unless index is false, the generated index.html. Official plugins use it through
// buildPluginUI; the in-repo service consumer demo (tools/plugin-devkit/examples) calls it directly.
export async function buildPluginPage(entry, uiDir, { label = entry, index = true } = {}) {
  const temp = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-ui-'));
  try {
    let components = await scanElementComponents(root, entry);
    let missing = await buildPageBundle(entry, temp, components);
    if (missing.length) {
      // A component reached the bundle without a named import (for example through another
      // component); add its stylesheet module and build once more.
      components = [...new Set([...components, ...missing])].sort();
      missing = await buildPageBundle(entry, temp, components);
      if (missing.length) throw new Error(`${label}: Element Plus styles missing for ${missing.join(', ')}`);
    }
    const emitted = await listFiles(temp);
    const unexpected = emitted.filter(file => !generatedFiles.includes(file) && !file.startsWith(`${chunkDir}/`));
    if (unexpected.length) throw new Error(`${label}: unexpected plugin UI build output: ${unexpected.join(', ')}`);
    await mkdir(uiDir, { recursive: true });
    for (const file of generatedFiles) await cp(resolve(temp, file), resolve(uiDir, file));
    // Chunk names are content hashes: drop the previous set so no stale chunk ships.
    await rm(resolve(uiDir, chunkDir), { recursive: true, force: true });
    if (emitted.some(file => file.startsWith(`${chunkDir}/`))) {
      await cp(resolve(temp, chunkDir), resolve(uiDir, chunkDir), { recursive: true });
    }
    if (index) await writeFile(resolve(uiDir, 'index.html'), generatedIndex);
  } finally {
    await rm(temp, { recursive: true, force: true });
  }
}

async function buildOnePluginUI(name, outputPackages) {
  const ui = resolve(outputPackages, name, 'ui');
  // knowledge-base owns a custom index; all other indexes are generated.
  await buildPluginPage(resolve(packages, name, 'ui-src/entry.js'), ui, { label: name, index: name !== 'knowledge-base' });
  if (name === 'draw') {
    await cp(resolve(packages, name, 'public/drawio'), resolve(ui, 'drawio'), { recursive: true });
    for (const file of ['LICENSE', 'THIRD_PARTY_NOTICES.md']) {
      await cp(resolve(packages, name, 'public', file), resolve(ui, file));
    }
  }
  if (name === 'excalidraw') {
    await cp(resolve(packages, name, 'public/excalidraw'), resolve(ui, 'excalidraw'), { recursive: true });
    await cp(resolve(packages, name, 'THIRD_PARTY_NOTICES.md'), resolve(ui, 'THIRD_PARTY_NOTICES.md'));
  }
  if (name === 'file-viewer') {
    // Chunk names are content hashes: drop the previous copy so no stale chunk ships.
    await rm(resolve(ui, 'viewer'), { recursive: true, force: true });
    await cp(resolve(packages, name, 'public/viewer'), resolve(ui, 'viewer'), { recursive: true });
    await cp(resolve(packages, name, 'THIRD_PARTY_NOTICES.md'), resolve(ui, 'THIRD_PARTY_NOTICES.md'));
  }
}

export async function buildPluginUI(requested = [], { outputPackages = packages, env = process.env } = {}) {
  const { names, headless } = await selectedPluginUIs(requested);
  if (names.includes('draw')) {
    await access(resolve(packages, 'draw/public/drawio/math4/es5/startup.js')).catch(() => {
      throw new Error('draw MathJax assets are missing. Run: pnpm prepare:draw-math');
    });
  }
  await buildPluginRuntime(names, { outputPackages });
  await buildGoPlugins([...names, ...headless], { outputPackages, env });
  await buildRustPlugins(names, { outputPackages, env });
  for (const name of names) await buildOnePluginUI(name, outputPackages);
  return names;
}

export async function verifyPluginUI(requested = []) {
  const outputPackages = await mkdtemp(resolve(tmpdir(), 'mincode-plugin-ui-check-'));
  try {
    const names = await buildPluginUI(requested, { outputPackages });
    const stale = [];
    await buildPluginRuntime(names, { check: true });
    for (const name of names) {
      const files = name === 'knowledge-base' ? [...generatedFiles] : [...generatedFiles, 'index.html'];
      const generatedChunks = await listFiles(resolve(outputPackages, name, 'ui', chunkDir));
      const shippedChunks = await listFiles(resolve(packages, name, 'ui', chunkDir));
      for (const chunk of shippedChunks) if (!generatedChunks.includes(chunk)) stale.push(`${name}/ui/${chunkDir}/${chunk} (no longer generated)`);
      files.push(...generatedChunks.map(chunk => `${chunkDir}/${chunk}`));
      if (name === 'draw') files.push('drawio/index.html', 'drawio/js/bootstrap.js', 'drawio/js/main.js', 'drawio/resources/dia.txt', 'drawio/resources/dia_zh.txt', 'drawio/LICENSE', 'drawio/math4/es5/startup.js', 'THIRD_PARTY_NOTICES.md');
      if (name === 'excalidraw') files.push('excalidraw/editor.js', 'excalidraw/editor.css', 'THIRD_PARTY_NOTICES.md');
      if (name === 'file-viewer') files.push('viewer/viewer.js', 'viewer/viewer.css', 'THIRD_PARTY_NOTICES.md');
      for (const file of files) {
        const generated = await readFile(resolve(outputPackages, name, 'ui', file));
        const shipped = await readFile(resolve(packages, name, 'ui', file)).catch(error => {
          if (error.code === 'ENOENT') return Buffer.alloc(0);
          throw error;
        });
        if (!generated.equals(shipped)) stale.push(`${name}/ui/${file}`);
      }
    }
    if (stale.length) {
      throw new Error(`Official plugin UI assets are stale:\n- ${stale.join('\n- ')}\nRun: pnpm --dir vue-app build:plugin-ui`);
    }
  } finally {
    await rm(outputPackages, { recursive: true, force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const check = args.includes('--check');
  const requested = args.filter(arg => arg !== '--check');
  if (check) await verifyPluginUI(requested);
  else { await buildPluginUI(requested); }
}
