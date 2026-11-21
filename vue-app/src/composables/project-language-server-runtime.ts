import { watch, type Ref } from "vue";
import { runtimePerformanceSettingsFromSnapshot } from "@/domain/performance/runtime-limits";
import { readSettingsSnapshot } from "@/services/settings-persistence";
import * as monaco from "@/services/monaco-runtime.js";
import { projectFileUri } from "@/composables/project-file-uri";
import {
  listLspServers,
  onLspEvent,
  onLspRestartRequired,
  onLspServerInstalled,
  replyLspRequest,
  sendLspNotify,
  sendLspRequest as sendNativeLspRequest,
  startLspServer,
  stopLspServer,
  type BuiltinServerAvailability,
  type LspEvent,
  type LspRestartRequiredEvent,
} from "@/services/lsp-service";
import { hasMonacoCommand, registerMonacoCommand } from "@/services/monaco-commands";
import { builtinServerLaunchInFlight, isLspServerHeld, lspRootKey, onLspServerReleased } from "@/services/lsp-server-holds";
import { lspConfigurationValue, lspSchemaAssociationNotification } from "@/domain/projects/lsp-schemas";
import {
  applyLspProgress,
  lspRuntimeState,
  markLspWatchLimited,
  setLspRuntimeState,
  setLspServerName,
  setLspWatchScanning,
} from "@/services/lsp-runtime-state";
import {
  isPluginLspServerId,
  LSP_FILE_WATCH_LIMITED_METHOD,
  LSP_FILE_WATCH_SCAN_METHOD,
  lspDocumentLanguageId,
  pluginApplyEditAllowed,
  routedServerLanguages,
  type LanguageRoutingContext,
} from "@/domain/projects/lsp";
import {
  documentContentChanges,
  initializeCapabilities,
  isPathInLspRoot,
  parseDocumentSync,
  parseDiagnosticProvider,
  parseDiagnostics,
  parseDocumentDiagnosticReport,
  parseWorkspaceDiagnosticReport,
  type LspDiagnostic,
  type LspDiagnosticProvider,
  type LspDocumentSync,
} from "@/domain/projects/lsp-protocol";
import {
  LSP_SEMANTIC_TOKEN_MODIFIERS,
  LSP_SEMANTIC_TOKEN_TYPES,
  MONACO_SEMANTIC_SCOPES,
  parseSemanticTokensProvider,
  remapSemanticTokens,
  semanticScopeMap,
} from "@/domain/projects/lsp-semantic-tokens";
import { languageServerErrorCode } from "@/domain/plugins/language-servers";
import { lspCommandFailureText, lspShowMessageToast } from "@/domain/projects/lsp-messages";
import { parseWorkDoneProgress } from "@/domain/projects/lsp-progress";
import { showLspCommandFailure, showLspMessage } from "@/services/lsp-feedback";
import {
  TSSERVER_BRIDGES,
  parseTsserverRequest,
  tsserverExecuteCommand,
  tsserverResponseBody,
  tsserverResponseParams,
} from "@/domain/projects/lsp-tsserver-bridge";
import { usePluginLanguageServersStore } from "@/stores/plugin-language-servers";
import { useProjectProblemsStore } from "@/stores/project-problems";
import { startPluginLanguageServer } from "@/services/plugin-language-server-service";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ServerCapabilities {
  hover: boolean;
  completion: boolean;
  definition: boolean;
  references: boolean;
  formatting: boolean;
  signatureHelp: boolean;
  rename: boolean;
  codeAction: boolean;
  codeLens: boolean;
  codeLensResolve: boolean;
  inlayHints: boolean;
  documentHighlight: boolean;
  implementation: boolean;
  typeDefinition: boolean;
  documentSymbol: boolean;
  semanticTokens: boolean;
}

interface SyncedDocument {
  text: string;
  modelVersion: number;
  diagnostics: {
    resultId?: string;
    revision: number;
    requested: boolean;
    running: boolean;
    timer?: ReturnType<typeof setTimeout>;
    abort?: AbortController;
  };
}

interface ActiveSession {
  root: string;
  serverId: string;
  generation: number;
  languages: string[];
  capabilities: ServerCapabilities;
  sync: LspDocumentSync;
  documents: Map<string, SyncedDocument>;
  notifications: Promise<void>;
  diagnosticProvider?: LspDiagnosticProvider;
  diagnosticRequests: Promise<void>;
  /** Present when the server answers workspace/diagnostic (files that are not open). */
  workspaceDiagnostics?: WorkspaceDiagnosticsState;
  /** 辅助型（Tailwind/Angular 等）：与主服务器共享语言，叠加而非顶替。 */
  auxiliary: boolean;
  /** Server legend index → MONACO_SEMANTIC_SCOPES index (-1 drops the token). */
  semanticScopes?: Int32Array;
}

interface DocumentVersion {
  uri: string;
  version: number;
}

interface WorkspaceDiagnosticsState {
  /** uri → resultId of the last workspace report, sent back as previousResultIds. */
  resultIds: Map<string, string>;
  running: boolean;
  requested: boolean;
  timer?: ReturnType<typeof setTimeout>;
  abort?: AbortController;
}

// ---------------------------------------------------------------------------
// Module-level registries
// ---------------------------------------------------------------------------
const activeSessions = new Map<string, ActiveSession>();
const startingSessions = new Set<string>();
const documentVersions = new Map<string, DocumentVersion>();
const languageProviders = new Map<string, monaco.IDisposable[]>();
// Asks Monaco to refetch semantic tokens (session ready/stopped, server refresh).
const semanticTokensChanged = new monaco.Emitter<void>();
// Asks Monaco to refetch CodeLens (session ready/stopped, workspace/codeLens/refresh).
const codeLensesChanged = new monaco.Emitter<void>();
// Monaco's runtime events take (listener, thisArg, disposables); the public IEvent type omits the last.
type MonacoEvent = (listener: () => void, thisArg?: unknown, disposables?: unknown) => monaco.IDisposable;

// 多会话共存：langId\0root → 按优先级排序的会话列表。
// 主服务器在前、辅助型在后：顺序决定重命名/格式化/符号等单 owner
// 能力的归属，也决定补全/悬停等合并结果的展示次序。
const languageSessionMap = new Map<string, ActiveSession[]>();

// 每个会话当前已同步的文档集合（sessionKey → uri Set），
// 同一文档可同时同步给多个服务器（didOpen/didChange 扇出）。
const openDocuments = new Map<string, Set<string>>();

// serverId → language list (from listLspServers or start response)
const serverLanguageMap = new Map<string, string[]>();
// serverId → 是否辅助型（来自 listLspServers / start 响应）
const serverAuxiliaryMap = new Map<string, boolean>();
// serverId → companion server ids that must run beside it (from listLspServers).
const serverCompanionMap = new Map<string, string[]>();
let serverMapLoaded = false;

let currentRoot = "";
let currentEnabledServers = new Set<string>();
// Project override: language id → plugin server id (built-ins win otherwise).
let currentOverrides: Record<string, string> = {};
// sessionKey → time of the last user-initiated rename or command on that
// session; a plugin server's workspace/applyEdit is honoured only shortly after.
const userActionAt = new Map<string, number>();
let currentWorkspaceActive = false;
const idleStopTimers = new Map<string, ReturnType<typeof setTimeout>>();

function lspRuntimeLimits() {
  const limits = runtimePerformanceSettingsFromSnapshot(readSettingsSnapshot());
  return {
    backgroundStopMs: limits.lspBackgroundStopSeconds * 1000,
    idleStopMs: limits.lspIdleStopSeconds * 1000,
    auxiliaryIdleStopMs: limits.lspAuxiliaryIdleStopSeconds * 1000,
  };
}

// Auto-restart: track crash count per session key
const restartAttempts = new Map<string, { count: number; since: number }>();

function sessionKey(root: string, serverId: string) {
  return `${root}\0${serverId}`;
}

function langSessionKey(langId: string, root: string) {
  return `${langId}\0${root}`;
}

// ---------------------------------------------------------------------------
// Plugin language servers: routing and state
// ---------------------------------------------------------------------------

function pluginLanguageServers() {
  return usePluginLanguageServersStore();
}

function routingContext(): LanguageRoutingContext {
  const builtinLanguages = new Map<string, readonly string[]>();
  for (const [serverId, languages] of serverLanguageMap) {
    if (!isPluginLspServerId(serverId) && !serverAuxiliaryMap.get(serverId)) builtinLanguages.set(serverId, languages);
  }
  const pluginLanguages = new Map<string, readonly string[]>();
  for (const server of pluginLanguageServers().servers) pluginLanguages.set(server.sessionId, server.languages);
  return { enabled: currentEnabledServers, overrides: currentOverrides, builtinLanguages, pluginLanguages };
}

/** Declared languages of a built-in (list/start response) or active plugin server. */
function declaredLanguages(serverId: string): string[] | undefined {
  if (isPluginLspServerId(serverId)) return pluginLanguageServers().server(serverId)?.languages;
  return serverLanguageMap.get(serverId);
}

/** The languages serverId serves in the current project after routing. */
function servedLanguages(serverId: string, declared: readonly string[] | undefined): string[] {
  if (!declared) return [];
  return routedServerLanguages(serverId, declared, routingContext(), serverAuxiliaryMap.get(serverId) ?? false);
}

function sameLanguages(a: readonly string[], b: readonly string[]) {
  return a.length === b.length && a.every((language) => b.includes(language));
}

function markUserAction(root: string, serverId: string) {
  if (isPluginLspServerId(serverId)) userActionAt.set(sessionKey(root, serverId), Date.now());
}

// When the user last ran a server command (CodeLens, code action) per session:
// info-level window/showMessage only shows as feedback shortly afterwards.
const userCommandAt = new Map<string, number>();

function recordPluginStartError(root: string, serverId: string, error: unknown) {
  const store = pluginLanguageServers();
  const code = languageServerErrorCode(error);
  if (code === "untrusted" || code === "changed") store.setState(root, serverId, { kind: "review", changed: code === "changed" });
  else if (code === "limit") store.setState(root, serverId, { kind: "limit" });
  else store.setState(root, serverId, { kind: "unavailable", message: error instanceof Error ? error.message : String(error) });
}

/** Keeps only the edits of a plugin server that target files under its root. */
function confineWorkspaceEditToRoot(edit: monaco.languages.WorkspaceEdit, root: string): monaco.languages.WorkspaceEdit | null {
  const prefix = root.endsWith("/") ? root : `${root}/`;
  const edits = edit.edits.filter((item) => {
    if (!("resource" in item) || item.resource.scheme !== "file") return false;
    const path = item.resource.fsPath ?? item.resource.path;
    return path.startsWith(prefix) && !path.split("/").includes("..");
  });
  return edits.length === edit.edits.length && edits.length ? { edits } : null;
}

async function ensureServerLanguageMap(): Promise<void> {
  if (serverMapLoaded) return;
  try {
    const servers = await listLspServers();
    for (const s of servers) {
      serverLanguageMap.set(s.id, s.languages);
      serverAuxiliaryMap.set(s.id, Boolean(s.auxiliary));
      if (s.companions?.length) serverCompanionMap.set(s.id, s.companions);
      else serverCompanionMap.delete(s.id);
    }
    serverMapLoaded = true;
  } catch {
    // best effort
  }
}

function withCompanions(enabled: readonly string[]): string[] {
  const result = new Set(enabled);
  for (const serverId of enabled) {
    for (const companion of serverCompanionMap.get(serverId) ?? []) result.add(companion);
  }
  return [...result];
}

// ---------------------------------------------------------------------------
// tsserver bridge (Vue 3.x hybrid mode)
// ---------------------------------------------------------------------------
// vue-ls 3.x asks for TypeScript work through tsserver/request notifications;
// the vue-ts companion (typescript-language-server + @vue/typescript-plugin)
// answers them via workspace/executeCommand. Every request gets a response,
// null on failure, so the Vue server never waits forever.
const COMPANION_WAIT_MS = 20_000;
const TSSERVER_REQUEST_TIMEOUT_MS = 30_000;

async function waitForSession(root: string, serverId: string, timeoutMs: number): Promise<ActiveSession | undefined> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const session = activeSessions.get(sessionKey(root, serverId));
    if (session) return session;
    if (Date.now() >= deadline || currentRoot !== root || !currentEnabledServers.has(serverId)) return undefined;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

