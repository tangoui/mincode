export type TransferState = "queued" | "running" | "cancelling" | "succeeded" | "failed" | "cancelled" | "interrupted";
export interface TransferTask {
  id: string;
  kind: "backup" | "restore" | "import";
  state: TransferState;
  revision: number;
  createdAt: number;
  updatedAt: number;
  connectionId?: string;
  workspaceId?: string;
  errorCode?: string;
  result: { records: number; objects: number; files: number; bytes: number; certainty: "none" | "known" | "uncertain" };
}
const states = new Set<TransferState>(["queued", "running", "cancelling", "succeeded", "failed", "cancelled", "interrupted"]);
export function transferTerminal(task: TransferTask): boolean {
  return !["queued", "running", "cancelling"].includes(task.state);
}
export function parseTransferTask(value: unknown): TransferTask {
  const fail = (): never => { throw new Error("Malformed database transfer task"); };
  if (!value || typeof value !== "object" || Array.isArray(value)) return fail();
  const raw = value as Record<string, unknown>;
  if (typeof raw.id !== "string" || !raw.id || !states.has(raw.state as TransferState) ||
      !["backup", "restore", "import"].includes(String(raw.kind)) || !Number.isSafeInteger(raw.revision) || Number(raw.revision) < 1 ||
      typeof raw.createdAt !== "number" || !Number.isFinite(raw.createdAt) || typeof raw.updatedAt !== "number" || !Number.isFinite(raw.updatedAt) ||
      !raw.result || typeof raw.result !== "object" || Array.isArray(raw.result)) return fail();
  const result = raw.result as Record<string, unknown>;
  for (const key of ["records", "objects", "files", "bytes"]) if (typeof result[key] !== "number" || !Number.isSafeInteger(result[key]) || Number(result[key]) < 0) return fail();
  if (!["none", "known", "uncertain"].includes(String(result.certainty))) return fail();
  return {
    id: raw.id, kind: raw.kind as TransferTask["kind"], state: raw.state as TransferState, revision: raw.revision as number,
    createdAt: raw.createdAt, updatedAt: raw.updatedAt,
    ...(typeof raw.connectionId === "string" ? { connectionId: raw.connectionId } : {}),
    ...(typeof raw.workspaceId === "string" ? { workspaceId: raw.workspaceId } : {}),
    ...(typeof raw.errorCode === "string" ? { errorCode: raw.errorCode } : {}),
    result: { records: result.records as number, objects: result.objects as number, files: result.files as number, bytes: result.bytes as number, certainty: result.certainty as TransferTask["result"]["certainty"] },
  };
}
export function mergeTransferTask(previous: TransferTask | undefined, incoming: TransferTask): TransferTask {
  return previous && previous.id === incoming.id && previous.revision >= incoming.revision ? previous : incoming;
}
