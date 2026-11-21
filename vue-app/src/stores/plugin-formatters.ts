/**
 * Plugin formatters (contributes.formatters): the re-validated formatters of
 * process plugins active in this workspace, and the machine-local
 * per-language preference (`mincode_plugin_formatters_v1`, never synced or
 * exported: it is not an `app_` key). Built-in formatting stays the default;
 * a plugin formatter runs only after the user picks it.
 */
import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  PLUGIN_FORMATTERS_STORAGE_KEY,
  emptyFormatterPreferences,
  formatterPreference,
  formattersForLanguage,
  normalizeFormatterPreferences,
  selectedFormatter,
  updateFormatterPreference,
  type FormatterIssue,
  type FormatterPreferences,
  type PluginFormatter,
} from "@/domain/plugins/formatters";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export const usePluginFormattersStore = defineStore("plugin-formatters", () => {
  const formatters = ref<PluginFormatter[]>([]);
  const issues = ref<FormatterIssue[]>([]);
  /** True once the consumer applied a snapshot. */
  const loaded = ref(false);
  const preferences = ref<FormatterPreferences>(emptyFormatterPreferences());
  const preferencesLoaded = ref(false);
  let loading: Promise<void> | undefined;
  let edited = false;

  /** Languages that at least one available plugin formatter covers, sorted. */
  const languages = computed(() => [...new Set(formatters.value.flatMap((formatter) => formatter.languages))].sort());

  /** Languages with a stored plugin selection whose formatter is not available now. */
  const unavailableLanguages = computed(() =>
    Object.keys(preferences.value.byLanguage)
      .filter((language) => preferences.value.byLanguage[language]?.formatter && !selectedFormatter(preferences.value, formatters.value, language))
      .sort()
  );

  function loadPreferences(): Promise<void> {
    loading ??= loadPersistedValue<unknown>(PLUGIN_FORMATTERS_STORAGE_KEY)
      .then((value) => {
        // A change made while loading wins over the stored value.
        if (!edited) preferences.value = normalizeFormatterPreferences(value);
      })
      .catch(() => undefined)
      .finally(() => {
        preferencesLoaded.value = true;
      });
    return loading;
  }

  function setContributions(next: PluginFormatter[], nextIssues: FormatterIssue[]) {
    formatters.value = next;
    issues.value = nextIssues;
    loaded.value = true;
  }

  function clearContributions() {
    formatters.value = [];
    issues.value = [];
  }

  function forLanguage(language: string) {
    return formattersForLanguage(formatters.value, language);
  }

  /** The available plugin formatter selected for a language, if any. */
  function selectedFor(language: string) {
    return selectedFormatter(preferences.value, formatters.value, language);
  }

  function preferenceFor(language: string) {
    return formatterPreference(preferences.value, language);
  }

  /** undefined `formatter` selects the built-in formatter (and turns format on save off). */
  function setPreference(language: string, change: { formatter?: string; formatOnSave?: boolean }) {
    edited = true;
    preferences.value = updateFormatterPreference(preferences.value, language, change);
    void savePersistedValue(PLUGIN_FORMATTERS_STORAGE_KEY, { version: 1, byLanguage: { ...preferences.value.byLanguage } }).catch(() => undefined);
  }

  return {
    formatters,
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
  import.meta.hot.accept(acceptHMRUpdate(usePluginFormattersStore, import.meta.hot));
}
