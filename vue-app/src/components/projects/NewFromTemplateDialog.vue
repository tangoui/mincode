<script setup lang="ts">
/**
 * "New File / New Project from Template" dialog for plugin `fileTemplates`.
 *
 * The user picks a template, enters a name (and, for a project, a parent
 * folder through the native dialog), reviews the rendered file list and then
 * presses Create. Only then are the sources fetched (hash-verified) and the
 * files written with create-only writes; an existing target stops the run and
 * the files already created are reported (never rolled back or deleted).
 */
import { computed, defineAsyncComponent, ref, shallowRef, watch } from "vue";
import { FolderOpened } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { resolveLocalizedTitle } from "@/domain/i18n/localized-title";
import {
  isValidTemplateName,
  renderTargetPaths,
  templateVariables,
  type FileTemplateKind,

} from "@/domain/projects/file-templates";
import {
  type FileTemplate,
  applyTemplateWrites,
  FileTemplateSourceError,
  type FileTemplateCreatedEvent,
  prepareTemplateWrites,
  templateTargetExists,
} from "@/services/file-template-service";
import { chooseNativeDirectory, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";

const props = defineProps<{
  kind: FileTemplateKind;
  templates: readonly FileTemplate[];
  /** File templates: the open project root. */
  projectRoot?: string;
  /** File templates: target folder relative to projectRoot ("" = root). */
  parentPath?: string;
}>();
const emit = defineEmits<{ created: [event: FileTemplateCreatedEvent] }>();
const visible = defineModel<boolean>({ default: false });

const { t, localeChain } = useVueI18n();
const CustomFileTemplatesDialog=defineAsyncComponent(()=>import("./CustomFileTemplatesDialog.vue"));
const managerVisible=ref(false),copySelection=ref<FileTemplate>();
function manage(copy=false){copySelection.value=copy?selected.value??undefined:undefined;managerVisible.value=true;}

const selectedKey = ref("");
const name = ref("");
const parentFolder = ref("");
const busy = ref(false);
const errorMessage = ref("");
const failure = shallowRef<{ created: string[]; filePath: string; message: string; alreadyExists: boolean } | null>(null);

const available = computed(() => props.templates.filter((template) => template.kind === props.kind));
const selected = computed(() => available.value.find((template) => template.key === selectedKey.value) ?? null);

const groups = computed(() => {
  const byCategory = new Map<string, FileTemplate[]>();
  for (const template of available.value) {
    const category = template.category || t("pluginContrib.fileTemplates.uncategorized");
    byCategory.set(category, [...(byCategory.get(category) ?? []), template]);
  }
  return [...byCategory.entries()].map(([label, templates]) => ({
    label,
    options: templates.map((template) => ({
      key: template.key,
      label: resolveLocalizedTitle(template, localeChain.value),
    })),
  }));
});

const description = computed(() => {
  const template = selected.value;
  if (!template?.description) return "";
  return template.descriptionLocalized
    ? resolveLocalizedTitle({ title: template.description, titleLocalized: template.descriptionLocalized }, localeChain.value)
    : template.description;
});

const trimmedName = computed(() => name.value.trim());
const nameValid = computed(() => isValidTemplateName(name.value));

function basename(path: string) {
  return path.replace(/\\/g, "/").replace(/\/+$/, "").split("/").pop() ?? "";
}

const variables = computed(() =>
  templateVariables(trimmedName.value, props.kind === "project" ? trimmedName.value : basename(props.projectRoot ?? ""))
);

const preview = computed<{ rows: Array<{ path: string; size: number }>; error: string }>(() => {
  const template = selected.value;
  if (!template || !nameValid.value) return { rows: [], error: "" };
  try {
    return { rows: renderTargetPaths(template, variables.value).map((target) => ({ path: target.relativePath, size: target.size })), error: "" };
  } catch (error) {
    return { rows: [], error: nativeErrorMessage(error) };
  }
});

const targetLabel = computed(() => {
  if (props.kind === "project") {
    return parentFolder.value && nameValid.value ? `${parentFolder.value.replace(/[\\/]+$/, "")}/${trimmedName.value}` : "";
  }
  return props.parentPath ? props.parentPath : basename(props.projectRoot ?? "");
});

const canCreate = computed(
  () =>
    !busy.value &&
    isDesktopRuntime() &&
    Boolean(selected.value) &&
    nameValid.value &&
    !preview.value.error &&
    preview.value.rows.length > 0 &&
    (props.kind === "project" ? Boolean(parentFolder.value) : Boolean(props.projectRoot))
);

function resetForm() {
  const first = available.value[0];
  selectedKey.value = first?.key ?? "";
  name.value = first?.defaultName ?? "";
  parentFolder.value = "";
  errorMessage.value = "";
  failure.value = null;
}

watch(visible, (open) => {
  if (open) resetForm();
});

watch(selectedKey, (key, previous) => {
  const template = available.value.find((item) => item.key === key);
  const previousTemplate = available.value.find((item) => item.key === previous);
  if (template && (!name.value.trim() || name.value === previousTemplate?.defaultName)) name.value = template.defaultName;
});

// A template that disappears (plugin disabled or uninstalled) can no longer be used.
watch(available, () => {
  if (selectedKey.value && !selected.value) {
    selectedKey.value = "";
    errorMessage.value = t("pluginContrib.fileTemplates.unavailable");
  }
});

async function chooseParent() {
  const directory = await chooseNativeDirectory(t("pluginContrib.fileTemplates.chooseParent"));
  if (directory) parentFolder.value = directory;
}

function formatSize(bytes: number) {
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KiB`;
}

async function create() {
  const template = selected.value;
  if (!template || !canCreate.value) return;
  busy.value = true;
  errorMessage.value = "";
  failure.value = null;
  try {
    const isProject = props.kind === "project";
    const root = isProject ? parentFolder.value : (props.projectRoot ?? "");
    const parent = isProject ? trimmedName.value : (props.parentPath ?? "");
    if (isProject && (await templateTargetExists(root, parent))) {
      errorMessage.value = t("pluginContrib.fileTemplates.folderExists", { name: parent });
      return;
    }
    const writes = await prepareTemplateWrites(template, variables.value, parent);
    const result = await applyTemplateWrites(root, writes);
    const projectPath = isProject ? `${root.replace(/[\\/]+$/, "")}/${parent}` : undefined;
    if (result.failed) {
      failure.value = { created: result.created, ...result.failed };
    } else {
      visible.value = false;
    }
    if (result.created.length || !result.failed) {
      emit("created", { kind: props.kind, root, created: result.created, projectPath, complete: !result.failed });
    }
  } catch (error) {
    errorMessage.value =
      error instanceof FileTemplateSourceError
        ? t("pluginContrib.fileTemplates.sourceFailed", { message: error.message })
        : nativeErrorMessage(error);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="t(kind === 'project' ? 'pluginContrib.fileTemplates.newProjectTitle' : 'pluginContrib.fileTemplates.newFileTitle')"
    width="560px"
    append-to-body
    :close-on-click-modal="!busy"
  >
    <el-button :disabled="busy" @click="manage()">{{t('pluginContrib.fileTemplates.manage')}}</el-button>
    <el-button :disabled="busy||!selected" @click="manage(true)">{{t('pluginContrib.fileTemplates.copyTemplate')}}</el-button>
    <el-empty v-if="!available.length" :image-size="56" :description="t('pluginContrib.fileTemplates.empty')" />
    <el-form v-else label-position="top" class="file-template-dialog" @submit.prevent>
      <el-form-item :label="t('pluginContrib.fileTemplates.template')">
        <el-select v-model="selectedKey" filterable :disabled="busy">
          <el-option-group v-for="group in groups" :key="group.label" :label="group.label">
            <el-option v-for="option in group.options" :key="option.key" :value="option.key" :label="option.label" />
          </el-option-group>
        </el-select>
        <p v-if="description" class="file-template-dialog__hint">{{ description }}</p>
      </el-form-item>
      <el-form-item
        :label="t(kind === 'project' ? 'pluginContrib.fileTemplates.folderName' : 'pluginContrib.fileTemplates.name')"
        :error="name && !nameValid ? t('project.validation.name') : ''"
      >
        <el-input v-model="name" :maxlength="64" :disabled="busy" />
      </el-form-item>
      <el-form-item v-if="kind === 'project'" :label="t('pluginContrib.fileTemplates.parentFolder')">
        <div class="file-template-dialog__parent">
          <el-input :model-value="parentFolder" readonly :placeholder="t('pluginContrib.fileTemplates.chooseParent')" />
          <el-button native-type="button" :icon="FolderOpened" :disabled="busy || !isDesktopRuntime()" @click="chooseParent">
            {{ t("pluginContrib.fileTemplates.browse") }}
          </el-button>
        </div>
      </el-form-item>
      <p v-if="targetLabel" class="file-template-dialog__hint">
        {{ t("pluginContrib.fileTemplates.target", { path: targetLabel }) }}
      </p>
      <div class="file-template-dialog__preview" :aria-label="t('pluginContrib.fileTemplates.preview')">
        <strong>{{ t("pluginContrib.fileTemplates.preview") }}</strong>
        <p v-if="preview.error" class="file-template-dialog__error" role="alert">{{ preview.error }}</p>
        <ul v-else-if="preview.rows.length" role="list">
          <li v-for="row in preview.rows" :key="row.path">
            <code>{{ row.path }}</code>
            <small>{{ formatSize(row.size) }}</small>
          </li>
        </ul>
        <p v-else class="file-template-dialog__hint">{{ t("pluginContrib.fileTemplates.previewEmpty") }}</p>
      </div>
      <p class="file-template-dialog__hint">{{ t("pluginContrib.fileTemplates.createOnlyNotice") }}</p>
      <p v-if="!isDesktopRuntime()" class="file-template-dialog__error">{{ t("project.desktopOnly") }}</p>
      <p v-if="errorMessage" class="file-template-dialog__error" role="alert">{{ errorMessage }}</p>
      <div v-if="failure" class="file-template-dialog__error" role="alert">
        <p>
          {{
            failure.alreadyExists
              ? t("pluginContrib.fileTemplates.alreadyExists", { path: failure.filePath })
              : t("pluginContrib.fileTemplates.writeFailed", { path: failure.filePath, message: failure.message })
          }}
        </p>
        <p>{{ t("pluginContrib.fileTemplates.partialFailure", { count: failure.created.length }) }}</p>
        <ul v-if="failure.created.length" role="list">
          <li v-for="path in failure.created" :key="path"><code>{{ path }}</code></li>
        </ul>
      </div>
    </el-form>
    <template #footer>
      <el-button native-type="button" :disabled="busy" @click="visible = false">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" native-type="button" :loading="busy" :disabled="!canCreate" @click="create">
        {{ t("pluginContrib.fileTemplates.create") }}
      </el-button>
    </template>
  </el-dialog>
  <CustomFileTemplatesDialog v-if="managerVisible" v-model="managerVisible" :initial-template="copySelection"/>
</template>

<style scoped>
.file-template-dialog__hint {
  margin: 4px 0 0;
  color: var(--app-text-secondary);
  font-size: 12px;
}

.file-template-dialog__parent {
  display: flex;
  width: 100%;
  gap: 8px;
}

.file-template-dialog__preview {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-top: 8px;
  padding: 8px;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
}

.file-template-dialog__preview ul,
.file-template-dialog__error ul {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 200px;
  margin: 0;
  padding: 0;
  overflow: auto;
  list-style: none;
}

.file-template-dialog__preview li {
  display: flex;
  justify-content: space-between;
  gap: 8px;
}

.file-template-dialog__preview code,
.file-template-dialog__error code {
  font-family: var(--app-font-mono);
  font-size: 12px;
  word-break: break-all;
}

.file-template-dialog__preview small {
  flex: none;
  color: var(--app-text-secondary);
}

.file-template-dialog__error {
  margin: 8px 0 0;
  color: var(--app-danger);
  font-size: 12px;
}

.file-template-dialog__error p {
  margin: 0 0 4px;
}
</style>
