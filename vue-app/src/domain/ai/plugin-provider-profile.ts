/**
 * Pure helpers that turn a plugin AI provider preset (contributes.aiProviders,
 * domain/plugins/ai-providers.ts) into an ordinary saved AI profile, shared by
 * Settings → AI → Connection and the AI request builders. A profile made from
 * a preset stores `provider: "plugin"` plus its source and is the user's own
 * configuration: nothing here changes it when the plugin goes away.
 */
import type { AiAnthropicAuth } from "@/domain/ai/anthropic-auth";
import type { AiApiFormat } from "@/domain/ai/api-format";
import type { PluginAiProvider } from "@/domain/plugins/ai-providers";

export interface AiProfileSource {
  pluginId: string;
  providerId: string;
}

export const PLUGIN_PROVIDER_VALUE_PREFIX = "plugin:";

const PLACEHOLDER_PATTERN = /\{([a-z_]{1,24})\}/g;
/** A placeholder value is one path segment, so it cannot change the request host. */
const PLACEHOLDER_VALUE_PATTERN = /^[A-Za-z0-9_-]{1,128}$/;

/** The provider select value: built-in providers keep theirs, presets use "plugin:<providerId>". */
export function aiProviderSelectValue(profile: { provider: string; source?: AiProfileSource }): string {
  return profile.provider === "plugin" && profile.source
    ? `${PLUGIN_PROVIDER_VALUE_PREFIX}${profile.source.providerId}`
    : profile.provider;
}

export interface AiProviderSelectOption {
  value: string;
  kind: "builtin" | "plugin" | "orphan";
  provider?: PluginAiProvider;
}

/**
 * Built-in providers (custom last), with plugin presets before "custom". A
 * profile whose preset is no longer contributed gets an "orphan" option so
 * the select shows a readable label instead of a raw value.
 */
export function aiProviderSelectOptions(
  builtinValues: readonly string[],
  providers: readonly PluginAiProvider[],
  current: { provider: string; source?: AiProfileSource },
): AiProviderSelectOption[] {
  const builtin = (value: string): AiProviderSelectOption => ({ value, kind: "builtin" });
  const options = [
    ...builtinValues.filter((value) => value !== "custom").map(builtin),
    ...providers.map((provider): AiProviderSelectOption => ({
      value: `${PLUGIN_PROVIDER_VALUE_PREFIX}${provider.id}`,
      kind: "plugin",
      provider,
    })),
    ...builtinValues.filter((value) => value === "custom").map(builtin),
  ];
  const selected = aiProviderSelectValue(current);
  if (selected.startsWith(PLUGIN_PROVIDER_VALUE_PREFIX) && !options.some((option) => option.value === selected)) {
    options.push({ value: selected, kind: "orphan" });
  }
  return options;
}

/** Profile fields a chosen preset writes; the key and failover chain start empty like a built-in preset. */
export function pluginProviderProfilePatch(provider: PluginAiProvider): {
  provider: "plugin";
  source: AiProfileSource;
  baseUrl: string;
  apiKey: "";
  model: string;
  apiFormat: AiApiFormat;
  anthropicAuth?: AiAnthropicAuth;
  commonModels: string[];
  noToolModels: string[];
  failoverEnabled: false;
  failoverModels: string[];
  subagentModel: "";
  contextWindow: number;
  maxInputTokens: number;
  maxOutputTokens: number;
} {
  return {
    provider: "plugin",
    source: { pluginId: provider.pluginId, providerId: provider.id },
    baseUrl: provider.baseUrl,
    apiKey: "",
    model: provider.model,
    apiFormat: provider.apiFormat,
    ...(provider.anthropicAuth ? { anthropicAuth: provider.anthropicAuth } : {}),
    commonModels: provider.models.map((model) => model.id),
    noToolModels: provider.models.filter((model) => !model.tools).map((model) => model.id),
    failoverEnabled: false,
    failoverModels: [],
    subagentModel: "",
    contextWindow: provider.contextWindow,
    maxInputTokens: provider.maxInputTokens,
    maxOutputTokens: provider.maxOutputTokens,
  };
}

/** The {name} placeholders still present in a base URL, in order. */
export function unresolvedBaseUrlPlaceholders(baseUrl: string): string[] {
  return [...new Set([...baseUrl.matchAll(PLACEHOLDER_PATTERN)].map((match) => match[1]!))];
}

/** Fills one placeholder; undefined when the value is not a single safe path segment or the name is absent. */
export function fillAiProviderPlaceholder(baseUrl: string, name: string, value: string): string | undefined {
  const token = `{${name}}`;
  if (!PLACEHOLDER_VALUE_PATTERN.test(value) || !baseUrl.includes(token)) return undefined;
  return baseUrl.split(token).join(value);
}

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
