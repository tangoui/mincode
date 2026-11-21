<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { ElMessage, type ScrollbarInstance } from "element-plus";
import { Delete, FolderOpened, Promotion, Star, VideoPause, VideoPlay } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { subscribeHttpStream } from "@/services/http-stream-subscription";
import { chooseHttpRequestFile, type SelectedHttpRequestFile } from "@/services/http-request-file-service";
import {
  loadSocketIOSavedEvents,
  saveSocketIOSavedEvents,
} from "@/services/http-socketio-persistence";
import { COMMON_HTTP_HEADER_NAMES } from "@/domain/http/request-collection";
import {
  appendStreamLogs,
  createFrameListBuffer,
  isStreamLogTruncated,
  streamLogPreview,
} from "@/domain/http/stream-log";
import {
  checkSocketIOArgs,
  formatSocketIOArgs,
  isReservedSocketIOEvent,
  isValidSocketIOAuth,
  SOCKETIO_MAX_ATTACHMENTS,
  upsertSocketIOSavedEvent,
  type SocketIOSavedEvent,
} from "@/domain/http/socketio";

type ConnectionState = "idle" | "connecting" | "connected" | "closed" | "error";
type RowKind = "header" | "query";

interface KeyValueRow {
  id: string;
  key: string;
  value: string;
  enabled: boolean;
}

interface SocketIOEvent {
  type: "transport" | "opened" | "message" | "ack" | "closed" | "error";
  timestamp: number;
  sid?: string;
  namespace?: string;
  pingInterval?: number;
  event?: string;
  data?: string;
  ackId?: number;
  attachments?: number;
  message?: string;
  size?: number;
}

interface LogEntry {
  id: string;
  direction: "system" | "sent" | "received" | "ack";
  timestamp: number;
  label: string;
  data: string;
  size?: number;
  meta?: string;
}

interface PendingAck {
  event: string;
  sentAt: number;
}

let sequence = 0;

const visible = defineModel<boolean>({ required: true });
const { t } = useVueI18n();

const state = ref<ConnectionState>("idle");
const url = ref("");
const path = ref("/socket.io");
const namespace = ref("/");
const auth = ref("");
const headers = ref<KeyValueRow[]>([createRow()]);
const query = ref<KeyValueRow[]>([createRow()]);
const settingsTab = ref<"auth" | "query" | "headers">("auth");
const eventName = ref("");
const eventArgs = ref("");
const requestAck = ref(false);
const attachments = ref<SelectedHttpRequestFile[]>([]);
const savedEvents = ref<SocketIOSavedEvent[]>([]);
const logs = shallowRef<LogEntry[]>([]);
const expandedLogIds = shallowRef(new Set<string>());
const logHost = ref<ScrollbarInstance>();
const pendingAcks = new Map<number, PendingAck>();
let sessionId = "";
let stopListening: (() => void) | undefined;

const connected = computed(() => state.value === "connected");
const busy = computed(() => state.value === "connecting" || state.value === "connected");

function createId(prefix: string) {
  sequence += 1;
  return `${prefix}-${Date.now()}-${sequence}`;
}

function createRow(): KeyValueRow {
  return { id: createId("socketio-row"), key: "", value: "", enabled: true };
}

function rowsFor(kind: RowKind) {
  return kind === "header" ? headers : query;
}

function enabledRows(rows: KeyValueRow[]): [string, string][] {
  return rows.filter((row) => row.enabled && row.key.trim()).map((row) => [row.key.trim(), row.value]);
}

const logBuffer = createFrameListBuffer<LogEntry>((entries) => {
  const next = appendStreamLogs(logs.value, entries);
  if (expandedLogIds.value.size && next[0] !== logs.value[0]) {
    const retained = new Set(next.map((entry) => entry.id));
    expandedLogIds.value = new Set([...expandedLogIds.value].filter((id) => retained.has(id)));
  }
  logs.value = next;
  void nextTick(() => {
    logHost.value?.setScrollTop(logHost.value.wrapRef?.scrollHeight ?? 0);
  });
});

