import { afterEach, describe, expect, it, vi } from "vitest";
import { defineComponent } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import SqlSafetyHistoryPanel from "@/components/database/safety/SqlSafetyHistoryPanel.vue";
import type { SqlSafetyRecord } from "@/domain/database/sql-safety";
import type { DatabaseConnection } from "@/domain/database/types";

const record: SqlSafetyRecord = {
  id: "r1", createdAt: "2026-10-31T08:00:00Z", connectionId: "c1", connectionName: "local", dbType: "mysql",
  database: "shop", table: { schema: "", name: "orders" }, statement: "DELETE FROM orders WHERE id = 1", kind: "data",
  source: "query", affected: 1, undoable: true, undoState: "available", primaryKey: ["id"], columns: ["id"], rowFiles: 1,
};
const list = vi.fn(async () => [record]);
vi.mock("@/services/sql-safety-history", () => ({
  listSqlSafetyHistory: () => list(),
  getSqlSafetyHistory: vi.fn(),
}));
vi.mock("@/stores/database-workspace", () => ({
  useDatabaseWorkspaceStore: () => ({ sessionIds: { c1: "s1" } }),
}));

const UndoDialogStub = defineComponent({ name: "SqlSafetyUndoDialog", props: ["modelValue", "record", "sessionId"], emits: ["done"], template: "<div />" });

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
  list.mockClear();
});

describe("SQL safety history panel", () => {
  it("reports a finished undo with its record so the open grid can reload", async () => {
    wrapper = mount(SqlSafetyHistoryPanel, {
      props: { modelValue: true, connection: { id: "c1", name: "local" } as DatabaseConnection },
      attachTo: document.body,
      global: { stubs: { SqlSafetyUndoDialog: UndoDialogStub } },
    });
    await flushPromises();
    const undoButton = [...document.querySelectorAll<HTMLButtonElement>(".db-safety-history button")]
      .find((button) => button.textContent?.trim() === "撤回");
    expect(undoButton).toBeTruthy();
    undoButton!.click();
    await flushPromises();
    wrapper.findComponent(UndoDialogStub).vm.$emit("done");
    await flushPromises();
    expect(wrapper.emitted("undone")).toEqual([[record]]);
    expect(list).toHaveBeenCalledTimes(2);
  });
});
