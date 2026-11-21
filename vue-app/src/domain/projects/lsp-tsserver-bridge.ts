/**
 * Vue language tools 3.x hybrid mode: vue-language-server sends
 * `tsserver/request` notifications that a tsserver loaded with
 * @vue/typescript-plugin must answer with `tsserver/response`. MinCode runs
 * that tsserver as the `vue-ts` companion (typescript-language-server) and the
 * project LSP runtime forwards between the two sessions with these helpers.
 */

/** Server id → companion session that answers its tsserver requests. */
export const TSSERVER_BRIDGES: Readonly<Record<string, string>> = { "vue-ls": "vue-ts" };

export interface TsserverRequest {
  id: number;
  command: string;
  args: unknown;
}

/**
 * vscode-jsonrpc sends the single array argument by position, so the wire
 * params are `[[id, command, args]]`; a flat `[id, command, args]` is accepted too.
 */
export function parseTsserverRequest(params: unknown): TsserverRequest | undefined {
  if (!Array.isArray(params)) return undefined;
  const tuple: unknown[] = Array.isArray(params[0]) ? params[0] : params;
  const [id, command, args] = tuple;
  if (!Number.isSafeInteger(id) || typeof command !== "string" || !command || command.length > 200) return undefined;
  return { id: id as number, command, args };
}

/** typescript-language-server's workspace/executeCommand for a raw tsserver request. */
export function tsserverExecuteCommand(request: TsserverRequest) {
  return {
    command: "typescript.tsserverRequest",
    arguments: [request.command, request.args, { isAsync: true, lowPriority: true }],
  };
}

/** The companion returns the tsserver response; the Vue server expects only its body. */
export function tsserverResponseBody(result: unknown): unknown {
  if (result !== null && typeof result === "object" && !Array.isArray(result) && "body" in result) {
    return (result as { body?: unknown }).body ?? null;
  }
  return result ?? null;
}

/** Params of the `tsserver/response` notification (same positional framing). */
export function tsserverResponseParams(id: number, body: unknown) {
  return [[id, body ?? null]];
}
