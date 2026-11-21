/**
 * Item-level diff for saving AI conversations. The Go store keeps every
 * message, history entry, agent event and queued message in its own row; a
 * save after the first only sends the items whose JSON changed since the last
 * successful save, plus each group's previous and current length.
 *
 * The last saved state keeps each item's JSON text: comparing strings is far
 * cheaper than hashing every item on each save, so callers bound how many
 * conversations they keep it for.
 */

export const AI_CONVERSATION_ITEM_FIELDS = ["messages", "agentHistory", "agentEvents", "messageQueue"] as const;

export type AiConversationItemField = (typeof AI_CONVERSATION_ITEM_FIELDS)[number];

/** Per group, one value per item, in order. */
export type ConversationItemGroups<T> = Record<AiConversationItemField, T[]>;

export interface SavedConversationItems {
  /** Revision the Go store returned for this exact item state. */
  revision: number;
  items: ConversationItemGroups<string>;
}

export interface ConversationItemGroupPatch {
  baseLength: number;
  length: number;
  changes: Array<{ position: number; item: string }>;
}

export interface ConversationItemPatch {
  baseRevision: number;
  groups: Record<AiConversationItemField, ConversationItemGroupPatch>;
}

export const AI_ITEM_PATCH_CONFLICT = "ai_store_item_patch_conflict";

/** Each item's JSON exactly as a full save would send it (a non-array group counts as empty). */
export function serializeConversationItems(conversation: Readonly<Record<string, unknown>>): ConversationItemGroups<string> {
  const serialized = {} as ConversationItemGroups<string>;
  for (const field of AI_CONVERSATION_ITEM_FIELDS) {
    const items = conversation[field];
    serialized[field] = Array.isArray(items) ? items.map((item) => JSON.stringify(item) ?? "null") : [];
  }
  return serialized;
}

export function diffConversationItems(
  saved: SavedConversationItems,
  items: ConversationItemGroups<string>,
): ConversationItemPatch {
  const groups = {} as ConversationItemPatch["groups"];
  for (const field of AI_CONVERSATION_ITEM_FIELDS) {
    const previous = saved.items[field];
    const changes: ConversationItemGroupPatch["changes"] = [];
    items[field].forEach((item, position) => {
      if (previous[position] !== item) changes.push({ position, item });
    });
    groups[field] = { baseLength: previous.length, length: items[field].length, changes };
  }
  return { baseRevision: saved.revision, groups };
}

/** The conversation without its item groups (sent alongside a patch). */
export function conversationMetadata<T extends Record<string, unknown>>(conversation: T): Omit<T, AiConversationItemField> {
  const metadata: Record<string, unknown> = { ...conversation };
  for (const field of AI_CONVERSATION_ITEM_FIELDS) delete metadata[field];
  return metadata as Omit<T, AiConversationItemField>;
}

export function isItemPatchConflict(error: unknown) {
  const message = error instanceof Error ? error.message : String(error);
  return message.includes(AI_ITEM_PATCH_CONFLICT);
}

interface SaveConversationResult {
  itemRevision?: number;
}

/** One `ai_store_save_conversation` call with `replaceItems: true`. */
export type SaveConversationRequest = (args: {
  scopeId: string;
  position: number;
  conversation: Record<string, unknown>;
  itemPatch?: ConversationItemPatch;
}) => Promise<SaveConversationResult | null | undefined>;

/**
 * Detail saves that send only changed items. Each conversation's item state
 * is remembered after a successful save; a patch the store rejects as
 * outdated is resent in full. Item JSON of a long conversation is several MB,
 * so only the `limit` most recently saved conversations are remembered.
 */
export function createConversationItemSaver(request: SaveConversationRequest, limit = 2) {
  const saved = new Map<string, SavedConversationItems>();
  const keyOf = (scopeId: string, conversationId: string) => JSON.stringify([scopeId, conversationId]);

  async function save(scopeId: string, position: number, conversation: Record<string, unknown> & { id: string }) {
    const key = keyOf(scopeId, conversation.id);
    const items = serializeConversationItems(conversation);
    const previous = saved.get(key);
    saved.delete(key);
    let result: SaveConversationResult | null | undefined;
    let sent = false;
    if (previous) {
      try {
        result = await request({
          scopeId,
          position,
          conversation: conversationMetadata(conversation),
          itemPatch: diffConversationItems(previous, items),
        });
        sent = true;
      } catch (error) {
        if (!isItemPatchConflict(error)) throw error;
      }
    }
    if (!sent) result = await request({ scopeId, position, conversation });
    const revision = result?.itemRevision;
    if (typeof revision !== "number" || revision <= 0) return;
    saved.set(key, { revision, items });
    if (saved.size > limit) saved.delete(saved.keys().next().value!);
  }

  function forget(conversationId: string) {
    for (const key of [...saved.keys()]) {
      if ((JSON.parse(key) as [string, string])[1] === conversationId) saved.delete(key);
    }
  }

  return { save, forget, remembered: () => [...saved.keys()].map((key) => JSON.parse(key) as [string, string]) };
}
