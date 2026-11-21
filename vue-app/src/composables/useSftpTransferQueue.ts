import { computed, ref } from "vue";
import {
  cancelQueuedSftpTransfers,
  createSftpBackendJobId,
  createSftpTransferItem,
  nextQueuedSftpTransfer,
  removeSettledSftpTransfers,
  retrySftpTransfer,
  summarizeSftpTransfers,
  type SftpTransferDraft,
  type SftpTransferItem,
  type SftpTransferProgress,
} from "@/domain/sftp/transfer-queue";

interface SftpTransferExecutionResult {
  status: "completed" | "cancelled" | "failed";
  error?: string;
}

interface SftpTransferQueueOptions {
  execute: (
    item: SftpTransferItem,
    reportProgress: (progress: SftpTransferProgress) => void
  ) => Promise<SftpTransferExecutionResult | void>;
  cancel?: (item: SftpTransferItem) => Promise<void>;
  onSettled?: (item: SftpTransferItem) => void | Promise<void>;
}

/** Completed transfers kept in the list; failed and cancelled ones stay until cleared because they can be retried. */
export const SFTP_COMPLETED_TRANSFER_LIMIT = 50;

/** Drops the oldest completed transfers beyond `limit`; returns the input array unchanged when nothing is pruned. */
export function pruneCompletedSftpTransfers<T extends Pick<SftpTransferItem, "status">>(
  items: readonly T[],
  limit = SFTP_COMPLETED_TRANSFER_LIMIT
): readonly T[] {
  let excess = items.filter((item) => item.status === "completed").length - Math.max(0, limit);
  if (excess <= 0) return items;
  return items.filter((item) => {
    if (excess > 0 && item.status === "completed") {
      excess -= 1;
      return false;
    }
    return true;
  });
}

type FrameHandle = { kind: "raf"; id: number } | { kind: "timeout"; id: ReturnType<typeof setTimeout> };

function requestFrame(callback: () => void): FrameHandle {
  if (typeof requestAnimationFrame === "function") {
    return { kind: "raf", id: requestAnimationFrame(() => callback()) };
  }
  return { kind: "timeout", id: setTimeout(callback, 16) };
}

function cancelFrame(handle: FrameHandle) {
  if (handle.kind === "raf") cancelAnimationFrame(handle.id);
  else clearTimeout(handle.id);
}

export function useSftpTransferQueue(options: SftpTransferQueueOptions) {
  const items = ref<SftpTransferItem[]>([]);
  const paused = ref(false);
  const processing = ref(false);
  const summary = computed(() => summarizeSftpTransfers(items.value));
  const active = computed(() => items.value.find((item) => item.status === "running"));
  let disposed = false;
  // Backend progress arrives every ~100ms per job; coalesce it into at most one reactive write per frame.
  const pendingProgress = new Map<SftpTransferItem, SftpTransferProgress>();
  let progressFrame: FrameHandle | undefined;

  function flushProgress() {
    progressFrame = undefined;
    for (const [item, progress] of pendingProgress) Object.assign(item, progress);
    pendingProgress.clear();
  }

  function queueProgress(item: SftpTransferItem, progress: SftpTransferProgress) {
    pendingProgress.set(item, { ...pendingProgress.get(item), ...progress });
    if (!progressFrame) progressFrame = requestFrame(flushProgress);
  }

  function applyPendingProgress(item: SftpTransferItem) {
    const progress = pendingProgress.get(item);
    if (!progress) return;
    pendingProgress.delete(item);
    Object.assign(item, progress);
  }

  function pruneCompleted() {
    const pruned = pruneCompletedSftpTransfers(items.value);
    if (pruned !== items.value) items.value = [...pruned];
  }

  async function drain() {
    if (processing.value || paused.value || disposed) return;
    processing.value = true;
    try {
      while (!paused.value && !disposed) {
        const item = nextQueuedSftpTransfer(items.value);
        if (!item) break;
        item.status = "running";
        item.error = undefined;
        item.startedAt = Date.now();
        item.finishedAt = undefined;
        item.attempt += 1;
        item.backendJobId = createSftpBackendJobId(item);
        item.speedBytesPerSecond = 0;
        let settled = false;
        try {
          const result = await options.execute(item, (progress) => {
            if (!settled && !disposed) queueProgress(item, progress);
          });
          settled = true;
          applyPendingProgress(item);
          item.status = result?.status ?? "completed";
          item.error = result?.error;
        } catch (error) {
          settled = true;
          applyPendingProgress(item);
          item.status = "failed";
          item.error = String(error);
        } finally {
          item.finishedAt = Date.now();
          item.speedBytesPerSecond = 0;
          await options.onSettled?.(item);
          pruneCompleted();
        }
      }
    } finally {
      processing.value = false;
      if (!paused.value && !disposed && nextQueuedSftpTransfer(items.value)) void drain();
    }
  }

  function enqueue(drafts: readonly SftpTransferDraft[]) {
    if (drafts.length === 0 || disposed) return [];
    const created = drafts.map((draft) => createSftpTransferItem(draft));
    items.value.push(...created);
    void drain();
    return created;
  }

  function pause() {
    paused.value = true;
  }

  function resume() {
    paused.value = false;
    void drain();
  }

  function cancelQueued() {
    items.value = cancelQueuedSftpTransfers(items.value);
  }

  async function cancel(id: string) {
    const item = items.value.find((candidate) => candidate.id === id);
    if (!item) return;
    if (item.status === "queued") {
      item.status = "cancelled";
      item.finishedAt = Date.now();
      return;
    }
    if (item.status === "running") await options.cancel?.(item);
  }

  function retry(id: string) {
    items.value = items.value.map((item) => (item.id === id ? retrySftpTransfer(item) : item));
    if (!paused.value) void drain();
  }

  function clearSettled() {
    items.value = removeSettledSftpTransfers(items.value);
  }

  function dispose() {
    disposed = true;
    paused.value = true;
    if (progressFrame) cancelFrame(progressFrame);
    progressFrame = undefined;
    pendingProgress.clear();
    cancelQueued();
    const running = active.value;
    if (running) void options.cancel?.(running).catch(() => undefined);
  }

  return {
    items,
    active,
    summary,
    paused,
    processing,
    enqueue,
    pause,
    resume,
    cancel,
    cancelQueued,
    retry,
    clearSettled,
    dispose,
  };
}

export type SftpTransferQueue = ReturnType<typeof useSftpTransferQueue>;