async function bridgeTsserverRequest(event: LspEvent, companionId: string) {
  const request = parseTsserverRequest(event.params);
  if (!request) return;
  let body: unknown = null;
  try {
    const companion = await waitForSession(event.root, companionId, COMPANION_WAIT_MS);
    if (companion) {
      const result = await sendNativeLspRequest(companion.root, companion.serverId, companion.generation,
        "workspace/executeCommand", tsserverExecuteCommand(request), TSSERVER_REQUEST_TIMEOUT_MS);
      body = tsserverResponseBody(result);
    }
  } catch {
    // An unavailable companion or failed request answers null.
  }
  await sendLspNotify(event.root, event.serverId, event.generation, "tsserver/response",
    tsserverResponseParams(request.id, body)).catch(() => {
    // The Vue session stopped meanwhile.
  });
}

// 同一语言可能由多个服务器同时服务（主 + 辅助型），返回列表而非单个。
function getSessionsForModel(model: monaco.editor.ITextModel): ActiveSession[] {
  if (model.uri.scheme !== "file") return [];
  const fsPath = model.uri.fsPath ?? model.uri.path;
  const langId = model.getLanguageId();
  const sessions = languageSessionMap.get(langSessionKey(langId, currentRoot)) ?? [];
  return sessions.filter((session) => isPathInLspRoot(fsPath, session.root));
}

function firstCapableSession(
  sessions: ActiveSession[],
  capability: keyof ServerCapabilities
): ActiveSession | null {
  return sessions.find((session) => session.capabilities[capability]) ?? null;
}

function registerInLangMap(session: ActiveSession) {
  for (const lang of session.languages) {
    const key = langSessionKey(lang, session.root);
    const list = languageSessionMap.get(key) ?? [];
    if (list.includes(session)) continue;
    if (session.auxiliary) list.push(session);
    else list.unshift(session);
    languageSessionMap.set(key, list);
  }
}

function unregisterFromLangMap(session: ActiveSession) {
  for (const lang of session.languages) {
    const key = langSessionKey(lang, session.root);
    const list = (languageSessionMap.get(key) ?? []).filter((s) => s !== session);
    if (list.length) languageSessionMap.set(key, list);
    else languageSessionMap.delete(key);
  }
  if (session.semanticScopes) semanticTokensChanged.fire();
  if (session.capabilities.codeLens) codeLensesChanged.fire();
}

// ---------------------------------------------------------------------------
// LSP <-> Monaco conversions
// ---------------------------------------------------------------------------

function toMonacoRange(range: {
  start: { line: number; character: number };
  end: { line: number; character: number };
}) {
  return new monaco.Range(
    range.start.line + 1,
    range.start.character + 1,
    range.end.line + 1,
    range.end.character + 1
  );
}

function toLspPosition(position: monaco.Position) {
  return { line: position.lineNumber - 1, character: position.column - 1 };
}

function toLspRange(range: monaco.IRange) {
  return {
    start: { line: range.startLineNumber - 1, character: range.startColumn - 1 },
    end: { line: range.endLineNumber - 1, character: range.endColumn - 1 },
  };
}

function toMonacoPosition(pos: { line: number; character: number }): monaco.IPosition {
  return { lineNumber: pos.line + 1, column: pos.character + 1 };
}

// ---------------------------------------------------------------------------
// Diagnostic conversion
// ---------------------------------------------------------------------------

const LSP_SEVERITY_MAP: Record<number, monaco.MarkerSeverity> = {
  1: monaco.MarkerSeverity.Error,
  2: monaco.MarkerSeverity.Warning,
  3: monaco.MarkerSeverity.Info,
  4: monaco.MarkerSeverity.Hint,
};

// 诊断按服务器分 owner 落 marker：多个服务器同时服务一个文档时（主 + 辅助型），
// 各自的 publishDiagnostics 不会互相覆盖。
function lspMarkerOwner(serverId: string) {
  return `project-lsp:${serverId}`;
}

function setDiagnosticMarkers(serverId: string, uri: string, diagnostics: readonly LspDiagnostic[]) {
  const model = monaco.editor.getModel(monaco.Uri.parse(uri));
  if (!model) return;
  const markers: monaco.editor.IMarkerData[] = diagnostics.map((d) => {
    const r = toMonacoRange(d.range);
    return {
      startLineNumber: r.startLineNumber,
      startColumn: r.startColumn,
      endLineNumber: r.endLineNumber,
      endColumn: r.endColumn,
      severity: LSP_SEVERITY_MAP[d.severity as number] ?? monaco.MarkerSeverity.Warning,
      message: String(d.message ?? ""),
      source: d.source ? String(d.source) : "lsp",
      code: d.code != null ? String(d.code) : undefined,
    };
  });
  monaco.editor.setModelMarkers(model, lspMarkerOwner(serverId), markers);
}

// Every report also lands in the project problems store, so diagnostics for
// files that are not open (push or workspace pull) stay visible and are shown
// as markers once the file opens.
function applyDiagnostics(root: string, serverId: string, uri: string, rawDiagnostics: unknown[]) {
  const diagnostics = parseDiagnostics(rawDiagnostics);
  const parsed = monaco.Uri.parse(uri);
  // Reports about files outside the project (SDKs, module caches) stay editor-only.
  if (parsed.scheme === "file" && isPathInLspRoot(parsed.fsPath ?? parsed.path, root)) {
    // Key by the editor's spelling so server URI encodings cannot duplicate a file.
    useProjectProblemsStore().set(root, serverId, parsed.toString(), diagnostics);
  }
  setDiagnosticMarkers(serverId, uri, diagnostics);
}

// Document pulls are serialized separately from notifications: a slow or failed
// diagnostic request must not block didChange/didSave or retire a healthy session.
const DIAGNOSTIC_DEBOUNCE_MS = 200;

function documentStillAttached(session: ActiveSession, uri: string, document: SyncedDocument) {
  return activeSessions.get(sessionKey(session.root, session.serverId)) === session &&
    session.documents.get(uri) === document;
}

function disposeDocumentDiagnostics(document: SyncedDocument) {
  if (document.diagnostics.timer) clearTimeout(document.diagnostics.timer);
  document.diagnostics.timer = undefined;
  document.diagnostics.requested = false;
  document.diagnostics.abort?.abort();
}

function scheduleDocumentDiagnostics(session: ActiveSession, model: monaco.editor.ITextModel) {
  const uri = model.uri.toString();
  const document = session.documents.get(uri);
  if (!session.diagnosticProvider || !document || !documentStillAttached(session, uri, document)) return;
  document.diagnostics.revision++;
  document.diagnostics.requested = true;
  debounceDocumentDiagnostics(session, model, document);
}

function debounceDocumentDiagnostics(session: ActiveSession, model: monaco.editor.ITextModel, document: SyncedDocument) {
  const diagnostics = document.diagnostics;
  if (diagnostics.timer) clearTimeout(diagnostics.timer);
  diagnostics.timer = setTimeout(() => {
    diagnostics.timer = undefined;
    if (diagnostics.running || !documentStillAttached(session, model.uri.toString(), document)) return;
    diagnostics.running = true;
    // At most one queued/running request per document; further events coalesce.
    session.diagnosticRequests = session.diagnosticRequests.then(async () => {
      const uri = model.uri.toString();
      if (!documentStillAttached(session, uri, document) || model.isDisposed()) return;
      await flushDidChange(model);
      await session.notifications;
      if (!documentStillAttached(session, uri, document) || model.isDisposed()) return;
      // An edit can land while native notifications are awaited. Do not label a
      // pull of the earlier server snapshot with the model's newer version.
      if (document.modelVersion !== model.getVersionId()) return;
      diagnostics.requested = false;
      if (diagnostics.timer) clearTimeout(diagnostics.timer);
      diagnostics.timer = undefined;
      const revision = diagnostics.revision;
      const version = model.getVersionId();
      const generation = session.generation;
      const previousResultId = diagnostics.resultId;
      const abort = new AbortController();
      diagnostics.abort = abort;
      const result = await sendNativeLspRequest(session.root, session.serverId, generation,
        "textDocument/diagnostic", {
          textDocument: { uri },
          ...(session.diagnosticProvider?.identifier !== undefined ? { identifier: session.diagnosticProvider.identifier } : {}),
          ...(previousResultId !== undefined ? { previousResultId } : {}),
        }, 10000, abort.signal);
      if (abort.signal.aborted || !documentStillAttached(session, uri, document) || session.generation !== generation ||
        monaco.editor.getModel(model.uri) !== model || model.isDisposed() || model.getVersionId() !== version ||
        diagnostics.revision !== revision) return;
      const report = parseDocumentDiagnosticReport(result);
      if (!report) return;
      if (report.kind === "full") {
        applyDiagnostics(session.root, session.serverId, uri, report.items);
        diagnostics.resultId = report.resultId;
      } else if (previousResultId !== undefined) {
        // Unchanged preserves the markers represented by the previous result id.
        diagnostics.resultId = report.resultId;
      }
    }).catch(() => {
      // Cancellation, timeout and invalid/failed pulls are isolated. Retry only
      // after a new open/edit/save event, never on a server's retrigger hint.
    }).finally(() => {
      diagnostics.running = false;
      diagnostics.abort = undefined;
      if (diagnostics.requested && !diagnostics.timer && documentStillAttached(session, model.uri.toString(), document)) {
        debounceDocumentDiagnostics(session, model, document);
      }
    });
  }, DIAGNOSTIC_DEBOUNCE_MS);
}

// ---------------------------------------------------------------------------
// Workspace pull diagnostics (workspace/diagnostic)
// ---------------------------------------------------------------------------
// Pulled after the session starts, after saves and edits (debounced), and on
// workspace/diagnostic/refresh. A server may hold the request open until
// something changes; the native request timeout ends such a poll and the next
// event starts a new one, so an idle project never loops.
const WORKSPACE_DIAGNOSTIC_DEBOUNCE_MS = 1500;
const WORKSPACE_DIAGNOSTIC_TIMEOUT_MS = 30000;

function sessionIsCurrent(session: ActiveSession) {
  return activeSessions.get(sessionKey(session.root, session.serverId)) === session;
}

function scheduleWorkspaceDiagnostics(session: ActiveSession, delay = WORKSPACE_DIAGNOSTIC_DEBOUNCE_MS) {
  const state = session.workspaceDiagnostics;
  if (!state || !sessionIsCurrent(session)) return;
  state.requested = true;
  if (state.running) return;
  if (state.timer) clearTimeout(state.timer);
  state.timer = setTimeout(() => {
    state.timer = undefined;
    void pullWorkspaceDiagnostics(session, state);
  }, delay);
}

async function pullWorkspaceDiagnostics(session: ActiveSession, state: WorkspaceDiagnosticsState) {
  if (state.running || !sessionIsCurrent(session)) return;
  state.running = true;
  state.requested = false;
  const abort = new AbortController();
  state.abort = abort;
  try {
    await session.notifications;
    if (!sessionIsCurrent(session)) return;
    const result = await sendNativeLspRequest(session.root, session.serverId, session.generation, "workspace/diagnostic", {
      ...(session.diagnosticProvider?.identifier !== undefined ? { identifier: session.diagnosticProvider.identifier } : {}),
      previousResultIds: [...state.resultIds].map(([uri, value]) => ({ uri, value })),
    }, WORKSPACE_DIAGNOSTIC_TIMEOUT_MS, abort.signal);
    if (abort.signal.aborted || !sessionIsCurrent(session)) return;
    for (const item of parseWorkspaceDiagnosticReport(result) ?? []) {
      // Open documents belong to the document pull, which tracks edits the
      // workspace report may not have seen yet.
      if (session.documents.has(monaco.Uri.parse(item.uri).toString())) continue;
      if (item.report.kind === "full") {
        applyDiagnostics(session.root, session.serverId, item.uri, item.report.items);
        if (item.report.resultId !== undefined) state.resultIds.set(item.uri, item.report.resultId);
        else state.resultIds.delete(item.uri);
      } else {
        state.resultIds.set(item.uri, item.report.resultId);
      }
    }
  } catch {
    // Timeouts, cancellation and server errors are isolated like document
    // pulls; the next save/edit/refresh asks again.
  } finally {
    state.running = false;
    state.abort = undefined;
    if (state.requested && sessionIsCurrent(session)) scheduleWorkspaceDiagnostics(session);
  }
}

function disposeWorkspaceDiagnostics(session: ActiveSession) {
  const state = session.workspaceDiagnostics;
  if (!state) return;
  if (state.timer) clearTimeout(state.timer);
  state.timer = undefined;
  state.requested = false;
  state.abort?.abort();
}

/** workspace/diagnostic/refresh: re-pull open documents and the workspace. */
function refreshSessionDiagnostics(root: string, serverId: string, generation: number) {
  const session = activeSessions.get(sessionKey(root, serverId));
  if (!session || session.generation !== generation) return;
  for (const uri of session.documents.keys()) {
    const model = monaco.editor.getModel(monaco.Uri.parse(uri));
    if (model) scheduleDocumentDiagnostics(session, model);
  }
  scheduleWorkspaceDiagnostics(session, 0);
}

