import { acceptHMRUpdate, defineStore } from "pinia";
import { computed, ref, shallowRef } from "vue";
import { parseCustomSummary, type CustomTemplateDraft, type CustomTemplateSnapshot } from "@/domain/projects/custom-file-templates";
import { listCustomTemplates, saveCustomTemplate, deleteCustomTemplate } from "@/services/custom-file-template-service";
export const useFileTemplatesStore = defineStore("file-templates", () => {
    const custom = shallowRef<CustomTemplateSnapshot>({ version: 1, revision: 0, templates: [] });
    const templates = computed(() => custom.value.templates);
    const loading = ref(false), error = ref("");
    let hydration: Promise<void> | undefined;
    async function refreshCustom() { loading.value = true; try {
        const next = await listCustomTemplates();
        if (next.revision >= custom.value.revision)
            custom.value = next;
        error.value = "";
    }
    catch (cause) {
        error.value = String(cause);
        throw cause;
    }
    finally {
        loading.value = false;
    } }
    function hydrateCustom() { if (!hydration) {
        hydration = refreshCustom().finally(() => { hydration = undefined; });
    } return hydration; }
    async function saveCustom(revision: number, draft: CustomTemplateDraft) {
        const saved = await saveCustomTemplate(revision, draft);
        const files = await Promise.all(saved.template.files.map(async (file) => {
            const bytes = new TextEncoder().encode(file.content);
            const digest = await crypto.subtle.digest("SHA-256", bytes);
            return { path: file.path, size: bytes.length, source: [...new Uint8Array(digest)].map(b => b.toString(16).padStart(2, "0")).join("") };
        }));
        if (custom.value.revision !== revision || saved.revision !== revision + 1) {
            try { await refreshCustom(); } catch { /* The mutation committed; keep the read error visible without retrying the write. */ }
            return saved;
        }
        if (saved.revision >= custom.value.revision) {
            const item = parseCustomSummary({ ...saved.template, files });
            custom.value = { version: 1, revision: saved.revision, templates: [...custom.value.templates.filter(t => t.id !== item.id), item] };
        }
        error.value = "";
        return saved;
    }
    async function deleteCustom(revision: number, id: string) {
        const next = await deleteCustomTemplate(revision, id);
        if (custom.value.revision !== revision || next !== revision + 1) {
            try { await refreshCustom(); } catch { /* Deletion committed; do not disguise it as a failed write. */ }
            return;
        }
        if (next >= custom.value.revision)
            custom.value = { version: 1, revision: next, templates: custom.value.templates.filter(t => t.id !== id) };
        error.value = "";
    }
    return { templates, custom, loading, error, hydrateCustom, refreshCustom, saveCustom, deleteCustom };
});
if (import.meta.hot)
    import.meta.hot.accept(acceptHMRUpdate(useFileTemplatesStore, import.meta.hot));
