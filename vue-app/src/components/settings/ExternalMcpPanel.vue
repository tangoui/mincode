<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { ElMessage } from "element-plus";
import { Check, CopyDocument } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { useLocalProjectStore } from "@/stores/local-project";
import { disableExternalMcp, enableExternalMcp, externalMcpState, getExternalMcpClientExecutable, refreshExternalMcp } from "@/services/external-mcp-service";
import { copyText, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";

const { t } = useVueI18n();
const projects = useLocalProjectStore();
const count = computed(() => projects.projects.length);
const executable = ref("");
const transient = ref(false);
const executableLoading = ref(false);
const executableError = ref("");
const clientConfig = computed(() => executable.value
  ? JSON.stringify({ mcpServers: { mincode: { command: executable.value, args: ["--external-local-mcp"] } } }, null, 2)
  : "");
let disposed = false;
const copied = ref(false);
const copying = ref(false);
let copyFeedbackTimer: ReturnType<typeof setTimeout> | undefined;

async function loadExecutable() {
  executableLoading.value = true;
  executableError.value = "";
  try {
    const result = await getExternalMcpClientExecutable();
    if (!disposed) {
      executable.value = result.executable;
      transient.value = result.transient;
    }
  } catch (error) {
    if (!disposed) executableError.value = nativeErrorMessage(error);
  } finally {
    if (!disposed) executableLoading.value = false;
  }
}

async function copyConfig() {
  if (!clientConfig.value || copying.value) return;
  copying.value = true;
  try {
    await copyText(clientConfig.value);
    if (disposed) return;
    copied.value = true;
    clearTimeout(copyFeedbackTimer);
    copyFeedbackTimer = setTimeout(() => { copied.value = false; }, 2000);
  } catch (error) {
    if (!disposed) ElMessage.error(nativeErrorMessage(error));
  } finally {
    if (!disposed) copying.value = false;
  }
}

async function toggle(enabled: boolean) {
  try {
    if (enabled) await enableExternalMcp();
    else await disableExternalMcp();
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  }
}

onMounted(() => {
  if (isDesktopRuntime()) {
    void refreshExternalMcp().catch((error) => ElMessage.error(nativeErrorMessage(error)));
    void loadExecutable();
  }
});
onBeforeUnmount(() => {
  disposed = true;
  clearTimeout(copyFeedbackTimer);
});
</script>

<template>
  <section class="settings-flat-page">
    <header>
      <div class="settings-section-label">{{ t("settings.externalMcp.title") }}</div>
      <p class="settings-page-note">{{ t("settings.externalMcp.description") }}</p>
    </header>
    <el-alert v-if="!isDesktopRuntime()" type="warning" :closable="false" :title="t('settings.externalMcp.desktopOnly')" />
    <el-alert v-if="externalMcpState.error" type="error" :closable="false" :title="t('settings.externalMcp.failed', { message: externalMcpState.error })" />
    <div class="settings-data-row">
      <span>
        <strong>{{ t("settings.externalMcp.enable") }}</strong>
        <small class="settings-page-note">{{ t("settings.externalMcp.scope", { count }) }}</small>
      </span>
      <el-switch
        :model-value="externalMcpState.status?.enabled ?? false"
        :loading="externalMcpState.busy"
        :disabled="!isDesktopRuntime() || externalMcpState.busy || !externalMcpState.status"
        :aria-label="t('settings.externalMcp.enable')"
        @change="toggle(Boolean($event))"
      />
    </div>
    <p class="settings-page-note">
      {{ externalMcpState.status?.enabled
        ? t("settings.externalMcp.running", { count: externalMcpState.status.projectCount })
        : t("settings.externalMcp.stopped") }}
    </p>
    <div class="settings-card settings-external-mcp-config">
      <strong>{{ t("settings.externalMcp.configTitle") }}</strong>
      <p class="settings-page-note">{{ t("settings.externalMcp.configHint") }}</p>
      <p v-if="executableLoading" class="settings-page-note">{{ t("settings.externalMcp.configLoading") }}</p>
      <el-alert v-else-if="executableError" type="error" :closable="false" :title="t('settings.externalMcp.configUnavailable', { message: executableError })" />
      <p v-else-if="!clientConfig" class="settings-page-note">{{ t("settings.externalMcp.configDesktopOnly") }}</p>
      <template v-if="clientConfig">
        <el-alert v-if="transient" type="warning" :closable="false" :title="t('settings.externalMcp.transientPath')" />
        <div class="settings-external-mcp-config__snippet">
          <div class="settings-external-mcp-config__toolbar">
            <el-button size="small" text :icon="copied ? Check : CopyDocument" :loading="copying" :aria-label="t('settings.externalMcp.copyConfig')" @click="copyConfig">
              {{ t(copied ? 'settings.externalMcp.configCopied' : 'common.copy') }}
            </el-button>
          </div>
          <pre class="settings-external-mcp-config__code"><code>{{ clientConfig }}</code></pre>
        </div>
        <p class="settings-page-note">{{ t("settings.externalMcp.pathHint") }}</p>
      </template>
    </div>
  </section>
</template>
