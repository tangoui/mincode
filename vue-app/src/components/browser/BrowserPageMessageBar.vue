<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { ArrowDown, ArrowUp, Close, Document, FolderOpened } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import type { MessageKey } from "@/i18n/locales/zh";
import {
  completeEmbeddedBrowserDialog,
  parseBrowserToolError,
  revealEmbeddedBrowserDownload,
} from "@/services/browser-page-service";
import {
  useBrowserPageMessagesStore,
  type BrowserDownloadRecord,
  type BrowserPageDialog,
} from "@/stores/browser-page-messages";

/**
 * 浏览器区域顶部的页面消息栏（设计 4.2）：原生 WKWebView 叠在 Vue 之上，
 * HTML 盖不住它，所以这里不用浮层；视图把原生视图的上边缘下移本栏的实际
 * 高度（通过 `height` 事件上报）。同一时刻按“弹框 > 文件选择 > 下载”显示一项。
 * 弹框正文是不可信的网页内容，只按文本渲染。
 */
const props = defineProps<{ tabId: string }>();
const emit = defineEmits<{ height: [value: number] }>();

const { t } = useVueI18n();
const browserWorkspace = useBrowserPageMessagesStore();

const LONG_MESSAGE_CHARS = 280;
const LONG_MESSAGE_LINES = 4;
const PROMPT_TEXT_LIMIT = 2000;
const KNOWN_DOWNLOAD_ERRORS = new Set([
  "cancelled",
  "destination_exists",
  "redirect_blocked",
  "replace_failed",
  "save_panel_unavailable",
  "tab_closed",
  "user_cancelled",
]);

const barEl = ref<HTMLElement>();
const okButton = ref<{ $el?: HTMLElement } | HTMLElement>();
const promptInput = ref<{ focus(): void; select?(): void }>();
const promptText = ref("");
const messageExpanded = ref(false);
const downloadsExpanded = ref(true);
const responding = ref(false);
const idPrefix = `browser-page-message-${Math.random().toString(36).slice(2, 10)}`;

const dialog = computed<BrowserPageDialog | undefined>(() =>
  props.tabId ? browserWorkspace.pendingDialogs[props.tabId] : undefined
);
const fileChooserOpen = computed(() => Boolean(props.tabId && browserWorkspace.fileChooserOpen[props.tabId]));
const downloads = computed(() => browserWorkspace.visibleDownloads);
const mode = computed<"dialog" | "file-chooser" | "downloads" | "">(() => {
  if (dialog.value) return "dialog";
  if (fileChooserOpen.value) return "file-chooser";
  if (downloads.value.length) return "downloads";
  return "";
});

/** Origins are scheme://host[:port]; opaque ones (data: pages) arrive empty or host-less. */
function hasNamedOrigin(origin: string) {
  return Boolean(origin) && !origin.endsWith("://");
}

const dialogTitle = computed(() => {
  const origin = dialog.value?.origin ?? "";
  return hasNamedOrigin(origin)
    ? t("browser.pageMessage.dialogTitle", { origin })
    : t("browser.pageMessage.dialogTitleUnknown");
});
const messageIsLong = computed(() => {
  const message = dialog.value?.message ?? "";
  return message.length > LONG_MESSAGE_CHARS || message.split("\n").length > LONG_MESSAGE_LINES;
});
const latestDownload = computed(() => downloads.value[downloads.value.length - 1]);

function isEditableElement(element: Element | null) {
  if (!(element instanceof HTMLElement)) return false;
  if (element.isContentEditable) return true;
  if (element instanceof HTMLTextAreaElement || element instanceof HTMLSelectElement) return true;
  if (!(element instanceof HTMLInputElement)) return false;
  return !["button", "checkbox", "radio", "submit", "reset", "range", "color", "file", "image"].includes(element.type);
}

/**
 * Moves focus to the dialog's primary action, unless the user is typing in a
 * text box elsewhere (for example the AI chat): a page must not be able to
 * pull focus out of what the user is writing.
 */
function focusPrimaryAction() {
  const active = document.activeElement;
  if (active && barEl.value?.contains(active)) return;
  if (isEditableElement(active)) return;
  if (dialog.value?.kind === "prompt") {
    promptInput.value?.focus();
    promptInput.value?.select?.();
    return;
  }
  const button = okButton.value;
  const element = button instanceof HTMLElement ? button : button?.$el;
  element?.focus();
}

watch(
  () => dialog.value?.id,
  async (id) => {
    messageExpanded.value = false;
    responding.value = false;
    promptText.value = dialog.value?.defaultText ?? "";
    if (!id) return;
    await nextTick();
    focusPrimaryAction();
  },
  { immediate: true }
);

