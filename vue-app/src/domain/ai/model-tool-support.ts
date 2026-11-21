/** The tools a request offers: none for a model listed in the profile's noToolModels. */
export function toolsForModel<T>(model: string, noToolModels: readonly string[] | undefined, tools: T[]): T[] {
  return noToolModels?.includes(model) ? [] : tools;
}

interface ChatHistoryMessage {
  role: string;
  content: string;
  toolCalls?: { name: string }[];
  toolCallId?: string;
  toolResult?: unknown;
  thinkingBlocks?: unknown[];
}

/**
 * History for a model that takes no tools: tool calls and their results from
 * earlier turns become plain assistant text, because providers reject
 * tool_calls or tool messages in a request without tool definitions. Runs of
 * assistant text are merged so roles still alternate. The input is not changed.
 */
export function plainChatHistory<T extends ChatHistoryMessage>(messages: readonly T[]): T[] {
  const out: T[] = [];
  for (const message of messages) {
    const usedTools = message.role === "tool" || Boolean(message.toolCalls?.length);
    if (!usedTools && message.role !== "assistant") {
      out.push(message);
      continue;
    }
    let content = message.content;
    if (message.role === "tool") content = `[Tool result]\n${message.content}`;
    else if (message.toolCalls?.length) {
      const calls = message.toolCalls.map((call) => `[Called tool ${call.name}]`).join("\n");
      content = content.trim() ? `${content}\n${calls}` : calls;
    }
    const { toolCalls: _calls, toolCallId: _id, toolResult: _result, thinkingBlocks, ...rest } = message;
    const plain = { ...rest, ...(usedTools || !thinkingBlocks ? {} : { thinkingBlocks }), role: "assistant", content } as T;
    const previous = out[out.length - 1];
    if (previous?.role === "assistant") {
      const { thinkingBlocks: _blocks, ...merged } = previous;
      out[out.length - 1] = { ...merged, content: `${previous.content}\n\n${content}` } as T;
    } else {
      out.push(plain);
    }
  }
  return out;
}
