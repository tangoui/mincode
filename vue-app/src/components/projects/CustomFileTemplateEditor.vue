<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useVueI18n } from "@/i18n/messages";
import { templateVariables, renderTemplateText, renderedTargetPathError, templateTargetPathError } from "@/domain/projects/file-templates";
import type { CustomTemplateDraft } from "@/domain/projects/custom-file-templates";
const draft = defineModel<CustomTemplateDraft>({ required: true });
const props = defineProps<{
    busy: boolean;
}>();
const { t } = useVueI18n();
const selected = ref(0);
watch(() => draft.value, () => { selected.value = 0; });
const file = computed(() => draft.value.files[selected.value]);
const preview = computed(() => draft.value.files.map(f => renderTemplateText(f.path, templateVariables(draft.value.defaultName, draft.value.defaultName))));
const pathError = computed(() => { const seen = new Set<string>(); for (let i = 0; i < preview.value.length; i++) {
    const p = preview.value[i]!;
    if (templateTargetPathError(draft.value.files[i]!.path) || renderedTargetPathError(p))
        return t('pluginContrib.fileTemplates.invalidPath');
    const fold = p.toLowerCase();
    if ([...seen].some(s => s === fold || s.startsWith(fold + '/') || fold.startsWith(s + '/')))
        return t('pluginContrib.fileTemplates.invalidPath');
    seen.add(fold);
} return ''; });
function add() { draft.value.files.push({ path: `file-${draft.value.files.length + 1}.txt`, content: "" }); selected.value = draft.value.files.length - 1; }
function remove() { if (draft.value.files.length < 2)
    return; draft.value.files.splice(selected.value, 1); selected.value = 0; }
</script>
<template>
 <el-form label-position="top" :disabled="props.busy">
  <el-form-item :label="t('pluginContrib.fileTemplates.titleLabel')" data-test="template-title"><el-input v-model="draft.title" :maxlength="64" /></el-form-item>
  <el-form-item :label="t('pluginContrib.fileTemplates.descriptionLabel')"><el-input v-model="draft.description" :maxlength="256" /></el-form-item>
  <el-form-item :label="t('pluginContrib.fileTemplates.categoryLabel')"><el-input v-model="draft.category" :maxlength="24" /></el-form-item>
  <el-form-item :label="t('pluginContrib.fileTemplates.defaultName')"><el-input v-model="draft.defaultName" :maxlength="64" /></el-form-item>
  <el-form-item :label="t('pluginContrib.fileTemplates.kindLabel')"><el-select v-model="draft.kind"><el-option value="file" :disabled="draft.files.length>1" :label="t('pluginContrib.fileTemplates.fileKind')"/><el-option value="project" :label="t('pluginContrib.fileTemplates.projectKind')"/></el-select></el-form-item>
  <el-form-item :label="t('pluginContrib.fileTemplates.filesLabel')">
   <el-select v-model="selected"><el-option v-for="(f,i) in draft.files" :key="i" :value="i" :label="f.path"/></el-select>
   <el-button :disabled="draft.kind==='file'||draft.files.length>=64" @click="add">{{t('pluginContrib.fileTemplates.addFile')}}</el-button><el-button :disabled="draft.files.length<2" @click="remove">{{t('pluginContrib.fileTemplates.removeFile')}}</el-button>
  </el-form-item>
  <template v-if="file">
   <el-form-item :label="t('pluginContrib.fileTemplates.targetPath')"><el-input v-model="file.path" :maxlength="200"/></el-form-item>
   <el-form-item :label="t('pluginContrib.fileTemplates.bodyLabel')"><el-input v-model="file.content" type="textarea" :rows="12" spellcheck="false"/></el-form-item>
  </template>
  <el-alert v-if="pathError" :closable="false" type="error" :title="pathError"/>
  <p>{{t('pluginContrib.fileTemplates.variablesHelp')}}</p>
  <el-form-item :label="t('pluginContrib.fileTemplates.previewLabel')"><el-scrollbar max-height="160px"><pre>{{preview.join('\n')}}</pre></el-scrollbar></el-form-item>
  <el-form-item v-if="file" :label="t('pluginContrib.fileTemplates.contentPreview')"><el-scrollbar max-height="180px"><pre>{{renderTemplateText(file.content,templateVariables(draft.defaultName,draft.defaultName))}}</pre></el-scrollbar></el-form-item>
 </el-form>
</template>
