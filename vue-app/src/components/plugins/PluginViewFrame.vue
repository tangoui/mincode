<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { ElAlert, ElButton, ElMessage, ElMessageBox } from "element-plus";
import { Loading } from "@element-plus/icons-vue";
import { describePluginActionFailure } from "@/domain/plugin-ui/action-error.mjs";
import { requiresPluginApproval } from "@/domain/plugin-ui/approval-policy";
import { isPluginHostDownloadResult, parsePluginHostOpenURLResult, parsePluginHostSaveTextResult } from "@/domain/plugin-ui/host-result.mjs";
import { acceptDrop, parseFileDrop, type PluginFileDrop } from "@/domain/plugin-ui/file-drop.mjs";
import { MAX_PICKED_PATHS, filterPickedPaths, parsePathPickRequest, pathPickNeedsConfirmation } from "@/domain/plugin-ui/path-picker.mjs";
import { saveTextFileToDisk } from "@/services/file-download";
import PluginViewUnavailable from "@/components/plugins/PluginViewUnavailable.vue";
import PluginViewSkeleton from "@/components/plugins/PluginViewSkeleton.vue";
import { aiConfigReviewRows, type AiConfigReviewRow } from "@/domain/plugin-ui/ai-config";
import {
  classifyPluginUISessionFailure,
  findPluginUIViewFailure,
  type PluginUISessionFailure,
} from "@/domain/plugin-ui/session-error";
import {
  PLUGIN_UI_MAX_IN_FLIGHT_REQUESTS,
  PLUGIN_UI_MAX_MESSAGE_BYTES,
  PLUGIN_UI_PROTOCOL_VERSION,
  parsePluginUIBinaryRequest,
  parsePluginUICallRequest,
  parsePluginUIHostRequest,
  parsePluginUIMessageEvent,
  parsePluginUIMessageRequest,
  parsePluginUIInvokeRequest,
  parsePluginUITaskRequest,
  PLUGIN_UI_MAX_TASKS,
  PLUGIN_UI_MAX_RETAINED_TASKS,
  PLUGIN_UI_MAX_TASK_RESPONSE_BYTES,
  rememberPluginUITask,
  markPluginUITaskDelivered,
  evictDeliveredPluginUITasks,
  parsePluginUITaskSnapshot,
  type PluginUITaskOwnership,
  parsePluginUIReadyMessage,
  measurePluginUIMessage,
  pluginUIMessageByteLength,
  serializePluginUIPayload,
  utf8ByteLengthUpTo,
  type PluginUIConnectMessage,
  type PluginUIContext,
  type PluginUIResponseMessage,
  PLUGIN_PROCESS_BUSY_ERROR_CODE,
  PLUGIN_UI_MAX_PENDING_OPEN_REQUESTS,
  type PluginUIOpenRequest,
  type PluginUIHostRequest,
} from "@/domain/plugin-ui/protocol";
import { PLUGIN_UI_TASK_CHANGED_EVENT, parsePluginUITaskChangedEvent, pluginUITaskPushMessage } from "@/domain/plugin-ui/task-push";
import { pluginUIPrefetchMessage, type PluginUIPrefetchOutcome } from "@/domain/plugin-ui/prefetch";
import { PLUGIN_UI_REVOKED_EVENT } from "@/domain/plugin-ui/warm-pool";
import { registerPluginViewSave } from "@/services/plugin-view-save";
import { authorizeWithoutPrompt } from "@/services/plugin-full-trust";
import { registerPluginViewOpenRequestReceiver } from "@/services/plugin-view-open-requests";
import { useVueI18n } from "@/i18n/messages";
import {
  beginPluginUISession,
  callPluginUIMethod,
  closePluginUISession,
  invokePluginUI,
  parsePluginUIBinaryEvent,
  postPluginUIMessage,
  sendPluginUIBinary,
  listPluginViews,
  listPlugins,
  startPluginUITask,
  getPluginUITask,
  cancelPluginUITask,
  acknowledgePluginUITask,
  activatePluginFloatingView,
  hidePluginFloatingView,
  parsePluginFloatingChange,
  pluginFloatingViewVisible,
  PLUGIN_FLOATING_CHANGED_EVENT,
  resizePluginFloatingView,
  showPluginFloatingView,
  type PluginDevelopmentSession,
  type PluginState,
  type PluginUIAction,
  type PluginUISession,
  PluginUIInvocationError,
} from "@/services/plugin-service";
import { requestPluginUIDirectToken } from "@/services/plugin-ui-direct";
import { usePluginTakeoverStore } from "@/stores/plugin-takeover";
import { currentPluginUIContext, watchPluginUIContext } from "@/services/plugin-ui-context";
import { chooseNativeDirectory, chooseNativeFile, chooseNativeFiles, copyText, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { getPluginConfiguration, listenPluginConfigurationChanged } from "@/services/plugin-configuration-service";
import { openPluginSettings } from "@/services/settings-navigation";
import { bridgeConfigurationValues } from "@/domain/plugins/configuration";
import { openHostLink } from "@/services/host-link";
import {
  base64ByteLength,
  customEditorErrorCode,
  isCustomEditorConflictError,
  parsePluginUIDocumentRequest,
  parsePluginUIDocumentSaveResult,
  type CustomEditorDocumentInfo,
  type PluginUIDocumentRequest,
} from "@/domain/plugin-ui/document-protocol";
import {
  abortCustomEditorUpload,
  beginCustomEditorDocument,
  commitCustomEditorUpload,
  readCustomEditorDocument,
  stageCustomEditorDocument,
} from "@/services/custom-editor-documents";

// ai.config: the host confirmation and the sync logic load only when a page proposes AI settings.
const PluginAiConfigConfirm = defineAsyncComponent(() => import("@/components/plugins/PluginAiConfigConfirm.vue"));

const props = defineProps<{
  pluginId: string;
  viewId: string;
  active: boolean;
  dropTargetId: string;
  /** contributes.customEditors: bind this frame to one project file (document mode). */
  document?: { editorId: string; projectRoot: string; relativePath: string };
  /**
   * contributes.dashboardWidgets: "widget" is a fixed-height dashboard card (no file drop, surface "widget").
   * views[].floating: "floating" fills a host floating window (surface "floating"); only it may resize that window.
   * contributes.settingsPages: "settings" is the page under Settings > Plugins (surface "settings"): no file drop,
   * no open requests, no AI settings writes, never parked in the warm pool; the host draws its header outside.
   */
  variant?: "page" | "widget" | "floating" | "settings";
  /** Accessible iframe title; defaults to the generic embedded-view title. */
  frameTitle?: string;
  /** Widget variant height: small 180 px, medium 280 px. */
  size?: "small" | "medium";
  /**
   * Workspace page only: the tab was closed and the frame is parked in the warm
   * pool (hidden and inert by the host). A parked frame receives no open
   * requests or drops, its page is told it is hidden, and leaving the pool
   * revalidates the plugin and view before the page is shown again.
   */
  warm?: boolean;
}>();

const emit = defineEmits<{
  /** Document mode: the page reported unsaved changes (or the host saved/reloaded). */
  "document-dirty": [dirty: boolean];
  "document-ready": [document: CustomEditorDocumentInfo];
  "document-saved": [];
  /** Document mode: the session failed closed (disable/uninstall/crash/stop). */
  "document-failed": [description: string];
  "reopen-text": [];
  close: [];
  /** Session status changes (with the failure text), for hosts that render their own recovery (floating windows, settings pages). */
  status: [status: "loading" | "ready" | "unavailable" | "error", description: string];
}>();

type FrameStatus = "loading" | "ready" | "unavailable" | "error";
interface PluginStateEvent {
  id: string;
  state: PluginState | "uninstalled";
  enabled: boolean;
  generation?: number;
}

const { t } = useVueI18n();
const iframe = ref<HTMLIFrameElement>();
const frameUrl = ref("");
const status = ref<FrameStatus>("loading");
const errorText = ref("");
// A frame leaving the warm pool stays shown but inert (no skeleton flash) until it is revalidated.
const reusePending = ref(false);
const pageVisible = computed(() => props.active && !props.warm && !reusePending.value);
// features.immersive / keyboardCapture: tell the page when the host ends what it holds.
const takeover = usePluginTakeoverStore();
const stopImmersiveWatch = takeover.onImmersiveChange((sessionId, active) => {
  if (currentSession?.sessionId === sessionId && connected && messagePort) messagePort.postMessage({ type: "mincode.host.immersive", active });
});
const stopCaptureWatch = takeover.onCaptureReleased((sessionId) => {
  if (currentSession?.sessionId === sessionId && connected && messagePort) messagePort.postMessage({ type: "mincode.host.keyboard", captured: false });
});
// The keyboard follows focus: switching to another app or window gives it back.
function releaseKeyboardOnBlur() {
  if (currentSession) takeover.releaseCapture(currentSession.sessionId);
}
window.addEventListener("blur", releaseKeyboardOnBlur);

let mounted = false;
let attempt = 0;
let currentSession: PluginUISession | undefined;
let messagePort: MessagePort | undefined;
let handshakeTimer: ReturnType<typeof setTimeout> | undefined;
let frameLoadCount = 0;
let frameProbeSequence = 0;
let frameProbeId = "";
let frameProbeTimer: ReturnType<typeof setTimeout> | undefined;
let connected = false;
let developmentGeneration = 0;
let ownedTasks = new Map<string, PluginUITaskOwnership>();
let pendingTaskStarts = 0;
const activeRequestIds = ref(new Set<string>());
const inFlight = computed(() => activeRequestIds.value.size);
const externalChange = ref(false);
let stopActionListener: (() => void) | undefined;
let stopStateListener: (() => void) | undefined;
let stopRevokedListener: (() => void) | undefined;
let stopConfigurationListener: (() => void) | undefined;
let stopBinaryListener: (() => void) | undefined;
let stopMessageListener: (() => void) | undefined;
let stopTaskListener: (() => void) | undefined;
let stopDevelopmentListener: (() => void) | undefined;
let stopFloatingListener: (() => void) | undefined;
let stopContextWatch: (() => void) | undefined;
let stopDropListener: (() => void) | undefined;
let stopDropStarted: (() => void) | undefined;
let dropSession: PluginUISession | undefined;
let dropTarget = "";
// views[].fileDrop of the current session's view; document mode never accepts drops.
const fileDrop = ref<PluginFileDrop>();
const isWidget = computed(() => props.variant === "widget");
const isFloating = computed(() => props.variant === "floating");
const isSettings = computed(() => props.variant === "settings");
// Widget cards, floating windows and settings pages never accept file drops.
const dropElementId = computed(() => fileDrop.value && !props.document && !isWidget.value && !isFloating.value && !isSettings.value && props.active && status.value === "ready"
  ? `plugin-drop-${fileDrop.value.mode}-${props.dropTargetId}`
  : undefined);
// contributes.customEditors document mode state (bound to currentSession).
let currentDocument: CustomEditorDocumentInfo | undefined;
let saveBuffer: { uploadId: string; chunks: string[]; bytes: number } | undefined;
let saveBufferSequence = 0;
let saveRequestSequence = 0;
const pendingSaveRequests = new Map<string, { resolve: () => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>();
const SAVE_REQUEST_TIMEOUT_MS = 10 * 60_000;
// Open requests (features.openRequest): only workspace page frames receive
// them. At most PLUGIN_UI_MAX_PENDING_OPEN_REQUESTS wait until the page
// subscribes; a session change drops them.
const acceptsOpenRequests = computed(() => !isWidget.value && !isFloating.value && !isSettings.value && !props.document);
const OPEN_REQUEST_SUBSCRIBE_MS = 3_000;
let pendingOpenRequests: PluginUIOpenRequest[] = [];
let openRequestSubscribed = false;
let openRequestUnsupported = false;
let openRequestTimer: ReturnType<typeof setTimeout> | undefined;
let openRequestSessionStarted = false;
let stopOpenRequestReceiver: (() => void) | undefined;

let saveSubscribed = false;
let stopViewSave: (() => void) | undefined;
let viewSaveSequence = 0;
const viewSaves = new Map<string, { resolve: () => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>();
function requestViewSave(): Promise<void> {
  if (!saveSubscribed || !connected || !messagePort) return Promise.resolve();
  const requestId = `view-save-${++viewSaveSequence}`;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { viewSaves.delete(requestId); reject(new Error(t("plugins.viewConnectionLost"))); }, 600_000);
    viewSaves.set(requestId, { resolve, reject, timer });
    messagePort!.postMessage({ type: "mincode.host.view.save-request", requestId });
  });
}
function handleViewSaveResult(data: Record<string, unknown>) {
  if (typeof data.requestId !== "string") return;
  const entry = viewSaves.get(data.requestId);
  if (!entry) return;
  clearTimeout(entry.timer); viewSaves.delete(data.requestId);
  if (data.ok === true) entry.resolve();
  else entry.reject(new Error(t("plugins.viewConnectionLost")));
}

