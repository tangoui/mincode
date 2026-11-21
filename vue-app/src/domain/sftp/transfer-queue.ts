export type SftpTransferDirection = "upload" | "download";
export type SftpTransferOperation = "upload-file" | "download-entry" | "create-remote-directory";
export type SftpTransferStatus = "queued" | "running" | "completed" | "failed" | "cancelled";
export type SftpConflictStrategy = "overwrite" | "skip" | "auto-rename";

export interface SftpTransferProgress {
  bytesTransferred: number;
  totalBytes: number;
  speedBytesPerSecond: number;
  resumedFrom?: number;
  backendJobId?: string;
}

export interface SftpTransferItem extends SftpTransferProgress {
  id: string;
  sessionId: string;
  direction: SftpTransferDirection;
  operation: SftpTransferOperation;
  name: string;
  localPath: string;
  remotePath: string;
  isDirectory: boolean;
  status: SftpTransferStatus;
  conflictStrategy?: SftpConflictStrategy;
  error?: string;
  createdAt: number;
  startedAt?: number;
  finishedAt?: number;
  attempt: number;
}

export interface SftpTransferDraft {
  sessionId: string;
  direction: SftpTransferDirection;
  operation: SftpTransferOperation;
  name: string;
  localPath: string;
  remotePath: string;
  isDirectory?: boolean;
  conflictStrategy?: SftpConflictStrategy;
}

export interface SftpTransferSummary {
  total: number;
  queued: number;
  running: number;
  completed: number;
  failed: number;
  cancelled: number;
}

export function isSafeSftpEntryName(name: string) {
  const trimmed = name.trim();
  return Boolean(trimmed) && trimmed !== "." && trimmed !== ".." && !/[\\/]/.test(trimmed);
}

let transferSequence = 0;

export function createSftpTransferItem(
  draft: SftpTransferDraft,
  now = Date.now()
): SftpTransferItem {
  transferSequence += 1;
  return {
    ...draft,
    id: `sftp-transfer-${now}-${transferSequence}`,
    isDirectory: draft.isDirectory ?? false,
    status: "queued",
    createdAt: now,
    attempt: 0,
    bytesTransferred: 0,
    totalBytes: 0,
    speedBytesPerSecond: 0,
  };
}

export function createSftpBackendJobId(item: SftpTransferItem, now = Date.now()) {
  transferSequence += 1;
  return `sftpx-${now}-${transferSequence}-${item.attempt + 1}`;
}

export function summarizeSftpTransfers(items: readonly SftpTransferItem[]): SftpTransferSummary {
  const summary: SftpTransferSummary = {
    total: items.length,
    queued: 0,
    running: 0,
    completed: 0,
    failed: 0,
    cancelled: 0,
  };
  for (const item of items) summary[item.status] += 1;
  return summary;
}

export function nextQueuedSftpTransfer(
  items: readonly SftpTransferItem[]
): SftpTransferItem | undefined {
  return items.find((item) => item.status === "queued");
}

export function retrySftpTransfer(item: SftpTransferItem): SftpTransferItem {
  if (item.status !== "failed" && item.status !== "cancelled") return item;
  return {
    ...item,
    status: "queued",
    error: undefined,
    startedAt: undefined,
    finishedAt: undefined,
    backendJobId: undefined,
    speedBytesPerSecond: 0,
  };
}

export function cancelQueuedSftpTransfers(
  items: readonly SftpTransferItem[],
  now = Date.now()
): SftpTransferItem[] {
  return items.map((item) =>
    item.status === "queued" ? { ...item, status: "cancelled", finishedAt: now } : item
  );
}

export function removeSettledSftpTransfers(items: readonly SftpTransferItem[]) {
  return items.filter((item) => item.status === "queued" || item.status === "running");
}
