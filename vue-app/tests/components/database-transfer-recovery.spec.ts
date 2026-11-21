import { describe, expect, it, vi } from "vitest";
import { listTransferTasks, submitPreparedImport } from "@/services/database-transfer-service";
import { runBackupJobNow } from "@/services/database-backup-service";
const invoke = vi.hoisted(() => vi.fn());
vi.mock("@/services/desktop-bridge", () => ({ invokeBackend: (...args: unknown[]) => invoke(...args), isDesktopRuntime: () => true, listenAppEvent: () => () => undefined }));
const task = { id: "import-1", kind: "import", state: "succeeded", revision: 2, createdAt: 1, updatedAt: 2, result: { records: 3, objects: 0, files: 0, bytes: 0, certainty: "known" } };
describe("transfer recovery", () => {
 it("recovers a lost manual backup start with the same admission key", async () => {
  invoke.mockReset();
  invoke.mockRejectedValueOnce(new Error("lost response")).mockResolvedValueOnce({ id: "backup-task", jobId: "job", trigger: "manual", status: "running", startedAt: 1 });
  expect(await runBackupJobNow("job")).toMatchObject({ id: "backup-task" });
  expect(invoke.mock.calls).toHaveLength(2);
  expect(invoke.mock.calls[0][1].idempotencyKey).toBeTruthy();
  expect(invoke.mock.calls[1][1]).toEqual(invoke.mock.calls[0][1]);
 });
 it("reads later pages instead of losing older tasks", async () => {
  invoke.mockReset(); invoke.mockResolvedValueOnce({ tasks: [], nextCursor: "next" }).mockResolvedValueOnce({ tasks: [task] });
  expect(await listTransferTasks("import", "scope")).toHaveLength(1);
  expect(invoke.mock.calls[1][1]).toMatchObject({ cursor: "next", workspaceId: "scope" });
 });
 it("does not retry a safety-net refusal with the spent token", async () => {
  invoke.mockReset();invoke.mockRejectedValueOnce(new Error('sqlsafety:{"code":"checkInvalid"}')).mockResolvedValueOnce(task);
  await expect(submitPreparedImport("reviewed", "same-key", { checkId: "spent" })).rejects.toThrow(/checkInvalid/);
  expect(invoke.mock.calls).toHaveLength(1);
  expect(invoke.mock.calls[0][1]).toEqual({ planId: "reviewed", idempotencyKey: "same-key", checkId: "spent" });
 });
 it("reuses the prepared idempotency key after a lost response", async () => {
  invoke.mockReset();invoke.mockRejectedValueOnce(new Error("lost response")).mockResolvedValueOnce(task);
  expect(await submitPreparedImport("reviewed", "same-key")).toMatchObject({ id: "import-1" });
  expect(invoke.mock.calls.map(call => call[1])).toEqual([{ planId: "reviewed", idempotencyKey: "same-key" }, { planId: "reviewed", idempotencyKey: "same-key" }]);
 });
});
