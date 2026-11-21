import { execFile } from 'node:child_process';
import { existsSync, readdirSync } from 'node:fs';
import { chmod, cp, mkdir, mkdtemp, readFile, readdir, rename, rm, rmdir, stat, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { promisify } from 'node:util';

const exec = promisify(execFile);
const packages = resolve(dirname(fileURLToPath(import.meta.url)), '../../official-plugins');
// Every official plugin with a Go backend; declarative (data-only) packages have none.
export const goPlugins = readdirSync(packages, { withFileTypes: true })
  .filter(entry => entry.isDirectory() && existsSync(resolve(packages, entry.name, 'backend', 'go.mod')))
  .map(entry => entry.name)
  .sort();

// Optional `backend/build-tags`: Go build tags the plugin always needs (for example
// `nodynamic`, which stops a dependency from loading a system library at runtime).
export async function goBuildTags(backend) {
  const text = await readFile(resolve(backend, 'build-tags'), 'utf8').catch(error => {
    if (error.code === 'ENOENT') return '';
    throw error;
  });
  const tags = text.split(/[\s,]+/).filter(Boolean);
  const invalid = tags.find(tag => !/^[A-Za-z0-9_.]+$/.test(tag));
  if (invalid) throw new Error(`Invalid Go build tag "${invalid}" in ${resolve(backend, 'build-tags')}`);
  return tags.length ? [`-tags=${tags.join(',')}`] : [];
}

// Optional `backend/cgo` marker: the plugin links C code (for example the prebuilt
// libduckdb) and is built with CGO_ENABLED=1; every other plugin stays pure Go
// (CGO_ENABLED=0). Non-comment lines list the GOOS/GOARCH targets the C
// dependency supports; an empty list means any official target.
export async function goCgo(backend) {
  const text = await readFile(resolve(backend, 'cgo'), 'utf8').catch(error => {
    if (error.code === 'ENOENT') return null;
    throw error;
  });
  if (text === null) return null;
  const targets = text.split('\n').map(line => line.replace(/#.*/, '').trim()).filter(Boolean)
    .flatMap(line => line.split(/[\s,]+/));
  const invalid = targets.find(target => !/^(darwin|linux|windows)\/(amd64|arm64)$/.test(target));
  if (invalid) throw new Error(`Invalid cgo target "${invalid}" in ${resolve(backend, 'cgo')}`);
  return { targets };
}

// The Go environment for one plugin build. cgo plugins build for the host target
// by default; a cross target needs a matching C toolchain in CC, so without one the
// build fails here with an explanation instead of later with a linker error (or,
// worse, a binary built without the C code).
export async function goCgoEnv(backend, target, env = process.env) {
  const cgo = await goCgo(backend);
  if (!cgo) return { CGO_ENABLED: '0' };
  const name = `${target.GOOS}/${target.GOARCH}`;
  if (cgo.targets.length && !cgo.targets.includes(name)) {
    throw new Error(`${backend} needs cgo and supports only ${cgo.targets.join(', ')}; cannot build for ${name}`);
  }
  const { stdout } = await exec('go', ['env', '-json', 'GOHOSTOS', 'GOHOSTARCH'], { env });
  const host = JSON.parse(stdout);
  if ((host.GOHOSTOS !== target.GOOS || host.GOHOSTARCH !== target.GOARCH) && !env.CC) {
    throw new Error(`${backend} needs cgo: cross-building for ${name} on ${host.GOHOSTOS}/${host.GOHOSTARCH} requires a C cross toolchain. Set CC (and CXX) to a ${name} compiler, or build on a ${name} host.`);
  }
  // cgo on Windows links with a MinGW toolchain, whose runtime (libwinpthread-1.dll,
  // libgcc_s, libstdc++) is not on users' machines: link it into the executable.
  if (target.GOOS === 'windows') {
    return { CGO_ENABLED: '1', CGO_LDFLAGS: [env.CGO_LDFLAGS, '-static'].filter(Boolean).join(' ') };
  }
  return { CGO_ENABLED: '1' };
}

// Optional `scripts/fetch-assets.mjs`: per-target native libraries and data files that
// are too large for git (for example the ONNX Runtime library and models of `ocr`). It
// exports `assetDirs` (the package directories it owns: lib/ and/or models/ only)
// and `prepareAssets({ GOOS, GOARCH }, pluginDir, { env })`, which downloads pinned,
// checksummed files into a cache and replaces those directories with the target's files.
// Every build of the plugin runs it, so a package never carries another target's library.
export const GO_PLUGIN_ASSET_DIRS = ['lib', 'models'];

export async function goPluginAssets(name, sourcePackages = packages) {
  const script = resolve(sourcePackages, name, 'scripts', 'fetch-assets.mjs');
  if (!existsSync(script)) return null;
  const module = await import(pathToFileURL(script).href);
  // The hook replaces these directories wholesale, so it may only own the generated
  // lib/ and models/ (both gitignored), never a committed directory such as assets/.
  if (!Array.isArray(module.assetDirs) || typeof module.prepareAssets !== 'function' || !module.assetDirs.length
    || module.assetDirs.some(dir => !GO_PLUGIN_ASSET_DIRS.includes(dir)) || new Set(module.assetDirs).size !== module.assetDirs.length) {
    throw new Error(`${script} must export prepareAssets() and assetDirs limited to ${GO_PLUGIN_ASSET_DIRS.join(', ')}`);
  }
  return module;
}

export async function goTarget(env = process.env) {
  const { stdout } = await exec('go', ['env', '-json', 'GOOS', 'GOARCH'], { env });
  const { GOOS, GOARCH } = JSON.parse(stdout);
  if (!['darwin', 'linux', 'windows'].includes(GOOS) || !['amd64', 'arm64'].includes(GOARCH)) {
    throw new Error(`Unsupported official Go plugin target: ${GOOS}/${GOARCH}`);
  }
  return { GOOS, GOARCH };
}

// Build every invocation. File existence is not evidence of the correct architecture
// or current module sources; Go's own content-addressed cache handles reuse.
export async function buildGoPlugins(requested = goPlugins, { outputPackages = packages, env = process.env } = {}) {
  const names = [...new Set(requested)].filter(name => goPlugins.includes(name));
  if (!names.length) return names;
  const target = await goTarget(env);
  for (const name of names) {
    const source = resolve(packages, name);
    const manifest = JSON.parse(await readFile(resolve(source, 'plugin.json'), 'utf8'));
    const entry = manifest.runtime.entrypoints[target.GOOS];
    const output = resolve(outputPackages, name);
    await mkdir(output, { recursive: true });
    const temp = await mkdtemp(resolve(output, '.go-build-'));
    try {
      console.log(`Building ${name} for ${target.GOOS}/${target.GOARCH}${existsSync(resolve(source, 'backend', 'cgo')) ? ' (cgo)' : ''}`);
      const tags = await goBuildTags(resolve(source, 'backend'));
      const cgo = await goCgoEnv(resolve(source, 'backend'), target, env);
      await exec('go', ['build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', ...tags, '-o', resolve(temp, 'plugin'), '.'], {
        cwd: resolve(source, 'backend'),
        env: { ...env, ...target, ...cgo, GOWORK: 'off' },
        timeout: 300000,
        maxBuffer: 4 * 1024 * 1024,
      });
      await chmod(resolve(temp, 'plugin'), 0o755);
      await mkdir(dirname(resolve(output, entry)), { recursive: true });
      await rename(resolve(temp, 'plugin'), resolve(output, entry));
      const assets = await goPluginAssets(name);
      if (assets) await assets.prepareAssets(target, output, { env });
    } finally {
      // The successful rename leaves only the empty temp directory. Remove it
      // non-recursively so Windows safe-delete does not invoke the trash path
      // for every plugin; retain non-empty failed stages for diagnostics.
      await rmdir(temp).catch(error => {
        if (error.code !== 'ENOENT' && error.code !== 'ENOTEMPTY' && error.code !== 'EEXIST') throw error;
      });
    }
  }
  return names;
}

// A clean, target-specific installable package, never a copy of backend/go.mod,
// dependencies, UI source, or the retired Node runtime. Refuse existing output.
export async function packageGoPlugin(name, destination, { env = process.env } = {}) {
  if (!goPlugins.includes(name)) throw new Error(`Unknown Go plugin: ${name}`);
  const target = await goTarget(env);
  await mkdir(destination, { recursive: false });
  try {
    const source = resolve(packages, name);
    const manifest = JSON.parse(await readFile(resolve(source, 'plugin.json'), 'utf8'));
    const { buildPluginUI } = await import('./build-plugin-ui.mjs');
    const stage = await mkdtemp(resolve(destination, '.stage-'));
    try {
      await buildPluginUI([name], { outputPackages: stage, env });
      manifest.platforms = [target.GOOS];
      manifest.runtime.entry = manifest.runtime.entrypoints[target.GOOS];
      manifest.runtime.entrypoints = { [target.GOOS]: manifest.runtime.entry };

      await writeFile(resolve(destination, 'plugin.json'), JSON.stringify(manifest, null, 2) + '\n');
      const binary = resolve(destination, manifest.runtime.entry);
      // Explicitly prepare the parent for filesystem adapters that do not create it during cp.
      await mkdir(dirname(binary), { recursive: true });
      await cp(resolve(stage, name, manifest.runtime.entry), binary, { recursive: true });
      // Driver-only plugins (no ui-src) have no UI to ship.
      if (existsSync(resolve(source, 'ui-src'))) await cp(resolve(stage, name, 'ui'), resolve(destination, 'ui'), { recursive: true });
      await cp(resolve(source, 'assets'), resolve(destination, 'assets'), { recursive: true })
        .catch(error => { if (error.code !== 'ENOENT') throw error; });
      // Target assets were placed into the stage by the build above.
      for (const dir of (await goPluginAssets(name))?.assetDirs ?? []) {
        await cp(resolve(stage, name, dir), resolve(destination, dir), { recursive: true });
      }

      let bytes = 0, files = 0;
      async function measure(path) {
        for (const entry of await readdir(path, { withFileTypes: true })) {
          const child = resolve(path, entry.name);
          if (entry.isDirectory()) await measure(child);
          else { files++; bytes += (await stat(child)).size; }
        }
      }
      await rm(stage, { recursive: true, force: true });
      await measure(destination);
      if (bytes > 64 * 1024 * 1024 || files > 2000) throw new Error('Go plugin exceeds host package limits');
    } finally {
      await rm(stage, { recursive: true, force: true });
    }
  } catch (error) {
    await rm(destination, { recursive: true, force: true });
    throw error;
  }
}

async function main(args) {
  if (args[0] === '--package' && args.length === 3) await packageGoPlugin(args[1], resolve(args[2]));
  else if (args.some(arg => !goPlugins.includes(arg))) throw new Error(`Usage: build-go-plugins.mjs [<go-plugin>] | --package <go-plugin> <new-directory>; known Go plugins: ${goPlugins.join(', ')}`);
  else await buildGoPlugins(args.length ? args : goPlugins);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  // Not a top-level await: packaging imports build-plugin-ui.mjs, which imports this
  // module back and would wait forever for its evaluation to finish.
  main(process.argv.slice(2)).catch(error => {
    console.error(error);
    process.exitCode = 1;
  });
}
