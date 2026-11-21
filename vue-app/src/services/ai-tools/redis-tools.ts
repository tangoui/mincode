import type { AiCapability, AiCapabilityRisk } from "@/domain/ai/capability-registry";
import type { AiResource } from "@/domain/ai/resource-registry";
import { requiredString, resourceCapability, sanitizeAiResult, stringArgument, tool, type AiToolDependencies } from "./shared";
import { createAiSqlSafety, type AiSqlSafety, type AiSqlSafetyRunner } from "./sql-safety";

export interface RedisToolDependencies extends AiToolDependencies {
  connectDatabase?(connectionId: string): Promise<unknown>;
  disconnectDatabase?(connectionId: string): Promise<unknown>;
  redisAction?(input: Record<string, unknown>): Promise<unknown>;
  /** SQL 安全网（必需）：改键命令经它取令牌，见 ./sql-safety。 */
  sqlSafety: AiSqlSafety;
}

type RedisWriteCommand = "redis_set_key" | "redis_add_key" | "redis_delete_key" | "redis_set_ttl";
const redisWriteActions: Record<RedisWriteCommand, string> = {
  redis_set_key: "set", redis_add_key: "add", redis_delete_key: "delete", redis_set_ttl: "ttl",
};

/**
 * Redis 单键写入经 SQL 安全网（operation kind "redis"）：单键为低风险，沿用工具审批；高风险时
 * 无论审批模式都由用户确认。
 */
function writeRedisKey(
  dependencies: RedisToolDependencies,
  safety: AiSqlSafetyRunner,
  command: RedisWriteCommand,
  args: { sessionId: string; dbIndex: number; key: string } & Record<string, unknown>,
) {
  return safety.run({
    sessionId: args.sessionId,
    operation: { kind: "redis", action: redisWriteActions[command], dbIndex: args.dbIndex, keys: [args.key] },
    execute: (token) => dependencies.invoke(command, { ...args, ...safety.tokenArgs(token) }),
  });
}

export const redisSavedCapabilityNames = ["redis.connect", "redis.open_key", "redis.format_open_json"];
export const redisSessionCapabilityNames = [
  "redis.disconnect", "redis.scan_keys", "redis.type", "redis.ttl", "redis.get",
  "redis.get_hash", "redis.get_list", "redis.get_set", "redis.get_zset", "redis.get_stream",
  "redis.add", "redis.set", "redis.delete", "redis.expire", "redis.persist", "redis.set_hash",
  "redis.set_list", "redis.set_set", "redis.set_zset", "redis.set_stream",
];

function metadataString(resource: AiResource, key: "connectionId" | "sessionId") {
  const value = resource.metadata?.[key];
  if (typeof value !== "string" || !value) throw new Error(`Redis resource has no bound ${key}`);
  return value;
}

function dbIndex(input: Record<string, unknown>) {
  return typeof input.dbIndex === "number" && Number.isInteger(input.dbIndex) && input.dbIndex >= 0
    ? input.dbIndex
    : 0;
}

function keyArgs(input: Record<string, unknown>, resource: AiResource, capability: string) {
  return {
    sessionId: metadataString(resource, "sessionId"),
    dbIndex: dbIndex(input),
    key: requiredString(input, "key", capability),
  };
}

function keyProperties(extra: Record<string, unknown> = {}) {
  return { dbIndex: { type: "integer", minimum: 0 }, key: { type: "string" }, ...extra };
}

function expirySeconds(input: Record<string, unknown>) {
  const value = input.ttlSeconds;
  if (typeof value !== "number" || !Number.isInteger(value) || value < 1) {
    throw new Error("redis.expire requires ttlSeconds to be an integer greater than or equal to 1");
  }
  return value;
}

function typedResult(result: unknown, expectedType: string, capability: string) {
  if (!result || typeof result !== "object") return result;
  const record = result as Record<string, unknown>;
  if (record.type !== expectedType) {
    throw new Error(`${capability} expected Redis ${expectedType}, got ${String(record.type)}`);
  }
  return record;
}