// ---------------------------------------------------------------------------
// Markdown helpers
// ---------------------------------------------------------------------------

function toMarkdownString(content: unknown): monaco.IMarkdownString {
  if (!content) return { value: "" };
  if (typeof content === "string") return { value: content };
  if (typeof content === "object" && content !== null) {
    const c = content as Record<string, unknown>;
    if (typeof c.value === "string") return { value: c.value };
    if (typeof c.language === "string" && typeof c.value === "string") {
      return { value: `\`\`\`${c.language}\n${c.value}\n\`\`\`` };
    }
  }
  return { value: String(content) };
}

function hoverContents(rawContents: unknown): monaco.IMarkdownString[] {
  if (!rawContents) return [];
  if (Array.isArray(rawContents)) return rawContents.map(toMarkdownString).filter((m) => m.value);
  const m = toMarkdownString(rawContents);
  return m.value ? [m] : [];
}

// ---------------------------------------------------------------------------
// WorkspaceEdit conversion (used by rename + code actions)
// ---------------------------------------------------------------------------

type LspTextEdit = { range: unknown; newText: string };

function convertWorkspaceEdit(raw: unknown): monaco.languages.WorkspaceEdit | null {
  if (!raw || typeof raw !== "object") return null;
  const we = raw as {
    changes?: Record<string, LspTextEdit[]>;
    documentChanges?: Array<{
      textDocument?: { uri: string };
      edits?: LspTextEdit[];
    }>;
  };

  const edits: monaco.languages.IWorkspaceTextEdit[] = [];

  if (we.changes) {
    for (const [uri, fileEdits] of Object.entries(we.changes)) {
      for (const edit of fileEdits) {
        edits.push({
          resource: monaco.Uri.parse(uri),
          textEdit: {
            range: toMonacoRange(edit.range as Parameters<typeof toMonacoRange>[0]),
            text: edit.newText,
          },
          versionId: undefined,
        });
      }
    }
  }

  if (we.documentChanges) {
    for (const dc of we.documentChanges) {
      if (dc.textDocument?.uri && dc.edits) {
        for (const edit of dc.edits) {
          edits.push({
            resource: monaco.Uri.parse(dc.textDocument.uri),
            textEdit: {
              range: toMonacoRange(edit.range as Parameters<typeof toMonacoRange>[0]),
              text: edit.newText,
            },
            versionId: undefined,
          });
        }
      }
    }
  }

  return edits.length ? { edits } : null;
}

// ---------------------------------------------------------------------------
// Document symbol conversion
// ---------------------------------------------------------------------------

function convertDocumentSymbols(raw: unknown): monaco.languages.DocumentSymbol[] {
  if (!Array.isArray(raw)) return [];
  return (raw as Record<string, unknown>[])
    .filter(Boolean)
    .map((item) => {
      if (item.range) {
        // DocumentSymbol format (hierarchical)
        return {
          name: String(item.name ?? ""),
          detail: String(item.detail ?? ""),
          kind: (item.kind as monaco.languages.SymbolKind) ?? monaco.languages.SymbolKind.Variable,
          tags: [],
          range: toMonacoRange(item.range as Parameters<typeof toMonacoRange>[0]),
          selectionRange: toMonacoRange(
            ((item.selectionRange ?? item.range) as Parameters<typeof toMonacoRange>[0])
          ),
          children: item.children ? convertDocumentSymbols(item.children) : [],
        } as monaco.languages.DocumentSymbol;
      }
      if (item.location && typeof item.location === "object") {
        // SymbolInformation format (flat)
        const loc = item.location as { range: unknown };
        return {
          name: String(item.name ?? ""),
          detail: String(item.containerName ?? ""),
          kind: (item.kind as monaco.languages.SymbolKind) ?? monaco.languages.SymbolKind.Variable,
          tags: [],
          range: toMonacoRange(loc.range as Parameters<typeof toMonacoRange>[0]),
          selectionRange: toMonacoRange(loc.range as Parameters<typeof toMonacoRange>[0]),
          children: [],
        } as monaco.languages.DocumentSymbol;
      }
      return null;
    })
    .filter((s): s is monaco.languages.DocumentSymbol => s !== null);
}

// ---------------------------------------------------------------------------
// Signature help conversion
// ---------------------------------------------------------------------------

function convertSignatureHelp(raw: unknown): monaco.languages.SignatureHelpResult | null {
  if (!raw || typeof raw !== "object") return null;
  const sh = raw as {
    signatures?: Array<{
      label: string;
      documentation?: unknown;
      parameters?: Array<{ label: string | [number, number]; documentation?: unknown }>;
    }>;
    activeSignature?: number;
    activeParameter?: number;
  };
  if (!sh.signatures?.length) return null;
  return {
    value: {
      activeSignature: sh.activeSignature ?? 0,
      activeParameter: sh.activeParameter ?? 0,
      signatures: sh.signatures.map((sig) => ({
        label: sig.label,
        documentation: sig.documentation ? toMarkdownString(sig.documentation) : undefined,
        parameters: (sig.parameters ?? []).map((p) => ({
          label: p.label,
          documentation: p.documentation ? toMarkdownString(p.documentation) : undefined,
        })),
      })),
    },
    dispose: () => {},
  };
}

// ---------------------------------------------------------------------------
// Inlay hints conversion
// ---------------------------------------------------------------------------

function convertInlayHints(raw: unknown[]): monaco.languages.InlayHint[] {
  return raw
    .filter((h): h is Record<string, unknown> => Boolean(h && typeof h === "object"))
    .map((h) => {
      const pos = h.position as { line: number; character: number };
      const label =
        typeof h.label === "string"
          ? h.label
          : Array.isArray(h.label)
          ? (h.label as Array<{ value: string }>).map((p) => p.value).join("")
          : String(h.label ?? "");
      return {
        position: toMonacoPosition(pos),
        label,
        kind: h.kind as monaco.languages.InlayHintKind | undefined,
        tooltip: h.tooltip ? toMarkdownString(h.tooltip) : undefined,
        paddingLeft: Boolean(h.paddingLeft),
        paddingRight: Boolean(h.paddingRight),
      } as monaco.languages.InlayHint;
    });
}

// ---------------------------------------------------------------------------
// Code action conversion (edits applied directly, commands via executeCommand)
// ---------------------------------------------------------------------------

interface LspCommandPayload {
  root: string;
  serverId: string;
  generation: number;
  title: string;
  command: string;
  arguments: unknown[];
}

const LSP_RUN_COMMAND_ID = "mincode.lsp.executeCommand";
let lspRunCommandRegistered = false;

// command 类 code action 由 Monaco 命令服务回调这里：执行 workspace/executeCommand，
// 服务器若返回 WorkspaceEdit（如 organize imports）则应用到对应模型。
function ensureLspRunCommand() {
  if (lspRunCommandRegistered) return;
  lspRunCommandRegistered = true;
  registerMonacoCommand(LSP_RUN_COMMAND_ID, async (payload: unknown) => {
    const command = payload as LspCommandPayload | undefined;
    if (!command?.command) return;
    markUserAction(command.root, command.serverId);
    userCommandAt.set(sessionKey(command.root, command.serverId), Date.now());
    try {
      const result = await sendLspRequest(
        command.root, command.serverId, command.generation,
        "workspace/executeCommand",
        { command: command.command, arguments: command.arguments },
        15000
      );
      applyWorkspaceEditResult(result, isPluginLspServerId(command.serverId) ? command.root : undefined);
    } catch (error) {
      // 不影响编辑器，但用户点了命令，必须看到失败及原因（详情见语言服务器日志）。
      showLspCommandFailure(command.serverId, command.title, lspCommandFailureText(error));
    }
  });
}

function applyWorkspaceEditResult(result: unknown, confineToRoot?: string): boolean {
  const converted = convertWorkspaceEdit(result);
  const workspaceEdit = converted && confineToRoot ? confineWorkspaceEditToRoot(converted, confineToRoot) : converted;
  if (!workspaceEdit) return false;
  const byModel = new Map<monaco.editor.ITextModel, Array<{ range: monaco.Range; text: string }>>();
  for (const edit of workspaceEdit.edits) {
    if (!("textEdit" in edit)) continue;
    const model = monaco.editor.getModel(edit.resource);
    if (!model) continue;
    const list = byModel.get(model) ?? [];
    list.push({ range: monaco.Range.lift(edit.textEdit.range), text: edit.textEdit.text });
    byModel.set(model, list);
  }
  for (const [model, edits] of byModel) {
    model.pushEditOperations([], edits, () => null);
  }
  return true;
}

// convertCodeAction / convertCodeLens 暂存 [command名, 参数]；provider 知道所属会话后
// 换成完整载荷，由 LSP_RUN_COMMAND_ID 执行 workspace/executeCommand。
function bindLspCommand(command: monaco.languages.Command, session: ActiveSession) {
  const [commandName, commandArguments] = command.arguments ?? [];
  const payload: LspCommandPayload = {
    root: session.root,
    serverId: session.serverId,
    generation: session.generation,
    title: command.title,
    command: String(commandName ?? ""),
    arguments: Array.isArray(commandArguments) ? commandArguments : [],
  };
  command.arguments = [payload];
}

function convertCodeAction(raw: Record<string, unknown>): monaco.languages.CodeAction | null {
  const title = String(raw.title ?? "");
  const edit = raw.edit ? convertWorkspaceEdit(raw.edit) : null;
  const command = (raw.command && typeof raw.command === "object" ? raw.command : null) as
    | { command?: unknown; arguments?: unknown }
    | null;

  const action: monaco.languages.CodeAction = {
    title,
    isPreferred: Boolean(raw.isPreferred),
  };
  if (raw.kind) action.kind = String(raw.kind);
  if (raw.disabled) action.disabled = String((raw.disabled as Record<string, unknown>).reason ?? "");
  if (edit) action.edit = edit;
  if (command?.command) {
    action.command = {
      id: LSP_RUN_COMMAND_ID,
      title,
      arguments: [command.command, command.arguments],
    };
  }
  if (!action.edit && !action.command) return null;
  return action;
}

function isLspPosition(value: unknown): value is { line: number; character: number } {
  const position = value as { line?: unknown; character?: unknown } | null;
  return Boolean(position && typeof position === "object") &&
    Number.isInteger(position?.line) && Number(position?.line) >= 0 &&
    Number.isInteger(position?.character) && Number(position?.character) >= 0;
}

// Lens commands Monaco runs itself: navigation only, never anything that edits
// or touches the clipboard. Value: the Monaco command that actually runs.
const MONACO_LENS_NAVIGATION_COMMANDS: ReadonlyMap<string, string> = new Map([
  ["editor.action.showReferences", "editor.action.peekLocations"],
  ["editor.action.peekLocations", "editor.action.peekLocations"],
  ["editor.action.goToLocations", "editor.action.goToLocations"],
]);
const MONACO_LOCATIONS_MULTIPLE = new Set(["peek", "gotoAndPeek", "goto"]);

function lspLocationToMonaco(value: unknown): monaco.languages.Location | null {
  const location = value as { uri?: unknown; range?: { start?: unknown; end?: unknown } } | null;
  if (!location || typeof location.uri !== "string") return null;
  if (!isLspPosition(location.range?.start) || !isLspPosition(location.range?.end)) return null;
  return {
    uri: monaco.Uri.parse(location.uri),
    range: toMonacoRange({ start: location.range.start, end: location.range.end }),
  };
}

/**
 * Allowlisted navigation lens commands, args (uri, position, locations[,
 * multiple]) converted from LSP. Malformed args give a plain label; null
 * leaves any other command to the server.
 */
function monacoLensCommand(name: string, title: string, args: unknown): monaco.languages.Command | null {
  const id = MONACO_LENS_NAVIGATION_COMMANDS.get(name);
  if (!id) return null;
  const [uri, position, locations, multiple] = Array.isArray(args) ? args : [];
  if (typeof uri !== "string" || !isLspPosition(position) || !Array.isArray(locations) || !hasMonacoCommand(id)) {
    return { id: "", title };
  }
  return {
    id,
    title,
    arguments: [
      monaco.Uri.parse(uri),
      toMonacoPosition(position),
      locations.map(lspLocationToMonaco).filter((item): item is monaco.languages.Location => item !== null),
      ...(typeof multiple === "string" && MONACO_LOCATIONS_MULTIPLE.has(multiple) ? [multiple] : []),
    ],
  };
}

/**
 * LSP CodeLens → Monaco CodeLens. Allowlisted navigation commands run in
 * Monaco; any other command goes through LSP_RUN_COMMAND_ID like a code
 * action; a command without a name is a plain label.
 */
