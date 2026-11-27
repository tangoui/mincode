import type { Pinia } from "pinia";
import { normalizeAiApprovalMode } from "@/domain/ai/approval-policy";
import { resolveRuntimePerformanceSettings, RUNTIME_PERFORMANCE_SETTING_KEYS } from "@/domain/performance/runtime-limits";
import { normalizeCustomEditorTheme } from "@/domain/themes/editor-theme-data";
import { useVueI18n } from "@/i18n/messages";
import { useAppUpdateStore } from "@/stores/app-update";
import { useAiConversationPreferencesStore } from "@/stores/ai-conversation-preferences";
import { useKeybindingsStore } from "@/stores/keybindings";
import { useWorkbenchStore } from "@/stores/workbench";
import { useWorkspaceStatePreferencesStore } from "@/stores/workspace-state-preferences";
import { activeApiKeySetting, agentTrustSettingKey } from "./ai-tools/configurable-settings";
import type { ConfigurableSettingsDependency } from "./ai-tools/settings-tools";
import { invokeAgentControl, setAgentControlTrusted, type AgentControlStatus } from "./agent-control-service";
import { hydrateAiApprovalMode, saveAiApprovalMode } from "./ai-approval-policy";
import { applyEditorThemePreference } from "./editor-theme";
import { defaultProfile, type AiProfile } from "./settings-model";
import { hydrateSettingsSnapshot, persistSettingsSnapshot } from "./settings-persistence";
import { setAutostart, setMinimizeToTray } from "./settings-service";
import { loadSidebarVisibility, saveSidebarVisibility, type SidebarVisibility } from "./sidebar-preferences";
import { applyTerminalColorScheme } from "./terminal-theme";
import { applyUiAppearance, legacyFontSizeToUiScale } from "./ui-appearance";
import { applyUiTheme, normalizeUiTheme } from "./ui-theme";

function profilesIn(snapshot: Record<string, unknown>): AiProfile[] {
  return Array.isArray(snapshot.aiProfiles) && snapshot.aiProfiles.length
    ? snapshot.aiProfiles : [defaultProfile];
}

