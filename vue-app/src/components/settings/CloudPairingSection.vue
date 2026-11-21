<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useId, watch } from "vue";
import QRCode from "qrcode";
import { ElMessage, ElMessageBox } from "element-plus";
import { Check, CopyDocument, Refresh } from "@element-plus/icons-vue";
import {
  CLOUD_ALWAYS_GRANTED_CAPABILITY,
  CLOUD_PAIRING_CAPABILITIES,
  cloudCreatePairing,
  cloudPairedDevices,
  cloudPairingCode,
  cloudPairingQrContent,
  cloudRevokeDevice,
  isCloudPairingCapability,
  onCloudRealtimeError,
  type CloudDeviceRegistrationState,
  type CloudPairedDevice,
  type CloudPairingCapability,
  type CloudPairingChallenge,
} from "@/services/cloud-companion-service";
import { copyText, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  /** CloudStatus.deviceRegistration of this desktop. */
  registration?: CloudDeviceRegistrationState;
  /** CloudStatus.deviceRegistrationError, shown when registration failed. */
  registrationError?: string;
}>();

const { t, intlLocale } = useVueI18n();
const headingId = useId();

// While a code is live the paired list is re-read so a phone that just paired shows up on its own.
const PAIRED_POLL_SECONDS = 5;

const selectedCapabilities = ref<CloudPairingCapability[]>([CLOUD_ALWAYS_GRANTED_CAPABILITY]);
const challenge = ref<CloudPairingChallenge | null>(null);
const creating = ref(false);
const copied = ref(false);
const nowSeconds = ref(Math.floor(Date.now() / 1000));
let ticker: ReturnType<typeof setInterval> | undefined;
let copiedTimer: ReturnType<typeof setTimeout> | undefined;
let ticks = 0;

const devices = ref<CloudPairedDevice[]>([]);
const devicesLoaded = ref(false);
const devicesLoading = ref(false);
const devicesError = ref("");
const revoking = ref("");
let devicesGeneration = 0;
let disposed = false;

const relayError = ref("");
let stopRelayErrors: (() => void) | undefined;

const pairingCode = computed(() => (challenge.value ? cloudPairingCode(challenge.value) : ""));
const remainingSeconds = computed(() => (challenge.value ? Math.max(0, challenge.value.expires_at - nowSeconds.value) : 0));
const expired = computed(() => Boolean(challenge.value) && remainingSeconds.value <= 0);
const qrDataUrl = ref("");
let qrGeneration = 0;

// 配对码变化时生成二维码（SVG，不依赖 canvas）；配对码清空或过期后不显示。
watch(pairingCode, async (code) => {
  const generation = ++qrGeneration;
  qrDataUrl.value = "";
  if (!code) return;
  try {
    const svg = await QRCode.toString(cloudPairingQrContent(code), { type: "svg", margin: 1, errorCorrectionLevel: "M" });
    if (generation === qrGeneration) qrDataUrl.value = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
  } catch {
    // 生成失败时仍可复制文本配对码。
  }
});
const countdown = computed(() => {
  const seconds = remainingSeconds.value;
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
});
const grantedCapabilities = computed(() => {
  const granted = challenge.value?.capabilities ?? [];
  // The cloud always adds AI_CHAT; show it even if an older server omitted it from the echo.
  return granted.includes(CLOUD_ALWAYS_GRANTED_CAPABILITY) ? granted : [CLOUD_ALWAYS_GRANTED_CAPABILITY, ...granted];
});

function capabilityLabel(capability: string) {
  return isCloudPairingCapability(capability) ? t(`settings.cloud.capability.${capability}`) : capability;
}

function formatTime(seconds?: number) {
  if (!seconds) return "—";
  return new Date(seconds * 1000).toLocaleString(intlLocale.value, { hour12: false });
}

function deviceName(device: CloudPairedDevice) {
  return device.name || device.device_model || t("settings.cloud.unnamedDevice");
}

