import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const appSource = await readFile(new URL("../src/App.vue", import.meta.url), "utf8");
const stateScopeSource = await readFile(
  new URL("../src/domain/workspace/state-scope.ts", import.meta.url),
  "utf8",
);
const assistantStoreSource = await readFile(
  new URL("../src/stores/ai-assistant.ts", import.meta.url),
  "utf8",
);
const dashboardSource = await readFile(
  new URL("../src/views/DashboardView.vue", import.meta.url),
  "utf8",
);

test("defers persisted AI state until the assistant is opened", () => {
  const restoreWorkbench = appSource.match(
    /async function restoreWorkbenchSession\(\)[\s\S]*?\n}\n/,
  )?.[0];
  assert.ok(restoreWorkbench);
  assert.doesNotMatch(restoreWorkbench, /assistant\.hydrate/);
  assert.match(appSource, /async function ensureAssistantLoaded\(\)/);
  assert.match(
    appSource,
    /await Promise\.allSettled\(\[[\s\S]*?aiStore\.hydrate\(scopeId, \{[\s\S]*?workspaceId: workbench\.activeWorkspaceId[\s\S]*?projectRoot:[\s\S]*?\}\),[\s\S]*?pendingEdits\.hydrate\(\)[\s\S]*?sshConnections\.hydrate\(\),[\s\S]*?\]\)/,
  );
  assert.match(appSource, /if \(open && !popupWindow\) void ensureAssistantLoaded\(\)/);
});

test("defers SSH profile and credential hydration until an AI workflow is needed", () => {
  const startupInitialization = appSource.match(
    /void license\.hydrate\(\);[\s\S]*?function syncAiWorkspaceDirectory/,
  )?.[0] ?? "";
  assert.doesNotMatch(startupInitialization, /sshConnections\.hydrate/);
  assert.match(appSource, /sshConnections\.hydrate\(\)/);
  assert.match(dashboardSource, /ssh\.hydrate\(\)/);
});

test("does not mount the temporary dashboard before workbench route restoration", () => {
  assert.match(appSource, /const routeSyncReady = ref\(false\);/);
  assert.match(appSource, /<RouterView v-else-if="routeSyncReady"/);
  assert.match(appSource, /routeSyncReady\.value = true;/);
});

test("preserves prompts created before lazy AI hydration", () => {
  assert.match(assistantStoreSource, /const preHydrateDraft = draft\.value;/);
  assert.match(
    assistantStoreSource,
    /if \(preHydrateDraft\.trim\(\) \|\| preHydrateDraftAttachments\.length\)[\s\S]*?draft\.value = preHydrateDraft;[\s\S]*?draftAttachments\.value = preHydrateDraftAttachments;/,
  );
  assert.match(
    assistantStoreSource,
    /pendingContextAttachments\.value = pendingContextAttachments\.value\.map[\s\S]*?workspaceScopeId: activeWorkspaceScopeId\.value,[\s\S]*?conversationId: activeConversationId\.value,/,
  );
});

test("uses a larger bounded workspace cache by default while keeping unlimited opt-in", () => {
  assert.match(stateScopeSource, /DEFAULT_WORKSPACE_CACHE_LIMIT = 6;/);
  assert.match(stateScopeSource, /MIN_WORKSPACE_CACHE_LIMIT = 0;/);
});