function convertCodeLens(raw: unknown): monaco.languages.CodeLens | null {
  if (!raw || typeof raw !== "object") return null;
  const item = raw as { range?: { start?: unknown; end?: unknown }; command?: unknown };
  if (!isLspPosition(item.range?.start) || !isLspPosition(item.range?.end)) return null;
  const lens: monaco.languages.CodeLens = {
    range: toMonacoRange({ start: item.range.start, end: item.range.end }),
  };
  const command = (item.command && typeof item.command === "object" ? item.command : null) as
    | { title?: unknown; command?: unknown; arguments?: unknown }
    | null;
  if (command) {
    const title = String(command.title ?? "");
    const name = typeof command.command === "string" ? command.command : "";
    lens.command = !name
      ? { id: "", title }
      : monacoLensCommand(name, title, command.arguments)
        ?? { id: LSP_RUN_COMMAND_ID, title, arguments: [name, command.arguments] };
  }
  return lens;
}

// ---------------------------------------------------------------------------
// #10 — serverCapabilities helper
// ---------------------------------------------------------------------------

function parseCapabilities(raw: unknown): ServerCapabilities {
  const c = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
  return {
    hover: Boolean(c.hoverProvider),
    completion: Boolean(c.completionProvider),
    definition: Boolean(c.definitionProvider),
    references: Boolean(c.referencesProvider),
    formatting: Boolean(c.documentFormattingProvider),
    signatureHelp: Boolean(c.signatureHelpProvider),
    rename: Boolean(c.renameProvider),
    codeAction: Boolean(c.codeActionProvider),
    codeLens: Boolean(c.codeLensProvider),
    codeLensResolve: Boolean((c.codeLensProvider as { resolveProvider?: unknown } | undefined)?.resolveProvider),
    inlayHints: Boolean(c.inlayHintProvider),
    documentHighlight: Boolean(c.documentHighlightProvider),
    implementation: Boolean(c.implementationProvider),
    typeDefinition: Boolean(c.typeDefinitionProvider),
    documentSymbol: Boolean(c.documentSymbolProvider),
    semanticTokens: Boolean(parseSemanticTokensProvider(c.semanticTokensProvider)),
  };
}

// LSP 补全项 → Monaco 建议。sortOffset 用于多会话合并时分配全局排序权重，
// 避免不同会话各自的 sortText 序号互相碰撞。
function convertCompletionItems(
  model: monaco.editor.ITextModel,
  position: monaco.Position,
  rawItems: unknown[],
  sortOffset: number
): monaco.languages.CompletionItem[] {
  return (rawItems as Record<string, unknown>[]).map(
    (item, i) => {
      const label = String(item.label ?? "");
      const kind = (item.kind as number) ?? 1;
      // 优先用服务器给的 textEdit（含精确替换范围），再退回 insertText/label。
      const textEdit = (item.textEdit && typeof item.textEdit === "object" ? item.textEdit : null) as
        | { range?: unknown; newText?: unknown }
        | null;
      const insertText = String(textEdit?.newText ?? item.insertText ?? item.label ?? "");
      const word = model.getWordUntilPosition(position);
      const range = textEdit?.range
        ? toMonacoRange(textEdit.range as Parameters<typeof toMonacoRange>[0])
        : new monaco.Range(
            position.lineNumber, word.startColumn,
            position.lineNumber, word.endColumn
          );
      const suggestion: monaco.languages.CompletionItem = {
        label,
        kind: Math.max(0, kind - 1) as monaco.languages.CompletionItemKind,
        insertText,
        range,
        detail: item.detail ? String(item.detail) : undefined,
        documentation: item.documentation ? toMarkdownString(item.documentation) : undefined,
        sortText: item.sortText ? String(item.sortText) : `${sortOffset + i}`.padStart(6, "0"),
        filterText: item.filterText ? String(item.filterText) : undefined,
      };
      // insertTextFormat=2 表示 snippet 语法（占位符/选择项）。
      if (item.insertTextFormat === 2) {
        suggestion.insertTextRules = monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet;
      }
      // additionalTextEdits 承载“补全即导入”等额外编辑，缺失会导致自动导入失效。
      // Monaco 运行时按 {range, text} 扁平形状应用（executeEdits），类型声明与运行时不一致，用断言兼容。
      const additional = Array.isArray(item.additionalTextEdits) ? item.additionalTextEdits : [];
      const additionalEdits = additional
        .filter((e): e is Record<string, unknown> => Boolean(e && typeof e === "object"))
        .map((e) => ({
          range: toMonacoRange(e.range as Parameters<typeof toMonacoRange>[0]),
          text: String(e.newText ?? ""),
        }));
      if (additionalEdits.length > 0) {
        suggestion.additionalTextEdits =
          additionalEdits as unknown as monaco.editor.ISingleEditOperation[];
      }
      return suggestion;
    }
  );
}

// ---------------------------------------------------------------------------
// Monaco provider registration per language (registered once, gated by caps)
// ---------------------------------------------------------------------------