function devicePlatform(device: CloudPairedDevice) {
  const os = ({ android: "Android", ios: "iOS", darwin: "macOS", windows: "Windows", linux: "Linux" } as Record<string, string>)[
    device.platform.toLowerCase()
  ] ?? device.platform;
  return [[os, device.os_version].filter(Boolean).join(" "), device.device_model].filter(Boolean).join(" · ") || "—";
}

function stopTicker() {
  if (ticker !== undefined) clearInterval(ticker);
  ticker = undefined;
}

function startTicker() {
  stopTicker();
  ticks = 0;
  nowSeconds.value = Math.floor(Date.now() / 1000);
  ticker = setInterval(() => {
    nowSeconds.value = Math.floor(Date.now() / 1000);
    if (expired.value) {
      stopTicker();
      return;
    }
    ticks += 1;
    if (ticks % PAIRED_POLL_SECONDS === 0 && !devicesLoading.value) void loadDevices({ quiet: true });
  }, 1000);
}

async function createPairing() {
  if (!isDesktopRuntime() || creating.value) return;
  creating.value = true;
  try {
    // AI_CHAT is implied by the cloud; send only the optional capabilities.
    const optional = selectedCapabilities.value.filter((capability) => capability !== CLOUD_ALWAYS_GRANTED_CAPABILITY);
    const result = await cloudCreatePairing(optional);
    if (disposed) return;
    challenge.value = result;
    copied.value = false;
    startTicker();
  } catch (error) {
    if (!disposed) ElMessage.error(nativeErrorMessage(error));
  } finally {
    creating.value = false;
  }
}

async function copyPairingCode() {
  if (!pairingCode.value || expired.value) return;
  try {
    await copyText(pairingCode.value);
    copied.value = true;
    ElMessage.success(t("settings.cloud.pairingCopied"));
    if (copiedTimer !== undefined) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => {
      copied.value = false;
      copiedTimer = undefined;
    }, 2000);
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  }
}

async function loadDevices(options: { quiet?: boolean } = {}) {
  if (!isDesktopRuntime()) return;
  const generation = ++devicesGeneration;
  // Pairing always issues a new grant, so a re-paired phone (same device_id) is detected by its grant_id.
  const known = new Set(devices.value.map((device) => device.grant_id));
  devicesLoading.value = true;
  if (!options.quiet) devicesError.value = "";
  try {
    const result = await cloudPairedDevices();
    if (generation !== devicesGeneration || disposed) return;
    const fresh = devicesLoaded.value && challenge.value ? result.find((device) => !known.has(device.grant_id)) : undefined;
    devices.value = result;
    devicesLoaded.value = true;
    devicesError.value = "";
    if (fresh) {
      // The pairing code is single-use: once a new phone appears it has been consumed.
      ElMessage.success(t("settings.cloud.pairingSucceeded", { name: deviceName(fresh) }));
      challenge.value = null;
      stopTicker();
    }
  } catch (error) {
    // A failed background poll keeps the last list instead of replacing it with an error.
    if (generation === devicesGeneration && !disposed && !options.quiet) devicesError.value = nativeErrorMessage(error);
  } finally {
    if (generation === devicesGeneration) devicesLoading.value = false;
  }
}

async function unpair(device: CloudPairedDevice) {
  const name = deviceName(device);
  try {
    await ElMessageBox.confirm(t("settings.cloud.unpairConfirm", { name }), t("settings.cloud.unpair"), {
      type: "warning",
      confirmButtonText: t("settings.cloud.unpair"),
      cancelButtonText: t("common.cancel"),
    });
  } catch {
    return;
  }
  revoking.value = device.device_id;
  try {
    await cloudRevokeDevice(device.device_id);
    if (disposed) return;
    ElMessage.success(t("settings.cloud.unpaired"));
    await loadDevices();
  } catch (error) {
    if (!disposed) ElMessage.error(nativeErrorMessage(error));
  } finally {
    revoking.value = "";
  }
}

onMounted(() => {
  void loadDevices();
  if (isDesktopRuntime()) {
    stopRelayErrors = onCloudRealtimeError((error) => {
      relayError.value = error.message || t("settings.cloud.relayErrorCode", { code: error.code });
    });
  }
});