function addLog(entry: Omit<LogEntry, "id" | "timestamp">) {
  logBuffer.push({ id: createId("socketio-log"), timestamp: Date.now(), ...entry });
}

function resetLogs() {
  logBuffer.clear();
  logs.value = [];
  expandedLogIds.value = new Set();
}

function toggleLogExpanded(id: string) {
  const next = new Set(expandedLogIds.value);
  if (!next.delete(id)) next.add(id);
  expandedLogIds.value = next;
}

function handleEvent(event: SocketIOEvent) {
  switch (event.type) {
    case "transport":
      addLog({
        direction: "system",
        label: t("http.socketio.transportOpened"),
        data: `sid: ${event.sid ?? ""}\npingInterval: ${event.pingInterval ?? 0}ms`,
      });
      return;
    case "opened":
      state.value = "connected";
      addLog({
        direction: "system",
        label: t("http.socketio.namespaceConnected", { namespace: event.namespace || "/" }),
        data: event.sid ? `sid: ${event.sid}` : "",
      });
      return;
    case "message": {
      const meta = [
        event.ackId !== undefined ? t("http.socketio.serverWantsAck", { id: event.ackId }) : "",
        event.attachments ? t("http.socketio.attachmentCount", { count: event.attachments }) : "",
      ]
        .filter(Boolean)
        .join(" · ");
      addLog({
        direction: "received",
        label: event.event || "message",
        data: formatSocketIOArgs(event.data ?? ""),
        size: event.size,
        meta,
      });
      return;
    }
    case "ack": {
      const pending = event.ackId !== undefined ? pendingAcks.get(event.ackId) : undefined;
      if (event.ackId !== undefined) pendingAcks.delete(event.ackId);
      addLog({
        direction: "ack",
        label: t("http.socketio.ackReceived", { event: pending?.event ?? `#${event.ackId ?? "?"}` }),
        data: formatSocketIOArgs(event.data ?? ""),
        size: event.size,
        meta: pending ? t("http.socketio.ackElapsed", { ms: Date.now() - pending.sentAt }) : "",
      });
      return;
    }
    case "error":
      state.value = "error";
      addLog({ direction: "system", label: t("http.streamError"), data: event.message ?? "" });
      return;
    case "closed":
      if (state.value !== "error") state.value = "closed";
      addLog({ direction: "system", label: t("http.streamDisconnected"), data: "" });
      cleanupListener();
  }
}

function cleanupListener() {
  stopListening?.();
  stopListening = undefined;
  sessionId = "";
  pendingAcks.clear();
}

async function connect() {
  if (!url.value.trim()) {
    ElMessage.warning(t("http.urlRequired"));
    return;
  }
  if (!isValidSocketIOAuth(auth.value)) {
    settingsTab.value = "auth";
    ElMessage.warning(t("http.socketio.authInvalid"));
    return;
  }
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  await disconnect(false);
  state.value = "connecting";
  const currentSession = createId("socketio");
  sessionId = currentSession;
  const subscription = subscribeHttpStream<SocketIOEvent>(currentSession, {
    onEvent: handleEvent,
    onTransportLost() {
      if (sessionId !== currentSession) return;
      state.value = "error";
      addLog({ direction: "system", label: t("http.streamError"), data: t("http.streamTransportDisconnected") });
      stopListening = undefined;
      sessionId = "";
    },
  });
  stopListening = subscription.stop;
  try {
    await subscription.ready;
    await invokeBackend("http_socketio_connect", {
      sessionId: currentSession,
      url: url.value.trim(),
      path: path.value.trim(),
      namespace: namespace.value.trim(),
      auth: auth.value,
      query: enabledRows(query.value),
      headers: enabledRows(headers.value),
    });
    if (sessionId !== currentSession) {
      await invokeBackend("http_stream_disconnect", { sessionId: currentSession });
    }
  } catch (error) {
    if (sessionId === currentSession) {
      state.value = "error";
      addLog({ direction: "system", label: t("http.streamError"), data: String(error) });
      cleanupListener();
    }
  }
}

