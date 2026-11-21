/**
 * Plugin AI prompts and skills (`aiPrompts` contribution point) plus the
 * user's per-entry enable switches. The contributed entries are runtime data
 * set by services/contributions/ai-prompts.ts; only the list of disabled
 * `<pluginId>.<id>` keys is persisted (global key `app_ai_plugin_prompts_v1`,
 * shape `{ disabled: string[] }`). Entries are enabled by default: plugin
 * prompts never send anything and plugin skills apply only on explicit
 * `/skill`, so installing and enabling the plugin is the opt-in.
 */
import { acceptHMRUpdate, defineStore } from "pinia";
import { computed, ref, shallowRef } from "vue";
import type { PluginAiPrompt, PluginAiPromptIssue } from "@/domain/ai/plugin-prompts";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export const AI_PLUGIN_PROMPTS_STORAGE_KEY = "app_ai_plugin_prompts_v1";
const MAX_DISABLED = 1024;
const KEY_PATTERN = /^[^\s]{1,256}$/;

export interface PersistedAiPluginPrompts {
  disabled: string[];
}

export function normalizePersistedAiPluginPrompts(value: unknown): PersistedAiPluginPrompts {
  const disabled = typeof value === "object" && value !== null && Array.isArray((value as { disabled?: unknown }).disabled)
    ? (value as { disabled: unknown[] }).disabled
    : [];
  const keys = disabled.filter((key): key is string => typeof key === "string" && KEY_PATTERN.test(key));
  return { disabled: [...new Set(keys)].slice(0, MAX_DISABLED) };
}

export const useAiPluginPromptsStore = defineStore("ai-plugin-prompts", () => {
  const contributed = shallowRef<readonly PluginAiPrompt[]>([]);
  const issues = shallowRef<readonly PluginAiPromptIssue[]>([]);
  const disabled = ref<string[]>([]);
  const hydrated = ref(false);
  let hydratePromise: Promise<void> | undefined;

  const disabledSet = computed(() => new Set(disabled.value));
  const enabledPrompts = computed(() => contributed.value.filter((entry) => entry.kind === "prompt" && !disabledSet.value.has(entry.key)));
  const enabledSkills = computed(() => contributed.value.filter((entry) => entry.kind === "skill" && !disabledSet.value.has(entry.key)));

  function setContributed(next: readonly PluginAiPrompt[], nextIssues: readonly PluginAiPromptIssue[] = []) {
    contributed.value = next;
    issues.value = nextIssues;
  }

  function isEnabled(key: string) {
    return !disabledSet.value.has(key);
  }

  async function hydrate() {
    if (hydrated.value) return;
    hydratePromise ??= (async () => {
      const stored = await loadPersistedValue<unknown>(AI_PLUGIN_PROMPTS_STORAGE_KEY);
      disabled.value = normalizePersistedAiPluginPrompts(stored).disabled;
      hydrated.value = true;
    })().finally(() => {
      hydratePromise = undefined;
    });
    return hydratePromise;
  }

  async function setEnabled(key: string, enabled: boolean) {
    await hydrate();
    const next = new Set(disabled.value);
    if (enabled) next.delete(key);
    else next.add(key);
    disabled.value = normalizePersistedAiPluginPrompts({ disabled: [...next] }).disabled;
    await savePersistedValue(AI_PLUGIN_PROMPTS_STORAGE_KEY, { disabled: [...disabled.value] });
  }

  return { contributed, issues, disabled, hydrated, enabledPrompts, enabledSkills, setContributed, isEnabled, hydrate, setEnabled };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useAiPluginPromptsStore, import.meta.hot));
}
