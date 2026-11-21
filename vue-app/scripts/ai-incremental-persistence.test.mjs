import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const [store, persistence] = await Promise.all([
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/services/ai-conversation-persistence.ts", import.meta.url), "utf8"),
]);

test("工作区只加载概览，会话详情按需加载并复用在途请求", () => {
  assert.match(persistence, /"ai_store_load_workspace"/);
  assert.doesNotMatch(persistence, /"ai_store_load"/);
  assert.match(persistence, /"ai_store_load_conversation"/);
  assert.match(store, /detailsLoaded\?: boolean/);
  assert.match(store, /conversation\.detailsLoaded === false/);
  assert.match(store, /conversationDetailLoads\.get\(key\)/);
  assert.match(store, /load = aiConversationPersistence\.loadDetail[\s\S]*?const loaded = await load/);
});

test("自动持久化只消费 dirty 工作区和 dirty 会话", () => {
  assert.match(store, /dirtyConversations = new Map/);
  assert.match(store, /dirtyWorkspaceMeta = new Set/);
  assert.match(store, /const scopeIds = new Set\(\[\.\.\.dirtyWorkspaceMeta, \.\.\.dirtyConversations\.keys\(\)\]\)/);
  assert.match(store, /aiConversationPersistence\.saveDetail/);
  assert.match(store, /aiConversationPersistence\.saveMeta/);
  assert.doesNotMatch(store, /const inactiveSaves =/);
  assert.doesNotMatch(store, /aiConversationPersistence\.save\(targetScopeId, conversationCollection\(\)\)/);
});

test("元数据变化不携带历史详情，内容变化才替换条目", () => {
  assert.match(store, /includeDetails = true/);
  assert.match(store, /messages: \[\],[\s\S]*?agentHistory: \[\],[\s\S]*?agentEvents: \[\]/);
  assert.match(store, /const replaceItems = kind === "details" && conversation\.detailsLoaded !== false/);
  assert.match(persistence, /"ai_store_save_conversation"/);
  assert.match(persistence, /replaceItems/);
});

test("dirty 或运行中的非活动工作区不会被缓存淘汰", () => {
  assert.match(store, /dirty: dirtyWorkspaceMeta\.has\(scopeId\) \|\| Boolean\(dirtyConversations\.get\(scopeId\)\?\.size\)/);
  assert.match(store, /!entry\.running && !entry\.dirty/);
});

test("会话内容变化会调度保存，运行中最多每 2 秒保存一次", () => {
  const markDirty = store.match(/function markConversationDirty\([\s\S]*?\n  }\n/)?.[0] ?? "";
  assert.match(markDirty, /if \(autoPersistencePauseDepth > 0\) autoPersistenceDirty = true;\s*else schedulePersistence\(\);/);
  const schedule = store.match(/function schedulePersistence\(\) \{[\s\S]*?\n  }\n/)?.[0] ?? "";
  assert.match(schedule, /if \(persistTimer && hasRunningConversation\) return;/);
  assert.match(schedule, /hasRunningConversation \? 2_000/);
});

test("切走的会话释放详情，最近查看的与运行中/未保存/有队列的会话保留", () => {
  assert.match(store, /const RETAINED_LOADED_CONVERSATIONS = 3;/);
  assert.match(store, /restoreSnapshot\(target, false, false\);\s*setConversationUnread\(id, false\);\s*noteConversationViewed\(id\);/);
  const release = store.match(/function releaseUnviewedConversationDetails\(\) \{[\s\S]*?\n  }\n/)?.[0] ?? "";
  for (const guard of [
    /conversation\.running/,
    /conversation\.mode === "agent"/,
    /conversation\.messageQueue\?\.length/,
    /conversation\.pendingPermission/,
    /dirty\?\.has\(conversation\.id\)/,
  ]) assert.match(release, guard);
  assert.match(release, /conversation\.messages = \[\];[\s\S]*?conversation\.detailsLoaded = false;/);
});

test("未加载详情的会话在发送、云端读取前先加载；清空会话会写入完整状态", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  const sidebar = await readFile(new URL("../src/components/ai/AiConversationSidebar.vue", import.meta.url), "utf8");
  const ensure = store.match(/async function ensureConversationDetails\(id: string\) \{[\s\S]*?\n  }\n/)?.[0] ?? "";
  assert.match(ensure, /await loadConversationDetail\(scopeId, id\)/);
  assert.match(ensure, /mergeLoadedConversationDetails\(current, loaded\);/);
  assert.match(store, /function mergeLoadedConversationDetails\([\s\S]*?current\.messages = mergeById\(loaded\.messages, current\.messages\);[\s\S]*?current\.detailsLoaded = true;/);
  assert.equal((drawer.match(/if \(!\(await ensureSendableConversation\(conversationId, request\)\)\) return;/g) ?? []).length, 2);
  assert.match(drawer, /if \(selected\) await assistant\.ensureConversationDetails\(selected\.id\);/);
  assert.match(drawer, /if \(!\(await assistant\.ensureConversationDetails\(conversation\.id\)\)\) \{\s*claimedCloudPayloads\.delete\(event\.requestId\);\s*continue;/);
  assert.match(store, /function clearConversationMessages\(id: string\) \{[\s\S]*?conversation\.detailsLoaded = true;\s*conversation\.messages = \[\];/);
  assert.match(sidebar, /conversation\.detailsLoaded !== false && !conversation\.messages\.length/);
});