export function createSettingsToolRuntime(pinia: Pinia | undefined): ConfigurableSettingsDependency {
  const updates = useAppUpdateStore(pinia);
  const shortcuts = useKeybindingsStore(pinia);
  const conversations = useAiConversationPreferencesStore(pinia);
  const workspace = useWorkbenchStore(pinia);
  const preferences = useWorkspaceStatePreferencesStore(pinia);
  const { t, setLocale } = useVueI18n();

  const liveSettings: Record<string, { read(): unknown; write(value: unknown): unknown }> = {
    [agentTrustSettingKey]: {
      read: async () => (await invokeAgentControl<AgentControlStatus>("agent_control_status")).trusted,
      write: (value) => setAgentControlTrusted(value === true),
    },
    "updates.autoCheck": {
      read: async () => { await updates.hydrate(); return updates.autoCheck; },
      write: (value) => updates.setAutoCheck(value === true),
    },
    "updates.skippedVersion": {
      read: async () => { await updates.hydrate(); return updates.skippedVersion ?? ""; },
      write: (value) => value ? updates.skipVersion(String(value)) : updates.clearSkippedVersion(),
    },
    "shortcuts.preset": {
      read: async () => { await shortcuts.hydrate(); return shortcuts.presetId; },
      write: async (value) => { await shortcuts.hydrate(); shortcuts.setPreset(String(value)); },
    },
    "shortcuts.overrides": {
      read: async () => { await shortcuts.hydrate(); return shortcuts.overrides; },
      write: async (value) => { await shortcuts.hydrate(); shortcuts.setOverrides(value as unknown[]); },
    },
    "shortcuts.doubleModifier": {
      read: async () => { await shortcuts.hydrate(); return shortcuts.doubleModifier; },
      write: async (value) => {
        await shortcuts.hydrate();
        shortcuts.setOptions({ doubleModifier: typeof value === "boolean" ? value : undefined });
      },
    },
    "shortcuts.sendKeybindingsToShell": {
      read: async () => { await shortcuts.hydrate(); return shortcuts.options.sendKeybindingsToShell === true; },
      write: async (value) => { await shortcuts.hydrate(); shortcuts.setOptions({ sendKeybindingsToShell: value === true }); },
    },
    "ai.approvalMode": { read: hydrateAiApprovalMode, write: (value) => saveAiApprovalMode(normalizeAiApprovalMode(value)) },
    "ai.conversationScope": {
      read: async () => { await conversations.hydrate(); return conversations.scopeMode; },
      write: (value) => conversations.setScopeMode(value === "workspace" ? "workspace" : "global"),
    },
    "ai.conversationSidebarPosition": {
      read: async () => { await conversations.hydrate(); return conversations.sidebarPosition; },
      write: (value) => conversations.setSidebarPosition(value === "right" ? "right" : "left"),
    },
    "workspace.isolation": {
      read: async () => { await preferences.hydrate(); return preferences.isolationEnabled; },
      write: async (value) => {
        await preferences.hydrate();
        await preferences.setIsolationEnabled(value === true, workspace.activeWorkspaceId);
      },
    },
    "sidebar.visibility": {
      read: loadSidebarVisibility,
      write: async (value) => saveSidebarVisibility({ ...await loadSidebarVisibility(), ...value as SidebarVisibility }),
    },
  };

  let pendingWrite: Promise<void> = Promise.resolve();
  async function writeSetting(key: string, value: unknown) {
    if (Object.prototype.hasOwnProperty.call(liveSettings, key)) {
      await liveSettings[key]!.write(value);
      return;
    }
    const snapshot = await hydrateSettingsSnapshot();
    const next = { ...snapshot };
    const changedKeys = [key];

    if (key === activeApiKeySetting) {
      const profiles = profilesIn(snapshot);
      const active = profiles.find((profile) => profile.id === snapshot.activeAiProfileId) ?? profiles[0]!;
      next.aiProfiles = profiles.map((profile) => profile === active ? { ...profile, apiKey: value } : profile);
      changedKeys[0] = "aiProfiles";
    } else if (key === "aiProfiles") {
      const profiles = value as AiProfile[];
      const oldProfiles = profilesIn(snapshot);
      next.aiProfiles = profiles.map((profile) => ({
        ...defaultProfile,
        ...profile,
        apiKey: profile.apiKey === "[REDACTED]"
          ? oldProfiles.find((old) => old.id === profile.id)?.apiKey ?? ""
          : profile.apiKey ?? "",
      }));
      if (!profiles.some((profile) => profile.id === snapshot.activeAiProfileId)) {
        next.activeAiProfileId = profiles[0]!.id;
        changedKeys.push("activeAiProfileId");
      }
    } else if (key === "activeAiProfileId" && !profilesIn(snapshot).some((profile) => profile.id === value)) {
      throw new Error(t("settings.tools.invalidValue", { key }));
    } else if (value === undefined) {
      delete next[key];
    } else {
      next[key] = value;
    }

    if (key === "editorCustomTheme") next.editorCustomTheme = normalizeCustomEditorTheme(value);
    if (RUNTIME_PERFORMANCE_SETTING_KEYS.some((field) => field === key)) {
      if (key !== "performanceProfile") {
        Object.assign(next, { ...resolveRuntimePerformanceSettings(snapshot), [key]: value, performanceProfile: "custom" });
      }
      Object.assign(next, resolveRuntimePerformanceSettings(next));
      changedKeys.push(...RUNTIME_PERFORMANCE_SETTING_KEYS);
    }

    if (key === "autostart") await setAutostart(value === true);
    if (key === "minimizeToTray") await setMinimizeToTray(value === true);
    await persistSettingsSnapshot(next, { strict: true, toolKeys: changedKeys });

    if (key === "uiTheme") applyUiTheme(normalizeUiTheme(next.uiTheme));
    if (key === "uiScale" || key === "uiFontFamily") applyUiAppearance(next);
    if (key === "editorColorTheme" || key === "editorCustomTheme") applyEditorThemePreference(next.editorColorTheme, next.editorCustomTheme);
    if (key === "terminalColorScheme") applyTerminalColorScheme(next.terminalColorScheme);
    if (key === "locale") setLocale(value === "en" ? "en" : "zh");
  }

  return {
    async read(key) {
      if (key && Object.prototype.hasOwnProperty.call(liveSettings, key)) return { [key]: await liveSettings[key]!.read() };
      const snapshot = await hydrateSettingsSnapshot();
      const values = { ...snapshot, ...resolveRuntimePerformanceSettings(snapshot) };
      if (!Object.prototype.hasOwnProperty.call(snapshot, "uiScale") && snapshot.fontSize !== undefined) {
        Object.assign(values, { uiScale: legacyFontSizeToUiScale(snapshot.fontSize) });
      }
      if (key) return values;
      const live = await Promise.all(Object.entries(liveSettings).map(async ([id, setting]) => [id, await setting.read()]));
      return { ...values, ...Object.fromEntries(live) };
    },
    write(key, value) {
      const write = pendingWrite.then(() => writeSetting(key, value));
      pendingWrite = write.catch(() => undefined);
      return write;
    },
  };
}
