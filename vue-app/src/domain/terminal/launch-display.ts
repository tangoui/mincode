// eslint-disable-next-line no-control-regex -- Render control characters visibly in launch confirmations.
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/g;
const SHELL_SAFE_ARG = /^[A-Za-z0-9_@%+=:,./-]+$/;

export function visibleTerminalText(value: string) {
  return value.replace(UNSAFE_TEXT, (char) => `\\u{${char.codePointAt(0)!.toString(16)}}`);
}

/** Quotes one argument for display (POSIX style); never used to build a command line. */
export function quoteTerminalArg(arg: string) {
  const visible = visibleTerminalText(arg);
  if (visible && SHELL_SAFE_ARG.test(visible)) return visible;
  return `'${visible.replace(/'/g, `'\\''`)}'`;
}

/** Display string of an argv (for the confirmation dialog and the pane tooltip). */
export function formatTerminalArgv(argv: readonly string[]) {
  return argv.map(quoteTerminalArg).join(" ");
}

/** Sorted `NAME=value` lines for display. */
export function formatTerminalEnv(env: Readonly<Record<string, string>>) {
  return Object.keys(env)
    .sort()
    .map((name) => `${name}=${visibleTerminalText(env[name] ?? "")}`);
}
