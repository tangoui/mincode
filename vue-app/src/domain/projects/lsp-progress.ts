// Language-server progress for the status bar: LSP work-done progress
// ($/progress begin/report/end), the host's own baseline file scan and the
// starting phase. Pure and bounded: server text is untrusted and only shown
// as plain text.

export type WorkDoneProgress =
  | { token: string; kind: "begin"; title: string; message?: string; percentage?: number }
  | { token: string; kind: "report"; message?: string; percentage?: number }
  | { token: string; kind: "end" };

export interface LspProgressToken {
  title: string;
  message?: string;
  percentage?: number;
}

export interface LspServerProgressState {
  name: string;
  phase?: "starting" | "initializing" | "ready" | "failed" | "stopped";
  scanning: boolean;
  tokens: ReadonlyMap<string, LspProgressToken>;
}

export type LspProgressSummary =
  | { kind: "progress"; name: string; title: string; message?: string; percentage?: number; busy: number }
  | { kind: "scanning"; name: string; busy: number }
  | { kind: "starting"; name: string; busy: number };

const MAX_TITLE = 80;
const MAX_MESSAGE = 160;
// A server can open tokens without ever ending them; keep the map bounded.
const MAX_TOKENS = 16;

function text(value: unknown, max: number): string | undefined {
  if (typeof value !== "string") return undefined;
  // eslint-disable-next-line no-control-regex -- strip control characters from untrusted server text
  const clean = value.replace(/[\u0000-\u001f\u007f-\u009f]+/g, " ").trim();
  return clean ? clean.slice(0, max) : undefined;
}

function percentage(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 && value <= 100 ? Math.round(value) : undefined;
}

function withOptional<T extends object>(base: T, extra: { message?: string; percentage?: number }) {
  return {
    ...base,
    ...(extra.message !== undefined ? { message: extra.message } : {}),
    ...(extra.percentage !== undefined ? { percentage: extra.percentage } : {}),
  };
}

/** Validates `$/progress` params; undefined for anything that is not work-done progress. */
export function parseWorkDoneProgress(params: unknown): WorkDoneProgress | undefined {
  if (!params || typeof params !== "object") return undefined;
  const { token: rawToken, value } = params as { token?: unknown; value?: unknown };
  const token = typeof rawToken === "string" ? rawToken.slice(0, 128) : typeof rawToken === "number" && Number.isFinite(rawToken) ? String(rawToken) : undefined;
  if (!token || !value || typeof value !== "object") return undefined;
  const raw = value as { kind?: unknown; title?: unknown; message?: unknown; percentage?: unknown };
  const extra = { message: text(raw.message, MAX_MESSAGE), percentage: percentage(raw.percentage) };
  switch (raw.kind) {
    case "begin":
      return withOptional({ token, kind: "begin" as const, title: text(raw.title, MAX_TITLE) ?? "" }, extra);
    case "report":
      return withOptional({ token, kind: "report" as const }, extra);
    case "end":
      return { token, kind: "end" };
    default:
      return undefined;
  }
}

/** Returns a new token map with the progress applied (begin adds, report updates, end removes). */
export function applyWorkDoneProgress(
  tokens: ReadonlyMap<string, LspProgressToken>,
  progress: WorkDoneProgress,
): Map<string, LspProgressToken> {
  const next = new Map(tokens);
  if (progress.kind === "end") {
    next.delete(progress.token);
  } else if (progress.kind === "begin") {
    if (!next.has(progress.token) && next.size >= MAX_TOKENS) return next;
    next.set(progress.token, withOptional({ title: progress.title }, progress));
  } else {
    const current = next.get(progress.token);
    // The backend keeps only the latest message per token within a short
    // window, so a begin may arrive as its first report: start it untitled.
    if (!current && next.size >= MAX_TOKENS) return next;
    next.set(progress.token, {
      ...(current ?? { title: "" }),
      ...(progress.message !== undefined ? { message: progress.message } : {}),
      ...(progress.percentage !== undefined ? { percentage: progress.percentage } : {}),
    });
  }
  return next;
}

/**
 * What the status bar shows: reported progress first (the least advanced
 * token, so it ends when everything is ready), then a running file scan, then
 * a server still starting. `busy` counts every busy entry.
 */
export function pickLspProgress(servers: readonly LspServerProgressState[]): LspProgressSummary | undefined {
  let progress: Extract<LspProgressSummary, { kind: "progress" }> | undefined;
  let scanning: string | undefined;
  let starting: string | undefined;
  let busy = 0;
  for (const server of servers) {
    for (const token of server.tokens.values()) {
      busy += 1;
      const rank = token.percentage ?? -1;
      if (!progress || rank < (progress.percentage ?? -1)) {
        progress = { kind: "progress", name: server.name, title: token.title || server.name, busy: 0,
          ...(token.message !== undefined ? { message: token.message } : {}),
          ...(token.percentage !== undefined ? { percentage: token.percentage } : {}) };
      }
    }
    if (server.scanning) {
      busy += 1;
      scanning ??= server.name;
    }
    if (server.phase === "starting" || server.phase === "initializing") {
      busy += 1;
      starting ??= server.name;
    }
  }
  if (progress) return { ...progress, busy };
  if (scanning !== undefined) return { kind: "scanning", name: scanning, busy };
  if (starting !== undefined) return { kind: "starting", name: starting, busy };
  return undefined;
}