function clearOpenRequestTimer() {
  if (openRequestTimer) clearTimeout(openRequestTimer);
  openRequestTimer = undefined;
}

function reportOpenRequestUnsupported() {
  pendingOpenRequests = [];
  ElMessage.warning(t("pluginContrib.processLaunch.openRequestUnsupported"));
}

function flushOpenRequests() {
  if (!connected || !messagePort || !openRequestSubscribed) return;
  const requests = pendingOpenRequests;
  pendingOpenRequests = [];
  for (const request of requests) {
    try {
      messagePort.postMessage({ type: "mincode.host.open-request", request });
    } catch {
      failClosed(t("plugins.viewConnectionLost"));
      return;
    }
  }
}

// Waits for the page to call onOpenRequest; a page that never does gets the plain view.
function armOpenRequestTimer() {
  clearOpenRequestTimer();
  if (!connected || openRequestSubscribed || pendingOpenRequests.length === 0) return;
  const session = currentSession;
  openRequestTimer = setTimeout(() => {
    openRequestTimer = undefined;
    if (currentSession !== session || openRequestSubscribed || pendingOpenRequests.length === 0) return;
    openRequestUnsupported = true;
    reportOpenRequestUnsupported();
  }, OPEN_REQUEST_SUBSCRIBE_MS);
}

function receiveOpenRequest(request: PluginUIOpenRequest) {
  if (!acceptsOpenRequests.value) return;
  if (connected && openRequestUnsupported) {
    reportOpenRequestUnsupported();
    return;
  }
  if (pendingOpenRequests.length >= PLUGIN_UI_MAX_PENDING_OPEN_REQUESTS) return;
  pendingOpenRequests.push(request);
  if (openRequestSubscribed) flushOpenRequests();
  else if (connected && !openRequestTimer) armOpenRequestTimer();
}

function registerOpenRequestReceiver() {
  stopOpenRequestReceiver?.();
  // A parked (warm) frame has no tab: requests wait until a reopened tab reuses it.
  stopOpenRequestReceiver = acceptsOpenRequests.value && !props.warm
    ? registerPluginViewOpenRequestReceiver(props.pluginId.trim(), props.viewId.trim(), receiveOpenRequest)
    : undefined;
}

function handleOpenRequestSubscribe() {
  if (!acceptsOpenRequests.value || !connected || openRequestSubscribed) return;
  openRequestSubscribed = true;
  openRequestUnsupported = false;
  clearOpenRequestTimer();
  flushOpenRequests();
}

interface PluginDropEvent {
  targetId?: string;
  error?: string;
  dropId?: number;
  files?: unknown;
  entries?: unknown;
}

let latestDropId = 0;
function handleDropStarted(event: PluginDropEvent) {
  if (!Number.isSafeInteger(event.dropId) || !event.targetId || event.targetId !== dropElementId.value) return;
  latestDropId = event.dropId!;
  dropTarget = event.targetId;
  dropSession = connected ? currentSession : undefined;
}

function handleDrop(event: PluginDropEvent) {
  if (typeof event.dropId !== "number" || event.dropId !== latestDropId || event.targetId !== dropTarget || currentSession !== dropSession) return;
  dropSession = undefined;
  if (!currentSession || event.targetId !== dropElementId.value || !connected || !messagePort) return;
  if (event.error) {
    ElMessage.error(event.error);
    return;
  }
  const decision = acceptDrop(fileDrop.value, event);
  if (!decision.ok) {
    if (decision.reason === "none") ElMessage.warning(t("plugins.fileDropNoneAccepted"));
    else if (decision.reason === "tooMany") ElMessage.warning(t("plugins.fileDropTooMany", { max: decision.max ?? 0 }));
    return;
  }
  const message = decision.entries
    ? { type: "mincode.host.paths-dropped", dropId: event.dropId, entries: decision.entries }
    : { type: "mincode.host.files-dropped", dropId: event.dropId, files: decision.files };
  if (decision.entries && pluginUIMessageByteLength(message) > PLUGIN_UI_MAX_MESSAGE_BYTES) {
    ElMessage.warning(t("plugins.fileDropTooMany", { max: decision.entries.length - 1 }));
    return;
  }
  try {
    messagePort.postMessage(message);
  } catch {
    failClosed(t("plugins.viewConnectionLost"));
  }
}

function currentContext(): PluginUIContext {
  const context = currentPluginUIContext();
  if (isWidget.value) return { ...context, surface: "widget" };
  if (isFloating.value) return { ...context, surface: "floating" };
  if (isSettings.value) return { ...context, surface: "settings" };
  return props.document ? { ...context, surface: "document" } : { ...context, surface: "workspace" };
}

function clearHandshakeTimer() {
  if (handshakeTimer) clearTimeout(handshakeTimer);
  handshakeTimer = undefined;
}

function clearFrameProbe() {
  if (frameProbeTimer) clearTimeout(frameProbeTimer);
  frameProbeTimer = undefined;
  frameProbeId = "";
}

function closeTransport() {
  saveSubscribed = false;
  for (const entry of viewSaves.values()) { clearTimeout(entry.timer); entry.reject(new Error(t("plugins.viewConnectionLost"))); }
  viewSaves.clear();
  clearHandshakeTimer();
  clearFrameProbe();
  window.removeEventListener("message", handleWindowMessage);
  if (messagePort) {
    messagePort.onmessage = null;
    messagePort.onmessageerror = null;
    messagePort.close();
  }
  messagePort = undefined;
  frameUrl.value = "";
  connected = false;
  sentVisible = undefined;
  // A revoked or replaced session never receives a prefetch made for it.
  prefetchState = undefined;
  reuseSequence += 1;
  reusePending.value = false;
  openRequestSubscribed = false;
  openRequestUnsupported = false;
  clearOpenRequestTimer();
  ownedTasks = new Map();
  pendingTaskStarts = 0;
  frameLoadCount = 0;
  floatingClick = undefined;
  currentDocument = undefined;
  saveBuffer = undefined;
  rejectPendingSaveRequests(t("pluginContrib.customEditors.unavailable"));
  dismissAiConfigConfirm();
  // Old requests retain their own set and cannot release slots in a new session.
  activeRequestIds.value = new Set<string>();
}

function releaseSession() {
  const session = currentSession;
  currentSession = undefined;
  fileDrop.value = undefined;
  // A view that goes away gives back immersive mode and the keyboard.
  if (session) void takeover.releaseSession(session.sessionId);
  if (session && isDesktopRuntime()) {
    void closePluginUISession(session.sessionId).catch(() => undefined);
  }
}

function failClosed(description: string, nextStatus: FrameStatus = "error") {
  attempt += 1;
  const hadDocument = Boolean(props.document);
  closeTransport();
  releaseSession();
  status.value = nextStatus;
  errorText.value = description;
  if (hadDocument) emit("document-failed", description);
}

// views[].floating: one single-use token per user click in the floating
// window; a page may only open something in the main window with a fresh one.
const FLOATING_CLICK_TOKEN_MS = 5_000;
let floatingClick: { token: string; expires: number } | undefined;

/**
 * Called by the floating window host on a click that was not a drag, with the
 * click position in window coordinates. The page receives no pointer input, so
 * the host tells it where (in page CSS pixels) the click landed.
 */
