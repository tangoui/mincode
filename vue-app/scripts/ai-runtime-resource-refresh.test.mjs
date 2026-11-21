import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const drawerSource = await readFile(
  new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
  "utf8",
);

test("a new AI run refreshes native resources before capturing its context", () => {
  assert.match(
    drawerSource,
    /runContext = workspaceContextForConversation\(conversationId\);[\s\S]*?await refreshConversationRuntimeResources\(runContext\.resources, runContext\.projectRoot\);/,
  );
});

test("a missing tool target refreshes runtime resources before failing resolution", () => {
  assert.match(
    drawerSource,
    /resources\.list\(call\.name\)\.length === 0[\s\S]*?await refreshConversationRuntimeResources\(resources, projectRoot, true\);[\s\S]*?resolveResourceFor\(call\.name, args, resources\)/,
  );
});

test("a successful mutation refreshes resources for later tools in the same run", () => {
  assert.match(
    drawerSource,
    /result = await aiCapabilityRegistry\.execute[\s\S]*?if \(capability\.risk !== "read"\) \{[\s\S]*?await refreshConversationRuntimeResources\(resources, projectRoot, true\);/,
  );
});

test("runtime refresh preserves the conversation SFTP transfer resource", () => {
  assert.match(drawerSource, /PROJECT_SCOPED_RESOURCE_KINDS = new Set\(\[[\s\S]*?"sftp-workspace-transfer"/);
  assert.match(
    drawerSource,
    /const projectResources = resources[\s\S]*?PROJECT_SCOPED_RESOURCE_KINDS\.has\(resource\.kind\)[\s\S]*?const runtimeResources = aiResourceRegistry[\s\S]*?!PROJECT_SCOPED_RESOURCE_KINDS\.has\(resource\.kind\)[\s\S]*?resources\.replace\("snapshot", \[\.\.\.projectResources, \.\.\.runtimeResources\]\)/,
  );
});
