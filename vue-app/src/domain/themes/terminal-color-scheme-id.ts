/**
 * Syntax of a contributed terminal colour scheme id `<pluginId>.<name>`
 * (contributes.terminalColorSchemes). Kept apart from the validator so the
 * terminal palette registry (services/terminal-theme.ts, entry chunk) does not
 * pull in the contribution contract.
 */
export const TERMINAL_COLOR_SCHEMES_CONTRIBUTION_POINT = "terminalColorSchemes";

const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;

/**
 * True for a syntactically valid plugin scheme id `<pluginId>.<name>` (split on
 * the last dot). Used to keep a stored selection whose plugin is not installed
 * (the terminal falls back to the default palette until it is).
 */
export function isPluginTerminalColorSchemeId(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const index = value.lastIndexOf(".");
  if (index <= 0) return false;
  return PLUGIN_ID_PATTERN.test(value.slice(0, index)) && NAME_PATTERN.test(value.slice(index + 1));
}
