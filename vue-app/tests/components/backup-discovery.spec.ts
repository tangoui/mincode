import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createApp, nextTick, ref } from "vue";
import { useBackupDiscovery } from "@/composables/use-backup-discovery";
import { discoverBackupTargets, type BackupDiscovery } from "@/services/database-backup-service";

vi.mock("@/services/database-backup-service", () => ({ discoverBackupTargets: vi.fn() }));
vi.mock("@/services/desktop-bridge", () => ({ nativeErrorMessage: (error: unknown) => String(error) }));
const cleanups: Array<() => void> = [];
afterEach(() => cleanups.splice(0).forEach(fn => fn()));
beforeEach(() => vi.mocked(discoverBackupTargets).mockReset());
function response(names: string[], table = ""): BackupDiscovery {
  return {
    capability: { engine: "sqlite", supported: true, format: "archive", fidelity: "logical-data", limitations: [], scope: "table", sqlFormat: true },
    schema: { databases: names.map(name => ({ name, tables: table ? [{ name: table, kind: "table" }] : [] })) },
  };
}
function pending<T>() {
  let resolve!: (value: T) => void, reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
async function settle() { for (let i = 0; i < 6; i++) await nextTick(); }
function setup(databaseName = "") {
  const connection = ref("one"), database = ref(databaseName), active = ref(true);
  let discovery!: ReturnType<typeof useBackupDiscovery>;
  const app = createApp({ setup() {
    discovery = useBackupDiscovery(connection, database, active);
    return () => null;
  } });
  app.mount(document.createElement("div"));
  let mounted = true;
  const unmount = () => { if (mounted) { mounted = false; app.unmount(); } };
  cleanups.push(unmount);
  return { connection, database, active, discovery, unmount };
}
describe("independent backup discovery", () => {
  it("retains all database choices when loading a saved target's objects", async () => {
    vi.mocked(discoverBackupTargets).mockResolvedValueOnce(response(["first", "second"]))
      .mockResolvedValueOnce(response(["second"], "documents"));
    const s = setup("second"); await settle();
    expect(discoverBackupTargets).toHaveBeenNthCalledWith(1, "one", "", expect.any(AbortSignal));
    expect(discoverBackupTargets).toHaveBeenNthCalledWith(2, "one", "second", expect.any(AbortSignal));
    expect(s.discovery.result.value?.schema.databases).toEqual([
      { name: "first", tables: [] }, { name: "second", tables: [{ name: "documents", kind: "table" }] },
    ]);
    expect(s.discovery.loading.value).toBe(false);
  });
  it("aborts old connections and ignores results even if cancellation is ignored", async () => {
    const old = pending<BackupDiscovery>();
    vi.mocked(discoverBackupTargets).mockReturnValueOnce(old.promise).mockResolvedValueOnce(response(["new"]));
    const s = setup();
    const signal = vi.mocked(discoverBackupTargets).mock.calls[0][2]!;
    s.connection.value = "two"; expect(signal.aborted).toBe(true);
    await settle(); old.resolve(response(["stale"])); await settle();
    expect(s.discovery.result.value?.schema.databases[0].name).toBe("new");
    expect(s.discovery.error.value).toBe("");
  });
  it("ignores late errors after closing and cancels object loading on unmount", async () => {
    const root = pending<BackupDiscovery>();
    vi.mocked(discoverBackupTargets).mockReturnValueOnce(root.promise);
    const s = setup(); s.active.value = false;
    root.reject(new Error("stale")); await settle();
    expect(s.discovery.error.value).toBe(""); expect(s.discovery.result.value).toBeUndefined();
    const objects = pending<BackupDiscovery>();
    vi.mocked(discoverBackupTargets).mockResolvedValueOnce(response(["db"]))
      .mockReturnValueOnce(objects.promise);
    s.database.value = "db"; s.active.value = true; await settle();
    const signal = vi.mocked(discoverBackupTargets).mock.calls.at(-1)![2]!;
    s.unmount(); expect(signal.aborted).toBe(true);
    objects.resolve(response(["stale"])); await settle();
    expect(s.discovery.result.value?.schema.databases[0].tables).toEqual([]);
  });
  it("surfaces a failed request and retries without changing the selected target", async () => {
    vi.mocked(discoverBackupTargets).mockRejectedValueOnce(new Error("permission denied"));
    const s = setup("saved"); await settle();
    expect(s.discovery.error.value).toContain("permission denied");
    vi.mocked(discoverBackupTargets).mockResolvedValueOnce(response(["saved", "other"]))
      .mockResolvedValueOnce(response(["saved"], "table"));
    await s.discovery.refresh();
    expect(s.database.value).toBe("saved"); expect(s.discovery.error.value).toBe("");
    expect(s.discovery.result.value?.schema.databases).toHaveLength(2);
  });
});
