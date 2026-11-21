import type { AiCapability } from "@/domain/ai/capability-registry";
import type { AiResource } from "@/domain/ai/resource-registry";

export interface AiToolDependencies {
  invoke<T>(command: string, args: Record<string, unknown>, options?: { signal?: AbortSignal }): Promise<T>;
  stageWorkspaceEdit?(input: {
    path: string;
    projectRoot: string;
    before: string | null;
    after: string;
  }): unknown;
  openBrowserTab?(input: { url?: string }): Promise<unknown>;
  /** The browser workspace's shared tabs (browser.list_tabs / browser.switch_tab run locally). */
  browserTabs?: BrowserTabsDependency;
}

export interface BrowserTabsDependency {
  listSharedTabs(): {
    tabs: Array<{ tabId: string; title: string; url: string; foreground: boolean; target: boolean; openerTabId?: string }>;
    unsharedCount: number;
  };
  switchAiTarget(tabId: string): Promise<unknown>;
}

export function stringArgument(args: Record<string, unknown>, key: string) {
  return typeof args[key] === "string" ? args[key].trim() : "";
}

export function requiredString(input: Record<string, unknown>, key: string, capability: string) {
  const value = stringArgument(input, key);
  if (!value) throw new Error(`${capability} requires ${key}`);
  return value;
}

const sensitiveDataNamePattern = /(?:^|[^a-z0-9])(password|passwd|pwd|token|secret|api[_-]?key|private[_-]?key|passphrase|credential|authorization|cookie|session[_-]?id)(?:$|[^a-z0-9])/i;

export function isSensitiveDataName(value: string) {
  return sensitiveDataNamePattern.test(value.replace(/([a-z0-9])([A-Z])/g, "$1_$2"));
}

function redactSensitiveText(value: string) {
  const redactedUrls = value.replace(/https?:\/\/[^\s"'<>]+/gi, (candidate) => sanitizeUrlForAi(candidate));
  return redactedUrls
    .replace(/((?:password|passwd|pwd|token|secret|api[_-]?key|private[_-]?key|passphrase|credential|authorization|cookie|session[_-]?id)["']?\s*[=:]\s*)(["']?)[^\s,'";}]+\2/gi, "$1[REDACTED]")
    .replace(/\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+/gi, "$1 [REDACTED]")
    .replace(/-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?-----END [^-]*PRIVATE KEY-----/gi, "[REDACTED PRIVATE KEY]");
}

export function sanitizeUrlForAi(rawUrl: string) {
  try {
    const url = new URL(rawUrl);
    if (url.username) url.username = "[REDACTED]";
    if (url.password) url.password = "[REDACTED]";
    for (const key of [...url.searchParams.keys()]) {
      if (isSensitiveDataName(key)) url.searchParams.set(key, "[REDACTED]");
    }
    return url.toString();
  } catch {
    return rawUrl
      .replace(/\/\/[^/@\s]+@/g, "//[REDACTED]@")
      .replace(/([?&](?:password|passwd|pwd|token|secret|api[_-]?key|private[_-]?key|passphrase|credential|authorization|cookie|session[_-]?id)=)[^&#\s]*/gi, "$1[REDACTED]");
  }
}

export function sanitizeAiResult(value: unknown, fieldName = "", seen = new WeakSet<object>()): unknown {
  if (isSensitiveDataName(fieldName)) return "[REDACTED]";
  if (typeof value === "string") return redactSensitiveText(value);
  if (value === null || typeof value !== "object") return value;
  if (seen.has(value)) return "[REDACTED CIRCULAR]";
  seen.add(value);
  try {
    if (Array.isArray(value)) {
      return value.map((item) => sanitizeAiResult(item, fieldName, seen));
    }
    const record = value as Record<string, unknown>;
    const columns = Array.isArray(record.columns) && record.columns.every((column) => typeof column === "string")
      ? record.columns as string[]
      : undefined;
    const result: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(record)) {
      if (key === "rows" && columns && Array.isArray(item)) {
        result[key] = item.map((row) => Array.isArray(row)
          ? row.map((cell, index) => sanitizeAiResult(cell, columns[index] ?? "", seen))
          : sanitizeAiResult(row, key, seen));
      } else {
        result[key] = sanitizeAiResult(item, key, seen);
      }
    }
    return result;
  } finally {
    seen.delete(value);
  }
}

export function safeAiText(value: unknown, maximumLength = 4_000) {
  const sanitized = sanitizeAiResult(value);
  const text = typeof sanitized === "string" ? sanitized : JSON.stringify(sanitized);
  if (!text) return "";
  return text.length <= maximumLength ? text : `${text.slice(0, maximumLength)}…`;
}

export function relativePath(input: Record<string, unknown>, key: string, capability: string) {
  const value = requiredString(input, key, capability).replace(/\\/g, "/");
  if (value.startsWith("/") || /^[A-Za-z]:\//.test(value)) {
    throw new Error(`${capability} requires a project-relative ${key}`);
  }
  const segments = value.split("/");
  if (segments.some((segment) => segment === "..")) {
    throw new Error(`${capability} ${key} escapes the project root`);
  }
  return segments.filter((segment) => segment && segment !== ".").join("/");
}

export function optionalRelativePath(input: Record<string, unknown>, key: string, capability: string) {
  return stringArgument(input, key) ? relativePath(input, key, capability) : null;
}

export function resourceMetadataPath(resource: AiResource, key: "projectRoot" | "repoPath") {
  const value = resource.metadata?.[key];
  if (typeof value !== "string" || !value) throw new Error(`Resource has no bound ${key}`);
  return value;
}

export function tool(
  name: string,
  description: string,
  properties: Record<string, unknown> = {},
  required: string[] = []
) {
  return {
    name,
    description,
    inputSchema: {
      type: "object",
      properties,
      ...(required.length ? { required } : {}),
      additionalProperties: false,
    },
  };
}

export function resourceCapability(capability: Omit<AiCapability, "target">): AiCapability {
  return { ...capability, target: "resource" };
}
