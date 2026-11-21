import type { DatabaseQueryResult } from "./types";

export interface DatabaseQueryResultChunk {
  queryId: string;
  sequence: number;
  columns?: string[];
  rows: unknown[][];
  estimatedBytes: number;
}

export interface DatabaseQueryStreamSummary {
  queryId: string;
  rowCount: number;
  chunkCount: number;
  affectedRows?: number | null;
  executionMs: number;
  notice?: string | null;
}

interface QueryResultStreamOptions {
  maxRows?: number;
  maxBytes?: number;
}

export type QueryResultProgressScheduler = (callback: () => void) => () => void;

const scheduleQueryResultProgress: QueryResultProgressScheduler = (callback) => {
  if (typeof requestAnimationFrame === "function") {
    const frameId = requestAnimationFrame(callback);
    return () => cancelAnimationFrame(frameId);
  }
  const timeoutId = globalThis.setTimeout(callback, 16);
  return () => globalThis.clearTimeout(timeoutId);
};

export function createQueryResultStream(
  queryId: string,
  options: QueryResultStreamOptions = {},
) {
  const maxRows = options.maxRows ?? 5_000;
  const maxBytes = options.maxBytes ?? 16 * 1024 * 1024;
  let columns: string[] = [];
  let rows: unknown[][] = [];
  let nextSequence = 0;
  let retainedBytes = 0;
  let released = false;
  let failure: Error | undefined;
  let pendingSummary: DatabaseQueryStreamSummary | undefined;
  let resolveResult: ((result: DatabaseQueryResult) => void) | undefined;
  let rejectResult: ((reason: Error) => void) | undefined;

  const buildResult = (summary: DatabaseQueryStreamSummary): DatabaseQueryResult => {
    const result: DatabaseQueryResult = {
      columns,
      rows,
      rowCount: summary.rowCount,
      executionMs: summary.executionMs,
    };
    if (summary.affectedRows != null) result.affectedRows = summary.affectedRows;
    if (summary.notice != null) result.notice = summary.notice;
    return result;
  };

  const completePending = () => {
    if (!pendingSummary || nextSequence !== pendingSummary.chunkCount || !resolveResult) return;
    resolveResult(buildResult(pendingSummary));
    pendingSummary = undefined;
    resolveResult = undefined;
    rejectResult = undefined;
  };

  return {
    append(chunk: DatabaseQueryResultChunk) {
      if (failure) throw failure;
      if (released) throw new Error("Query result stream has been released");
      if (chunk.queryId !== queryId) throw new Error("Stale query result chunk");
      if (chunk.sequence !== nextSequence) throw new Error("Out-of-order query result chunk");
      if (rows.length + chunk.rows.length > maxRows || retainedBytes + chunk.estimatedBytes > maxBytes) {
        throw new Error("Query result stream exceeded its memory boundary");
      }
      if (chunk.columns?.length) columns = [...chunk.columns];
      rows.push(...chunk.rows);
      retainedBytes += chunk.estimatedBytes;
      nextSequence += 1;
      completePending();
    },
    snapshot(executionMs = 0): DatabaseQueryResult {
      if (failure) throw failure;
      if (released) throw new Error("Query result stream has been released");
      return {
        columns: [...columns],
        rows: rows.slice(),
        rowCount: rows.length,
        executionMs,
      };
    },
    finish(summary: DatabaseQueryStreamSummary): Promise<DatabaseQueryResult> {
      if (failure) return Promise.reject(failure);
      if (released) return Promise.reject(new Error("Query result stream has been released"));
      if (summary.queryId !== queryId || summary.chunkCount < nextSequence) {
        return Promise.reject(new Error("Query result stream summary does not match received chunks"));
      }
      if (summary.chunkCount === nextSequence) return Promise.resolve(buildResult(summary));
      pendingSummary = summary;
      return new Promise<DatabaseQueryResult>((resolve, reject) => {
        resolveResult = resolve;
        rejectResult = reject;
      });
    },
    fail(reason: Error) {
      if (released || failure) return;
      failure = reason;
      pendingSummary = undefined;
      rejectResult?.(reason);
      resolveResult = undefined;
      rejectResult = undefined;
    },
    release() {
      released = true;
      rejectResult?.(failure ?? new Error("Query result stream has been released"));
      pendingSummary = undefined;
      resolveResult = undefined;
      rejectResult = undefined;
      columns = [];
      rows = [];
      retainedBytes = 0;
    },
    retainedRows() {
      return rows.length;
    },
    /** 已收到的分块数；有输出的流不能再重跑同一语句。 */
    receivedChunks() {
      return nextSequence;
    },
  };
}

export function createQueryResultProgressBatcher(
  stream: ReturnType<typeof createQueryResultStream>,
  onProgress: (result: DatabaseQueryResult) => void,
  scheduler: QueryResultProgressScheduler = scheduleQueryResultProgress,
) {
  let cancelScheduled: (() => void) | undefined;
  let active = true;

  return {
    schedule() {
      if (!active || cancelScheduled) return;
      cancelScheduled = scheduler(() => {
        cancelScheduled = undefined;
        if (active) onProgress(stream.snapshot());
      });
    },
    cancel() {
      active = false;
      cancelScheduled?.();
      cancelScheduled = undefined;
    },
  };
}
