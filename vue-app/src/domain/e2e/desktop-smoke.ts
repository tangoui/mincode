/**
 * Pure rules for the desktop smoke harness (tools/e2e/desktop-smoke.mjs): the
 * scenario config it hands to an `-tags e2e` build, the report the WebView
 * sends back, and output matching that cannot be satisfied by local echo.
 */

export const SMOKE_SCENARIOS = ["terminal", "ssh", "database", "plugin", "ai", "associations", "ebook", "formatter", "ai-catalogs", "browser", "ai-prompts", "ai-sidebar", "rust-plugin", "ts-plugin", "file-templates", "lsp", "linters", "port-manager", "terminal-attention", "services", "large-text", "mail-client", "debugger", "sql-safety", "sftp-transfers", "plugin-providers", "ui-runtime", "perf", "perf-database", "perf-http", "perf-ai", "perf-remote", "laravel", "perf-sessions", "perf-git", "perf-pages", "perf-plugins", "perf-plugin-startup", "git-ai-commit", "terminal-tabs", "terminal-size"] as const;
export type SmokeScenario = (typeof SMOKE_SCENARIOS)[number];

export interface SmokeConfig {
  scenarios: SmokeScenario[];
  /** Random per run; part of every marker so output cannot come from an older run. */
  nonce: string;
  timeoutMs: number;
  ssh?: { host: string; port: number; username: string; password: string };
  database?: { filePath: string };
  /** Packaged python-debug plugin, an interpreter with debugpy, and a fixture project/script with logpoint and stop lines. */
  debugger?: { packagePath: string; python: string; projectRoot: string; program: string; logLine: number; stopLine: number };
  sqlSafety?: { filePath: string };
  /** Packaged process showcase plugin and a git repository with two staged files plus a staged .env. */
  pluginProviders?: { packagePath: string; repoRoot: string };
  /** Upload source, download destination and the exact byte size (needs the ssh fixture). */
  sftpTransfers?: { uploadPath: string; downloadPath: string; size: number };
  plugin?: { packagePath: string; pluginId: string };
  fileTemplates?: {packagePath:string; projectRoot:string; phase:"setup"|"cold"};
  /** Disposable Go module; gopls must be installed on the harness PATH. */
  lsp?: { projectRoot: string };
  largeText?: { filePath: string };
  perfDatabase?: { filePath: string; table: string; rows: number; columns: number; extraTables?: number; redis?: { port: number; keys: number } };
  perfAi?: { turns: number };
  perfPages?: { sshProfiles: number; browserUrls: string[] };
  perfPlugins?: { packages: { pluginId: string; packagePath: string; view: boolean }[] };
  perfPluginStartup?: { phase: "setup" | "cold"; packages: { pluginId: string; packagePath: string; enable: boolean }[] };
  perfGit?: { repoRoot: string; modified: number; untracked: number; historyCommits?: number };
  perfSessions?: { projects: number; sessionsPerProject: number; longTitle: string; longMessages: number; expandProjects?: number };
  perfRemote?: {
    remoteDir: string; remoteFiles: number; localLines: number; sshLines: number; uploadFolder?: string; uploadFiles?: number; only?: "typing";
    /** A second e2e-sshd (same host/user/password as `ssh`) delaying its output, for the predictive echo block. */
    laggySsh?: { port: number };
  };
  perfHttp?: { baseUrl: string; bodies: { label: string; path: string; marker: string }[] };
  perf?: { projectRoot: string; fileCount: number; bigFile: string; bigFileLines: number; bigPlainFile?: string; smallFiles: string[]; terminalLines: number; only?: "pages" };
  /** Packaged Port Manager plugin and a TCP server the harness started (its port and PID). */
  portManager?: { packagePath: string; port: number; pid: number };
  /**
   * Packaged service providers (crypto-toolkit, image-toolkit, optional ai-translator and ocr), an
   * optional built service-consumer-demo and invoice-manager (OCR consumer), image fixtures (the
   * invoice scan is a copy of the OCR plugin's committed test image), the directory output files
   * are saved into, and an optional OpenAI-compatible loopback mock for ai-translator.
   */
  services?: {
    packages: { cryptoToolkit: string; imageToolkit: string; aiTranslator?: string; consumerDemo?: string; ocr?: string; invoiceManager?: string };
    invoiceImagePath?: string;
    pngPath: string;
    exifJpegPath: string;
    outputDir: string;
    ai?: { baseUrl: string; model: string; apiKey: string; sourceText: string; expectedText: string };
  };
  /** Packaged external-linters plugin and a project with PHP and ESLint config; php must be on PATH. */
  linters?: { packagePath: string; projectRoot: string };
  /** Packaged laravel-helper and i18n-helper plugins, a synthetic Laravel project and a secret canary that must never surface. */
  laravel?: { laravelPackage: string; i18nPackage: string; projectRoot: string; canary: string };
  /** A repository with one staged file and an OpenAI-compatible endpoint: silent, then a 45 s reasoning model, then silent. */
  gitAiCommit?: { repoRoot: string; baseUrl: string; apiKey: string; model: string; answer: string };
  rustPlugin?: { packagePath: string; pluginId: string };
  associations?: { packagePath: string; torrentPath: string; ordinaryPath: string; phase: "setup" | "cold" };
  ebook?: { packagePath: string; paths: string[] };
  formatter?: { packagePath: string; imagePath: string };
  mailClient?: {packagePath:string;outputDir:string;attachmentPath:string;controlUrl:string;expectedSubject:string};
  aiCatalogs?: { packagePath: string };
  aiPrompts?: { packagePath: string };
  tsPlugin?: { packagePath: string };
  uiRuntime?: { plain: { pluginId: string; packagePath: string }; wasm: { pluginId: string; packagePath: string } };
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
  if(config.scenarios.includes("file-templates")) {
    const v=record(raw.fileTemplates);
    if(v?.phase!=="setup"&&v?.phase!=="cold")throw new Error("invalid fileTemplates phase");
    config.fileTemplates={packagePath:text(v.packagePath,"fileTemplates.packagePath"),projectRoot:text(v.projectRoot,"fileTemplates.projectRoot"),phase:v.phase};
  }
  if (config.scenarios.includes("port-manager")) {
    const fixture = record(raw.portManager);
    const pid = fixture?.pid;
    if (!Number.isInteger(pid) || (pid as number) < 1) throw new Error("invalid smoke config: portManager.pid");
    config.portManager = { packagePath: text(fixture?.packagePath, "portManager.packagePath"), port: port(fixture?.port, "portManager.port"), pid: pid as number };
  }
  if (config.scenarios.includes("services")) {
    const fixture = record(raw.services);
    const packages = record(fixture?.packages);
    const optional = (value: unknown, field: string) => value === undefined ? undefined : text(value, field);
    const ai = record(fixture?.ai);
    config.services = {
      packages: {
        cryptoToolkit: text(packages?.cryptoToolkit, "services.packages.cryptoToolkit"),
        imageToolkit: text(packages?.imageToolkit, "services.packages.imageToolkit"),
        aiTranslator: optional(packages?.aiTranslator, "services.packages.aiTranslator"),
        consumerDemo: optional(packages?.consumerDemo, "services.packages.consumerDemo"),
        ocr: optional(packages?.ocr, "services.packages.ocr"),
        invoiceManager: optional(packages?.invoiceManager, "services.packages.invoiceManager"),
      },
      invoiceImagePath: optional(fixture?.invoiceImagePath, "services.invoiceImagePath"),
      pngPath: text(fixture?.pngPath, "services.pngPath"),
      exifJpegPath: text(fixture?.exifJpegPath, "services.exifJpegPath"),
      outputDir: text(fixture?.outputDir, "services.outputDir"),
      ai: ai ? {
        baseUrl: loopbackUrl(ai.baseUrl, "services.ai.baseUrl"),
        model: text(ai.model, "services.ai.model", 128),
        apiKey: text(ai.apiKey, "services.ai.apiKey", 256),
        sourceText: text(ai.sourceText, "services.ai.sourceText"),
        expectedText: text(ai.expectedText, "services.ai.expectedText"),
      } : undefined,
    };
  }
  if (config.scenarios.includes("perf-database")) {
    const db = record(raw.perfDatabase);
    config.perfDatabase = {
      filePath: text(db?.filePath, "perfDatabase.filePath"), table: text(db?.table, "perfDatabase.table"),
      rows: Number(db?.rows) || 0, columns: Number(db?.columns) || 0,
      ...(Number(db?.extraTables) > 0 ? { extraTables: Number(db?.extraTables) } : {}),
      ...(Number(record(db?.redis)?.port) > 0 ? { redis: { port: Number(record(db?.redis)?.port), keys: Number(record(db?.redis)?.keys) || 0 } } : {}),
    };
  }
  if (config.scenarios.includes("perf-plugin-startup")) {
    const startup = record(raw.perfPluginStartup);
    if (startup?.phase !== "setup" && startup?.phase !== "cold") throw new Error("invalid perfPluginStartup phase");
    const packages = Array.isArray(startup.packages) ? startup.packages.map((item, index) => {
      const entry = record(item);
      return { pluginId: text(entry?.pluginId, `perfPluginStartup.packages[${index}].pluginId`), packagePath: text(entry?.packagePath, `perfPluginStartup.packages[${index}].packagePath`), enable: entry?.enable === true };
    }) : [];
    config.perfPluginStartup = { phase: startup.phase, packages };
  }
  if (config.scenarios.includes("perf-plugins")) {
    const plugins = record(raw.perfPlugins);
    const packages = Array.isArray(plugins?.packages) ? plugins!.packages.map((item, index) => {
      const entry = record(item);
      return { pluginId: text(entry?.pluginId, `perfPlugins.packages[${index}].pluginId`), packagePath: text(entry?.packagePath, `perfPlugins.packages[${index}].packagePath`), view: entry?.view === true };
    }) : [];
    config.perfPlugins = { packages };
  }
  if (config.scenarios.includes("perf-pages")) {
    const pages = record(raw.perfPages);
    const browserUrls = Array.isArray(pages?.browserUrls) ? pages!.browserUrls.map((item, index) => text(item, `perfPages.browserUrls[${index}]`)) : [];
    config.perfPages = { sshProfiles: Number(pages?.sshProfiles) || 0, browserUrls };
  }
  if (config.scenarios.includes("perf-git")) {
    const git = record(raw.perfGit);
    config.perfGit = { repoRoot: text(git?.repoRoot, "perfGit.repoRoot"), modified: Number(git?.modified) || 0, untracked: Number(git?.untracked) || 0, ...(Number(git?.historyCommits) > 0 ? { historyCommits: Number(git?.historyCommits) } : {}) };
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
  if (config.scenarios.includes("perf")) {
    const perf = record(raw.perf);
    const smallFiles = Array.isArray(perf?.smallFiles) ? perf!.smallFiles.map((item, index) => text(item, `perf.smallFiles[${index}]`)) : [];
    if (!smallFiles.length) throw new Error("perf.smallFiles required");
    config.perf = {
      projectRoot: text(perf?.projectRoot, "perf.projectRoot"), fileCount: Number(perf?.fileCount) || 0,
      bigFile: text(perf?.bigFile, "perf.bigFile"), bigFileLines: Number(perf?.bigFileLines) || 0,
      ...(perf?.bigPlainFile === undefined ? {} : { bigPlainFile: text(perf.bigPlainFile, "perf.bigPlainFile") }),
      ...(perf?.only === "pages" ? { only: "pages" as const } : {}),
      smallFiles, terminalLines: Number(perf?.terminalLines) || 100000,
    };
  }
  if (config.scenarios.includes("large-text")) {
    config.largeText = { filePath: text(record(raw.largeText)?.filePath, "largeText.filePath") };
  }
  if (config.scenarios.includes("lsp")) {
    config.lsp = { projectRoot: text(record(raw.lsp)?.projectRoot, "lsp.projectRoot") };
  }
  if (config.scenarios.includes("laravel")) {
    const fixture = record(raw.laravel);
    config.laravel = {
      laravelPackage: text(fixture?.laravelPackage, "laravel.laravelPackage"),
      i18nPackage: text(fixture?.i18nPackage, "laravel.i18nPackage"),
      projectRoot: text(fixture?.projectRoot, "laravel.projectRoot"),
      canary: text(fixture?.canary, "laravel.canary"),
    };
  }
  if (config.scenarios.includes("git-ai-commit")) {
    const fixture = record(raw.gitAiCommit);
    config.gitAiCommit = {
      repoRoot: text(fixture?.repoRoot, "gitAiCommit.repoRoot"),
      baseUrl: loopbackUrl(fixture?.baseUrl, "gitAiCommit.baseUrl"),
      apiKey: text(fixture?.apiKey, "gitAiCommit.apiKey", 256),
      model: text(fixture?.model, "gitAiCommit.model", 128),
      answer: text(fixture?.answer, "gitAiCommit.answer", 256),
    };
  }
  if (config.scenarios.includes("linters")) {
    const fixture = record(raw.linters);
    config.linters = { packagePath: text(fixture?.packagePath, "linters.packagePath"), projectRoot: text(fixture?.projectRoot, "linters.projectRoot") };
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
  if (config.scenarios.includes("debugger")) {
    const fixture = record(raw.debugger);
    const line = (value: unknown, field: string) => {
      if (!Number.isInteger(value) || (value as number) < 1) throw new Error(`invalid smoke config: ${field}`);
      return value as number;
    };
    config.debugger = {
      packagePath: text(fixture?.packagePath, "debugger.packagePath"),
      python: text(fixture?.python, "debugger.python"),
      projectRoot: text(fixture?.projectRoot, "debugger.projectRoot"),
      program: text(fixture?.program, "debugger.program"),
      logLine: line(fixture?.logLine, "debugger.logLine"),
      stopLine: line(fixture?.stopLine, "debugger.stopLine"),
    };
  }
  if (config.scenarios.includes("plugin-providers")) {
    const fixture = record(raw.pluginProviders);
    config.pluginProviders = { packagePath: text(fixture?.packagePath, "pluginProviders.packagePath"), repoRoot: text(fixture?.repoRoot, "pluginProviders.repoRoot") };
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
  if (config.scenarios.includes("ui-runtime")) {
    const runtime = record(raw.uiRuntime);
    const fixture = (name: "plain" | "wasm") => ({
      pluginId: text(record(runtime?.[name])?.pluginId, `uiRuntime.${name}.pluginId`),
      packagePath: text(record(runtime?.[name])?.packagePath, `uiRuntime.${name}.packagePath`),
    });
    config.uiRuntime = { plain: fixture("plain"), wasm: fixture("wasm") };
  }
  if (config.scenarios.includes("ts-plugin")) {
    config.tsPlugin = { packagePath: text(record(raw.tsPlugin)?.packagePath, "tsPlugin.packagePath") };
  }
  if (config.scenarios.includes("ai-prompts")) {
    config.aiPrompts = { packagePath: text(record(raw.aiPrompts)?.packagePath, "aiPrompts.packagePath") };
  }
  if (config.scenarios.includes("ai-catalogs")) {
    config.aiCatalogs = { packagePath: text(record(raw.aiCatalogs)?.packagePath, "aiCatalogs.packagePath") };
  }
  if (config.scenarios.includes("rust-plugin")) {
    const plugin = record(raw.rustPlugin);
    config.rustPlugin = { packagePath: text(plugin?.packagePath, "rustPlugin.packagePath"), pluginId: text(plugin?.pluginId, "rustPlugin.pluginId", 128) };
  }
  if (config.scenarios.includes("plugin")) {
    const plugin = record(raw.plugin);
    config.plugin = { packagePath: text(plugin?.packagePath, "plugin.packagePath"), pluginId: text(plugin?.pluginId, "plugin.pluginId", 128) };
  }
  if (config.scenarios.includes("associations")) {
    const fixture = record(raw.associations);
    if (fixture?.phase !== "setup" && fixture?.phase !== "cold") throw new Error("invalid smoke config: associations.phase");
    config.associations = {
      packagePath: text(fixture.packagePath, "associations.packagePath"),
      torrentPath: text(fixture.torrentPath, "associations.torrentPath"),
      ordinaryPath: text(fixture.ordinaryPath, "associations.ordinaryPath"),
      phase: fixture.phase,
    };
  }
  if (config.scenarios.includes("ebook")) {
    const fixture = record(raw.ebook);
    if (!Array.isArray(fixture?.paths) || fixture.paths.length < 1 || fixture.paths.length > 16) throw new Error("invalid smoke config: ebook.paths");
    config.ebook = { packagePath: text(fixture.packagePath, "ebook.packagePath"), paths: fixture.paths.map(value => text(value, "ebook.paths")) };
  }
  if(config.scenarios.includes("mail-client")){const fixture=record(raw.mailClient);config.mailClient={packagePath:text(fixture?.packagePath,"mailClient.packagePath"),outputDir:text(fixture?.outputDir,"mailClient.outputDir"),attachmentPath:text(fixture?.attachmentPath,"mailClient.attachmentPath"),controlUrl:loopbackUrl(fixture?.controlUrl,"mailClient.controlUrl"),expectedSubject:text(fixture?.expectedSubject,"mailClient.expectedSubject")};}
  if (config.scenarios.includes("formatter")) {
    const fixture = record(raw.formatter);
    config.formatter = { packagePath: text(fixture?.packagePath, "formatter.packagePath"), imagePath: text(fixture?.imagePath, "formatter.imagePath") };
  }
  if (config.scenarios.includes("browser")) {
    const browser = record(raw.browser);
    config.browser = {
      firstUrl: loopbackUrl(browser?.firstUrl, "browser.firstUrl"),
      secondUrl: loopbackUrl(browser?.secondUrl, "browser.secondUrl"),
      errorUrl: loopbackUrl(browser?.errorUrl, "browser.errorUrl"),
    };
    const use = record(browser?.use);
    if (use) {
      config.browser.use = {
        openerUrl: loopbackUrl(use.openerUrl, "browser.use.openerUrl"),
        formsUrl: loopbackUrl(use.formsUrl, "browser.use.formsUrl"),
        articleUrl: loopbackUrl(use.articleUrl, "browser.use.articleUrl"),
        filesUrl: loopbackUrl(use.filesUrl, "browser.use.filesUrl"),
        downloadName: text(use.downloadName, "browser.use.downloadName", 255),
      };
    }
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
