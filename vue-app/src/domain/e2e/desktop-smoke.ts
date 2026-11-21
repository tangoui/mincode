/**
 * Pure rules for the desktop smoke harness (tools/e2e/desktop-smoke.mjs): the
 * scenario config it hands to an `-tags e2e` build, the report the WebView
 * sends back, and output matching that cannot be satisfied by local echo.
 */

export const SMOKE_SCENARIOS = ["terminal", "ssh", "database", "ai", "ai-sidebar", "terminal-attention", "sql-safety", "sftp-transfers", "perf-database", "perf-http", "perf-ai", "perf-remote", "perf-sessions", "perf-pages", "terminal-tabs", "terminal-size"] as const;
export type SmokeScenario = (typeof SMOKE_SCENARIOS)[number];

export interface SmokeConfig {
  scenarios: SmokeScenario[];
  /** Random per run; part of every marker so output cannot come from an older run. */
  nonce: string;
  timeoutMs: number;
  ssh?: { host: string; port: number; username: string; password: string };
  database?: { filePath: string };
  sqlSafety?: { filePath: string };
  /** Upload source, download destination and the exact byte size (needs the ssh fixture). */
  sftpTransfers?: { uploadPath: string; downloadPath: string; size: number };
  /** Disposable Go module; gopls must be installed on the harness PATH. */
  lsp?: { projectRoot: string };
  largeText?: { filePath: string };
  perfDatabase?: { filePath: string; table: string; rows: number; columns: number; extraTables?: number; redis?: { port: number; keys: number } };
  perfAi?: { turns: number };
  perfPages?: { sshProfiles: number; browserUrls: string[] };
  perfGit?: { repoRoot: string; modified: number; untracked: number; historyCommits?: number };
  perfSessions?: { projects: number; sessionsPerProject: number; longTitle: string; longMessages: number; expandProjects?: number };
  perfRemote?: {
    remoteDir: string; remoteFiles: number; localLines: number; sshLines: number; uploadFolder?: string; uploadFiles?: number; only?: "typing";
    /** A second e2e-sshd (same host/user/password as `ssh`) delaying its output, for the predictive echo block. */
    laggySsh?: { port: number };
  };
  perfHttp?: { baseUrl: string; bodies: { label: string; path: string; marker: string }[] };
  perf?: { projectRoot: string; fileCount: number; bigFile: string; bigFileLines: number; bigPlainFile?: string; smallFiles: string[]; terminalLines: number; only?: "pages" };
  /** A repository with one staged file and an OpenAI-compatible endpoint: silent, then a 45 s reasoning model, then silent. */
  gitAiCommit?: { repoRoot: string; baseUrl: string; apiKey: string; model: string; answer: string };
  browser?: {
    firstUrl: string;
    secondUrl: string;
    errorUrl: string;
    /** macOS browser-use pages (popup/opener, dialogs, forms, article, files); absent on Windows runs of older harnesses. */
    use?: { openerUrl: string; formsUrl: string; articleUrl: string; filesUrl: string; downloadName: string };
  };
  ai?: { baseUrl: string; model: string; apiKey: string; expectedText: string; slowPrompt: string };
}

export type SmokeStatus = "passed" | "failed" | "skipped";

export interface SmokeResult {
  scenario: SmokeScenario;
  status: SmokeStatus;
  durationMs: number;
  /** Completed steps, in order, so a failure shows how far the chain got. */
  steps: string[];
  error?: string;
}

