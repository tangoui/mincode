import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { defineComponent, h, KeepAlive, ref, type Ref } from "vue";
import type { PluginDecorationProvider } from "@/domain/plugins/file-decorations";
import { usePluginDataProvidersStore } from "@/stores/plugin-data-providers";
import { useLocalProjectStore } from "@/stores/local-project";

interface Call {
  pluginId: string;
  directory: string;
  names: string[];
  signal: AbortSignal;
  resolve: (value: Map<string, { badge?: string }> | "failed" | "rejected") => void;
}

const host = vi.hoisted(() => ({
  calls: [] as Call[],
  refreshHandlers: [] as Array<(event: unknown) => void>,
}));

vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/desktop-bridge")>()),
  isDesktopRuntime: () => true,
  listenAppEvent: (name: string, handler: (event: unknown) => void) => {
    if (name !== "plugin:decorations-refresh") return () => undefined;
    host.refreshHandlers.push(handler);
    return () => {
      host.refreshHandlers = host.refreshHandlers.filter((item) => item !== handler);
    };
  },
}));

function recordRequest(provider: PluginDecorationProvider, _root: string, directory: string, names: string[], signal: AbortSignal) {
  return new Promise((resolve: Call["resolve"]) => host.calls.push({ pluginId: provider.pluginId, directory, names, signal, resolve }));
}
vi.mock("@/services/plugin-file-decorations", () => ({ requestFileDecorations: recordRequest }));

const { usePluginFileDecorations } = await import("@/composables/use-plugin-file-decorations");

type Api = ReturnType<typeof usePluginFileDecorations>;

const lint: PluginDecorationProvider = { pluginId: "lint.plugin", pluginName: "Lint", id: "lint", title: "Lint", activeInWorkspace: true };

function harness(use: typeof usePluginFileDecorations = usePluginFileDecorations) {
  const apis: Record<string, Api> = {};
  const page = (name: string) =>
    defineComponent({
      name,
      setup() {
        apis[name] = use({ renamingPath: ref("") });
        return () => h("div", name);
      },
    });
  const pages = { A: page("A"), B: page("B") };
  const current: Ref<"A" | "B"> = ref("A");
  const Root = defineComponent({
    setup() {
      return () => h(KeepAlive, null, [h(pages[current.value])]);
    },
  });
  return { apis, current, Root };
}

const node = (path: string) => ({ id: `/p:${path}`, projectRoot: "/p", path });

let wrapper: VueWrapper | undefined;
beforeEach(() => {
  vi.useFakeTimers();
  host.calls = [];
  host.refreshHandlers = [];
  const project = useLocalProjectStore();
  project.projects = [{ root: "/p", name: "p" }] as typeof project.projects;
  usePluginDataProvidersStore().setDecorationProviders([lint]);
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
});

async function tick(ms = 150) {
  await flushPromises();
  await vi.advanceTimersByTimeAsync(ms);
  await flushPromises();
}

describe("usePluginFileDecorations", () => {
  it("a re-expanded folder is decorated again after collapse", async () => {
    const { apis, Root } = harness();
    wrapper = mount(Root);
    apis.A.directoryLoaded("/p", "src", ["a.ts"]);
    await tick();
    host.calls[0].resolve(new Map([["a.ts", { badge: "E" }]]));
    await flushPromises();
    expect(apis.A.decorationFor(node("src/a.ts"))?.badge).toBe("E");
    apis.A.directoryCollapsed("/p", "src");
    // The view calls directoryLoaded again from @node-expand (load runs only once).
    apis.A.directoryLoaded("/p", "src", ["a.ts"]);
    await tick();
    expect(host.calls).toHaveLength(2);
    host.calls[1].resolve(new Map([["a.ts", { badge: "W" }]]));
    await flushPromises();
    expect(apis.A.decorationFor(node("src/a.ts"))?.badge).toBe("W");
  });

  it("a hidden kept-alive page cancels its requests and asks nothing until shown", async () => {
    const { apis, current, Root } = harness();
    wrapper = mount(Root);
    apis.A.directoryLoaded("/p", "src", ["a.ts"]);
    apis.A.directoryLoaded("/p", "docs", ["b.md"]);
    await tick();
    expect(host.calls).toHaveLength(1);
    current.value = "B";
    await flushPromises();
    expect(host.calls[0].signal.aborted).toBe(true);
    // Hidden: refresh hints, new folders and provider changes request nothing.
    host.refreshHandlers.forEach((handler) => handler({ pluginId: "lint.plugin" }));
    apis.A.directoryLoaded("/p", "lib", ["c.ts"]);
    usePluginDataProvidersStore().setDecorationProviders([{ ...lint, pluginName: "Lint 2" }]);
    await tick(1000);
    expect(host.calls.map((call) => call.directory)).toEqual(["src"]);
    // Shown again: the open folders are asked again.
    current.value = "A";
    await tick();
    expect(host.calls.length).toBe(2);
    expect(host.calls[1].signal.aborted).toBe(false);
  });

  it("retries the request module after a failed load", async () => {
    // A fresh module graph, so the lazy request module has not been loaded yet.
    vi.resetModules();
    vi.doMock("@/services/plugin-file-decorations", () => {
      throw new Error("chunk load failed");
    });
    const fresh = await import("@/composables/use-plugin-file-decorations");
    const { apis, Root } = harness(fresh.usePluginFileDecorations);
    wrapper = mount(Root);
    apis.A.directoryLoaded("/p", "src", ["a.ts"]);
    await tick();
    expect(host.calls).toHaveLength(0);
    vi.doMock("@/services/plugin-file-decorations", () => ({ requestFileDecorations: recordRequest }));
    apis.A.directoryLoaded("/p", "src", ["a.ts"]);
    // The module loads asynchronously before the debounce timer starts.
    await vi.waitFor(() => expect(vi.getTimerCount()).toBeGreaterThan(0), { timeout: 2000 });
    await tick();
    expect(host.calls).toHaveLength(1);
  });

  it("refresh keeps the most recently expanded folders beyond the round cap", async () => {
    const { apis, Root } = harness();
    wrapper = mount(Root);
    const dirs = Array.from({ length: 40 }, (_, i) => `d${String(i).padStart(2, "0")}`);
    // Expanded newest-first relative to tree order: d39 first, d00 last.
    for (const directory of [...dirs].reverse()) apis.A.directoryLoaded("/p", directory, ["a.ts"]);
    await tick();
    while (host.calls.some((call) => !call.signal.aborted && !(call as { done?: boolean }).done)) {
      const call = host.calls.find((item) => !(item as { done?: boolean }).done)!;
      (call as { done?: boolean }).done = true;
      call.resolve(new Map());
      await flushPromises();
    }
    const before = host.calls.length;
    apis.A.refreshExpanded(dirs.map((directory) => ({ projectRoot: "/p", directory, names: ["a.ts"] })));
    await tick();
    for (let i = 0; i < 40; i += 1) {
      const call = host.calls.slice(before).find((item) => !(item as { done?: boolean }).done);
      if (!call) break;
      (call as { done?: boolean }).done = true;
      call.resolve(new Map());
      await flushPromises();
    }
    const refreshed = host.calls.slice(before).map((call) => call.directory).sort();
    expect(refreshed).toEqual(dirs.slice(0, 32));
  });
});
