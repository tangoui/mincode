import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import ts from "typescript";

const sourcePath = new URL("../src/domain/workspace/state-scope.ts", import.meta.url);
const source = await readFile(sourcePath, "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText;
const moduleUrl = `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`;
const scope = await import(moduleUrl);

const terminalSourcePath = new URL(
  "../src/domain/workspace/terminal-layout-scope.ts",
  import.meta.url
);
const terminalSource = await readFile(terminalSourcePath, "utf8");
const terminalCompiled = ts.transpileModule(terminalSource, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText;
const terminalModuleUrl = `data:text/javascript;base64,${Buffer.from(terminalCompiled).toString("base64")}`;
const terminalScope = await import(terminalModuleUrl);

const defaults = scope.restoreWorkspaceStatePreferences(undefined);
assert.equal(defaults.isolationEnabled, true);
assert.equal(scope.resolveWorkspaceStateScope(defaults, "workspace:a"), "workspace:a");

const shared = scope.updateWorkspaceStateIsolation(defaults, false, "workspace:a");
assert.equal(shared.isolationEnabled, false);
assert.equal(shared.sharedWorkspaceId, "workspace:a");
assert.equal(scope.resolveWorkspaceStateScope(shared, "workspace:b"), "workspace:a");

const isolatedAgain = scope.updateWorkspaceStateIsolation(shared, true, "workspace:b");
assert.equal(scope.resolveWorkspaceStateScope(isolatedAgain, "workspace:b"), "workspace:b");
assert.equal(isolatedAgain.sharedWorkspaceId, "workspace:a");

const restored = scope.restoreWorkspaceStatePreferences({
  version: 1,
  isolationEnabled: false,
  sharedWorkspaceId: " workspace:shared ",
});
assert.equal(restored.sharedWorkspaceId, "workspace:shared");
assert.equal(scope.resolveWorkspaceStateScope(restored, "workspace:b"), "workspace:shared");

// 工作区缓存上限：0 表示无限缓存，非法值回退默认，超大值钳制到安全上限。
assert.equal(scope.normalizeWorkspaceCacheLimit(undefined), scope.DEFAULT_WORKSPACE_CACHE_LIMIT);
assert.equal(scope.normalizeWorkspaceCacheLimit("abc"), scope.DEFAULT_WORKSPACE_CACHE_LIMIT);
assert.equal(scope.normalizeWorkspaceCacheLimit(4), 4);
assert.equal(scope.normalizeWorkspaceCacheLimit(0), 0);
assert.equal(scope.normalizeWorkspaceCacheLimit(-1), scope.DEFAULT_WORKSPACE_CACHE_LIMIT);
assert.equal(scope.normalizeWorkspaceCacheLimit(201), scope.MAX_WORKSPACE_CACHE_LIMIT);
assert.equal(scope.normalizeWorkspaceCacheLimit(6.4), 6);

const terminalTabs = [
  { id: "terminal-a-1", workspaceId: "workspace:a", view: "terminal" },
  { id: "terminal-a-2", workspaceId: "workspace:a", view: "terminal" },
  { id: "terminal-b-1", workspaceId: "workspace:b", view: "terminal" },
  { id: "terminal-b-2", workspaceId: "workspace:b", view: "terminal" },
];
const isolatedTerminal = terminalScope.resolveTerminalLayoutTarget(
  terminalTabs,
  terminalTabs[2],
  true,
  "workspace:a"
);
assert.deepEqual(isolatedTerminal, { layoutId: "terminal-b-1" });

const sharedTerminal = terminalScope.resolveTerminalLayoutTarget(
  terminalTabs,
  terminalTabs[3],
  false,
  "workspace:a"
);
assert.deepEqual(sharedTerminal, {
  layoutId: "workspace-scope:workspace%3Aa:terminal:1",
  fallbackLayoutId: "terminal-a-2",
});


const routeSourcePath = new URL(
  "../src/domain/workspace/route-state-scope.ts",
  import.meta.url
);
const routeSource = await readFile(routeSourcePath, "utf8");
const routeCompiled = ts.transpileModule(routeSource, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText;
const routeModuleUrl = `data:text/javascript;base64,${Buffer.from(routeCompiled).toString("base64")}`;
const routeScope = await import(routeModuleUrl);

assert.equal(routeScope.resolveWorkspaceRouteInstanceScope("dashboard", "workspace:a"), "shared");
assert.equal(routeScope.resolveWorkspaceRouteInstanceScope("terminal", "workspace:a"), "shared");
// 笔记是全局资源，笔记页实例不按工作区隔离。
assert.equal(routeScope.resolveWorkspaceRouteInstanceScope("notes", "workspace:a"), "shared");
assert.equal(
  routeScope.resolveWorkspaceRouteInstanceScope("projects", "workspace:a"),
  "workspace:a"
);
assert.equal(
  routeScope.resolveWorkspaceRouteInstanceScope("future-feature", "workspace:a"),
  "workspace:a"
);
assert.equal(routeScope.resolveWorkspaceRouteInstanceScope(undefined, "workspace:a"), "shared");

const appSource = await readFile(new URL("../src/App.vue", import.meta.url), "utf8");
assert.match(appSource, /resolveWorkspaceRouteInstanceScope/);
assert.doesNotMatch(appSource, /scopedRouteViews/);
// KeepAlive 缓存上限来自设置，并始终受安全上限约束。
assert.match(appSource, /:max="workspaceCacheLimit \|\| undefined"/);
assert.doesNotMatch(appSource, /Infinity/);

const viewsDirectory = new URL("../src/views/", import.meta.url);
const viewFiles = (await readdir(viewsDirectory)).filter((name) => name.endsWith(".vue"));
const viewSources = await Promise.all(
  viewFiles.map(async (name) => [name, await readFile(new URL(name, viewsDirectory), "utf8")])
);
const directScopeDerivations = viewSources
  .filter(([, viewSource]) => viewSource.includes("scopeIdFor("))
  .map(([name]) => name);
const lowLevelScopeKeys = viewSources
  .filter(
    ([, viewSource]) =>
      viewSource.includes("workspaceScopedStorageKey(") ||
      viewSource.includes("ownsLegacyWorkspaceSnapshot(")
  )
  .map(([name]) => name);
assert.deepEqual(directScopeDerivations, []);
assert.deepEqual(lowLevelScopeKeys, []);

console.log("workspace state scope: 24/24 passed");
