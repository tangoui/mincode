import { computed, onScopeDispose, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  createFreeLicenseRecord,
  type LicensePricing,
  type LicenseRecord,
} from "@/domain/license/license";
import { removeLicenseRecord } from "@/services/license-service";
import type { EffectiveQuota } from "@/services/cloud-companion-service";
import { useQuotaStore } from "@/stores/quota";

const MAX_EXPIRATION_TIMEOUT_MS = 2_147_000_000;

/** Thrown by the retired standalone activation flow; Pro now comes from the signed-in cloud account. */
export class LegacyLicenseRetiredError extends Error {
  constructor() {
    super("Standalone license keys are retired. Sign in to your cloud account in Settings; Pro follows the account entitlement.");
    this.name = "LegacyLicenseRetiredError";
  }
}

/**
 * Maps the member entitlement resolved by the Go backend (signed snapshot, or its bounded offline copy)
 * onto the legacy record shape. Only a verified/offline annual or perpetual plan that has not expired
 * counts as Pro; everything else — including any record stored by the retired key flow — is free.
 */
export function licenseRecordFromEntitlement(effective: EffectiveQuota | null | undefined, now = Date.now()): LicenseRecord {
  if (!effective || (effective.source !== "verified" && effective.source !== "offline")) return createFreeLicenseRecord(now);
  const plan = effective.plan;
  if (plan !== "annual" && plan !== "perpetual") return createFreeLicenseRecord(now);
  const lifetime = plan === "perpetual";
  const expiresAt = typeof effective.expiresAt === "number" && Number.isFinite(effective.expiresAt) && effective.expiresAt > 0
    ? effective.expiresAt * 1000
    : undefined;
  if (!lifetime && expiresAt === undefined) return createFreeLicenseRecord(now);
  const maxDevices = effective.limits?.max_devices;
  return {
    plan: "personal_pro",
    status: lifetime || (expiresAt ?? 0) > now ? "active" : "expired",
    expiresAt,
    lastVerifiedAt: typeof effective.checkedAt === "number" ? effective.checkedAt * 1000 : undefined,
    updatedAt: now,
    licenseType: lifetime ? "lifetime" : "annual",
    lifetime,
    maxDevices,
    unlimitedDevices: maxDevices === 0,
  };
}

export const useLicenseStore = defineStore("license", () => {
  const quota = useQuotaStore();
  const hydrated = ref(false);
  const busy = ref(false);
  // The standalone pricing catalog is retired; plans are listed from the cloud framework plans instead.
  const pricing = ref<LicensePricing>({ plans: [] });
  const pricingLoading = ref(false);
  const authorizationNow = ref(Date.now());
  let expirationTimer: ReturnType<typeof setTimeout> | undefined;

  const record = computed(() => licenseRecordFromEntitlement(quota.effective, authorizationNow.value));
  const isAuthorized = computed(() => hydrated.value && record.value.status === "active");

  function clearExpirationTimer() {
    if (expirationTimer === undefined) return;
    clearTimeout(expirationTimer);
    expirationTimer = undefined;
  }

  // Re-evaluate exactly when an annual grant lapses, so Pro turns off without waiting for a refresh.
  function scheduleExpiration() {
    clearExpirationTimer();
    authorizationNow.value = Date.now();
    const current = record.value;
    if (current.status !== "active" || current.lifetime || !current.expiresAt) return;
    const remaining = current.expiresAt - authorizationNow.value;
    expirationTimer = setTimeout(scheduleExpiration, Math.min(Math.max(remaining + 25, 1_000), MAX_EXPIRATION_TIMEOUT_MS));
  }

  // Pro features are covered by the member plan as a whole; the cloud entitlement has no feature list.
  function hasFeature(_feature: string) {
    return isAuthorized.value;
  }

  async function refreshVerification(_force = true) {
    await quota.refresh();
    scheduleExpiration();
    return isAuthorized.value;
  }

  async function hydrate() {
    if (hydrated.value) return;
    // The quota store schedules its own (idle, periodic) entitlement lookup; do not add a startup request.
    hydrated.value = true;
    scheduleExpiration();
  }

  async function refreshPricing() {
    pricing.value = { plans: [] };
  }

  async function activate(_email: string, _licenseKey: string): Promise<LicenseRecord> {
    throw new LegacyLicenseRetiredError();
  }

  /** Deletes a record left by the retired key flow. It never affects the cloud entitlement. */
  async function remove() {
    busy.value = true;
    try {
      await removeLicenseRecord();
    } finally {
      busy.value = false;
    }
  }

  const stopWatching = quota.$subscribe(() => {
    if (hydrated.value) scheduleExpiration();
  });

  onScopeDispose(() => {
    clearExpirationTimer();
    stopWatching();
  });

  return {
    record,
    hydrated,
    busy,
    pricing,
    pricingLoading,
    isAuthorized,
    hasFeature,
    hydrate,
    activate,
    remove,
    refreshPricing,
    refreshVerification,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useLicenseStore, import.meta.hot));
}
