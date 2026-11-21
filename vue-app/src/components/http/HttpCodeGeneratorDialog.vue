<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { CopyDocument } from "@element-plus/icons-vue";
import {
  generateHttpCode,
  HTTP_CODE_LANGUAGES,
  type HttpCodeLanguage,
} from "@/domain/http/code-generation";
import type { HttpRequestDraft } from "@/domain/http/request-collection";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  modelValue: boolean;
  request: HttpRequestDraft;
}>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();

const { t } = useVueI18n();
const language = ref<HttpCodeLanguage>("curl");
const code = computed(() => generateHttpCode(props.request, language.value));

async function copyCode() {
  if (!code.value) return;
  try {
    await navigator.clipboard.writeText(code.value);
    ElMessage.success(t("http.codeCopied"));
  } catch (error) {
    ElMessage.error(t("http.copyFailed", { message: String(error) }));
  }
}

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) return;
    language.value = "curl";
  }
);
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('http.generateCode')"
    width="min(820px, 94vw)"
    append-to-body
    destroy-on-close
    top="4vh"
    class="http-code-dialog http-fit-dialog"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="http-code-dialog__toolbar">
      <el-scrollbar class="http-code-dialog__languages">
        <el-segmented
          v-model="language"
          :options="HTTP_CODE_LANGUAGES"
        />
      </el-scrollbar>
      <el-button :icon="CopyDocument" :disabled="!code" @click="copyCode">{{ t("common.copy") }}</el-button>
    </div>
    <el-scrollbar class="http-code-dialog__content">
      <pre><code>{{ code }}</code></pre>
    </el-scrollbar>
    <template #footer>
      <el-button type="primary" @click="emit('update:modelValue', false)">
        {{ t("common.close") }}
      </el-button>
    </template>
  </el-dialog>
</template>
