<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Connection, Delete, Download, Lock, Upload } from "@element-plus/icons-vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  decodeConfigSyncSnapshot,
  encodeConfigSyncSnapshot,
  normalizeConfigSyncRepository,
  type ConfigSyncAuthMode,
  type ConfigSyncRepository,
  type ConfigSyncSnapshot,
} from "@/domain/settings/config-sync";
import { useVueI18n } from "@/i18n/messages";
import {
  clearConfigSyncRepository,
  loadConfigSyncRepository,
  pullConfigSyncSnapshot,
  pushConfigSyncSnapshot,
  saveConfigSyncRepository,
  testConfigSyncConnection,
} from "@/services/config-sync-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

const props = defineProps<{
  modelValue: boolean;
  createSnapshot: () => Promise<ConfigSyncSnapshot>;
  applySnapshot: (snapshot: ConfigSyncSnapshot) => Promise<void>;
}>();
const emit = defineEmits<{
  "update:modelValue": [value: boolean];
}>();
const { t } = useVueI18n();

const form = reactive<ConfigSyncRepository>(normalizeConfigSyncRepository(undefined));
const passwordConfirm = ref("");
const loading = ref(false);
const testing = ref(false);
const pulling = ref(false);
const pushing = ref(false);
const connected = ref(false);
const desktopRuntime = computed(() => isDesktopRuntime());
const passwordMismatch = computed(
  () => Boolean(passwordConfirm.value) && form.encryptionPassword !== passwordConfirm.value
);
const usesToken = computed(() => form.authMode === "https-token");
const canSync = computed(
  () =>
    desktopRuntime.value &&
    connected.value &&
    form.encryptionPassword.length >= 8 &&
    !passwordMismatch.value
);
const authOptions = computed<Array<{ value: ConfigSyncAuthMode; label: string }>>(() => [
  { value: "system", label: t("settings.data.configSyncAuthSystem") },
  { value: "https-token", label: t("settings.data.configSyncAuthToken") },
  { value: "ssh", label: t("settings.data.configSyncAuthSsh") },
]);
const lastSyncLabel = computed(() => {
  if (!form.lastSyncAt) return t("settings.data.configSyncNever");
  return new Intl.DateTimeFormat(undefined, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(form.lastSyncAt);
});

function errorText(error: unknown) {
  if (error instanceof Error) {
    if (error.message === "config_sync_password_too_short") {
      return t("settings.data.configSyncPasswordLength");
    }
    if (error.message === "config_sync_unsupported_version") {
      return t("settings.data.configSyncUnsupported");
    }
    if (error.message === "config_sync_invalid_payload") {
      return t("settings.data.configSyncInvalid");
    }
    if (
      error.message.startsWith("Decryption failed") ||
      error.message.startsWith("Unsupported encrypted payload")
    ) {
      return t("settings.data.configSyncDecryptFailed");
    }
    return error.message;
  }
  return String(error);
}

async function hydrate() {
  loading.value = true;
  try {
    Object.assign(form, await loadConfigSyncRepository());
    passwordConfirm.value = form.encryptionPassword;
    connected.value = Boolean(form.remoteUrl);
  } catch (error) {
    ElMessage.error(t("settings.data.configSyncLoadFailed", { message: errorText(error) }));
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.modelValue,
  (open) => {
    if (open) void hydrate();
  },
  { immediate: true }
);

function validateForm() {
  if (!desktopRuntime.value) throw new Error(t("common.desktopRequired"));
  if (!form.remoteUrl.trim()) throw new Error(t("settings.data.configSyncRemoteRequired"));
  if (!form.branch.trim()) throw new Error(t("settings.data.configSyncBranchRequired"));
  if (usesToken.value && (!form.username.trim() || !form.token)) {
    throw new Error(t("settings.data.configSyncTokenRequired"));
  }
  if (form.encryptionPassword.length < 8) {
    throw new Error(t("settings.data.configSyncPasswordLength"));
  }
  if (form.encryptionPassword !== passwordConfirm.value) {
    throw new Error(t("settings.data.configSyncPasswordMismatch"));
  }
}

async function persistConnection() {
  await saveConfigSyncRepository({ ...form });
}

async function testAndSave() {
  testing.value = true;
  try {
    validateForm();
    const result = await testConfigSyncConnection(form);
    connected.value = result.ok;
    await persistConnection();
    ElMessage.success(
      result.repository_empty
        ? t("settings.data.configSyncConnectedEmpty")
        : t("settings.data.configSyncConnected")
    );
  } catch (error) {
    connected.value = false;
    ElMessage.error(t("settings.data.configSyncConnectFailed", { message: errorText(error) }));
  } finally {
    testing.value = false;
  }
}

async function push() {
  pushing.value = true;
  try {
    validateForm();
    const snapshot = await props.createSnapshot();
    const payload = await encodeConfigSyncSnapshot(snapshot, form.encryptionPassword);
    const result = await pushConfigSyncSnapshot(form, payload);
    form.lastSyncAt = Date.now();
    form.lastCommit = result.commit;
    connected.value = true;
    await persistConnection();
    ElMessage.success(
      result.changed
        ? t("settings.data.configSyncPushed")
        : t("settings.data.configSyncNoChanges")
    );
  } catch (error) {
    ElMessage.error(t("settings.data.configSyncPushFailed", { message: errorText(error) }));
  } finally {
    pushing.value = false;
  }
}

async function pull() {
  pulling.value = true;
  try {
    validateForm();
    const result = await pullConfigSyncSnapshot(form);
    if (!result.exists) {
      ElMessage.warning(t("settings.data.configSyncRemoteEmpty"));
      return;
    }
    const snapshot = await decodeConfigSyncSnapshot(result.payload, form.encryptionPassword);
    await ElMessageBox.confirm(
      t("settings.data.configSyncApplyHint"),
      t("settings.data.configSyncApplyTitle"),
      { type: "warning" }
    );
    await props.applySnapshot(snapshot);
    form.lastSyncAt = Date.now();
    form.lastCommit = result.commit;
    connected.value = true;
    await persistConnection();
    ElMessage.success(t("settings.data.configSyncPulled"));
  } catch (error) {
    if (error === "cancel" || error === "close") return;
    ElMessage.error(t("settings.data.configSyncPullFailed", { message: errorText(error) }));
  } finally {
    pulling.value = false;
  }
}

async function disconnect() {
  try {
    await ElMessageBox.confirm(
      t("settings.data.configSyncDisconnectHint"),
      t("settings.data.configSyncDisconnect"),
      { type: "warning" }
    );
    await clearConfigSyncRepository();
    Object.assign(form, normalizeConfigSyncRepository(undefined));
    passwordConfirm.value = "";
    connected.value = false;
    ElMessage.success(t("settings.data.configSyncDisconnected"));
  } catch (error) {
    if (error === "cancel" || error === "close") return;
    ElMessage.error(errorText(error));
  }
}
</script>

<template>
  <el-dialog
    class="config-sync-dialog"
    :model-value="modelValue"
    width="min(720px, calc(100vw - 40px))"
    align-center
    append-to-body
    destroy-on-close
    @update:model-value="emit('update:modelValue', $event)"
  >
    <template #header>
      <strong>{{ t("settings.data.configSyncDialogTitle") }}</strong>
    </template>

    <div v-loading="loading" class="config-sync-dialog__body">
      <el-alert
        v-if="!desktopRuntime"
        type="warning"
        :closable="false"
        :title="t('settings.data.configSyncDesktopOnly')"
      />
      <el-alert
        v-else
        type="warning"
        :closable="false"
        :title="t('settings.data.configSyncPrivateHint')"
      />

      <el-form class="config-sync-dialog__form" label-position="top">
        <el-form-item :label="t('settings.data.configSyncRemote')">
          <el-input
            v-model="form.remoteUrl"
            :placeholder="t('settings.data.configSyncRemotePlaceholder')"
            autocomplete="off"
            :disabled="!desktopRuntime"
            @input="connected = false"
          />
          <small>{{ t("settings.data.configSyncRemoteHint") }}</small>
        </el-form-item>

        <div class="config-sync-dialog__grid">
          <el-form-item :label="t('settings.data.configSyncAuth')">
            <el-select v-model="form.authMode" :disabled="!desktopRuntime" @change="connected = false">
              <el-option
                v-for="option in authOptions"
                :key="option.value"
                :value="option.value"
                :label="option.label"
              />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('settings.data.configSyncBranch')">
            <el-input v-model="form.branch" :disabled="!desktopRuntime" @input="connected = false" />
          </el-form-item>
        </div>

        <div v-if="usesToken" class="config-sync-dialog__grid">
          <el-form-item :label="t('settings.data.configSyncUsername')">
            <el-input v-model="form.username" autocomplete="username" @input="connected = false" />
          </el-form-item>
          <el-form-item :label="t('settings.data.configSyncToken')">
            <el-input
              v-model="form.token"
              type="password"
              show-password
              autocomplete="new-password"
              @input="connected = false"
            />
          </el-form-item>
        </div>

        <div class="config-sync-dialog__grid">
          <el-form-item :label="t('settings.data.configSyncPassword')">
            <el-input
              v-model="form.encryptionPassword"
              type="password"
              show-password
              autocomplete="new-password"
            />
          </el-form-item>
          <el-form-item :label="t('settings.data.configSyncPasswordConfirm')">
            <el-input
              v-model="passwordConfirm"
              type="password"
              show-password
              autocomplete="new-password"
              :class="{ 'is-error': passwordMismatch }"
            />
          </el-form-item>
        </div>
        <div class="config-sync-dialog__security-hint">
          <el-icon><Lock /></el-icon>
          <span>{{ t("settings.data.configSyncPasswordHint") }}</span>
          <el-checkbox v-model="form.rememberPassword">
            {{ t("settings.data.configSyncRememberPassword") }}
          </el-checkbox>
        </div>
      </el-form>

      <dl v-if="form.remoteUrl" class="config-sync-dialog__status">
        <div>
          <dt>{{ t("settings.data.configSyncStatus") }}</dt>
          <dd>{{ connected ? t("settings.data.configSyncReady") : t("settings.data.configSyncUnverified") }}</dd>
        </div>
        <div>
          <dt>{{ t("settings.data.configSyncLastSync") }}</dt>
          <dd>{{ lastSyncLabel }}</dd>
        </div>
        <div v-if="form.lastCommit">
          <dt>{{ t("settings.data.configSyncCommit") }}</dt>
          <dd><code>{{ form.lastCommit.slice(0, 12) }}</code></dd>
        </div>
      </dl>
    </div>

    <template #footer>
      <div class="config-sync-dialog__footer">
        <el-button
          v-if="form.remoteUrl"
          text
          type="danger"
          :icon="Delete"
          :disabled="pulling || pushing || testing"
          @click="disconnect"
        >
          {{ t("settings.data.configSyncDisconnect") }}
        </el-button>
        <span />
        <el-button :icon="Connection" :loading="testing" :disabled="!desktopRuntime" @click="testAndSave">
          {{ t("settings.data.configSyncTest") }}
        </el-button>
        <el-button :icon="Download" :loading="pulling" :disabled="!canSync || pushing" @click="pull">
          {{ t("settings.data.configSyncPull") }}
        </el-button>
        <el-button type="primary" :icon="Upload" :loading="pushing" :disabled="!canSync || pulling" @click="push">
          {{ t("settings.data.configSyncPush") }}
        </el-button>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped>
.config-sync-dialog__body {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.config-sync-dialog__form {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.config-sync-dialog__form :deep(.el-form-item) {
  margin-bottom: 14px;
}

.config-sync-dialog__form :deep(.el-form-item__label) {
  color: var(--app-text);
  font-size: var(--app-ui-font-size-small);
  font-weight: 550;
}

.config-sync-dialog__form small {
  margin-top: 5px;
  color: var(--app-text-muted);
  font-size: var(--app-ui-font-size-extra-small);
  line-height: 1.45;
}

.config-sync-dialog__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 14px;
}

.config-sync-dialog__security-hint {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 7px;
  color: var(--app-text-muted);
  font-size: var(--app-ui-font-size-extra-small);
  line-height: 1.45;
}

.config-sync-dialog__security-hint .el-icon {
  color: var(--app-accent);
}

.config-sync-dialog__status {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin: 0;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 7px;
  background: var(--app-muted);
}

.config-sync-dialog__status > div {
  min-width: 0;
  padding: 9px 11px;
  border-right: 1px solid var(--app-border);
}

.config-sync-dialog__status > div:last-child {
  border-right: 0;
}

.config-sync-dialog__status dt,
.config-sync-dialog__status dd {
  margin: 0;
}

.config-sync-dialog__status dt {
  color: var(--app-text-muted);
  font-size: var(--app-ui-font-size-extra-small);
}

.config-sync-dialog__status dd {
  margin-top: 4px;
  overflow: hidden;
  color: var(--app-text);
  font-size: var(--app-ui-font-size-small);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.config-sync-dialog__footer {
  display: grid;
  grid-template-columns: auto 1fr auto auto auto;
  gap: 8px;
  align-items: center;
}

@media (max-width: 680px) {
  .config-sync-dialog__grid,
  .config-sync-dialog__status {
    grid-template-columns: minmax(0, 1fr);
  }

  .config-sync-dialog__status > div {
    border-right: 0;
    border-bottom: 1px solid var(--app-border);
  }

  .config-sync-dialog__status > div:last-child {
    border-bottom: 0;
  }

  .config-sync-dialog__security-hint {
    grid-template-columns: auto minmax(0, 1fr);
  }

  .config-sync-dialog__security-hint .el-checkbox {
    grid-column: 1 / -1;
  }

  .config-sync-dialog__footer {
    grid-template-columns: 1fr 1fr;
  }

  .config-sync-dialog__footer > span {
    display: none;
  }
}
</style>
