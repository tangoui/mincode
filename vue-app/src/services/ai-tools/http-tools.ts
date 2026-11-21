import type { AiCapability } from "@/domain/ai/capability-registry";
import {
  isSensitiveDataName,
  requiredString,
  sanitizeAiResult,
  sanitizeUrlForAi,
  stringArgument,
  tool,
  type AiToolDependencies,
} from "./shared";

const sensitiveResponseHeaders = new Set([
  "authorization",
  "proxy-authorization",
  "set-cookie",
  "set-cookie2",
  "www-authenticate",
  "proxy-authenticate",
]);

function stringPairs(value: unknown): [string, string][] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((row) => {
    if (!Array.isArray(row) || row.length !== 2) return [];
    const [key, itemValue] = row;
    return typeof key === "string" && typeof itemValue === "string" && key.trim()
      ? [[key.trim(), itemValue] as [string, string]]
      : [];
  });
}

// 后端 mockRule 的延迟字段是 delay_ms，工具 schema 用 delayMs，这里必须转一次。
function mockRules(input: Record<string, unknown>) {
  if (!Array.isArray(input.rules)) throw new Error("http.start_mock_server requires rules");
  return input.rules.slice(0, 200).map((entry) => {
    const rule = entry && typeof entry === "object" && !Array.isArray(entry) ? entry as Record<string, unknown> : {};
    const method = stringArgument(rule, "method").toUpperCase();
    const path = stringArgument(rule, "path");
    if (!method || !path) throw new Error("http.start_mock_server requires a method and path in every rule");
    return {
      method,
      path,
      status: typeof rule.status === "number" ? Math.max(100, Math.min(599, Math.round(rule.status))) : 200,
      body: typeof rule.body === "string" ? rule.body : "",
      headers: stringPairs(rule.headers),
      delay_ms: typeof rule.delayMs === "number" ? Math.max(0, Math.min(60000, Math.round(rule.delayMs))) : 0,
    };
  });
}

function requestUrl(input: Record<string, unknown>) {  const rawUrl = requiredString(input, "url", "http.send_request");
  const query = stringPairs(input.query);
  if (!query.length) return rawUrl;
  let url: URL;
  try {
    url = new URL(rawUrl);
  } catch {
    throw new Error("http.send_request requires a valid absolute URL");
  }
  for (const [key, value] of query) url.searchParams.append(key, value);
  return url.toString();
}

function safeHttpResponse(value: unknown): unknown {
  if (!value || typeof value !== "object" || Array.isArray(value)) return sanitizeAiResult(value);
  const response = value as Record<string, unknown>;
  const headers = stringPairs(response.headers).filter(
    ([name]) => !sensitiveResponseHeaders.has(name.toLowerCase()) && !isSensitiveDataName(name)
  );
  let body = response.body;
  if (typeof body === "string") {
    try {
      body = JSON.stringify(sanitizeAiResult(JSON.parse(body)));
    } catch {
      body = sanitizeAiResult(body);
    }
  }
  return sanitizeAiResult({
    ...response,
    ...(Array.isArray(response.headers) ? { headers } : {}),
    ...(typeof response.body === "string" ? { body } : {}),
  });
}

function httpApprovalPreview(input: Record<string, unknown>) {
  const method = (stringArgument(input, "method") || "GET").toUpperCase();
  const url = sanitizeUrlForAi(requestUrl(input));
  const headerNames = stringPairs(input.headers).map(([name]) => name);
  const body = typeof input.body === "string" ? input.body : "";
  const contentType = stringPairs(input.headers).find(([name]) => name.toLowerCase() === "content-type")?.[1];
  const bodyType = body ? contentType?.split(";", 1)[0]?.trim() || "inline text" : "none";
  return `${method} ${url}\nHeaders: ${headerNames.length ? headerNames.join(", ") : "none"}\nBody: ${bodyType}, ${new TextEncoder().encode(body).byteLength} bytes`;
}

export interface HttpWorkspaceAiDependencies extends AiToolDependencies {
  httpAction?(input: Record<string, unknown>): Promise<unknown>;
}

