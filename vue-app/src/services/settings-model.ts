import type { AiApiFormat } from "@/domain/ai/api-format";
import type { AiAnthropicAuth } from "@/domain/ai/anthropic-auth";
import type { AiReasoningEffortChoice } from "@/domain/ai/model-selection";
import type { AiRuleDefinition } from "@/domain/ai/rules";
import { DEFAULT_AI_MAX_LOOPS } from "@/domain/ai/agent-loop-limit";
import { DEFAULT_RUNTIME_PERFORMANCE_SETTINGS, type RuntimePerformanceSettings } from "@/domain/performance/runtime-limits";
import { normalizeCustomEditorTheme, type CustomEditorTheme } from "@/domain/themes/editor-theme-data";
import { EDITOR_THEME_FOLLOW } from "@/domain/themes/editor-theme-id";
import { DEFAULT_WORKSPACE_CACHE_LIMIT } from "@/domain/workspace/state-scope";
import { DEFAULT_TERMINAL_SCROLLBACK } from "@/services/terminal-preferences";
import type { UiFontFamily } from "@/services/ui-appearance";
import type { UiTheme } from "@/services/ui-theme";
import type { StoredMcpServer } from "@/domain/ai/settings-integrations";

export type AiProvider =
  | "openai"
  | "anthropic"
  | "ollama"
  | "deepseek"
  | "gemini"
  | "openrouter"
  | "groq"
  | "siliconflow"
  | "qwen"
  | "zhipu"
  | "kimi"
  | "doubao"
  | "custom";

export interface AiProfile {
  id: string;
  name: string;
  provider: AiProvider;
  baseUrl: string;
  apiKey: string;
  model: string;
  apiFormat: AiApiFormat;
  /** Credential header used when apiFormat is "anthropic". */
  anthropicAuth: AiAnthropicAuth;
  commonModels: string[];
  /** Explicit retry chain used only when an AI request fails before output. */
  failoverEnabled: boolean;
  failoverModels: string[];
  /** Empty means subagents inherit the parent assistant model. */
  subagentModel: string;
  reasoningEffort: "default" | AiReasoningEffortChoice;
  contextWindow: number;
  maxInputTokens: number;
  maxOutputTokens: number;
  openaiStreamUsage: "auto" | "always" | "never";
  /** Models that cannot take tool definitions; requests to them carry no tools. */
  noToolModels: string[];
}

export interface AiProviderPreset {
  value: AiProvider;
  label: string;
  baseUrl: string;
  model: string;
  apiFormat: AiProfile["apiFormat"];
  commonModels: string[];
  contextWindow: number;
  maxInputTokens: number;
  maxOutputTokens: number;
}
export type McpServer = StoredMcpServer;
export interface AcpAgent {
  id: string;
  name: string;
  command: string;
  args: string;
}
export interface AiRule extends AiRuleDefinition {
  id: string;
  title: string;
  content: string;
  enabled: boolean;
}
export interface VueSettings extends RuntimePerformanceSettings {
  locale: "zh" | "en";
  minimizeToTray: boolean;
  autostart: boolean;
  globalShortcutEnabled: boolean;
  uiTheme: UiTheme;
  uiScale: number;
  uiFontFamily: UiFontFamily;
  shellType: string;
  terminalFontFamily: string;
  scrollback: number;
  cursorBlink: boolean;
  webgl: boolean;
  predictiveEcho: "auto" | "always" | "off";
  terminalAttention: boolean;
  terminalAttentionSystemNotify: boolean;
  wireTransport: "auto" | "binary" | "json";
  adaptiveRender: "auto" | "quality" | "performance";
  cursorStyle: "block" | "underline" | "bar";
  terminalColorScheme: string;
  aiCommand: string;
  aiMaxLoops: number;
  activeAiProfileId: string;
  aiProfiles: AiProfile[];
  mcpServers: McpServer[];
  aiRules: AiRule[];
  acpAdapterId: string;
  acpInjectRules: boolean;
  acpAgents: AcpAgent[];
  workspaceCacheLimit: number;
  /** "follow", "custom" or a contributed editor theme ID (editorThemes point). */
  editorColorTheme: string;
  /** Colors of the "custom" editor theme. */
  editorCustomTheme: CustomEditorTheme;
}

