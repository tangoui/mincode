import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import SqlSafetyUndoDialog from "@/components/database/safety/SqlSafetyUndoDialog.vue";
import type { SqlSafetyRecord, SqlSafetyUndoPreview } from "@/domain/database/sql-safety";

const preview = vi.fn<(sessionId: string, id: string) => Promise<SqlSafetyUndoPreview>>();
const undo = vi.fn();
const highRiskRows = vi.fn<() => Promise<number | undefined>>();
vi.mock("@/services/sql-safety-history", () => ({
  previewSqlSafetyUndo: (sessionId: string, id: string) => preview(sessionId, id),
  undoSqlSafety: (args: unknown) => undo(args),
  sqlSafetyHighRiskRows: () => highRiskRows(),
}));

const record: SqlSafetyRecord = {
  id: "r1", createdAt: "2026-10-31T08:00:00Z", connectionId: "c1", connectionName: "local", dbType: "mysql",
  database: "shop", table: { schema: "", name: "orders" }, statement: "DELETE FROM orders", kind: "data",
  source: "query", affected: 3, undoable: true, undoState: "available", primaryKey: ["id"], columns: ["id"], rowFiles: 1,
};

function previewOf(overrides: Partial<SqlSafetyUndoPreview>): SqlSafetyUndoPreview {
  return { record, conflicts: [], conflictCount: 0, total: 3, previewToken: "tok-1", ...overrides };
}

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => {
  wrapper?.unmount();
  preview.mockReset();
  undo.mockReset();
  highRiskRows.mockReset();
});

async function open() {
  wrapper = mount(SqlSafetyUndoDialog, { props: { modelValue: true, record, sessionId: "s1" }, attachTo: document.body });
  await flushPromises();
}

function confirmButton() {
  return [...document.querySelectorAll<HTMLButtonElement>(".db-safety-undo-dialog button")]
    .find((button) => button.textContent?.includes("确认撤回"));
}