export function httpCapabilities(dependencies: HttpWorkspaceAiDependencies): AiCapability[] {
  const workspaceActions: AiCapability[] = dependencies.httpAction ? [
    {
      module: "http", target: "none", discovery: true,
      tool: tool("http.list_workspace_assets", "List non-sensitive HTTP workspace assets: request tabs, environment names, collections, saved requests and history metadata."),
      mode: "read", risk: "read",
      execute: () => dependencies.httpAction!({ action: "list-assets" }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.new_request_tab", "Create and focus a visible HTTP request tab. Method and URL are optional.", { method: { type: "string" }, url: { type: "string" }, name: { type: "string" } }),
      mode: "write", risk: "write",
      approvalPreview: ({ input }) => stringArgument(input, "name") || "新建 HTTP 请求标签",
      execute: ({ input }) => dependencies.httpAction!({ action: "new-request", method: stringArgument(input, "method") || undefined, url: stringArgument(input, "url") || undefined, name: stringArgument(input, "name") || undefined }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.close_request_tab", "Close one visible HTTP request tab by ID, or the active tab when tabId is omitted.", { tabId: { type: "string" } }),
      mode: "write", risk: "write",
      approvalPreview: ({ input }) => `Close HTTP request tab ${stringArgument(input, "tabId") || "active"}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "close-request", tabId: stringArgument(input, "tabId") || undefined }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.send_active_request", "Send the visible active HTTP request after it has been opened or configured in MinCode."),
      mode: "write", risk: "network",
      approvalPreview: () => "Send the visible active HTTP request. URL, headers and body are redacted.",
      execute: () => dependencies.httpAction!({ action: "send-active" }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.create_environment", "Create and select an HTTP environment by name. Variables remain editable in the visible HTTP workspace.", { collectionId: { type: "string" }, name: { type: "string" } }, ["collectionId", "name"]),
      mode: "write", risk: "write",
      approvalPreview: ({ input }) => `Create HTTP environment ${requiredString(input, "name", "http.create_environment")}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "create-environment", collectionId: requiredString(input, "collectionId", "http.create_environment"), name: requiredString(input, "name", "http.create_environment") }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.select_environment", "Select a visible HTTP environment by ID.", { collectionId: { type: "string" }, environmentId: { type: "string" } }, ["collectionId", "environmentId"]),
      mode: "write", risk: "write",
      approvalPreview: ({ input }) => `Select HTTP environment ${requiredString(input, "environmentId", "http.select_environment")}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "select-environment", collectionId: requiredString(input, "collectionId", "http.select_environment"), environmentId: requiredString(input, "environmentId", "http.select_environment") }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.delete_environment", "Delete an HTTP environment by ID. The final remaining environment cannot be deleted.", { collectionId: { type: "string" }, environmentId: { type: "string" } }, ["collectionId", "environmentId"]),
      mode: "write", risk: "dangerous",
      approvalPreview: ({ input }) => `Delete HTTP environment ${requiredString(input, "environmentId", "http.delete_environment")}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "delete-environment", collectionId: requiredString(input, "collectionId", "http.delete_environment"), environmentId: requiredString(input, "environmentId", "http.delete_environment") }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.create_collection", "Create an HTTP collection. Optionally provide a parent collection ID.", { name: { type: "string" }, parentId: { type: "string" } }, ["name"]),
      mode: "write", risk: "write",
      approvalPreview: ({ input }) => `Create HTTP collection ${requiredString(input, "name", "http.create_collection")}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "create-collection", name: requiredString(input, "name", "http.create_collection"), parentId: stringArgument(input, "parentId") || undefined }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.rename_collection", "Rename an HTTP collection by ID.", { collectionId: { type: "string" }, name: { type: "string" } }, ["collectionId", "name"]),
      mode: "write", risk: "write",
      approvalPreview: ({ input }) => `Rename HTTP collection ${requiredString(input, "collectionId", "http.rename_collection")}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "rename-collection", collectionId: requiredString(input, "collectionId", "http.rename_collection"), name: requiredString(input, "name", "http.rename_collection") }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.delete_collection", "Delete an HTTP collection and its saved requests by ID.", { collectionId: { type: "string" } }, ["collectionId"]),
      mode: "write", risk: "dangerous",
      approvalPreview: ({ input }) => `Delete HTTP collection ${requiredString(input, "collectionId", "http.delete_collection")}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "delete-collection", collectionId: requiredString(input, "collectionId", "http.delete_collection") }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.open_saved_request", "Open a saved HTTP request in a new visible tab.", { collectionId: { type: "string" }, requestId: { type: "string" } }, ["collectionId", "requestId"]),
      mode: "write", risk: "write",
      approvalPreview: ({ input }) => `Open saved HTTP request ${requiredString(input, "requestId", "http.open_saved_request")}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "open-saved-request", collectionId: requiredString(input, "collectionId", "http.open_saved_request"), requestId: requiredString(input, "requestId", "http.open_saved_request") }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.save_active_request", "Save the active visible HTTP request into a collection.", { collectionId: { type: "string" } }, ["collectionId"]),
      mode: "write", risk: "write",
      approvalPreview: ({ input }) => `Save active HTTP request to collection ${requiredString(input, "collectionId", "http.save_active_request")}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "save-active-request", collectionId: requiredString(input, "collectionId", "http.save_active_request") }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.open_history_item", "Open an HTTP history item in a new visible request tab.", { historyId: { type: "string" } }, ["historyId"]),
      mode: "write", risk: "write",
      approvalPreview: ({ input }) => `Open HTTP history ${requiredString(input, "historyId", "http.open_history_item")}`,
      execute: ({ input }) => dependencies.httpAction!({ action: "open-history", historyId: requiredString(input, "historyId", "http.open_history_item") }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.clear_history", "Clear all saved HTTP history entries."),
      mode: "write", risk: "dangerous",
      approvalPreview: () => "Clear all HTTP history entries",
      execute: () => dependencies.httpAction!({ action: "clear-history" }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.open_stream_panel", "Open the WebSocket or SSE panel in the visible HTTP workspace.", { kind: { type: "string", enum: ["ws", "sse"] } }, ["kind"]),
      mode: "read", risk: "read",
      execute: ({ input }) => dependencies.httpAction!({ action: "open-stream", kind: requiredString(input, "kind", "http.open_stream_panel") }),
    },
    {
      module: "http", target: "none",
      tool: tool("http.open_mock_server", "Open the Mock Server panel in the visible HTTP workspace."),
      mode: "read", risk: "read",
      execute: () => dependencies.httpAction!({ action: "open-mock-server" }),
    },
  ] : [];
  return [...workspaceActions, {
    module: "http",
    target: "none",
    tool: tool("http.send_request", "Send an HTTP request through the desktop network client without local file uploads.", {
      method: { type: "string", description: "HTTP method; defaults to GET." },
      url: { type: "string", description: "Absolute HTTP or HTTPS URL." },
      headers: {
        type: "array",
        description: "Request header name/value pairs supplied directly by the model.",
        items: { type: "array", minItems: 2, maxItems: 2, items: { type: "string" } },
      },
      query: {
        type: "array",
        description: "Query parameter name/value pairs appended to the URL.",
        items: { type: "array", minItems: 2, maxItems: 2, items: { type: "string" } },
      },
      body: { type: "string", description: "Inline request body. Local file paths are not supported." },
      timeoutMs: { type: "integer", minimum: 1, maximum: 120000 },
    }, ["url"]),
    mode: "write",
    risk: "network",
    approvalPreview: ({ input }) => httpApprovalPreview(input),
    execute: async ({ input }) => {
      const result = await dependencies.invoke("http_send_request", { payload: {
        method: (stringArgument(input, "method") || "GET").toUpperCase(),
        url: requestUrl(input),
        headers: stringPairs(input.headers),
        body: typeof input.body === "string" ? input.body : "",
        timeoutMs: typeof input.timeoutMs === "number" ? input.timeoutMs : 30000,
      } });
      return safeHttpResponse(result);
    },
  }, {
    module: "http",
    target: "none",
    tool: tool("http.list_mock_servers", "List the local mock HTTP servers this app is running, with their port and rules."),
    mode: "read",
    risk: "read",
    execute: () => dependencies.invoke("mock_server_list", {}),
  }, {
    module: "http",
    target: "none",
    /**
     * 后端只监听 127.0.0.1，但仍是本机开端口 + 起进程内服务，
     * 端口和规则条数进审批文案，让用户知道它要占哪个口。
     */
    tool: tool("http.start_mock_server", "Start a local mock HTTP server on 127.0.0.1 that answers the given rules. Use port 0 to let the system pick a free port.", {
      port: { type: "integer", minimum: 0, maximum: 65535, description: "0 lets the system pick a free port." },
      rules: {
        type: "array",
        maxItems: 200,
        description: "Rules matched in order against the incoming method and path.",
        items: {
          type: "object",
          properties: {
            method: { type: "string", description: "HTTP method to match." },
            path: { type: "string", description: "Request path to match, such as /api/users." },
            status: { type: "integer", minimum: 100, maximum: 599 },
            body: { type: "string", description: "Response body returned verbatim." },
            headers: { type: "array", items: { type: "array", minItems: 2, maxItems: 2, items: { type: "string" } } },
            delayMs: { type: "integer", minimum: 0, maximum: 60000 },
          },
          required: ["method", "path", "status"],
          additionalProperties: false,
        },
      },
    }, ["rules"]),
    mode: "write",
    risk: "network",
    approvalPreview: ({ input }) => {
      const port = typeof input.port === "number" ? input.port : 0;
      const count = Array.isArray(input.rules) ? input.rules.length : 0;
      return `Start a mock HTTP server on 127.0.0.1:${port || "(auto)"} with ${count} rule(s)`;
    },
    execute: ({ input }) => dependencies.invoke("mock_server_start", {
      port: typeof input.port === "number" ? Math.max(0, Math.min(65535, Math.round(input.port))) : 0,
      rules: mockRules(input),
    }),
  }, {
    module: "http",
    target: "none",
    tool: tool("http.stop_mock_server", "Stop a local mock HTTP server.", {
      serverId: { type: "string", description: "Server id from http.list_mock_servers." },
    }, ["serverId"]),
    mode: "write",
    risk: "write",
    approvalPreview: ({ input }) => `Stop mock HTTP server ${requiredString(input, "serverId", "http.stop_mock_server")}`,
    execute: ({ input }) => dependencies.invoke("mock_server_stop", {
      serverId: requiredString(input, "serverId", "http.stop_mock_server"),
    }),
  }];
}
