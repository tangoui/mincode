/**
 * 浏览器本地模式的 AI 会话存储：九个 `ai_store_*` 命令由 IndexedDB 实现，代替
 * backend/aistore（SQLite）；另有只接受 `delete_ai_scope` 的 `store_apply_batch`，
 * 供永久删除工作区时清除其 AI 历史。输入输出形状与 Go 命令一致（调用方见
 * services/ai-conversation-persistence.ts），错误文案也与 Go 相同。
 *
 * 与桌面端的差异（不影响调用方）：
 * - 会话元数据与条目分两个 store 保存，每次整体写入；附件内联保存，不做去重。
 * - 不发放 `itemRevision`：前端的增量保存器因此每次发送完整会话；若仍收到 `itemPatch`，
 *   按 Go 的「条目已过期」冲突处理（`ai_store_item_patch_conflict`），前端会改发完整会话。
 * - 每个命令在一个 IndexedDB 事务里完成，失败时整体回滚。
 * 数据只保存在当前浏览器（随站点数据清除）。
 */
import { AI_CONVERSATION_ITEM_FIELDS, AI_ITEM_PATCH_CONFLICT } from "@/domain/ai/conversation-item-sync";
import { openIdbDatabase, type IdbDatabase, type IdbSchema, type IdbTransaction } from "./idb";
import { registerBrowserHandler } from "./registry";

type Json = Record<string, unknown>;
type ItemField = (typeof AI_CONVERSATION_ITEM_FIELDS)[number];

interface WorkspaceRecord {
  scopeId: string;
  activeConversationId: string;
  updatedAt: number;
}

interface ConversationRecord {
  scopeId: string;
  conversationId: string;
  position: number;
  /** The conversation without detailsLoaded and its item groups. */
  payload: Json;
  updatedAt: number;
}

type ItemsRecord = { scopeId: string; conversationId: string } & Record<ItemField, unknown[]>;

export const AI_STORE_SCHEMA = {
  workspaces: "scopeId",
  conversations: ["scopeId", "conversationId"],
  items: ["scopeId", "conversationId"],
} as const satisfies IdbSchema;

const ALL_STORES = Object.keys(AI_STORE_SCHEMA);
const DATABASE_NAME = "mincode_browser_ai_v1";
const DATABASE_VERSION = 1;

/** Go search limits (aistore/search.go). */
const DEFAULT_SEARCH_LIMIT = 30;
const MAX_SEARCH_LIMIT = 100;
const MAX_SEARCH_QUERY_RUNES = 200;
const SNIPPET_RADIUS = 60;
const MAX_SEARCH_SCAN_ROWS = 20_000;

let database: IdbDatabase | undefined;
function aiStoreDatabase() {
  database ??= openIdbDatabase(DATABASE_NAME, DATABASE_VERSION, AI_STORE_SCHEMA);
  return database;
}

/** 仅供测试：换成内存实现。 */
export function __setAiStoreDatabaseForTests(replacement: IdbDatabase) {
  database = replacement;
}

let lastTimestamp = 0;
/** Strictly increasing, so "updated last" ordering stays deterministic within a tab. */
function timestamp() {
  lastTimestamp = Math.max(Date.now(), lastTimestamp + 1);
  return lastTimestamp;
}

