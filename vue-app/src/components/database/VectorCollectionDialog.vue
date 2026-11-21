<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import type { FormInstance, FormRules } from "element-plus";
import type { DatabaseConnection } from "@/domain/database/types";
import type { VectorCollectionCreateRequest } from "@/domain/database/vector-database";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  modelValue: boolean;
  connection?: DatabaseConnection;
  saving?: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  create: [request: VectorCollectionCreateRequest];
}>();

const { t } = useVueI18n();
const formRef = ref<FormInstance>();
const form = reactive<VectorCollectionCreateRequest>({
  name: "",
  dimension: 1536,
  metric: "cosine",
  cloud: "aws",
  region: "us-east-1",
  vectorName: "",
});

const isPinecone = computed(() => props.connection?.dbType === "pinecone");
const isWeaviate = computed(() => props.connection?.dbType === "weaviate");
const requiresDimension = computed(() =>
  ["qdrant", "milvus", "pinecone"].includes(props.connection?.dbType ?? "")
);
const objectKind = computed(() =>
  isPinecone.value ? t("database.vectorIndex") : t("database.vectorCollection")
);
const rules = computed<FormRules>(() => ({
  name: [{ required: true, message: t("database.vectorNameRequired"), trigger: "blur" }],
  dimension: requiresDimension.value
    ? [
        {
          type: "number",
          required: true,
          min: 1,
          max: 1_000_000,
          message: t("database.vectorDimensionRange"),
          trigger: "change",
        },
      ]
    : [],
  cloud: isPinecone.value
    ? [{ required: true, message: t("database.vectorCloudRequired"), trigger: "change" }]
    : [],
  region: isPinecone.value
    ? [{ required: true, message: t("database.vectorRegionRequired"), trigger: "blur" }]
    : [],
}));

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) return;
    Object.assign(form, {
      name: "",
      dimension: 1536,
      metric: "cosine",
      cloud: "aws",
      region: "us-east-1",
      vectorName: "",
    });
    formRef.value?.clearValidate();
  }
);

function close() {
  if (props.saving) return;
  emit("update:modelValue", false);
}

async function submit() {
  if (!(await formRef.value?.validate().catch(() => false))) return;
  emit("create", {
    name: form.name.trim(),
    dimension: form.dimension,
    metric: form.metric,
    cloud: form.cloud.trim(),
    region: form.region.trim(),
    vectorName: form.vectorName.trim(),
  });
}
</script>

<template>
  <el-dialog
    append-to-body
    :model-value="modelValue"
    :title="t('database.createVectorObject', { kind: objectKind })"
    width="480px"
    destroy-on-close
    :close-on-click-modal="!saving"
    @close="close"
  >
    <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent="submit">
      <el-form-item :label="t('database.vectorObjectName', { kind: objectKind })" prop="name">
        <el-input v-model="form.name" autofocus maxlength="255" />
      </el-form-item>
      <div
        class="app-vector-create-grid"
        :class="{ 'app-vector-create-grid--single': !requiresDimension }"
      >
        <el-form-item v-if="requiresDimension" :label="t('database.vectorDimension')" prop="dimension">
          <el-input-number v-model="form.dimension" :min="1" :max="1000000" :controls="false" />
        </el-form-item>
        <el-form-item :label="t('database.vectorMetric')" prop="metric">
          <el-select v-model="form.metric">
            <el-option :label="t('database.vectorMetricCosine')" value="cosine" />
            <el-option :label="t('database.vectorMetricEuclidean')" value="euclidean" />
            <el-option :label="t('database.vectorMetricDotProduct')" value="dotproduct" />
          </el-select>
        </el-form-item>
      </div>
      <template v-if="isPinecone">
        <div class="app-vector-create-grid">
          <el-form-item :label="t('database.vectorCloud')" prop="cloud">
            <el-select v-model="form.cloud">
              <el-option label="AWS" value="aws" />
              <el-option label="Google Cloud" value="gcp" />
              <el-option label="Azure" value="azure" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('database.vectorRegion')" prop="region">
            <el-input v-model="form.region" placeholder="us-east-1" />
          </el-form-item>
        </div>
      </template>
      <el-form-item v-if="isWeaviate" :label="t('database.weaviateNamedVector')" prop="vectorName">
        <el-input v-model="form.vectorName" :placeholder="t('database.weaviateNamedVectorPlaceholder')" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button :disabled="saving" @click="close">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :loading="saving" @click="submit">
        {{ t("common.create") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.app-vector-create-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 12px;
}

.app-vector-create-grid--single {
  grid-template-columns: minmax(0, 1fr);
}

.app-vector-create-grid :deep(.el-input-number),
.app-vector-create-grid :deep(.el-select) {
  width: 100%;
}
</style>
