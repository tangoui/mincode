import type {
  AiAssistantEvent,
  AiAssistantMessage,
  AiAssistantPermission,
  AiConversation,
  AiConversationStatus,
} from "@/stores/ai-assistant";
import { describeAiAgentEventActivity, type AiAgentActivityKind } from "./agent-event-activity";
import type { AiAnchoredEventGroup } from "./agent-event-groups";
import { isAiAgentEventRunning } from "./agent-event-timing";
import type { ToolApprovalQueueItem } from "./tool-approval-queue";

export const AI_TIMELINE_READ_ONLY_KINDS: readonly AiAgentActivityKind[] = [
  "readFile", "openFile", "searchCode", "searchFiles",
];

const FAILED_STATUSES = new Set(["failed", "error", "cancelled"]);

export type AiTimelineDelegationChild = Pick<AiConversation, "id" | "title" | "createdAt" | "selectedModel" | "status" | "delegation">;

export function aiTimelineDelegationStatus(child: AiTimelineDelegationChild): AiConversationStatus {
  return child.delegation?.resultStatus ?? child.status;
}

export function isAiTimelineDelegationSettled(child: AiTimelineDelegationChild) {
  const status = aiTimelineDelegationStatus(child);
  return child.delegation?.result !== undefined || status === "completed" || status === "failed" || status === "interrupted";
}

export type AiTimelineItem =
  | { kind: "message"; key: string; index: number; message: AiAssistantMessage }
  | { kind: "tool"; key: string; event: AiAssistantEvent; awaitingApproval: boolean }
  | { kind: "toolSummary"; key: string; events: AiAssistantEvent[]; running: boolean }
  | { kind: "thought" | "plan" | "mode"; key: string; event: AiAssistantEvent }
  | { kind: "approval"; key: string; approval: ToolApprovalQueueItem }
  | { kind: "permission"; key: string; permission: AiAssistantPermission }
  | { kind: "continue"; key: string; index: number }
  | { kind: "thinking"; key: string }
  | { kind: "delegation"; key: string; toolKey?: string; children: AiTimelineDelegationChild[] };

interface TimelineDelegations {
  messages: readonly AiAssistantMessage[];
  children: readonly AiTimelineDelegationChild[];
}

export interface AiTimelineInput {
  messages: ReadonlyArray<{ message: AiAssistantMessage; index: number }>;
  groupsByAnchor: ReadonlyMap<number, AiAnchoredEventGroup>;
  activeEvents: readonly AiAssistantEvent[];
  anchoredApprovals: ReadonlyMap<number, readonly ToolApprovalQueueItem[]>;
  activeApprovals: readonly ToolApprovalQueueItem[];
  permission: AiAssistantPermission | null;
  continueAt: number | null;
  running: boolean;
  conversationRunning?: boolean;
  delegations?: TimelineDelegations;
}

export function aiTimelineToolCallIds(approval: ToolApprovalQueueItem): string[] {
  const id = approval.request.toolCallId;
  return id ? [`loop-tool-${id}`, id] : [];
}

export function aiPermissionToolCallId(permission: AiAssistantPermission): string | undefined {
  const call = permission.toolCall;
  if (!call || typeof call !== "object" || !("toolCallId" in call)) return undefined;
  return typeof call.toolCallId === "string" ? call.toolCallId : undefined;
}

type ApprovalItem = Extract<AiTimelineItem, { kind: "approval" | "permission" }>;

export function aiTimelineEventKey(event: AiAssistantEvent) {
  return JSON.stringify([event.triggerMessageId ?? event.afterMessageIndex, event.id]);
}

function approvalItem(approval: ToolApprovalQueueItem): ApprovalItem {
  return { kind: "approval", key: `approval:${approval.id}`, approval };
}

