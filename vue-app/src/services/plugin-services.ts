/**
 * Host-side plugin services (design §7.2, §7.4, §11): the one module every
 * surface (file tree, editor selection, command palette) goes through.
 *
 * - `pluginServices` lists the providers effective in this workspace
 *   (`plugin_services_list`), loaded at startup and refreshed when plugin
 *   contributions, plugin state or the workspace change.
 * - `runPluginService` collects `params` with the plugin command parameter
 *   form when the provider declares them, then starts the call. Provider
 *   choice and dangerous confirmation are host prompts the Go side shows
 *   before `plugin_services_start` returns (PluginHostUiLayer.vue).
 * - Running calls follow `plugin:service-call-changed` snapshots into
 *   `pluginServiceCalls`, rendered by PluginServiceCallsHost.vue with
 *   progress and a cancel button. Cancelling is not a rollback: a provider
 *   that never confirms leaves a failed call marked unconfirmed, shown as
 *   such and never as cancelled.
 * - Results: text/json open PluginServiceResultDialog.vue (copy, save as a
 *   file, open in a read-only tab through plugin-service-result-tabs.ts,
 *   replace/insert for an editor selection); output files go through the native save dialog to
 *   `plugin_services_output_save`; `none` shows a success message.
 * - `setPluginServiceDefault` saves or clears a service's default provider
 *   (plugin manager Services tab, PluginServicesCatalog.vue).
 * - `loadPluginServicesOnce` reads the list where the sync does not run (a
 *   popped-out plugin manager), so the Services tab still lists and manages
 *   services there.
 * - `loadDeclaredPluginServices` reads every service one installed plugin
 *   declares, live or not (`plugin_services_declared`), for the plugin
 *   details; `onPluginServicesChanged` tells it when to read again. Only the
 *   details show inactive services; every other surface uses
 *   `pluginServices`.
 *
 * Browser preview has no plugin host: the list stays empty and nothing runs.
 */