function notifyFloatingClick(clientX?: number, clientY?: number) {
  if (!isFloating.value || !connected || !messagePort) return;
  const rect = iframe.value?.getBoundingClientRect();
  const point = rect && Number.isFinite(clientX) && Number.isFinite(clientY)
    ? { x: Math.round((clientX as number) - rect.left), y: Math.round((clientY as number) - rect.top) }
    : undefined;
  const inside = point && point.x >= 0 && point.y >= 0 && point.x <= rect!.width && point.y <= rect!.height;
  const token = typeof crypto.randomUUID === "function" ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  floatingClick = { token, expires: Date.now() + FLOATING_CLICK_TOKEN_MS };
  try {
    messagePort.postMessage({ type: "mincode.host.floating.click", token, ...(inside ? point : {}) });
  } catch {
    failClosed(t("plugins.viewConnectionLost"));
  }
}

function takeFloatingClick(token: string) {
  const click = floatingClick;
  floatingClick = undefined;
  return Boolean(click && click.token === token && click.expires >= Date.now());
}

function rejectPendingSaveRequests(message: string) {
  for (const [id, entry] of pendingSaveRequests) {
    clearTimeout(entry.timer);
    pendingSaveRequests.delete(id);
    entry.reject(new Error(message));
  }
}

/**
 * Document mode: asks the page to save (Save / Save All / dirty close). The
 * page answers by calling `document.save(...)`, then reports the result.
 */
function requestSave(): Promise<void> {
  if (!props.document || !messagePort || !connected || !currentDocument) {
    return Promise.reject(new Error(t("pluginContrib.customEditors.unavailable")));
  }
  if (currentDocument.access !== "readWrite") return Promise.resolve();
  const requestId = `save-${++saveRequestSequence}`;
  return new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      pendingSaveRequests.delete(requestId);
      reject(new Error(t("pluginContrib.customEditors.saveFailed")));
    }, SAVE_REQUEST_TIMEOUT_MS);
    pendingSaveRequests.set(requestId, { resolve, reject, timer });
    try {
      messagePort!.postMessage({ type: "mincode.host.document.save-request", requestId });
    } catch {
      clearTimeout(timer);
      pendingSaveRequests.delete(requestId);
      reject(new Error(t("plugins.viewConnectionLost")));
      failClosed(t("plugins.viewConnectionLost"));
    }
  });
}

defineExpose({ requestSave, notifyFloatingClick });

function handleDocumentSaveResult(data: unknown) {
  const result = parsePluginUIDocumentSaveResult(data);
  const entry = result ? pendingSaveRequests.get(result.requestId) : undefined;
  if (!result || !entry) return;
  clearTimeout(entry.timer);
  pendingSaveRequests.delete(result.requestId);
  if (result.ok) entry.resolve();
  else entry.reject(new Error(result.error || t("pluginContrib.customEditors.saveFailed")));
}

function isDocumentRequestType(value: unknown) {
  return typeof value === "string" && value.startsWith("mincode.plugin.document.");
}

async function handleDocumentRequest(data: unknown) {
  const session = currentSession;
  const document = currentDocument;
  if (!session || !connected || !messagePort) return;
  const request = document ? parsePluginUIDocumentRequest(data, document.chunkBytes) : undefined;
  if (!request) {
    const raw = data as Record<string, unknown>;
    if (typeof raw.id === "string" && raw.id.length > 0 && raw.id.length <= 128) {
      rejectRequest(raw.id, document ? "invalid-request" : "not-allowed", t(document ? "plugins.actionInvalidPayload" : "plugins.actionNotAllowed"));
    }
    return;
  }
  const requestIds = activeRequestIds.value;
  if (requestIds.has(request.id)) {
    rejectRequest(request.id, "invalid-request", t("plugins.actionInvalidPayload"));
    return;
  }
  if (requestIds.size >= PLUGIN_UI_MAX_IN_FLIGHT_REQUESTS) {
    rejectRequest(request.id, "busy", t("plugins.actionBusy"));
    return;
  }
  requestIds.add(request.id);
  try {
    await runDocumentRequest(request, session, document!);
  } finally {
    requestIds.delete(request.id);
  }
}

async function runDocumentRequest(request: PluginUIDocumentRequest, session: PluginUISession, document: CustomEditorDocumentInfo) {
  const live = () => currentSession === session && connected;
  if (request.type === "mincode.plugin.document.dirty") {
    emit("document-dirty", request.dirty);
    safePost({ type: "mincode.host.response", id: request.id, ok: true, result: true });
    return;
  }
  if (request.type === "mincode.plugin.document.read") {
    try {
      const chunk = await readCustomEditorDocument(session.sessionId, session.nonce, request.offset, request.length);
      if (live()) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: chunk });
    } catch (cause) {
      if (live()) rejectRequest(request.id, customEditorErrorCode(cause) === "too_large" ? "payload-too-large" : "host-unavailable",
        describePluginActionFailure(cause, t("plugins.hostOperationFailed")));
    }
    return;
  }
  // mincode.plugin.document.save: buffered in the host, written only on the last chunk.
  if (document.access !== "readWrite") {
    rejectRequest(request.id, "not-allowed", t("plugins.actionNotAllowed"));
    return;
  }
  if (request.uploadId === undefined) {
    saveBuffer = { uploadId: `upload-${++saveBufferSequence}`, chunks: [], bytes: 0 };
  } else if (saveBuffer?.uploadId !== request.uploadId) {
    rejectRequest(request.id, "invalid-request", t("plugins.actionInvalidPayload"));
    return;
  }
  const buffer = saveBuffer!;
  buffer.chunks.push(request.data);
  buffer.bytes += base64ByteLength(request.data);
  if (buffer.bytes > document.maxWriteBytes) {
    saveBuffer = undefined;
    rejectRequest(request.id, "payload-too-large", t("pluginContrib.customEditors.tooLarge"));
    return;
  }
  if (!request.last) {
    safePost({ type: "mincode.host.response", id: request.id, ok: true, result: { uploadId: buffer.uploadId } });
    return;
  }
  saveBuffer = undefined;
  await commitDocumentSave(request.id, session, buffer.chunks, buffer.bytes);
}

async function commitDocumentSave(requestId: string, session: PluginUISession, chunks: string[], bytes: number) {
  const live = () => currentSession === session && connected;
  let uploadId = "";
  try {
    uploadId = await stageCustomEditorDocument(session.sessionId, session.nonce, chunks, bytes);
    if (!live()) {
      await abortCustomEditorUpload(session.sessionId, session.nonce, uploadId);
      return;
    }
    try {
      await commitCustomEditorUpload(session.sessionId, session.nonce, uploadId);
    } catch (cause) {
      if (!isCustomEditorConflictError(cause)) throw cause;
      const decision = await resolveSaveConflict();
      if (!live()) return;
      if (decision !== "overwrite") {
        await abortCustomEditorUpload(session.sessionId, session.nonce, uploadId);
        rejectRequest(requestId, "conflict", t("pluginContrib.customEditors.saveConflict"));
        if (decision === "reload") {
          emit("document-dirty", false);
          void startSession();
        }
        return;
      }
      // force is decided here, in the trusted host, after two explicit confirmations.
      await commitCustomEditorUpload(session.sessionId, session.nonce, uploadId, true);
    }
    if (!live()) return;
    emit("document-dirty", false);
    emit("document-saved");
    safePost({ type: "mincode.host.response", id: requestId, ok: true, result: { saved: true, size: bytes } });
  } catch (cause) {
    if (uploadId) await abortCustomEditorUpload(session.sessionId, session.nonce, uploadId);
    if (!live()) return;
    const code = customEditorErrorCode(cause);
    if (code === "conflict") {
      rejectRequest(requestId, "conflict", t("pluginContrib.customEditors.saveConflict"));
      return;
    }
    const message = code === "too_large"
      ? t("pluginContrib.customEditors.tooLarge")
      : `${t("pluginContrib.customEditors.saveFailed")}: ${describePluginActionFailure(cause, t("plugins.hostOperationFailed"))}`;
    rejectRequest(requestId, code === "too_large" ? "payload-too-large" : "host-unavailable", message);
  }
}

/** Save conflict: overwrite (with a second host confirmation), reload from disk, or cancel. */
async function resolveSaveConflict(): Promise<"overwrite" | "reload" | "cancel"> {
  const name = currentDocument?.name ?? props.document?.relativePath ?? "";
  try {
    await ElMessageBox.confirm(
      t("pluginContrib.customEditors.saveConflictDescription", { name }),
      t("pluginContrib.customEditors.saveConflict"),
      {
        type: "warning",
        distinguishCancelAndClose: true,
        confirmButtonText: t("pluginContrib.customEditors.overwrite"),
        cancelButtonText: t("pluginContrib.customEditors.reload"),
      },
    );
  } catch (action) {
    return action === "cancel" ? "reload" : "cancel";
  }
  try {
    await ElMessageBox.confirm(
      t("pluginContrib.customEditors.overwriteConfirm", { name }),
      t("pluginContrib.customEditors.overwrite"),
      { type: "warning", confirmButtonText: t("pluginContrib.customEditors.overwrite"), cancelButtonText: t("common.cancel") },
    );
    return "overwrite";
  } catch {
    return "cancel";
  }
}

// Returns whether the response reached the port unchanged (not replaced by a size error).
function safePost(response: PluginUIResponseMessage, taskResponse = false): boolean {
  let outgoing = response;
  const limit = taskResponse ? PLUGIN_UI_MAX_TASK_RESPONSE_BYTES : PLUGIN_UI_MAX_MESSAGE_BYTES;
  // One serialization, counted into a reusable buffer instead of an encoded copy.
  if (pluginUIMessageByteLength(outgoing, limit) > limit) {
    outgoing = {
      type: "mincode.host.response",
      id: response.id,
      ok: false,
      error: { code: "result-too-large", message: t("plugins.actionResultTooLarge") },
    };
  }
  try {
    if (!messagePort) return false;
    messagePort.postMessage(outgoing);
    return outgoing === response;
  } catch {
    failClosed(t("plugins.viewConnectionLost"));
    return false;
  }
}

function rejectRequest(
  id: string,
  code: NonNullable<PluginUIResponseMessage["error"]>["code"],
  message: string,
  metadata?: Pick<NonNullable<PluginUIResponseMessage["error"]>, "retryable" | "details">,
) {
  safePost({ type: "mincode.host.response", id, ok: false, error: { code, message, ...metadata } });
}