export function buildAiTimelineItems(input: AiTimelineInput): AiTimelineItem[] {
  const items: AiTimelineItem[] = [];
  const toolMatches = new Map<string, AiAssistantEvent[]>();
  const attachedApprovals = new Map<AiAssistantEvent, ApprovalItem[]>();
  const placed = new Set<string>();
  const groupAnchors = new Map<number, number>();

  function indexTools(events: readonly AiAssistantEvent[]) {
    for (const event of events) {
      if (event.kind !== "tool") continue;
      const matches = toolMatches.get(event.id) ?? [];
      matches.push(event);
      toolMatches.set(event.id, matches);
    }
  }

  function attach(item: ApprovalItem, ids: readonly string[], anchor?: string) {
    const candidates = ids.flatMap((id) => toolMatches.get(id) ?? []);
    const event = (anchor && candidates.find((candidate) => candidate.triggerMessageId === anchor))
      || candidates[candidates.length - 1];
    if (!event) return;
    const approvals = attachedApprovals.get(event) ?? [];
    approvals.push(item);
    attachedApprovals.set(event, approvals);
    placed.add(item.key);
  }

  if (input.anchoredApprovals.size || input.activeApprovals.length || input.permission) {
    for (const group of input.groupsByAnchor.values()) {
      for (const anchor of group.anchorIndexes) groupAnchors.set(anchor, group.anchorIndex);
      indexTools(group.events);
    }
    indexTools(input.activeEvents);
  }

  const unmatchedAt = new Map<number, ApprovalItem[]>();
  for (const [anchor, approvals] of input.anchoredApprovals) {
    for (const approval of approvals) {
      const item = approvalItem(approval);
      attach(item, aiTimelineToolCallIds(approval), approval.triggerMessageId);
      if (placed.has(item.key)) continue;
      const groupAnchor = groupAnchors.get(anchor) ?? anchor;
      const remaining = unmatchedAt.get(groupAnchor) ?? [];
      remaining.push(item);
      unmatchedAt.set(groupAnchor, remaining);
    }
  }
  const activeApprovals = input.activeApprovals.map((approval) => {
    const item = approvalItem(approval);
    attach(item, aiTimelineToolCallIds(approval));
    return item;
  });
  const permission: ApprovalItem | undefined = input.permission
    ? { kind: "permission", key: `permission:${input.permission.requestId}`, permission: input.permission }
    : undefined;
  if (permission) {
    const id = aiPermissionToolCallId(permission.permission);
    if (id) attach(permission, [id]);
  }

  function appendEvents(events: readonly AiAssistantEvent[]) {
    let summary: Extract<AiTimelineItem, { kind: "toolSummary" }> | undefined;
    const conversationRunning = input.conversationRunning ?? input.running;
    for (const event of events) {
      const key = aiTimelineEventKey(event);
      if (event.kind !== "tool") {
        summary = undefined;
        items.push({ kind: event.kind, key: `${event.kind}:${key}`, event });
        continue;
      }
      const approvals = attachedApprovals.get(event);
      const unfinished = isAiAgentEventRunning(event);
      const readOnly = !approvals && (conversationRunning || !unfinished)
        && !FAILED_STATUSES.has(event.status?.trim().toLowerCase() ?? "")
        && AI_TIMELINE_READ_ONLY_KINDS.includes(describeAiAgentEventActivity(event).kind);
      if (readOnly) {
        if (!summary) {
          summary = { kind: "toolSummary", key: `toolSummary:${key}`, events: [], running: false };
          items.push(summary);
        }
        summary.events.push(event);
        summary.running ||= unfinished;
        continue;
      }
      summary = undefined;
      items.push({ kind: "tool", key: `tool:${key}`, event, awaitingApproval: Boolean(approvals) });
      if (approvals) items.push(...approvals);
    }
  }

  const messagesByIndex = new Map(input.messages.map(({ message, index }) => [index, message]));
  const anchors = new Set([...messagesByIndex.keys(), ...input.groupsByAnchor.keys(), ...unmatchedAt.keys()]);
  for (const index of [...anchors].sort((left, right) => left - right)) {
    const message = messagesByIndex.get(index);
    if (message) {
      items.push({ kind: "message", key: `message:${message.id ?? index}`, index, message });
      if (input.continueAt === index) items.push({ kind: "continue", key: `continue:${message.id ?? index}`, index });
    }
    const group = input.groupsByAnchor.get(index);
    if (group) appendEvents(group.events);
    const approvals = unmatchedAt.get(index);
    if (approvals) items.push(...approvals);
  }

  appendEvents(input.activeEvents);
  items.push(...activeApprovals.filter(({ key }) => !placed.has(key)));
  if (permission && !placed.has(permission.key)) items.push(permission);
  if (input.running) items.push({ kind: "thinking", key: "thinking" });
  return input.delegations?.children.length ? insertDelegations(items, input.delegations) : items;
}