import { shallowRef, watch, type Ref } from "vue";
import { ElMessage } from "element-plus";
import {
  parseCommandInputForm,
  serializeCommandInputValues,
  type CommandInputForm,
} from "@/domain/commands/input-schema";
import {
  httpResponseFileName,
  resolveDeclaredPluginServices,
  resolvePluginServices,
  serviceAccepts,
  serviceEntryTitle,
  serviceExchangeName,
  SERVICE_DB_RESULT_FILE_NAME,
  SERVICE_LIMITS,
  type PluginDeclaredServiceEntry,
  type PluginServiceEntry,
  type ServiceOutputKind,
  type ServiceSurface,
} from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";
import { parseContributionError, type ContributionError } from "@/services/plugin-contribution-requests";
import { saveTextFileToDisk } from "@/services/file-download";
import { installMonacoServiceMenus } from "@/services/monaco-service-menus";
import { PLUGIN_CONTRIBUTIONS_CHANGED_EVENT } from "@/services/plugin-contributions";
import { chooseNativeSaveFile, copyText, invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

export const PLUGIN_SERVICE_CALL_CHANGED_EVENT = "plugin:service-call-changed";

const REFRESH_DEBOUNCE_MS = 100;
/** Events after which the live providers may differ: contributions, and enabling, disabling and workspace scoping. */
const SERVICE_CHANGE_EVENTS = [
  PLUGIN_CONTRIBUTIONS_CHANGED_EVENT,
  "plugin:state-changed",
  "plugin:workspace-changed",
  "plugin:startup-completed",
] as const;
/** Finished rows kept on screen besides the running ones; older finished rows are dropped. */
const FINISHED_CALLS_KEPT = 8;
/** Change events that arrived before their start returned, kept until it does. */
const EARLY_SNAPSHOTS_KEPT = 32;
const PROGRESS_MESSAGE_MAX_RUNES = 200;

/** Where an editor selection surface puts a text result. */
export interface SelectionTarget {
  replace(text: string): void;
  insertBelow(text: string): void;
}

export interface PluginServiceProviderRef {
  pluginId: string;
  entryId: string;
}

export interface PluginServiceRunRequest {
  service: string;
  surface: ServiceSurface;
  provider?: PluginServiceProviderRef;
  /** Text input (selection or palette input box). */
  input?: string;
  /** Absolute paths of input files the user handed over. */
  paths?: string[];
  /** sftpFile: a file of an SFTP / FTP session the view has open; the host reads it (no credential crosses). */
  remoteFile?: { sessionId: string; path: string };
  /** dbResult, httpResponse: content the host hands over as result.json or response.<ext>. */
  content?: { text: string; contentType?: string };
  /** The newer surfaces' context (domain/plugins/service-surfaces.ts); the host trims it per provider. */
  surfaceContext?: Record<string, unknown>;
  selection?: SelectionTarget;
}

export type PluginServiceCallState = "running" | "succeeded" | "failed" | "cancelled";

export interface PluginServiceProgress {
  completed?: number;
  total?: number;
  /** Plain text, at most 200 characters. */
  message?: string;
}

export interface PluginServiceOutputFile {
  token: string;
  name: string;
  mime: string;
  size: number;
}

/** One host call as PluginServiceCallsHost.vue shows it. */
export interface PluginServiceCallView {
  callId: string;
  service: string;
  title: string;
  pluginName: string;
  outputKind: ServiceOutputKind;
  state: PluginServiceCallState;
  /** "input" while the host still reads the input (a remote file) before the provider runs. */
  phase?: "input";
  /** The call reads a remote file: failures before the provider ran are explained as remote read errors. */
  remoteInput: boolean;
  /**
   * The input preparation itself (a remote read) failed: the host keeps phase
   * "input" on that failed snapshot only. A failure after the read (activation,
   * the provider's run) has no phase and is not reported as a remote error.
   */
  failedInInput: boolean;
  progress?: PluginServiceProgress;
  /** 0–100 when the provider (or, in the input phase, the remote read) reports a total; undefined shows indeterminate progress. */
  percent?: number;
  output?: unknown;
  files: PluginServiceOutputFile[];
  savedTokens: string[];
  saving: boolean;
  error?: ContributionError;
  /** Failed without the provider confirming the outcome: side effects may have happened. */
  unconfirmed: boolean;
  cancelling: boolean;
}

/**
 * The one-shot load of a window without the sync: `loading` until the first
 * answer, `error` with the host's detail when it failed, else `idle`.
 */
export interface PluginServicesLoad {
  status: "idle" | "loading" | "error";
  /** Error detail when status is `error`. */
  error?: string;
}

/** A provider the user picks in the parameter form when several fit with different params. */
export interface PluginServiceProviderChoice {
  /** `<pluginId>/<entryId>` */
  id: string;
  /** Plugin name and entry title. */
  label: string;
  /** The provider's params form; null when it takes none. */
  form: CommandInputForm | null;
}

/**
 * The parameter form waiting for the user (rendered with
 * PluginCommandParamsDialog.vue). With `providers`, the user first picks one
 * and the form follows that provider's params.
 */
export interface PluginServiceParamRequest {
  title: string;
  form: CommandInputForm | null;
  providers?: PluginServiceProviderChoice[];
}

export interface PluginServiceResultView {
  callId: string;
  title: string;
  kind: "text" | "json";
  text: string;
  /** The call came from an editor selection that can still be replaced or extended. */
  selection: boolean;
}

type ParamValues = Record<string, string | number | boolean | undefined>;

interface ParamsAnswer {
  values: ParamValues;
  providerId?: string;
}

interface ServiceCallSnapshot {
  callId: string;
  state: PluginServiceCallState;
  phase?: "input";
  provider: PluginServiceProviderRef;
  progress?: PluginServiceProgress;
  output?: unknown;
  files: PluginServiceOutputFile[];
  error?: string;
  unconfirmed: boolean;
}

const servicesRef = shallowRef<PluginServiceEntry[]>([]);
const loadRef = shallowRef<PluginServicesLoad>({ status: "idle" });
const callsRef = shallowRef<PluginServiceCallView[]>([]);
const paramRequestRef = shallowRef<PluginServiceParamRequest | null>(null);
const resultRef = shallowRef<PluginServiceResultView | null>(null);
const selections = new Map<string, SelectionTarget>();
const earlySnapshots = new Map<string, ServiceCallSnapshot>();
let pendingParams: ((answer: ParamsAnswer | null) => void) | undefined;
let running = false;
let refreshSequence = 0;
let refreshTimer: ReturnType<typeof setTimeout> | undefined;

/** Providers effective in this workspace, revalidated (empty in browser preview). */
export const pluginServices: Readonly<Ref<PluginServiceEntry[]>> = servicesRef;
/** State of the one-shot load where the sync does not run (`loadPluginServicesOnce`). */
export const pluginServicesLoad: Readonly<Ref<PluginServicesLoad>> = loadRef;
/** Host calls started from a surface, running and recently finished. */
export const pluginServiceCalls: Readonly<Ref<PluginServiceCallView[]>> = callsRef;
export const pluginServiceParamRequest: Readonly<Ref<PluginServiceParamRequest | null>> = paramRequestRef;
/** The text/json result shown in PluginServiceResultDialog.vue. */
export const pluginServiceResult: Readonly<Ref<PluginServiceResultView | null>> = resultRef;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function parseProgress(raw: unknown): PluginServiceProgress | undefined {
  if (!isRecord(raw)) return undefined;
  const progress: PluginServiceProgress = {};
  const completed = finiteNumber(raw.completed);
  const total = finiteNumber(raw.total);
  if (completed !== undefined) progress.completed = completed;
  if (total !== undefined) progress.total = total;
  if (typeof raw.message === "string" && raw.message) {
    progress.message = Array.from(raw.message).slice(0, PROGRESS_MESSAGE_MAX_RUNES).join("");
  }
  return progress;
}

function parseFiles(raw: unknown): PluginServiceOutputFile[] {
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((item) => {
    if (!isRecord(item) || typeof item.token !== "string" || !item.token || typeof item.name !== "string") return [];
    return [{
      token: item.token,
      name: item.name,
      mime: typeof item.mime === "string" ? item.mime : "",
      size: finiteNumber(item.size) ?? 0,
    }];
  });
}

/** Validates a ServiceCallSnapshot from the host (service_calls.go). */
function parseSnapshot(raw: unknown): ServiceCallSnapshot | undefined {
  if (!isRecord(raw) || typeof raw.callId !== "string" || !raw.callId) return undefined;
  const state = raw.state;
  if (state !== "running" && state !== "succeeded" && state !== "failed" && state !== "cancelled") return undefined;
  const provider = isRecord(raw.provider) ? raw.provider : {};
  const snapshot: ServiceCallSnapshot = {
    callId: raw.callId,
    state,
    provider: {
      pluginId: typeof provider.pluginId === "string" ? provider.pluginId : "",
      entryId: typeof provider.entryId === "string" ? provider.entryId : "",
    },
    files: parseFiles(raw.files),
    unconfirmed: raw.unconfirmed === true,
  };
  if (raw.phase === "input") snapshot.phase = "input";
  const progress = parseProgress(raw.progress);
  if (progress) snapshot.progress = progress;
  if (raw.output !== undefined) snapshot.output = raw.output;
  if (typeof raw.error === "string" && raw.error) snapshot.error = raw.error;
  return snapshot;
}

function progressPercent(progress: PluginServiceProgress | undefined): number | undefined {
  if (!progress?.total || progress.total <= 0) return undefined;
  const percent = Math.round(((progress.completed ?? 0) / progress.total) * 100);
  return Math.min(100, Math.max(0, percent));
}

/** Codes of a remote file read that failed before the provider ran (service_commands.go), localized here. */
const REMOTE_INPUT_ERRORS = {
  unavailable: "plugins.services.remoteError.unavailable",
  not_found: "plugins.services.remoteError.notFound",
  invalid_request: "plugins.services.remoteError.invalid",
  too_large: "plugins.services.remoteError.tooLarge",
  timeout: "plugins.services.remoteError.timeout",
} as const;

/** User-facing text of a failed call: a remote read failure by its code, anything else as pluginServiceErrorText. */
export function pluginServiceCallErrorText(call: Pick<PluginServiceCallView, "error" | "title" | "remoteInput" | "failedInInput">): string {
  const code = call.error?.code;
  if (call.remoteInput && call.failedInInput && code && Object.prototype.hasOwnProperty.call(REMOTE_INPUT_ERRORS, code)) {
    const { t } = useVueI18n();
    return t(REMOTE_INPUT_ERRORS[code as keyof typeof REMOTE_INPUT_ERRORS]);
  }
  return pluginServiceErrorText(call.error, call.title);
}

/** User-facing text of a failed start or call. */
export function pluginServiceErrorText(error: ContributionError | undefined, title: string): string {
  const { t } = useVueI18n();
  if (error?.code === "too_large") return t("plugins.services.tooLarge");
  // busy covers several host limits (calls per caller, file-call slots, exchange space, provider concurrency, an open prompt).
  if (error?.code === "busy") return error.detail ? t("plugins.services.busyDetail", { name: title, detail: error.detail }) : t("plugins.services.busy", { name: title });
  return t("plugins.services.failed", { name: title, detail: error?.detail || error?.code || "" });
}

function findCall(callId: string) {
  return callsRef.value.find((call) => call.callId === callId);
}

function updateCall(callId: string, patch: Partial<PluginServiceCallView>) {
  callsRef.value = callsRef.value.map((call) => (call.callId === callId ? { ...call, ...patch } : call));
}

function removeCall(callId: string) {
  callsRef.value = callsRef.value.filter((call) => call.callId !== callId);
  selections.delete(callId);
}

function releaseOutputs(callId: string) {
  void invokeBackend("plugin_services_release", { callId }).catch(() => undefined);
}

function hasUnsavedOutputs(call: PluginServiceCallView) {
  return call.state === "succeeded" && call.files.some((file) => !call.savedTokens.includes(file.token));
}

function rememberEarlySnapshot(snapshot: ServiceCallSnapshot) {
  const kept = earlySnapshots.get(snapshot.callId);
  // A terminal snapshot is final; a later running one is older news.
  if (kept && kept.state !== "running") return;
  earlySnapshots.delete(snapshot.callId);
  earlySnapshots.set(snapshot.callId, snapshot);
  while (earlySnapshots.size > EARLY_SNAPSHOTS_KEPT) {
    const oldest = earlySnapshots.keys().next().value;
    if (oldest === undefined) break;
    earlySnapshots.delete(oldest);
  }
}

function applySnapshot(snapshot: ServiceCallSnapshot) {
  const current = findCall(snapshot.callId);
  if (!current) {
    // The host emits as soon as the call changes, possibly before start returns.
    if (running) rememberEarlySnapshot(snapshot);
    return;
  }
  if (current.state !== "running") return;
  // Leaving the input phase drops its byte progress; the provider reports its own.
  const leftInput = current.phase === "input" && snapshot.phase !== "input";
  const progress = snapshot.progress ?? (leftInput ? undefined : current.progress);
  const next: PluginServiceCallView = {
    ...current,
    state: snapshot.state,
    phase: snapshot.state === "running" ? snapshot.phase : undefined,
    failedInInput: snapshot.state === "failed" && snapshot.phase === "input",
    progress,
    percent: progressPercent(progress),
    output: snapshot.output,
    files: snapshot.files,
    error: snapshot.error ? parseContributionError(snapshot.error) : undefined,
    unconfirmed: snapshot.unconfirmed,
    cancelling: snapshot.state === "running" && current.cancelling,
  };
  callsRef.value = callsRef.value.map((call) => (call.callId === next.callId ? next : call));
  if (next.state !== "running") finishCall(next);
}

function inferredOutputKind(call: PluginServiceCallView): ServiceOutputKind {
  if (call.files.length) return call.files.length > 1 ? "files" : "file";
  if (call.output === undefined) return "none";
  return typeof call.output === "string" ? "text" : "json";
}

function finishCall(call: PluginServiceCallView) {
  if (call.state !== "succeeded") {
    selections.delete(call.callId);
    return;
  }
  const kind = call.outputKind === "none" && call.output !== undefined ? inferredOutputKind(call) : call.outputKind;
  if (call.files.length) {
    void savePluginServiceOutputs(call.callId);
  } else if (kind === "text" || kind === "json") {
    if (!resultRef.value) openPluginServiceResult(call.callId);
  } else {
    const { t } = useVueI18n();
    ElMessage.success({ message: t("plugins.services.succeeded", { name: call.title }), grouping: true });
    removeCall(call.callId);
  }
}

function baseName(path: string) {
  return path.slice(Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\")) + 1);
}

/**
 * The file names the host hands a request's files over as: the paths' base
 * names, a remote file's sanitized base name, result.json or response.<ext>.
 */
function inputFileNames(request: PluginServiceRunRequest): string[] {
  const names = (request.paths ?? []).map(baseName);
  if (request.remoteFile) names.push(serviceExchangeName(request.remoteFile.path));
  if (request.content) {
    const name = request.surface === "dbResult" ? SERVICE_DB_RESULT_FILE_NAME : httpResponseFileName(request.content.contentType ?? "");
    if (name) names.push(name);
  }
  return names;
}

/** Whether a provider takes the request's input; mirrors hostServiceInput.fits in service_commands.go. */
function takesInput(entry: PluginServiceEntry, request: PluginServiceRunRequest) {
  const { kind, accepts = [], maxFiles = SERVICE_LIMITS.filesDefault } = entry.input;
  const names = inputFileNames(request);
  if (request.input !== undefined) return kind === "text" && !names.length;
  if (!names.length) return kind === "none" && !request.content && !request.remoteFile;
  if (kind === "file" ? names.length !== 1 : kind !== "files" || names.length > maxFiles) return false;
  return names.every((name) => serviceAccepts(accepts, name));
}

function sameProvider(entry: PluginServiceEntry, ref: PluginServiceProviderRef) {
  return entry.pluginId === ref.pluginId && entry.entryId === ref.entryId;
}

function candidatesFor(request: PluginServiceRunRequest) {
  return servicesRef.value
    .filter((entry) => entry.service === request.service && entry.surfaces.includes(request.surface) && takesInput(entry, request))
    .sort((left, right) =>
      Number(right.isDefault) - Number(left.isDefault)
      || left.pluginName.localeCompare(right.pluginName)
      || left.pluginId.localeCompare(right.pluginId)
      || left.entryId.localeCompare(right.entryId));
}

function entryForm(entry: PluginServiceEntry) {
  return entry.params ? parseCommandInputForm(entry.params) : null;
}

function providerChoiceId(entry: PluginServiceEntry) {
  return `${entry.pluginId}/${entry.entryId}`;
}

type ParamsPlan =
  | { kind: "none" }
  /** One provider's form; `pin` names that provider in the call. */
  | { kind: "form"; entry: PluginServiceEntry; form: CommandInputForm; pin: boolean }
  /** Several providers with different params: the user picks one in the form. */
  | { kind: "choose"; providers: PluginServiceEntry[] };

/**
 * How params are collected before the call (design §9, §11). The host picks
 * the provider only inside start, so the form must already match the
 * provider it will use: the requested one, the only one, or the default
 * (pinned in the call). When several fit and none is the default, the host
 * prompt still decides if their params are identical (or absent); if they
 * differ, the user picks the provider in the form and the call names it.
 */
function paramsPlan(request: PluginServiceRunRequest, candidates: PluginServiceEntry[]): ParamsPlan {
  let chosen: PluginServiceEntry | undefined;
  if (request.provider) {
    const requested = request.provider;
    chosen = candidates.find((candidate) => sameProvider(candidate, requested))
      ?? servicesRef.value.find((candidate) => candidate.service === request.service && sameProvider(candidate, requested));
  } else if (candidates.length === 1) {
    chosen = candidates[0];
  } else {
    chosen = candidates.find((candidate) => candidate.isDefault);
  }
  if (chosen || request.provider) {
    const form = chosen ? entryForm(chosen) : null;
    return chosen && form ? { kind: "form", entry: chosen, form, pin: true } : { kind: "none" };
  }
  if (!candidates.some((candidate) => candidate.params)) return { kind: "none" };
  const first = JSON.stringify(candidates[0].params ?? null);
  if (candidates.every((candidate) => JSON.stringify(candidate.params ?? null) === first)) {
    const form = entryForm(candidates[0]);
    return form ? { kind: "form", entry: candidates[0], form, pin: false } : { kind: "none" };
  }
  return { kind: "choose", providers: candidates };
}

function askParams(request: PluginServiceParamRequest): Promise<ParamsAnswer | null> {
  pendingParams?.(null);
  return new Promise((resolve) => {
    pendingParams = resolve;
    paramRequestRef.value = request;
  });
}

function settleParams(answer: ParamsAnswer | null) {
  const resolve = pendingParams;
  pendingParams = undefined;
  paramRequestRef.value = null;
  resolve?.(answer);
}

/**
 * Submits the pending service parameter form; the waiting runPluginService
 * starts the call. `providerId` is the provider picked in the form when it
 * offered a choice (PluginServiceProviderChoice.id).
 */
export async function confirmPluginServiceParams(values: ParamValues, providerId?: string): Promise<void> {
  settleParams({ values: { ...values }, ...(providerId ? { providerId } : {}) });
}

export function cancelPluginServiceParams(): void {
  settleParams(null);
}

/**
 * Runs a service from a surface: params form (when the provider declares
 * params), then `plugin_services_start`, whose host prompts (provider choice,
 * dangerous confirmation) finish before it returns. Failures are shown to the
 * user; a declined prompt or cancelled form ends quietly.
 */
export async function runPluginService(request: PluginServiceRunRequest): Promise<void> {
  const { t, localeChain } = useVueI18n();
  // Titles the user sees (form, provider choice, call row, result) follow the UI language.
  const titleOf = (entry: PluginServiceEntry) => serviceEntryTitle(entry, localeChain.value);
  if (!isDesktopRuntime()) {
    ElMessage.info({ message: t("plugins.services.desktopOnly"), grouping: true });
    return;
  }
  const candidates = candidatesFor(request);
  const requested = request.provider;
  const requestedEntry = requested ? servicesRef.value.find((entry) => entry.service === request.service && sameProvider(entry, requested)) : undefined;
  const preferred = requestedEntry ?? candidates[0];
  const title = preferred ? titleOf(preferred) : request.service;
  let provider = request.provider;
  let params: Record<string, unknown> | undefined;
  const plan = paramsPlan(request, candidates);
  if (plan.kind === "form") {
    const answer = await askParams({ title: titleOf(plan.entry), form: plan.form });
    if (!answer) return;
    params = JSON.parse(serializeCommandInputValues(plan.form, answer.values)) as Record<string, unknown>;
    if (plan.pin) provider = { pluginId: plan.entry.pluginId, entryId: plan.entry.entryId };
  } else if (plan.kind === "choose") {
    const answer = await askParams({
      title,
      form: null,
      providers: plan.providers.map((entry) => ({ id: providerChoiceId(entry), label: `${entry.pluginName} · ${titleOf(entry)}`, form: entryForm(entry) })),
    });
    if (!answer) return;
    const entry = plan.providers.find((candidate) => providerChoiceId(candidate) === answer.providerId);
    if (!entry) return;
    const form = entryForm(entry);
    if (form) params = JSON.parse(serializeCommandInputValues(form, answer.values)) as Record<string, unknown>;
    provider = { pluginId: entry.pluginId, entryId: entry.entryId };
  }

  let snapshot: ServiceCallSnapshot | undefined;
  try {
    snapshot = parseSnapshot(await invokeBackend<unknown>("plugin_services_start", {
      service: request.service,
      surface: request.surface,
      ...(provider ? { provider } : {}),
      ...(request.input !== undefined ? { input: request.input } : {}),
      ...(request.paths?.length ? { paths: request.paths } : {}),
      ...(request.remoteFile ? { remoteFile: { sessionId: request.remoteFile.sessionId, path: request.remoteFile.path } } : {}),
      ...(request.content
        ? { content: request.content.contentType === undefined ? { text: request.content.text } : { text: request.content.text, contentType: request.content.contentType } }
        : {}),
      ...(request.surfaceContext ? { surfaceContext: request.surfaceContext } : {}),
      ...(params ? { params } : {}),
    }));
  } catch (error) {
    const parsed = parseContributionError(error);
    // The user declined the provider choice or the confirmation.
    if (parsed.code === "cancelled") return;
    ElMessage.error({ message: pluginServiceErrorText(parsed, title), grouping: true, showClose: true });
    return;
  }
  if (!snapshot) {
    ElMessage.error({ message: pluginServiceErrorText({ code: "invalid_result", detail: "" }, title), grouping: true, showClose: true });
    return;
  }
  const started = snapshot;
  const entry = servicesRef.value.find((candidate) => candidate.service === request.service && sameProvider(candidate, started.provider));
  const call: PluginServiceCallView = {
    callId: started.callId,
    service: request.service,
    title: entry ? titleOf(entry) : title,
    pluginName: entry?.pluginName ?? started.provider.pluginId,
    outputKind: (entry?.output.kind as ServiceOutputKind | undefined) ?? "none",
    state: "running",
    remoteInput: Boolean(request.remoteFile),
    failedInInput: false,
    files: [],
    savedTokens: [],
    saving: false,
    unconfirmed: false,
    cancelling: false,
  };
  if (request.selection) selections.set(call.callId, request.selection);
  const finished = callsRef.value.filter((item) => item.state !== "running");
  const dropped = new Set(finished.slice(0, Math.max(0, finished.length - FINISHED_CALLS_KEPT)).map((item) => item.callId));
  for (const callId of dropped) {
    const old = findCall(callId);
    if (old && hasUnsavedOutputs(old)) releaseOutputs(callId);
    selections.delete(callId);
  }
  callsRef.value = [...callsRef.value.filter((item) => !dropped.has(item.callId) && item.callId !== call.callId), call];
  applySnapshot(started);
  const early = earlySnapshots.get(call.callId);
  earlySnapshots.delete(call.callId);
  if (early) applySnapshot(early);
}

/** Asks the host to cancel a running call; the provider decides whether it ends cancelled. */
export async function cancelPluginServiceCall(callId: string): Promise<void> {
  const call = findCall(callId);
  if (!call || call.state !== "running" || call.cancelling) return;
  updateCall(callId, { cancelling: true });
  try {
    const snapshot = parseSnapshot(await invokeBackend<unknown>("plugin_services_cancel", { callId }));
    if (snapshot) applySnapshot(snapshot);
  } catch (error) {
    updateCall(callId, { cancelling: false });
    ElMessage.error({ message: pluginServiceErrorText(parseContributionError(error), call.title), grouping: true, showClose: true });
  }
}

/** Removes a finished call from the list and releases output files that were not saved. */
export function dismissPluginServiceCall(callId: string): void {
  const call = findCall(callId);
  if (!call || call.state === "running") return;
  removeCall(callId);
  if (resultRef.value?.callId === callId) resultRef.value = null;
  if (hasUnsavedOutputs(call)) releaseOutputs(callId);
}

/**
 * Saves a succeeded call's output files one by one through the native save
 * dialog. A cancelled dialog keeps the remaining files for a later save;
 * once every file is saved the outputs are released and the row removed.
 */
export async function savePluginServiceOutputs(callId: string): Promise<void> {
  const call = findCall(callId);
  if (!call || call.saving || !hasUnsavedOutputs(call)) return;
  const { t } = useVueI18n();
  updateCall(callId, { saving: true });
  try {
    for (const file of call.files) {
      if (findCall(callId)?.savedTokens.includes(file.token)) continue;
      const path = await chooseNativeSaveFile(t("plugins.services.saveTitle", { name: call.title }), file.name);
      if (!path) return;
      await invokeBackend("plugin_services_output_save", { callId, token: file.token, path });
      updateCall(callId, { savedTokens: [...(findCall(callId)?.savedTokens ?? []), file.token] });
      ElMessage.success({ message: t("plugins.services.saved", { name: file.name }), grouping: true });
    }
    removeCall(callId);
    releaseOutputs(callId);
  } catch (error) {
    ElMessage.error({ message: pluginServiceErrorText(parseContributionError(error), call.title), grouping: true, showClose: true });
  } finally {
    updateCall(callId, { saving: false });
  }
}

/** Shows a succeeded text/json call's result. */
export function openPluginServiceResult(callId: string): void {
  const call = findCall(callId);
  if (!call || call.state !== "succeeded" || call.output === undefined) return;
  const kind = call.outputKind === "json" || (call.outputKind !== "text" && typeof call.output !== "string") ? "json" : "text";
  const text = kind === "text" && typeof call.output === "string" ? call.output : JSON.stringify(call.output, null, 2) ?? "";
  resultRef.value = { callId, title: call.title, kind, text, selection: selections.has(callId) };
}

export function closePluginServiceResult(): void {
  resultRef.value = null;
}

/** The text a result dialog or result tab copies and saves. */
export type PluginServiceResultContent = Pick<PluginServiceResultView, "title" | "kind" | "text">;

/** Copies the shown result, or `result` (a result tab's content). */
export async function copyPluginServiceResult(result: PluginServiceResultContent | null = resultRef.value): Promise<void> {
  if (!result) return;
  const { t } = useVueI18n();
  try {
    await copyText(result.text);
    ElMessage.success({ message: t("plugins.services.result.copied"), grouping: true });
  } catch {
    ElMessage.error({ message: t("plugins.services.result.copyFailed"), grouping: true });
  }
}

/** Replaces the editor selection with the result or inserts it below; a selection takes one edit. */
export function applyPluginServiceResult(mode: "replace" | "insertBelow"): void {
  const result = resultRef.value;
  const selection = result ? selections.get(result.callId) : undefined;
  if (!result || !selection) return;
  selections.delete(result.callId);
  resultRef.value = null;
  if (mode === "replace") selection.replace(result.text);
  else selection.insertBelow(result.text);
}

/** A file name from the service title: path separators and reserved characters become "_". */
function resultFileName(title: string, kind: PluginServiceResultView["kind"]) {
  // eslint-disable-next-line no-control-regex
  const base = title.replace(/[\u0000-\u001f\\/:*?"<>|]/g, "_").replace(/^[\s.]+|[\s.]+$/g, "") || "result";
  return `${base}.${kind === "json" ? "json" : "txt"}`;
}

/** Saves the shown result, or `result`, (JSON pretty-printed) through the native save dialog. */
export async function savePluginServiceResultAs(result: PluginServiceResultContent | null = resultRef.value): Promise<void> {
  if (!result) return;
  const { t } = useVueI18n();
  const filename = resultFileName(result.title, result.kind);
  try {
    const saved = await saveTextFileToDisk({
      title: t("plugins.services.result.saveAs"),
      filename,
      content: result.text,
      browserMimeType: result.kind === "json" ? "application/json" : "text/plain",
    });
    if (saved) ElMessage.success({ message: t("plugins.services.saved", { name: filename }), grouping: true });
  } catch (error) {
    ElMessage.error({ message: pluginServiceErrorText(parseContributionError(error), result.title), grouping: true, showClose: true });
  }
}

/**
 * Makes pluginId the default provider of service (`plugin_services_default_set`);
 * an empty pluginId clears it so the user is asked every time. The list is
 * patched right away (the host emits no event for it) and then re-read,
 * through the sync or, where it does not run, once.
 * Throws the host error; the list is left unchanged then.
 */
export async function setPluginServiceDefault(service: string, pluginId: string): Promise<void> {
  await invokeBackend("plugin_services_default_set", { service, pluginId });
  // A refresh started before the change would bring back the old default.
  refreshSequence++;
  servicesRef.value = servicesRef.value.map((entry) => {
    if (entry.service !== service) return entry;
    const isDefault = entry.pluginId === pluginId;
    return entry.isDefault === isDefault ? entry : { ...entry, isDefault };
  });
  void (running ? refreshPluginServices() : loadPluginServicesOnce());
}

/**
 * Reads the provider list once where startPluginServiceSync does not run
 * (popup windows); no-op where the sync keeps it fresh and in browser
 * preview. Progress and failures go to `pluginServicesLoad` so a window never
 * mistakes a pending or failed load for "no services"; a failure keeps the
 * last list. Stale responses are ignored.
 */
export async function loadPluginServicesOnce(): Promise<void> {
  if (running || !isDesktopRuntime()) return;
  const sequence = ++refreshSequence;
  loadRef.value = { status: "loading" };
  try {
    const response = await invokeBackend<{ services?: unknown }>("plugin_services_list", {});
    if (sequence !== refreshSequence || running) return;
    servicesRef.value = resolvePluginServices(response?.services);
    loadRef.value = { status: "idle" };
  } catch (error) {
    if (sequence !== refreshSequence || running) return;
    const { code, detail } = parseContributionError(error);
    loadRef.value = { status: "error", error: detail || code };
  }
}

/**
 * Every service entry pluginId declares, live or not, with why an inactive
 * one is not offered. Empty in browser preview. Throws the host error.
 */
export async function loadDeclaredPluginServices(pluginId: string): Promise<PluginDeclaredServiceEntry[]> {
  if (!isDesktopRuntime()) return [];
  const response = await invokeBackend<{ services?: unknown }>("plugin_services_declared", { pluginId });
  return resolveDeclaredPluginServices(response?.services, pluginId);
}

/**
 * Calls onChange (debounced) when which services are live may have changed:
 * plugin contributions, plugin state, workspace scoping, startup, or a
 * re-read provider list (a saved default). Works in any window. Returns a
 * stop function.
 */
export function onPluginServicesChanged(onChange: () => void): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const schedule = () => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = undefined;
      onChange();
    }, REFRESH_DEBOUNCE_MS);
  };
  const stops = [
    ...SERVICE_CHANGE_EVENTS.map((event) => listenAppEvent(event, schedule)),
    watch(servicesRef, schedule),
  ];
  return () => {
    if (timer) clearTimeout(timer);
    timer = undefined;
    for (const stop of stops) stop();
  };
}