// Requests a parked (warm, closed-tab) page may not make: each raises host UI
// or has a side effect. Reads, task get/cancel and visibility stay available;
// everything is allowed again once a reopened tab reuses the page.
const WARM_BLOCKED_HOST_REQUESTS = new Set<string>([
  "mincode.plugin.confirm",
  "mincode.plugin.ai-config.apply",
  "mincode.plugin.clipboard.write-text",
  "mincode.plugin.configuration.open",
  "mincode.plugin.floating.activate",
  "mincode.plugin.floating.show",
  "mincode.plugin.floating.resize",
]);

function rejectWarm(id: string) {
  rejectRequest(id, "not-allowed", t("plugins.actionNotAllowedWhileClosed"));
}

function rejectInvocation(id: string, cause: unknown) {
  if (cause instanceof PluginUIInvocationError) {
    // 进程单任务门闩冲突映射为 "busy"，插件 UI 的共享 transport 会退避重试
    rejectRequest(id, cause.code === PLUGIN_PROCESS_BUSY_ERROR_CODE ? "busy" : "invoke-failed", cause.message, {
      retryable: cause.data?.retryable === true,
      details: cause.data && (cause.data.kind || cause.data.tool)
        ? { kind: cause.data.kind, tool: cause.data.tool }
        : undefined,
    });
    return;
  }
  rejectRequest(id, "invoke-failed", describePluginActionFailure(cause, t("plugins.viewConnectionLost")));
}

async function pluginDisplayName(pluginId: string): Promise<string> {
  try {
    return (await listPlugins()).find((plugin) => plugin.id === pluginId)?.name.trim() || pluginId;
  } catch {
    return pluginId;
  }
}

async function confirmAction(action: PluginUIAction, pluginId: string) {
  if (!requiresPluginApproval(action.risk)) return true;
  // Go decides and audits; any failure answers false and the prompt below shows.
  if (await authorizeWithoutPrompt(pluginId, "viewAction", action.risk, action.id)) return true;
  const title = t("plugins.actionConfirmTitle");
  const message = action.risk === "dangerous"
    ? t("plugins.actionConfirmDangerous", { action: action.id })
    : t("plugins.actionConfirmWrite", { action: action.id });
  try {
    await ElMessageBox.confirm(message, title, {
      type: "warning",
      confirmButtonText: t("common.confirm"),
      cancelButtonText: t("common.cancel"),
    });
    return true;
  } catch {
    return false;
  }
}

async function handleInvokeRequest(data: unknown) {
  // The message is serialized once: its size gates every request type below, and an invoke/task
  // payload serialized here is the exact string (and byte count) sent on to the plugin.
  const measured = measurePluginUIMessage(data, PLUGIN_UI_MAX_MESSAGE_BYTES);
  if (measured.bytes > PLUGIN_UI_MAX_MESSAGE_BYTES) return;
  const hostRequest = parsePluginUIHostRequest(data, measured.bytes);
  if (hostRequest) {
    await handleHostRequest(hostRequest);
    return;
  }
  const request = parsePluginUIInvokeRequest(data) ?? parsePluginUITaskRequest(data, measured.bytes);
  if (!request) {
    if (connected && data && typeof data === "object" && "type" in data && "id" in data
      && typeof data.type === "string"
      && (data.type.startsWith("mincode.plugin.task.")
        || data.type.startsWith("mincode.plugin.configuration.")
        || data.type.startsWith("mincode.plugin.floating.")
        || data.type.startsWith("mincode.plugin.ai-config.")
        || data.type === "mincode.plugin.clipboard.write-text"
        || data.type === "mincode.plugin.confirm")
      && typeof data.id === "string" && data.id.length > 0 && data.id.length <= 128) {
      rejectRequest(data.id, "invalid-request", t("plugins.actionInvalidPayload"));
    }
    return;
  }
  const session = currentSession;
  if (!session || !connected || !messagePort) return;
  if (request.type === "mincode.plugin.task.get" || request.type === "mincode.plugin.task.cancel") {
    await handleTaskLookup(request, session);
    return;
  }
  if (!("action" in request)) return;
  const isTask = request.type === "mincode.plugin.task.start";
  const tasks = ownedTasks;
  for (const [id, task] of tasks) if (task.expires <= Date.now()) tasks.delete(id);
  // Results the page already received do not hold slots (the backend evicts them the same way).
  if (isTask) evictDeliveredPluginUITasks(tasks, pendingTaskStarts);
  const activeTasks = [...tasks.values()].filter(task => !task.terminal).length;
  if (isTask && (activeTasks + pendingTaskStarts >= PLUGIN_UI_MAX_TASKS
    || tasks.size + pendingTaskStarts >= PLUGIN_UI_MAX_RETAINED_TASKS)) {
    rejectRequest(request.id, "busy", t("plugins.actionBusy"));
    return;
  }
  const action = session.actions.find((candidate) => candidate.id === request.action);
  if (!action || (isTask && action.resultHandling)) {
    rejectRequest(request.id, "not-allowed", t("plugins.actionNotAllowed"));
    return;
  }
  // A closed tab kept warm only reads: no task starts, no write/dangerous actions, no host result handling.
  if (props.warm && (isTask || action.risk !== "read" || action.resultHandling)) {
    rejectWarm(request.id);
    return;
  }
  const requestIds = activeRequestIds.value;
  if (requestIds.has(request.id)) {
    rejectRequest(request.id, "invalid-request", t("plugins.actionInvalidPayload"));
    return;
  }
  if (requestIds.size >= PLUGIN_UI_MAX_IN_FLIGHT_REQUESTS) {
    rejectRequest(request.id, "busy", t("plugins.actionBusy"));
    return;
  }
  let payload: string;
  let payloadBytes: number;
  if (measured.payload !== undefined && measured.payloadBytes !== undefined) {
    payload = measured.payload;
    payloadBytes = measured.payloadBytes;
  } else {
    // No payload member (sent as null) or a non-plain message: nothing large to reuse.
    try {
      payload = serializePluginUIPayload(request.payload);
    } catch {
      rejectRequest(request.id, "invalid-request", t("plugins.actionInvalidPayload"));
      return;
    }
    payloadBytes = utf8ByteLengthUpTo(payload, PLUGIN_UI_MAX_MESSAGE_BYTES);
  }
  if (payloadBytes > action.maxPayloadBytes) {
    rejectRequest(request.id, "payload-too-large", t("plugins.actionPayloadTooLarge"));
    return;
  }

  requestIds.add(request.id);
  if (isTask) pendingTaskStarts += 1;
  const sessionId = session.sessionId;
  try {
    if (!await confirmAction(action, session.pluginId)) {
      if (currentSession?.sessionId === sessionId && connected) {
        rejectRequest(request.id, "cancelled", t("plugins.actionCancelled"));
      }
      return;
    }
    if (currentSession?.sessionId !== sessionId || !connected) return;
    const result = request.type === "mincode.plugin.task.start"
      ? await startPluginUITask(sessionId, session.nonce, action.id, payload, request.timeoutMs)
      : await invokePluginUI(sessionId, session.nonce, action.id, payload);
    if (isTask && result && typeof result === "object" && "taskId" in result && typeof result.taskId === "string") {
      rememberPluginUITask(tasks, parsePluginUITaskSnapshot(result));
    }
    if (currentSession?.sessionId !== sessionId || !connected) return;
    if (!isTask && action.resultHandling === "save-text-file") {
      const hostResult = parsePluginHostSaveTextResult(result);
      const saved = await saveTextFileToDisk({
        title: t("plugins.saveGeneratedFile"),
        filename: hostResult.filename,
        content: hostResult.content,
      });
      if (currentSession === session && connected) {
        safePost({
          type: "mincode.host.response",
          id: request.id,
          ok: true,
          result: saved ? hostResult.savedResult : hostResult.cancelledResult,
        });
      }
      return;
    }
    if (!isTask && action.resultHandling === "open-url") {
      const url = parsePluginHostOpenURLResult(result);
      await openHostLink(url, { download: isPluginHostDownloadResult(result) });
      if (currentSession === session && connected) {
        safePost({ type: "mincode.host.response", id: request.id, ok: true, result: true });
      }
      return;
    }
    safePost({ type: "mincode.host.response", id: request.id, ok: true, result }, isTask);
  } catch (cause) {
    if (currentSession?.sessionId !== sessionId || !connected) return;
    rejectInvocation(request.id, cause);
  } finally {
    requestIds.delete(request.id);
    if (isTask && currentSession === session) pendingTaskStarts -= 1;
  }
}

async function handleTaskLookup(request: { id: string; taskId: string; type: string }, session: PluginUISession) {
  const tasks = ownedTasks;
  if ((tasks.get(request.taskId)?.expires ?? 0) <= Date.now()) {
    tasks.delete(request.taskId);
    rejectRequest(request.id, "not-allowed", t("plugins.taskNotOwned"));
    return;
  }
  const requestIds = activeRequestIds.value;
  if (requestIds.has(request.id) || requestIds.size >= PLUGIN_UI_MAX_IN_FLIGHT_REQUESTS) {
    rejectRequest(request.id, "busy", t("plugins.actionBusy"));
    return;
  }
  requestIds.add(request.id);
  try {
    const operation = request.type === "mincode.plugin.task.cancel" ? cancelPluginUITask : getPluginUITask;
    const result = await operation(session.sessionId, session.nonce, request.taskId);
    if (currentSession !== session || !connected) return;
    rememberPluginUITask(tasks, result);
    if (safePost({ type: "mincode.host.response", id: request.id, ok: true, result }, true)) markPluginUITaskDelivered(tasks, result.taskId);
  } catch (cause) {
    if (currentSession !== session || !connected) return;
    rejectInvocation(request.id, cause);
  } finally {
    requestIds.delete(request.id);
  }
}

