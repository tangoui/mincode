import { throwIfAborted, createAbortError } from "@/domain/async/abort";
import type { AiCapability } from "@/domain/ai/capability-registry";
import { useVueI18n } from "@/i18n/messages";
import type { AiAssistantMessage, AiConversation } from "@/stores/ai-assistant";
import { redactSensitiveText, tool } from "./shared";

export type AiSessionTerminalState = "idle" | "failed";
export type AiSessionState = AiSessionTerminalState | "running" | "waiting_approval" | "waiting_input";

export interface AiSessionTurn {
  messageId: string;
  done: Promise<AiSessionTerminalState>;
  cancel(): void;
}

export interface AiSessionRunner {
  start(id: string, text: string): AiSessionTurn;
  state(id: string): AiSessionState;
  stop(id: string): Promise<boolean>;
}

interface LocalAgent {
  id: string;
  name: string;
}

export interface AiSessionToolDependencies {
  listLocalAgents(): Promise<LocalAgent[]>;
  listConversations(): Promise<AiConversation[]>;
  getConversation(id: string, details?: boolean): Promise<AiConversation | undefined>;
  createConversation(input: {
    mode: AiConversation["mode"];
    agentId?: string;
    title?: string;
    cwd?: string;
    fullAccess: boolean;
  }): Promise<AiConversation>;
  loadRunner(): Promise<AiSessionRunner>;
}

const { t } = useVueI18n();
const string = { type: "string" };
const conversationId = { conversationId: string };
const conversationRequired = ["conversationId"];
const maxWaitSeconds = 540;

function invalid(parameter: string): never {
  throw new Error(t("settings.agentControl.invalidArgument", { parameter }));
}

function optionalString(value: unknown, name: string) {
  if (value === undefined) return undefined;
  if (typeof value !== "string" || !value.trim()) return invalid(name);
  return value.trim();
}

function integer(value: unknown, name: string, fallback: number, min: number, max: number) {
  if (value === undefined) return fallback;
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < min || value > max) return invalid(name);
  return value;
}

function summary(conversation: AiConversation, agents: LocalAgent[]) {
  const agentId = conversation.mode === "agent" ? (conversation.requestedAgentId ?? conversation.acpAdapterId) : undefined;
  return {
    id: conversation.id,
    title: redactSensitiveText(conversation.title),
    mode: conversation.mode === "agent" ? "local-agent" : "native",
    agentId,
    agentName: agentId ? agents.find((agent) => agent.id === agentId)?.name ?? agentId : undefined,
    status: conversation.status,
    updatedAt: conversation.updatedAt,
  };
}

function messageSummary({ id, role, content, createdAt }: AiAssistantMessage) {
  return { id, role, content: redactSensitiveText(content), createdAt };
}

function lastReply(conversation: AiConversation, messageId: string) {
  const index = conversation.messages.findIndex((message) => message.id === messageId);
  if (index < 0) return null;
  let reply: AiAssistantMessage | undefined;
  for (let cursor = index + 1; cursor < conversation.messages.length; cursor += 1) {
    const message = conversation.messages[cursor]!;
    if (message.role === "user") break;
    if (message.role === "assistant" && !message.eventAnchor && message.content.trim()) reply = message;
  }
  return reply ? messageSummary(reply) : null;
}

function waitForState(
  readState: () => AiSessionState,
  timeoutSecs: number,
  signal: AbortSignal,
  turn?: AiSessionTurn,
) {
  return new Promise<Exclude<AiSessionState, "running"> | "timeout">((resolve, reject) => {
    const deadline = Date.now() + timeoutSecs * 1_000;
    let timer: ReturnType<typeof setTimeout> | undefined;
    function cleanup() {
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
    }
    function finish(state: Exclude<AiSessionState, "running"> | "timeout") {
      cleanup();
      resolve(state);
    }
    function abort() {
      cleanup();
      reject(createAbortError());
      turn?.cancel();
    }
    function check() {
      const state = readState();
      if (state === "waiting_approval" || state === "waiting_input" || (!turn && state !== "running")) {
        finish(state);
      } else {
        const remaining = deadline - Date.now();
        if (remaining <= 0) finish("timeout");
        else timer = setTimeout(check, Math.min(remaining, 100));
      }
    }
    if (signal.aborted) return abort();
    signal.addEventListener("abort", abort, { once: true });
    if (turn) void turn.done.then(finish, () => finish("failed"));
    check();
  });
}