function ensureLanguageProviders(languageId: string) {
  if (languageProviders.has(languageId)) return;
  const disposables: monaco.IDisposable[] = [];

  // Helper: build AbortController tied to CancellationToken
  function abortable(token: monaco.CancellationToken) {
    const ctrl = new AbortController();
    token.onCancellationRequested(() => ctrl.abort());
    return ctrl;
  }

  // — Hover（合并所有服务该语言的会话结果，主服务器内容在前） -----
  disposables.push(
    monaco.languages.registerHoverProvider(languageId, {
      async provideHover(model, position, token) {
        const sessions = getSessionsForModel(model).filter((s) => s.capabilities.hover);
        if (!sessions.length) return null;
        const ctrl = abortable(token);
        const results = await Promise.all(
          sessions.map((session) =>
            sendLspRequest(
              session.root, session.serverId, session.generation,
              "textDocument/hover",
              { textDocument: { uri: model.uri.toString() }, position: toLspPosition(position) },
              5000, ctrl.signal
            ).catch(() => null)
          )
        );
        const contents: monaco.IMarkdownString[] = [];
        let firstRange: unknown;
        for (const result of results) {
          if (!result) continue;
          const r = result as { contents?: unknown; range?: unknown };
          contents.push(...hoverContents(r.contents));
          if (firstRange == null && r.range) firstRange = r.range;
        }
        if (!contents.length) return null;
        return {
          contents,
          range: firstRange
            ? toMonacoRange(firstRange as Parameters<typeof toMonacoRange>[0])
            : undefined,
        };
      },
    })
  );

  // — Completion（合并多会话：如 css-ls 的属性 + Tailwind 的类名） -------
  disposables.push(
    monaco.languages.registerCompletionItemProvider(languageId, {
      triggerCharacters: [".", ":", "@", "<", "(", "[", "#", " "],
      async provideCompletionItems(model, position, _context, token) {
        const sessions = getSessionsForModel(model).filter((s) => s.capabilities.completion);
        if (!sessions.length) return null;
        const ctrl = abortable(token);
        const results = await Promise.all(
          sessions.map((session) =>
            sendLspRequest(
              session.root, session.serverId, session.generation,
              "textDocument/completion",
              { textDocument: { uri: model.uri.toString() }, position: toLspPosition(position) },
              10000, ctrl.signal
            ).catch(() => null)
          )
        );
        const suggestions: monaco.languages.CompletionItem[] = [];
        for (const result of results) {
          if (!result) continue;
          const r = result as unknown[] | { items?: unknown[] };
          const rawItems = Array.isArray(r) ? r : (r.items ?? []);
          suggestions.push(...convertCompletionItems(model, position, rawItems, suggestions.length));
        }
        return { suggestions };
      },
    })
  );

  // — Signature help -------------------------------------------------------
  disposables.push(
    monaco.languages.registerSignatureHelpProvider(languageId, {
      signatureHelpTriggerCharacters: ["(", ",", "<"],
      signatureHelpRetriggerCharacters: [","],
      async provideSignatureHelp(model, position, token) {
        // 单 owner：签名帮助只取优先级最高的有能力会话。
        const session = firstCapableSession(getSessionsForModel(model), "signatureHelp");
        if (!session) return null;
        const ctrl = abortable(token);
        try {
          const result = await sendLspRequest(
            session.root, session.serverId, session.generation,
            "textDocument/signatureHelp",
            { textDocument: { uri: model.uri.toString() }, position: toLspPosition(position) },
            5000, ctrl.signal
          );
          return convertSignatureHelp(result);
        } catch { return null; }
      },
    })
  );

  // — Definition -----------------------------------------------------------
  disposables.push(
    monaco.languages.registerDefinitionProvider(languageId, {
      async provideDefinition(model, position, token) {
        const sessions = getSessionsForModel(model).filter((s) => s.capabilities.definition);
        if (!sessions.length) return null;
        const ctrl = abortable(token);
        const results = await Promise.all(
          sessions.map((session) =>
            sendLspRequest(
              session.root, session.serverId, session.generation,
              "textDocument/definition",
              { textDocument: { uri: model.uri.toString() }, position: toLspPosition(position) },
              10000, ctrl.signal
            ).catch(() => null)
          )
        );
        return mergeLocations(results);
      },
    })
  );

  // — Type definition ------------------------------------------------------
  disposables.push(
    monaco.languages.registerTypeDefinitionProvider(languageId, {
      async provideTypeDefinition(model, position, token) {
        const sessions = getSessionsForModel(model).filter((s) => s.capabilities.typeDefinition);
        if (!sessions.length) return null;
        const ctrl = abortable(token);
        const results = await Promise.all(
          sessions.map((session) =>
            sendLspRequest(
              session.root, session.serverId, session.generation,
              "textDocument/typeDefinition",
              { textDocument: { uri: model.uri.toString() }, position: toLspPosition(position) },
              10000, ctrl.signal
            ).catch(() => null)
          )
        );
        return mergeLocations(results);
      },
    })
  );

  // — Implementation -------------------------------------------------------
  disposables.push(
    monaco.languages.registerImplementationProvider(languageId, {
      async provideImplementation(model, position, token) {
        const sessions = getSessionsForModel(model).filter((s) => s.capabilities.implementation);
        if (!sessions.length) return null;
        const ctrl = abortable(token);
        const results = await Promise.all(
          sessions.map((session) =>
            sendLspRequest(
              session.root, session.serverId, session.generation,
              "textDocument/implementation",
              { textDocument: { uri: model.uri.toString() }, position: toLspPosition(position) },
              10000, ctrl.signal
            ).catch(() => null)
          )
        );
        return mergeLocations(results);
      },
    })
  );

  // — References -----------------------------------------------------------
  disposables.push(
    monaco.languages.registerReferenceProvider(languageId, {
      async provideReferences(model, position, _context, token) {
        const sessions = getSessionsForModel(model).filter((s) => s.capabilities.references);
        if (!sessions.length) return null;
        const ctrl = abortable(token);
        const results = await Promise.all(
          sessions.map((session) =>
            sendLspRequest(
              session.root, session.serverId, session.generation,
              "textDocument/references",
              {
                textDocument: { uri: model.uri.toString() },
                position: toLspPosition(position),
                context: { includeDeclaration: true },
              },
              10000, ctrl.signal
            ).catch(() => null)
          )
        );
        return mergeLocations(results);
      },
    })
  );

  // — Document highlight ---------------------------------------------------
  disposables.push(
    monaco.languages.registerDocumentHighlightProvider(languageId, {
      async provideDocumentHighlights(model, position, token) {
        const sessions = getSessionsForModel(model).filter((s) => s.capabilities.documentHighlight);
        if (!sessions.length) return null;
        const ctrl = abortable(token);
        const results = await Promise.all(
          sessions.map((session) =>
            sendLspRequest(
              session.root, session.serverId, session.generation,
              "textDocument/documentHighlight",
              { textDocument: { uri: model.uri.toString() }, position: toLspPosition(position) },
              5000, ctrl.signal
            ).catch(() => null)
          )
        );
        const highlights: monaco.languages.DocumentHighlight[] = [];
        for (const result of results) {
          const list = result as Array<{ range: unknown; kind?: number }> | null;
          if (!list) continue;
          for (const h of list) {
            highlights.push({
              range: toMonacoRange(h.range as Parameters<typeof toMonacoRange>[0]),
              kind: h.kind as monaco.languages.DocumentHighlightKind ?? monaco.languages.DocumentHighlightKind.Text,
            });
          }
        }
        return highlights.length ? highlights : null;
      },
    })
  );

  // — Rename ---------------------------------------------------------------
  disposables.push(
    monaco.languages.registerRenameProvider(languageId, {
      async provideRenameEdits(model, position, newName, token) {
        // 单 owner：重命名只由优先级最高的有能力会话执行，避免多会话重复改写。
        const session = firstCapableSession(getSessionsForModel(model), "rename");
        if (!session) return null;
        markUserAction(session.root, session.serverId);
        const ctrl = abortable(token);
        try {
          const result = await sendLspRequest(
            session.root, session.serverId, session.generation,
            "textDocument/rename",
            { textDocument: { uri: model.uri.toString() }, position: toLspPosition(position), newName },
            10000, ctrl.signal
          );
          return convertWorkspaceEdit(result);
        } catch { return null; }
      },
    })
  );

  // — Code actions ---------------------------------------------------------
  disposables.push(
    monaco.languages.registerCodeActionProvider(languageId, {
      async provideCodeActions(model, range, context, token) {
        const sessions = getSessionsForModel(model).filter((s) => s.capabilities.codeAction);
        if (!sessions.length) return null;
        const ctrl = abortable(token);
        const lspDiagnostics = context.markers.map((m) => ({
          range: {
            start: { line: m.startLineNumber - 1, character: m.startColumn - 1 },
            end: { line: m.endLineNumber - 1, character: m.endColumn - 1 },
          },
          message: m.message,
          severity: Object.entries(LSP_SEVERITY_MAP).find(([, v]) => v === m.severity)?.[0] ?? 1,
          source: m.source,
          code: m.code,
        }));
        const results = await Promise.all(
          sessions.map((session) =>
            sendLspRequest(
              session.root, session.serverId, session.generation,
              "textDocument/codeAction",
              {
                textDocument: { uri: model.uri.toString() },
                range: toLspRange(range),
                context: { diagnostics: lspDiagnostics },
              },
              10000, ctrl.signal
            ).catch(() => null)
          )
        );
        const actions: monaco.languages.CodeAction[] = [];
        for (let index = 0; index < results.length; index += 1) {
          const raw = results[index] as Record<string, unknown>[] | null;
          if (!raw) continue;
          const session = sessions[index];
          for (const item of raw) {
            if (!item) continue;
            const action = convertCodeAction(item);
            if (!action) continue;
            if (action.command) bindLspCommand(action.command, session);
            actions.push(action);
          }
        }
        return { actions, dispose: () => {} };
      },
    })
  );

  // — CodeLens（合并所有会话；缺少命令的 lens 在可见时由所属会话 resolve） ----
  const codeLensOrigins = new WeakMap<monaco.languages.CodeLens, { session: ActiveSession; raw: unknown }>();
  const codeLensProvider: monaco.languages.CodeLensProvider = {
    // Monaco 的 onDidChange 回传 provider 本身；共享事件按语言转发，
    // 第三个 disposables 参数（Monaco 事件约定）原样传给共享事件。
    onDidChange: (listener, thisArg, disposables?: unknown) =>
      (codeLensesChanged.event as MonacoEvent)(() => listener.call(thisArg, codeLensProvider), undefined, disposables),
    async provideCodeLenses(model, token) {
      const sessions = getSessionsForModel(model).filter((s) => s.capabilities.codeLens);
      if (!sessions.length) return null;
      const ctrl = abortable(token);
      const results = await Promise.all(
        sessions.map((session) =>
          sendLspRequest(
            session.root, session.serverId, session.generation,
            "textDocument/codeLens",
            { textDocument: { uri: model.uri.toString() } },
            10000, ctrl.signal
          ).catch(() => null)
        )
      );
      const lenses: monaco.languages.CodeLens[] = [];
      for (let index = 0; index < results.length; index += 1) {
        const raw = results[index];
        if (!Array.isArray(raw)) continue;
        const session = sessions[index];
        for (const item of raw) {
          const lens = convertCodeLens(item);
          if (!lens) continue;
          if (lens.command?.id === LSP_RUN_COMMAND_ID) bindLspCommand(lens.command, session);
          // Monaco 不显示没有命令的 lens；服务器不能 resolve 时直接丢弃。
          else if (!lens.command && !session.capabilities.codeLensResolve) continue;
          codeLensOrigins.set(lens, { session, raw: item });
          lenses.push(lens);
        }
      }
      return { lenses, dispose: () => {} };
    },
    async resolveCodeLens(_model, codeLens, token) {
      const origin = codeLensOrigins.get(codeLens);
      if (codeLens.command || !origin?.session.capabilities.codeLensResolve) return codeLens;
      const { session, raw } = origin;
      const ctrl = abortable(token);
      try {
        const result = await sendLspRequest(
          session.root, session.serverId, session.generation,
          "codeLens/resolve", raw, 10000, ctrl.signal
        );
        // range 在 resolve 结果里可省略，沿用原 lens 的范围。
        const resolved = convertCodeLens(result && typeof result === "object" && (result as { range?: unknown }).range == null
          ? { ...result, range: (raw as { range?: unknown }).range }
          : result);
        if (!resolved) return codeLens;
        if (resolved.command?.id === LSP_RUN_COMMAND_ID) bindLspCommand(resolved.command, session);
        return resolved;
      } catch { return codeLens; }
    },
  };
  disposables.push(monaco.languages.registerCodeLensProvider(languageId, codeLensProvider));

  // — Document formatting --------------------------------------------------
  disposables.push(
    monaco.languages.registerDocumentFormattingEditProvider(languageId, {
      async provideDocumentFormattingEdits(model, options, token) {
        // 单 owner：格式化只由优先级最高的有能力会话执行。
        const session = firstCapableSession(getSessionsForModel(model), "formatting");
        if (!session) return null;
        const ctrl = abortable(token);
        try {
          const result = (await sendLspRequest(
            session.root, session.serverId, session.generation,
            "textDocument/formatting",
            {
              textDocument: { uri: model.uri.toString() },
              options: { tabSize: options.tabSize, insertSpaces: options.insertSpaces },
            },
            15000, ctrl.signal
          )) as Array<{ range: unknown; newText: string }> | null;
          if (!result) return [];
          return result.map((edit) => ({
            range: toMonacoRange(edit.range as Parameters<typeof toMonacoRange>[0]),
            text: edit.newText,
          }));
        } catch { return null; }
      },
    })
  );

  // — Inlay hints ----------------------------------------------------------
  disposables.push(
    monaco.languages.registerInlayHintsProvider(languageId, {
      async provideInlayHints(model, range, token) {
        const sessions = getSessionsForModel(model).filter((s) => s.capabilities.inlayHints);
        if (!sessions.length) return null;
        const ctrl = abortable(token);
        const results = await Promise.all(
          sessions.map((session) =>
            sendLspRequest(
              session.root, session.serverId, session.generation,
              "textDocument/inlayHint",
              { textDocument: { uri: model.uri.toString() }, range: toLspRange(range) },
              10000, ctrl.signal
            ).catch(() => null)
          )
        );
        const hints: monaco.languages.InlayHint[] = [];
        for (const result of results) {
          if (result) hints.push(...convertInlayHints(result as unknown[]));
        }
        return { hints, dispose: () => {} };
      },
    })
  );

  // — Document symbols -----------------------------------------------------
  disposables.push(
    monaco.languages.registerDocumentSymbolProvider(languageId, {
      async provideDocumentSymbols(model, token) {
        // 单 owner：文档符号树只取优先级最高的有能力会话，避免多份结构重复。
        const session = firstCapableSession(getSessionsForModel(model), "documentSymbol");
        if (!session) return null;
        const ctrl = abortable(token);
        try {
          const result = await sendLspRequest(
            session.root, session.serverId, session.generation,
            "textDocument/documentSymbol",
            { textDocument: { uri: model.uri.toString() } },
            10000, ctrl.signal
          );
          return convertDocumentSymbols(result);
        } catch { return null; }
      },
    })
  );

  // — Semantic tokens（单 owner；服务器图例统一映射到主题规则作用域） ---------
  const semanticLegend: monaco.languages.SemanticTokensLegend = {
    tokenTypes: [...MONACO_SEMANTIC_SCOPES],
    tokenModifiers: [],
  };
  disposables.push(
    monaco.languages.registerDocumentSemanticTokensProvider(languageId, {
      onDidChange: semanticTokensChanged.event,
      getLegend: () => semanticLegend,
      async provideDocumentSemanticTokens(model, _lastResultId, token) {
        const session = firstCapableSession(getSessionsForModel(model), "semanticTokens");
        if (!session?.semanticScopes) return null;
        const ctrl = abortable(token);
        try {
          const result = await sendLspRequest(
            session.root, session.serverId, session.generation,
            "textDocument/semanticTokens/full",
            { textDocument: { uri: model.uri.toString() } },
            10000, ctrl.signal
          );
          const data = (result as { data?: unknown } | null)?.data;
          if (!Array.isArray(data)) return null;
          return { data: remapSemanticTokens(data as number[], session.semanticScopes) };
        } catch { return null; }
      },
      releaseDocumentSemanticTokens() {},
    })
  );

  languageProviders.set(languageId, disposables);
}

// ---------------------------------------------------------------------------
// Server-to-client request handler
// ---------------------------------------------------------------------------
// LSP servers can initiate requests (id + method both set). If not replied to,
// the server times out and degrades: Volar drops workspace/configuration,
// gopls can't register file watchers, refactor code actions fail.

async function handleServerRequest(event: LspEvent & { requestId: number }) {
  const { root, serverId, generation, method, params, requestId } = event;
  let result: unknown;

  switch (method) {
    case "workspace/configuration": {
      // null lets a server use its defaults; a few built-in servers get schema settings.
      const items = (params as { items?: unknown } | null)?.items;
      result = Array.isArray(items)
        ? items.map((item) => lspConfigurationValue(serverId, (item as { section?: unknown } | null)?.section))
        : [];
      break;
    }
    case "workspace/applyEdit": {
      // Server requests the client to apply a WorkspaceEdit (e.g. rename-file).
      const we = (params as { edit?: unknown } | null)?.edit;
      if (isPluginLspServerId(serverId)) {
        // A plugin server may only edit shortly after a user action on its
        // session (rename, code action command), and only files under its root.
        const allowed = pluginApplyEditAllowed(userActionAt.get(sessionKey(root, serverId)), Date.now());
        result = allowed && we ? { applied: applyWorkspaceEditResult(we, root) } : { applied: false, failureReason: "not allowed" };
        break;
      }
      const edit = we ? convertWorkspaceEdit(we) : null;
      if (edit) applyWorkspaceEditResult(we);
      result = { applied: edit != null };
      break;
    }
    case "workspace/semanticTokens/refresh":
      semanticTokensChanged.fire();
      result = null;
      break;
    case "workspace/codeLens/refresh":
      codeLensesChanged.fire();
      result = null;
      break;
    case "workspace/diagnostic/refresh":
      refreshSessionDiagnostics(root, serverId, generation);
      result = null;
      break;
    case "client/registerCapability":
    case "client/unregisterCapability":
      // Only workspace/didChangeWatchedFiles is advertised as dynamic; the Go
      // session applies those registrations itself, so acknowledging is enough.
      result = null;
      break;
    default:
      result = null;
  }

  try {
    await replyLspRequest(root, serverId, generation, requestId, result);
  } catch {
    // best effort — server will time out if we can't reply, but that's better
    // than an unhandled rejection propagating up
  }
}

function lspLocationsToMonaco(result: unknown): monaco.languages.Location[] | null {
  if (!result) return null;
  const items = Array.isArray(result) ? result : [result];
  return (items as Record<string, unknown>[])
    .filter((loc) => loc && typeof loc === "object" && typeof loc.uri === "string")
    .map((loc) => ({
      uri: monaco.Uri.parse(loc.uri as string),
      range: toMonacoRange(loc.range as Parameters<typeof toMonacoRange>[0]),
    }));
}

// 多会话查询结果合并成一份位置列表（定义/引用/实现等跳转类能力共用）。
function mergeLocations(results: unknown[]): monaco.languages.Location[] | null {
  const locations: monaco.languages.Location[] = [];
  for (const result of results) {
    const found = lspLocationsToMonaco(result);
    if (found) locations.push(...found);
  }
  return locations.length ? locations : null;
}

// ---------------------------------------------------------------------------
// Document synchronization
// ---------------------------------------------------------------------------

function nextDocVersion(uri: string) {
  const current = documentVersions.get(uri);
  const next = (current?.version ?? 0) + 1;
  documentVersions.set(uri, { uri, version: next });
  return next;
}

// Serialize native notifications, including open/change/save/close, so the
// old-text baseline never outruns the server when IPC calls overlap.
function queueSessionNotification(session: ActiveSession, operation: () => Promise<void>): Promise<void> {
  const pending = session.notifications.then(operation);
  session.notifications = pending;
  return pending;
}