async function handleHostRequest(request: NonNullable<ReturnType<typeof parsePluginUIHostRequest>>) {
  const session = currentSession;
  const requestIds = activeRequestIds.value;
  if (!session || !connected || !messagePort) return;
  if (requestIds.has(request.id)) {
    rejectRequest(request.id, "invalid-request", t("plugins.actionInvalidPayload"));
    return;
  }
  if (requestIds.size >= PLUGIN_UI_MAX_IN_FLIGHT_REQUESTS) {
    rejectRequest(request.id, "busy", t("plugins.actionBusy"));
    return;
  }
  // A closed tab kept warm cannot raise host UI or cause side effects; reading settings stays allowed.
  if (props.warm && WARM_BLOCKED_HOST_REQUESTS.has(request.type)) {
    rejectWarm(request.id);
    return;
  }
  requestIds.add(request.id);
  try {
    // features.immersive / keyboardCapture: a page may take over only while it is the visible view; the host
    // keeps its own exit (PluginImmersiveExit, release chord) and ends both when the view goes away.
    if (request.type === "mincode.plugin.immersive.enter" || request.type === "mincode.plugin.keyboard.capture") {
      if (!pageVisible.value) {
        rejectRequest(request.id, "not-allowed", t("plugins.actionNotAllowed"));
        return;
      }
      if (request.type === "mincode.plugin.immersive.enter") await takeover.enterImmersive(session.sessionId);
      else takeover.startCapture(session.sessionId);
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: true });
      return;
    }
    if (request.type === "mincode.plugin.immersive.exit" || request.type === "mincode.plugin.keyboard.release") {
      const ended = request.type === "mincode.plugin.immersive.exit"
        ? await takeover.exitImmersive(session.sessionId)
        : takeover.releaseCapture(session.sessionId);
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: ended });
      return;
    }
    // features.binaryDirect: only framed-transport views; the backend binds the token to this session.
    if (request.type === "mincode.plugin.direct.token") {
      if (!session.binary) {
        rejectRequest(request.id, "not-allowed", t("plugins.actionNotAllowed"));
        return;
      }
      const ticket = await requestPluginUIDirectToken(session.sessionId, session.nonce);
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: { url: ticket.url } });
      return;
    }
    if (request.type === "mincode.plugin.ai-config.preview" || request.type === "mincode.plugin.ai-config.apply") {
      await handleAiConfigRequest(request, session);
      return;
    }
    // Plugin settings: the sandboxed page only ever receives non-secret values
    // (bridgeConfigurationValues); secrets stay with the plugin process.
    if (request.type === "mincode.plugin.configuration.get") {
      const state = await getPluginConfiguration(session.pluginId);
      const values = bridgeConfigurationValues(state, request.keys);
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: { values } });
      return;
    }
    if (request.type === "mincode.plugin.configuration.open") {
      openPluginSettings(session.pluginId);
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: true });
      return;
    }
    if (request.type === "mincode.plugin.clipboard.write-text") {
      if (request.sensitive) {
        try {
          await ElMessageBox.confirm(t("plugins.clipboardSensitiveDescription"), t("plugins.clipboardSensitiveTitle"), {
            type: "warning",
            confirmButtonText: t("plugins.clipboardReplace"),
            cancelButtonText: t("common.cancel"),
          });
        } catch {
          if (currentSession === session && connected) rejectRequest(request.id, "cancelled", t("plugins.actionCancelled"));
          return;
        }
      }
      if (currentSession !== session || !connected) return;
      await copyText(request.text);
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: true });
      return;
    }
    if (request.type === "mincode.plugin.floating.resize") {
      if (!isFloating.value) {
        rejectRequest(request.id, "not-allowed", t("plugins.actionNotAllowed"));
        return;
      }
      await resizePluginFloatingView(session.pluginId, session.viewId, request.width, request.height);
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: true });
      return;
    }
    if (request.type === "mincode.plugin.floating.activate") {
      if (!isFloating.value || !takeFloatingClick(request.token)) {
        rejectRequest(request.id, "not-allowed", t("plugins.actionNotAllowed"));
        return;
      }
      await activatePluginFloatingView(session.pluginId, session.viewId, request.target);
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: true });
      return;
    }
    if (request.type === "mincode.plugin.floating.show" || request.type === "mincode.plugin.floating.hide"
      || request.type === "mincode.plugin.floating.status") {
      // The session's own plugin ID is used; a page can never address another plugin's views.
      const visible = request.type === "mincode.plugin.floating.show"
        ? await showPluginFloatingView(session.pluginId, request.viewId)
        : request.type === "mincode.plugin.floating.hide"
          ? await hidePluginFloatingView(session.pluginId, request.viewId)
          : await pluginFloatingViewVisible(session.pluginId, request.viewId);
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: { visible } });
      return;
    }
    if (request.type !== "mincode.plugin.confirm") {
      rejectRequest(request.id, "not-allowed", t("plugins.actionNotAllowed"));
      return;
    }
    if (request.intent !== "dangerous") {
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: true });
      return;
    }
    const description = t("plugins.trustedConfirmDangerous", { subject: request.subject });
    // The page asked for this confirmation itself (e.g. before a delete), so it stays even
    // when the plugin is trusted; naming the plugin keeps it from reading as a host block.
    const pluginName = await pluginDisplayName(session.pluginId);
    try {
      await ElMessageBox.confirm(description, t("plugins.trustedConfirmTitle", { plugin: pluginName }), {
        type: request.intent === "dangerous" ? "warning" : "info",
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      });
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: true });
    } catch {
      if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: false });
    }
  } catch (cause) {
    if (currentSession === session && connected) {
      rejectRequest(request.id, "host-unavailable", describePluginActionFailure(cause, t("plugins.hostOperationFailed")));
    }
  } finally {
    requestIds.delete(request.id);
  }
}

// ai.config (spec §7): the page hands over the source data; comparing, the
// confirmation and the write all happen here. Only a workspace page of a plugin
// that declared ai.config gets features.aiConfig; the page never receives saved
// settings or key values (preview entries carry none).
function aiConfigAllowed(session: PluginUISession) {
  return session.capabilities.includes("ai.config") && !currentDocument && !isWidget.value && !isFloating.value && !isSettings.value;
}

type AiConfigRequest = Extract<PluginUIHostRequest, { type: "mincode.plugin.ai-config.preview" | "mincode.plugin.ai-config.apply" }>;
/** Values typed into the confirmation: per sync key, "env:NAME" / "header:NAME" to the value. */
type AiConfigPromptValues = Record<string, Record<string, string>>;
interface AiConfigConfirmResult {
  confirmed: boolean;
  values: AiConfigPromptValues;
}
interface AiConfigReview {
  pluginName: string;
  rows: AiConfigReviewRow[];
  resolve: (result: AiConfigConfirmResult) => void;
}
const aiConfigReview = shallowRef<AiConfigReview>();

/**
 * Shows the host's own confirmation; resolves unconfirmed on cancel or when the
 * session ends. The typed values only travel in the result, never kept here.
 */
function requestAiConfigConfirm(pluginName: string, rows: AiConfigReviewRow[]) {
  return new Promise<AiConfigConfirmResult>((resolve) => {
    aiConfigReview.value = { pluginName, rows, resolve };
  });
}

function settleAiConfigConfirm(confirmed: boolean, values: AiConfigPromptValues = {}) {
  const review = aiConfigReview.value;
  aiConfigReview.value = undefined;
  review?.resolve(confirmed ? { confirmed, values } : { confirmed, values: {} });
}

function dismissAiConfigConfirm() {
  settleAiConfigConfirm(false);
}

async function handleAiConfigRequest(request: AiConfigRequest, session: PluginUISession) {
  if (!aiConfigAllowed(session)) {
    rejectRequest(request.id, "not-allowed", t("plugins.actionNotAllowed"));
    return;
  }
  const [{ parseSyncItems, syncKey, withPromptValues }, sync] = await Promise.all([
    import("@/domain/ai/external-sync"),
    import("@/services/ai-external-sync"),
  ]);
  const live = () => currentSession === session && connected;
  const items = parseSyncItems(request.items);
  if (!live()) return;
  if (!items) {
    rejectRequest(request.id, "invalid-request", t("plugins.actionInvalidPayload"));
    return;
  }
  if (request.type === "mincode.plugin.ai-config.preview") {
    const entries = await sync.previewExternalSync(session.pluginId, items);
    if (live()) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: { entries } });
    return;
  }
  // Every choice must name one of these items of this plugin.
  const keys = new Set(items.map((item) => syncKey(session.pluginId, item)));
  if (request.choices.some((choice) => !keys.has(choice.key))) {
    rejectRequest(request.id, "invalid-request", t("plugins.actionInvalidPayload"));
    return;
  }
  // One confirmation and write per plugin at a time, across all its frames.
  const release = sync.claimExternalSyncReview(session.pluginId);
  if (!release) {
    rejectRequest(request.id, "busy", t("plugins.actionBusy"));
    return;
  }
  try {
    // The review compares against the Settings page's pending edits too.
    await sync.flushPendingSettingsEdits();
    const [entries, pluginName] = await Promise.all([
      sync.previewExternalSync(session.pluginId, items),
      pluginDisplayName(session.pluginId),
    ]);
    if (!live()) return;
    if (props.warm) {
      rejectWarm(request.id);
      return;
    }
    const rows = aiConfigReviewRows(items, entries, request.choices, sync.savedMcpServers());
    const reviewedKeys = new Set(rows.map((row) => row.key));
    // Only what the dialog lists is ever written; the rest is reported back.
    const dropped = request.choices
      .filter((choice) => !reviewedKeys.has(choice.key))
      .map((choice) => ({ key: choice.key, reason: "invalid-choice" }));
    if (!rows.length) {
      safePost({ type: "mincode.host.response", id: request.id, ok: true, result: { added: 0, overwritten: 0, failed: dropped } });
      return;
    }
    const { confirmed, values } = await requestAiConfigConfirm(pluginName, rows);
    if (!live()) return;
    if (!confirmed) {
      rejectRequest(request.id, "cancelled", t("plugins.actionCancelled"));
      return;
    }
    const reviewedChoices = rows.map((row) => ({ key: row.key, action: row.action }));
    // An entry that changed after the dialog (Settings save, another write) is refused, not written unseen.
    const reviewed = entries.filter((entry) => reviewedKeys.has(entry.key));
    // Values typed into the dialog fill only the prompted keys, which the
    // reviewed entries do not depend on; they never go back to the plugin.
    const filled = withPromptValues(items, session.pluginId, values);
    const result = await sync.applyExternalSync(session.pluginId, filled, reviewedChoices, reviewed);
    if (live()) {
      safePost({ type: "mincode.host.response", id: request.id, ok: true, result: { ...result, failed: [...dropped, ...result.failed] } });
    }
  } finally {
    release();
  }
}

