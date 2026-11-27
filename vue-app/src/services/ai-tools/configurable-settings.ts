import { MAX_AI_MAX_LOOPS, MIN_AI_MAX_LOOPS } from "@/domain/ai/agent-loop-limit";
import { AI_APPROVAL_MODES, DEFAULT_AI_APPROVAL_MODE } from "@/domain/ai/approval-policy";
import { normalizeKeybindingOverride, MAX_KEYBINDING_OVERRIDES } from "@/domain/keybindings/persistence";
import { listKeymapPresets, MINCODE_PRESET_ID } from "@/domain/keybindings/preset";
import { RUNTIME_PERFORMANCE_LIMITS } from "@/domain/performance/runtime-limits";
import { DEFAULT_WORKSPACE_STATE_ISOLATION_ENABLED, MAX_WORKSPACE_CACHE_LIMIT, MIN_WORKSPACE_CACHE_LIMIT } from "@/domain/workspace/state-scope";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { defaultProfile, defaultSettings, providerPresets, type VueSettings } from "@/services/settings-model";
import { createDefaultSidebarVisibility, sidebarItemIds } from "@/services/sidebar-preferences";
import { isBuiltInTerminalColorScheme } from "@/services/terminal-theme";
import { MAX_UI_SCALE, MIN_UI_SCALE, UI_SCALE_STEP } from "@/services/ui-appearance";
import { listBuiltinUiThemes } from "@/services/ui-theme";
import { isSensitiveDataName, sanitizeAiResult } from "./shared";

const { t } = useVueI18n();
export const agentTrustSettingKey = "agentControl.trusted";
export const activeApiKeySetting = "ai.apiKey";
const hidden = "[REDACTED]";

type SettingType = "boolean" | "number" | "string" | "array" | "object";
type Group = "appearance" | "general" | "terminal" | "editor" | "ai" | "shortcuts" | "updates" | "external-mcp";
type Definition = {
  group: Group;
  label: MessageKey;
  type?: SettingType;
  allowed?: readonly (string | boolean)[];
  options?: () => readonly string[];
  range?: { min: number; max: number; step?: number };
  validate?: (value: unknown) => boolean;
  secret?: boolean;
};
type Setting = Definition & { key: string; initial: unknown };

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function namedRecords(value: unknown) {
  return Array.isArray(value) && value.every((item) => record(item)
    && typeof item.id === "string" && Boolean(item.id.trim()))
    && new Set(value.map((item) => item.id)).size === value.length;
}
function stringArray(value: unknown) {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}
function profileList(value: unknown) {
  return namedRecords(value) && Array.isArray(value) && value.length > 0 && value.every((item) => record(item)
    && Object.entries(item).every(([key, field]) => {
      if (!Object.prototype.hasOwnProperty.call(defaultProfile, key)) return false;
      const initial = defaultProfile[key as keyof typeof defaultProfile];
      return Array.isArray(initial) ? stringArray(field)
        : typeof initial === "number" ? typeof field === "number" && Number.isSafeInteger(field) && field > 0
          : typeof field === typeof initial;
    })
    && (item.provider === undefined || providerPresets.some((preset) => preset.value === item.provider))
    && (item.apiFormat === undefined || ["openai", "openai-responses", "anthropic"].includes(String(item.apiFormat)))
    && (item.anthropicAuth === undefined || item.anthropicAuth === "x-api-key" || item.anthropicAuth === "bearer")
    && (item.openaiStreamUsage === undefined || ["auto", "always", "never"].includes(String(item.openaiStreamUsage))));
}
function stringRecord(value: unknown) {
  return record(value) && Object.values(value).every((item) => typeof item === "string");
}
function mcpList(value: unknown) {
  return namedRecords(value) && Array.isArray(value) && value.every((item) => record(item)
    && (item.transport === "stdio" || item.transport === "http")
    && ["command", "args", "url"].every((key) => typeof item[key] === "string")
    && typeof item.enabled === "boolean"
    && stringRecord(item.env) && stringRecord(item.headers)
    && (item.auth === undefined || item.auth === "oauth"));
}
function ruleList(value: unknown) {
  return namedRecords(value) && Array.isArray(value) && value.every((item) => record(item)
    && typeof item.title === "string" && typeof item.content === "string" && typeof item.enabled === "boolean"
    && (item.scope === undefined || item.scope === "global"
      || (item.scope === "workspace" && typeof item.workspaceId === "string" && Boolean(item.workspaceId.trim()))));
}

