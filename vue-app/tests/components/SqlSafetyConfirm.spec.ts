import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SqlSafetyCheck } from "@/domain/database/sql-safety";
import { confirmSqlSafety } from "@/services/sql-safety-confirm";

const settings = vi.hoisted(() => ({ enabled: true }));
vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: async (command: string) => (command === "db_safety_settings_get" ? { ...settings } : undefined),
  isDesktopRuntime: () => true,
  listenAppEvent: () => () => undefined,
}));

function check(overrides: Partial<SqlSafetyCheck> = {}): SqlSafetyCheck {
  return {
    checkId: "chk-1",
    sessionId: "s1",
    database: "shop",
    statements: [{
      index: 0,
      text: "DELETE FROM orders",
      kind: "data",
      table: { schema: "", name: "orders" },
      estimate: { rows: 5000, exact: false, source: "explain" },
      risk: "high",
      reasons: ["noWhere", "cascade"],
      undoable: false,
    }],
    risk: "high",
    confirmTarget: "orders",
    undoable: false,
    expiresAt: "2026-10-31T00:05:00Z",
    ...overrides,
  };
}

const box = () => document.querySelector<HTMLElement>(".app-sql-safety-box");
const confirmButton = () => box()!.querySelector<HTMLButtonElement>(".el-message-box__btns .el-button--primary")!;
const cancelButton = () => box()!.querySelector<HTMLButtonElement>(".el-message-box__btns .el-button:not(.el-button--primary)")!;
async function type(value: string) {
  const input = box()!.querySelector<HTMLInputElement>("input")!;
  input.value = value;
  input.dispatchEvent(new Event("input"));
  await flushPromises();
}

afterEach(async () => {
  settings.enabled = true;
  document.body.innerHTML = "";
  await flushPromises();
});

describe("SQL safety confirmation", () => {
  it("passes none, and low risk from preconfirmed callers, without a dialog", async () => {
    expect(await confirmSqlSafety(check({ risk: "none", checkId: "" }), "user")).toEqual({});
    expect(await confirmSqlSafety(check({ risk: "low" }), "preconfirmed")).toEqual({});
    expect(box()).toBeNull();
  });

  it("AI callers: low risk relies on the tool approval, high risk always asks for the typed target", async () => {
    expect(await confirmSqlSafety(check({ risk: "low" }), "ai")).toEqual({});
    expect(box()).toBeNull();
    const answer = confirmSqlSafety(check(), "ai");
    await flushPromises();
    expect(box()!.textContent).toContain("请输入表名 orders 以确认");
    cancelButton().click();
    await expect(answer).resolves.toBeUndefined();
  });

  it("the AI row threshold explains a running total, without a single-statement count or an undo claim", async () => {
    const answer = confirmSqlSafety(check({
      checkId: "",
      statements: [{ ...check().statements[0], text: "UPDATE orders", kind: "rows", reasons: ["overThreshold"],
        estimate: { rows: 1001, exact: true, source: "count" } }],
    }), "ai", "aiRowThreshold");
    await flushPromises();
    const text = box()!.textContent ?? "";
    expect(text).toContain("本次对话中 AI 逐行修改的行数将累计达到 1,001 行");
    expect(text).not.toContain("此操作不支持撤回");
    expect(text).not.toContain("已自动留底");
    expect(text.split("1,001 行").length - 1).toBe(1); // only in the notice, no per-statement estimate
    expect(text).toContain("请输入表名 orders 以确认");
    cancelButton().click();
    await expect(answer).resolves.toBeUndefined();
  });

  it("high risk lists reasons and the estimate, and confirms only after the table name is typed", async () => {
    const answer = confirmSqlSafety(check(), "preconfirmed");
    await flushPromises();
    const text = box()!.textContent ?? "";
    expect(text).toContain("撤回只能恢复本表，关联表的改动无法恢复");
    expect(text).toContain("没有 WHERE 条件");
    expect(text).toContain("约 5,000 行");
    expect(text).toContain("此操作不支持撤回");
    expect(text).toContain("请输入表名 orders 以确认");

    await type("orderz");
    confirmButton().click();
    await flushPromises();
    expect(box()!.textContent).toContain("输入的名称与目标不一致");

    await type("`ORDERS`");
    confirmButton().click();
    await expect(answer).resolves.toEqual({ confirmText: "`ORDERS`" });
  });

  it("high risk layout: facts rows, no duplicate unknown-estimate reason, no status icon, typed-target hint", async () => {
    const answer = confirmSqlSafety(check({
      statements: [{ ...check().statements[0], reasons: ["noEstimate", "unparsed"],
        estimate: { rows: 0, exact: false, source: "none" } }],
    }), "user");
    await flushPromises();
    const text = box()!.textContent ?? "";
    expect(text).toContain("影响行数");
    expect(text).toContain("能否撤回");
    expect(text.split("无法估算").length - 1).toBe(1);
    expect(text).toContain("无法分析该语句，不能留底");
    expect(box()!.querySelector(".el-message-box__status")).toBeNull();
    expect(box()!.querySelector(".app-sql-safety__target")!.textContent).toBe("orders");
    expect(box()!.querySelector<HTMLInputElement>("input")!.placeholder).toBe("在此输入 orders");
    cancelButton().click();
    await expect(answer).resolves.toBeUndefined();
  });

  it("shows the CONFIRM literal as the word to type", async () => {
    const answer = confirmSqlSafety(check({ confirmTarget: "CONFIRM", statements: [{ ...check().statements[0], kind: "procedure", table: undefined }] }), "user");
    await flushPromises();
    expect(box()!.textContent).toContain("请输入 CONFIRM 以确认");
    cancelButton().click();
    await expect(answer).resolves.toBeUndefined();
  });

  it("a reconfirm notice opens a dialog even for low risk preconfirmed callers", async () => {
    const answer = confirmSqlSafety(check({ risk: "low", confirmTarget: "", undoable: true }), "preconfirmed", "reconfirm");
    await flushPromises();
    expect(box()!.textContent).toContain("请重新确认");
    expect(box()!.textContent).toContain("已自动留底，可撤回");
    confirmButton().click();
    await expect(answer).resolves.toEqual({});
  });

  it("with the safety net disabled shows the generic confirmation without undo or estimate", async () => {
    settings.enabled = false;
    const low = check({ risk: "low", confirmTarget: "", statements: [{ ...check().statements[0], risk: "low", reasons: [], estimate: { rows: 0, exact: false, source: "none" } }] });
    const answer = confirmSqlSafety(low, "user");
    await flushPromises();
    const text = box()!.textContent ?? "";
    expect(text).toContain("此操作将修改数据库，确定执行吗？");
    expect(text).not.toContain("撤回");
    expect(text).not.toContain("无法估算行数");
    confirmButton().click();
    await expect(answer).resolves.toEqual({});
  });
});