async function disconnect(addEntry = true) {
  const currentSession = sessionId;
  if (!currentSession) {
    if (busy.value) state.value = "closed";
    return;
  }
  cleanupListener();
  try {
    await invokeBackend("http_stream_disconnect", { sessionId: currentSession });
  } catch (error) {
    if (addEntry) ElMessage.error(String(error));
  }
  state.value = "closed";
  if (addEntry) addLog({ direction: "system", label: t("http.streamDisconnected"), data: "" });
}

function validateEvent() {
  const name = eventName.value.trim();
  if (!name) {
    ElMessage.warning(t("http.socketio.eventRequired"));
    return false;
  }
  if (isReservedSocketIOEvent(name)) {
    ElMessage.warning(t("http.socketio.eventReserved", { event: name }));
    return false;
  }
  const check = checkSocketIOArgs(eventArgs.value);
  if (!check.ok) {
    ElMessage.warning(t(check.reason === "notArray" ? "http.socketio.argsNotArray" : "http.socketio.argsInvalid"));
    return false;
  }
  return true;
}

async function emitEvent() {
  if (!connected.value || !sessionId || !validateEvent()) return;
  const name = eventName.value.trim();
  const files = attachments.value;
  const sentAt = Date.now();
  try {
    const result = await invokeBackend<{ ackId: number | null }>("http_socketio_emit", {
      sessionId,
      event: name,
      args: eventArgs.value,
      ack: requestAck.value,
      attachments: files.map((file) => ({ path: file.path })),
    });
    if (typeof result?.ackId === "number") pendingAcks.set(result.ackId, { event: name, sentAt });
    const meta = [
      requestAck.value ? t("http.socketio.waitingAck") : "",
      files.length ? files.map((file) => file.name).join(", ") : "",
    ]
      .filter(Boolean)
      .join(" · ");
    addLog({ direction: "sent", label: name, data: eventArgs.value.trim(), meta });
  } catch (error) {
    ElMessage.error(String(error));
  }
}

async function chooseAttachment() {
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  if (attachments.value.length >= SOCKETIO_MAX_ATTACHMENTS) {
    ElMessage.warning(t("http.socketio.attachmentLimit", { count: SOCKETIO_MAX_ATTACHMENTS }));
    return;
  }
  const selected = await chooseHttpRequestFile(t("http.socketio.chooseAttachment"));
  if (selected) attachments.value = [...attachments.value, selected];
}

function removeAttachment(index: number) {
  attachments.value = attachments.value.filter((_, itemIndex) => itemIndex !== index);
}

async function persistSavedEvents(next: SocketIOSavedEvent[]) {
  const previous = savedEvents.value;
  savedEvents.value = next;
  try {
    await saveSocketIOSavedEvents(next);
  } catch (error) {
    savedEvents.value = previous;
    ElMessage.error(t("http.socketio.saveFailed", { message: String(error) }));
    return false;
  }
  return true;
}

async function saveCurrentEvent() {
  if (!validateEvent()) return;
  const saved = await persistSavedEvents(
    upsertSocketIOSavedEvent(
      savedEvents.value,
      { event: eventName.value, args: eventArgs.value, ack: requestAck.value },
      () => createId("socketio-saved")
    )
  );
  if (saved) ElMessage.success(t("http.socketio.eventSaved"));
}

function applySavedEvent(id: string) {
  const item = savedEvents.value.find((entry) => entry.id === id);
  if (!item) return;
  eventName.value = item.event;
  eventArgs.value = item.args;
  requestAck.value = item.ack;
}

function removeSavedEvent(id: string) {
  void persistSavedEvents(savedEvents.value.filter((item) => item.id !== id));
}

