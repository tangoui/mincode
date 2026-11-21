<script setup lang="ts">
// ai.config login (spec §7.2): a plugin page asked the host to sign in to a
// connector it installed. The host frame runs the sign-in and shows this
// dialog: which plugin asked, for which connector and server. It always asks
// first (phase "confirm") and nothing opens before Continue; while signing in it shows the device code when the server uses the
// device flow, and a cancel. The plugin only learns whether the sign-in
// succeeded; nothing here goes back to it.
import { computed } from "vue";
import { ElButton, ElDialog, ElMessage } from "element-plus";
import { visibleReviewText } from "@/domain/plugin-ui/ai-config";
import { useVueI18n } from "@/i18n/messages";
import type { McpDeviceCode } from "@/services/mcp-auth-service";
import { copyText, nativeErrorMessage } from "@/services/desktop-bridge";
import { openSystemBrowser } from "@/services/web-automation-service";

const props = defineProps<{
  pluginName: string;
  connector: string;
  /** Host of the connector's address. */
  host: string;
  phase: "confirm" | "signing-in";
  deviceCode: McpDeviceCode | null;
}>();
const emit = defineEmits<{ continue: []; cancel: [] }>();

const { t } = useVueI18n();

const visible = computed({
  get: () => true,
  set: (next: boolean) => {
    if (!next) emit("cancel");
  },
});
// Names and address come from outside the host (manifest, connector settings): make hidden characters visible.
const title = computed(() => t("plugins.aiConfig.signIn.title", { plugin: visibleReviewText(props.pluginName) }));
const description = computed(() =>
  t(props.phase === "confirm" ? "plugins.aiConfig.signIn.confirm" : "plugins.aiConfig.signIn.description", {
    plugin: visibleReviewText(props.pluginName),
    connector: visibleReviewText(props.connector),
    host: visibleReviewText(props.host),
  }),
);

async function copyAndOpen() {
  const code = props.deviceCode;
  if (!code) return;
  try {
    await copyText(code.userCode);
  } catch {
    ElMessage.warning(t("settings.mcp.auth.copyFailed"));
  }
  try {
    await openSystemBrowser(code.verificationUri);
  } catch (error) {
    ElMessage.error(t("settings.mcp.auth.openFailed", { message: nativeErrorMessage(error) }));
  }
}
</script>

<template>
  <ElDialog
    v-model="visible"
    :title="title"
    width="440px"
    :close-on-click-modal="false"
    append-to-body
    class="app-plugin-mcp-sign-in"
  >
    <div class="app-plugin-mcp-sign-in__body" aria-live="polite">
      <p>{{ description }}</p>
      <template v-if="phase === 'signing-in'">
        <p v-if="!deviceCode" class="app-plugin-mcp-sign-in__hint">{{ t("settings.mcp.auth.waiting") }}</p>
        <div v-else class="app-plugin-mcp-sign-in__device">
          <span class="app-plugin-mcp-sign-in__hint">{{ t("settings.mcp.auth.deviceHint") }}</span>
          <code class="app-plugin-mcp-sign-in__code" data-mcp-device-code>{{ deviceCode.userCode }}</code>
          <ElButton size="small" type="primary" data-mcp-auth-action="copy-open" @click="copyAndOpen">{{
            t("settings.mcp.auth.copyAndOpen")
          }}</ElButton>
        </div>
      </template>
    </div>
    <template #footer>
      <ElButton data-mcp-auth-action="cancel" @click="emit('cancel')">{{ t("settings.mcp.auth.cancel") }}</ElButton>
      <ElButton v-if="phase === 'confirm'" type="primary" data-mcp-auth-action="continue" @click="emit('continue')">{{
        t("plugins.aiConfig.signIn.continue")
      }}</ElButton>
    </template>
  </ElDialog>
</template>

<style scoped>
.app-plugin-mcp-sign-in__body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.app-plugin-mcp-sign-in__body p {
  margin: 0;
  color: var(--app-text);
  overflow-wrap: anywhere;
}

.app-plugin-mcp-sign-in__body .app-plugin-mcp-sign-in__hint {
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}

.app-plugin-mcp-sign-in__device {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.app-plugin-mcp-sign-in__code {
  padding: 2px 8px;
  border: 1px solid var(--app-border-strong);
  border-radius: 4px;
  color: var(--app-text);
  font-family: var(--app-font-mono);
  font-size: 14px;
  letter-spacing: 0.08em;
  user-select: all;
}
</style>