let pathPickerBusy = false;
async function handlePathPicker(data: Record<string, unknown>) {
  const session = currentSession;
  const request = parsePathPickRequest(data);
  if (!session || !connected || pathPickerBusy || !request) return;
  if (props.warm) {
    rejectWarm(request.id);
    return;
  }
  pathPickerBusy = true;
  try {
    // A sandbox request alone is not user consent: without a click just before
    // it, confirm in the trusted host first.
    if (pathPickNeedsConfirmation(navigator.userActivation)) {
      await ElMessageBox.confirm(t("plugins.pathPickerConfirmDescription"), t("plugins.pathPickerConfirmTitle"), { type: "info" });
    }
    if (currentSession !== session || !connected) return;
    let result: string | string[] | null;
    if (request.kind === "directory") {
      result = await chooseNativeDirectory(t("plugins.pathPickerDirectoryTitle"));
    } else {
      const title = t("plugins.pathPickerFileTitle");
      const picked = request.multiple
        ? await chooseNativeFiles(title, request.extensions)
        : await chooseNativeFile(title, request.extensions.length > 0
          ? [{ DisplayName: title, Pattern: request.extensions.map((extension) => `*${extension}`).join(";") }]
          : []).then((path) => (path ? [path] : []));
      const accepted = filterPickedPaths(picked, request.extensions, request.multiple ? MAX_PICKED_PATHS : 1);
      result = request.multiple ? (accepted.length > 0 ? accepted : null) : (accepted[0] ?? null);
    }
    if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result });
  } catch {
    if (currentSession === session && connected) safePost({ type: "mincode.host.response", id: request.id, ok: true, result: null });
  } finally { pathPickerBusy = false; }
}
function handlePortMessage(event: MessageEvent<unknown>) {
  const data = event.data;
  if (data && typeof data === "object" && "type" in data) {
    if (data.type === "mincode.plugin.probe" && "id" in data && data.id === frameProbeId) {
      clearFrameProbe();
      return;
    }
    if (data.type === "mincode.plugin.view.save-subscribe") { saveSubscribed = true; return; }
    if (data.type === "mincode.plugin.view.save-result") {
      if (pluginUIMessageByteLength(data) <= 1024) handleViewSaveResult(data as Record<string, unknown>);
      return;
    }
    if (data.type === "mincode.plugin.pick-path") {
      if (pluginUIMessageByteLength(data) <= PLUGIN_UI_MAX_MESSAGE_BYTES) void handlePathPicker(data as Record<string, unknown>);
      return;
    }
    if (data.type === "mincode.plugin.open-request.subscribe") {
      handleOpenRequestSubscribe();
      return;
    }
    if (data.type === "mincode.plugin.document.save-result") {
      handleDocumentSaveResult(data);
      return;
    }
    if (isDocumentRequestType(data.type)) {
      if (pluginUIMessageByteLength(data) <= PLUGIN_UI_MAX_MESSAGE_BYTES) void handleDocumentRequest(data);
      return;
    }
    if (data.type === "mincode.plugin.call" || data.type === "mincode.plugin.binary" || data.type === "mincode.plugin.message") {
      void handleProcessRequest(data);
      return;
    }
  }
  void handleInvokeRequest(data);
}

// plugin.call, view messages and binary frames go to methods and channels the
// plugin process handles itself. Unlike manifest actions they carry no risk
// confirmation and are never offered to AI; they still share the in-flight
// request bound.
async function handleProcessRequest(data: object) {
  const session = currentSession;
  if (!session || !connected || !messagePort) return;
  const request = parsePluginUICallRequest(data) ?? parsePluginUIMessageRequest(data) ?? parsePluginUIBinaryRequest(data);
  if (!request) {
    if ("id" in data && typeof data.id === "string" && data.id.length > 0 && data.id.length <= 128) {
      rejectRequest(data.id, "invalid-request", t("plugins.actionInvalidPayload"));
    }
    return;
  }
  if (request.type === "mincode.plugin.binary" && !session.binary) {
    rejectRequest(request.id, "not-allowed", t("plugins.actionNotAllowed"));
    return;
  }
  const requestIds = activeRequestIds.value;
  if (requestIds.has(request.id)) {
    rejectRequest(request.id, "invalid-request", t("plugins.actionInvalidPayload"));
    return;
  }
  if (requestIds.size >= PLUGIN_UI_MAX_IN_FLIGHT_REQUESTS) {
    rejectRequest(request.id, "busy", t("plugins.actionBusy"));
    return;
  }
  requestIds.add(request.id);
  const sessionId = session.sessionId;
  try {
    const result = request.type === "mincode.plugin.call"
      ? await callPluginUIMethod(sessionId, session.nonce, request.method, request.params)
      : request.type === "mincode.plugin.message"
        ? await enqueueViewMessage(sessionId, session.nonce, request.message)
        : await sendPluginUIBinary(sessionId, session.nonce, request.channel, request.data);
    if (currentSession?.sessionId !== sessionId || !connected) return;
    safePost({ type: "mincode.host.response", id: request.id, ok: true, result });
  } catch (cause) {
    if (currentSession?.sessionId !== sessionId || !connected) return;
    rejectInvocation(request.id, cause);
  } finally {
    requestIds.delete(request.id);
  }
}

// View messages reach the plugin process one at a time, in the order the page posted them.
let messageQueue: Promise<unknown> = Promise.resolve();
function enqueueViewMessage(sessionId: string, nonce: string, message: unknown) {
  const delivery = messageQueue.then(() => postPluginUIMessage(sessionId, nonce, message));
  messageQueue = delivery.catch(() => undefined);
  return delivery;
}

// Binary frames the plugin process sent; only the current generation's views receive them.
function handleProcessBinary(value: unknown) {
  const frame = parsePluginUIBinaryEvent(value);
  const session = currentSession;
  if (!frame || frame.pluginId !== props.pluginId || !session?.binary || !connected || !messagePort
    || frame.generation !== session.generation) return;
  try {
    messagePort.postMessage({ type: "mincode.host.binary", channel: frame.channel, data: frame.data }, [frame.data]);
  } catch {
    // A closed port is handled by the session lifecycle.
  }
}

// Messages the plugin process posted (host.ui.postMessage): the current generation's
// views receive them, all of them or only those with the addressed view ID.
function handleProcessMessage(value: unknown) {
  const event = parsePluginUIMessageEvent(value);
  const session = currentSession;
  if (!event || !session || !connected || !messagePort || event.pluginId !== props.pluginId
    || event.generation !== session.generation || (event.viewId && event.viewId !== session.viewId)) return;
  try {
    messagePort.postMessage({ type: "mincode.host.message", message: event.message });
  } catch {
    // A closed port is handled by the session lifecycle.
  }
}

// Task snapshots the backend pushed; only the owning session's page receives them, and only for tasks it started.
function handleTaskChanged(value: unknown) {
  const session = currentSession;
  if (!session || !connected || !messagePort) return;
  const event = parsePluginUITaskChangedEvent(value, { pluginId: props.pluginId, sessionId: session.sessionId });
  if (!event) return;
  const message = pluginUITaskPushMessage(event, { pluginId: props.pluginId, sessionId: session.sessionId, generation: session.generation }, ownedTasks);
  if (!message) return;
  try {
    messagePort.postMessage(message);
  } catch {
    // A closed port is handled by the session lifecycle.
    return;
  }
  if (message.snapshot.state === "running") return;
  // The page now holds the final result: release its slot here and in the backend.
  markPluginUITaskDelivered(ownedTasks, message.taskId);
  void acknowledgePluginUITask(session.sessionId, session.nonce, message.taskId).catch(() => {
    // Best effort: an unacknowledged result keeps its slot until retention ends.
  });
}

// Tells the page when one of its plugin's floating views opened or closed.
function handleFloatingChanged(value: unknown) {
  const change = parsePluginFloatingChange(value);
  const session = currentSession;
  if (!change || !session || change.pluginId !== session.pluginId || currentDocument || !connected || !messagePort) return;
  try {
    messagePort.postMessage({ type: "mincode.host.floating", viewId: change.viewId, visible: change.visible });
  } catch {
    // A closed port is handled by the session lifecycle.
  }
}

// Pushes the plugin's non-secret settings after they change. Keys of secret
// settings are dropped too, so the page cannot observe secret edits.
let configurationPushSequence = 0;
async function pushConfiguration(pluginId: string, changedKeys: string[]) {
  const session = currentSession;
  if (!session || session.pluginId !== pluginId || !messagePort || !connected) return;
  const sequence = ++configurationPushSequence;
  let values: Record<string, unknown>;
  let keys: string[];
  try {
    const state = await getPluginConfiguration(pluginId);
    values = bridgeConfigurationValues(state);
    keys = changedKeys.filter((key) => state.schema.properties[key] && !state.schema.properties[key]!.secret);
    if (changedKeys.length > 0 && keys.length === 0) return;
  } catch {
    return;
  }
  if (sequence !== configurationPushSequence || currentSession !== session || !messagePort || !connected) return;
  try {
    messagePort.postMessage({ type: "mincode.host.configuration", keys, values });
  } catch {
    failClosed(t("plugins.viewConnectionLost"));
  }
}

function sendContext() {
  if (!messagePort || !connected) return;
  try {
    messagePort.postMessage({ type: "mincode.host.context", context: currentContext() });
  } catch {
    failClosed(t("plugins.viewConnectionLost"));
  }
}

function syncActiveContext(active: boolean) {
  if (!active) return;
  void nextTick(() => sendContext());
}

// views[].prefetch: right after the session exists, and in parallel with the
// frame load, the host invokes the view's declared read-only action through
// the same session (plugin_host_ui_invoke: session, nonce, generation,
// workspace and action allowlist are checked by Go exactly as for a page
// invoke). The outcome is kept for that session only and pushed once after
// the handshake; a session closed or replaced before then drops it.
interface PrefetchState {
  session: PluginUISession;
  action: string;
  outcome?: PluginUIPrefetchOutcome;
  delivered: boolean;
}
let prefetchState: PrefetchState | undefined;

