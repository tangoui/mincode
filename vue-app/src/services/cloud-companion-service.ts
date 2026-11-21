import { invokeBackend, listenAppEvent } from "@/services/desktop-bridge";

export interface CloudDeviceIdentity {
  deviceId: string;
  signingPublicKey: string;
  encryptionPublicKey: string;
}

export interface CloudStatus {
  configured: boolean;
  signedIn: boolean;
  apiBaseUrl?: string;
  accountId?: number;
  email?: string;
  expiresAt?: string;
  deviceAvailable: boolean;
  device?: CloudDeviceIdentity;
  /** Registration finished but a captcha login is still required before a session exists. */
  registeredNeedsLogin?: boolean;
  /** Background POST /devices/register state for this account and device; empty before any attempt. */
  deviceRegistration?: CloudDeviceRegistrationState;
  /** Last failed registration (for example the plan's device limit); other features keep working. */
  deviceRegistrationError?: string;
}

export type CloudDeviceRegistrationState = "pending" | "failed" | "registered" | "";

export interface CloudRealtimeState {
  connected: boolean;
  connecting: boolean;
  lastError?: string;
  connectedAt?: number;
}

export interface CloudDevice {
  device_id: string;
  device_type: "desktop" | "mobile";
  name: string;
  platform: string;
  os_version: string;
  device_model: string;
  architecture: string;
  signing_public_key: string;
  encryption_public_key: string;
  app_version: string;
  first_login_at: number;
  last_login_at: number;
  last_seen_at: number;
  last_ip: string;
  login_count: number;
}

export interface CloudDeviceStats {
  total: number;
  desktop: number;
  mobile: number;
}

export interface CloudDeviceList {
  items: CloudDevice[];
  stats: CloudDeviceStats;
}

/** Pairing capability vocabulary; the cloud always adds AI_CHAT to every grant. */
export const CLOUD_PAIRING_CAPABILITIES = ["AI_CHAT", "TERMINAL", "SSH_CONNECT", "DATABASE_READ"] as const;
export type CloudPairingCapability = (typeof CLOUD_PAIRING_CAPABILITIES)[number];
export const CLOUD_ALWAYS_GRANTED_CAPABILITY: CloudPairingCapability = "AI_CHAT";

export function isCloudPairingCapability(value: string): value is CloudPairingCapability {
  return (CLOUD_PAIRING_CAPABILITIES as readonly string[]).includes(value);
}

/** One-time pairing challenge from POST /pairings; `expires_at` is unix seconds (5 minutes after creation). */
export interface CloudPairingChallenge {
  challenge_id: string;
  pairing_secret: string;
  expires_at: number;
  capabilities: string[];
  desktop: CloudDevice;
}

/** The code the phone enters or scans: `challenge_id.pairing_secret` (challenge_id never contains a dot). */
export function cloudPairingCode(challenge: Pick<CloudPairingChallenge, "challenge_id" | "pairing_secret">) {
  return `${challenge.challenge_id}.${challenge.pairing_secret}`;
}

/** 配对二维码内容；与手机端扫码解析（mobile/lib/core/cloud/pairing_payload.dart）一致。 */
export function cloudPairingQrContent(code: string) {
  return `mincode://pair?code=${code}`;
}

/** A peer device (normally a phone) holding an active grant to this desktop. */
export interface CloudPairedDevice extends CloudDevice {
  capabilities: string[];
  grant_id: string;
}

export interface CloudLoginCaptcha {
  required: boolean;
  id?: string;
  text?: string[];
  base64?: string;
  width?: number;
  height?: number;
}

export function cloudLoginCaptcha(purpose: "login" | "register" = "login") {
  return invokeBackend<CloudLoginCaptcha>("cloud_login_captcha", { purpose });
}

export function cloudCheckLoginCaptcha(id: string, info: string, purpose: "login" | "register" = "login") {
  return invokeBackend<boolean>("cloud_login_captcha_check", { id, info, purpose });
}

export function cloudLogin(input: { email: string; password: string; captchaId?: string; captchaInfo?: string }) {
  return invokeBackend<CloudStatus>("cloud_login", input);
}

export function cloudRegisterCode(input: { email: string; captchaId: string; captchaInfo: string }) {
  return invokeBackend<boolean>("cloud_register_code", input);
}

export function cloudRegister(input: {
  email: string;
  username: string;
  password: string;
  passwordConfirmation: string;
  code: string;
}) {
  return invokeBackend<CloudStatus>("cloud_register", input);
}

export function cloudStatus() {
  return invokeBackend<CloudStatus>("cloud_status", {});
}

/** Display-only member profile; the mobile number arrives already masked. */
export interface CloudAccountProfile {
  accountId: number;
  username: string;
  nickname: string;
  email: string;
  mobile: string;
  motto: string;
  money: string;
  score: number;
  joinTime: number;
  lastLoginTime: number;
  lastLoginIp: string;
}

export function cloudAccountProfile() {
  return invokeBackend<CloudAccountProfile>("cloud_account_profile", {});
}

export function cloudLogout() {
  return invokeBackend<CloudStatus>("cloud_logout", {});
}

export interface FrameworkPlan {
  plan: "free" | "annual" | "perpetual";
  display_name: string;
  duration_days: number | null;
  price_minor: number;
  /** Null when there is no discount; otherwise a list price above price_minor. */
  original_price_minor: number | null;
  currency: string;
  version_range: string;
  policy_revision: number;
  effective_at: number;
  limits: {
    max_devices: number;
    max_active_workspaces: number;
    max_windows_per_workspace: number;
    max_enabled_plugins?: number;
    plugins_disabled?: boolean;
  };
}

