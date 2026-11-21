import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { reactive } from "vue";
import { installPluginHostEventBridge } from "@/services/plugin-host-events";
import { currentPluginWorkspaceScope, installPluginWorkspaceScopeBridge, onPluginWorkspaceScopeChange } from "@/services/plugin-workspace-scope";

const fixtures = vi.hoisted(() => ({
 project: {} as Record<string, unknown>, workbench: {} as Record<string, unknown>, preferences: {} as Record<string, unknown>,
 calls: [] as { command: string; args: Record<string, string> }[],
 begins: [] as { workspaceId: string; resolve: (value: unknown) => void }[],
 delay: false, delayCommit: false, failRestore: false, delayRestore: false, restoreResolvers: [] as (() => void)[], commitResolvers: [] as (() => void)[], revision: 0, inFlight: 0, maxInFlight: 0, desktop: true,
}));
vi.mock("@/stores/local-project", () => ({ useLocalProjectStore: () => fixtures.project }));
vi.mock("@/stores/workbench", () => ({ useWorkbenchStore: () => fixtures.workbench }));
vi.mock("@/stores/workspace-state-preferences", () => ({ useWorkspaceStatePreferencesStore: () => fixtures.preferences }));
vi.mock("@/services/desktop-bridge", () => ({
 isDesktopRuntime: () => fixtures.desktop,
 invokeBackend: async (command: string, args: Record<string, string> = {}) => {
  fixtures.calls.push({ command, args });
  if (command.endsWith("_begin")) {
   fixtures.inFlight++; fixtures.maxInFlight = Math.max(fixtures.inFlight, fixtures.maxInFlight);
   const scope = { workspaceId: args.workspaceId, root: "", revision: String(++fixtures.revision), ready: false };
   if (fixtures.delay) await new Promise((resolve) => fixtures.begins.push({ workspaceId: args.workspaceId, resolve }));
   fixtures.inFlight--;
   return scope;
  }
  if (command.endsWith("_commit")) {
   if (fixtures.delayCommit) await new Promise<void>((resolve) => fixtures.commitResolvers.push(resolve));
   return { workspaceId: args.workspaceId, root: args.root, revision: args.expectedRevision, ready: Boolean(args.root) };
  }
  if (command.endsWith("_get")) return { workspaceId: "old", root: "/old", revision: "1", ready: true };
  if (command === "plugin_host_floating_restore") {
   if (fixtures.delayRestore) await new Promise<void>((resolve) => fixtures.restoreResolvers.push(resolve));
   if (fixtures.failRestore) throw new Error("restore failed");
  }
  return undefined;
 },
}));
let disposers: (() => void)[] = [];
const settle = async () => { for (let i = 0; i < 30; i++) await Promise.resolve(); };
const commits = () => fixtures.calls.filter((x) => x.command.endsWith("_commit")).map((x) => x.args);
beforeEach(() => {
 fixtures.calls=[]; fixtures.begins=[]; fixtures.commitResolvers=[]; fixtures.restoreResolvers=[]; fixtures.delayCommit=false; fixtures.failRestore=false; fixtures.delayRestore=false; fixtures.revision=0; fixtures.delay=false; fixtures.inFlight=0; fixtures.maxInFlight=0; fixtures.desktop=true;
 fixtures.project=reactive({ hydrated: true, activeWorkspaceId: "A", projectRoot: "/root-A", roots: { A: "/root-A", B: "/root-B", C: "/root-C" } as Record<string,string>,
  projectRootForWorkspace(scopeId: string): string { return (this.roots as Record<string,string>)[scopeId] ?? ""; }, $onAction: () => () => {} });
 fixtures.workbench=reactive({ activeWorkspaceId: "A", hydrated: true, workspaces: [{ id:"A" },{id:"B"},{id:"C"}], activeWorktreePath: "" });
 fixtures.preferences=reactive({ hydrated:true, isolationEnabled:true, sharedWorkspaceId:"shared", scopeIdFor(id: string): string { return this.isolationEnabled ? id : String(this.sharedWorkspaceId); } });
});
afterEach(async () => { for (const dispose of disposers) dispose(); disposers=[]; fixtures.delay=false; fixtures.delayCommit=false; fixtures.delayRestore=false; for (const resolve of fixtures.restoreResolvers.splice(0)) resolve(); for (const resolve of fixtures.commitResolvers.splice(0)) resolve(); for (const begin of fixtures.begins.splice(0)) begin.resolve(undefined); await settle(); });

