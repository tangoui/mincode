export type LicensePlan =
  | "free"
  | "personal_pro"
  | "annual_3_devices"
  | "lifetime_3_devices"
  | "lifetime_unlimited_devices";
export type LicenseStatus = "free" | "active" | "expired";

export interface LicenseEntitlement {
  plan?: LicensePlan;
  licenseType?: "annual" | "lifetime" | string;
  lifetime?: boolean;
  maxDevices?: number;
  unlimitedDevices?: boolean;
  activeDevices?: number;
  features?: string[];
}

export interface LicenseRecord extends LicenseEntitlement {
  plan: LicensePlan;
  status: LicenseStatus;
  licenseKey?: string;
  email?: string;
  activatedAt?: number;
  expiresAt?: number;
  lastVerifiedAt?: number;
  updatedAt: number;
}

export interface LicensePlanPricing {
  id: Exclude<LicensePlan, "free" | "personal_pro">;
  billing: "annual" | "lifetime";
  lifetime: boolean;
  salePrice: number;
  originalPrice: number;
  currency: string;
  maxDevices: number;
  unlimitedDevices: boolean;
  features: string[];
}

export interface LicensePricing {
  version?: string;
  updatedAt?: number;
  currency?: string;
  plans: LicensePlanPricing[];
}

export const DEFAULT_LICENSE_RECORD: LicenseRecord = {
  plan: "free",
  status: "free",
  updatedAt: 0,
};

export const DEFAULT_LICENSE_PRICING: LicensePricing = { plans: [] };

function optionalString(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function optionalTimestamp(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : undefined;
}

function optionalNonNegativeInteger(value: unknown) {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

function optionalFeatures(value: unknown) {
  if (!Array.isArray(value)) return undefined;
  return [...new Set(value.filter((feature): feature is string => (
    typeof feature === "string" && feature.trim().length > 0
  )).map((feature) => feature.trim()))];
}

function isLicensePlan(value: unknown): value is LicensePlan {
  return value === "free"
    || value === "personal_pro"
    || value === "annual_3_devices"
    || value === "lifetime_3_devices"
    || value === "lifetime_unlimited_devices";
}

export function createFreeLicenseRecord(now = Date.now()): LicenseRecord {
  return { ...DEFAULT_LICENSE_RECORD, updatedAt: now };
}

export function normalizeLicenseRecord(raw: unknown, now = Date.now()): LicenseRecord {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return createFreeLicenseRecord(now);

  const value = raw as Record<string, unknown>;
  const licenseKey = optionalString(value.licenseKey);
  const expiresAt = optionalTimestamp(value.expiresAt);
  const lifetime = value.lifetime === true;
  const requestedStatus = value.status === "active" || value.status === "expired"
    ? value.status
    : "free";
  const status = requestedStatus === "active" && (
    !licenseKey || (!lifetime && (!expiresAt || expiresAt <= now))
  )
    ? "expired"
    : requestedStatus;
  const requestedPlan = isLicensePlan(value.plan) ? value.plan : "free";
  const plan = status === "active" && requestedPlan === "free" ? "personal_pro" : requestedPlan;
  const maxDevices = optionalNonNegativeInteger(value.maxDevices);
  const unlimitedDevices = value.unlimitedDevices === true || maxDevices === 0;

  return {
    plan,
    status,
    licenseKey,
    email: optionalString(value.email),
    activatedAt: optionalTimestamp(value.activatedAt),
    expiresAt,
    lastVerifiedAt: optionalTimestamp(value.lastVerifiedAt),
    updatedAt: optionalTimestamp(value.updatedAt) ?? now,
    licenseType: optionalString(value.licenseType),
    lifetime,
    maxDevices,
    unlimitedDevices,
    activeDevices: optionalNonNegativeInteger(value.activeDevices),
    features: optionalFeatures(value.features),
  };
}

export function createActiveLicenseRecord(
  licenseKey: string,
  email: string,
  entitlement: LicenseEntitlement & { expiresAt?: number | null },
  now = Date.now(),
): LicenseRecord {
  return normalizeLicenseRecord({
    plan: entitlement.plan ?? "personal_pro",
    status: "active",
    licenseKey,
    email,
    activatedAt: now,
    expiresAt: entitlement.expiresAt ?? undefined,
    lastVerifiedAt: now,
    updatedAt: now,
    licenseType: entitlement.licenseType,
    lifetime: entitlement.lifetime === true,
    maxDevices: entitlement.maxDevices,
    unlimitedDevices: entitlement.unlimitedDevices,
    activeDevices: entitlement.activeDevices,
    features: entitlement.features,
  }, now);
}

export function isLicenseAuthorized(record: LicenseRecord, now = Date.now()) {
  return record.status === "active"
    && Boolean(record.licenseKey)
    && (record.lifetime === true || (typeof record.expiresAt === "number" && record.expiresAt > now));
}

export function applyLicenseVerification(
  record: LicenseRecord,
  valid: boolean,
  entitlement: LicenseEntitlement & { expiresAt?: number | null },
  now = Date.now(),
): LicenseRecord {
  if (!valid) {
    return normalizeLicenseRecord({ ...record, status: "expired", updatedAt: now }, now);
  }

  return normalizeLicenseRecord({
    ...record,
    status: "active",
    plan: entitlement.plan ?? record.plan,
    expiresAt: entitlement.expiresAt ?? record.expiresAt,
    lastVerifiedAt: now,
    updatedAt: now,
    licenseType: entitlement.licenseType ?? record.licenseType,
    lifetime: entitlement.lifetime === true,
    maxDevices: entitlement.maxDevices ?? record.maxDevices,
    unlimitedDevices: entitlement.unlimitedDevices ?? record.unlimitedDevices,
    activeDevices: entitlement.activeDevices ?? record.activeDevices,
    features: entitlement.features ?? record.features,
  }, now);
}

export function normalizeLicensePricing(raw: unknown): LicensePricing {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return { ...DEFAULT_LICENSE_PRICING };
  const value = raw as Record<string, unknown>;
  const defaultCurrency = optionalString(value.currency);
  const plans = Array.isArray(value.plans) ? value.plans.flatMap((rawPlan) => {
    if (!rawPlan || typeof rawPlan !== "object" || Array.isArray(rawPlan)) return [];
    const plan = rawPlan as Record<string, unknown>;
    const id = plan.id;
    const billing = plan.billing;
    const salePrice = optionalNonNegativeInteger(plan.sale_price);
    const originalPrice = optionalNonNegativeInteger(plan.original_price);
    const maxDevices = optionalNonNegativeInteger(plan.max_devices);
    if ((id !== "annual_3_devices" && id !== "lifetime_3_devices" && id !== "lifetime_unlimited_devices")
      || (billing !== "annual" && billing !== "lifetime")
      || salePrice === undefined
      || originalPrice === undefined
      || maxDevices === undefined) {
      return [];
    }
    const lifetime = plan.lifetime === true || billing === "lifetime";
    return [{
      id,
      billing,
      lifetime,
      salePrice,
      originalPrice,
      currency: optionalString(plan.currency) ?? defaultCurrency ?? "CNY",
      maxDevices,
      unlimitedDevices: plan.unlimited_devices === true || maxDevices === 0,
      features: optionalFeatures(plan.features) ?? [],
    } satisfies LicensePlanPricing];
  }) : [];

  return {
    version: optionalString(value.version),
    updatedAt: optionalTimestamp(value.updated_at),
    currency: defaultCurrency,
    plans,
  };
}
