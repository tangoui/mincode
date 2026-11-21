import { retainWithinJsonBudget, truncateUtf8 } from "@/domain/persistence/byte-budget";

export const SSH_BATCH_HISTORY_MAX_ITEMS = 20;
export const SSH_BATCH_HISTORY_MAX_TOTAL_BYTES = 1024 * 1024;
export const SSH_BATCH_RESULT_MAX_OUTPUT_BYTES = 64 * 1024;
export const SSH_BATCH_OUTPUT_TRUNCATED_SUFFIX = "\n... (persisted output truncated)";

export interface PersistedBatchResult {
  id: string;
  host: string;
  stdout: string;
  stderr: string;
  exitCode?: number;
  durationMs: number;
  error?: string;
}

export interface PersistedBatchHistoryEntry<T extends PersistedBatchResult = PersistedBatchResult> {
  id: string;
  command: string;
  serverHosts: string[];
  timestamp: number;
  results: T[];
}

function normalizeResult<T extends PersistedBatchResult>(result: T): T {
  const stdoutBudget = Math.floor(SSH_BATCH_RESULT_MAX_OUTPUT_BYTES / 2);
  const stderrBudget = SSH_BATCH_RESULT_MAX_OUTPUT_BYTES - stdoutBudget;
  return {
    ...result,
    stdout: truncateUtf8(result.stdout, stdoutBudget, SSH_BATCH_OUTPUT_TRUNCATED_SUFFIX),
    stderr: truncateUtf8(result.stderr, stderrBudget, SSH_BATCH_OUTPUT_TRUNCATED_SUFFIX),
    error: result.error
      ? truncateUtf8(result.error, 8 * 1024, SSH_BATCH_OUTPUT_TRUNCATED_SUFFIX)
      : undefined,
  };
}

export function normalizeSshBatchHistoryForPersistence<
  T extends PersistedBatchHistoryEntry,
>(entries: readonly T[]) {
  return retainWithinJsonBudget(entries, {
    maxItems: SSH_BATCH_HISTORY_MAX_ITEMS,
    maxTotalBytes: SSH_BATCH_HISTORY_MAX_TOTAL_BYTES,
    normalize: (entry) => ({
      ...entry,
      command: truncateUtf8(entry.command, 16 * 1024, SSH_BATCH_OUTPUT_TRUNCATED_SUFFIX),
      serverHosts: entry.serverHosts.map((host) => truncateUtf8(host, 1024)),
      results: entry.results.map(normalizeResult),
    }),
  });
}
