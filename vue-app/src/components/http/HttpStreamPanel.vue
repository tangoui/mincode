<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from "vue";
import { ElMessage, type ScrollbarInstance } from "element-plus";
import { Delete, Promotion, VideoPause, VideoPlay } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { subscribeHttpStream } from "@/services/http-stream-subscription";
import { COMMON_HTTP_HEADER_NAMES } from "@/domain/http/request-collection";
import {
  appendStreamLogs,
  createFrameListBuffer,
  isStreamLogTruncated,
  streamLogPreview,
} from "@/domain/http/stream-log";

type StreamKind = "ws" | "sse";
type ConnectionState = "idle" | "connecting" | "connected" | "closed" | "error";

interface HeaderRow {
  id: string;
  key: string;
  value: string;
  enabled: boolean;
}

interface StreamEvent {
  type: "opened" | "message" | "closed" | "error";
  timestamp: number;
  status?: number;
  protocol?: string;
  message?: string;
  messageType?: "text" | "binary";
  event?: string;
  data?: string;
  id?: string;
  retry?: number;
  size?: number;
}

interface LogEntry {
  id: string;
  direction: "system" | "sent" | "received";
  timestamp: number;
  label: string;
  data: string;
  size?: number;
}

let sequence = 0;

const props = defineProps<{ modelValue: boolean; kind: StreamKind }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useVueI18n();

const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit("update:modelValue", value),
});
const title = computed(() =>
  props.kind === "ws" ? t("http.wsClientTitle") : t("http.sseClientTitle")
);
const placeholder = computed(() =>
  props.kind === "ws" ? "wss://echo.websocket.org" : "https://example.com/events"
);
const state = ref<ConnectionState>("idle");
const url = ref("");
const headers = ref<HeaderRow[]>([createHeader()]);
const message = ref("");
const logs = shallowRef<LogEntry[]>([]);
const expandedLogIds = shallowRef(new Set<string>());
const logHost = ref<ScrollbarInstance>();
let sessionId = "";
let stopListening: (() => void) | undefined;

function createId(prefix: string) {
  sequence += 1;
  return `${prefix}-${Date.now()}-${sequence}`;
}

function createHeader(): HeaderRow {
  return { id: createId("stream-header"), key: "", value: "", enabled: true };
}

function stateText() {
  return t(`http.streamState.${state.value}` as never);
}

// 消息按帧合并后一次性写入列表，每帧只读一次 scrollHeight。
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

