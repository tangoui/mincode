import { beforeEach, expect, it, vi } from "vitest";
import { withoutStalePluginFileResults, type CommandPaletteItem } from "@/domain/commands/command-palette";
import { openPluginSearchFileTarget, validatePluginSearchFileTarget } from "@/services/plugin-search-file-targets";

type Scope = { workspaceId: string; root: string; revision: string; ready: boolean };
type Call = { command: string; args: Record<string, unknown> };

const fixtures = vi.hoisted(() => ({
  desktop: true,
  scope: undefined as Scope | undefined,
  calls: [] as Call[],
  respond: undefined as undefined | ((args: Record<string, unknown>) => Promise<unknown>),
}));
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => fixtures.desktop,
  nativeErrorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
  invokeBackend: async (command: string, args: Record<string, unknown> = {}) => {
    fixtures.calls.push({ command, args });
    if (fixtures.respond) return fixtures.respond(args);
    const { path, line, column } = args;
    return { type: "file", path, ...(line ? { line } : {}), ...(column ? { column } : {}) };
  },
}));
vi.mock("@/services/plugin-workspace-scope", () => ({
  currentPluginWorkspaceScope: () => fixtures.scope,
  onPluginWorkspaceScopeChange: () => () => undefined,
}));

const service = { openPluginSearchFileTarget, validatePluginSearchFileTarget };

const scopeA = { workspaceId: "ws-a", root: "/projects/a", revision: "4" };
const scopeB = { workspaceId: "ws-b", root: "/projects/b", revision: "5" };
const providerRuntime = { instance: "2", generation: "1" };
const activation = (overrides: Record<string, unknown> = {}) => ({
  pluginId: "acme.search",
  providerId: "code",
  fileScope: scopeA,
  providerRuntime,
  target: { type: "file" as const, path: "src/main.go", line: 12, column: 3 },
  ...overrides,
});
const validateCalls = () => fixtures.calls.filter((call) => call.command === "plugin_host_validate_search_file_target");

beforeEach(() => {
  fixtures.desktop = true;
  fixtures.scope = { ...scopeA, ready: true };
  fixtures.calls = [];
  fixtures.respond = undefined;
});

it("drops stale file results before and after native validation", async () => {
  const open = vi.fn(async () => undefined);

  // Current: a result from scope A opens in A's root with the host's canonical target.
  await expect(service.openPluginSearchFileTarget(activation(), open)).resolves.toEqual({ status: "opened" });
  expect(validateCalls()).toHaveLength(1);
  expect(validateCalls()[0].args).toEqual({
    pluginId: "acme.search",
    providerId: "code",
    fileScope: scopeA,
    providerRuntime,
    path: "src/main.go",
    line: 12,
    column: 3,
  });
  expect(open).toHaveBeenCalledWith({ root: "/projects/a", path: "src/main.go", line: 12, column: 3 });

  // An A result after switching to B never reaches the host or the opener.
  open.mockClear();
  fixtures.calls = [];
  fixtures.scope = { ...scopeB, ready: true };
  await expect(service.openPluginSearchFileTarget(activation(), open)).resolves.toEqual({ status: "stale" });
  expect(validateCalls()).toHaveLength(0);
  expect(open).not.toHaveBeenCalled();
  // Switching back to the same root still uses a new revision.
  fixtures.scope = { ...scopeA, revision: "6", ready: true };
  await expect(service.openPluginSearchFileTarget(activation(), open)).resolves.toEqual({ status: "stale" });
  fixtures.scope = { ...scopeA, ready: false };
  await expect(service.openPluginSearchFileTarget(activation(), open)).resolves.toEqual({ status: "stale" });
  expect(open).not.toHaveBeenCalled();

  // The root changes while the host validates: the accepted answer is not opened.
  fixtures.scope = { ...scopeA, ready: true };
  let release: (() => void) | undefined;
  fixtures.respond = (args) =>
    new Promise((resolve) => {
      release = () => resolve({ type: "file", path: args.path, line: args.line, column: args.column });
    });
  const pending = service.openPluginSearchFileTarget(activation(), open);
  await vi.waitFor(() => expect(release).toBeTypeOf("function"));
  fixtures.scope = { ...scopeA, root: "/projects/a-moved", revision: "7", ready: true };
  release?.();
  await expect(pending).resolves.toEqual({ status: "stale" });
  expect(open).not.toHaveBeenCalled();

  // A host "unavailable" (scope or runtime changed) is reported as stale, not as an open.
  fixtures.scope = { ...scopeA, ready: true };
  fixtures.respond = async () => {
    throw new Error("unavailable: the workspace changed since this search; search again");
  };
  await expect(service.openPluginSearchFileTarget(activation(), open)).resolves.toEqual({ status: "stale" });
  fixtures.respond = async () => {
    throw new Error('not_found: file "src/main.go" does not exist');
  };
  await expect(service.openPluginSearchFileTarget(activation(), open)).resolves.toEqual({
    status: "failed",
    error: { code: "not_found", detail: 'file "src/main.go" does not exist' },
  });
  // A malformed host answer is never opened.
  fixtures.respond = async () => ({ type: "file", path: "../escape" });
  await expect(service.openPluginSearchFileTarget(activation(), open)).resolves.toMatchObject({ status: "failed" });
  expect(open).not.toHaveBeenCalled();
});

it("clears only stale file results from the palette list", () => {
  const base = { kind: "plugin-result" as const, title: "t" };
  const items: CommandPaletteItem[] = [
    { ...base, id: "view", action: { type: "plugin-open-view", pluginId: "p", view: "main", providerId: "code" } },
    { ...base, id: "command", action: { type: "plugin-run-command", pluginId: "p", command: "lookup" } },
    { ...base, id: "url", action: { type: "open-url", url: "https://example.com/" } },
    {
      ...base,
      id: "file",
      action: {
        type: "plugin-open-file",
        pluginId: "p",
        providerId: "code",
        fileScope: scopeA,
        providerRuntime,
        target: { type: "file", path: "src/a.go" },
      },
    },
  ];
  expect(withoutStalePluginFileResults(items, { ...scopeA, ready: true }).map((item) => item.id)).toEqual([
    "view",
    "command",
    "url",
    "file",
  ]);
  for (const current of [undefined, { ...scopeB, ready: true }, { ...scopeA, revision: "9", ready: true }, { ...scopeA, ready: false }]) {
    expect(withoutStalePluginFileResults(items, current).map((item) => item.id)).toEqual(["view", "command", "url"]);
  }
});

it("browser unavailable never pretends to open", async () => {
  fixtures.desktop = false;
  const open = vi.fn(async () => undefined);
  await expect(service.openPluginSearchFileTarget(activation(), open)).resolves.toEqual({ status: "desktop-required" });
  await expect(service.validatePluginSearchFileTarget(activation())).rejects.toMatchObject({ code: "unavailable" });
  expect(fixtures.calls).toHaveLength(0);
  expect(open).not.toHaveBeenCalled();
});