describe("SQL safety undo dialog", () => {
  it("hides the mode choice without conflicts and undoes with the preview token", async () => {
    highRiskRows.mockResolvedValue(1000);
    preview.mockResolvedValue(previewOf({}));
    undo.mockResolvedValue({ restored: 3, skipped: 0, recordId: "u1", recordIds: ["u1"] });
    await open();
    expect(document.body.textContent).toContain("共 3 行可恢复，没有冲突");
    expect(document.querySelector(".db-safety-undo__mode")).toBeNull();
    confirmButton()?.click();
    await flushPromises();
    expect(undo).toHaveBeenCalledWith({ sessionId: "s1", id: "r1", mode: "skipConflicts", previewToken: "tok-1" });
    expect(wrapper?.emitted("done")).toHaveLength(1);
  });

  it("re-previews on previewStale and never executes again on its own", async () => {
    highRiskRows.mockResolvedValue(1000);
    preview
      .mockResolvedValueOnce(previewOf({ conflicts: [{ primaryKey: { id: 2 }, kind: "changed" }], conflictCount: 1 }))
      .mockResolvedValueOnce(previewOf({ previewToken: "tok-2", conflicts: [{ primaryKey: { id: 2 }, kind: "missing" }], conflictCount: 1 }));
    undo.mockRejectedValueOnce(new Error(`sqlsafety:${JSON.stringify({ code: "previewStale" })}`));
    await open();
    expect(document.querySelector(".db-safety-undo__mode")).not.toBeNull();
    expect(document.body.textContent).toContain("操作后已被改动");
    confirmButton()?.click();
    await flushPromises();
    expect(preview).toHaveBeenCalledTimes(2);
    expect(undo).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("已重新预览");
    expect(document.body.textContent).toContain("行已不存在");
    expect(wrapper?.emitted("done")).toBeUndefined();

    undo.mockResolvedValueOnce({ restored: 2, skipped: 1, recordId: "u1", recordIds: ["u1"] });
    confirmButton()?.click();
    await flushPromises();
    expect(undo).toHaveBeenLastCalledWith(expect.objectContaining({ previewToken: "tok-2" }));
  });

  it("asks for the table name over the threshold and shows tableMissing as restore-structure-first", async () => {
    highRiskRows.mockResolvedValue(2);
    preview.mockResolvedValue(previewOf({}));
    undo.mockRejectedValueOnce(new Error(`sqlsafety:${JSON.stringify({ code: "tableMissing" })}`));
    await open();
    expect(document.body.textContent).toContain("请输入表名 orders 以确认");
    expect(confirmButton()?.disabled).toBe(true);
    const input = document.querySelector<HTMLInputElement>("#db-safety-undo-confirm")!;
    input.value = "Orders";
    input.dispatchEvent(new Event("input"));
    await flushPromises();
    expect(confirmButton()?.disabled).toBe(false);
    confirmButton()?.click();
    await flushPromises();
    expect(undo).toHaveBeenCalledWith(expect.objectContaining({ confirmText: "Orders" }));
    expect(document.body.textContent).toContain("请先恢复表结构");
    expect(confirmButton()?.disabled).toBe(true);
  });

  it("blocks uncertain records with the commit-outcome warning", async () => {
    highRiskRows.mockResolvedValue(1000);
    preview.mockResolvedValue(previewOf({ record: { ...record, undoState: "uncertain" } }));
    await open();
    expect(document.body.textContent).toContain("提交结果未知，无法确定是否已生效");
    expect(confirmButton()?.disabled).toBe(true);
  });

  it("sends skipConflicts by default and overwrite once the user picks it", async () => {
    highRiskRows.mockResolvedValue(1000);
    preview.mockResolvedValue(previewOf({ conflicts: [{ primaryKey: { id: 2 }, kind: "changed" }], conflictCount: 1 }));
    undo.mockRejectedValueOnce(new Error("boom"));
    await open();
    confirmButton()?.click();
    await flushPromises();
    expect(undo).toHaveBeenLastCalledWith(expect.objectContaining({ mode: "skipConflicts" }));

    undo.mockResolvedValueOnce({ restored: 3, skipped: 0, recordId: "u1", recordIds: ["u1"] });
    const overwrite = document.querySelector<HTMLInputElement>(".db-safety-undo__mode input[value='overwrite']")!;
    overwrite.click();
    await flushPromises();
    confirmButton()?.click();
    await flushPromises();
    expect(undo).toHaveBeenLastCalledWith(expect.objectContaining({ mode: "overwrite" }));
  });

  it("resets the mode and typed name after a previewStale refresh", async () => {
    highRiskRows.mockResolvedValue(1); // 2 rows (skip) and 3 rows (overwrite) both need the name
    preview.mockResolvedValue(previewOf({ conflicts: [{ primaryKey: { id: 2 }, kind: "changed" }], conflictCount: 1 }));
    undo.mockRejectedValueOnce(new Error(`sqlsafety:${JSON.stringify({ code: "previewStale" })}`));
    await open();
    document.querySelector<HTMLInputElement>(".db-safety-undo__mode input[value='overwrite']")!.click();
    await flushPromises();
    const input = document.querySelector<HTMLInputElement>("#db-safety-undo-confirm")!;
    input.value = "orders";
    input.dispatchEvent(new Event("input"));
    await flushPromises();
    confirmButton()?.click();
    await flushPromises();
    expect(undo).toHaveBeenCalledWith(expect.objectContaining({ mode: "overwrite", confirmText: "orders" }));
    expect(document.querySelector<HTMLInputElement>("#db-safety-undo-confirm")!.value).toBe("");
    expect(document.querySelector<HTMLInputElement>(".db-safety-undo__mode input[value='skipConflicts']")!.checked).toBe(true);
    expect(confirmButton()?.disabled).toBe(true);
  });

  it("blocks submit until the threshold is known and asks for the name when it cannot be read", async () => {
    let resolveThreshold: (rows: number | undefined) => void = () => undefined;
    highRiskRows.mockReturnValue(new Promise((resolve) => { resolveThreshold = resolve; }));
    preview.mockResolvedValue(previewOf({}));
    await open();
    expect(confirmButton()?.disabled).toBe(true);
    resolveThreshold(undefined);
    await flushPromises();
    expect(document.body.textContent).toContain("请输入表名 orders 以确认");
    expect(confirmButton()?.disabled).toBe(true);
  });
});
