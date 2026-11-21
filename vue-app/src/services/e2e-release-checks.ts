/**
 * Release smoke scenarios for features that previously had only unit and
 * machine evidence: the debugger (python-debug + debugpy, a logpoint and an
 * ordinary breakpoint), the SQL safety net (high-risk confirm, backup record
 * and undo) and SFTP transfer jobs with progress events. Each drives the real
 * WebView → desktop IPC → Go path. Loaded only by e2e-smoke-runner.ts in
 * `-tags e2e` builds.
 */
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import type { DatabaseQueryResult } from "@/domain/database/types";
import type { SqlSafetyCheck } from "@/domain/database/sql-safety";
import { installLocalPlugin, previewLocalPlugin, setPluginEnabled, uninstallPlugin } from "@/services/plugin-service";
import { checkSqlSafety, runWithSafety, sqlSafetyArgs } from "@/services/sql-safety-service";
import { listSqlSafetyHistory, previewSqlSafetyUndo, undoSqlSafety } from "@/services/sql-safety-history";
import { runSftpTransfer, type SftpTransferSnapshot } from "@/services/sftp-service";
import { invokeBackend, nativeErrorMessage } from "@/services/desktop-bridge";

interface StepLog {
  done(step: string): void;
}

function delay(ms: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, ms));
}

async function until<T>(read: () => T | undefined | false | null | Promise<T | undefined | false | null>, what: string, timeoutMs: number): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = await read();
    if (value) return value;
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await delay(100);
  }
}

function randomId(prefix: string) {
  return `${prefix}-${Math.random().toString(36).slice(2, 10)}`;
}

// ---- SQL safety net ----

/**
 * A high-risk DELETE (no WHERE) must be rejected without the typed table
 * name and run only with it; the deleted rows are backed up, listed in the
 * operation history and restored by undo.
 */
export async function sqlSafetyScenario(config: SmokeConfig, log: StepLog) {
  const connectionId = randomId("e2e-safety");
  const sessionId = await invokeBackend<string>("db_connect", {
    config: { dbType: "sqlite", host: "", port: 0, filePath: config.sqlSafety!.filePath, connectionId },
  });
  const run = (sql: string, confirmText?: (check: SqlSafetyCheck) => string | undefined) =>
    runWithSafety({
      sessionId, query: sql, source: "e2e", origin: "preconfirmed",
      confirm: async (check) => ({ confirmText: confirmText ? confirmText(check) : check.risk === "high" ? check.confirmTarget : undefined }),
      execute: (token) => invokeBackend<DatabaseQueryResult>("db_execute_query", { sessionId, query: sql, queryId: randomId("e2e-q"), source: "e2e", ...sqlSafetyArgs(token) }),
    });
  try {
    await run("CREATE TABLE safety_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)");
    await run(`INSERT INTO safety_items (name) VALUES ('a'), ('b'), ('${config.nonce}')`);
    log.done("fixture table created with 3 rows");
    const check = await checkSqlSafety({ sessionId, query: "DELETE FROM safety_items", source: "e2e" });
    if (check.risk !== "high" || check.confirmTarget !== "safety_items" || !check.checkId) throw new Error(`DELETE without WHERE not classified high risk: ${JSON.stringify(check)}`);
    log.done("DELETE without WHERE is high risk and requires typing safety_items");
    let rejected = "";
    try {
      await invokeBackend("db_execute_query", { sessionId, query: "DELETE FROM safety_items", queryId: randomId("e2e-q"), source: "e2e", checkId: check.checkId, confirmText: "wrong" });
    } catch (error) {
      rejected = nativeErrorMessage(error);
    }
    if (!rejected) throw new Error("high-risk delete ran with a wrong confirmation");
    const count = async () => Number((await run("SELECT COUNT(*) AS n FROM safety_items"))!.rows[0]?.[0]);
    if (await count() !== 3) throw new Error("rows changed after a rejected confirmation");
    log.done("wrong confirmation rejected and nothing changed");
    await run("DELETE FROM safety_items");
    if (await count() !== 0) throw new Error("confirmed delete did not run");
    log.done("delete ran after typing the table name");
    const history = await until(async () => (await listSqlSafetyHistory(connectionId)).find((record) => /^delete/i.test(record.statement.trim()) && record.undoable), "undoable backup record", config.timeoutMs);
    if (history.affected !== 3 || history.table.name !== "safety_items") throw new Error(`backup record: ${JSON.stringify({ affected: history.affected, table: history.table })}`);
    const preview = await previewSqlSafetyUndo(sessionId, history.id);
    if (preview.total !== 3 || preview.conflictCount !== 0) throw new Error(`undo preview: ${JSON.stringify({ total: preview.total, conflicts: preview.conflictCount })}`);
    const undo = await undoSqlSafety({ sessionId, id: history.id, mode: "skipConflicts", previewToken: preview.previewToken, confirmText: "safety_items" }).catch(async (error: unknown) => {
      // Medium-risk undo needs no confirmation text; retry without it if the host refuses the extra field.
      if (/confirm/i.test(nativeErrorMessage(error))) return undoSqlSafety({ sessionId, id: history.id, mode: "skipConflicts", previewToken: preview.previewToken });
      throw error;
    });
    if (undo.restored !== 3) throw new Error(`undo restored ${undo.restored} rows`);
    const names = (await run("SELECT name FROM safety_items ORDER BY id"))!.rows.map((row) => row[0]);
    if (names.join(",") !== `a,b,${config.nonce}`) throw new Error(`restored rows differ: ${names.join(",")}`);
    log.done("operation history listed the backup; undo restored all 3 original rows");
  } finally {
    await invokeBackend("db_disconnect", { sessionId }).catch(() => undefined);
  }
}

