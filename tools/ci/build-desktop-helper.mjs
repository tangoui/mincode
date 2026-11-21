import { spawnSync } from 'node:child_process';
import { isAbsolute } from 'node:path';

const { TAN_TARGET_OS: platform, TAN_TARGET_ARCH: arch, TAN_HELPER_OUTPUT: output } = process.env;
if (!/^(darwin|linux|windows)$/.test(platform) || !/^(arm64|amd64|universal)$/.test(arch) || !output || !isAbsolute(output)) {
  throw new Error('Run this helper through tan build or tan package');
}

const result = spawnSync('bash', ['tools/ci/build-plugin-dev-cli.sh', platform, arch, output.replaceAll('\\', '/')], { stdio: 'inherit' });
if (result.error) throw result.error;
process.exitCode = result.status ?? 1;