function documentSyncFailed(session: ActiveSession) {
  // Do not log document contents or raw server errors. A failed transport must
  // not keep serving providers against a stale document snapshot.
  setLspRuntimeState(session.root, session.serverId, { phase: "failed", failure: "sync" });
  if (activeSessions.get(sessionKey(session.root, session.serverId)) === session) {
    void stopSession(session.root, session.serverId);
  }
}

function didOpen(session: ActiveSession, model: monaco.editor.ITextModel) {
  const uri = model.uri.toString();
  const textDocument = {
    uri, languageId: lspDocumentLanguageId(model.getLanguageId(), model.uri.path), version: nextDocVersion(uri), text: model.getValue(),
  };
  session.documents.set(uri, {
    text: textDocument.text, modelVersion: model.getVersionId(),
    diagnostics: { revision: 0, requested: false, running: false },
  });
  // Diagnostics pushed or pulled while the file was closed show immediately.
  const known = useProjectProblemsStore().get(session.root, session.serverId, uri);
  if (known.length) setDiagnosticMarkers(session.serverId, uri, known);
  scheduleDocumentDiagnostics(session, model);
  if (!session.sync.openClose) return;
  void queueSessionNotification(session, () => sendLspNotify(
    session.root, session.serverId, session.generation, "textDocument/didOpen", { textDocument },
  )).catch(() => documentSyncFailed(session));
}

async function didClose(session: ActiveSession, uri: string) {
  const document = session.documents.get(uri);
  if (document) disposeDocumentDiagnostics(document);
  session.documents.delete(uri);
  // Clear before awaiting transport, so an old close cannot erase markers from
  // a newly opened model/session using the same URI and owner.
  const model = monaco.editor.getModel(monaco.Uri.parse(uri));
  if (model) monaco.editor.setModelMarkers(model, lspMarkerOwner(session.serverId), []);
  if (session.diagnosticProvider) {
    // Document-pull results describe the open buffer only; a workspace pull
    // re-reports the file from disk, otherwise its problems end with the close.
    if (session.workspaceDiagnostics) scheduleWorkspaceDiagnostics(session);
    else useProjectProblemsStore().set(session.root, session.serverId, uri, []);
  }
  if (session.sync.openClose) {
    await queueSessionNotification(session, () => sendLspNotify(
      session.root, session.serverId, session.generation, "textDocument/didClose", { textDocument: { uri } },
    )).catch(() => {
      // Closing is best effort once the session/document is detached.
    });
  }
  // Versions are shared across sessions and remain until model detach.
}

// ---------------------------------------------------------------------------
// Per-model change debounce
// ---------------------------------------------------------------------------
const changeTimers = new Map<string, ReturnType<typeof setTimeout>>();

// 内容监听每模型只装一个，变更时动态枚举当前服务该语言的所有会话扇出
// didChange，保证后启动的辅助型会话也能收到后续编辑。
async function flushDidChange(model: monaco.editor.ITextModel): Promise<void> {
  const uri = model.uri.toString();
  const timer = changeTimers.get(uri);
  if (timer) clearTimeout(timer);
  changeTimers.delete(uri);
  const text = model.getValue();
  const modelVersion = model.getVersionId();
  const version = nextDocVersion(uri);
  await Promise.all(getSessionsForModel(model).map((session) => {
    const document = session.documents.get(uri);
    if (!document) return;
    return queueSessionNotification(session, async () => {
      const changed = document.modelVersion !== modelVersion;
      const contentChanges = documentContentChanges(session.sync.change, document.text, text);
      if (contentChanges.length) {
        await sendLspNotify(session.root, session.serverId, session.generation, "textDocument/didChange", {
          textDocument: { uri, version }, contentChanges,
        });
        document.text = text;
      }
      document.modelVersion = modelVersion;
      if (changed) {
        scheduleDocumentDiagnostics(session, model);
        scheduleWorkspaceDiagnostics(session);
      }
    }).catch(() => documentSyncFailed(session));
  }));
}

// Provider requests must not read the server's pre-debounce snapshot either.
async function sendLspRequest(
  root: string, serverId: string, generation: number, method: string, params: unknown,
  timeoutMs?: number, signal?: AbortSignal,
): Promise<unknown> {
  const session = activeSessions.get(sessionKey(root, serverId));
  const uri = (params as { textDocument?: { uri?: string } } | null)?.textDocument?.uri;
  const model = uri ? monaco.editor.getModel(monaco.Uri.parse(uri)) : null;
  if (model) await flushDidChange(model);
  if (session) await session.notifications;
  if (!session || activeSessions.get(sessionKey(root, serverId)) !== session || session.generation !== generation) {
    throw new Error("LSP session is no longer active");
  }
  if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
  return sendNativeLspRequest(root, serverId, generation, method, params, timeoutMs, signal);
}

function scheduleDidChange(model: monaco.editor.ITextModel) {
  const uri = model.uri.toString();
  const timer = changeTimers.get(uri);
  if (timer) clearTimeout(timer);
  changeTimers.set(uri, setTimeout(() => {
    void flushDidChange(model);
  }, 300));
}

// ---------------------------------------------------------------------------
// Session start / stop
// ---------------------------------------------------------------------------
const modelListeners = new Map<string, monaco.IDisposable>();

function openDocumentSet(session: ActiveSession): Set<string> {
  const key = sessionKey(session.root, session.serverId);
  let uris = openDocuments.get(key);
  if (!uris) {
    uris = new Set();
    openDocuments.set(key, uris);
  }
  return uris;
}

function clearIdleStopTimer(root: string, serverId: string) {
  const key = sessionKey(root, serverId);
  const timer = idleStopTimers.get(key);
  if (!timer) return;
  clearTimeout(timer);
  idleStopTimers.delete(key);
}

function scheduleIdleSessionStop(session: ActiveSession) {
  const key = sessionKey(session.root, session.serverId);
  clearIdleStopTimer(session.root, session.serverId);
  if ((openDocuments.get(key)?.size ?? 0) > 0) return;
  // Held (e.g. jdtls hosting a Java debug session): never idle-stopped.
  if (isLspServerHeld(session.root, session.serverId)) return;

  const limits = lspRuntimeLimits();
  const delay = session.auxiliary ? limits.auxiliaryIdleStopMs : limits.idleStopMs;
  // 0 表示不限制：空闲会话保持存活，不调度回收。
  if (delay <= 0) return;
  idleStopTimers.set(
    key,
    setTimeout(() => {
      idleStopTimers.delete(key);
      const current = activeSessions.get(key);
      if (!current || current.generation !== session.generation) return;
      if ((openDocuments.get(key)?.size ?? 0) > 0 || isLspServerHeld(current.root, current.serverId)) return;
      void stopSession(current.root, current.serverId);
    }, delay)
  );
}

function attachModel(session: ActiveSession, model: monaco.editor.ITextModel) {
  clearIdleStopTimer(session.root, session.serverId);
  const uriStr = model.uri.toString();
  const uris = openDocumentSet(session);
  if (uris.has(uriStr)) return; // 该会话已同步此文档
  uris.add(uriStr);
  if (!modelListeners.has(uriStr)) {
    modelListeners.set(uriStr, model.onDidChangeContent(() => scheduleDidChange(model)));
  }
  didOpen(session, model);
}

function detachModel(uri: string) {
  modelListeners.get(uri)?.dispose();
  modelListeners.delete(uri);
  const timer = changeTimers.get(uri);
  if (timer) { clearTimeout(timer); changeTimers.delete(uri); }
  // 向所有已同步该文档的会话发 didClose，再回收共享版本号。
  for (const [key, uris] of openDocuments) {
    if (!uris.has(uri)) continue;
    uris.delete(uri);
    const session = activeSessions.get(key);
    if (session) {
      didClose(session, uri);
      scheduleIdleSessionStop(session);
    }
  }
  documentVersions.delete(uri);
}

async function startSession(root: string, serverId: string) {
  const key = sessionKey(root, serverId);
  if (activeSessions.has(key) || startingSessions.has(key)) return;
  startingSessions.add(key);
  const plugin = isPluginLspServerId(serverId);
  let started = false;
  setLspRuntimeState(root, serverId, { phase: "starting" });
  try {
    let result: Awaited<ReturnType<typeof startLspServer>>;
    if (plugin) {
      // Plugin servers start only through the plugin host, never prompting here.
      if (!servedLanguages(serverId, declaredLanguages(serverId)).length) {
        setLspRuntimeState(root, serverId, { phase: "stopped" });
        return;
      }
      try {
        result = await startPluginLanguageServer(serverId, root);
      } catch (error) {
        recordPluginStartError(root, serverId, error);
        setLspRuntimeState(root, serverId, { phase: "failed", failure: "start" });
        return;
      }
    } else {
      result = await startLspServer(root, serverId);
    }
    if (!result.running || result.generation == null) {
      setLspRuntimeState(root, serverId, { phase: "failed", failure: "start" });
      return;
    }
    started = true;
    setLspRuntimeState(root, serverId, { phase: "initializing" });
    if (!plugin && typeof result.name === "string") setLspServerName(root, serverId, result.name);

    if (!plugin) {
      serverLanguageMap.set(serverId, result.languages);
      serverAuxiliaryMap.set(serverId, Boolean(result.auxiliary ?? serverAuxiliaryMap.get(serverId)));
    }

    const rootUri = projectFileUri(root, "").toString();
    const basename = root.replace(/\\/g, "/").replace(/\/+$/, "").split("/").pop() ?? root;

    // Only a valid initialize result AND an accepted initialized notification
    // allow this session into the provider/document registries.
    const initResult = await sendNativeLspRequest(root, serverId, result.generation, "initialize", {
        processId: null,
        clientInfo: { name: "mincode" },
        rootUri,
        workspaceFolders: [{ uri: rootUri, name: basename }],
        // The native Vue adapter supplies the validated standalone version and SDK.
        capabilities: {
          general: { positionEncodings: ["utf-16"] },
          textDocument: {
            synchronization: { willSave: false, willSaveWaitUntil: false, didSave: true },
            completion: { completionItem: { snippetSupport: true, documentationFormat: ["markdown", "plaintext"] } },
            hover: { contentFormat: ["markdown", "plaintext"] },
            signatureHelp: { signatureInformation: { documentationFormat: ["markdown", "plaintext"], parameterInformation: { labelOffsetSupport: true } } },
            definition: {},
            typeDefinition: {},
            implementation: {},
            references: {},
            documentHighlight: {},
            documentSymbol: { hierarchicalDocumentSymbolSupport: true },
            semanticTokens: {
              requests: { full: true },
              tokenTypes: LSP_SEMANTIC_TOKEN_TYPES,
              tokenModifiers: LSP_SEMANTIC_TOKEN_MODIFIERS,
              formats: ["relative"],
              multilineTokenSupport: false,
              overlappingTokenSupport: false,
            },
            rename: { prepareSupport: false },
            formatting: {},
            inlayHint: {},
            codeAction: { codeActionLiteralSupport: { codeActionKind: { valueSet: ["", "quickfix", "refactor", "source"] } } },
            codeLens: {},
            publishDiagnostics: { relatedInformation: false },
            diagnostic: { dynamicRegistration: false, relatedDocumentSupport: false },
          },
          // window/workDoneProgress/create is acknowledged (handleServerRequest)
          // and $/progress drives the status-bar progress.
          window: { workDoneProgress: true },
          workspace: {
              applyEdit: true,
              workspaceFolders: true,
              configuration: true,
              // The Go session mirrors these registrations and watches the root natively.
              didChangeWatchedFiles: { dynamicRegistration: true, relativePatternSupport: true },
              semanticTokens: { refreshSupport: true },
              codeLens: { refreshSupport: true },
              diagnostics: { refreshSupport: true },
            },
        },
      }, 15000);
    const rawCapabilities = initializeCapabilities(initResult);
    const capabilities = parseCapabilities(rawCapabilities);
    const sync = parseDocumentSync(rawCapabilities.textDocumentSync);
    await sendLspNotify(root, serverId, result.generation, "initialized", {});
    const schemaAssociations = lspSchemaAssociationNotification(serverId);
    if (schemaAssociations) {
      await sendLspNotify(root, serverId, result.generation, schemaAssociations.method, schemaAssociations.params);
    }

    const languages = servedLanguages(serverId, result.languages);
    if (!currentWorkspaceActive || currentRoot !== root || !currentEnabledServers.has(serverId) || !languages.length) {
      setLspRuntimeState(root, serverId, { phase: "stopped" });
      await stopLspServer(root, serverId).catch(() => {});
      return;
    }

    const diagnosticProvider = parseDiagnosticProvider(rawCapabilities.diagnosticProvider);
    const session: ActiveSession = {
      root, serverId, generation: result.generation,
      languages, capabilities, sync,
      documents: new Map(), notifications: Promise.resolve(),
      diagnosticProvider,
      diagnosticRequests: Promise.resolve(),
      ...(diagnosticProvider?.workspaceDiagnostics
        ? { workspaceDiagnostics: { resultIds: new Map(), running: false, requested: false } }
        : {}),
      auxiliary: serverAuxiliaryMap.get(serverId) ?? false,
    };
    const semanticProvider = parseSemanticTokensProvider(rawCapabilities.semanticTokensProvider);
    if (semanticProvider) session.semanticScopes = semanticScopeMap(semanticProvider.legend.tokenTypes);
    activeSessions.set(key, session);
    setLspRuntimeState(root, serverId, { phase: "ready" });
    registerInLangMap(session);
    if (plugin) pluginLanguageServers().setState(root, serverId, undefined);

    for (const lang of session.languages) ensureLanguageProviders(lang);

    for (const model of monaco.editor.getModels()) {
      if (
        model.uri.scheme === "file" &&
        session.languages.includes(model.getLanguageId()) &&
        isPathInLspRoot(model.uri.fsPath ?? model.uri.path, root)
      ) {
        attachModel(session, model);
      }
    }
    if (session.semanticScopes) semanticTokensChanged.fire();
    if (session.capabilities.codeLens) codeLensesChanged.fire();
    scheduleWorkspaceDiagnostics(session, 0);
    // 文件可能在异步握手期间关闭；启动完成后仍需安排空闲回收，
    // 避免竞态导致无文档的语言服务器永久驻留。
    scheduleIdleSessionStop(session);
  } catch (error) {
    setLspRuntimeState(root, serverId, { phase: "failed", failure: started ? "initialize" : "start" });
    if (plugin) recordPluginStartError(root, serverId, error);
    if (started) {
      await stopLspServer(root, serverId).catch(() => {
        // eslint-disable-next-line no-console -- Cleanup failure must stay diagnosable without raw server data.
        console.warn("[LSP] failed to stop uninitialized server", serverId);
      });
    }
  } finally {
    startingSessions.delete(key);
  }
}