/** Re-reads the provider list; stale responses are ignored and failures keep the last list. */
export async function refreshPluginServices(): Promise<void> {
  if (!running) return;
  const sequence = ++refreshSequence;
  try {
    const response = await invokeBackend<{ services?: unknown }>("plugin_services_list", {});
    if (sequence !== refreshSequence || !running) return;
    servicesRef.value = resolvePluginServices(response?.services);
  } catch {
    // Keep the last known list on transient failures.
  }
}

function scheduleRefresh() {
  if (refreshTimer) clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => {
    refreshTimer = undefined;
    void refreshPluginServices();
  }, REFRESH_DEBOUNCE_MS);
}

/**
 * Loads the services, follows host calls and installs the editor selection
 * menu (desktop runtime, main window). Returns a stop function.
 */
export function startPluginServiceSync(): () => void {
  if (!isDesktopRuntime() || running) return () => undefined;
  running = true;
  // The sync owns the list from now on; a one-shot load in flight is dropped.
  loadRef.value = { status: "idle" };
  const stops = [
    installMonacoServiceMenus({ services: servicesRef, run: runPluginService }),
    ...SERVICE_CHANGE_EVENTS.map((event) => listenAppEvent(event, scheduleRefresh)),
    listenAppEvent<unknown>(PLUGIN_SERVICE_CALL_CHANGED_EVENT, (payload) => {
      const snapshot = parseSnapshot(payload);
      if (snapshot) applySnapshot(snapshot);
    }),
  ];
  void refreshPluginServices();
  return () => {
    running = false;
    refreshSequence++;
    if (refreshTimer) clearTimeout(refreshTimer);
    refreshTimer = undefined;
    for (const stop of stops) stop();
    settleParams(null);
    for (const call of callsRef.value) {
      if (hasUnsavedOutputs(call)) releaseOutputs(call.callId);
    }
    servicesRef.value = [];
    loadRef.value = { status: "idle" };
    callsRef.value = [];
    resultRef.value = null;
    selections.clear();
    earlySnapshots.clear();
  };
}