export function aiSessionCapabilities(deps: AiSessionToolDependencies): AiCapability[] {
  const base = { module: "ai", target: "none" } as const;
  const read = { ...base, mode: "read", risk: "read" } as const;
  const write = { ...base, mode: "write", risk: "write" } as const;
  async function getConversation(id: string, signal: AbortSignal, details = false) {
    const conversation = await deps.getConversation(id, details);
    throwIfAborted(signal);
    if (!conversation) throw new Error(t("settings.agentControl.conversationMissing", { id }));
    return conversation;
  }
  return [
    {
      ...read,
      tool: tool("ai.list_conversations", "List recent AI and local Agent conversations.", {
        limit: { type: "integer", minimum: 1, maximum: 50 },
      }),
      execute: async ({ input }) => {
        const limit = integer(input.limit, "limit", 50, 1, 50);
        const [conversations, agents] = await Promise.all([deps.listConversations(), deps.listLocalAgents()]);
        return { conversations: [...conversations].sort((a, b) => b.updatedAt - a.updatedAt).slice(0, limit).map((item) => summary(item, agents)) };
      },
    },
    {
      ...read,
      tool: tool("ai.list_local_agents", "List available local Agent configurations and their IDs."),
      execute: async () => ({ agents: await deps.listLocalAgents() }),
    },
    {
      ...write,
      tool: tool("ai.create_conversation", "Create an AI conversation in the background. Local Agent mode requires an agentId from ai.list_local_agents.", {
        mode: { type: "string", enum: ["native", "local-agent"] }, agentId: string, title: string, cwd: string,
        approval: { type: "string", enum: ["default", "full-access"], default: "default" },
      }, ["mode"]),
      execute: async ({ input, projectRoot, externalAgent, signal }) => {
        if (input.mode !== "native" && input.mode !== "local-agent") invalid("mode");
        const agentId = optionalString(input.agentId, "agentId");
        const title = optionalString(input.title, "title");
        const cwd = optionalString(input.cwd, "cwd") ?? (externalAgent ? projectRoot || undefined : undefined);
        if (input.approval !== undefined && input.approval !== "default" && input.approval !== "full-access") invalid("approval");
        const local = input.mode === "local-agent";
        if (local !== Boolean(agentId)) invalid("agentId");
        const agents = local ? await deps.listLocalAgents() : [];
        if (agentId && !agents.some((agent) => agent.id === agentId)) throw new Error(t("settings.agentControl.agentMissing", { id: agentId }));
        throwIfAborted(signal);
        return summary(await deps.createConversation({ mode: local ? "agent" : "agentic", agentId, title, cwd, fullAccess: input.approval === "full-access" }), agents);
      },
    },
    {
      ...write,
      tool: tool("ai.send_message", "Send a message in the background. Set wait to return the latest reply from this turn or a status if it needs the user.", {
        ...conversationId, text: string, wait: { type: "boolean", default: false },
      }, [...conversationRequired, "text"]),
      execute: async ({ input, signal }) => {
        const id = optionalString(input.conversationId, "conversationId") ?? invalid("conversationId");
        const text = optionalString(input.text, "text") ?? invalid("text");
        if (input.wait !== undefined && typeof input.wait !== "boolean") invalid("wait");
        await getConversation(id, signal, true);
        const runner = await deps.loadRunner();
        throwIfAborted(signal);
        const turn = runner.start(id, text);
        const result = { conversationId: id, messageId: turn.messageId };
        if (!input.wait) return result;
        const state = await waitForState(() => runner.state(id), maxWaitSeconds, signal, turn);
        const conversation = await deps.getConversation(id);
        return { ...result, state, reply: conversation ? lastReply(conversation, turn.messageId) : null };
      },
    },
    {
      ...read,
      tool: tool("ai.get_messages", "Read a page of conversation messages with tool call summaries.", {
        ...conversationId, sinceIndex: { type: "integer", minimum: 0 }, limit: { type: "integer", minimum: 1, maximum: 100 },
      }, conversationRequired),
      execute: async ({ input, signal }) => {
        const id = optionalString(input.conversationId, "conversationId") ?? invalid("conversationId");
        const limit = integer(input.limit, "limit", 100, 1, 100);
        const requestedIndex = integer(input.sinceIndex, "sinceIndex", 0, 0, Number.MAX_SAFE_INTEGER);
        const conversation = await getConversation(id, signal, true);
        const start = Math.min(requestedIndex, conversation.messages.length);
        const page = conversation.messages.slice(start, start + limit);
        const ids = new Map(page.map((message, offset) => [message.id, start + offset]));
        const toolCalls = new Map<number, Array<{ name?: string; title: string; status?: string }>>();
        for (const event of conversation.agentEvents) {
          if (event.kind !== "tool") continue;
          const index = event.triggerMessageId ? ids.get(event.triggerMessageId) : event.afterMessageIndex;
          if (index === undefined || index < start || index >= start + page.length) continue;
          const calls = toolCalls.get(index) ?? [];
          calls.push({ name: event.toolName, title: redactSensitiveText(event.title), status: event.status });
          toolCalls.set(index, calls);
        }
        return {
          conversationId: id,
          nextIndex: start + page.length,
          messages: page.map((message, offset) => ({ ...messageSummary(message), index: start + offset, toolCalls: toolCalls.get(start + offset) ?? [] })),
        };
      },
    },
    {
      ...read,
      tool: tool("ai.wait_idle", "Wait for a conversation to become idle, fail, or wait for approval or input.", {
        ...conversationId, timeoutSecs: { type: "integer", minimum: 0, maximum: maxWaitSeconds, default: 60 },
      }, conversationRequired),
      execute: async ({ input, signal, conversationId: caller }) => {
        const id = optionalString(input.conversationId, "conversationId") ?? invalid("conversationId");
        const timeout = integer(input.timeoutSecs, "timeoutSecs", 60, 0, maxWaitSeconds);
        if (id === caller) throw new Error(t("settings.agentControl.conversationSelfWait"));
        await getConversation(id, signal);
        const runner = await deps.loadRunner();
        return { conversationId: id, state: await waitForState(() => runner.state(id), timeout, signal) };
      },
    },
    {
      ...write,
      tool: tool("ai.stop", "Stop the current turn of a conversation.", conversationId, conversationRequired),
      execute: async ({ input, signal }) => {
        const id = optionalString(input.conversationId, "conversationId") ?? invalid("conversationId");
        await getConversation(id, signal);
        const runner = await deps.loadRunner();
        throwIfAborted(signal);
        return { conversationId: id, stopped: await runner.stop(id) };
      },
    },
  ];
}
