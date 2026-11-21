import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const packages = resolve(repo, 'official-plugins');
const templates = resolve(repo, 'tools/plugin-templates');
// local-process-node copies the runtime from @mincodeai/plugin-sdk at build time (scripts/copy-runtime.mjs).
const nodeTemplates = ['contributions-process-showcase', 'host-api-showcase'];

export async function buildPluginRuntime(requested = [], {
  outputPackages = packages,
  outputTemplates = templates,
  check = false,
} = {}) {
  const available = [];
  const nodePackages = [];
  for (const entry of await readdir(packages, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const manifest = await readFile(resolve(packages, entry.name, 'plugin.json'), 'utf8')
      .then(value => JSON.parse(value))
      .catch(error => {
        if (error.code === 'ENOENT') return null;
        throw error;
      });
    if (manifest?.runtime?.type === 'process') {
      available.push(entry.name);
      const entries = [manifest.runtime.entry, ...Object.values(manifest.runtime.entrypoints ?? {})].filter(Boolean);
      if (entries.length && entries.every(path => /\.(?:mjs|cjs|js)$/.test(path))) nodePackages.push(entry.name);
    }
  }
  // Only repository-owned packages and this explicit template are managed.
  for (const name of nodeTemplates) {
    const manifest = JSON.parse(await readFile(resolve(templates, name, 'plugin.json'), 'utf8'));
    if (manifest.runtime?.type !== 'process') throw new Error(`Invalid Node template: ${name}`);
  }
  const selected = requested.length ? [...new Set(requested)] : [...nodePackages.sort(), ...nodeTemplates];
  for (const name of selected) {
    if (!available.includes(name) && !nodeTemplates.includes(name)) throw new Error(`Unknown process plugin: ${name}`);
  }
  const names = selected.filter(name => nodePackages.includes(name) || nodeTemplates.includes(name));
  const source = await readFile(resolve(repo, 'packages/plugin-sdk/node/runtime.mjs'));
  // Templates also get the type declarations for editor completion; official packages are plain JS.
  const declarations = await readFile(resolve(repo, 'packages/plugin-sdk/node/runtime.d.mts'));
  for (const name of names) {
    const directory = resolve(nodeTemplates.includes(name) ? outputTemplates : outputPackages, name, 'lib');
    const files = [['runtime.mjs', source], ...(nodeTemplates.includes(name) ? [['runtime.d.mts', declarations]] : [])];
    for (const [file, content] of files) {
      const target = resolve(directory, file);
      if (check) {
        const current = await readFile(target).catch(error => {
          if (error.code === 'ENOENT') return Buffer.alloc(0);
          throw error;
        });
        if (!content.equals(current)) throw new Error(`Stale runtime: ${name} (${file}). Run pnpm --dir vue-app build:plugin-runtime`);
      } else {
        await mkdir(directory, { recursive: true });
        await writeFile(target, content);
      }
    }
  }
  return names;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await buildPluginRuntime(process.argv.slice(2).filter(arg => arg !== '--check'), {
    check: process.argv.includes('--check'),
  });
}
