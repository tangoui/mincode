import { beforeEach, expect, test, vi } from "vitest";

const plugins = vi.hoisted(() => ({ installed: new Set<string>(), roots: new Map<string, string>() }));
vi.mock("@/services/plugin-service", () => ({
  previewLocalPlugin: vi.fn(async (root: string) => ({ manifest: { id: plugins.roots.get(root) } })),
  listPlugins: vi.fn(async () => [...plugins.installed].map((id) => ({ id }))),
  installLocalPlugin: vi.fn(async (root: string) => { plugins.installed.add(plugins.roots.get(root)!); }),
  setPluginEnabled: vi.fn(async () => undefined),
  invokePlugin: vi.fn(async (_id: string, _action: string, payload: string) => ({ message: `Hello, ${JSON.parse(payload).name}!` })),
  uninstallPlugin: vi.fn(async (id: string) => { plugins.installed.delete(id); }),
}));
vi.mock("@/services/desktop-bridge", () => ({
  invokeBackend: vi.fn(), listenAppEvent: vi.fn(), nativeErrorMessage: String,
}));

import { runDesktopSmoke } from "@/services/e2e-smoke-runner";
import { installLocalPlugin, invokePlugin } from "@/services/plugin-service";

beforeEach(() => {
  vi.clearAllMocks();
  plugins.installed.clear();
  plugins.roots = new Map([["/node", "example.node"], ["/rust", "example.rust"]]);
});

test("combined Node and Rust smoke scenarios install and invoke their own fixture", async () => {
  const report = await runDesktopSmoke({
    scenarios: ["plugin", "rust-plugin"], nonce: "abc123", timeoutMs: 5000,
    plugin: { packagePath: "/node", pluginId: "example.node" },
    rustPlugin: { packagePath: "/rust", pluginId: "example.rust" },
  });
  expect(report.results.map(({ scenario, status }) => ({ scenario, status }))).toEqual([
    { scenario: "plugin", status: "passed" }, { scenario: "rust-plugin", status: "passed" },
  ]);
  expect(vi.mocked(installLocalPlugin).mock.calls.map(([root]) => root)).toEqual(["/node", "/rust"]);
  expect(vi.mocked(invokePlugin).mock.calls.map(([id]) => id)).toEqual(["example.node", "example.rust"]);
  expect(plugins.installed.size).toBe(0);
});
