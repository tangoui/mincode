import { sanitizeAiResult } from "@/services/ai-tools/shared";

const MAX_SUMMARY_LENGTH = 800;
const MAX_DEPTH = 4;
const MAX_ARRAY_ITEMS = 8;
const MAX_OBJECT_KEYS = 16;
const MAX_STRING_LENGTH = 160;

const SENSITIVE_KEYS = new Set([
  "authorization",
  "cookie",
  "token",
  "password",
  "secret",
  "key",
  "passphrase",
  "headers",
  "body",
  "content",
  "data",
]);

function isSensitiveKey(key: string) {
  const normalized = key.toLowerCase().replace(/[^a-z0-9]/g, "");
  return SENSITIVE_KEYS.has(normalized)
    || normalized.endsWith("token")
    || normalized.endsWith("password")
    || normalized.endsWith("secret")
    || normalized.endsWith("passphrase")
    || normalized.endsWith("apikey")
    || normalized.endsWith("privatekey");
}

function truncate(value: string, limit: number) {
  return value.length <= limit ? value : `${value.slice(0, Math.max(0, limit - 1))}…`;
}

function summarizeValue(value: unknown, depth: number, seen: WeakSet<object>): unknown {
  if (value === null || typeof value === "boolean" || typeof value === "number") return value;
  if (typeof value === "string") {
    const sanitized = String(sanitizeAiResult(value));
    return truncate(sanitized === "[REDACTED CIRCULAR]" ? "[Circular]" : sanitized, MAX_STRING_LENGTH);
  }
  if (typeof value === "bigint") return value.toString();
  if (typeof value !== "object") return undefined;
  if (seen.has(value)) return "[Circular]";
  if (depth >= MAX_DEPTH) return "[Truncated]";
  seen.add(value);
  try {
    if (Array.isArray(value)) {
      const summarized = value.slice(0, MAX_ARRAY_ITEMS)
        .map((item) => summarizeValue(item, depth + 1, seen))
        .filter((item) => item !== undefined);
      if (value.length > MAX_ARRAY_ITEMS) summarized.push(`[+${value.length - MAX_ARRAY_ITEMS} items]`);
      return summarized;
    }
    const result: Record<string, unknown> = {};
    const entries = Object.entries(value).slice(0, MAX_OBJECT_KEYS);
    for (const [key, item] of entries) {
      if (isSensitiveKey(key)) {
        result[key] = "[REDACTED]";
        continue;
      }
      const summarized = summarizeValue(item, depth + 1, seen);
      if (summarized !== undefined) result[key] = summarized;
    }
    if (Object.keys(value).length > MAX_OBJECT_KEYS) result["…"] = "[Truncated]";
    return result;
  } finally {
    seen.delete(value);
  }
}

export function summarizeToolApprovalArguments(value: unknown): string | undefined {
  const summarized = summarizeValue(sanitizeAiResult(value), 0, new WeakSet());
  if (summarized === undefined) return undefined;
  try {
    return truncate(JSON.stringify(summarized), MAX_SUMMARY_LENGTH);
  } catch {
    return undefined;
  }
}
