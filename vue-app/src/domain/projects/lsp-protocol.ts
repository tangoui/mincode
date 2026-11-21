export type LspSyncKind = 0 | 1 | 2;

export interface LspDocumentSync {
  openClose: boolean;
  change: LspSyncKind;
  save: boolean;
  saveIncludeText: boolean;
}

function object(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

/** A process being alive is not a successful LSP initialize handshake. */
export function initializeCapabilities(result: unknown): Record<string, unknown> {
  const capabilities = object(object(result)?.capabilities);
  if (!capabilities) throw new Error("Invalid LSP initialize result: capabilities missing");
  if (capabilities.positionEncoding !== undefined && capabilities.positionEncoding !== "utf-16") {
    throw new Error("Unsupported LSP position encoding");
  }
  return capabilities;
}

export function parseDocumentSync(value: unknown): LspDocumentSync {
  const options = object(value);
  const change = options ? options.change : value;
  const kind: LspSyncKind = change === 1 || change === 2 ? change : 0;
  const save = options?.save;
  return {
    // The legacy numeric form implies open/close for a syncing server.
    openClose: options ? options.openClose === true : kind !== 0,
    change: kind,
    save: save === true || object(save) !== undefined,
    saveIncludeText: object(save)?.includeText === true,
  };
}

export interface LspDiagnosticProvider {
  identifier?: string;
  /** The server also answers workspace/diagnostic for files that are not open. */
  workspaceDiagnostics: boolean;
}

export function parseDiagnosticProvider(value: unknown): LspDiagnosticProvider | undefined {
  const options = object(value);
  if (!options) return undefined;
  return {
    ...(typeof options.identifier === "string" ? { identifier: options.identifier } : {}),
    workspaceDiagnostics: options.workspaceDiagnostics === true,
  };
}

export interface LspDiagnostic {
  range: {
    start: { line: number; character: number };
    end: { line: number; character: number };
  };
  message: string;
  severity?: number;
  source?: string;
  code?: string | number;
}

export type LspDocumentDiagnosticReport =
  | { kind: "full"; items: LspDiagnostic[]; resultId?: string }
  | { kind: "unchanged"; resultId: string };

function diagnosticPosition(value: unknown): boolean {
  const position = object(value);
  return Number.isInteger(position?.line) && Number(position?.line) >= 0 &&
    Number.isInteger(position?.character) && Number(position?.character) >= 0;
}

export function parseDocumentDiagnosticReport(value: unknown): LspDocumentDiagnosticReport | undefined {
  const report = object(value);
  if (!report) return undefined;
  if (report.kind === "unchanged" && typeof report.resultId === "string") {
    return { kind: "unchanged", resultId: report.resultId };
  }
  if (report.kind !== "full" || !Array.isArray(report.items)) return undefined;
  return {
    kind: "full", items: parseDiagnostics(report.items),
    ...(typeof report.resultId === "string" ? { resultId: report.resultId } : {}),
  };
}

/** Keeps the well-formed diagnostics of a server-supplied list. */
export function parseDiagnostics(items: unknown[]): LspDiagnostic[] {
  return items.filter((item): item is LspDiagnostic => {
    const diagnostic = object(item);
    const range = object(diagnostic?.range);
    return typeof diagnostic?.message === "string" &&
      diagnosticPosition(range?.start) && diagnosticPosition(range?.end);
  });
}

export interface LspWorkspaceDiagnosticItem {
  uri: string;
  /** Document version the report was computed for; null for files that are not open. */
  version: number | null;
  report: LspDocumentDiagnosticReport;
}

/** Bounds one workspace/diagnostic response; extra reports wait for the next pull. */
export const MAX_WORKSPACE_DIAGNOSTIC_REPORTS = 2000;

export function parseWorkspaceDiagnosticReport(value: unknown): LspWorkspaceDiagnosticItem[] | undefined {
  const report = object(value);
  if (!report || !Array.isArray(report.items)) return undefined;
  const items: LspWorkspaceDiagnosticItem[] = [];
  for (const raw of report.items) {
    if (items.length >= MAX_WORKSPACE_DIAGNOSTIC_REPORTS) break;
    const item = object(raw);
    if (typeof item?.uri !== "string" || !item.uri.startsWith("file:")) continue;
    const parsed = parseDocumentDiagnosticReport(item);
    if (!parsed) continue;
    const version = Number.isSafeInteger(item.version) ? item.version as number : null;
    items.push({ uri: item.uri, version, report: parsed });
  }
  return items;
}

export interface LspContentChange {
  text: string;
  range?: {
    start: { line: number; character: number };
    end: { line: number; character: number };
  };
}

/** Debounced incremental sync replaces the entire *last sent* document, in UTF-16. */
export function documentContentChanges(kind: LspSyncKind, previous: string, text: string): LspContentChange[] {
  if (kind === 0 || previous === text) return [];
  if (kind === 1) return [{ text }];
  const lines = previous.split(/\r\n|\r|\n/);
  return [{
    range: {
      start: { line: 0, character: 0 },
      end: { line: lines.length - 1, character: lines[lines.length - 1]?.length ?? 0 },
    },
    text,
  }];
}

/** Compare path segments, not a textual prefix; Monaco fsPath uses host separators. */
export function isPathInLspRoot(path: string, root: string): boolean {
  if (!root) return false;
  let normalizedPath = path.replace(/\\/g, "/");
  let normalizedRoot = root.replace(/\\/g, "/").replace(/\/+$/, "");
  if (/^[a-z]:/i.test(normalizedRoot) || normalizedRoot.startsWith("//")) {
    normalizedPath = normalizedPath.toLowerCase();
    normalizedRoot = normalizedRoot.toLowerCase();
  }
  if (normalizedPath.split("/").includes("..")) return false;
  return normalizedPath === normalizedRoot || normalizedPath.startsWith(`${normalizedRoot}/`);
}