function isRecord(value: unknown): value is Json {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function text(value: unknown) {
  return typeof value === "string" ? value : "";
}

function blank(value: unknown) {
  return text(value).trim() === "";
}

function ids(value: unknown) {
  return Array.isArray(value) ? value.map(text) : [];
}

/**
 * Copies a caller value the way Go receives it (JSON): drops functions and undefined, and turns
 * reactive proxies into plain data IndexedDB can store.
 */
function jsonCopy(value: unknown): unknown {
  const serialized = JSON.stringify(value);
  return serialized === undefined ? undefined : JSON.parse(serialized);
}

/**
 * Go externalizeAttachments removes `previewUrl` from attachments whose data it moves into blobs;
 * the browser keeps the data inline but drops the preview the same way.
 */
function dropAttachmentPreviews(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(dropAttachmentPreviews);
  if (!isRecord(value)) return value;
  const result: Json = {};
  for (const [key, nested] of Object.entries(value)) result[key] = dropAttachmentPreviews(nested);
  const isAttachment = !blank(result.id) && !blank(result.name) && typeof result.size === "number" && !blank(result.mimeType);
  if (isAttachment && (result.kind === "image" || result.kind === "text") && text(result.data)) delete result.previewUrl;
  return result;
}

const conversationKey = (scopeId: string, conversationId: string) => [scopeId, conversationId];

async function conversationsOf(tx: IdbTransaction, scopeId: string) {
  const records = await tx.getAll<ConversationRecord>("conversations", scopeId);
  return records.sort((a, b) => a.position - b.position || (a.conversationId < b.conversationId ? -1 : 1));
}

function overviewConversation(record: ConversationRecord): Json {
  return { ...record.payload, messages: [], agentHistory: [], agentEvents: [], messageQueue: [], detailsLoaded: false };
}

async function setPositions(tx: IdbTransaction, scopeId: string, conversationIds: string[], skipBlank = false) {
  for (const [position, id] of conversationIds.entries()) {
    if (skipBlank && !id.trim()) continue;
    const record = await tx.get<ConversationRecord>("conversations", conversationKey(scopeId, id));
    if (record) await tx.put("conversations", { ...record, position });
  }
}

/** Go `UPDATE ai_workspaces SET active_conversation_id=?, updated_at=…` (a missing workspace stays missing). */
async function setActiveConversation(tx: IdbTransaction, scopeId: string, activeConversationId: string) {
  const workspace = await tx.get<WorkspaceRecord>("workspaces", scopeId);
  if (workspace) await tx.put("workspaces", { ...workspace, activeConversationId, updatedAt: timestamp() });
}

async function removeConversation(tx: IdbTransaction, scopeId: string, conversationId: string) {
  await tx.delete("conversations", conversationKey(scopeId, conversationId));
  await tx.delete("items", conversationKey(scopeId, conversationId));
}

/** Go saveConversationTx (full save; patches never reach here). Returns the saved conversation id. */
async function writeConversation(tx: IdbTransaction, scopeId: string, conversation: Json, position: number, replaceItems: boolean) {
  const conversationId = text(conversation.id);
  if (!conversationId) throw new Error("AI 会话 id 不能为空");
  const fields: Json = { ...conversation };
  delete fields.detailsLoaded;
  const items = {} as Record<ItemField, unknown[]>;
  for (const field of AI_CONVERSATION_ITEM_FIELDS) {
    // A non-array group is treated as empty, like Go.
    items[field] = Array.isArray(fields[field]) ? fields[field] as unknown[] : [];
    delete fields[field];
  }
  if (!(await tx.get<WorkspaceRecord>("workspaces", scopeId))) {
    await tx.put("workspaces", { scopeId, activeConversationId: "", updatedAt: timestamp() } satisfies WorkspaceRecord);
  }
  await tx.put("conversations", {
    scopeId,
    conversationId,
    position,
    payload: replaceItems ? dropAttachmentPreviews(fields) as Json : fields,
    updatedAt: timestamp(),
  } satisfies ConversationRecord);
  if (replaceItems) {
    await tx.put("items", {
      scopeId,
      conversationId,
      ...(dropAttachmentPreviews(items) as Record<ItemField, unknown[]>),
    } satisfies ItemsRecord);
  }
  return conversationId;
}

/** The `conversation` argument as a JSON object, with Go's validation messages. */
function conversationObject(value: unknown, invalidMessage: string): Json {
  if (value === undefined || value === null) throw new Error(invalidMessage);
  const copy = jsonCopy(value);
  if (!isRecord(copy)) throw new Error("JSON 根节点不是对象");
  return copy;
}

const run = <T>(mode: IDBTransactionMode, operation: (tx: IdbTransaction) => Promise<T>) =>
  aiStoreDatabase().transaction(ALL_STORES, mode, operation);

async function loadWorkspace(args: Json) {
  const scopeId = text(args.scopeId);
  if (!scopeId.trim()) throw new Error("AI 存储 scopeId 不能为空");
  return run("readonly", async (tx) => {
    const workspace = await tx.get<WorkspaceRecord>("workspaces", scopeId);
    if (!workspace) return null;
    const conversations = await conversationsOf(tx, scopeId);
    return { version: 2, activeConversationId: workspace.activeConversationId, conversations: conversations.map(overviewConversation) };
  });
}

async function listWorkspaces() {
  return run("readonly", async (tx) => {
    const workspaces = await tx.getAll<WorkspaceRecord>("workspaces");
    workspaces.sort((a, b) => b.updatedAt - a.updatedAt || (a.scopeId < b.scopeId ? -1 : a.scopeId > b.scopeId ? 1 : 0));
    const overviews: Array<{ scopeId: string; collection: Json }> = [];
    for (const workspace of workspaces) {
      const conversations = await conversationsOf(tx, workspace.scopeId);
      overviews.push({
        scopeId: workspace.scopeId,
        collection: { version: 2, activeConversationId: workspace.activeConversationId, conversations: conversations.map(overviewConversation) },
      });
    }
    return overviews;
  });
}

async function loadConversation(args: Json) {
  const scopeId = text(args.scopeId);
  const conversationId = text(args.conversationId);
  if (!scopeId.trim() || !conversationId.trim()) throw new Error("AI 会话定位参数不能为空");
  return run("readonly", async (tx) => {
    const record = await tx.get<ConversationRecord>("conversations", conversationKey(scopeId, conversationId));
    if (!record) return null;
    const items = await tx.get<ItemsRecord>("items", conversationKey(scopeId, conversationId));
    const conversation: Json = { ...record.payload };
    // Go returns null (a nil slice) for a group without rows.
    for (const field of AI_CONVERSATION_ITEM_FIELDS) conversation[field] = items?.[field]?.length ? items[field] : null;
    conversation.detailsLoaded = true;
    return conversation;
  });
}

async function saveConversation(args: Json) {
  const scopeId = text(args.scopeId);
  if (!scopeId.trim() || args.conversation === undefined || args.conversation === null) throw new Error("AI 会话保存参数无效");
  const replaceItems = args.replaceItems === true;
  if (args.itemPatch !== undefined && args.itemPatch !== null) {
    if (!replaceItems) throw new Error("AI 会话增量保存必须同时保存条目");
    // No revision is ever handed out, so every patch is against an unknown state: ask for a full save.
    throw new Error(AI_ITEM_PATCH_CONFLICT);
  }
  const conversation = conversationObject(args.conversation, "AI 会话保存参数无效");
  const position = typeof args.position === "number" ? args.position : 0;
  await run("readwrite", (tx) => writeConversation(tx, scopeId, conversation, position, replaceItems));
  return {};
}

async function saveWorkspaceMeta(args: Json) {
  if (isRecord(args.archive)) return archiveConversations(args.archive);
  const scopeId = text(args.scopeId);
  if (!scopeId.trim()) throw new Error("AI 存储 scopeId 不能为空");
  const activeConversationId = text(args.activeConversationId);
  await run("readwrite", async (tx) => {
    const workspace = await tx.get<WorkspaceRecord>("workspaces", scopeId);
    await tx.put("workspaces", { ...workspace, scopeId, activeConversationId, updatedAt: timestamp() } satisfies WorkspaceRecord);
    await setPositions(tx, scopeId, ids(args.conversationIds), true);
  });
  return null;
}

async function deleteConversation(args: Json) {
  const scopeId = text(args.scopeId);
  const conversationId = text(args.conversationId);
  if (!scopeId.trim() || !conversationId.trim()) throw new Error("AI 会话定位参数不能为空");
  await run("readwrite", async (tx) => {
    await removeConversation(tx, scopeId, conversationId);
    await removeRelatedConversations(tx, scopeId, args.relatedConversations);
    await setActiveConversation(tx, scopeId, text(args.activeConversationId));
    await setPositions(tx, scopeId, ids(args.conversationIds));
  });
  return null;
}

function locations(value: unknown) {
  if (!Array.isArray(value)) return [];
  return value.filter(isRecord).map((location) => ({
    scopeId: text(location.scopeId),
    conversationId: text(location.conversationId),
  }));
}

async function removeRelatedConversations(tx: IdbTransaction, primaryScope: string, value: unknown) {
  const affectedScopes = new Set<string>();
  for (const { scopeId, conversationId } of locations(value)) {
    await removeConversation(tx, scopeId, conversationId);
    if (scopeId !== primaryScope) affectedScopes.add(scopeId);
  }
  for (const scopeId of affectedScopes) {
    const workspace = await tx.get<WorkspaceRecord>("workspaces", scopeId);
    if (!workspace) continue;
    if (await tx.get("conversations", conversationKey(scopeId, workspace.activeConversationId))) continue;
    const remaining = await conversationsOf(tx, scopeId);
    await setActiveConversation(tx, scopeId, remaining[0]?.conversationId ?? "");
  }
}

async function archiveConversations(args: Json) {
  await run("readwrite", async (tx) => {
    for (const { scopeId, conversationId } of locations(args.conversations)) {
      const record = await tx.get<ConversationRecord>("conversations", conversationKey(scopeId, conversationId));
      if (!record) continue;
      await tx.put("conversations", {
        ...record,
        payload: { ...record.payload, archived: args.archived === true },
        updatedAt: timestamp(),
      });
    }
  });
  return null;
}

async function replaceConversation(args: Json) {
  const scopeId = text(args.scopeId);
  const conversationId = text(args.conversationId);
  const invalid = "AI 会话原子替换参数无效";
  if (!scopeId.trim() || !conversationId.trim() || args.conversation === undefined || args.conversation === null) {
    throw new Error(invalid);
  }
  const conversation = conversationObject(args.conversation, invalid);
  await run("readwrite", async (tx) => {
    await removeConversation(tx, scopeId, conversationId);
    await removeRelatedConversations(tx, scopeId, args.relatedConversations);
    const savedId = await writeConversation(tx, scopeId, conversation, 0, true);
    await setActiveConversation(tx, scopeId, savedId);
  });
  return null;
}

async function moveConversation(args: Json) {
  const sourceScopeId = text(args.sourceScopeId);
  const targetScopeId = text(args.targetScopeId);
  const conversationId = text(args.conversationId);
  const invalid = "AI 会话迁移参数无效";
  if (!sourceScopeId.trim() || !targetScopeId.trim() || !conversationId.trim() || sourceScopeId === targetScopeId
    || args.conversation === undefined || args.conversation === null) {
    throw new Error(invalid);
  }
  const conversation = conversationObject(args.conversation, invalid);
  const targetIds = ids(args.targetConversationIds);
  await run("readwrite", async (tx) => {
    if (!(await tx.get("conversations", conversationKey(sourceScopeId, conversationId)))) {
      throw new Error("要迁移的 AI 会话不存在");
    }
    if (await tx.get("conversations", conversationKey(targetScopeId, conversationId))) {
      throw new Error("目标工作区已存在同 ID 的 AI 会话");
    }
    await writeConversation(tx, targetScopeId, conversation, targetIds.length, true);
    await removeConversation(tx, sourceScopeId, conversationId);
    await setActiveConversation(tx, sourceScopeId, text(args.sourceActiveConversationId));
    await setPositions(tx, sourceScopeId, ids(args.sourceConversationIds));
    await setPositions(tx, targetScopeId, [conversationId, ...targetIds]);
    await setActiveConversation(tx, targetScopeId, conversationId);
  });
  return null;
}

/**
 * 浏览器里的 `store_apply_batch` 只承担 AI 历史那一部分：永久删除工作区时的 `delete_ai_scope`
 * （Go 删除 ai_workspaces 行，会话与条目随之级联删除）。其余配置键在浏览器中由 localStorage
 * 保存，persistent-store 不会把它们发到这里；收到任何其他操作都按 Go 的「不支持」报错，绝不伪装成功。
 */
async function applyStoreBatch(args: Json) {
  const operations = Array.isArray(args.operations) ? args.operations : [];
  const scopeIds: string[] = [];
  for (const operation of operations) {
    const type = isRecord(operation) ? text(operation.type) : "";
    if (type !== "delete_ai_scope") throw new Error(`不支持的应用配置批处理操作: ${JSON.stringify(type)}`);
    scopeIds.push(text(operation.key));
  }
  if (!scopeIds.length) return null;
  await run("readwrite", async (tx) => {
    for (const scopeId of scopeIds) {
      for (const record of await tx.getAll<ConversationRecord>("conversations", scopeId)) {
        await removeConversation(tx, scopeId, record.conversationId);
      }
      // 没有会话元数据的残留条目（理论上不存在）一并清掉。
      for (const record of await tx.getAll<ItemsRecord>("items", scopeId)) {
        await tx.delete("items", conversationKey(scopeId, record.conversationId));
      }
      await tx.delete("workspaces", scopeId);
    }
  });
  return null;
}

/** Go matchSnippet: the text around the first case-insensitive match, on one line, ±60 runes. */
function matchSnippet(content: string, needle: string) {
  const chars = Array.from(content);
  const needleLength = Array.from(needle).length;
  let at = -1;
  const lower = content.toLowerCase();
  const index = lower.length === content.length ? lower.indexOf(needle) : -1;
  if (index >= 0) {
    at = Array.from(content.slice(0, index)).length;
  } else {
    for (let start = 0; start + needleLength <= chars.length; start += 1) {
      if (chars.slice(start, start + needleLength).join("").toLowerCase() === needle) {
        at = start;
        break;
      }
    }
  }
  if (at < 0) return undefined;
  const start = Math.max(0, at - SNIPPET_RADIUS);
  const end = Math.min(chars.length, at + needleLength + SNIPPET_RADIUS);
  let snippet = chars.slice(start, end).join("").split(/\s+/).filter(Boolean).join(" ");
  if (start > 0) snippet = `…${snippet}`;
  if (end < chars.length) snippet += "…";
  return snippet;
}

interface MessageSearchMatch {
  scopeId: string;
  conversationId: string;
  title: string;
  messageId: string;
  role: string;
  snippet: string;
  createdAt?: number;
}

async function searchMessages(args: Json) {
  const query = text(args.query).trim();
  if (!query || Array.from(query).length > MAX_SEARCH_QUERY_RUNES) throw new Error("AI 消息搜索关键字为空或过长");
  const requested = typeof args.limit === "number" && args.limit > 0 ? Math.floor(args.limit) : DEFAULT_SEARCH_LIMIT;
  const limit = Math.min(requested, MAX_SEARCH_LIMIT);
  const scopes = new Set(ids(args.scopeIds));
  const needle = query.toLowerCase();
  return run("readonly", async (tx) => {
    const conversations = (await tx.getAll<ConversationRecord>("conversations"))
      .filter((record) => scopes.size === 0 || scopes.has(record.scopeId))
      .sort((a, b) => b.updatedAt - a.updatedAt || (a.conversationId < b.conversationId ? -1 : a.conversationId > b.conversationId ? 1 : 0));
    const matches: MessageSearchMatch[] = [];
    let scanned = 0;
    for (const record of conversations) {
      if (matches.length >= limit || scanned >= MAX_SEARCH_SCAN_ROWS) break;
      const items = await tx.get<ItemsRecord>("items", conversationKey(record.scopeId, record.conversationId));
      for (const item of items?.messages ?? []) {
        if (matches.length >= limit || scanned >= MAX_SEARCH_SCAN_ROWS) break;
        scanned += 1;
        if (!isRecord(item) || blank(item.id) || item.presentation === "error" || typeof item.content !== "string") continue;
        const snippet = matchSnippet(item.content, needle);
        if (snippet === undefined) continue;
        const createdAt = typeof item.createdAt === "number" && Number.isInteger(item.createdAt) ? item.createdAt : 0;
        matches.push({
          scopeId: record.scopeId,
          conversationId: record.conversationId,
          title: text(record.payload.title),
          messageId: text(item.id),
          role: text(item.role),
          snippet,
          // Go omitempty: a zero createdAt is left out.
          ...(createdAt ? { createdAt } : {}),
        });
      }
    }
    return matches;
  });
}

registerBrowserHandler("ai_store_load_workspace", loadWorkspace);
registerBrowserHandler("ai_store_list_workspaces", listWorkspaces);
registerBrowserHandler("ai_store_load_conversation", loadConversation);
registerBrowserHandler("ai_store_save_conversation", saveConversation);
registerBrowserHandler("ai_store_save_workspace_meta", saveWorkspaceMeta);
registerBrowserHandler("ai_store_delete_conversation", deleteConversation);
registerBrowserHandler("ai_store_replace_conversation", replaceConversation);
registerBrowserHandler("ai_store_move_conversation", moveConversation);
registerBrowserHandler("ai_store_search_messages", searchMessages);
registerBrowserHandler("store_apply_batch", applyStoreBatch);
