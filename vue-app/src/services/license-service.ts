import {
  createActiveLicenseRecord,
  createFreeLicenseRecord,
  normalizeLicensePricing,
  normalizeLicenseRecord,
  type LicenseEntitlement,
  type LicensePricing,
  type LicenseRecord,
} from "@/domain/license/license";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

const LICENSE_STORAGE_KEY = "app_license";

interface LicenseServerEntitlement {
  plan?: LicenseEntitlement["plan"];
  license_type?: LicenseEntitlement["licenseType"];
  lifetime?: boolean;
  max_devices?: number;
  unlimited_devices?: boolean;
  active_devices?: number;
  features?: string[];
}

export interface LicenseActivationResult extends LicenseServerEntitlement {
  success: boolean;
  expires_at: number | null;
  email: string | null;
  message: string;
}

export interface LicenseVerificationResult extends LicenseServerEntitlement {
  valid: boolean;
  expires_at: number | null;
  message: string | null;
}

interface LicensePricingResult {
  version?: string;
  updated_at?: number;
  currency?: string;
  plans?: unknown[];
  message?: string;
}

function entitlementFromResult(result: LicenseServerEntitlement & { expires_at: number | null }) {
  return {
    plan: result.plan,
    licenseType: result.license_type,
    lifetime: result.lifetime,
    maxDevices: result.max_devices,
    unlimitedDevices: result.unlimited_devices,
    activeDevices: result.active_devices,
    features: result.features,
    expiresAt: result.expires_at,
  };
}

async function readStoredValue(key: string) {
  return invokeBackend<unknown>("store_get", { key });
}

async function decodeStoredLicense(raw: unknown) {
  if (typeof raw === "string" && raw.startsWith("enc:v2:")) {
    const json = await invokeBackend<string>("unseal_object", { cipher: raw });
    return JSON.parse(json) as unknown;
  }
  return raw;
}

export async function loadLicenseRecord(): Promise<LicenseRecord> {
  if (!isDesktopRuntime()) return createFreeLicenseRecord();

  try {
    const raw = await readStoredValue(LICENSE_STORAGE_KEY);
    if (raw === null || raw === undefined) return createFreeLicenseRecord();

    const decoded = await decodeStoredLicense(raw);
    const record = normalizeLicenseRecord(decoded);
    if (typeof raw !== "string") {
      await saveLicenseRecord(record);
    }
    return record;
  } catch {
    return createFreeLicenseRecord();
  }
}

export async function saveLicenseRecord(record: LicenseRecord) {
  if (!isDesktopRuntime()) throw new Error("Desktop runtime is required");
  const normalized = normalizeLicenseRecord(record);
  const cipher = await invokeBackend<string>("seal_object", { json: JSON.stringify(normalized) });
  await invokeBackend<void>("store_set", { key: LICENSE_STORAGE_KEY, value: cipher });
}

export async function loadLicensePricing(): Promise<LicensePricing> {
  if (!isDesktopRuntime()) return normalizeLicensePricing(undefined);
  const result = await invokeBackend<LicensePricingResult>("license_pricing", {});
  return normalizeLicensePricing(result);
}

export async function activateLicense(email: string, licenseKey: string) {
  if (!isDesktopRuntime()) throw new Error("Desktop runtime is required");
  const normalizedKey = licenseKey.trim();
  const normalizedEmail = email.trim();
  const result = await invokeBackend<LicenseActivationResult>("license_activate", {
    email: normalizedEmail,
    licenseKey: normalizedKey,
  });
  const entitlement = entitlementFromResult(result);
  const currentlyValid = result.lifetime === true || (
    typeof result.expires_at === "number" && result.expires_at > Date.now()
  );
  if (!result.success || !currentlyValid) {
    throw new Error(result.message || "License activation failed");
  }
  return createActiveLicenseRecord(
    normalizedKey,
    result.email?.trim() || normalizedEmail,
    entitlement,
  );
}

export function verifyLicense(licenseKey: string) {
  return invokeBackend<LicenseVerificationResult>("license_verify", {
    licenseKey: licenseKey.trim(),
  });
}

export function licenseEntitlementFromVerification(result: LicenseVerificationResult) {
  return entitlementFromResult(result);
}

export async function removeLicenseRecord() {
  if (!isDesktopRuntime()) throw new Error("Desktop runtime is required");
  await invokeBackend<void>("store_del", { key: LICENSE_STORAGE_KEY });
}