onBeforeUnmount(() => {
  disposed = true;
  devicesGeneration += 1;
  stopTicker();
  if (copiedTimer !== undefined) clearTimeout(copiedTimer);
  stopRelayErrors?.();
  // Do not keep the one-time secret around after the panel closes.
  challenge.value = null;
});
</script>

<template>
  <section class="settings-cloud-pairing" :aria-labelledby="headingId">
    <div class="settings-cloud-pairing__head">
      <strong :id="headingId">{{ t("settings.cloud.pairing") }}</strong>
    </div>
    <p class="settings-page-note">{{ t("settings.cloud.pairingHint") }}</p>

    <el-alert
      v-if="props.registration === 'pending'"
      type="info"
      show-icon
      :closable="false"
      :title="t('settings.cloud.registrationPending')"
    />
    <el-alert
      v-else-if="props.registration === 'failed'"
      type="warning"
      show-icon
      :closable="false"
      :title="t('settings.cloud.registrationFailed')"
      :description="props.registrationError || undefined"
    />
    <el-alert
      v-if="relayError"
      type="warning"
      show-icon
      :title="t('settings.cloud.relayErrorTitle')"
      :description="relayError"
      @close="relayError = ''"
    />

    <div class="settings-cloud-pairing__capabilities">
      <span class="settings-cloud-pairing__label">{{ t("settings.cloud.pairingCapabilities") }}</span>
      <el-checkbox-group v-model="selectedCapabilities" :aria-label="t('settings.cloud.pairingCapabilities')">
        <el-checkbox
          v-for="capability in CLOUD_PAIRING_CAPABILITIES"
          :key="capability"
          :value="capability"
          :disabled="capability === CLOUD_ALWAYS_GRANTED_CAPABILITY || creating"
        >
          {{ capabilityLabel(capability) }}
        </el-checkbox>
      </el-checkbox-group>
      <small>{{ t("settings.cloud.capabilityAlwaysOn") }}</small>
    </div>

    <div class="settings-cloud-pairing__actions">
      <el-button type="primary" :loading="creating" :disabled="!isDesktopRuntime()" @click="createPairing">
        {{ challenge ? t("settings.cloud.regeneratePairing") : t("settings.cloud.createPairing") }}
      </el-button>
    </div>

    <div v-if="challenge" class="settings-cloud-pairing__code">
      <div v-if="qrDataUrl && !expired" class="settings-cloud-pairing__qr">
        <img :src="qrDataUrl" :alt="t('settings.cloud.pairingQrAlt')" width="168" height="168" />
        <small>{{ t("settings.cloud.pairingQrHint") }}</small>
      </div>
      <el-input
        :model-value="pairingCode"
        readonly
        :disabled="expired"
        class="settings-cloud-pairing__code-input"
        :aria-label="t('settings.cloud.pairingCode')"
      >
        <template #append>
          <el-button
            :icon="copied ? Check : CopyDocument"
            :disabled="expired"
            :aria-label="t('settings.cloud.copyPairingCode')"
            :title="t('settings.cloud.copyPairingCode')"
            @click="copyPairingCode"
          />
        </template>
      </el-input>
      <div class="settings-cloud-pairing__code-meta">
        <span v-if="expired" class="settings-cloud-pairing__expired" role="status">{{ t("settings.cloud.pairingExpired") }}</span>
        <span v-else role="timer">{{ t("settings.cloud.pairingExpiresIn", { time: countdown }) }}</span>
        <span class="settings-cloud-pairing__granted">
          {{ t("settings.cloud.pairingGranted") }}
          <el-tag v-for="capability in grantedCapabilities" :key="capability" size="small" effect="plain">{{ capabilityLabel(capability) }}</el-tag>
        </span>
      </div>
    </div>

    <div class="settings-cloud-pairing__head">
      <strong>{{ t("settings.cloud.pairedDevices") }}</strong>
      <el-button
        text
        size="small"
        :icon="Refresh"
        :loading="devicesLoading && devicesLoaded"
        :disabled="devicesLoading || !isDesktopRuntime()"
        :aria-label="t('settings.cloud.pairedDevicesRefresh')"
        :title="t('settings.cloud.pairedDevicesRefresh')"
        @click="loadDevices()"
      />
    </div>
    <div v-loading="devicesLoading && !devicesLoaded" class="settings-cloud-pairing__list">
      <el-alert v-if="devicesError" type="error" show-icon :closable="false" :title="t('settings.cloud.pairedDevicesFailed')">
        <!-- The default slot replaces `description`, so the message and the retry share it. -->
        <div class="settings-cloud-pairing__error">
          <span>{{ devicesError }}</span>
          <el-button size="small" @click="loadDevices()">{{ t("settings.cloud.retry") }}</el-button>
        </div>
      </el-alert>
      <p v-else-if="devicesLoaded && !devices.length" class="settings-page-note">{{ t("settings.cloud.pairedDevicesEmpty") }}</p>
      <div v-for="device in devices" :key="device.device_id" class="settings-cloud-pairing__device">
        <div class="settings-cloud-pairing__device-text">
          <strong>{{ deviceName(device) }}</strong>
          <small>{{ devicePlatform(device) }} · {{ t("settings.cloud.pairedLastSeen", { time: formatTime(device.last_seen_at) }) }}</small>
          <span class="settings-cloud-pairing__tags">
            <el-tag
              v-for="capability in device.capabilities"
              :key="capability"
              size="small"
              :type="capability === CLOUD_ALWAYS_GRANTED_CAPABILITY ? 'success' : 'info'"
              effect="plain"
            >
              {{ capabilityLabel(capability) }}
            </el-tag>
          </span>
        </div>
        <el-button
          text
          type="danger"
          size="small"
          :loading="revoking === device.device_id"
          :disabled="!!revoking"
          @click="unpair(device)"
        >
          {{ t("settings.cloud.unpair") }}
        </el-button>
      </div>
    </div>
  </section>