function beginPrefetch(session: PluginUISession) {
  prefetchState = undefined;
  const spec = session.prefetch;
  if (!spec) return;
  const action = session.actions.find((candidate) => candidate.id === spec.action);
  // Never anything a page invoke would have to confirm: read risk, no host result handling.
  if (!action || action.risk !== "read" || requiresPluginApproval(action.risk) || action.resultHandling) return;
  const state: PrefetchState = { session, action: action.id, delivered: false };
  prefetchState = state;
  invokePluginUI(session.sessionId, session.nonce, action.id, spec.payload).then(
    (result) => settlePrefetch(state, { ok: true, result }),
    (cause) => settlePrefetch(state, {
      ok: false,
      error: {
        code: cause instanceof PluginUIInvocationError && cause.code === PLUGIN_PROCESS_BUSY_ERROR_CODE ? "busy" : "invoke-failed",
        message: describePluginActionFailure(cause, t("plugins.viewConnectionLost")),
      },
    }),
  );
}

function settlePrefetch(state: PrefetchState, outcome: PluginUIPrefetchOutcome) {
  if (prefetchState !== state) return;
  state.outcome = outcome;
  deliverPrefetch();
}

function deliverPrefetch() {
  const state = prefetchState;
  if (!state?.outcome || state.delivered || !connected || !messagePort || currentSession !== state.session) return;
  state.delivered = true;
  const message = pluginUIPrefetchMessage(state.action, state.outcome, t("plugins.actionResultTooLarge"));
  // Delivered once: the page owns the data now.
  state.outcome = undefined;
  try {
    messagePort.postMessage(message);
  } catch {
    failClosed(t("plugins.viewConnectionLost"));
  }
}

// features.visibility: the page learns when it is hidden (inactive tab, parked
// in the warm pool) so it can pause timers and polling. Advisory only.
let sentVisible: boolean | undefined;
function sendVisibility() {
  const visible = pageVisible.value;
  if (!messagePort || !connected || sentVisible === visible) return;
  sentVisible = visible;
  try {
    messagePort.postMessage({ type: "mincode.host.visibility", visible });
  } catch {
    failClosed(t("plugins.viewConnectionLost"));
  }
}

// Leaving the warm pool: the page is shown again only after the plugin is
// confirmed enabled, running and effective here with the view still declared.
// Every revocation path already destroyed the parked frame; this closes the
// window between the last event and the reopen.
let reuseSequence = 0;
async function revalidateReusedSession() {
  const session = currentSession;
  const sequence = ++reuseSequence;
  if (!session || !connected) {
    if (status.value !== "loading") void startSession();
    return;
  }
  reusePending.value = true;
  try {
    const views = await listPluginViews({ includeFloating: isFloating.value });
    if (sequence !== reuseSequence || currentSession !== session) return;
    const view = views.find((item) => item.pluginId === session.pluginId && item.id === session.viewId);
    if (!view) {
      failClosed(sessionFailureDescription("missing-view"), "unavailable");
    } else if (!view.enabled) {
      failClosed(sessionFailureDescription("disabled"), "unavailable");
    } else if (!view.activeInWorkspace || view.state !== "running") {
      failClosed(sessionFailureDescription("not-running"), "unavailable");
    }
  } catch {
    if (sequence === reuseSequence && currentSession === session) failClosed(sessionFailureDescription("host-unavailable"), "unavailable");
  } finally {
    if (sequence === reuseSequence) {
      reusePending.value = false;
      sendVisibility();
    }
  }
}

function handleWarmChange(warm: boolean, wasWarm: boolean | undefined) {
  if (!mounted || warm === Boolean(wasWarm)) return;
  registerOpenRequestReceiver();
  if (warm) {
    // A closed tab cannot keep a write confirmation or a sign-in open.
    dismissAiConfigConfirm();
    reuseSequence += 1;
    reusePending.value = false;
    sendVisibility();
    return;
  }
  void revalidateReusedSession();
}

function handleWindowMessage(event: MessageEvent<unknown>) {
  const session = currentSession;
  const target = iframe.value?.contentWindow;
  if (!session || connected || !target || event.source !== target) return;
  const ready = parsePluginUIReadyMessage(event.data);
  if (
    !ready
    || ready.sessionId !== session.sessionId
    || ready.nonce !== session.nonce
    || ready.generation !== session.generation
  ) return;

  const channel = new MessageChannel();
  messagePort = channel.port1;
  channel.port1.onmessage = handlePortMessage;
  channel.port1.onmessageerror = () => failClosed(t("plugins.viewConnectionLost"));
  channel.port1.start();
  const connect: PluginUIConnectMessage = {
    type: "mincode.host.connect",
    protocolVersion: PLUGIN_UI_PROTOCOL_VERSION,
    sessionId: session.sessionId,
    nonce: session.nonce,
    generation: session.generation,
    requestTimeoutMs: session.requestTimeoutMs,
    features: {
      // A settings page has no tab to close, so it never gets save requests.
      ...(isSettings.value ? {} : { viewSave: 1 as const }),
      tasks: 1,
      taskEvents: 1,
      clipboardWrite: 1,
      trustedConfirm: 1,
      configuration: 1,
      calls: 1,
      messages: 1,
      ...(currentDocument ? {} : { floating: 1 as const }),
      ...(session.binary ? { binary: 1 as const, binaryDirect: 1 as const } : {}),
      immersive: 1,
      keyboardCapture: 1,
      ...(currentDocument ? { document: 1 as const } : {}),
      ...(acceptsOpenRequests.value ? { openRequest: 1 as const } : {}),
      // Level 2: preview/apply plus connector prompts, auth and login.
      ...(aiConfigAllowed(session) ? { aiConfig: 2 as const } : {}),
      visibility: 1,
      prefetch: 1,
    },
    visible: pageVisible.value,
    ...(prefetchState?.session === session ? { prefetch: { action: prefetchState.action } } : {}),
    context: currentContext(),
    actions: session.actions,
    ...(currentDocument ? { document: currentDocument } : {}),
  };
  try {
    target.postMessage(connect, "*", [channel.port2]);
  } catch {
    channel.port1.close();
    messagePort = undefined;
    failClosed(t("plugins.viewHandshakeFailed"));
    return;
  }
  connected = true;
  sentVisible = connect.visible;
  clearHandshakeTimer();
  window.removeEventListener("message", handleWindowMessage);
  deliverPrefetch();
  status.value = "ready";
  errorText.value = "";
  if (currentDocument) emit("document-ready", currentDocument);
  armOpenRequestTimer();
}

function renewPluginDocument() {
  clearFrameProbe();
  if (mounted) void startSession();
}

function probePluginDocument() {
  if (!messagePort || !connected) return;
  clearFrameProbe();
  const probeId = `frame-${++frameProbeSequence}`;
  frameProbeId = probeId;
  frameProbeTimer = setTimeout(() => {
    if (frameProbeId === probeId) renewPluginDocument();
  }, 2_000);
  try {
    messagePort.postMessage({ type: "mincode.host.probe", id: probeId });
  } catch {
    renewPluginDocument();
  }
}

function handleFrameLoad() {
  frameLoadCount += 1;
  if (frameLoadCount <= 1) return;
  probePluginDocument();
}

function sessionFailureDescription(failure: PluginUISessionFailure) {
  switch (failure) {
    case "disabled":
      return t("plugins.viewDisabled");
    case "not-running":
      return t("plugins.viewNotRunning");
    case "missing-plugin":
      return t("plugins.viewPluginMissing");
    case "missing-view":
      return t("plugins.viewMissing");
    case "host-unavailable":
      return t("plugins.viewHostUnavailable");
    case "origin-rejected":
      return t("plugins.viewSessionRejected");
    case "invalid-response":
      return t("plugins.viewInvalidSession");
    default:
      return t("plugins.viewUnavailableDescription");
  }
}

async function startSession() {
  const currentAttempt = ++attempt;
  closeTransport();
  releaseSession();
  // A new session (reload, generation change) drops requests meant for the old page.
  if (openRequestSessionStarted) pendingOpenRequests = [];
  openRequestSessionStarted = true;
  status.value = "loading";
  externalChange.value = false;
  errorText.value = "";

  if (!isDesktopRuntime()) {
    status.value = "unavailable";
    errorText.value = t("common.desktopRequired");
    return;
  }
  const pluginId = props.pluginId.trim();
  const viewId = props.viewId.trim();
  if (!pluginId || !viewId) {
    status.value = "unavailable";
    errorText.value = t("plugins.viewUnavailableDescription");
    return;
  }

  try {
    // The catalog lookup is advisory (it only words a clearer failure), while the session API
    // is the authoritative check, so both run in parallel instead of delaying every open.
    const viewFailureLookup = Promise.all([listPlugins(), listPluginViews({ includeFloating: isFloating.value })])
      .then(([plugins, views]) => findPluginUIViewFailure(plugins, views, pluginId, viewId))
      .catch(() => undefined);
    const target = props.document;
    let document: CustomEditorDocumentInfo | undefined;
    let session: PluginUISession;
    try {
      if (target) {
        // The host binds the session to this one file; the page never sees or sends a path it was not given.
        const bound = await beginCustomEditorDocument({ pluginId, viewId, ...target });
        session = bound.session;
        document = bound.document;
      } else {
        session = await beginPluginUISession(pluginId, viewId);
      }
      // First-screen data in parallel with the frame load (views[].prefetch). A superseded
      // attempt or an unmounted frame must neither invoke nor replace the current prefetch.
      if (mounted && currentAttempt === attempt) beginPrefetch(session);
    } catch (error) {
      const viewFailure = await viewFailureLookup;
      if (!viewFailure) throw error;
      if (currentAttempt !== attempt) return;
      status.value = "unavailable";
      errorText.value = sessionFailureDescription(viewFailure);
      return;
    }
    const viewFailure = await viewFailureLookup;
    if (viewFailure || !mounted || currentAttempt !== attempt) {
      void closePluginUISession(session.sessionId).catch(() => undefined);
      if (viewFailure && currentAttempt === attempt) {
        status.value = "unavailable";
        errorText.value = sessionFailureDescription(viewFailure);
      }
      return;
    }
    currentSession = session;
    currentDocument = document;
    fileDrop.value = parseFileDrop(session.fileDrop);
    window.addEventListener("message", handleWindowMessage);
    frameUrl.value = session.url;
    await nextTick();
    handshakeTimer = setTimeout(() => {
      if (currentSession?.sessionId === session.sessionId && !connected) {
        failClosed(t("plugins.viewHandshakeFailed"));
      }
    }, 10_000);
  } catch (error) {
    if (currentAttempt !== attempt) return;
    closeTransport();
    releaseSession();
    status.value = "unavailable";
    const code = props.document ? customEditorErrorCode(error) : undefined;
    errorText.value = code === "too_large"
      ? t("pluginContrib.customEditors.tooLarge")
      : code === "not_found" || code === "forbidden"
        ? describePluginActionFailure(error, t("plugins.viewUnavailableDescription"))
        : sessionFailureDescription(classifyPluginUISessionFailure(error));
    if (props.document) emit("document-failed", errorText.value);
  }
}

