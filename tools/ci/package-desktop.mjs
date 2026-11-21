import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const project = fileURLToPath(new URL('../..', import.meta.url));
const result = spawnSync('tan', [
  'package', '-C', project, '-o', 'dist/desktop', ...process.argv.slice(2),
], {
  cwd: project,
  stdio: 'inherit',
});

if (result.error) throw result.error;
process.exitCode = result.status ?? 1;