it("never commits another local-project scope root", async () => {
 const rootA="/root-A";
 fixtures.workbench.activeWorkspaceId="B"; // mounted project is still A
 disposers.push(installPluginWorkspaceScopeBridge(),installPluginHostEventBridge());
 await settle();
 expect(commits().some((x) => x.workspaceId === "B" && x.root === "/root-B")).toBe(true);
 expect(commits().every((x) => x.workspaceId !== "B" || x.root !== rootA)).toBe(true);
 expect(fixtures.calls.some((x) => x.command === "plugin_host_set_workspace_root")).toBe(false);
});

it("serializes switches and discards delayed roots", async () => {
 fixtures.delay=true;
 const observed: (string | undefined)[]=[];
 disposers.push(onPluginWorkspaceScopeChange((scope) => observed.push(scope?.workspaceId)),installPluginWorkspaceScopeBridge());
 await settle();
 fixtures.workbench.activeWorkspaceId="B";
 fixtures.workbench.activeWorkspaceId="C";
 expect(currentPluginWorkspaceScope()).toBeUndefined();
 fixtures.begins.shift()?.resolve(undefined);
 await settle();
 expect(fixtures.begins.map((x) => x.workspaceId)).toEqual(["C"]);
 expect(commits()).toEqual([]);
 fixtures.begins.shift()?.resolve(undefined);
 await settle();
 expect(fixtures.maxInFlight).toBe(1);
 expect(commits().map((x) => x.workspaceId)).toEqual(["C"]);
 expect(currentPluginWorkspaceScope()?.workspaceId).toBe("C");
 expect(observed).not.toContain("A");
});

it("waits for hydration and begins again for same-workspace root changes and reload", async () => {
 fixtures.project.hydrated=false;
 fixtures.preferences.hydrated=false;
 disposers.push(installPluginWorkspaceScopeBridge());
 await settle();
 expect(commits()).toEqual([]);
 expect(currentPluginWorkspaceScope()).toBeUndefined();
 fixtures.preferences.hydrated=true;
 await settle();
 expect(commits()).toEqual([]);
 fixtures.project.hydrated=true;
 await settle();
 const first=currentPluginWorkspaceScope()?.revision;
 (fixtures.project.roots as Record<string,string>).A="/changed";
 expect(currentPluginWorkspaceScope()).toBeUndefined();
 await settle();
 expect(currentPluginWorkspaceScope()?.root).toBe("/changed");
 expect(currentPluginWorkspaceScope()?.revision).not.toBe(first);
 disposers.pop()?.();
 expect(currentPluginWorkspaceScope()).toBeUndefined();
 disposers.push(installPluginWorkspaceScopeBridge());
 await settle();
 expect(fixtures.calls.at(-2)?.command).toBe("plugin_host_workspace_scope_commit");
 expect(fixtures.calls.filter((x) => x.command.endsWith("_begin")).length).toBeGreaterThanOrEqual(3);
});

it("rejects old async results even when workspaces share an isolated scope id", async () => {
 fixtures.preferences.isolationEnabled=false;
 (fixtures.project.roots as Record<string,string>).shared="/shared";
 fixtures.delay=true;
 disposers.push(installPluginWorkspaceScopeBridge());
 await settle();
 fixtures.workbench.activeWorkspaceId="B";
 fixtures.begins.shift()?.resolve(undefined);
 await settle();
 expect(commits()).toEqual([]);
 fixtures.begins.shift()?.resolve(undefined);
 await settle();
 expect(commits().map((x) => x.workspaceId)).toEqual(["B"]);
});