export function redisCapabilities(dependencies: RedisToolDependencies): AiCapability[] {
  const workspaceActions: AiCapability[] = dependencies.redisAction ? [
    {
      module: "redis", target: "none", discovery: true,
      tool: tool("redis.open_key", "Connect a saved Redis connection when needed, select its database, and open a key in the visible Redis workspace.", { connectionId: { type: "string" }, key: { type: "string" }, dbIndex: { type: "integer", minimum: 0 } }, ["connectionId", "key"]),
      mode: "write", risk: "network",
      approvalPreview: ({ input }) => `Open Redis key ${requiredString(input, "key", "redis.open_key")}`,
      execute: ({ input }) => dependencies.redisAction!({ action: "open-key", connectionId: requiredString(input, "connectionId", "redis.open_key"), key: requiredString(input, "key", "redis.open_key"), dbIndex: dbIndex(input) }),
    },
    {
      module: "redis", target: "none",
      tool: tool("redis.format_open_json", "Format the currently open Redis JSON object or array in the visible editor without saving it."),
      mode: "write", risk: "write",
      approvalPreview: () => "格式化当前未保存的 Redis JSON 编辑内容",
      execute: () => dependencies.redisAction!({ action: "format-json" }),
    },
  ] : [];
  const redis = resourceCapability;
  const safety = createAiSqlSafety(dependencies.invoke, dependencies.sqlSafety);
  const getKey = (name: string, expectedType?: string) => redis({
    module: "redis",
    tool: tool(name, `Read a Redis ${expectedType ?? "key"} from the bound active session.`, keyProperties(), ["key"]),
    mode: "read",
    risk: "read",
    execute: async ({ input, resource }) => {
      const result = sanitizeAiResult(await dependencies.invoke("redis_get_key", keyArgs(input, resource, name)));
      return expectedType ? typedResult(result, expectedType, name) : result;
    },
  });
  const setKey = (name: string, keyType: string, risk: AiCapabilityRisk = "dangerous") => redis({
    module: "redis",
    tool: tool(name, `Write a Redis ${keyType} through the bound active session.`, keyProperties({
      value: {},
    }), ["key", "value"]),
    mode: "write",
    risk,
    approvalPreview: ({ input }) => `${requiredString(input, "key", name)} (value redacted)`,
    execute: ({ input, resource }) => writeRedisKey(dependencies, safety, "redis_set_key", {
      ...keyArgs(input, resource, name), keyType, value: input.value,
    }),
  });

  return [...workspaceActions,
    redis({ module: "redis", tool: tool("redis.connect", "Connect the bound saved Redis connection without exposing credentials."),
      mode: "write", risk: "network", execute: ({ resource }) => dependencies.connectDatabase?.(metadataString(resource, "connectionId")) }),
    redis({ module: "redis", tool: tool("redis.disconnect", "Disconnect the bound active Redis session."),
      mode: "write", risk: "write", execute: ({ resource }) => dependencies.disconnectDatabase?.(metadataString(resource, "connectionId")) }),
    redis({ module: "redis", tool: tool("redis.scan_keys", "Scan Redis keys in the bound active session.", {
      dbIndex: { type: "integer", minimum: 0 }, pattern: { type: "string" }, cursor: { type: "string" },
      count: { type: "integer", minimum: 1, maximum: 1000 },
    }), mode: "read", risk: "read", execute: async ({ input, resource }) => sanitizeAiResult(await dependencies.invoke("redis_list_keys", {
      sessionId: metadataString(resource, "sessionId"), dbIndex: dbIndex(input),
      pattern: stringArgument(input, "pattern") || "*", cursor: stringArgument(input, "cursor") || "0",
      count: typeof input.count === "number" ? Math.max(1, Math.min(1000, Math.round(input.count))) : 100,
    })) }),
    ...(["type", "ttl"] as const).map((field) => redis({
      module: "redis", tool: tool(`redis.${field}`, `Read Redis ${field.toUpperCase()}.`, keyProperties(), ["key"]),
      mode: "read", risk: "read", execute: async ({ input, resource }) => {
        if (field === "ttl") return sanitizeAiResult(await dependencies.invoke("redis_get_ttl", keyArgs(input, resource, `redis.${field}`)));
        const result = sanitizeAiResult(await dependencies.invoke<Record<string, unknown>>("redis_get_key", keyArgs(input, resource, `redis.${field}`))) as Record<string, unknown>;
        return { type: result.type };
      },
    })),
    getKey("redis.get", "string"), getKey("redis.get_hash", "hash"), getKey("redis.get_list", "list"),
    getKey("redis.get_set", "set"), getKey("redis.get_zset", "zset"), getKey("redis.get_stream", "stream"),
    redis({ module: "redis", tool: tool("redis.add", "Add a Redis key through the bound active session.", keyProperties({
      keyType: { type: "string", enum: ["string", "hash", "list", "set", "zset", "stream"] }, value: {},
    }), ["key", "keyType", "value"]), mode: "write", risk: "dangerous",
      approvalPreview: ({ input }) => `${requiredString(input, "key", "redis.add")} (value redacted)`,
      execute: ({ input, resource }) => writeRedisKey(dependencies, safety, "redis_add_key", {
        ...keyArgs(input, resource, "redis.add"),
        keyType: requiredString(input, "keyType", "redis.add"),
        value: input.value,
      }) }),
    setKey("redis.set", "string"),
    redis({ module: "redis", tool: tool("redis.delete", "Delete a Redis key.", keyProperties(), ["key"]),
      mode: "write", risk: "dangerous", approvalPreview: ({ input }) => requiredString(input, "key", "redis.delete"),
      execute: ({ input, resource }) => writeRedisKey(dependencies, safety, "redis_delete_key", keyArgs(input, resource, "redis.delete")) }),
    redis({ module: "redis", tool: tool("redis.expire", "Set expiry seconds on a Redis key.", keyProperties({ ttlSeconds: { type: "integer", minimum: 1 } }), ["key", "ttlSeconds"]),
      mode: "write", risk: "dangerous", execute: async ({ input, resource }) => {
        const ttlSeconds = expirySeconds(input);
        return writeRedisKey(dependencies, safety, "redis_set_ttl", {
          ...keyArgs(input, resource, "redis.expire"),
          ttlSeconds,
        });
      } }),
    redis({ module: "redis", tool: tool("redis.persist", "Remove expiry from a Redis key.", keyProperties(), ["key"]),
      mode: "write", risk: "dangerous", execute: ({ input, resource }) => writeRedisKey(dependencies, safety, "redis_set_ttl", {
        ...keyArgs(input, resource, "redis.persist"), ttlSeconds: -1,
      }) }),
    setKey("redis.set_hash", "hash"), setKey("redis.set_list", "list"), setKey("redis.set_set", "set"),
    setKey("redis.set_zset", "zset"), setKey("redis.set_stream", "stream"),
  ];
}