function handlePluginState(event: PluginStateEvent) {
  const session = currentSession;
  if (!session || event?.id !== session.pluginId) return;
  const generationChanged = typeof event.generation === "number"
    && event.generation > 0
    && event.generation !== session.generation;
  if (!event.enabled || event.state !== "running") {
    failClosed(t("plugins.viewStopped"), "unavailable");
  } else if (generationChanged && session.mode !== "development") {
    failClosed(t("plugins.viewStopped"), "unavailable");
  }
}

// "plugin:ui-revoked" lists the sessions the backend closed; an open frame
// whose session is listed fails closed at once (older events about earlier
// sessions do not match). Development sessions are renewed by
// handleDevelopmentChange instead, which needs the session to still be set.
function handleUIRevoked(value: unknown) {
  const session = currentSession;
  if (!session || session.mode === "development" || !value || typeof value !== "object") return;
  const event = value as { pluginId?: unknown; sessionIds?: unknown };
  if (event.pluginId !== session.pluginId || !Array.isArray(event.sessionIds) || !event.sessionIds.includes(session.sessionId)) return;
  failClosed(t("plugins.viewStopped"), "unavailable");
}

function handleDevelopmentChange(event: Omit<Partial<PluginDevelopmentSession>, "status"> & { status?: string }) {
  const session = currentSession;
  if (!session || session.mode !== "development" || event.pluginId !== session.pluginId) return;
  if (event.status === "disconnected") {
    failClosed(t("plugins.viewStopped"), "unavailable");
    return;
  }
  if (typeof event.generation === "number" && event.generation > developmentGeneration) {
    developmentGeneration = event.generation;
    void startSession();
  }
}

watch(() => props.active, syncActiveContext, { flush: "post" });
watch(() => props.warm, handleWarmChange);
watch(pageVisible, () => sendVisibility(), { flush: "post" });
// A hidden view (inactive tab, warm pool) cannot keep the window or the keyboard.
watch(pageVisible, (visible) => {
  if (!visible && currentSession) void takeover.releaseSession(currentSession.sessionId);
});
watch(status, (value) => emit("status", value, errorText.value));
watch(
  () => [props.pluginId, props.viewId, props.document?.editorId, props.document?.projectRoot, props.document?.relativePath] as const,
  () => {
    developmentGeneration = 0;
    openRequestSessionStarted = false;
    pendingOpenRequests = [];
    if (mounted) registerOpenRequestReceiver();
    if (mounted) void startSession();
  },
);

onMounted(() => {
  mounted = true;
  // Save guards belong to workspace tabs; a settings page has no tab to close.
  if (!isSettings.value) stopViewSave = registerPluginViewSave(props.dropTargetId, requestViewSave);
  stopContextWatch = watchPluginUIContext(sendContext);
  if (isDesktopRuntime()) {
    stopActionListener = listenAppEvent<{ id: string; risk?: string }>("plugin:action-completed", (event) => {
      if (event.id === props.pluginId && event.risk !== "read") externalChange.value = true;
    });
    stopStateListener = listenAppEvent<PluginStateEvent>("plugin:state-changed", handlePluginState);
    stopRevokedListener = listenAppEvent<unknown>(PLUGIN_UI_REVOKED_EVENT, handleUIRevoked);
    stopConfigurationListener = listenPluginConfigurationChanged((event) => {
      if (event.pluginId === props.pluginId) void pushConfiguration(event.pluginId, event.keys);
    });
    stopBinaryListener = listenAppEvent<unknown>("plugin:ui-binary", handleProcessBinary);
    stopMessageListener = listenAppEvent<unknown>("plugin:ui-message", handleProcessMessage);
    stopTaskListener = listenAppEvent<unknown>(PLUGIN_UI_TASK_CHANGED_EVENT, handleTaskChanged);
    stopFloatingListener = listenAppEvent<unknown>(PLUGIN_FLOATING_CHANGED_EVENT, handleFloatingChanged);
    stopDropStarted = listenAppEvent<PluginDropEvent>("plugin-drop-started", handleDropStarted);
    stopDropListener = listenAppEvent<PluginDropEvent>("plugin-files-dropped", handleDrop);
    stopDevelopmentListener = listenAppEvent<Omit<Partial<PluginDevelopmentSession>, "status"> & { status?: string }>(
      "plugin:development-changed",
      handleDevelopmentChange,
    );
  }
  registerOpenRequestReceiver();
  void startSession();
});

onBeforeUnmount(() => {
  mounted = false;
  window.removeEventListener("blur", releaseKeyboardOnBlur);
  stopImmersiveWatch();
  stopCaptureWatch();
  if (currentSession) void takeover.releaseSession(currentSession.sessionId);
  stopViewSave?.();
  stopActionListener?.();
  stopDropListener?.();
  stopDropStarted?.();
  attempt += 1;
  stopStateListener?.();
  stopStateListener = undefined;
  stopRevokedListener?.();
  stopRevokedListener = undefined;
  stopConfigurationListener?.();
  stopConfigurationListener = undefined;
  stopBinaryListener?.();
  stopBinaryListener = undefined;
  stopMessageListener?.();
  stopMessageListener = undefined;
  stopTaskListener?.();
  stopTaskListener = undefined;
  stopFloatingListener?.();
  stopFloatingListener = undefined;
  stopDevelopmentListener?.();
  stopDevelopmentListener = undefined;
  stopContextWatch?.();
  stopContextWatch = undefined;
  stopOpenRequestReceiver?.();
  stopOpenRequestReceiver = undefined;
  pendingOpenRequests = [];
  closeTransport();
  releaseSession();
});
</script>

<template>
  <div
    class="plugin-view-frame"
    :class="isWidget ? ['plugin-view-frame--widget', `plugin-view-frame--${size ?? 'small'}`] : isFloating ? 'plugin-view-frame--floating' : isSettings ? 'plugin-view-frame--settings' : undefined"
    :aria-busy="status === 'loading' || reusePending"
  >
    <ElAlert class="plugin-view-frame__notice" v-if="externalChange && !isFloating" :title="t('plugins.externalChangeNotice')" type="info" :closable="false">
      <ElButton size="small" :disabled="inFlight > 0" @click="externalChange = false; startSession()">{{ t("plugins.reloadView") }}</ElButton>
    </ElAlert>
    <!-- Asset URLs are stable per plugin asset generation and sessions differ only in the
         fragment; the key forces a fresh frame document instead of a same-document hash change. -->
    <iframe
      v-if="frameUrl"
      :key="frameUrl"
      ref="iframe"
      class="plugin-view-frame__iframe"
      :class="{ 'is-ready': status === 'ready' }"
      :inert="reusePending || undefined"
      :src="frameUrl"
      sandbox="allow-scripts"
      referrerpolicy="no-referrer"
      :title="frameTitle || t('plugins.embeddedViewTitle')"
      :id="dropElementId"
      :data-file-drop-target="dropElementId ? '' : undefined"
      @load="handleFrameLoad"
    />
    <!-- Host skeleton from the first frame of the open until the page handshake; the iframe stays
         hidden underneath (visibility) and replaces it in place once ready. -->
    <PluginViewSkeleton
      v-if="status === 'loading' && !isFloating"
      class="plugin-view-frame__skeleton"
      :plugin-id="pluginId"
      :view-id="viewId"
      :layout="isWidget ? 'compact' : 'page'"
    />
    <div v-else-if="status === 'loading'" class="plugin-view-frame__loading" role="status">
      <el-icon class="is-loading"><Loading /></el-icon>
      <span>{{ t("plugins.viewLoading") }}</span>
    </div>
    <div v-else-if="document && (status === 'unavailable' || status === 'error')" class="plugin-view-unavailable" role="alert">
      <strong>{{ t("pluginContrib.customEditors.unavailable") }}</strong>
      <p>{{ errorText }}</p>
      <div class="plugin-view-frame__document-actions">
        <ElButton type="primary" native-type="button" @click="emit('reopen-text')">{{ t("pluginContrib.customEditors.reopenAsText") }}</ElButton>
        <ElButton native-type="button" @click="emit('close')">{{ t("common.close") }}</ElButton>
        <ElButton v-if="isDesktopRuntime()" native-type="button" @click="startSession">{{ t("plugins.retryView") }}</ElButton>
      </div>
    </div>
    <PluginViewUnavailable
      v-else-if="(status === 'unavailable' || status === 'error') && !isSettings"
      :description="errorText"
      :retryable="isDesktopRuntime()"
      @retry="startSession"
    />
    <PluginAiConfigConfirm
      v-if="aiConfigReview"
      :plugin-name="aiConfigReview.pluginName"
      :rows="aiConfigReview.rows"
      @confirm="settleAiConfigConfirm(true, $event)"
      @cancel="settleAiConfigConfirm(false)"
    />
  </div>
</template>

<style scoped>
.plugin-view-frame--widget {
  flex: none;
  height: 180px;
  min-height: 0;
}

.plugin-view-frame--widget.plugin-view-frame--medium {
  height: 280px;
}

.plugin-view-frame__document-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 8px;
}
</style>
