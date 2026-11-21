import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

/** A page's JavaScript alert/confirm/prompt, as reported by `browser:dialog-opened`. */
export interface BrowserPageDialog {
  id: string;
  tabId: string;
  kind: "alert" | "confirm" | "prompt" | string;
  message: string;
  defaultText: string;
  /** scheme://host[:port]; empty for opaque origins such as data: pages. */
  origin: string;
  openedAt: number;
}

export interface BrowserDialogClosed {
  tabId: string;
  dialogId: string;
  by: string;
  accepted: boolean;
}

export interface BrowserFileChooserState {
  tabId: string;
  open: boolean;
  selectedCount: number;
}

export type BrowserDownloadState = "pending" | "saving" | "completed" | "failed" | "cancelled";

/** A page download, as reported by `browser:download-updated` (file name only, never a path). */
export interface BrowserDownloadRecord {
  id: string;
  tabId: string;
  origin: string;
  filename: string;
  state: BrowserDownloadState | string;
  error?: string;
  bytes: number;
  totalBytes: number;
  startedAt: number;
  finishedAt?: number;
}

/** Downloads the page blocked because nobody clicked: the user did not ask for them, so the bar stays quiet. */
export function isBlockedPageDownload(record: BrowserDownloadRecord) {
  return record.state === "cancelled" && record.error === "no_user_action";
}

const MAX_TRACKED_DOWNLOADS = 20;

/**
 * 页面消息栏（设计 4.2–4.4）的状态：每个标签页至多一个待处理弹框、
 * 文件选择框是否打开，以及最近的下载记录（按出现顺序，最多 20 条）。
 * 只有懒加载的浏览器工作区用到它，所以和 browser-workspace store 分开，
 * 不进入口包。
 */
export const useBrowserPageMessagesStore = defineStore("browser-page-messages", () => {
  const pendingDialogs = ref<Record<string, BrowserPageDialog>>({});
  const fileChooserOpen = ref<Record<string, boolean>>({});
  const downloads = ref<BrowserDownloadRecord[]>([]);
  const dismissedDownloadIds = ref<Set<string>>(new Set());

  /** 标签页关闭或原生视图被拆除：忘掉它的页面消息。 */
  function forgetTab(tabId: string) {
    delete pendingDialogs.value[tabId];
    delete fileChooserOpen.value[tabId];
  }

  function recordDialogOpened(dialog: BrowserPageDialog) {
    if (!dialog?.tabId || !dialog.id) return;
    pendingDialogs.value[dialog.tabId] = {
      ...dialog,
      message: typeof dialog.message === "string" ? dialog.message : "",
      defaultText: typeof dialog.defaultText === "string" ? dialog.defaultText : "",
      origin: typeof dialog.origin === "string" ? dialog.origin : "",
    };
  }

  /** Drops the dialog once anyone (user, AI or system) answered it; a newer dialog on the tab stays. */
  function recordDialogClosed(closed: Pick<BrowserDialogClosed, "tabId" | "dialogId">) {
    const current = pendingDialogs.value[closed?.tabId];
    if (current && (!closed.dialogId || current.id === closed.dialogId)) {
      delete pendingDialogs.value[closed.tabId];
    }
  }

  function recordFileChooser(state: BrowserFileChooserState) {
    if (!state?.tabId) return;
    if (state.open) fileChooserOpen.value[state.tabId] = true;
    else delete fileChooserOpen.value[state.tabId];
  }

  function recordDownload(record: BrowserDownloadRecord) {
    if (!record?.id) return;
    const index = downloads.value.findIndex((item) => item.id === record.id);
    if (index >= 0) downloads.value.splice(index, 1, record);
    else downloads.value.push(record);
    // 被关掉的提示在同一下载有新进展（例如保存完成）时重新出现。
    dismissedDownloadIds.value.delete(record.id);
    while (downloads.value.length > MAX_TRACKED_DOWNLOADS) {
      const dropped = downloads.value.shift();
      if (dropped) dismissedDownloadIds.value.delete(dropped.id);
    }
  }

  /** Downloads the message bar shows: not dismissed and not silently blocked by the page gate. */
  const visibleDownloads = computed(() =>
    downloads.value.filter((item) => !dismissedDownloadIds.value.has(item.id) && !isBlockedPageDownload(item))
  );

  function dismissDownloads() {
    for (const item of downloads.value) dismissedDownloadIds.value.add(item.id);
  }

  let pageEventSubscribers = 0;
  let pageEventUnsubscribers: Array<() => void> = [];

  /**
   * Subscribes to the backend's page events (dialogs, file chooser, downloads).
   * Reference counted; the returned function unsubscribes this caller.
   */
  function subscribePageEvents() {
    pageEventSubscribers += 1;
    if (pageEventSubscribers === 1 && isDesktopRuntime()) {
      pageEventUnsubscribers = [
        listenAppEvent<BrowserPageDialog>("browser:dialog-opened", recordDialogOpened),
        listenAppEvent<BrowserDialogClosed>("browser:dialog-closed", recordDialogClosed),
        listenAppEvent<BrowserFileChooserState>("browser:file-chooser", recordFileChooser),
        listenAppEvent<BrowserDownloadRecord>("browser:download-updated", recordDownload),
      ];
    }
    let released = false;
    return () => {
      if (released) return;
      released = true;
      pageEventSubscribers -= 1;
      if (pageEventSubscribers > 0) return;
      for (const unsubscribe of pageEventUnsubscribers) unsubscribe();
      pageEventUnsubscribers = [];
      pendingDialogs.value = {};
      fileChooserOpen.value = {};
    };
  }

  return {
    pendingDialogs,
    fileChooserOpen,
    downloads,
    visibleDownloads,
    forgetTab,
    recordDialogOpened,
    recordDialogClosed,
    recordFileChooser,
    recordDownload,
    dismissDownloads,
    subscribePageEvents,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useBrowserPageMessagesStore, import.meta.hot));
}