it("has one owner and discards completions after disposal", async () => {
 fixtures.delay=true;
 const dispose=installPluginWorkspaceScopeBridge();
 disposers.push(dispose,installPluginWorkspaceScopeBridge());
 await settle();
 expect(fixtures.begins).toHaveLength(1);
 dispose();
 fixtures.begins.shift()?.resolve(undefined);
 await settle();
 expect(commits()).toEqual([]);
 expect(currentPluginWorkspaceScope()).toBeUndefined();
});

it("keeps a committed scope when floating restore fails", async () => {
 fixtures.failRestore=true;
 disposers.push(installPluginWorkspaceScopeBridge());
 await settle();
 expect(fixtures.calls.some((x) => x.command === "plugin_host_floating_restore")).toBe(true);
 expect(currentPluginWorkspaceScope()?.workspaceId).toBe("A");
 expect(currentPluginWorkspaceScope()?.ready).toBe(true);
});

it("does not let a pending floating restore delay the next switch", async () => {
 fixtures.delayRestore=true;
 disposers.push(installPluginWorkspaceScopeBridge());
 await settle();
 expect(fixtures.restoreResolvers).toHaveLength(1);
 fixtures.workbench.activeWorkspaceId="B";
 await settle();
 expect(commits().map((x) => x.workspaceId)).toEqual(["A","B"]);
 expect(currentPluginWorkspaceScope()?.workspaceId).toBe("B");
});

it("discards a delayed commit before restoring floating views", async () => {
 fixtures.delayCommit=true;
 disposers.push(installPluginWorkspaceScopeBridge());
 await settle();
 fixtures.workbench.activeWorkspaceId="B";
 fixtures.commitResolvers.shift()?.();
 await settle();
 expect(currentPluginWorkspaceScope()).toBeUndefined();
 expect(fixtures.calls.filter((x) => x.command === "plugin_host_floating_restore")).toHaveLength(0);
 fixtures.commitResolvers.shift()?.();
 await settle();
 expect(currentPluginWorkspaceScope()?.workspaceId).toBe("B");
});

it("serializes disposal and reinstallation with an old native begin still pending", async () => {
 fixtures.delay=true;
 const dispose=installPluginWorkspaceScopeBridge();
 await settle();
 dispose();
 fixtures.workbench.activeWorkspaceId="B";
 disposers.push(installPluginWorkspaceScopeBridge());
 await settle();
 expect(fixtures.begins.map((x) => x.workspaceId)).toEqual(["A"]);
 fixtures.begins.shift()?.resolve(undefined);
 await settle();
 expect(fixtures.begins.map((x) => x.workspaceId)).toEqual(["B"]);
 expect(commits()).toEqual([]);
 fixtures.begins.shift()?.resolve(undefined);
 await settle();
 expect(currentPluginWorkspaceScope()?.workspaceId).toBe("B");
 expect(fixtures.maxInFlight).toBe(1);
});

it("falls back only to the captured workspace's worktree", async () => {
 fixtures.workbench.activeWorkspaceId="B";
 (fixtures.project.roots as Record<string,string>).B="";
 fixtures.workbench.workspaces=[{id:"A",worktree:{worktreePath:"/fallback-A"}},{id:"B",worktree:{worktreePath:"/fallback-B"}}];
 disposers.push(installPluginWorkspaceScopeBridge());
 await settle();
 expect(commits().map((x) => x.root)).toEqual(["/fallback-B"]);
});

it("does not simulate native success in a browser", async () => {
 fixtures.desktop=false;
 disposers.push(installPluginWorkspaceScopeBridge());
 await settle();
 expect(fixtures.calls).toEqual([]);
 expect(currentPluginWorkspaceScope()).toBeUndefined();
});