</template>

<style scoped>
.settings-cloud-pairing { display: grid; gap: 10px; padding: 12px 14px; border-radius: 8px; background: var(--el-fill-color-light); }
.settings-cloud-pairing .settings-page-note { margin: 0; }
.settings-cloud-pairing small { display: block; color: var(--el-text-color-secondary); }
.settings-cloud-pairing__head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 24px; }
.settings-cloud-pairing__capabilities { display: grid; gap: 4px; }
.settings-cloud-pairing__label { color: var(--el-text-color-regular); font-size: 12px; }
.settings-cloud-pairing__actions { display: flex; align-items: center; gap: 8px; }
.settings-cloud-pairing__code { display: grid; gap: 6px; }
.settings-cloud-pairing__qr { display: flex; align-items: center; gap: 14px; }
.settings-cloud-pairing__qr img { flex: none; border-radius: 8px; background: #fff; padding: 6px; border: 1px solid var(--el-border-color-lighter); }
.settings-cloud-pairing__qr small { color: var(--el-text-color-secondary); line-height: 1.6; }
.settings-cloud-pairing__code-input :deep(.el-input__inner) { font-family: var(--app-font-mono); }
.settings-cloud-pairing__code-meta { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; color: var(--el-text-color-secondary); font-size: 12px; }
.settings-cloud-pairing__expired { color: var(--el-color-warning); }
.settings-cloud-pairing__granted { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 4px; }
.settings-cloud-pairing__error { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.settings-cloud-pairing__list {display: grid; gap: 8px; min-height: 32px; }
.settings-cloud-pairing__device { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 10px; border-radius: 6px; background: var(--el-bg-color); }
.settings-cloud-pairing__device-text { display: grid; gap: 4px; min-width: 0; }
.settings-cloud-pairing__device-text strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.settings-cloud-pairing__device-text small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.settings-cloud-pairing__tags { display: flex; flex-wrap: wrap; gap: 4px; }
</style>