// ---- SFTP transfers ----

/** Uploads then downloads a fixture file through transfer jobs and checks progress events and bytes. */
export async function sftpTransfersScenario(config: SmokeConfig, log: StepLog) {
  const ssh = config.ssh!;
  const fixture = config.sftpTransfers!;
  const sessionId = randomId("e2e-sftp");
  await invokeBackend("sftp_connect", { payload: { sessionId, protocol: "sftp", host: ssh.host, port: ssh.port, username: ssh.username, authType: "password", password: ssh.password, algorithmPolicy: "modern" } });
  try {
    const home = await invokeBackend<string>("sftp_get_home", { sessionId });
    const remotePath = `${home.replace(/\/$/, "")}/upload-${config.nonce}.bin`;
    const upload: SftpTransferSnapshot[] = [];
    const uploaded = await runSftpTransfer({ jobId: randomId("e2e-up"), sessionId, direction: "upload", localPath: fixture.uploadPath, remotePath, resume: false, conflictStrategy: "overwrite" }, (snapshot) => upload.push({ ...snapshot }));
    if (uploaded.status !== "completed" || uploaded.bytesTransferred !== fixture.size || uploaded.totalBytes !== fixture.size) throw new Error(`upload ended ${JSON.stringify(uploaded)}`);
    const partial = upload.filter((snapshot) => snapshot.status === "running" && snapshot.bytesTransferred > 0 && snapshot.bytesTransferred < fixture.size);
    if (!partial.length) throw new Error(`no intermediate upload progress among ${upload.length} snapshots`);
    log.done(`upload completed (${fixture.size} bytes) with ${partial.length} intermediate progress events`);
    const listing = await invokeBackend<Array<{ name: string; size: number }>>("sftp_list_dir", { sessionId, path: home });
    if (listing.find((entry) => entry.name === `upload-${config.nonce}.bin`)?.size !== fixture.size) throw new Error("uploaded file missing or wrong size in the remote listing");
    const download: SftpTransferSnapshot[] = [];
    const downloaded = await runSftpTransfer({ jobId: randomId("e2e-down"), sessionId, direction: "download", localPath: fixture.downloadPath, remotePath, resume: false }, (snapshot) => download.push({ ...snapshot }));
    if (downloaded.status !== "completed" || downloaded.bytesTransferred !== fixture.size) throw new Error(`download ended ${JSON.stringify(downloaded)}`);
    if (!download.some((snapshot) => snapshot.status === "running" && snapshot.bytesTransferred > 0 && snapshot.bytesTransferred < fixture.size)) throw new Error("no intermediate download progress");
    log.done("download completed with intermediate progress events; harness compares bytes");
  } finally {
    await invokeBackend("sftp_disconnect", { sessionId }).catch(() => undefined);
  }
}