export const providerPresets: AiProviderPreset[] = [
  {
    value: "openai",
    label: "OpenAI",
    baseUrl: "https://api.openai.com/v1",
    model: "gpt-6-astra",
    apiFormat: "openai-responses",
    commonModels: ["gpt-6-astra", "gpt-6.1-sol", "gpt-6-luna"],
    contextWindow: 1050000,
    maxInputTokens: 922000,
    maxOutputTokens: 128000,
  },
  {
    value: "anthropic",
    label: "Anthropic",
    baseUrl: "https://api.anthropic.com/v1",
    model: "claude-opus-5-5",
    apiFormat: "anthropic",
    commonModels: ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-1"],
    contextWindow: 1000000,
    maxInputTokens: 872000,
    maxOutputTokens: 128000,
  },
  {
    value: "ollama",
    label: "Ollama",
    baseUrl: "http://127.0.0.1:11434/v1",
    model: "qwen3.8:27b",
    apiFormat: "openai",
    commonModels: ["qwen3.8:27b", "gemma4:e4b", "gpt-oss:20b"],
    contextWindow: 32768,
    maxInputTokens: 28672,
    maxOutputTokens: 4096,
  },
  {
    value: "deepseek",
    label: "DeepSeek",
    baseUrl: "https://api.deepseek.com/v1",
    model: "deepseek-flash",
    apiFormat: "openai",
    commonModels: ["deepseek-flash", "deepseek-v4-pro"],
    contextWindow: 1000000,
    maxInputTokens: 900000,
    maxOutputTokens: 65536,
  },
  {
    value: "gemini",
    label: "Google Gemini",
    baseUrl: "https://generativelanguage.googleapis.com/v1beta/openai",
    model: "gemini-3.8-flash",
    apiFormat: "openai",
    commonModels: ["gemini-3.8-flash", "gemini-4-argon", "gemini-3.5-flash-lite"],
    contextWindow: 1048576,
    maxInputTokens: 1000000,
    maxOutputTokens: 65536,
  },
  {
    value: "openrouter",
    label: "OpenRouter",
    baseUrl: "https://openrouter.ai/api/v1",
    model: "anthropic/claude-sonnet-5.5",
    apiFormat: "openai",
    commonModels: ["anthropic/claude-sonnet-5.5", "openai/gpt-6.1-sol", "google/gemini-3.8-flash"],
    contextWindow: 1000000,
    maxInputTokens: 872000,
    maxOutputTokens: 128000,
  },
  {
    value: "groq",
    label: "Groq",
    baseUrl: "https://api.groq.com/openai/v1",
    model: "openai/gpt-oss-120b",
    apiFormat: "openai",
    commonModels: ["openai/gpt-oss-120b", "openai/gpt-oss-20b", "qwen/qwen3.8-27b"],
    contextWindow: 131072,
    maxInputTokens: 114688,
    maxOutputTokens: 16384,
  },
  {
    value: "siliconflow",
    label: "硅基流动",
    baseUrl: "https://api.siliconflow.cn/v1",
    model: "deepseek-ai/DeepSeek-V4.1-Flash",
    apiFormat: "openai",
    commonModels: ["deepseek-ai/DeepSeek-V4.1-Flash", "deepseek-ai/DeepSeek-V4-Pro", "Qwen/Qwen3.8-27B"],
    contextWindow: 1000000,
    maxInputTokens: 900000,
    maxOutputTokens: 65536,
  },
  {
    value: "qwen",
    label: "通义千问",
    baseUrl: "https://dashscope.aliyuncs.com/compatible-mode/v1",
    model: "qwen3.8-max",
    apiFormat: "openai",
    commonModels: ["qwen3.8-max", "qwen3.7-plus", "qwen3.8-flash"],
    contextWindow: 1000000,
    maxInputTokens: 950000,
    maxOutputTokens: 32768,
  },
  {
    value: "zhipu",
    label: "智谱 GLM",
    baseUrl: "https://open.bigmodel.cn/api/paas/v4",
    model: "glm-5.3",
    apiFormat: "openai",
    commonModels: ["glm-5.3", "glm-5-turbo"],
    contextWindow: 1000000,
    maxInputTokens: 872000,
    maxOutputTokens: 128000,
  },
  {
    value: "kimi",
    label: "Kimi",
    baseUrl: "https://api.moonshot.cn/v1",
    model: "kimi-k3",
    apiFormat: "openai",
    commonModels: ["kimi-k3", "kimi-k2.7-code", "kimi-k2.6"],
    contextWindow: 1048576,
    maxInputTokens: 917504,
    maxOutputTokens: 131072,
  },
  {
    value: "doubao",
    label: "豆包",
    baseUrl: "https://ark.cn-beijing.volces.com/api/v3",
    model: "doubao-seed-2-1-pro-260915",
    apiFormat: "openai",
    commonModels: ["doubao-seed-2-1-pro-260915", "doubao-seed-2-1-lite-260915", "doubao-seed-evolving"],
    contextWindow: 256000,
    maxInputTokens: 240000,
    maxOutputTokens: 16384,
  },
  {
    value: "custom",
    label: "",
    baseUrl: "",
    model: "",
    apiFormat: "openai",
    commonModels: [],
    contextWindow: 128000,
    maxInputTokens: 120000,
    maxOutputTokens: 8192,
  },
];