function addLog(direction: LogEntry["direction"], label: string, data = "", size?: number) {
  logBuffer.push({ id: createId("stream-log"), direction, timestamp: Date.now(), label, data, size });
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

function enabledHeaders(): [string, string][] {
  return headers.value
    .filter((row) => row.enabled && row.key.trim())
    .map((row) => [row.key.trim(), row.value]);
}

function handleEvent(event: StreamEvent) {
  if (event.type === "opened") {
    state.value = "connected";
    addLog("system", t("http.streamConnected"), event.protocol || String(event.status ?? ""));
    return;
  }
  if (event.type === "message") {
    const label =
      props.kind === "ws"
        ? event.messageType === "binary"
          ? t("http.binaryMessage")
          : t("http.textMessage")
        : event.event || "message";
    const metadata = props.kind === "sse" && event.id ? `id: ${event.id}` : "";
    addLog("received", label, [metadata, event.data ?? ""].filter(Boolean).join("\n"), event.size);
    return;
  }
  if (event.type === "error") {
    state.value = "error";
    addLog("system", t("http.streamError"), event.message ?? "");
    return;
  }
  state.value = "closed";
  addLog("system", t("http.streamDisconnected"));
  cleanupListener();
}

function cleanupListener() {
  stopListening?.();
  stopListening = undefined;
  sessionId = "";
}

async function connect() {
  if (!url.value.trim()) {
    ElMessage.warning(t("http.urlRequired"));
    return;
  }
  if (!isDesktopRuntime()) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  await disconnect(false);
  state.value = "connecting";
  const currentSession = createId(props.kind);
  sessionId = currentSession;
  const subscription = subscribeHttpStream<StreamEvent>(currentSession, {
    onEvent: handleEvent,
    onTransportLost() {
      if (sessionId !== currentSession) return;
      state.value = "error";
      addLog("system", t("http.streamError"), t("http.streamTransportDisconnected"));
      stopListening = undefined;
      sessionId = "";
    },
  });
  stopListening = subscription.stop;
  try {
    await subscription.ready;
    await invokeBackend(props.kind === "ws" ? "http_ws_connect" : "http_sse_connect", {
      sessionId: currentSession,
      url: url.value.trim(),
      headers: enabledHeaders(),
    });
    if (sessionId !== currentSession) {
      await invokeBackend("http_stream_disconnect", { sessionId: currentSession });
      return;
    }
    if (state.value === "connecting") state.value = "connected";
  } catch (error) {
    if (sessionId === currentSession) {
      state.value = "error";
      addLog("system", t("http.streamError"), String(error));
      cleanupListener();
    }
  }
}

async function disconnect(addEntry = true) {
  const currentSession = sessionId;
  if (!currentSession) {
    if (state.value === "connecting" || state.value === "connected") state.value = "closed";
    return;
  }
  cleanupListener();
  try {
    await invokeBackend("http_stream_disconnect", { sessionId: currentSession });
  } catch (error) {
    if (addEntry) ElMessage.error(String(error));
  }
  state.value = "closed";
  if (addEntry) addLog("system", t("http.streamDisconnected"));
}

async function sendMessage() {
  if (props.kind !== "ws" || state.value !== "connected" || !sessionId) return;
  if (!message.value) {
    ElMessage.warning(t("http.messageRequired"));
    return;
  }
  const payload = message.value;
  try {
    await invokeBackend("http_ws_send", { sessionId, data: payload });
    addLog("sent", t("http.textMessage"), payload, new TextEncoder().encode(payload).length);
    message.value = "";
  } catch (error) {
    ElMessage.error(String(error));
  }
}

function clearLogs() {
  resetLogs();
}

function formatTime(timestamp: number) {
  return new Date(timestamp).toLocaleTimeString();
}

watch(
  () => props.kind,
  async () => {
    await disconnect(false);
    state.value = "idle";
    resetLogs();
    url.value = "";
    message.value = "";
  }
);

watch(visible, (isVisible) => {
  if (!isVisible) void disconnect(false);
});

onBeforeUnmount(() => {
  logBuffer.clear();
  void disconnect(false);
});
</script>

<template>
  <el-dialog v-model="visible" :title="title" width="min(900px, 94vw)" top="4vh" class="http-stream-dialog http-fit-dialog" destroy-on-close append-to-body>
    <div class="http-stream-panel">
      <div class="http-stream-address">
        <el-input v-model="url" :placeholder="placeholder" @keyup.enter="connect" />
        <el-button
          v-if="state !== 'connected' && state !== 'connecting'"
          type="primary"
          :icon="VideoPlay"
          @click="connect"
        >
          {{ t("http.connect") }}
        </el-button>
        <el-button v-else :icon="VideoPause" :loading="state === 'connecting'" @click="disconnect()">
          {{ t("http.disconnect") }}
        </el-button>
      </div>

      <div class="http-stream-status">
        <span class="http-stream-status__dot" :class="`is-${state}`" />
        <strong>{{ stateText() }}</strong>
        <span>{{ props.kind === "ws" ? t("http.wsHint") : t("http.sseHint") }}</span>
      </div>

      <section class="http-stream-section">
        <header>
          <strong>{{ t("http.headers") }}</strong>
          <el-button text native-type="button" @click="headers.push(createHeader())">{{ t("common.add") }}</el-button>
        </header>
        <el-scrollbar class="http-stream-headers">
          <div v-for="(row, index) in headers" :key="row.id" class="http-stream-header-row">
            <el-checkbox v-model="row.enabled" />
            <el-select v-model="row.key" filterable allow-create default-first-option :placeholder="t('http.key')">
              <el-option v-for="name in COMMON_HTTP_HEADER_NAMES" :key="name" :label="name" :value="name" />
            </el-select>
            <el-input v-model="row.value" :placeholder="t('http.value')" />
            <el-button text :icon="Delete" :title="t('common.delete')" @click="headers.splice(index, 1)" />
          </div>
        </el-scrollbar>
      </section>

      <section class="http-stream-section http-stream-section--logs">
        <header>
          <strong>{{ props.kind === "ws" ? t("http.messages") : t("http.events") }}</strong>
          <span>{{ logs.length }}</span>
          <el-button text native-type="button" :disabled="logs.length === 0" @click="clearLogs">{{ t("common.clear") }}</el-button>
        </header>
        <el-scrollbar ref="logHost" class="http-stream-logs">
          <article v-for="entry in logs" :key="entry.id" :class="`is-${entry.direction}`">
            <div>
              <time>{{ formatTime(entry.timestamp) }}</time>
              <strong>{{ entry.label }}</strong>
              <small v-if="entry.size !== undefined">{{ entry.size }} B</small>
            </div>
            <template v-if="entry.data">
              <pre>{{
                expandedLogIds.has(entry.id) ? entry.data : streamLogPreview(entry.data)
              }}</pre>
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

      <div v-if="props.kind === 'ws'" class="http-stream-compose">
        <el-input
          v-model="message"
          type="textarea"
          resize="none"
          :rows="3"
          :disabled="state !== 'connected'"
          :placeholder="t('http.messagePlaceholder')"
          @keydown.meta.enter.prevent="sendMessage"
          @keydown.ctrl.enter.prevent="sendMessage"
        />
        <el-button type="primary" :icon="Promotion" :disabled="state !== 'connected'" @click="sendMessage">
          {{ t("http.sendMessage") }}
        </el-button>
      </div>
    </div>
  </el-dialog>
</template>
