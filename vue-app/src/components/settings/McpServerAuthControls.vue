<script setup lang="ts">
/**
 * Settings → AI → Connectors: sign-in state of one remote connector
 * (spec §7.2). Signs in through the host (browser or device flow), shows the
 * device code while the flow waits, and signs out by deleting this device's
 * tokens. Tokens never reach this component.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { mcpAuthErrorCode, mcpAuthErrorMessageKey } from "@/domain/ai/mcp-auth";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import {
  mcpAuthStatus,
  onMcpDeviceCode,
  signInMcpServer,
  signOutMcpServer,
  type McpAuthStatus,
  type McpAuthTarget,
  type McpDeviceCode,
} from "@/services/mcp-auth-service";
import { copyText, nativeErrorMessage } from "@/services/desktop-bridge";
import { openSystemBrowser } from "@/services/web-automation-service";

const props = defineProps<{
  server: McpAuthTarget;
  /** The last connect failed with [MCP_AUTH_REQUIRED]; this wins over a stored sign-in. */
  authRequired?: boolean;
}>();
const emit = defineEmits<{ "signed-in": []; "signed-out": [] }>();

const { t } = useVueI18n();
const status = ref<McpAuthStatus | null>(null);
const busy = ref<"sign-in" | "sign-out" | null>(null);
const deviceCode = ref<McpDeviceCode | null>(null);
const errorText = ref("");
let controller: AbortController | null = null;
let stopDeviceCode: (() => void) | null = null;

const displayStatus = computed<McpAuthStatus | null>(() =>
  props.authRequired && status.value === "signed-in" ? "needs-login" : status.value,
);
const statusTagType = computed(() =>
  displayStatus.value === "signed-in" ? "success" : displayStatus.value === "needs-login" ? "warning" : "info",
);
const canSignOut = computed(() => status.value === "signed-in" || status.value === "needs-login");

function describeError(error: unknown) {
  const code = mcpAuthErrorCode(error);
  const key = mcpAuthErrorMessageKey(code) as MessageKey;
  return code && !key.endsWith(".generic") ? t(key) : t(key, { message: nativeErrorMessage(error) });
}

async function refreshStatus() {
  try {
    status.value = await mcpAuthStatus(props.server);
  } catch (error) {
    status.value = null;
    errorText.value = describeError(error);
  }
}

function stopListening() {
  stopDeviceCode?.();
  stopDeviceCode = null;
}

async function signIn() {
  if (busy.value) return;
  busy.value = "sign-in";
  errorText.value = "";
  deviceCode.value = null;
  const current = new AbortController();
  controller = current;
  stopDeviceCode = onMcpDeviceCode(props.server.id, (code) => {
    deviceCode.value = code;
  });
  const result = await signInMcpServer(props.server, {
    signal: current.signal,
    onError: (error) => {
      errorText.value = describeError(error);
    },
  });
  if (controller !== current) return;
  controller = null;
  stopListening();
  deviceCode.value = null;
  busy.value = null;
  if (result === "signed-in") {
    status.value = "signed-in";
    ElMessage.success(t("settings.mcp.auth.signedIn", { name: props.server.id }));
    emit("signed-in");
  } else if (result === "failed") {
    await refreshStatus();
  }
}

function cancelSignIn() {
  controller?.abort();
}

async function copyAndOpen() {
  const code = deviceCode.value;
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

async function signOut() {
  if (busy.value) return;
  busy.value = "sign-out";
  errorText.value = "";
  try {
    await signOutMcpServer(props.server);
    status.value = "signed-out";
    ElMessage.success(t("settings.mcp.auth.signedOut", { name: props.server.id }));
    emit("signed-out");
  } catch (error) {
    ElMessage.error(t("settings.mcp.auth.signOutFailed", { message: nativeErrorMessage(error) }));
  } finally {
    busy.value = null;
  }
}

onMounted(refreshStatus);
watch(() => [props.server.id, props.server.url], () => {
  // A different connector or address: a login in progress was for the old one.
  cancelSignIn();
  errorText.value = "";
  void refreshStatus();
});
onBeforeUnmount(() => {
  cancelSignIn();
  stopListening();
});
</script>

<template>
  <div class="mcp-auth-controls" role="group" :aria-label="t('settings.mcp.auth.label')">
    <div class="mcp-auth-controls__row">
      <el-tag size="small" effect="plain" :type="statusTagType" data-mcp-auth-status>{{
        displayStatus ? t(`settings.mcp.auth.status.${displayStatus}`) : t("settings.mcp.auth.status.unknown")
      }}</el-tag>
      <div class="mcp-auth-controls__actions">
        <el-button
          v-if="displayStatus !== 'signed-in'"
          size="small"
          type="primary"
          plain
          :loading="busy === 'sign-in'"
          :disabled="busy === 'sign-out'"
          data-mcp-auth-action="sign-in"
          @click="signIn"
          >{{ displayStatus === "needs-login" ? t("settings.mcp.auth.signInAgain") : t("settings.mcp.auth.signIn") }}</el-button
        >
        <el-button v-if="busy === 'sign-in'" size="small" data-mcp-auth-action="cancel" @click="cancelSignIn">{{
          t("settings.mcp.auth.cancel")
        }}</el-button>
        <el-button
          v-else-if="canSignOut"
          size="small"
          :loading="busy === 'sign-out'"
          data-mcp-auth-action="sign-out"
          @click="signOut"
          >{{ t("settings.mcp.auth.signOut") }}</el-button
        >
      </div>
    </div>
    <p v-if="busy === 'sign-in' && !deviceCode" class="mcp-auth-controls__hint">{{ t("settings.mcp.auth.waiting") }}</p>
    <div v-if="busy === 'sign-in' && deviceCode" class="mcp-auth-controls__device">
      <span class="mcp-auth-controls__hint">{{ t("settings.mcp.auth.deviceHint") }}</span>
      <code class="mcp-auth-controls__code" data-mcp-device-code>{{ deviceCode.userCode }}</code>
      <el-button size="small" type="primary" data-mcp-auth-action="copy-open" @click="copyAndOpen">{{
        t("settings.mcp.auth.copyAndOpen")
      }}</el-button>
    </div>
    <p v-if="errorText" class="mcp-auth-controls__error" role="alert" data-mcp-auth-error>{{ errorText }}</p>
  </div>
</template>

<style scoped>
.mcp-auth-controls {
  display: grid;
  gap: 6px;
}

.mcp-auth-controls__row,
.mcp-auth-controls__device {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.mcp-auth-controls__row {
  justify-content: space-between;
}

.mcp-auth-controls__actions {
  display: flex;
  gap: 6px;
}

.mcp-auth-controls__actions .el-button + .el-button {
  margin-left: 0;
}

.mcp-auth-controls__hint,
.mcp-auth-controls__error {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
}

.mcp-auth-controls__hint {
  color: var(--app-text-muted);
}

.mcp-auth-controls__error {
  color: var(--el-color-danger);
  overflow-wrap: anywhere;
}

.mcp-auth-controls__code {
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