export const defaultProviderPreset = providerPresets[0]!;
export const defaultProfile: AiProfile = {
  id: "default",
  name: "Default",
  provider: defaultProviderPreset.value,
  baseUrl: defaultProviderPreset.baseUrl,
  apiKey: "",
  model: defaultProviderPreset.model,
  apiFormat: defaultProviderPreset.apiFormat,
  anthropicAuth: "x-api-key",
  commonModels: [...defaultProviderPreset.commonModels],
  failoverEnabled: false,
  failoverModels: [],
  subagentModel: "",
  reasoningEffort: "default",
  contextWindow: defaultProviderPreset.contextWindow,
  maxInputTokens: defaultProviderPreset.maxInputTokens,
  maxOutputTokens: defaultProviderPreset.maxOutputTokens,
  openaiStreamUsage: "auto",
  noToolModels: [],
};
export const defaultSettings: VueSettings = {
  locale: "zh",
  minimizeToTray: true,
  autostart: false,
  globalShortcutEnabled: false,
  uiTheme: "light",
  uiScale: 100,
  uiFontFamily: "system",
  shellType: "system",
  terminalFontFamily: "Menlo, Monaco, monospace",
  scrollback: DEFAULT_TERMINAL_SCROLLBACK,
  cursorBlink: true,
  webgl: true,
  predictiveEcho: "auto",
  terminalAttention: true,
  terminalAttentionSystemNotify: true,
  wireTransport: "auto",
  adaptiveRender: "auto",
  cursorStyle: "block",
  terminalColorScheme: "default",
  aiCommand: "claude",
  aiMaxLoops: DEFAULT_AI_MAX_LOOPS,
  activeAiProfileId: defaultProfile.id,
  aiProfiles: [defaultProfile],
  mcpServers: [],
  aiRules: [],
  acpAdapterId: "claude-code",
  acpInjectRules: true,
  acpAgents: [],
  workspaceCacheLimit: DEFAULT_WORKSPACE_CACHE_LIMIT,
  editorColorTheme: EDITOR_THEME_FOLLOW,
  editorCustomTheme: normalizeCustomEditorTheme(undefined),
  ...DEFAULT_RUNTIME_PERFORMANCE_SETTINGS,
};
