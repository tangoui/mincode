import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import DatabaseRestoreDialog from "@/components/database/DatabaseRestoreDialog.vue";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import type { RestorePlan } from "@/services/database-restore-service";

const history = vi.hoisted(() => ({ rows: [] as unknown[] }));
const plan = vi.fn<(id: string, path: string) => Promise<RestorePlan>>();
const start = vi.fn<(id: string, path: string, options: unknown) => Promise<string>>();
vi.mock("@/services/database-restore-service", () => ({
  planRestore: (id: string, path: string) => plan(id, path),
  startRestore: (id: string, path: string, options: unknown) => start(id, path, options),
  cancelRestore: vi.fn(),
  onRestoreEvent: () => () => undefined,
}));
vi.mock("@/services/database-transfer-service", () => ({
  listTransferTasks: async () => history.rows,
  getTransferTask: async () => ({ id: "restore-1", kind: "restore", state: "succeeded", revision: 2, createdAt: 1, updatedAt: 2, result: { records: 1, objects: 1, files: 0, bytes: 0, certainty: "known" } }),
  onTransferTask: () => () => undefined,
  cancelTransferTask: vi.fn(),
}));
vi.mock("element-plus", async (original) => {
  const actual = await original<typeof import("element-plus")>();
  return { ...actual, ElMessageBox: { confirm: () => Promise.resolve() } };
});

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => { history.rows = []; wrapper?.unmount(); plan.mockReset(); start.mockReset(); });

function restorePlan(overrides: Partial<RestorePlan> = {}): RestorePlan {
  return {
    format: "archive", planId: "reviewed-plan", mode: "constrained", supported: true, objects: [{ database: "app", object: "users", records: 0 }],
    conflicts: [], statements: 12, destructive: false, sizeBytes: 2048, warnings: [], ...overrides,
  };
}

async function open(result: RestorePlan) {
  plan.mockResolvedValue(result);
  start.mockResolvedValue("restore-1");
  useDatabaseWorkspaceStore().connections = [{ id: "c1", name: "Staging", dbType: "mysql" } as never];
  wrapper = mount(DatabaseRestoreDialog, {
    props: { modelValue: true, initialPath: "/backups/app.sql.gz", initialConnectionId: "c1" },
    attachTo: document.body,
  });
  await flushPromises();
}

const startButton = () => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("开始恢复"));

describe("restore dialog", () => {
  it("plans the chosen file and target and starts with no extra permissions", async () => {
    await open(restorePlan());
    expect(plan).toHaveBeenCalledWith("c1", "/backups/app.sql.gz");
    expect(document.body.textContent).toContain("2.0 KB");
    startButton()?.click();
    await flushPromises();
    expect(start).toHaveBeenCalledWith("reviewed-plan", expect.any(String), { allowExisting: false, allowDestructive: false });
  });

  it("requires a backend-reviewed plan before admission", async () => {
    await open(restorePlan({ planId: undefined }));
    expect(startButton()?.disabled).toBe(true);
    startButton()?.click(); await flushPromises(); expect(start).not.toHaveBeenCalled();
  });

  it("shows why an unsupported backup cannot be restored and blocks starting", async () => {
    await open(restorePlan({ supported: false, reason: "该归档来自 MongoDB，只能恢复到 MongoDB 连接" }));
    expect(document.body.textContent).toContain("只能恢复到 MongoDB 连接");
    expect(startButton()?.disabled).toBe(true);
  });
  it("reopens a terminal result without locking new admission", async () => {
    history.rows = [{ id: "old", kind: "restore", connectionId: "c1", state: "failed", revision: 2, createdAt: 1, updatedAt: 2, errorCode: "restore_insert_failed", result: { records: 500, objects: 1, files: 0, bytes: 0, certainty: "uncertain" } }];
    await open(restorePlan());
    expect(document.body.textContent).toContain("500");
    expect(document.body.textContent).toContain("uncertain");
    expect(document.body.textContent).toContain("restore_insert_failed");
    expect(startButton()?.disabled).toBe(false);
  });

  it("terminal history does not disable a new restore", async () => {
    history.rows = [{ id: "old", kind: "restore", connectionId: "c1", state: "succeeded", revision: 2, createdAt: 1, updatedAt: 2, result: { records: 1, objects: 1, files: 0, bytes: 0, certainty: "known" } }];
    await open(restorePlan());
    expect(startButton()?.disabled).toBe(false);
  });

});