async function respond(accept: boolean) {
  const current = dialog.value;
  if (!current || responding.value) return;
  responding.value = true;
  const text = accept && current.kind === "prompt" ? promptText.value.slice(0, PROMPT_TEXT_LIMIT) : "";
  try {
    await completeEmbeddedBrowserDialog({ tabId: current.tabId, dialogId: current.id, accept, text });
    browserWorkspace.recordDialogClosed({ tabId: current.tabId, dialogId: current.id });
  } catch (error) {
    const parsed = parseBrowserToolError(error);
    if (parsed.code === "dialog_already_handled") {
      // AI (or the page navigating away) answered first; the bar just catches up.
      browserWorkspace.recordDialogClosed({ tabId: current.tabId, dialogId: current.id });
      return;
    }
    ElMessage.error(parsed.message || t("browser.pageMessage.dialogAnswerFailed"));
  } finally {
    responding.value = false;
  }
}

function handleDialogKeydown(event: KeyboardEvent) {
  if (event.key !== "Escape" || !dialog.value) return;
  event.preventDefault();
  // Escape is Cancel, as in the browser's own dialogs; an alert only has OK.
  void respond(dialog.value.kind === "alert");
}

function downloadPercent(item: BrowserDownloadRecord) {
  if (!item.totalBytes || item.totalBytes <= 0) return undefined;
  return Math.max(0, Math.min(100, Math.floor((item.bytes / item.totalBytes) * 100)));
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${unit === 0 ? size : size.toFixed(1)} ${units[unit]}`;
}

function downloadStatus(item: BrowserDownloadRecord) {
  switch (item.state) {
    case "pending":
      return t("browser.pageMessage.downloadPending");
    case "saving": {
      const percent = downloadPercent(item);
      return percent === undefined
        ? t("browser.pageMessage.downloadSavingBytes", { bytes: formatBytes(item.bytes) })
        : t("browser.pageMessage.downloadSaving", { percent: `${percent}%` });
    }
    case "completed":
      return t("browser.pageMessage.downloadCompleted");
    case "cancelled":
      return t("browser.pageMessage.downloadCancelled");
    case "failed":
      return item.error ? downloadErrorText(item.error) : t("browser.pageMessage.downloadFailed");
    default:
      return item.state;
  }
}

function downloadErrorText(error: string) {
  return KNOWN_DOWNLOAD_ERRORS.has(error)
    ? t(`browser.pageMessage.downloadError.${error}` as MessageKey)
    : t("browser.pageMessage.downloadFailedWith", { error });
}

async function reveal(item: BrowserDownloadRecord) {
  try {
    await revealEmbeddedBrowserDownload(item.id);
  } catch (error) {
    ElMessage.error(parseBrowserToolError(error).message || t("browser.pageMessage.revealFailed"));
  }
}

let observer: ResizeObserver | undefined;
let reportedHeight = 0;

function reportHeight(value: number) {
  const height = Math.max(0, Math.ceil(value));
  if (height === reportedHeight) return;
  reportedHeight = height;
  emit("height", height);
}

function handleResize(entries: ResizeObserverEntry[]) {
  const entry = entries[entries.length - 1];
  if (!entry || entry.target !== barEl.value) return;
  reportHeight(entry.borderBoxSize?.[0]?.blockSize ?? entry.contentRect.height);
}

watch(barEl, (element, previous) => {
  if (previous) observer?.unobserve(previous);
  if (element) observer?.observe(element);
  else reportHeight(0);
}, { flush: "post" });

let unsubscribe: (() => void) | undefined;

onMounted(() => {
  unsubscribe = browserWorkspace.subscribePageEvents();
  observer = new ResizeObserver(handleResize);
  if (barEl.value) observer.observe(barEl.value);
});

onBeforeUnmount(() => {
  observer?.disconnect();
  observer = undefined;
  unsubscribe?.();
  reportHeight(0);
});
</script>

<template>
  <div
    v-if="mode"
    ref="barEl"
    class="browser-page-message-bar"
    :class="`is-${mode}`"
  >
    <div
      v-if="mode === 'dialog' && dialog"
      role="alertdialog"
      class="browser-page-message-bar__dialog"
      :aria-labelledby="`${idPrefix}-title`"
      :aria-describedby="`${idPrefix}-message`"
      @keydown="handleDialogKeydown"
    >
      <div class="browser-page-message-bar__body">
        <strong :id="`${idPrefix}-title`" class="browser-page-message-bar__title">{{ dialogTitle }}</strong>
        <p
          :id="`${idPrefix}-message`"
          class="browser-page-message-bar__message"
          :class="{ 'is-collapsed': messageIsLong && !messageExpanded }"
        >{{ dialog.message }}</p>
        <el-button
          v-if="messageIsLong"
          text
          size="small"
          class="browser-page-message-bar__expand"
          :aria-expanded="messageExpanded ? 'true' : 'false'"
          :aria-controls="`${idPrefix}-message`"
          @click="messageExpanded = !messageExpanded"
        >
          {{ t(messageExpanded ? "browser.pageMessage.collapseMessage" : "browser.pageMessage.expandMessage") }}
        </el-button>
        <el-input
          v-if="dialog.kind === 'prompt'"
          ref="promptInput"
          v-model="promptText"
          size="small"
          class="browser-page-message-bar__prompt"
          :maxlength="PROMPT_TEXT_LIMIT"
          :aria-label="t('browser.pageMessage.promptLabel')"
          @keydown.enter.prevent="respond(true)"
        />
      </div>
      <div class="browser-page-message-bar__actions">
        <el-button
          v-if="dialog.kind !== 'alert'"
          size="small"
          class="browser-page-message-bar__cancel"
          :disabled="responding"
          @click="respond(false)"
        >
          {{ t("browser.pageMessage.cancel") }}
        </el-button>
        <el-button
          ref="okButton"
          type="primary"
          size="small"
          class="browser-page-message-bar__ok"
          :loading="responding"
          @click="respond(true)"
        >
          {{ t("browser.pageMessage.ok") }}
        </el-button>
      </div>
    </div>

    <div
      v-else-if="mode === 'file-chooser'"
      role="status"
      class="browser-page-message-bar__file-chooser"
    >
      <el-icon><FolderOpened /></el-icon>
      <span>{{ t("browser.pageMessage.fileChooserWaiting") }}</span>
    </div>

    <div
      v-else-if="mode === 'downloads'"
      role="status"
      class="browser-page-message-bar__downloads"
      :aria-label="t('browser.pageMessage.downloadsLabel')"
    >
      <div class="browser-page-message-bar__downloads-header">
        <el-icon><Document /></el-icon>
        <span v-if="!downloadsExpanded && latestDownload" class="browser-page-message-bar__summary">
          <span class="browser-page-message-bar__filename">{{ latestDownload.filename }}</span>
          <span class="browser-page-message-bar__state">{{ downloadStatus(latestDownload) }}</span>
          <span v-if="downloads.length > 1" class="browser-page-message-bar__more">
            {{ t("browser.pageMessage.moreDownloads", { count: downloads.length - 1 }) }}
          </span>
        </span>
        <span v-else class="browser-page-message-bar__heading">{{ t("browser.pageMessage.downloadsLabel") }}</span>
        <el-button
          text
          size="small"
          class="browser-page-message-bar__collapse"
          :icon="downloadsExpanded ? ArrowUp : ArrowDown"
          :aria-expanded="downloadsExpanded ? 'true' : 'false'"
          :aria-label="t(downloadsExpanded ? 'browser.pageMessage.collapseDownloads' : 'browser.pageMessage.expandDownloads')"
          :title="t(downloadsExpanded ? 'browser.pageMessage.collapseDownloads' : 'browser.pageMessage.expandDownloads')"
          @click="downloadsExpanded = !downloadsExpanded"
        />
        <el-button
          text
          size="small"
          class="browser-page-message-bar__dismiss"
          :icon="Close"
          :aria-label="t('browser.pageMessage.dismissDownloads')"
          :title="t('browser.pageMessage.dismissDownloads')"
          @click="browserWorkspace.dismissDownloads()"
        />
      </div>
      <ul v-if="downloadsExpanded" class="browser-page-message-bar__download-list">
        <li
          v-for="item in downloads"
          :key="item.id"
          class="browser-page-message-bar__download"
          :class="`is-${item.state}`"
        >
          <span class="browser-page-message-bar__filename" :title="item.filename">{{ item.filename }}</span>
          <el-progress
            v-if="item.state === 'saving' && downloadPercent(item) !== undefined"
            class="browser-page-message-bar__progress"
            :percentage="downloadPercent(item)"
            :show-text="false"
            :stroke-width="4"
          />
          <span class="browser-page-message-bar__state">{{ downloadStatus(item) }}</span>
          <el-button
            v-if="item.state === 'completed'"
            text
            size="small"
            type="primary"
            class="browser-page-message-bar__reveal"
            @click="reveal(item)"
          >
            {{ t("browser.pageMessage.revealInFinder") }}
          </el-button>
        </li>
      </ul>
    </div>
  </div>
</template>