export interface SmokeReport {
  nonce: string;
  startedAt: string;
  finishedAt: string;
  userAgent: string;
  results: SmokeResult[];
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function text(value: unknown, field: string, max = 4096): string {
  if (typeof value !== "string" || !value || value.length > max) throw new Error(`invalid smoke config: ${field}`);
  return value;
}

function port(value: unknown, field: string): number {
  if (!Number.isInteger(value) || (value as number) < 1 || (value as number) > 65535) {
    throw new Error(`invalid smoke config: ${field}`);
  }
  return value as number;
}

function loopbackUrl(value: unknown, field: string): string {
  const raw = text(value, field);
  const url = new URL(raw);
  if (url.protocol !== "http:" || (url.hostname !== "127.0.0.1" && url.hostname !== "localhost")) {
    throw new Error(`invalid smoke config: ${field} must be a loopback http URL`);
  }
  return raw;
}

/** Validates the harness config; scenarios without their fixture are rejected. */
export function parseSmokeConfig(value: unknown): SmokeConfig {
  const raw = record(value);
  if (!raw) throw new Error("invalid smoke config");
  const scenarios = Array.isArray(raw.scenarios) ? raw.scenarios : [];
  const known = new Set<string>(SMOKE_SCENARIOS);
  if (!scenarios.length || scenarios.some((item) => typeof item !== "string" || !known.has(item))) {
    throw new Error("invalid smoke config: scenarios");
  }
  const nonce = text(raw.nonce, "nonce", 64);
  if (!/^[A-Za-z0-9]+$/.test(nonce)) throw new Error("invalid smoke config: nonce");
  const timeoutMs = Number.isInteger(raw.timeoutMs) && (raw.timeoutMs as number) > 0 ? raw.timeoutMs as number : 30_000;
  const config: SmokeConfig = { scenarios: [...new Set(scenarios as SmokeScenario[])], nonce, timeoutMs };
  if (config.scenarios.includes("perf-database")) {
    const db = record(raw.perfDatabase);
    config.perfDatabase = {
      filePath: text(db?.filePath, "perfDatabase.filePath"), table: text(db?.table, "perfDatabase.table"),
      rows: Number(db?.rows) || 0, columns: Number(db?.columns) || 0,
      ...(Number(db?.extraTables) > 0 ? { extraTables: Number(db?.extraTables) } : {}),
      ...(Number(record(db?.redis)?.port) > 0 ? { redis: { port: Number(record(db?.redis)?.port), keys: Number(record(db?.redis)?.keys) || 0 } } : {}),
    };
  }
  if (config.scenarios.includes("perf-pages")) {
    const pages = record(raw.perfPages);
    const browserUrls = Array.isArray(pages?.browserUrls) ? pages!.browserUrls.map((item, index) => text(item, `perfPages.browserUrls[${index}]`)) : [];
    config.perfPages = { sshProfiles: Number(pages?.sshProfiles) || 0, browserUrls };
  }
  if (config.scenarios.includes("perf-sessions")) {
    const sessions = record(raw.perfSessions);
    config.perfSessions = {
      projects: Number(sessions?.projects) || 0, sessionsPerProject: Number(sessions?.sessionsPerProject) || 0,
      longTitle: text(sessions?.longTitle, "perfSessions.longTitle"), longMessages: Number(sessions?.longMessages) || 0,
      ...(Number(sessions?.expandProjects) > 0 ? { expandProjects: Number(sessions?.expandProjects) } : {}),
    };
  }
  if (config.scenarios.includes("perf-remote")) {
    const remote = record(raw.perfRemote);
    config.perfRemote = {
      remoteDir: text(remote?.remoteDir, "perfRemote.remoteDir"), remoteFiles: Number(remote?.remoteFiles) || 0,
      localLines: Number(remote?.localLines) || 20000, sshLines: Number(remote?.sshLines) || 20000,
      ...(typeof remote?.uploadFolder === "string" && Number(remote?.uploadFiles) > 0 ? { uploadFolder: remote.uploadFolder, uploadFiles: Number(remote.uploadFiles) } : {}),
      ...(remote?.only === "typing" ? { only: "typing" as const } : {}),
      ...(remote?.laggySsh !== undefined ? { laggySsh: { port: port(record(remote.laggySsh)?.port, "perfRemote.laggySsh.port") } } : {}),
    };
  }
  if (config.scenarios.includes("perf-ai")) {
    config.perfAi = { turns: Number(record(raw.perfAi)?.turns) || 100 };
  }
  if (config.scenarios.includes("perf-http")) {
    const http = record(raw.perfHttp);
    const bodies = Array.isArray(http?.bodies) ? http!.bodies.map((item, index) => {
      const body = record(item);
      return { label: text(body?.label, `perfHttp.bodies[${index}].label`), path: text(body?.path, `perfHttp.bodies[${index}].path`), marker: text(body?.marker, `perfHttp.bodies[${index}].marker`) };
    }) : [];
    if (!bodies.length) throw new Error("perfHttp.bodies required");
    config.perfHttp = { baseUrl: text(http?.baseUrl, "perfHttp.baseUrl"), bodies };
  }
  if (config.scenarios.includes("ssh") || config.scenarios.includes("sftp-transfers") || config.scenarios.includes("perf-remote")) {
    const ssh = record(raw.ssh);
    config.ssh = {
      host: text(ssh?.host, "ssh.host", 255),
      port: port(ssh?.port, "ssh.port"),
      username: text(ssh?.username, "ssh.username", 255),
      password: text(ssh?.password, "ssh.password", 255),
    };
  }
  if (config.scenarios.includes("database")) {
    config.database = { filePath: text(record(raw.database)?.filePath, "database.filePath") };
  }
  if (config.scenarios.includes("sql-safety")) {
    config.sqlSafety = { filePath: text(record(raw.sqlSafety)?.filePath, "sqlSafety.filePath") };
  }
  if (config.scenarios.includes("sftp-transfers")) {
    if (!config.scenarios.includes("ssh") && !record(raw.ssh)) throw new Error("invalid smoke config: sftp-transfers needs ssh");
    const fixture = record(raw.sftpTransfers);
    const size = fixture?.size;
    if (!Number.isInteger(size) || (size as number) < 1) throw new Error("invalid smoke config: sftpTransfers.size");
    config.sftpTransfers = { uploadPath: text(fixture?.uploadPath, "sftpTransfers.uploadPath"), downloadPath: text(fixture?.downloadPath, "sftpTransfers.downloadPath"), size: size as number };
  }
  if (config.scenarios.includes("ai")) {
    const ai = record(raw.ai);
    config.ai = {
      baseUrl: loopbackUrl(ai?.baseUrl, "ai.baseUrl"),
      model: text(ai?.model, "ai.model", 128),
      apiKey: text(ai?.apiKey, "ai.apiKey", 256),
      expectedText: text(ai?.expectedText, "ai.expectedText"),
      slowPrompt: text(ai?.slowPrompt, "ai.slowPrompt"),
    };
  }
  return config;
}

/**
 * A command whose printed result differs from the command line itself, so a
 * terminal that merely echoes typed input never satisfies the expectation.
 */
export function echoProbe(nonce: string, label: string) {
  return {
    command: `echo MINCODE_${label}_$((40+2))_${nonce}`,
    expected: `MINCODE_${label}_42_${nonce}`,
  };
}

// eslint-disable-next-line no-control-regex -- stripping terminal escape sequences is the point
const ANSI_PATTERN = /\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]/g;

/** Terminal output without escape sequences or carriage returns. */
export function plainTerminalText(output: string): string {
  return output.replace(ANSI_PATTERN, "").replace(/\r/g, "");
}

export function summarizeSmoke(results: readonly SmokeResult[]) {
  return {
    passed: results.filter((result) => result.status === "passed").length,
    failed: results.filter((result) => result.status === "failed").length,
    skipped: results.filter((result) => result.status === "skipped").length,
  };
}