function formatTime(timestamp: number) {
  return new Date(timestamp).toLocaleTimeString();
}

watch(visible, (isVisible) => {
  if (!isVisible) void disconnect(false);
});

onMounted(async () => {
  try {
    savedEvents.value = await loadSocketIOSavedEvents();
  } catch (error) {
    ElMessage.error(t("http.socketio.loadFailed", { message: String(error) }));
  }
});

onBeforeUnmount(() => {
  logBuffer.clear();
  void disconnect(false);
});
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="t('http.socketio.title')"
    width="min(960px, 94vw)"
    top="4vh"
    class="http-stream-dialog http-socketio-dialog http-fit-dialog"
    destroy-on-close
    append-to-body
  >
    <div class="http-stream-panel http-socketio-panel">
      <div class="http-stream-address">
        <el-input
          v-model="url"
          placeholder="http://localhost:3000"
          :disabled="busy"
          :aria-label="t('http.socketio.serverUrl')"
          @keyup.enter="connect"
        />
        <el-input
          v-model="namespace"
          class="http-socketio-namespace"
          placeholder="/"
          :disabled="busy"
          :aria-label="t('http.socketio.namespace')"
          :title="t('http.socketio.namespace')"
        >
          <template #prepend>{{ t("http.socketio.namespaceShort") }}</template>
        </el-input>
        <el-button v-if="!busy" type="primary" :icon="VideoPlay" @click="connect">
          {{ t("http.connect") }}
        </el-button>
        <el-button v-else :icon="VideoPause" :loading="state === 'connecting'" @click="disconnect()">
          {{ t("http.disconnect") }}
        </el-button>
      </div>

      <div class="http-stream-status">
        <span class="http-stream-status__dot" :class="`is-${state}`" />
        <strong>{{ t(`http.streamState.${state}` as never) }}</strong>
        <span>{{ t("http.socketio.hint") }}</span>
      </div>

      <section class="http-stream-section">
        <el-tabs v-model="settingsTab" class="http-socketio-settings">
          <el-tab-pane :label="t('http.socketio.authTab')" name="auth">
            <div class="http-socketio-auth">
              <el-input
                v-model="auth"
                type="textarea"
                resize="none"
                :rows="2"
                :disabled="busy"
                :placeholder="t('http.socketio.authPlaceholder')"
              />
              <el-input v-model="path" :disabled="busy" placeholder="/socket.io">
                <template #prepend>{{ t("http.socketio.path") }}</template>
              </el-input>
            </div>
          </el-tab-pane>
          <el-tab-pane
            v-for="kind in (['query', 'header'] as const)"
            :key="kind"
            :label="kind === 'query' ? t('http.socketio.queryTab') : t('http.headers')"
            :name="kind === 'query' ? 'query' : 'headers'"
          >
            <el-scrollbar class="http-stream-headers">
              <div v-for="(row, index) in rowsFor(kind).value" :key="row.id" class="http-stream-header-row">
                <el-checkbox v-model="row.enabled" :disabled="busy" />
                <el-select
                  v-if="kind === 'header'"
                  v-model="row.key"
                  filterable
                  allow-create
                  default-first-option
                  :disabled="busy"
                  :placeholder="t('http.key')"
                >
                  <el-option v-for="name in COMMON_HTTP_HEADER_NAMES" :key="name" :label="name" :value="name" />
                </el-select>
                <el-input v-else v-model="row.key" :disabled="busy" :placeholder="t('http.key')" />
                <el-input v-model="row.value" :disabled="busy" :placeholder="t('http.value')" />
                <el-button
                  text
                  :icon="Delete"
                  :disabled="busy"
                  :title="t('common.delete')"
                  @click="rowsFor(kind).value.splice(index, 1)"
                />
              </div>
              <el-button text size="small" :disabled="busy" @click="rowsFor(kind).value.push(createRow())">
                {{ t("common.add") }}
              </el-button>
            </el-scrollbar>
          </el-tab-pane>
        </el-tabs>
      </section>

      <section class="http-stream-section http-stream-section--logs">
        <header>
          <strong>{{ t("http.events") }}</strong>
          <span>{{ logs.length }}</span>
          <el-button text native-type="button" :disabled="logs.length === 0" @click="resetLogs">
            {{ t("common.clear") }}
          </el-button>
        </header>
        <el-scrollbar ref="logHost" class="http-stream-logs">
          <article v-for="entry in logs" :key="entry.id" :class="`is-${entry.direction}`">
            <div>
              <time>{{ formatTime(entry.timestamp) }}</time>
              <strong>{{ entry.label }}</strong>
              <span v-if="entry.meta">{{ entry.meta }}</span>
              <small v-if="entry.size !== undefined">{{ entry.size }} B</small>
            </div>
            <template v-if="entry.data">
              <pre>{{ expandedLogIds.has(entry.id) ? entry.data : streamLogPreview(entry.data) }}</pre>
              <el-button
                v-if="isStreamLogTruncated(entry.data)"
                text
                size="small"
                native-type="button"
                @click="toggleLogExpanded(entry.id)"
              >
                {{
                  expandedLogIds.has(entry.id)
                    ? t("http.streamCollapseMessage")
                    : t("http.streamExpandMessage", { size: entry.data.length })
                }}
              </el-button>
            </template>
          </article>
          <el-empty v-if="logs.length === 0" :description="t('http.streamEmpty')" :image-size="48" />
        </el-scrollbar>
      </section>

      <div class="http-socketio-compose">
        <div class="http-socketio-compose__head">
          <el-input
            v-model="eventName"
            class="http-socketio-event"
            :placeholder="t('http.socketio.eventPlaceholder')"
            :aria-label="t('http.socketio.eventName')"
          />
          <el-select
            class="http-socketio-saved"
            :model-value="''"
            filterable
            :placeholder="t('http.socketio.savedEvents')"
            :no-data-text="t('http.socketio.noSavedEvents')"
            @change="applySavedEvent"
          >
            <el-option v-for="item in savedEvents" :key="item.id" :label="item.event" :value="item.id">
              <div class="http-socketio-saved__option">
                <span>{{ item.event }}</span>
                <small>{{ item.args }}</small>
                <el-button
                  text
                  size="small"
                  :icon="Delete"
                  :title="t('common.delete')"
                  @click.stop="removeSavedEvent(item.id)"
                />
              </div>
            </el-option>
          </el-select>
          <el-button :icon="Star" :title="t('http.socketio.saveEvent')" @click="saveCurrentEvent">
            {{ t("common.save") }}
          </el-button>
          <el-checkbox v-model="requestAck">{{ t("http.socketio.requestAck") }}</el-checkbox>
          <el-button :icon="FolderOpened" :title="t('http.socketio.chooseAttachment')" @click="chooseAttachment">
            {{ t("http.socketio.attachment") }}
          </el-button>
        </div>
        <div v-if="attachments.length" class="http-socketio-attachments">
          <el-tag
            v-for="(file, index) in attachments"
            :key="`${file.path}-${index}`"
            closable
            size="small"
            :title="file.path"
            @close="removeAttachment(index)"
          >
            #{{ index }} {{ file.name }}
          </el-tag>
          <span>{{ t("http.socketio.attachmentHint") }}</span>
        </div>
        <div class="http-stream-compose">
          <el-input
            v-model="eventArgs"
            type="textarea"
            resize="none"
            :rows="3"
            :placeholder="t('http.socketio.argsPlaceholder')"
            @keydown.meta.enter.prevent="emitEvent"
            @keydown.ctrl.enter.prevent="emitEvent"
          />
          <el-button type="primary" :icon="Promotion" :disabled="!connected" @click="emitEvent">
            {{ t("http.socketio.emit") }}
          </el-button>
        </div>
      </div>
    </div>
  </el-dialog>
</template>
