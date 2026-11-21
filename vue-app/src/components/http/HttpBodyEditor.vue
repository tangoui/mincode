<script setup lang="ts">
import { Delete, FolderOpened, Plus } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import type {
  HttpBodyField,
  HttpBodyFieldType,
  HttpBodyType,
} from "@/domain/http/request-collection";
import { chooseHttpRequestUploadFile } from "@/services/http-request-file-service";
import { canInvoke } from "@/services/desktop-bridge";

const bodyType = defineModel<HttpBodyType>("bodyType", { required: true });
const body = defineModel<string>("body", { required: true });
const formBody = defineModel<HttpBodyField[]>("formBody", { required: true });
const binaryFilePath = defineModel<string>("binaryFilePath", { required: true });
const binaryFileName = defineModel<string>("binaryFileName", { required: true });
const graphqlQuery = defineModel<string>("graphqlQuery", { required: true });
const graphqlVariables = defineModel<string>("graphqlVariables", { required: true });

const { t } = useVueI18n();
let sequence = 0;
const bodyTypes: HttpBodyType[] = [
  "none",
  "json",
  "xml",
  "text",
  "html",
  "javascript",
  "form-data",
  "x-www-form-urlencoded",
  "graphql",
  "binary",
];
const fieldTypes: HttpBodyFieldType[] = [
  "text",
  "integer",
  "number",
  "boolean",
  "array",
  "object",
  "file",
];

function newField(): HttpBodyField {
  sequence += 1;
  return {
    id: `body-${Date.now()}-${sequence}`,
    key: "",
    value: "",
    enabled: true,
    fieldType: "text",
  };
}

function patchField(index: number, patch: Partial<HttpBodyField>) {
  formBody.value = formBody.value.map((row, rowIndex) =>
    rowIndex === index ? { ...row, ...patch } : row
  );
}

function updateFieldType(index: number, value: HttpBodyFieldType) {
  const row = formBody.value[index];
  if (!row) return;
  const switchesFileMode = (row.fieldType === "file") !== (value === "file");
  patchField(index, {
    fieldType: value,
    ...(switchesFileMode ? { value: "", fileName: undefined } : {}),
  });
}

function addField() {
  formBody.value = [...formBody.value, newField()];
}

function removeField(index: number) {
  formBody.value = formBody.value.filter((_, rowIndex) => rowIndex !== index);
}

async function chooseFieldFile(index: number) {
  if (!canInvoke("http_send_request")) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  const selected = await chooseHttpRequestUploadFile(t("http.chooseUploadFile"));
  if (!selected) return;
  patchField(index, { value: selected.path, fileName: selected.name });
}

async function chooseBinaryFile() {
  if (!canInvoke("http_send_request")) {
    ElMessage.warning(t("common.desktopRequired"));
    return;
  }
  const selected = await chooseHttpRequestUploadFile(t("http.chooseBinaryFile"));
  if (!selected) return;
  binaryFilePath.value = selected.path;
  binaryFileName.value = selected.name;
}
</script>

<template>
  <div class="http-body-editor">
    <el-segmented
      v-model="bodyType"
      class="http-body-editor__types"
      :options="bodyTypes.map((type) => ({ label: t(`http.bodyType.${type}` as never), value: type }))"
      :aria-label="t('http.bodyType')"
    />

    <div v-if="bodyType === 'none'" class="http-body-editor__empty">
      {{ t("http.noBody") }}
    </div>

    <el-input
      v-else-if="['json', 'xml', 'text', 'html', 'javascript', 'raw'].includes(bodyType)"
      v-model="body"
      type="textarea"
      resize="none"
      :placeholder="bodyType === 'json' ? '{\n  &quot;key&quot;: &quot;value&quot;\n}' : t('http.bodyPlaceholder')"
    />

    <div v-else-if="bodyType === 'graphql'" class="http-graphql-body">
      <label>
        <span>{{ t("http.graphqlQuery") }}</span>
        <el-input
          v-model="graphqlQuery"
          type="textarea"
          resize="none"
          :placeholder="t('http.graphqlQueryPlaceholder')"
        />
      </label>
      <label>
        <span>{{ t("http.graphqlVariables") }}</span>
        <el-input
          v-model="graphqlVariables"
          type="textarea"
          resize="none"
          :placeholder="t('http.graphqlVariablesPlaceholder')"
        />
      </label>
    </div>

    <el-scrollbar
      v-else-if="bodyType === 'form-data' || bodyType === 'x-www-form-urlencoded'"
      class="http-body-fields"
    >
      <div class="http-body-fields__head" :class="{ 'is-form-data': bodyType === 'form-data' }">
        <span />
        <b>{{ t("http.key") }}</b>
        <b v-if="bodyType === 'form-data'">{{ t("http.fieldType") }}</b>
        <b>{{ t("http.value") }}</b>
        <b v-if="bodyType === 'form-data'">{{ t("http.contentType") }}</b>
        <span />
      </div>
      <div
        v-for="(row, index) in formBody"
        :key="row.id"
        class="http-body-field"
        :class="{ 'is-form-data': bodyType === 'form-data' }"
      >
        <el-checkbox
          :model-value="row.enabled"
          @update:model-value="patchField(index, { enabled: Boolean($event) })"
        />
        <el-input
          :model-value="row.key"
          :placeholder="t('http.field')"
          @update:model-value="patchField(index, { key: String($event) })"
        />
        <el-select
          v-if="bodyType === 'form-data'"
          :model-value="row.fieldType"
          @update:model-value="updateFieldType(index, $event as HttpBodyFieldType)"
        >
          <el-option
            v-for="fieldType in fieldTypes"
            :key="fieldType"
            :label="t(`http.fieldType.${fieldType}` as never)"
            :value="fieldType"
          />
        </el-select>
        <el-input
          v-if="bodyType !== 'form-data' || row.fieldType !== 'file'"
          :model-value="row.value"
          :placeholder="t('http.value')"
          @update:model-value="patchField(index, { value: String($event) })"
        />
        <el-button
          v-else
          native-type="button"
          class="http-body-field__file"
          :title="t('http.chooseUploadFile')"
          @click="chooseFieldFile(index)"
        >
          <el-icon><FolderOpened /></el-icon>
          <span>{{ row.fileName || t("http.chooseFile") }}</span>
        </el-button>
        <el-input
          v-if="bodyType === 'form-data'"
          :model-value="row.contentType"
          :placeholder="t('http.contentTypeAuto')"
          @update:model-value="patchField(index, { contentType: String($event) })"
        />
        <el-button
          text
          :icon="Delete"
          :aria-label="t('common.delete')"
          :title="t('common.delete')"
          @click="removeField(index)"
        />
      </div>
      <el-button text native-type="button" class="http-add-row" @click="addField">
        <el-icon><Plus /></el-icon>{{ t("common.add") }}
      </el-button>
    </el-scrollbar>

    <div v-else class="http-binary-body">
      <el-button native-type="button" @click="chooseBinaryFile">
        <el-icon><FolderOpened /></el-icon>
        <span>{{ binaryFileName || t("http.chooseBinaryFile") }}</span>
      </el-button>
      <small>{{ t("http.nativeFileHint") }}</small>
    </div>
  </div>
</template>
