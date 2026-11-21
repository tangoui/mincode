/**
 * Project-relative breakpoint keys. Breakpoints are stored under a `/`
 * separated path relative to the project root, so a project that moves or is
 * opened with a different drive-letter case keeps its breakpoints; the
 * absolute path the debug adapter needs is rebuilt from the root's spelling.
 */

const WINDOWS_DRIVE_ROOT = /^[A-Za-z]:$/;

function slashed(path: string): string {
  return path.replace(/\\/g, "/");
}

function isAbsolute(path: string): boolean {
  return path.startsWith("/") || /^[A-Za-z]:\//.test(path);
}

/** `/`-separated root without a trailing separator ("/" stays "", "C:/" stays "C:"). */
function rootPrefix(projectRoot: string): string {
  return slashed(projectRoot).replace(/\/+$/, "");
}

/**
 * The `/`-separated path of absPath relative to projectRoot, or null when it
 * is not a file inside the root. The prefix comparison ignores case when
 * caseInsensitive (macOS and Windows file systems); the key keeps absPath's
 * spelling.
 */
export function toRelativeKey(projectRoot: string, absPath: string, caseInsensitive: boolean): string | null {
  const root = rootPrefix(projectRoot);
  const path = slashed(absPath);
  if (!projectRoot || !isAbsolute(path) || !isAbsolute(`${root}/`)) return null;
  const prefix = `${root}/`;
  const comparable = (value: string) => (caseInsensitive ? value.toLowerCase() : value);
  if (!comparable(path).startsWith(comparable(prefix))) return null;
  const segments = path.slice(prefix.length).split("/").filter((segment) => segment !== "" && segment !== ".");
  if (segments.length === 0 || segments.includes("..")) return null;
  return segments.join("/");
}

/**
 * The absolute path of a relative key, joined in the project root's own
 * spelling and separator (`\` for a Windows root written with backslashes).
 */
export function toSourcePath(projectRoot: string, relativeKey: string): string {
  const root = rootPrefix(projectRoot);
  const backslash = projectRoot.includes("\\") || (WINDOWS_DRIVE_ROOT.test(root) && !projectRoot.includes("/"));
  const separator = backslash ? "\\" : "/";
  const base = backslash ? root.replace(/\//g, "\\") : root;
  const key = relativeKey.split("/").filter(Boolean).join(separator);
  return `${base}${separator}${key}`;
}

/**
 * A project root as a persisted map key: trailing separators trimmed, except
 * for a file-system root ("/", "C:\").
 */
export function projectRootKey(projectRoot: string): string {
  const trimmed = projectRoot.replace(/[\\/]+$/, "");
  if (trimmed === "") return projectRoot.slice(0, 1);
  if (WINDOWS_DRIVE_ROOT.test(trimmed)) return projectRoot.slice(0, 3);
  return trimmed;
}

/**
 * The key of keys equal to key, or (when caseInsensitive) the first one
 * equal ignoring case; otherwise key itself. Keeps one map entry per path on
 * case-insensitive file systems whatever spelling a caller uses.
 */
export function existingKey(keys: Iterable<string>, key: string, caseInsensitive: boolean): string {
  if (!caseInsensitive) return key;
  const lower = key.toLowerCase();
  let match: string | undefined;
  for (const candidate of keys) {
    if (candidate === key) return key;
    if (match === undefined && candidate.toLowerCase() === lower) match = candidate;
  }
  return match ?? key;
}