async function stopSession(root: string, serverId: string) {
  const key = sessionKey(root, serverId);
  clearIdleStopTimer(root, serverId);
  const session = activeSessions.get(key);
  if (!session) return;
  activeSessions.delete(key);
  disposeWorkspaceDiagnostics(session);
  useProjectProblemsStore().clearServer(root, serverId);
  if (lspRuntimeState(root, serverId)?.phase !== "failed") {
    setLspRuntimeState(root, serverId, { phase: "stopped" });
  }
  unregisterFromLangMap(session);
  for (const [uri, document] of session.documents) {
    disposeDocumentDiagnostics(document);
    const model = monaco.editor.getModel(monaco.Uri.parse(uri));
    if (model) monaco.editor.setModelMarkers(model, lspMarkerOwner(serverId), []);
  }

  const opened = openDocuments.get(key) ?? new Set<string>();
  for (const model of monaco.editor.getModels()) {
    const uriStr = model.uri.toString();
    if (!opened.has(uriStr)) continue;
    await didClose(session, uriStr);
    // 该文档不再被任何会话服务时，回收内容监听与定时器。
    if (!getSessionsForModel(model).length) {
      modelListeners.get(uriStr)?.dispose();
      modelListeners.delete(uriStr);
      const timer = changeTimers.get(uriStr);
      if (timer) { clearTimeout(timer); changeTimers.delete(uriStr); }
    }
  }
  openDocuments.delete(key);

  try {
    await stopLspServer(root, serverId);
  } catch {
    // best effort
  }
}

// ---------------------------------------------------------------------------
// Auto-restart on unexpected server exit
// ---------------------------------------------------------------------------

function handleUnexpectedStop(root: string, serverId: string, generation: number, final = false) {
  const key = sessionKey(root, serverId);
  const session = activeSessions.get(key);
  // If session no longer matches (already replaced or intentionally stopped), ignore.
  if (!session || session.generation !== generation) return;
  setLspRuntimeState(root, serverId, final ? { phase: "stopped" } : { phase: "failed", failure: "exit" });
  clearIdleStopTimer(root, serverId);
  const drop = () => {
    unregisterFromLangMap(session);
    activeSessions.delete(key);
    openDocuments.delete(key);
    userActionAt.delete(key);
    for (const document of session.documents.values()) disposeDocumentDiagnostics(document);
    session.documents.clear();
    disposeWorkspaceDiagnostics(session);
    useProjectProblemsStore().clearServer(root, serverId);
    for (const model of monaco.editor.getModels()) {
      monaco.editor.setModelMarkers(model, lspMarkerOwner(serverId), []);
    }
  };
  // A final stop (plugin disabled, uninstalled, upgraded, Restart) never
  // restarts on its own.
  if (final) {
    drop();
    return;
  }
  drop();
  if (!currentWorkspaceActive || !currentEnabledServers.has(serverId) || currentRoot !== root) return;

  const now = Date.now();
  const attempts = restartAttempts.get(key) ?? { count: 0, since: now };
  if (now - attempts.since > 60_000) { attempts.count = 0; attempts.since = now; }
  attempts.count++;
  restartAttempts.set(key, attempts);

  if (attempts.count > 3) return; // give up after 3 crashes within 60 s

  const delay = Math.min(3000 * attempts.count, 15_000);
  setTimeout(() => {
    if (currentWorkspaceActive && currentEnabledServers.has(serverId) && currentRoot === root) {
      startSession(root, serverId);
    }
  }, delay);
}

// ---------------------------------------------------------------------------
// Lazy-start: start an enabled session when a new model needs it
// ---------------------------------------------------------------------------

async function tryLazyStart(model: monaco.editor.ITextModel) {
  if (!currentRoot || !currentWorkspaceActive) return;
  const fsPath = model.uri.fsPath ?? model.uri.path;
  if (model.uri.scheme !== "file" || model.isDisposed() || !isPathInLspRoot(fsPath, currentRoot)) return;
  const langId = model.getLanguageId();
  const root = currentRoot;

  const candidates = [...serverLanguageMap.keys(), ...pluginLanguageServers().servers.map((server) => server.sessionId)];
  for (const serverId of candidates) {
    if (!currentWorkspaceActive || currentRoot !== root || model.isDisposed() || model.getLanguageId() !== langId) return;
    if (
      servedLanguages(serverId, declaredLanguages(serverId)).includes(langId) &&
      currentEnabledServers.has(serverId) &&
      !activeSessions.has(sessionKey(root, serverId)) &&
      !startingSessions.has(sessionKey(root, serverId))
    ) {
      await startSession(root, serverId);
      // startSession attaches all still-live matching models after handshake.
      // The model may have been disposed or changed language while awaiting it.
    }
  }
}

// ---------------------------------------------------------------------------
// Global Monaco hooks (installed once)
// ---------------------------------------------------------------------------
let globalHooksInstalled = false;

function installGlobalHooks() {
  if (globalHooksInstalled) return;
  globalHooksInstalled = true;
  ensureLspRunCommand();

  monaco.editor.onDidCreateModel(async (model) => {
    if (model.uri.scheme !== "file") return;
    for (const session of getSessionsForModel(model)) attachModel(session, model);
    // A running primary does not mean every enabled auxiliary is running.
    await tryLazyStart(model);
  });

  monaco.editor.onDidChangeModelLanguage(({ model }) => {
    if (model.uri.scheme !== "file") return;
    // didClose old language first, then reopen even if the same server serves
    // both languages; didOpen carries the document's language id.
    detachModel(model.uri.toString());
    for (const session of getSessionsForModel(model)) attachModel(session, model);
    void tryLazyStart(model);
  });

  monaco.editor.onWillDisposeModel((model) => {
    if (model.uri.scheme !== "file") return;
    detachModel(model.uri.toString());
  });

  // Never removed: like the Monaco hooks above it lives as long as the runtime.
  onLspRestartRequired((event) => void promptBuiltinRestart(event));
  // The last hold released: a restart deferred by the prompt runs now; a
  // session of a project that is no longer current gets the stop it was
  // spared; an idle one may be idle-stopped again (a background stop that
  // came due is retried by its controller).
  onLspServerReleased((root, serverId) => {
    const pendingKey = sessionKey(lspRootKey(root), serverId);
    const restart = pendingRestarts.delete(pendingKey);
    for (const session of [...activeSessions.values()]) {
      if (session.serverId !== serverId || lspRootKey(session.root) !== lspRootKey(root)) continue;
      if (lspRootKey(session.root) !== lspRootKey(currentRoot)) void stopSession(session.root, session.serverId);
      else if (restart) void restartBuiltinServer(session.root, serverId);
      else scheduleIdleSessionStop(session);
    }
  });
}

// Sessions (sessionKey of the normalized root) to restart once their last hold
// is released: the user chose Restart while they hosted a debug session.
const pendingRestarts = new Set<string>();

// ---------------------------------------------------------------------------
// Built-in server restarts requested by other features
// ---------------------------------------------------------------------------

// Live controllers, so a restart can reach the one owning a project root.
const controllerRestarters = new Set<{ root: () => string; restart: (root: string, serverId: string) => Promise<boolean> }>();

/**
 * Restarts the built-in serverId of root (stops its session, clears the
 * crash-restart count and starts it again; starts it when it was not running,
 * even without an open file of its languages). Resolves whether it runs
 * afterwards; false when no active controller owns root or the server is not
 * enabled there.
 */
export async function restartBuiltinServer(root: string, serverId: string): Promise<boolean> {
  for (const owner of [...controllerRestarters]) {
    // An inactive controller of the same root declines; try the others.
    if (owner.root() === root && (await owner.restart(root, serverId))) return true;
  }
  return false;
}

/**
 * project-lsp:restart-required: offers to restart the running sessions of the
 * server (jdtls after a change of the confirmed plugin bundles). Nothing to
 * restart when none runs: a later start already loads the new set.
 */
async function promptBuiltinRestart(event: LspRestartRequiredEvent) {
  // A launch restarting the server itself (Java debugging) caused the event:
  // decided now, before the import, and again after it.
  if (builtinServerLaunchInFlight(event.serverId)) return;
  const runningRoots = () =>
    [...new Set([...activeSessions.values()].filter((session) => session.serverId === event.serverId).map((session) => session.root))];
  // Restarting a server that hosts a debug session would end that session.
  const hosting = (root: string) => isLspServerHeld(root, event.serverId, "debug-session");
  if (!runningRoots().length) return;
  const { handleJdtlsRestartRequired } = await import("@/services/jdtls-bundles");
  if (builtinServerLaunchInFlight(event.serverId)) return;
  handleJdtlsRestartRequired(event, async () => {
    const roots = runningRoots();
    for (const root of roots.filter(hosting)) pendingRestarts.add(sessionKey(lspRootKey(root), event.serverId));
    await Promise.all(roots.filter((root) => !hosting(root)).map((root) => restartBuiltinServer(root, event.serverId)));
  }, { deferred: runningRoots().some(hosting) });
}

// ---------------------------------------------------------------------------
// Controller (driven by the Monaco-free useProjectLanguageServers wrapper)
// ---------------------------------------------------------------------------

export interface ProjectLanguageServerController {
  activate(): void;
  deactivate(): void;
  settingsChanged(): void;
  sync(root: string, enabled: string[], overrides?: Record<string, string>): void;
  dispose(): Promise<void>;
  notifyFileSaved(path: string): Promise<void>;
  formatDocument(path: string): Promise<void>;
  /** Whether the built-in serverId serves language in this project (see BuiltinServerAvailability). */
  builtinServerAvailability(serverId: string, language: string): Promise<BuiltinServerAvailability>;
  /** See the module-level restartBuiltinServer. */
  restartBuiltinServer(root: string, serverId: string): Promise<boolean>;
}