const snapshotDefinitions = {
  locale: { group: "appearance", label: "settings.language", allowed: ["zh", "en"] },
  minimizeToTray: { group: "general", label: "settings.minimizeToTray" },
  autostart: { group: "general", label: "settings.autostart" },
  globalShortcutEnabled: { group: "shortcuts", label: "settings.globalShortcut" },
  uiTheme: { group: "appearance", label: "settings.theme", options: listBuiltinUiThemes },
  uiScale: { group: "appearance", label: "settings.uiScale", range: { min: MIN_UI_SCALE, max: MAX_UI_SCALE, step: UI_SCALE_STEP } },
  uiFontFamily: { group: "appearance", label: "settings.uiFont", allowed: ["system", "sans", "mono"] },
  shellType: { group: "terminal", label: "settings.defaultShell", allowed: ["system", "zsh", "bash", "fish", "pwsh"] },
  terminalFontFamily: { group: "terminal", label: "settings.terminalFont" },
  scrollback: { group: "terminal", label: "settings.scrollback", range: { min: 1000, max: 100000, step: 1000 } },
  cursorBlink: { group: "terminal", label: "settings.cursorBlink" },
  cursorStyle: { group: "terminal", label: "settings.cursorStyle", allowed: ["block", "underline", "bar"] },
  webgl: { group: "terminal", label: "settings.webgl" },
  predictiveEcho: { group: "terminal", label: "settings.predictiveEcho", allowed: ["auto", "always", "off"] },
  terminalAttention: { group: "terminal", label: "settings.terminalAttention" },
  terminalAttentionSystemNotify: { group: "terminal", label: "settings.terminalAttentionSystemNotify" },
  wireTransport: { group: "terminal", label: "settings.wireTransport", allowed: ["auto", "binary", "json"] },
  adaptiveRender: { group: "terminal", label: "settings.adaptiveRender", allowed: ["auto", "quality", "performance"] },
  terminalColorScheme: { group: "terminal", label: "settings.tools.terminalColorScheme", validate: (value) => isBuiltInTerminalColorScheme(value) },
  aiCommand: { group: "terminal", label: "settings.ai.defaultCommand", allowed: ["claude", "codex", "gemini"] },
  aiMaxLoops: { group: "ai", label: "settings.ai.maxLoops", range: { min: MIN_AI_MAX_LOOPS, max: MAX_AI_MAX_LOOPS } },
  activeAiProfileId: { group: "ai", label: "settings.ai.connection" },
  aiProfiles: { group: "ai", label: "settings.ai.connection", validate: profileList },
  mcpServers: { group: "ai", label: "settings.ai.mcp", validate: mcpList },
  aiRules: { group: "ai", label: "settings.ai.rules", validate: ruleList },
  acpAdapterId: { group: "ai", label: "settings.ai.localAgent" },
  acpInjectRules: { group: "ai", label: "settings.tools.acpInjectRules" },
  acpAgents: { group: "ai", label: "settings.ai.localAgent", validate: (value) => namedRecords(value) && Array.isArray(value) && value.every((item) => record(item) && ["name", "command", "args"].every((key) => typeof item[key] === "string")) },
  workspaceCacheLimit: { group: "general", label: "settings.workspaceCacheLimit", range: { min: MIN_WORKSPACE_CACHE_LIMIT, max: MAX_WORKSPACE_CACHE_LIMIT } },
  editorColorTheme: { group: "editor", label: "settings.editorTheme", validate: (value) => value === "follow" || value === "custom" },
  editorCustomTheme: { group: "editor", label: "settings.editorTheme.custom" },
  performanceProfile: { group: "general", label: "settings.performanceProfile", allowed: ["low-memory", "balanced", "high-performance", "custom"] },
  aiInactiveWorkspaceCacheCount: { group: "general", label: "settings.aiInactiveCacheCount", range: RUNTIME_PERFORMANCE_LIMITS.aiInactiveWorkspaceCacheCount },
  aiInactiveWorkspaceCacheMiB: { group: "general", label: "settings.aiInactiveCacheSize", range: RUNTIME_PERFORMANCE_LIMITS.aiInactiveWorkspaceCacheMiB },
  terminalLayoutCacheLimit: { group: "general", label: "settings.terminalLayoutCache", range: RUNTIME_PERFORMANCE_LIMITS.terminalLayoutCacheLimit },
} satisfies Partial<Record<keyof VueSettings, Definition>>;

