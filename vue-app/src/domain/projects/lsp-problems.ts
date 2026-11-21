import type { LspDiagnostic } from "@/domain/projects/lsp-protocol";

/** Files kept per project; diagnostics for further files are counted as truncated. */
export const MAX_PROBLEM_FILES = 5000;
/** Diagnostics kept per file and server. */
export const MAX_PROBLEMS_PER_FILE = 500;

export type ProblemSeverity = 1 | 2 | 3 | 4;

export interface ProblemEntry {
  serverId: string;
  severity: ProblemSeverity;
  message: string;
  source?: string;
  code?: string;
  /** Zero-based LSP position (UTF-16 columns). */
  line: number;
  character: number;
}

export interface ProblemFile {
  uri: string;
  /** Root-relative, slash-separated path for display. */
  path: string;
  errors: number;
  warnings: number;
  others: number;
  entries: ProblemEntry[];
}

export interface ProblemSummary {
  files: ProblemFile[];
  errors: number;
  warnings: number;
  others: number;
  truncated: boolean;
}

/** root → uri → serverId → diagnostics. */
export interface ProblemRegistry {
  roots: Map<string, Map<string, Map<string, LspDiagnostic[]>>>;
  truncatedRoots: Set<string>;
}

export function createProblemRegistry(): ProblemRegistry {
  return { roots: new Map(), truncatedRoots: new Set() };
}

function severityOf(diagnostic: LspDiagnostic): ProblemSeverity {
  // The editor markers treat a missing severity as a warning; stay consistent.
  return diagnostic.severity === 1 || diagnostic.severity === 2 || diagnostic.severity === 3 || diagnostic.severity === 4
    ? diagnostic.severity
    : 2;
}

/** Replaces one server's diagnostics for one file. Returns whether anything changed. */
export function setProblems(
  registry: ProblemRegistry,
  root: string,
  serverId: string,
  uri: string,
  diagnostics: readonly LspDiagnostic[],
): boolean {
  let files = registry.roots.get(root);
  const servers = files?.get(uri);
  if (!diagnostics.length) {
    if (!servers?.delete(serverId)) return false;
    if (!servers.size) files!.delete(uri);
    if (!files!.size) registry.roots.delete(root);
    return true;
  }
  if (!files) {
    files = new Map();
    registry.roots.set(root, files);
  }
  if (!servers && files.size >= MAX_PROBLEM_FILES) {
    registry.truncatedRoots.add(root);
    return false;
  }
  const kept = diagnostics.slice(0, MAX_PROBLEMS_PER_FILE);
  if (servers) servers.set(serverId, kept);
  else files.set(uri, new Map([[serverId, kept]]));
  return true;
}

export function problemsFor(registry: ProblemRegistry, root: string, serverId: string, uri: string): readonly LspDiagnostic[] {
  return registry.roots.get(root)?.get(uri)?.get(serverId) ?? [];
}

/** Drops a stopped server's diagnostics. Returns whether anything changed. */
export function clearServerProblems(registry: ProblemRegistry, root: string, serverId: string): boolean {
  const files = registry.roots.get(root);
  if (!files) return false;
  let changed = false;
  for (const [uri, servers] of files) {
    if (servers.delete(serverId)) changed = true;
    if (!servers.size) files.delete(uri);
  }
  if (!files.size) {
    registry.roots.delete(root);
    registry.truncatedRoots.delete(root);
  }
  return changed;
}

/** Decoded, slash-separated path of a file:// URI ("C:/x" on Windows); undefined for other URIs. */
export function fileUriPath(uri: string): string | undefined {
  let url: URL;
  try {
    url = new URL(uri);
  } catch {
    return undefined;
  }
  if (url.protocol !== "file:") return undefined;
  let path: string;
  try {
    path = decodeURIComponent(url.pathname);
  } catch {
    return undefined;
  }
  return /^\/[A-Za-z]:\//.test(path) ? path.slice(1) : path;
}

/** Root-relative display path of a file:// URI; the decoded path when outside root. */
export function problemPath(uri: string, root: string): string {
  const path = fileUriPath(uri);
  if (path === undefined) return uri;
  const normalizedRoot = root.replace(/\\/g, "/").replace(/\/+$/, "");
  const windows = /^[A-Za-z]:/.test(normalizedRoot);
  const comparablePath = windows ? path.toLowerCase() : path;
  const comparableRoot = windows ? normalizedRoot.toLowerCase() : normalizedRoot;
  return comparablePath.startsWith(`${comparableRoot}/`) ? path.slice(normalizedRoot.length + 1) : path;
}

/** Files ordered by error count, then path; entries by severity, then position. */
export function summarizeProblems(registry: ProblemRegistry, root: string): ProblemSummary {
  const summary: ProblemSummary = { files: [], errors: 0, warnings: 0, others: 0, truncated: registry.truncatedRoots.has(root) };
  for (const [uri, servers] of registry.roots.get(root) ?? []) {
    const file: ProblemFile = { uri, path: problemPath(uri, root), errors: 0, warnings: 0, others: 0, entries: [] };
    for (const [serverId, diagnostics] of servers) {
      for (const diagnostic of diagnostics) {
        const severity = severityOf(diagnostic);
        if (severity === 1) file.errors++;
        else if (severity === 2) file.warnings++;
        else file.others++;
        file.entries.push({
          serverId,
          severity,
          message: diagnostic.message,
          ...(diagnostic.source ? { source: String(diagnostic.source) } : {}),
          ...(diagnostic.code != null ? { code: String(diagnostic.code) } : {}),
          line: diagnostic.range.start.line,
          character: diagnostic.range.start.character,
        });
      }
    }
    file.entries.sort((a, b) => a.severity - b.severity || a.line - b.line || a.character - b.character);
    summary.errors += file.errors;
    summary.warnings += file.warnings;
    summary.others += file.others;
    summary.files.push(file);
  }
  summary.files.sort((a, b) => b.errors - a.errors || b.warnings - a.warnings || a.path.localeCompare(b.path));
  return summary;
}
