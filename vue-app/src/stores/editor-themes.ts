/**
 * Contributed editor themes (contributes.editorThemes) as re-validated
 * display data for the editor theme setting. The Monaco themes themselves
 * are defined by services/editor-theme.ts.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { EditorThemeIssue, EditorThemeRecord } from "@/domain/themes/editor-themes";

export const useEditorThemesStore = defineStore("editor-themes", () => {
  const themes = ref<EditorThemeRecord[]>([]);
  const issues = ref<EditorThemeIssue[]>([]);
  /** True once the consumer applied a snapshot (so a missing theme means its plugin went away). */
  const loaded = ref(false);

  function setThemes(next: EditorThemeRecord[], nextIssues: EditorThemeIssue[]) {
    themes.value = next;
    issues.value = nextIssues;
    loaded.value = true;
  }

  function markLoaded() {
    loaded.value = true;
  }

  function clearThemes() {
    themes.value = [];
    issues.value = [];
  }

  function themeById(id: string) {
    return themes.value.find((theme) => theme.id === id);
  }

  return { themes, issues, loaded, setThemes, markLoaded, clearThemes, themeById };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useEditorThemesStore, import.meta.hot));
}
