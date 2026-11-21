/**
 * Plugin linters (contributes.linters): the re-validated linters of process
 * plugins active in this workspace, and the machine-local per-language
 * preference (`mincode_plugin_linters_v1`, never synced or exported: it is
 * not an `app_` key). No plugin linter runs until the user selects it for a
 * language; lint-while-typing is a separate per-language opt-in.
 */
import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  PLUGIN_LINTERS_STORAGE_KEY,
  emptyLinterPreferences,
  linterPreference,
  lintersForLanguage,
  normalizeLinterPreferences,
  selectedLinter,
  updateLinterPreference,
  type LinterIssue,
  type LinterPreferences,
  type PluginLinter,
} from "@/domain/plugins/linters";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export const usePluginLintersStore = defineStore("plugin-linters", () => {
  const linters = ref<PluginLinter[]>([]);
  const issues = ref<LinterIssue[]>([]);
  const loaded = ref(false);
  const preferences = ref<LinterPreferences>(emptyLinterPreferences());
  const preferencesLoaded = ref(false);
  let loading: Promise<void> | undefined;
  let edited = false;

  /** Languages that at least one available plugin linter covers, sorted. */
  const languages = computed(() => [...new Set(linters.value.flatMap((linter) => linter.languages))].sort());

  /** Languages with a stored selection whose linter is not available now. */
  const unavailableLanguages = computed(() =>
    Object.keys(preferences.value.byLanguage)
      .filter((language) => preferences.value.byLanguage[language]?.linter && !selectedLinter(preferences.value, linters.value, language))
      .sort()
  );

  function loadPreferences(): Promise<void> {
    loading ??= loadPersistedValue<unknown>(PLUGIN_LINTERS_STORAGE_KEY)
      .then((value) => {
        if (!edited) preferences.value = normalizeLinterPreferences(value);
      })
      .catch(() => undefined)
      .finally(() => {
        preferencesLoaded.value = true;
      });
    return loading;
  }

  function setContributions(next: PluginLinter[], nextIssues: LinterIssue[]) {
    linters.value = next;
    issues.value = nextIssues;
    loaded.value = true;
  }

  function clearContributions() {
    linters.value = [];
    issues.value = [];
  }

  function forLanguage(language: string) {
    return lintersForLanguage(linters.value, language);
  }

  function selectedFor(language: string) {
    return selectedLinter(preferences.value, linters.value, language);
  }

  function preferenceFor(language: string) {
    return linterPreference(preferences.value, language);
  }

  /** undefined `linter` turns plugin linting off for the language (and lint-while-typing with it). */
  function setPreference(language: string, change: { linter?: string; onType?: boolean }) {
    edited = true;
    preferences.value = updateLinterPreference(preferences.value, language, change);
    void savePersistedValue(PLUGIN_LINTERS_STORAGE_KEY, { version: 1, byLanguage: { ...preferences.value.byLanguage } }).catch(() => undefined);
  }

  return {
    linters,
    issues,
    loaded,
    preferences,
    preferencesLoaded,
    languages,
    unavailableLanguages,
    loadPreferences,
    setContributions,
    clearContributions,
    forLanguage,
    selectedFor,
    preferenceFor,
    setPreference,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginLintersStore, import.meta.hot));
}
