import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { usePluginAiCatalogsStore } from "@/stores/plugin-ai-catalogs";
import { resolvePluginRegistryEntry, searchPluginAiCatalogs } from "@/services/plugin-ai-catalog-service";
import { invokeBackend } from "@/services/desktop-bridge";

vi.mock("@/services/desktop-bridge", () => ({ invokeBackend: vi.fn(), isDesktopRuntime: () => true, nativeErrorMessage: (error: unknown) => String(error) }));
const catalog = { pluginId: "demo.catalog", pluginName: "Demo", id: "team", kind: "mcp" as const, title: "Team" };
function result(name = "Acme") { return { kind: "mcp", items: [{ id: "acme", name }], dropped: 0, truncated: false }; }
function pending<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(yes => { resolve = yes; });
  return { promise, resolve };
}
beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(invokeBackend).mockReset();
  usePluginAiCatalogsStore().setContributions([catalog], []);
});
describe("plugin AI catalogs through the native command boundary", () => {
  it("merges successful catalogs and ignores a failing catalog without fetching install content", async () => {
    const store = usePluginAiCatalogsStore();
    store.setContributions([catalog, { ...catalog, id: "broken" }], []);
    vi.mocked(invokeBackend).mockImplementation(async (_command, args) => {
      if ((args as { catalogId: string }).catalogId === "broken") throw new Error("plugin failed");
      return result() as never;
    });
    const entries = await searchPluginAiCatalogs("mcp", "acme", "en");
    expect(entries).toEqual([{
      id: "plugin:demo.catalog/team/acme", name: "Acme", description: "", source: "plugin",
      installRef: "plugin:demo.catalog/team/acme",
      plugin: { pluginId: "demo.catalog", pluginName: "Demo", catalogId: "team", itemId: "acme", kind: "mcp", fingerprint: "" },
    }]);
    expect(invokeBackend).toHaveBeenCalledTimes(2);
    expect(vi.mocked(invokeBackend).mock.calls.every(([command]) => command === "plugin_host_ai_catalog_search")).toBe(true);
  });
  it("ignores a response from a disabled or replaced catalog", async () => {
    const response = pending<ReturnType<typeof result>>();
    vi.mocked(invokeBackend).mockReturnValueOnce(response.promise as never);
    const search = searchPluginAiCatalogs("mcp", "acme", "en");
    usePluginAiCatalogsStore().clearContributions();
    response.resolve(result());
    expect(await search).toEqual([]);
  });
  it("rejects install content if the catalog changed kind before Add", async () => {
    vi.mocked(invokeBackend).mockResolvedValueOnce(result() as never);
    const [entry] = await searchPluginAiCatalogs("mcp", "acme", "en");
    usePluginAiCatalogsStore().setContributions([{ ...catalog, kind: "skill" }], []);
    await expect(resolvePluginRegistryEntry(entry!, "en")).rejects.toThrow("unavailable");
    expect(invokeBackend).toHaveBeenCalledTimes(1);
  });
  it("fetches install content on Add and rejects a mismatching host kind", async () => {
    vi.mocked(invokeBackend).mockResolvedValueOnce(result() as never);
    const [entry] = await searchPluginAiCatalogs("mcp", "acme", "en");
    vi.mocked(invokeBackend).mockResolvedValueOnce({ kind: "skill", content: "body" } as never);
    await expect(resolvePluginRegistryEntry(entry!, "en")).rejects.toThrow();
  });
});