const liveDefinitions = {
  [activeApiKeySetting]: { group: "ai", label: "settings.ai.connection", initial: "", secret: true },
  [agentTrustSettingKey]: { group: "external-mcp", label: "settings.agentControl.trusted", initial: false },
  "updates.autoCheck": { group: "updates", label: "settings.update.autoCheck", initial: true },
  "updates.skippedVersion": { group: "updates", label: "settings.tools.skippedVersion", initial: "" },
  "shortcuts.preset": { group: "shortcuts", label: "settings.keybindings.preset", initial: MINCODE_PRESET_ID, options: () => listKeymapPresets().map((preset) => preset.id) },
  "shortcuts.overrides": { group: "shortcuts", label: "settings.keybindings.hint", initial: [], validate: (value) => Array.isArray(value) && value.length <= MAX_KEYBINDING_OVERRIDES && value.every((item) => normalizeKeybindingOverride(item) !== null) },
  "shortcuts.doubleModifier": { group: "shortcuts", label: "settings.keybindings.option.doubleModifier", type: "boolean", initial: undefined },
  "shortcuts.sendKeybindingsToShell": { group: "shortcuts", label: "settings.keybindings.option.sendToShell", initial: false },
  "ai.approvalMode": { group: "ai", label: "settings.ai.permissionsTitle", initial: DEFAULT_AI_APPROVAL_MODE, allowed: AI_APPROVAL_MODES },
  "ai.conversationScope": { group: "ai", label: "settings.tools.conversationScope", initial: "global", allowed: ["global", "workspace"] },
  "ai.conversationSidebarPosition": { group: "ai", label: "settings.tools.conversationSidebar", initial: "left", allowed: ["left", "right"] },
  "workspace.isolation": { group: "general", label: "settings.tools.workspaceIsolation", initial: DEFAULT_WORKSPACE_STATE_ISOLATION_ENABLED },
  "sidebar.visibility": { group: "appearance", label: "settings.tools.sidebarVisibility", initial: createDefaultSidebarVisibility(),
    validate: (value) => record(value) && Object.entries(value).every(([id, visible]) =>
      sidebarItemIds.some((item) => item === id) && typeof visible === "boolean") },
} satisfies Record<string, Definition & { initial: unknown }>;

export const settingsCatalog: readonly Setting[] = [
  ...Object.entries(snapshotDefinitions).map(([key, definition]) => ({
    key, ...definition, initial: defaultSettings[key as keyof VueSettings],
  })),
  ...Object.entries(liveDefinitions).map(([key, definition]) => ({ key, ...definition })),
];

const settingsByKey = new Map(settingsCatalog.map((setting) => [setting.key, setting]));
function settingType(setting: Setting): SettingType {
  if (setting.type) return setting.type;
  if (Array.isArray(setting.initial)) return "array";
  const type = typeof setting.initial;
  return type === "string" || type === "number" || type === "boolean" ? type : "object";
}
export function findSetting(key: string) {
  const setting = settingsByKey.get(key);
  if (!setting) throw new Error(t("settings.tools.unknownKey", { key }));
  return setting;
}
export function checkedSettingValue(setting: Setting, value: unknown) {
  const type = settingType(setting);
  const correctlyTyped = type === "array" ? Array.isArray(value)
    : type === "object" ? record(value)
      : typeof value === type;
  const allowed = setting.options?.() ?? setting.allowed;
  const bounds = setting.range;
  const withinRange = !bounds || (typeof value === "number" && Number.isInteger(value)
    && value >= bounds.min && value <= bounds.max
    && (bounds.step === undefined || (value - bounds.min) % bounds.step === 0));
  if (!correctlyTyped || (type === "number" && !Number.isFinite(value)) || !withinRange
    || (allowed && !allowed.some((option) => option === value)) || (setting.validate && !setting.validate(value))) {
    throw new Error(t("settings.tools.invalidValue", { key: setting.key }));
  }
  return value;
}

/** Connector headers, environment variables and commands can carry secrets under arbitrary names. */
function redactSettingsValue(value: unknown, key = ""): unknown {
  if (isSensitiveDataName(key) || /^(env|headers|args|command|auth|oauth|passcode)$/i.test(key)) return hidden;
  if (Array.isArray(value)) return value.map((item) => redactSettingsValue(item));
  if (record(value)) return Object.fromEntries(Object.entries(value).map(([field, item]) => [field, redactSettingsValue(item, field)]));
  return sanitizeAiResult(value);
}

export function describeSetting(setting: Setting, snapshot: Record<string, unknown>) {
  const value = Object.prototype.hasOwnProperty.call(snapshot, setting.key) ? snapshot[setting.key] : setting.initial;
  return {
    key: setting.key,
    group: setting.group,
    type: settingType(setting),
    value: setting.secret ? hidden : redactSettingsValue(value) ?? null,
    options: setting.options?.() ?? setting.allowed ?? [],
    ...(setting.range ? { range: setting.range } : {}),
    description: t(setting.label),
  };
}
