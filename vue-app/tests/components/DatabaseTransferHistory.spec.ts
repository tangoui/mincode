import { mount, flushPromises } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import DatabaseTransferHistory from "@/components/database/DatabaseTransferHistory.vue";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { useDatabaseTransferTasks } from "@/stores/database-transfer-tasks";
vi.mock("@/services/database-transfer-service", () => ({ listTransferTasks: async () => [], getTransferTask: vi.fn(), onTransferTask: () => () => undefined, cancelTransferTask: vi.fn(), submitPreparedImport: vi.fn() }));
describe("import task history", () => {
 it("shows recovered committed counts and pending admission retry", async () => {
  const scope = useWorkspaceStateScope();const store=useDatabaseTransferTasks();
  store.merge({id:"recovered",kind:"import",workspaceId:scope.scopeId.value,state:"failed",revision:3,createdAt:1,updatedAt:3,result:{records:500,objects:0,files:0,bytes:0,certainty:"uncertain"}});
  store.rememberImport("pending","same-key",scope.scopeId.value);
  const wrapper=mount(DatabaseTransferHistory,{attachTo:document.body});await flushPromises();
  expect(wrapper.text()).toContain("500");expect(wrapper.text()).toContain("uncertain");expect(wrapper.text()).toContain("重查启动结果");wrapper.unmount();
 });
});