// ---- plugin data providers and full trust ----

/**
 * Installs the process showcase plugin and calls its sessionSources,
 * scmCommitMessage and fileDecorations providers through the real host
 * commands; then toggles full trust: it binds to the package fingerprint,
 * is audited, and lapses when the plugin is disabled.
 */
export async function pluginProvidersScenario(config: SmokeConfig, log: StepLog) {
  const fixture = config.pluginProviders!;
  const pluginId = "contributions-process-showcase";
  const { loadPluginSessions, loadPluginSessionMessages } = await import("@/services/plugin-session-sources");
  const { generatePluginCommitMessage } = await import("@/services/plugin-scm-commit-message");
  const { requestFileDecorations } = await import("@/services/plugin-file-decorations");
  const { getFullTrust, setFullTrust, getFullTrustAudit } = await import("@/services/plugin-full-trust");
  const preview = await previewLocalPlugin(fixture.packagePath);
  if (preview.manifest.id !== pluginId) throw new Error(`unexpected package id ${preview.manifest.id}`);
  await installLocalPlugin(fixture.packagePath);
  try {
    await setPluginEnabled(pluginId, true);
    log.done("process showcase plugin installed and enabled");
    const base = { pluginId, pluginName: preview.manifest.name, activeInWorkspace: true };
    const source = { ...base, key: `plugin:${pluginId}/showcase`, id: "showcase", title: "Showcase sessions", icon: "magic" as const, mark: "S" };
    const sessions = await loadPluginSessions(source, "all", undefined);
    if (!sessions.ok || !sessions.response.sessions.length) throw new Error(`session list failed: ${JSON.stringify(sessions)}`);
    const first = sessions.response.sessions[0]!;
    const messages = await loadPluginSessionMessages(source, first.id);
    if (!messages.ok || !messages.response.messages.length) throw new Error(`session messages failed: ${JSON.stringify(messages)}`);
    log.done(`sessionSources listed ${sessions.response.sessions.length} sessions and read ${messages.response.messages.length} messages as plain text`);
    const draft = await generatePluginCommitMessage({ ...base, id: "showcase", title: "Showcase draft" }, fixture.repoRoot, fixture.repoRoot);
    if (!draft.ok || !draft.response.message.startsWith("chore(showcase): update 2 files")) throw new Error(`commit draft: ${JSON.stringify(draft)}`);
    if (/secret/i.test(draft.response.message)) throw new Error("commit draft leaked the staged .env content");
    log.done("scmCommitMessage drafted from the staged diff (.env excluded); nothing was committed");
    const decorations = await requestFileDecorations({ ...base, id: "showcase", title: "Showcase markers" }, fixture.repoRoot, "", ["README.md", "TODO", "main.go"], new AbortController().signal);
    if (decorations === "failed" || decorations === "rejected") throw new Error(`file decorations ${decorations}`);
    if (decorations.get("README.md")?.badge !== "M" || !decorations.has("TODO") || decorations.has("main.go")) throw new Error(`decorations: ${JSON.stringify([...decorations])}`);
    log.done("fileDecorations returned markers for README.md and TODO only");
    const status = await getFullTrust(pluginId, { full: true });
    if (status.trusted || !status.subject.eligible || !status.subject.fingerprint) throw new Error(`full trust preconditions: ${JSON.stringify(status)}`);
    const trusted = await setFullTrust(pluginId, true, status.subject.fingerprint);
    if (!trusted.trusted) throw new Error("full trust was not granted");
    await setPluginEnabled(pluginId, false);
    const lapsed = await getFullTrust(pluginId);
    if (lapsed.trusted) throw new Error("full trust survived disabling the plugin");
    const audit = await getFullTrustAudit(pluginId);
    if (!audit.some((entry) => entry.kind === "granted") || !audit.some((entry) => entry.kind === "lapsed")) throw new Error(`audit: ${JSON.stringify(audit.map((entry) => entry.kind))}`);
    log.done("full trust granted for the exact package fingerprint, lapsed on disable, both audited");
  } finally {
    await setPluginEnabled(pluginId, false).catch(() => undefined);
    await uninstallPlugin(pluginId, true).catch(() => undefined);
  }
  log.done("plugin uninstalled with its data");
}