function insertDelegations(items: AiTimelineItem[], { messages, children }: TimelineDelegations): AiTimelineItem[] {
  const turnByMessageId = new Map<string, number>();
  const turnByMessageIndex = new Map<number, number>();
  const datedTurns: Array<{ index: number; createdAt: number }> = [];
  let turnIndex: number | undefined;
  for (const [index, message] of messages.entries()) {
    if (message.role === "user") {
      turnIndex = index;
      if (message.createdAt) datedTurns.push({ index, createdAt: message.createdAt });
    }
    if (turnIndex === undefined) continue;
    turnByMessageIndex.set(index, turnIndex);
    if (message.id) turnByMessageId.set(message.id, turnIndex);
  }

  const newestDatedTurns = datedTurns.reverse();
  const dispatches = new Map<string, Array<{ position: number; turn: number | undefined }>>();
  const turnEnds = new Map<number, number>();
  turnIndex = undefined;
  for (const [position, item] of items.entries()) {
    if (item.kind === "message" && item.message.role === "user") {
      if (position > 0 && turnIndex !== undefined) turnEnds.set(turnIndex, position - 1);
      turnIndex = item.index;
    } else if (item.kind === "tool" && item.event.toolName === "agent.run_parallel") {
      const { event } = item;
      const turn = (event.triggerMessageId ? turnByMessageId.get(event.triggerMessageId) : undefined)
        ?? turnByMessageIndex.get(event.afterMessageIndex ?? -1) ?? turnIndex;
      const calls = dispatches.get(event.id) ?? [];
      calls.push({ position, turn });
      dispatches.set(event.id, calls);
    }
  }
  if (turnIndex !== undefined) turnEnds.set(turnIndex, items.length - 1);

  const groupsAt = new Map<number, Array<Extract<AiTimelineItem, { kind: "delegation" }>>>();
  const groupsByKey = new Map<string, Extract<AiTimelineItem, { kind: "delegation" }>>();
  for (const child of children) {
    const { toolCallId, triggerMessageId, turnMessageId } = child.delegation ?? {};
    const turn = (turnMessageId ? turnByMessageId.get(turnMessageId) : undefined)
      ?? (triggerMessageId ? turnByMessageId.get(triggerMessageId) : undefined)
      ?? (!turnMessageId && !triggerMessageId
        ? newestDatedTurns.find((candidate) => candidate.createdAt <= child.createdAt)?.index
        : undefined);
    const candidates = toolCallId
      ? dispatches.get(`loop-tool-${toolCallId}`) ?? dispatches.get(toolCallId) ?? []
      : [];
    const candidatesInTurn = turn === undefined ? candidates : candidates.filter(candidate => candidate.turn === turn);
    const exact = triggerMessageId && candidatesInTurn.find(({ position }) => {
      const item = items[position];
      return item?.kind === "tool" && item.event.triggerMessageId === triggerMessageId;
    });
    const dispatch = exact || (candidatesInTurn.length === 1 ? candidatesInTurn[0] : undefined);
    const toolKey = dispatch ? items[dispatch.position]?.key : undefined;
    const turnEnd = turn === undefined ? undefined : turnEnds.get(turn);
    const turnKey = turn !== undefined && turnEnd !== undefined ? `turn:${messages[turn]?.id ?? turn}` : "end";
    const anchorKey = toolKey ?? turnKey;
    const key = `delegation:${anchorKey}`;
    const position = dispatch?.position ?? turnEnd ?? items.length - 1;
    let group = groupsByKey.get(key);
    if (!group) {
      group = { kind: "delegation", key, toolKey, children: [] };
      groupsByKey.set(key, group);
      const atPosition = groupsAt.get(position) ?? [];
      atPosition.push(group);
      groupsAt.set(position, atPosition);
    }
    group.children.push(child);
  }
  return [...groupsAt.get(-1) ?? [], ...items.flatMap((item, index) => [item, ...groupsAt.get(index) ?? []])];
}
