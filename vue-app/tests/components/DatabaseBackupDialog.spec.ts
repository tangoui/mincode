import { afterEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import DatabaseBackupDialog from "@/components/database/DatabaseBackupDialog.vue";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import type { BackupDiscovery } from "@/services/database-backup-service";

const discover = vi.fn<(id: string, db: string) => Promise<BackupDiscovery>>();
vi.mock("@/services/desktop-bridge", async (original) => ({
  ...(await original<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => true,
}));
vi.mock("@/services/database-backup-service", () => ({
  discoverBackupTargets: (id: string, db: string) => discover(id, db),
  listBackupJobs: () => Promise.resolve([]),
  onBackupRunEvent: () => () => undefined,
  saveBackupJob: vi.fn(), deleteBackupJob: vi.fn(), runBackupJobNow: vi.fn(),
  cancelBackupJob: vi.fn(), listBackupRuns: vi.fn(), revealBackupPath: vi.fn(),
}));

let wrapper: ReturnType<typeof mount> | undefined;
afterEach(() => { wrapper?.unmount(); discover.mockReset(); });

function capability(overrides: Partial<BackupDiscovery["capability"]>): BackupDiscovery {
  return {
    capability: { engine: "redis", supported: true, format: "archive", fidelity: "dump", limitations: ["redisScan"], scope: "keys", sqlFormat: false, ...overrides },
    schema: { databases: [{ name: "db0", tables: [] }] },
  };
}

async function openEditor(result: BackupDiscovery) {
  discover.mockResolvedValue(result);
  const store = useDatabaseWorkspaceStore();
  store.connections = [{ id: "c1", name: "Cache", dbType: "redis" } as never];
  wrapper = mount(DatabaseBackupDialog, { props: { modelValue: true, initialConnectionId: "c1" }, attachTo: document.body });
  await flushPromises();
  const create = [...document.querySelectorAll<HTMLButtonElement>(".db-backup-toolbar button")][0];
  create.click();
  await flushPromises();
}

describe("backup dialog capability", () => {
  it("labels scope by engine, translates limitations and disables legacy SQL for non-SQL", async () => {
    await openEditor(capability({}));
    const text = document.body.textContent ?? "";
    expect(text).toContain("Key");
    expect(text).toContain("SCAN 不是全局快照");
    expect(text).not.toContain("redisScan");
    expect(text).toContain("不支持旧版 SQL 脚本格式");
  });
  it("shows a translated reason and blocks saving when the engine cannot be backed up", async () => {
    await openEditor(capability({ supported: false, reason: "pluginNoStream", limitations: [], scope: "table" }));
    expect(document.body.textContent).toContain("当前驱动无法完整导出数据库");
    const save = [...document.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent?.includes("保存"));
    expect(save?.disabled).toBe(true);
  });
});
