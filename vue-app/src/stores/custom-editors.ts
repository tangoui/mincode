/**
 * Custom editors (contributes.customEditors): the validated editor list from
 * the contribution consumer plus the user's editor associations, persisted
 * as `app_editor_associations_v1`. Associations are a global user
 * preference (not per workspace); an association naming an editor that is
 * not currently available stays stored but inert.
 */
import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  emptyEditorAssociations,
  normalizeEditorAssociations,
  resolveCustomEditors,
  resolveEditorsForPath,
  TEXT_EDITOR_ASSOCIATION,
  type CustomEditorContribution,
  type EditorAssociations,
} from "@/domain/plugins/custom-editors";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export const EDITOR_ASSOCIATIONS_KEY = "app_editor_associations_v1";

export const useCustomEditorsStore = defineStore("custom-editors", () => {
  const editors = ref<CustomEditorContribution[]>([]);
  /** True once the consumer applied a snapshot (so a missing editor means it went away). */
  const loaded = ref(false);
  const associations = ref<EditorAssociations>(emptyEditorAssociations());
  const hydrated = ref(false);
  let hydration: Promise<void> | undefined;

  const byKey = computed(() => new Map(editors.value.map((editor) => [editor.key, editor])));

  function setContributions(contributions: readonly PointContribution[]) {
    editors.value = resolveCustomEditors(contributions);
    loaded.value = true;
  }

  function clearContributions() {
    editors.value = [];
  }

  function editorByKey(key: string) {
    return byKey.value.get(key);
  }

  async function hydrate() {
    if (hydrated.value) return;
    hydration ??= (async () => {
      try {
        associations.value = normalizeEditorAssociations(await loadPersistedValue<unknown>(EDITOR_ASSOCIATIONS_KEY));
      } catch {
        associations.value = emptyEditorAssociations();
      }
      hydrated.value = true;
    })();
    try {
      await hydration;
    } finally {
      hydration = undefined;
    }
  }

  async function persist(next: EditorAssociations) {
    associations.value = normalizeEditorAssociations(next);
    await savePersistedValue(EDITOR_ASSOCIATIONS_KEY, associations.value);
  }

  function resolveForPath(relativePath: string) {
    return resolveEditorsForPath(editors.value, relativePath, associations.value);
  }

  /** Pins `pattern` to an editor key or the text editor; `null` removes the association. */
  async function setAssociation(pattern: string, value: string | null) {
    await hydrate();
    const byPattern = { ...associations.value.byPattern };
    if (value === null) delete byPattern[pattern.toLowerCase()];
    else byPattern[pattern.toLowerCase()] = value;
    await persist({ ...associations.value, byPattern });
  }

  async function confirmDefault(key: string) {
    await hydrate();
    if (associations.value.confirmedDefaults.includes(key)) return;
    await persist({ ...associations.value, confirmedDefaults: [...associations.value.confirmedDefaults, key] });
  }

  async function declineDefault(pattern: string) {
    await setAssociation(pattern, TEXT_EDITOR_ASSOCIATION);
  }

  return {
    editors,
    loaded,
    associations,
    hydrated,
    setContributions,
    clearContributions,
    editorByKey,
    hydrate,
    resolveForPath,
    setAssociation,
    confirmDefault,
    declineDefault,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useCustomEditorsStore, import.meta.hot));
}
