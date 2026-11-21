<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import type { PluginServiceProviderChoice } from "@/services/plugin-services";
import {
  commandInputInitialValues,
  validateCommandInputValues,
  type CommandInputForm,
} from "@/domain/commands/input-schema";
import { useVueI18n } from "@/i18n/messages";

// Plugin commands and plugin services (services/plugin-services.ts) share this
// form. A service request may list providers whose params differ: the user
// picks one first and the form follows that provider's params.
const props = defineProps<{
  request: { title: string; form: CommandInputForm | null; providers?: readonly PluginServiceProviderChoice[] } | null;
}>();
const emit = defineEmits<{
  confirm: [values: Record<string, string | number | boolean | undefined>, providerId?: string];
  cancel: [];
}>();

const { t } = useVueI18n();

const values = ref<Record<string, string | number | boolean | undefined>>({});
const submitting = ref(false);
const providerId = ref("");

const form = computed<CommandInputForm | null>(() => {
  const request = props.request;
  if (!request) return null;
  if (!request.providers) return request.form;
  return request.providers.find((provider) => provider.id === providerId.value)?.form ?? null;
});

watch(
  () => props.request,
  () => {
    providerId.value = "";
    submitting.value = false;
  },
  { immediate: true },
);

watch(
  form,
  (current) => {
    values.value = current ? commandInputInitialValues(current) : {};
  },
  { immediate: true },
);

const visible = computed({
  get: () => props.request !== null,
  set: (next: boolean) => {
    if (!next) emit("cancel");
  },
});

function enumOptions(property: { enum?: (string | number | boolean)[] }) {
  return (property.enum ?? []).map((value) => ({ value, label: String(value) }));
}

async function submit() {
  const request = props.request;
  if (!request || submitting.value) return;
  if (request.providers && !request.providers.some((provider) => provider.id === providerId.value)) {
    ElMessage.warning(t("plugins.services.chooseProviderRequired"));
    return;
  }
  const failure = form.value ? validateCommandInputValues(form.value, values.value) : null;
  if (failure) {
    ElMessage.warning(
      t(
        failure.reason === "required"
          ? "commandPalette.pluginParams.required"
          : "commandPalette.pluginParams.invalidType",
        { name: failure.property },
      ),
    );
    return;
  }
  submitting.value = true;
  try {
    if (request.providers) emit("confirm", { ...values.value }, providerId.value);
    else emit("confirm", { ...values.value });
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="t('commandPalette.pluginParams.title')"
    width="420px"
    :close-on-click-modal="false"
    append-to-body
  >
    <p v-if="request" class="app-plugin-params-subtitle">
      {{ request.title }}
    </p>
    <p v-if="form?.description" class="app-plugin-params-description">
      {{ form.description }}
    </p>
    <el-form v-if="request" label-position="top" @submit.prevent="submit">
      <el-form-item
        v-if="request.providers"
        :label="t('plugins.services.chooseProvider')"
        required
      >
        <el-select
          v-model="providerId"
          :placeholder="t('plugins.services.chooseProviderPlaceholder')"
          class="app-plugin-params-control app-plugin-params-provider"
        >
          <el-option
            v-for="provider in request.providers"
            :key="provider.id"
            :label="provider.label"
            :value="provider.id"
          />
        </el-select>
      </el-form-item>
      <el-form-item
        v-for="property in form?.properties ?? []"
        :key="property.name"
        :label="property.name"
        :required="property.required"
      >
        <el-select
          v-if="property.enum"
          v-model="values[property.name]"
          :placeholder="property.description"
          class="app-plugin-params-control"
        >
          <el-option
            v-for="option in enumOptions(property)"
            :key="String(option.value)"
            :label="option.label"
            :value="option.value"
          />
        </el-select>
        <el-switch
          v-else-if="property.type === 'boolean'"
          v-model="values[property.name] as boolean"
        />
        <el-input-number
          v-else-if="property.type === 'integer' || property.type === 'number'"
          v-model="values[property.name] as number | undefined"
          :precision="property.type === 'integer' ? 0 : undefined"
          controls-position="right"
          class="app-plugin-params-control"
        />
        <el-input
          v-else
          v-model="values[property.name] as string | undefined"
          :placeholder="property.description"
          clearable
        />
        <p v-if="property.description && !property.enum" class="app-plugin-params-hint">
          {{ property.description }}
        </p>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="emit('cancel')">
        {{ t("commandPalette.pluginParams.cancel") }}
      </el-button>
      <el-button type="primary" :loading="submitting" @click="submit">
        {{ t("commandPalette.pluginParams.confirm") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.app-plugin-params-subtitle {
  margin: 0 0 4px;
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
}

.app-plugin-params-description {
  margin: 0 0 12px;
  font-size: 12px;
  color: var(--app-text-muted);
}

.app-plugin-params-hint {
  margin: 4px 0 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--app-text-muted);
}

.app-plugin-params-control {
  width: 100%;
}
</style>
