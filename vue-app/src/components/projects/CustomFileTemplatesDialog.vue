<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElMessageBox } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { useFileTemplatesStore } from "@/stores/file-templates";
import { getCustomTemplate } from "@/services/custom-file-template-service";
import { type FileTemplate } from "@/services/file-template-service";
import { isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";
import type { CustomTemplateDraft } from "@/domain/projects/custom-file-templates";
import { resolveLocalizedTitle } from "@/domain/i18n/localized-title";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import CustomFileTemplateEditor from "./CustomFileTemplateEditor.vue";
const visible = defineModel<boolean>({ default: false });
const props = defineProps<{
    initialTemplate?: FileTemplate;
    initialKind?: "file" | "project";
}>();
const { t, localeChain } = useVueI18n(), store = useFileTemplatesStore();
const query = ref(''), category = ref(''), draft = ref<CustomTemplateDraft>(), baseline = ref(''), revision = ref(0), busy = ref(false), error = ref('');
let generation = 0, disposed = false;
onBeforeUnmount(() => { disposed = true; generation++; });
const dirty = computed(() => draft.value && JSON.stringify(draft.value) !== baseline.value);
const categories = computed(() => [...new Set(store.templates.map(x => x.category).filter(Boolean))]);
const items = computed(() => store.templates.filter(x => (!category.value || x.category === category.value) && `${x.title} ${x.description} ${x.category}`.toLowerCase().includes(query.value.toLowerCase())));
const label = (x: FileTemplate) => resolveLocalizedTitle(x, localeChain.value);
async function discard() { if (!dirty.value)
    return true; try {
    await ElMessageBox.confirm(t('pluginContrib.fileTemplates.discardBody'), t('pluginContrib.fileTemplates.manage'), { type: 'warning' });
    return true;
}
catch {
    return false;
} }
function setDraft(value: CustomTemplateDraft, rev: number, asNew = false) { draft.value = value; revision.value = rev; baseline.value = asNew ? '' : JSON.stringify(value); error.value = ''; }
async function create(kind: 'file' | 'project') { if (busy.value || !await discard())
    return; generation++; setDraft({ id: '', revision: 0, kind, title: '', description: '', category: '', defaultName: 'Example', files: [{ path: '{{mincode.namePascal}}.txt', content: '' }] }, store.custom.revision, true); }
async function select(item: FileTemplate, copy = false) {
    if (busy.value || !await discard())
        return;
    const version = ++generation;
    busy.value = true;
    error.value = '';
    try {
        const got = await getCustomTemplate(item.id);
        if (disposed || version !== generation)
            return;
        setDraft({ ...got.template, id: copy ? '' : got.template.id, revision: copy ? 0 : got.template.revision }, got.revision, copy);
    }
    catch (cause) {
        error.value = nativeErrorMessage(cause);
    }
    finally {
        if (version === generation)
            busy.value = false;
    }
}
async function save() { if (!draft.value || busy.value)
    return; busy.value = true; error.value = ''; try {
    const saved = await store.saveCustom(revision.value, draft.value);
    setDraft(saved.template, saved.revision);
}
catch (cause) {
    error.value = nativeErrorMessage(cause);
}
finally {
    busy.value = false;
} }
async function remove(item: FileTemplate) { if (busy.value || !await discard())
    return; try {
    await ElMessageBox.confirm(t('pluginContrib.fileTemplates.deleteBody'), t('pluginContrib.fileTemplates.deleteTemplate'), { type: 'warning' });
}
catch {
    return;
} busy.value = true; try {
    await store.deleteCustom(store.custom.revision, item.id);
    draft.value = undefined;
    baseline.value = '';
}
catch (cause) {
    error.value = nativeErrorMessage(cause);
}
finally {
    busy.value = false;
} }
async function close(done: () => void) { if (!busy.value && await discard()) {
    generation++;
    done();
} }
async function reload() { try {
    await store.refreshCustom();
    if (draft.value?.id) {
        const item = store.custom.templates.find(t => t.id === draft.value!.id);
        if (item)
            await select(item);
    }
    else
        revision.value = store.custom.revision;
}
catch (cause) {
    error.value = nativeErrorMessage(cause);
} }
watch(visible, async (open) => { if (!open) {
    generation++;
    return;
} error.value = ''; draft.value = undefined; baseline.value = ''; try {
    await store.hydrateCustom();
    if (props.initialTemplate)
        await select(props.initialTemplate, true);
    else if (props.initialKind)
        await create(props.initialKind);
}
catch (cause) {
    error.value = nativeErrorMessage(cause);
} }, { immediate: true });
</script>
<template>
 <el-dialog v-model="visible" :title="t('pluginContrib.fileTemplates.manage')" width="min(1000px, calc(100vw - 32px))" append-to-body :before-close="close" :close-on-click-modal="false" :close-on-press-escape="!busy">
  <el-alert :closable="false" type="info" :title="t('pluginContrib.fileTemplates.localOnly')"/>
  <el-alert v-if="!isDesktopRuntime()" :closable="false" type="warning" :title="t('common.desktopRequired')"/>
  <el-alert v-if="error||store.error" :closable="false" type="error" :title="error||store.error"/>
  <div class="custom-file-templates__toolbar">
   <el-button :disabled="busy||!isDesktopRuntime()" @click="create('file')">{{t('pluginContrib.fileTemplates.newCustomFile')}}</el-button><el-button :disabled="busy||!isDesktopRuntime()" @click="create('project')">{{t('pluginContrib.fileTemplates.newCustomProject')}}</el-button><el-button :disabled="busy||!isDesktopRuntime()" @click="reload">{{t('pluginContrib.fileTemplates.reload')}}</el-button>
  </div>
  <div class="custom-file-templates__layout">
   <aside>
    <AppSearchField v-model="query" :placeholder="t('pluginContrib.fileTemplates.search')"/>
    <el-select v-model="category" clearable :placeholder="t('pluginContrib.fileTemplates.categoryLabel')"><el-option v-for="c in categories" :key="c" :value="c" :label="c"/></el-select>
    <el-scrollbar max-height="460px">
     <el-empty v-if="!items.length" :image-size="40" :description="t('pluginContrib.fileTemplates.empty')"/>
     <div v-for="item in items" :key="item.key" class="custom-file-templates__item">
      <el-button text :disabled="busy" @click="select(item)">{{label(item)}}</el-button>
      <el-button text :disabled="busy" @click="select(item,true)">{{t('pluginContrib.fileTemplates.copyTemplate')}}</el-button>
      <el-button text type="danger" :disabled="busy" @click="remove(item)">{{t('pluginContrib.fileTemplates.deleteTemplate')}}</el-button>
     </div>
    </el-scrollbar>
   </aside>
   <el-scrollbar max-height="560px" class="custom-file-templates__editor"><CustomFileTemplateEditor v-if="draft" v-model="draft" :busy="busy"/><el-empty v-else :description="t('pluginContrib.fileTemplates.selectOrCreate')"/></el-scrollbar>
  </div>
  <template #footer><el-button v-if="draft" :disabled="busy" @click="draft.id='';draft.revision=0;revision=store.custom.revision;baseline=''">{{t('pluginContrib.fileTemplates.saveAsCopy')}}</el-button><el-button type="primary" :disabled="!draft||busy||!isDesktopRuntime()||store.loading" @click="save">{{t('pluginContrib.fileTemplates.saveTemplate')}}</el-button></template>
 </el-dialog>
</template>
<style scoped>
.custom-file-templates__toolbar { display:flex; flex-wrap:wrap; gap:8px; margin:12px 0; }
.custom-file-templates__layout { display:grid; grid-template-columns:260px minmax(0,1fr); gap:16px; }
.custom-file-templates__layout aside { display:flex; flex-direction:column; gap:8px; min-width:0; }
.custom-file-templates__item { padding:8px 0; border-bottom:1px solid var(--app-border); }
.custom-file-templates__editor { min-width:0; }
@media(max-width:700px) { .custom-file-templates__layout { grid-template-columns:minmax(0,1fr); } }
</style>