export function createProjectLanguageServerController(options: {
  projectRoot: Ref<string>;
  enabledServers: Ref<string[]>;
  pluginLanguageOverrides?: Ref<Record<string, string>>;
  active: boolean;
}): ProjectLanguageServerController {
  const { projectRoot, enabledServers } = options;
  const overridesNow = () => ({ ...(options.pluginLanguageOverrides?.value ?? {}) });

  installGlobalHooks();

  const unsubscribe = onLspEvent((event) => {
    if (event.method === "$/serverStopped") {
      const final = (event.params as { final?: unknown } | null)?.final === true;
      handleUnexpectedStop(event.root, event.serverId, event.generation, final);
      return;
    }

    // Server-initiated request (requestId set): must reply before anything else.
    // These arrive before the session is in activeSessions (e.g. during initialize
    // handshake), so we don't gate on an active session here.
    if (event.requestId != null) {
      void handleServerRequest(event as LspEvent & { requestId: number });
      return;
    }

    // Arrives during the Vue server's own initialization too, so it is not
    // gated on an active session either.
    const companionId = event.method === "tsserver/request" ? TSSERVER_BRIDGES[event.serverId] : undefined;
    if (companionId) {
      void bridgeTsserverRequest(event, companionId);
      return;
    }

    // Also during initialize (e.g. gopls cannot find go), so before the active-session gate.
    if (event.method === "window/showMessage") {
      const at = userCommandAt.get(sessionKey(event.root, event.serverId));
      const toast = lspShowMessageToast(event.params, at === undefined ? undefined : Date.now() - at);
      if (toast) showLspMessage(event.serverId, toast);
      return;
    }

    // The backend acknowledged a file-watch registration without watching:
    // the project exceeds the watch limits. Shown in the language-servers dialog.
    if (event.method === LSP_FILE_WATCH_LIMITED_METHOD) {
      markLspWatchLimited(event.root, event.serverId);
      return;
    }

    // Status-bar progress: indexing reported by the server, and the backend's
    // baseline file scan, which the server waits on before it starts indexing.
    if (event.method === "$/progress") {
      applyLspProgress(event.root, event.serverId, event.params);
      // With window.workDoneProgress declared, servers report a command's
      // result in its progress end (gopls: "completed") instead of
      // window/showMessage; it is feedback under the same rules.
      const message = (event.params as { value?: { message?: unknown } } | null)?.value?.message;
      if (parseWorkDoneProgress(event.params)?.kind === "end" && typeof message === "string") {
        const at = userCommandAt.get(sessionKey(event.root, event.serverId));
        const toast = lspShowMessageToast({ type: 3, message }, at === undefined ? undefined : Date.now() - at);
        if (toast) showLspMessage(event.serverId, toast);
      }
      return;
    }
    if (event.method === LSP_FILE_WATCH_SCAN_METHOD) {
      setLspWatchScanning(event.root, event.serverId, (event.params as { state?: unknown } | null)?.state === "begin");
      return;
    }

    const session = activeSessions.get(sessionKey(event.root, event.serverId));
    if (!session || session.generation !== event.generation) return;

    if (event.method === "textDocument/publishDiagnostics") {
      const params = event.params as { uri?: string; diagnostics?: unknown[] };
      if (typeof params?.uri === "string") {
        applyDiagnostics(event.root, event.serverId, params.uri, Array.isArray(params.diagnostics) ? params.diagnostics : []);
      }
    }
  });

  async function syncSessions(root: string, enabled: string[]) {
    if (!root) {
      for (const session of [...activeSessions.values()]) {
        if (!isLspServerHeld(session.root, session.serverId)) await stopSession(session.root, session.serverId);
      }
      return;
    }

    const nextEnabled = new Set(enabled);

    for (const session of [...activeSessions.values()]) {
      // A routing change (override, newly enabled built-in, plugin change)
      // restarts the session with its new language set.
      const routed = session.root === root ? servedLanguages(session.serverId, declaredLanguages(session.serverId) ?? session.languages) : [];
      // A held session of another project (jdtls hosting a debug session) keeps
      // running until released (see onLspServerReleased).
      if (session.root !== root && isLspServerHeld(session.root, session.serverId)) continue;
      if (session.root !== root || !nextEnabled.has(session.serverId) || !sameLanguages(routed, session.languages)) {
        await stopSession(session.root, session.serverId);
      }
    }

    const openLanguages = new Set(
      monaco.editor.getModels()
        .filter((m) => m.uri.scheme === "file" && isPathInLspRoot(m.uri.fsPath ?? m.uri.path, root))
        .map((m) => m.getLanguageId())
    );

    // 所有语言服务器都按需启动：仅打开项目内匹配语言文件后才创建进程。
    // 安装状态由 startSession/后端校验，避免项目打开时为无关语言常驻大量进程。
    await Promise.all(
      [...nextEnabled].map(async (serverId) => {
        const key = sessionKey(root, serverId);
        if (activeSessions.has(key) || startingSessions.has(key)) return;
        const languages = servedLanguages(serverId, declaredLanguages(serverId));
        if (!languages.some((language) => openLanguages.has(language))) return;
        await startSession(root, serverId);
      })
    );
  }

  let backgroundStopTimer: ReturnType<typeof setTimeout> | undefined;
  let active = options.active;

  const unsubscribeInstalled = onLspServerInstalled(async (serverId) => {
    serverMapLoaded = false;
    const root = projectRoot.value;
    if (!active || currentRoot !== root) return;
    // Vue hybrid installation replaces both executables and the shared SDK.
    // Reinstalling an already enabled server must retry its failed handshake.
    const affected = new Set([serverId]);
    if (["vue-ls", "vue-ts", "ts-ls"].includes(serverId)) {
      for (const id of ["vue-ls", "vue-ts", "ts-ls", "astro-ls"]) affected.add(id);
    }
    for (const session of [...activeSessions.values()]) {
      if (session.root === root && affected.has(session.serverId)) {
        await stopSession(session.root, session.serverId);
      }
    }
    for (const id of affected) restartAttempts.delete(sessionKey(root, id));
    if (active && currentRoot === root && projectRoot.value === root) {
      await activateSessions(root, enabledServers.value);
    }
  });

  function clearBackgroundStopTimer() {
    if (!backgroundStopTimer) return;
    clearTimeout(backgroundStopTimer);
    backgroundStopTimer = undefined;
  }

  function scheduleBackgroundStop() {
    clearBackgroundStopTimer();
    const inactiveRoot = projectRoot.value;
    const delay = lspRuntimeLimits().backgroundStopMs;
    // 0 表示不限制：后台项目的语言服务器不自动回收。
    if (delay <= 0) return;
    backgroundStopTimer = setTimeout(() => {
      backgroundStopTimer = undefined;
      for (const session of [...activeSessions.values()]) {
        if (session.root !== inactiveRoot) continue;
        // Held (jdtls hosting a debug session): stopped once released, see below.
        if (isLspServerHeld(session.root, session.serverId)) backgroundStopDue = true;
        else void stopSession(session.root, session.serverId);
      }
    }, delay);
  }

  // The background stop came due while a session was held: stop it on release.
  let backgroundStopDue = false;
  const unsubscribeReleased = onLspServerReleased((root, serverId) => {
    if (active || !backgroundStopDue || lspRootKey(root) !== lspRootKey(projectRoot.value)) return;
    for (const session of [...activeSessions.values()]) {
      if (session.serverId === serverId && session.root === projectRoot.value && !isLspServerHeld(session.root, serverId)) {
        void stopSession(session.root, session.serverId);
      }
    }
  });

  async function activateSessions(root: string, enabled: string[], overrides: Record<string, string> = overridesNow()) {
    if (!active) return;
    currentOverrides = overrides;
    currentWorkspaceActive = true;
    if (root !== currentRoot) {
      for (const session of [...activeSessions.values()]) {
        if (session.root !== root && !isLspServerHeld(session.root, session.serverId)) await stopSession(session.root, session.serverId);
      }
      currentRoot = root;
    }
    currentEnabledServers = new Set(enabled);
    await ensureServerLanguageMap();
    // Companions (Vue 3.x hybrid → vue-ts) run whenever their primary is enabled.
    const effective = withCompanions(enabled);
    currentEnabledServers = new Set(effective);
    if (active) await syncSessions(root, effective);
  }

  function settingsChanged() {
    for (const session of activeSessions.values()) scheduleIdleSessionStop(session);
    if (!active) scheduleBackgroundStop();
  }

  function activate() {
    active = true;
    backgroundStopDue = false;
    clearBackgroundStopTimer();
    void activateSessions(projectRoot.value, enabledServers.value);
  }

  function deactivate() {
    active = false;
    currentWorkspaceActive = false;
    scheduleBackgroundStop();
  }

  async function notifyFileSaved(path: string) {
    if (!currentRoot) return;
    const uri = projectFileUri(currentRoot, path).toString();
    const model = monaco.editor.getModel(monaco.Uri.parse(uri));
    if (!model) return;
    // Await an in-flight change too, not just the debounce timer.
    await flushDidChange(model);
    const text = model.getValue();
    await Promise.all(getSessionsForModel(model).map(async (session) => {
      if (!session.documents.has(uri)) return;
      if (session.sync.save) await queueSessionNotification(session, () => sendLspNotify(
        session.root, session.serverId, session.generation, "textDocument/didSave", {
          textDocument: { uri }, ...(session.sync.saveIncludeText ? { text } : {}),
        },
      )).catch(() => documentSyncFailed(session));
      scheduleDocumentDiagnostics(session, model);
      scheduleWorkspaceDiagnostics(session);
    }));
  }

  /** Apply LSP formatting to the file at the given path (for format-on-save). */
  async function formatDocument(path: string): Promise<void> {
    if (!currentRoot) return;
    const uri = projectFileUri(currentRoot, path).toString();
    const model = monaco.editor.getModel(monaco.Uri.parse(uri));
    if (!model) return;

    // 格式化是单 owner 能力：按会话优先级取第一个有格式化能力的会话
    //（主服务器优先于辅助型），避免多个服务器互相改写同一文档。
    const session = firstCapableSession(getSessionsForModel(model), "formatting");
    if (!session) return;
    await flushDidChange(model);
    if (activeSessions.get(sessionKey(session.root, session.serverId)) !== session) return;
    try {
      const result = (await sendLspRequest(
        session.root, session.serverId, session.generation,
        "textDocument/formatting",
        { textDocument: { uri }, options: { tabSize: 2, insertSpaces: true } },
        15000
      )) as Array<{ range: unknown; newText: string }> | null;
      if (result?.length) {
        model.pushEditOperations(
          [],
          result.map((e) => ({
            range: toMonacoRange(e.range as Parameters<typeof toMonacoRange>[0]),
            text: e.newText,
          })),
          () => null
        );
      }
    } catch {
      // best effort
    }
  }

  // A plugin snapshot change or a user confirmation/Restart re-routes and
  // re-syncs the open project.
  const pluginStore = pluginLanguageServers();
  const stopPluginWatch = watch(
    () => [pluginStore.revision, pluginStore.syncRequests],
    () => {
      if (active) void activateSessions(projectRoot.value, enabledServers.value);
    },
  );

  async function dispose() {
    active = false;
    currentWorkspaceActive = false;
    clearBackgroundStopTimer();
    unsubscribe();
    unsubscribeInstalled();
    unsubscribeReleased();
    controllerRestarters.delete(restarter);
    stopPluginWatch();
    const ownedRoot = projectRoot.value;
    for (const session of [...activeSessions.values()]) {
      if (session.root === ownedRoot) await stopSession(session.root, session.serverId);
    }
  }

  async function builtinServerAvailability(serverId: string, language: string): Promise<BuiltinServerAvailability> {
    if (isPluginLspServerId(serverId) || !active) return "unavailable";
    await activateSessions(projectRoot.value, enabledServers.value);
    if (!active) return "unavailable";
    if (!currentEnabledServers.has(serverId)) return "disabled";
    const declared = declaredLanguages(serverId);
    if (!declared?.includes(language)) return "unavailable";
    return servedLanguages(serverId, declared).includes(language) ? "available" : "plugin";
  }

  // The reinstall flow above for one server, plus a direct start: the caller
  // (Java debugging) needs the server even when no file of its language is open.
  async function restartBuiltin(root: string, serverId: string): Promise<boolean> {
    if (isPluginLspServerId(serverId) || !active || projectRoot.value !== root) return false;
    const key = sessionKey(root, serverId);
    if (activeSessions.has(key)) await stopSession(root, serverId);
    restartAttempts.delete(key);
    await activateSessions(root, enabledServers.value);
    if (!active || currentRoot !== root || !currentEnabledServers.has(serverId)) return false;
    if (!activeSessions.has(key)) {
      if (startingSessions.has(key)) await waitForSession(root, serverId, COMPANION_WAIT_MS);
      else await startSession(root, serverId);
    }
    return activeSessions.has(key);
  }

  const restarter = { root: () => projectRoot.value, restart: restartBuiltin };
  controllerRestarters.add(restarter);

  if (active) void activateSessions(projectRoot.value, enabledServers.value);

  return {
    activate,
    deactivate,
    settingsChanged,
    sync: (root, enabled, overrides) => void activateSessions(root, enabled, overrides ?? overridesNow()),
    dispose,
    notifyFileSaved,
    formatDocument,
    builtinServerAvailability,
    restartBuiltinServer: restartBuiltin,
  };
}
