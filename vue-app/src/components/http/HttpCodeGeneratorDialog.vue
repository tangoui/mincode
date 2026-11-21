<script setup lang="ts">
import { computed, ref, shallowRef, watch } from "vue";
import { ElMessage } from "element-plus";
import { CopyDocument } from "@element-plus/icons-vue";
import {
  generateHttpCode,
  HTTP_CODE_LANGUAGES,
  type HttpCodeLanguage,
} from "@/domain/http/code-generation";
import { CodeTemplateError } from "@/domain/http/code-template";
import type { HttpRequestDraft } from "@/domain/http/request-collection";
import { renderPluginHttpCode, type PluginHttpCodeGenerator } from "@/domain/plugins/http-code-generators";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { useVueI18n } from "@/i18n/messages";
import { HttpCodeTemplateSourceError, loadHttpCodeTemplate } from "@/services/http-code-generator-service";
import { useHttpCodeGeneratorsStore } from "@/stores/http-code-generators";

const props = defineProps<{
  modelValue: boolean;
  request: HttpRequestDraft;
}>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();

const { t, localeChain } = useVueI18n();
const generatorsStore = useHttpCodeGeneratorsStore();
const language = ref<HttpCodeLanguage>("curl");
/** Selected plugin generator key (`<pluginId>.<id>`); empty uses the built-in language. */
const pluginKey = ref("");
/** Verified template text of the selected generator (null while loading or on error). */
const pluginSource = shallowRef<{ key: string; template: string; text: string } | null>(null);
const pluginLoadError = ref("");
const pluginLoading = ref(false);
let loadSequence = 0;

const pluginOptions = computed(() =>
  generatorsStore.generators.map((generator) => ({
    value: generator.key,
    label: t("pluginContrib.httpCodeGenerators.option", {
      title: resolvePluginTitle(generator, localeChain.value),
      plugin: generator.pluginName,
    }),
  })),
);
const selectedGenerator = computed<PluginHttpCodeGenerator | undefined>(() =>
  pluginKey.value ? generatorsStore.generators.find((generator) => generator.key === pluginKey.value) : undefined,
);

const pluginResult = computed<{ code: string; error: string }>(() => {
  if (!pluginKey.value) return { code: "", error: "" };
  const generator = selectedGenerator.value;
  if (!generator) return { code: "", error: t("pluginContrib.httpCodeGenerators.unavailable") };
  if (pluginLoadError.value) return { code: "", error: pluginLoadError.value };
  const source = pluginSource.value;
  if (!source || source.key !== generator.key || source.template !== generator.template) return { code: "", error: "" };
  try {
    return { code: renderPluginHttpCode(generator, source.text, props.request), error: "" };
  } catch (error) {
    if (error instanceof CodeTemplateError && error.code === "output_too_large") {
      return { code: "", error: t("pluginContrib.httpCodeGenerators.tooLarge") };
    }
    return { code: "", error: t("pluginContrib.httpCodeGenerators.renderFailed", { detail: error instanceof Error ? error.message : String(error) }) };
  }
});

const code = computed(() => (pluginKey.value ? pluginResult.value.code : generateHttpCode(props.request, language.value)));

function sourceErrorMessage(error: unknown): string {
  if (error instanceof HttpCodeTemplateSourceError) {
    if (error.reason === "desktop") return t("pluginContrib.httpCodeGenerators.desktopOnly");
    if (error.reason === "unavailable") return t("pluginContrib.httpCodeGenerators.unavailable");
  }
  return t("pluginContrib.httpCodeGenerators.renderFailed", { detail: error instanceof Error ? error.message : String(error) });
}

watch(
  () => [selectedGenerator.value?.key, selectedGenerator.value?.template] as const,
  async ([key, template]) => {
    const sequence = ++loadSequence;
    pluginLoadError.value = "";
    const generator = selectedGenerator.value;
    if (!key || !template || !generator) {
      pluginLoading.value = false;
      return;
    }
    if (pluginSource.value?.key === key && pluginSource.value.template === template) return;
    pluginLoading.value = true;
    try {
      const text = await loadHttpCodeTemplate(generator);
      if (sequence === loadSequence) pluginSource.value = { key, template, text };
    } catch (error) {
      if (sequence === loadSequence) pluginLoadError.value = sourceErrorMessage(error);
    } finally {
      if (sequence === loadSequence) pluginLoading.value = false;
    }
  },
);

function selectLanguage() {
  pluginKey.value = "";
}

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
    pluginKey.value = "";
    pluginSource.value = null;
    pluginLoadError.value = "";
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
          :class="{ 'is-inactive': pluginKey }"
          :options="HTTP_CODE_LANGUAGES"
          @change="selectLanguage"
        />
      </el-scrollbar>
      <el-select
        v-if="pluginOptions.length"
        v-model="pluginKey"
        class="http-code-dialog__plugins"
        clearable
        :placeholder="t('pluginContrib.httpCodeGenerators.group')"
        :aria-label="t('pluginContrib.httpCodeGenerators.group')"
      >
        <el-option v-for="option in pluginOptions" :key="option.value" :label="option.label" :value="option.value" />
      </el-select>
      <el-button :icon="CopyDocument" :disabled="!code" @click="copyCode">{{ t("common.copy") }}</el-button>
    </div>
    <el-alert
      v-if="pluginKey"
      class="http-code-dialog__notice"
      type="info"
      show-icon
      :closable="false"
      :title="t('pluginContrib.httpCodeGenerators.redactionNotice')"
    />
    <el-alert
      v-if="pluginKey && pluginResult.error"
      class="http-code-dialog__notice"
      type="error"
      show-icon
      :closable="false"
      :title="pluginResult.error"
      role="alert"
    />
    <el-scrollbar class="http-code-dialog__content" :aria-busy="pluginKey && pluginLoading ? 'true' : undefined">
      <pre><code>{{ pluginKey && pluginLoading && !code ? t("pluginContrib.httpCodeGenerators.loading") : code }}</code></pre>
    </el-scrollbar>
    <template #footer>
      <el-button type="primary" @click="emit('update:modelValue', false)">
        {{ t("common.close") }}
      </el-button>
    </template>
  </el-dialog>
</template>
