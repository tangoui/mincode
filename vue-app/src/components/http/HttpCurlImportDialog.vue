<script setup lang="ts">
import { ref, watch } from "vue";
import type { ParsedCurlRequest } from "@/domain/http/curl";
import { parseCurlCommand } from "@/domain/http/curl";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  import: [request: ParsedCurlRequest];
}>();

const { t } = useVueI18n();
const command = ref("");
const error = ref("");

function importCommand() {
  const request = parseCurlCommand(command.value.trim());
  if (!request) {
    error.value = t("http.curlParseError");
    return;
  }
  emit("import", request);
  emit("update:modelValue", false);
}

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) return;
    command.value = "";
    error.value = "";
  }
);
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('http.importCurlTitle')"
    width="min(620px, 92vw)"
    append-to-body
    destroy-on-close
    top="4vh"
    class="http-curl-dialog http-fit-dialog"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <p class="http-curl-dialog__hint">{{ t("http.curlHint") }}</p>
    <el-input
      v-model="command"
      type="textarea"
      :rows="10"
      resize="none"
      :placeholder="t('http.curlExample')"
      :aria-label="t('http.importCurlTitle')"
      @keydown.meta.enter.prevent="importCommand"
      @keydown.ctrl.enter.prevent="importCommand"
    />
    <p v-if="error" class="http-curl-dialog__error" role="alert">{{ error }}</p>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :disabled="!command.trim()" @click="importCommand">
        {{ t("http.importCurl") }}
      </el-button>
    </template>
  </el-dialog>
</template>