export function fetchFrameworkPlans() {
  return invokeBackend<FrameworkPlan[]>("cloud_framework_plans", {});
}

export interface VerifiedMemberQuota {
  accountId: number;
  plan: "free" | "annual" | "perpetual";
  expiresAt: number | null;
  snapshotExpiresAt: number;
  limits: {
    max_devices: number;
    max_active_workspaces: number;
    max_windows_per_workspace: number;
    max_enabled_plugins?: number;
    plugins_disabled?: boolean;
  };
}

export interface MemberQuotaResult {
  status: "signed_out" | "unconfigured" | "failed" | "verified" | "expired";
  message?: string;
  snapshot?: VerifiedMemberQuota;
}

export function fetchMemberQuota(expectedAccountId: number) {
  return invokeBackend<MemberQuotaResult>("cloud_entitlement_snapshot", { expectedAccountId });
}

/** Limits applied to new workspaces and tabs; `fallback` marks the built-in free limits used when no cloud policy was ever obtained. */
export interface EffectiveQuota {
  source: "verified" | "offline" | "free" | "none";
  plan?: "free" | "annual" | "perpetual";
  accountId?: number;
  expiresAt?: number | null;
  checkedAt?: number;
  offlineUntil?: number;
  limits?: VerifiedMemberQuota["limits"];
  reason?: "signed_out" | "unconfigured" | "fetch_failed" | "expired" | "offline_expired" | "clock_rollback" | "device_limit";
  deviceLimit?: { current: number; limit: number };
  fallback?: boolean;
}

/** A device registered to the signed-in member; `current` marks this machine. Only desktops count toward the device limit. */
export interface MemberDevice {
  deviceId: string;
  deviceType: "desktop" | "mobile";
  name: string;
  platform: string;
  appVersion: string;
  firstSeenAt: number;
  lastSeenAt: number;
  current: boolean;
}

/** Devices that use a slot of the plan's device limit: desktops and phones share one limit. */
export function countLimitedMemberDevices(devices: readonly Pick<MemberDevice, "deviceType">[]): number {
  return devices.length;
}

export function listMemberDevices() {
  return invokeBackend<MemberDevice[]>("cloud_member_devices", {});
}

export function removeMemberDevice(deviceId: string) {
  return invokeBackend<boolean>("cloud_member_device_remove", { deviceId });
}

export function fetchEffectiveQuota() {
  return invokeBackend<EffectiveQuota>("cloud_quota_effective", {});
}

// A grant receipt is not a verified entitlement or a license activation.
export function redeemMemberCode(code: string, expectedAccountId: number) {
  return invokeBackend<{ grant_id: string }>("cloud_entitlement_redeem", {
    code,
    expectedAccountId,
  });
}

/**
 * Mobile pairing and the realtime relay (/api/mincode/v1/*, /ws/mincode/v1) use the member access token,
 * so any signed-in member can pair. Registration of this desktop runs in the background; while it is
 * pending or failed the UI shows a hint (see CloudStatus.deviceRegistration) instead of hiding pairing.
 */
export function isCloudDevicePairingAvailable(status: Pick<CloudStatus, "signedIn"> | null | undefined) {
  return Boolean(status?.signedIn);
}

export function cloudRealtimeStatus() {
  return invokeBackend<CloudRealtimeState>("cloud_realtime_status", {});
}

export function cloudRealtimeConnect() {
  return invokeBackend<CloudRealtimeState>("cloud_realtime_connect", {});
}

export function cloudRealtimeDisconnect() {
  return invokeBackend<CloudRealtimeState>("cloud_realtime_disconnect", {});
}

export function cloudCreatePairing(capabilities: string[]) {
  return invokeBackend<CloudPairingChallenge>("cloud_pairing_create", { capabilities });
}

export async function cloudPairedDevices(): Promise<CloudPairedDevice[]> {
  const result = await invokeBackend<CloudPairedDevice[] | null>("cloud_paired_devices", {});
  if (!Array.isArray(result)) return [];
  return result
    .filter((device) => typeof device?.device_id === "string" && device.device_id !== "")
    .map((device) => ({
      ...device,
      capabilities: Array.isArray(device.capabilities) ? device.capabilities.filter((item) => typeof item === "string") : [],
    }));
}

export function cloudListDevices() {
  return invokeBackend<CloudDeviceList>("cloud_devices_list", {});
}

export function cloudRevokeDevice(deviceId: string) {
  return invokeBackend<{ revoked: boolean }>("cloud_device_revoke", { deviceId });
}

/** `cloud:realtime` error frame; `message` is the native layer's readable text for `code` when it knows it. */
export interface CloudRealtimeError {
  code: string;
  message: string;
  requestId?: string;
}

/** Normalizes a `cloud:realtime` payload into an error when it is an error frame, otherwise null. */
export function parseCloudRealtimeError(payload: unknown): CloudRealtimeError | null {
  if (!payload || typeof payload !== "object") return null;
  const event = payload as Record<string, unknown>;
  if (event.type !== "error") return null;
  const code = typeof event.code === "string" ? event.code.trim() : "";
  const message = typeof event.message === "string" ? event.message.trim() : "";
  if (!code && !message) return null;
  const requestId = typeof event.requestId === "string" && event.requestId ? event.requestId : undefined;
  return { code, message, requestId };
}

/** Subscribes to relay error frames; returns the unsubscribe function. */
export function onCloudRealtimeError(handler: (error: CloudRealtimeError) => void) {
  return listenAppEvent<unknown>("cloud:realtime", (payload) => {
    const error = parseCloudRealtimeError(payload);
    if (error) handler(error);
  });
}
