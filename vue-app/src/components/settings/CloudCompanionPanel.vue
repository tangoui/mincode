<script lang="ts">
import type { CloudAccountProfile, CloudStatus, MemberQuotaResult } from "@/services/cloud-companion-service";

// The settings view unmounts this panel on every tab switch; keeping the last known
// account state lets a remount render it immediately instead of the sign-in form.
const accountCache: { status?: CloudStatus; profile?: CloudAccountProfile | null; quota?: MemberQuotaResult | null } = {};
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  cloudAccountProfile,
  cloudLogin,
  cloudLoginCaptcha,
  cloudCheckLoginCaptcha,
  cloudRegister,
  cloudRegisterCode,
  cloudLogout,
  cloudStatus,
  countLimitedMemberDevices,
  fetchMemberQuota,
  isCloudDevicePairingAvailable,
  listMemberDevices,
  removeMemberDevice,
  type CloudLoginCaptcha,
  type MemberDevice,
} from "@/services/cloud-companion-service";
import { isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";
import { useVueI18n } from "@/i18n/messages";
import { useQuotaStore } from "@/stores/quota";
import CloudPairingSection from "@/components/settings/CloudPairingSection.vue";

const { t, intlLocale } = useVueI18n();
const quotaStore = useQuotaStore();
const status = ref<CloudStatus>(accountCache.status ?? { configured: false, signedIn: false, deviceAvailable: false });
const statusReady = ref(Boolean(accountCache.status) || !isDesktopRuntime());
const profile = ref<CloudAccountProfile | null>(
  accountCache.profile && accountCache.profile.accountId === status.value.accountId ? accountCache.profile : null,
);
const profileLoading = ref(false);
const profileError = ref("");
let profileGeneration = 0;
const quota = ref<MemberQuotaResult | null>(accountCache.quota ?? null);
const quotaLoading = ref(false);
let quotaGeneration = 0;
const verifiedQuota = computed(() =>
  quota.value?.status === "verified" && quota.value.snapshot?.accountId === status.value.accountId ? quota.value.snapshot : null,
);
const quotaLimits = computed((): { key: string; label: string; value: number; forbidden?: boolean }[] => {
  const limits = verifiedQuota.value?.limits;
  if (!limits) return [];
  return [
    { key: "devices", label: t("settings.cloud.limitDevices"), value: limits.max_devices },
    { key: "workspaces", label: t("settings.cloud.limitWorkspaces"), value: limits.max_active_workspaces },
    { key: "windows", label: t("settings.cloud.limitWindows"), value: limits.max_windows_per_workspace },
    { key: "plugins", label: t("settings.cloud.limitPlugins"), value: limits.max_enabled_plugins ?? 0, forbidden: limits.plugins_disabled === true },
  ];
});
const displayName = computed(() => profile.value?.nickname || profile.value?.username || status.value.email || "");
const devices = ref<MemberDevice[]>([]);
const devicesLoading = ref(false);
const removingDevice = ref("");
let devicesGeneration = 0;
const deviceRefused = computed(() => (quotaStore.effective?.reason === "device_limit" ? quotaStore.effective.deviceLimit : undefined));
const limitedDeviceCount = computed(() => countLimitedMemberDevices(devices.value));
const deviceLimit = computed(() => verifiedQuota.value?.limits.max_devices ?? deviceRefused.value?.limit ?? 0);
const avatarText = computed(() => [...displayName.value.trim()][0]?.toUpperCase() ?? "?");
const loading = ref(false);
const credentials = reactive({ email: "", username: "", password: "", passwordConfirmation: "", code: "" });
const authMode = ref<"login" | "register">("login");
const codeSending = ref(false);
const captcha = ref<CloudLoginCaptcha | null>(null);
const captchaOpen = ref(false);
const captchaBusy = ref(false);
const points = ref<{ x: number; y: number }[]>([]);

async function refresh() {
  if (!isDesktopRuntime()) return;
  try {
    status.value = await cloudStatus();
  } finally {
    statusReady.value = true;
  }
  await Promise.all([loadProfile(), loadQuota(), loadDevices()]);
}

watch([status, profile, quota], ([nextStatus, nextProfile, nextQuota]) => {
  Object.assign(accountCache, { status: nextStatus, profile: nextProfile, quota: nextQuota });
});

async function loadQuota() {
  const generation = ++quotaGeneration;
  const accountId = status.value.signedIn ? status.value.accountId : undefined;
  if (!accountId) {
    quota.value = null;
    quotaLoading.value = false;
    return;
  }
  quotaLoading.value = true;
  try {
    const result = await fetchMemberQuota(accountId);
    if (generation === quotaGeneration) quota.value = result;
  } catch (error) {
    if (generation === quotaGeneration) quota.value = { status: "failed", message: nativeErrorMessage(error) };
  } finally {
    if (generation === quotaGeneration) quotaLoading.value = false;
  }
}

async function refreshStatus() {
  if (!isDesktopRuntime()) return;
  try {
    status.value = await cloudStatus();
  } catch {
    // Keep the last known status; the account sections surface their own errors.
  }
}

// Device registration runs in the background after sign-in or startup; re-read the status until it settles
// so the pairing section's "registering" hint clears on its own.
const REGISTRATION_POLL_MS = 3000;
const registrationPending = computed(() => status.value.signedIn && status.value.deviceRegistration === "pending");
let registrationPoll: ReturnType<typeof setTimeout> | undefined;
let unmounted = false;

function scheduleRegistrationPoll() {
  if (registrationPoll !== undefined) clearTimeout(registrationPoll);
  registrationPoll = undefined;
  if (unmounted || !registrationPending.value) return;
  registrationPoll = setTimeout(() => {
    registrationPoll = undefined;
    void refreshStatus().then(scheduleRegistrationPoll);
  }, REGISTRATION_POLL_MS);
}

watch(registrationPending, scheduleRegistrationPoll, { immediate: true });

function reloadAccount() {
  // cloud_status also retries a failed device registration, so "Refresh" picks up a freed device slot.
  void refreshStatus();
  void loadProfile();
  void loadQuota();
  void loadDevices();
  void quotaStore.refresh();
}

async function loadDevices() {
  const generation = ++devicesGeneration;
  if (!status.value.signedIn) {
    devices.value = [];
    devicesLoading.value = false;
    return;
  }
  devicesLoading.value = true;
  try {
    const result = await listMemberDevices();
    if (generation === devicesGeneration) devices.value = result;
  } catch (error) {
    if (generation === devicesGeneration) ElMessage.warning(nativeErrorMessage(error));
  } finally {
    if (generation === devicesGeneration) devicesLoading.value = false;
  }
}

async function removeDevice(device: MemberDevice) {
  try {
    await ElMessageBox.confirm(
      t(device.current ? "settings.cloud.removeCurrentDeviceConfirm" : "settings.cloud.removeDeviceConfirm", { name: device.name || t("settings.cloud.unnamedDevice") }),
      t("settings.cloud.removeDevice"),
      { type: "warning", confirmButtonText: t("settings.cloud.removeDevice"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  removingDevice.value = device.deviceId;
  try {
    await removeMemberDevice(device.deviceId);
    ElMessage.success(t("settings.cloud.deviceRemoved"));
    // A freed slot may let this device register again, so re-resolve the entitlement too.
    reloadAccount();
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  } finally {
    removingDevice.value = "";
  }
}

function platformLabel(platform: string) {
  const [os = "", arch = ""] = platform.split("/");
  const name = ({ darwin: "macOS", windows: "Windows", linux: "Linux" } as Record<string, string>)[os] ?? os;
  return arch ? `${name} · ${arch}` : name || "—";
}

async function loadProfile() {
  const generation = ++profileGeneration;
  profileError.value = "";
  if (!status.value.signedIn) {
    profile.value = null;
    profileLoading.value = false;
    return;
  }
  profileLoading.value = true;
  try {
    const result = await cloudAccountProfile();
    if (generation === profileGeneration) profile.value = result;
  } catch (error) {
    if (generation === profileGeneration) profileError.value = nativeErrorMessage(error);
  } finally {
    if (generation === profileGeneration) profileLoading.value = false;
  }
}

function formatTime(seconds?: number) {
  if (!seconds) return "—";
  return new Date(seconds * 1000).toLocaleString(intlLocale.value, { hour12: false });
}

async function submitRegister(captchaId: string, captchaInfo: string) {
  codeSending.value = true;
  try {
    await cloudRegisterCode({ email: credentials.email.trim(), captchaId, captchaInfo });
    captchaOpen.value = false;
    ElMessage.success(t("settings.cloud.codeSent"));
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
    await reloadCaptcha("register");
  } finally {
    codeSending.value = false;
  }
}

async function submitLogin(captchaId?: string, captchaInfo?: string) {
  loading.value = true;
  try {
    status.value = await cloudLogin({ email: credentials.email.trim(), password: credentials.password, captchaId, captchaInfo });
    credentials.password = "";
    captchaOpen.value = false;
    ElMessage.success(t("settings.cloud.loginSuccess"));
    reloadAccount();
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
    if (captchaId) await reloadCaptcha("login");
  } finally {
    loading.value = false;
  }
}

async function reloadCaptcha(purpose: "login" | "register" = authMode.value) {
  captchaBusy.value = true;
  points.value = [];
  try {
    captcha.value = await cloudLoginCaptcha(purpose);
    if (!captcha.value.required) captchaOpen.value = false;
  } catch (error) {
    captcha.value = null;
    ElMessage.error(nativeErrorMessage(error));
  } finally {
    captchaBusy.value = false;
  }
}

async function login() {
  if (authMode.value === "register") {
    if (!credentials.email.trim() || !credentials.username.trim() || !credentials.password || credentials.password !== credentials.passwordConfirmation || !credentials.code) {
      ElMessage.warning(t("settings.cloud.registrationRequired"));
      return;
    }
    loading.value = true;
    try {
      const registered = await cloudRegister({ email: credentials.email.trim(), username: credentials.username.trim(), password: credentials.password, passwordConfirmation: credentials.passwordConfirmation, code: credentials.code });
      status.value = registered;
      credentials.passwordConfirmation = "";
      credentials.code = "";
      authMode.value = "login";
      if (registered.registeredNeedsLogin) {
        // The account exists but the server needs an interactive (captcha) login: keep the password filled in.
        credentials.email = registered.email ?? credentials.email;
        ElMessage.success(t("settings.cloud.registerLoginRequired"));
      } else {
        credentials.password = "";
        ElMessage.success(t("settings.cloud.registerSuccess"));
        reloadAccount();
      }
    } catch (error) { ElMessage.error(nativeErrorMessage(error)); }
    finally { loading.value = false; }
    return;
  }
  if (!credentials.email.trim() || !credentials.password) {
    ElMessage.warning(t("settings.cloud.credentialsRequired"));
    return;
  }
  loading.value = true;
  try {
    const challenge = await cloudLoginCaptcha("login");
    if (!challenge.required) {
      await submitLogin();
      return;
    }
    captcha.value = challenge;
    points.value = [];
    captchaOpen.value = true;
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
  } finally {
    loading.value = false;
  }
}

async function recordCaptchaClick(event: MouseEvent) {
  const challenge = captcha.value;
  if (!challenge?.id || !challenge.text || !challenge.width || !challenge.height || captchaBusy.value || loading.value) return;
  const image = event.currentTarget as HTMLImageElement;
  const rect = image.getBoundingClientRect();
  const x = Math.min(challenge.width - 1, Math.max(0, Math.floor((event.clientX - rect.left) * challenge.width / rect.width)));
  const y = Math.min(challenge.height - 1, Math.max(0, Math.floor((event.clientY - rect.top) * challenge.height / rect.height)));
  points.value.push({ x, y });
  if (points.value.length !== challenge.text.length) return;
  const info = `${points.value.map((point) => `${point.x},${point.y}`).join("-")};${challenge.width};${challenge.height}`;
  captchaBusy.value = true;
  try {
    await cloudCheckLoginCaptcha(challenge.id, info, authMode.value);
    if (authMode.value === "register") await submitRegister(challenge.id, info);
    else await submitLogin(challenge.id, info);
  } catch (error) {
    ElMessage.error(nativeErrorMessage(error));
    await reloadCaptcha(authMode.value);
  } finally {
    captchaBusy.value = false;
  }
}

async function logout() {
  try {
    await ElMessageBox.confirm(t("settings.cloud.logoutConfirm"), t("settings.cloud.logout"), { type: "warning" });
    status.value = await cloudLogout();
    await Promise.all([loadProfile(), loadQuota(), loadDevices(), quotaStore.refresh()]);
  } catch (error) {
    if (error !== "cancel" && error !== "close") {
      ElMessage.warning(nativeErrorMessage(error));
      // Remote revocation may fail after the native layer has removed local credentials.
      await refresh().catch(() => undefined);
    }
  }
}

async function sendRegistrationCode() {
  if (!credentials.email.trim() || !credentials.username.trim() || !credentials.password || credentials.password !== credentials.passwordConfirmation) {
    ElMessage.warning(t("settings.cloud.registrationRequired"));
    return;
  }
  codeSending.value = true;
  try {
    captcha.value = await cloudLoginCaptcha("register");
    points.value = [];
    captchaOpen.value = true;
  } catch (error) { ElMessage.error(nativeErrorMessage(error)); }
  finally { codeSending.value = false; }
}

onMounted(() => void refresh().catch((error) => ElMessage.error(nativeErrorMessage(error))));
onBeforeUnmount(() => {
  unmounted = true;
  if (registrationPoll !== undefined) clearTimeout(registrationPoll);
  registrationPoll = undefined;
});
</script>

<template>
  <section class="settings-flat-page settings-cloud-page">
    <el-alert v-if="!isDesktopRuntime()" type="warning" :closable="false" :title="t('settings.cloud.nativeOnly')" />

    <div v-if="!statusReady" v-loading="true" class="settings-card settings-cloud-pending" />

    <div v-else-if="!status.signedIn" class="settings-card settings-cloud-login">
      <el-segmented
        v-model="authMode"
        class="settings-cloud-auth-mode"
        :options="[
          { label: t('settings.cloud.login'), value: 'login' },
          { label: t('settings.cloud.register'), value: 'register' },
        ]"
      />
      <el-input v-model="credentials.email" :placeholder="t('settings.cloud.email')" autocomplete="email" />
      <el-input v-if="authMode === 'register'" v-model="credentials.username" :placeholder="t('settings.cloud.username')" autocomplete="username" />
      <el-input v-model="credentials.password" type="password" show-password :placeholder="t('settings.cloud.password')" :autocomplete="authMode === 'register' ? 'new-password' : 'current-password'" />
      <el-input v-if="authMode === 'register'" v-model="credentials.passwordConfirmation" type="password" show-password :placeholder="t('settings.cloud.passwordConfirmation')" autocomplete="new-password" />
      <div v-if="authMode === 'register'" class="settings-cloud-code-actions">
        <el-input v-model="credentials.code" :placeholder="t('settings.cloud.emailCode')" autocomplete="one-time-code" />
        <el-button class="settings-cloud-code-button" :loading="codeSending" :disabled="!isDesktopRuntime()" @click="sendRegistrationCode">{{ t('settings.cloud.sendCode') }}</el-button>
      </div>
      <p class="settings-page-note">{{ t(authMode === 'login' ? "settings.cloud.memberHint" : "settings.cloud.registerHint") }}</p>
      <div class="settings-cloud-actions settings-cloud-actions--primary">
        <el-button class="settings-cloud-submit" type="primary" :loading="loading" :disabled="!isDesktopRuntime()" @click="login">{{ t(authMode === 'login' ? "settings.cloud.login" : "settings.cloud.register") }}</el-button>
      </div>
    </div>

    <el-dialog v-model="captchaOpen" :title="t('settings.cloud.captchaTitle')" width="420px" append-to-body destroy-on-close>
      <div v-loading="captchaBusy" class="settings-cloud-captcha">
        <p class="settings-page-note">{{ t('settings.cloud.captchaPrompt') }} {{ captcha?.text?.join(' → ') }}</p>
        <div class="settings-cloud-captcha-image">
          <img v-if="captcha?.base64" :src="captcha.base64" :alt="t('settings.cloud.captchaTitle')" @click="recordCaptchaClick" />
          <span v-for="(point, index) in points" :key="index" class="settings-cloud-captcha-marker" :style="{ left: `${point.x / (captcha?.width || 1) * 100}%`, top: `${point.y / (captcha?.height || 1) * 100}%` }">{{ index + 1 }}</span>
        </div>
        <el-button :disabled="captchaBusy || loading || codeSending" @click="reloadCaptcha()">{{ t('settings.cloud.captchaRefresh') }}</el-button>
      </div>
    </el-dialog>

    <template v-if="status.signedIn">
      <div class="settings-card settings-cloud-profile" v-loading="profileLoading && !profile">
        <div class="settings-cloud-identity">
          <div class="settings-cloud-avatar" aria-hidden="true">{{ avatarText }}</div>
          <div class="settings-cloud-identity-text">
            <strong>{{ displayName }}</strong>
            <small>
              <template v-if="profile?.username">@{{ profile.username }} · </template>
              {{ t("settings.cloud.accountId") }} {{ profile?.accountId ?? status.accountId ?? "—" }}
            </small>
            <small v-if="profile?.motto">{{ profile.motto }}</small>
          </div>
          <div class="settings-cloud-account-actions">
            <el-tag type="success" effect="plain">{{ t("settings.cloud.signedIn") }}</el-tag>
            <el-button :loading="profileLoading || quotaLoading" @click="reloadAccount">{{ t("settings.cloud.refreshProfile") }}</el-button>
            <el-button type="danger" plain @click="logout">{{ t("settings.cloud.logout") }}</el-button>
          </div>
        </div>

        <el-alert v-if="profileError" type="warning" show-icon :closable="false" :title="t('settings.cloud.profileFailed')" :description="profileError" />

        <el-descriptions :column="2" border size="small" class="settings-cloud-details">
          <el-descriptions-item :label="t('settings.cloud.email')">{{ profile?.email || status.email || "—" }}</el-descriptions-item>
          <el-descriptions-item :label="t('settings.cloud.mobile')">{{ profile?.mobile || "—" }}</el-descriptions-item>
          <el-descriptions-item :label="t('settings.cloud.joinTime')">{{ formatTime(profile?.joinTime) }}</el-descriptions-item>
          <el-descriptions-item :label="t('settings.cloud.lastLoginTime')">{{ formatTime(profile?.lastLoginTime) }}</el-descriptions-item>
          <el-descriptions-item :label="t('settings.cloud.lastLoginIp')">{{ profile?.lastLoginIp || "—" }}</el-descriptions-item>
          <el-descriptions-item :label="t('settings.cloud.score')">{{ profile?.score ?? "—" }}</el-descriptions-item>
          <el-descriptions-item :label="t('settings.cloud.balance')">{{ profile?.money ? `¥${profile.money}` : "—" }}</el-descriptions-item>
        </el-descriptions>

        <div v-loading="quotaLoading && !quota" class="settings-cloud-entitlement">
          <div class="settings-cloud-entitlement-head">
            <strong>{{ t("settings.cloud.entitlement") }}</strong>
            <template v-if="verifiedQuota">
              <el-tag :type="verifiedQuota.plan === 'free' ? 'info' : 'success'" effect="light">{{ t(`settings.pro.cloudPlan.${verifiedQuota.plan}`) }}</el-tag>
              <small>{{ verifiedQuota.expiresAt ? t("settings.cloud.entitlementExpires", { date: formatTime(verifiedQuota.expiresAt) }) : t("settings.pro.noExpiry") }}</small>
            </template>
          </div>
          <div v-if="verifiedQuota" class="settings-cloud-limits">
            <div v-for="limit in quotaLimits" :key="limit.key" class="settings-cloud-limit">
              <span>{{ limit.label }}</span>
              <strong>{{
                limit.forbidden ? t("settings.cloud.pluginsNotAllowed") : limit.value === 0 ? t("settings.pro.noLimit") : limit.value
              }}</strong>
            </div>
          </div>
          <p v-else-if="quota?.status === 'unconfigured'" class="settings-page-note">{{ t("settings.pro.entitlementPending") }}</p>
          <p v-else-if="quota?.status === 'expired'" class="settings-page-note">{{ t("settings.pro.quotaExpired") }}</p>
          <p v-else-if="quota && !quotaLoading" class="settings-page-note">{{ t("settings.cloud.entitlementFailed", { message: quota.message || "—" }) }}</p>
        </div>

        <div v-loading="devicesLoading && !devices.length" class="settings-cloud-entitlement">
          <div class="settings-cloud-entitlement-head">
            <strong>{{ t("settings.cloud.memberDevices") }}</strong>
            <small v-if="deviceLimit > 0">{{ t("settings.cloud.deviceUsage", { current: limitedDeviceCount, limit: deviceLimit }) }}</small>
            <small v-else-if="verifiedQuota">{{ t("settings.cloud.deviceUsageUnlimited", { current: limitedDeviceCount }) }}</small>
          </div>
          <el-alert
            v-if="deviceRefused"
            type="warning"
            show-icon
            :closable="false"
            :title="t('settings.cloud.deviceRefused', { current: deviceRefused.current, limit: deviceRefused.limit })"
          />
          <p v-if="!devices.length && !devicesLoading" class="settings-page-note">{{ t("settings.cloud.noDevices") }}</p>
          <div v-for="device in devices" :key="device.deviceId" class="settings-cloud-device">
            <div class="settings-cloud-device-text">
              <span>
                <strong>{{ device.name || t("settings.cloud.unnamedDevice") }}</strong>
                <el-tag v-if="device.current" size="small" type="success" effect="plain">{{ t("settings.cloud.thisDevice") }}</el-tag>
                <el-tag v-if="device.deviceType === 'mobile'" size="small" type="info" effect="plain">{{ t("settings.cloud.mobileDevice") }}</el-tag>
              </span>
              <small>{{ platformLabel(device.platform) }} · v{{ device.appVersion || "—" }} · {{ t("settings.cloud.deviceLastSeen", { time: formatTime(device.lastSeenAt) }) }}</small>
            </div>
            <el-button text type="danger" size="small" :loading="removingDevice === device.deviceId" :disabled="!!removingDevice" @click="removeDevice(device)">
              {{ t("settings.cloud.removeDevice") }}
            </el-button>
          </div>
        </div>

        <CloudPairingSection
          v-if="isCloudDevicePairingAvailable(status)"
          :registration="status.deviceRegistration"
          :registration-error="status.deviceRegistrationError"
        />

      </div>
    </template>
  </section>
</template>

<style scoped>
.settings-cloud-page { display: grid; gap: 16px; }
.settings-cloud-login { display: grid; gap: 12px; }
.settings-cloud-pending { min-height: 180px; }

.settings-card { display: grid; gap: 12px; padding: 16px; border: 1px solid var(--el-border-color-light); border-radius: 10px; }
.settings-card small { display: block; margin-top: 4px; color: var(--el-text-color-secondary); }
.settings-cloud-actions { display: flex; align-items: center; gap: 12px; }
.settings-cloud-actions--primary { margin-top: 2px; }
.settings-cloud-submit {
  width: 100%;
  min-height: 32px;
  font-weight: 600;
}
.settings-cloud-code-actions { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 8px; }
.settings-cloud-code-button { width: 104px; min-width: 104px; }
.settings-cloud-captcha { display: grid; gap: 12px; }
.settings-cloud-captcha-image { position: relative; width: fit-content; max-width: 100%; }
.settings-cloud-captcha-image img { display: block; max-width: 100%; cursor: crosshair; }
.settings-cloud-captcha-marker { position: absolute; transform: translate(-50%, -50%); pointer-events: none; border-radius: 50%; padding: 2px 6px; background: var(--app-accent); color: var(--app-surface); }
.settings-cloud-profile { gap: 16px; }
.settings-cloud-identity { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 12px; }
.settings-cloud-account-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 8px; }
.settings-cloud-account-actions .el-button + .el-button { margin-left: 0; }
.settings-cloud-identity-text { display: grid; gap: 2px; min-width: 0; }
.settings-cloud-identity-text strong { font-size: 16px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.settings-cloud-identity-text small { margin-top: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.settings-cloud-avatar {
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  border-radius: 50%;
  background: var(--app-accent);
  color: var(--app-surface);
  font-size: 18px;
  font-weight: 600;
}
.settings-cloud-entitlement { display: grid; gap: 10px; padding: 12px 14px; border-radius: 8px; background: var(--el-fill-color-light); }
.settings-cloud-entitlement-head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.settings-cloud-entitlement-head small { margin-top: 0; color: var(--el-text-color-secondary); }
.settings-cloud-entitlement .settings-page-note { margin: 0; }
.settings-cloud-limits { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; }
.settings-cloud-device { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 10px; border-radius: 6px; background: var(--el-bg-color); }
.settings-cloud-device-text { display: grid; gap: 2px; min-width: 0; }
.settings-cloud-device-text span { display: flex; align-items: center; gap: 6px; min-width: 0; }
.settings-cloud-device-text strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.settings-cloud-device-text small { margin-top: 0; color: var(--el-text-color-secondary); }
.settings-cloud-limit { display: grid; gap: 2px; padding: 8px 10px; border-radius: 6px; background: var(--el-bg-color); }
.settings-cloud-limit span { color: var(--el-text-color-secondary); font-size: 12px; }
.settings-cloud-limit strong { font-size: 18px; }
.settings-cloud-details :deep(.el-descriptions__label) { width: 96px; white-space: nowrap; }
</style>
