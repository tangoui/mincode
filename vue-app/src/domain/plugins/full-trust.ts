// Pure parsers for the plugin_full_trust_* commands (backend/plugin/
// full_trust_commands.go). Go is the only judge of full trust; these only shape
// its replies. Anything malformed reads as "not trusted", so a bad reply always
// falls back to the normal confirmation. Imported only by lazily loaded
// modules: keep it out of the entry chain (scripts/plugin-full-trust.test.mjs).

export type FullTrustSurface = "viewAction" | "codeLens" | "notification" | "tray" | "testRun";
export type FullTrustRisk = "network" | "write" | "dangerous";
export type FullTrustAuditSurface = FullTrustSurface | "pluginCall" | "serviceCall" | "hostService";

export interface FullTrustSubject {
  pluginId: string;
  name: string;
  version: string;
  capabilities: string[];
  /** Empty unless Go hashed the package (get with `full`, or a stored record). */
  fingerprint: string;
  eligible: boolean;
  reason?: string;
}

export interface FullTrustStatus {
  pluginId: string;
  trusted: boolean;
  grantedAt?: number;
  legacyDangerous: boolean;
  lapsed?: string;
  subject: FullTrustSubject;
}

export interface FullTrustListItem {
  pluginId: string;
  name: string;
  version: string;
  grantedAt: number;
}

export interface FullTrustAuditEntry {
  at: number;
  lastAt: number;
  count: number;
  kind: "skipped" | "granted" | "revoked" | "lapsed";
  surface?: string;
  risk?: string;
  subject?: string;
  counterpart?: string;
  reason?: string;
}

export const FULL_TRUST_AUDIT_MAX = 200;

const AUDIT_KINDS = new Set(["skipped", "granted", "revoked", "lapsed"]);
const AUDIT_TEXT_FIELDS = ["surface", "risk", "subject", "counterpart", "reason"] as const;

/**
 * Plugin-provided text (manifest name, version, IDs, audit subjects) as shown
 * in full trust UI: format characters (\p{Cf}, including bidi overrides and
 * isolates, zero-width characters) are removed, and any run of whitespace or
 * control characters becomes one space. Render the result inside <bdi> as well.
 */
export function fullTrustDisplayText(value: string): string {
  return value.replace(/\p{Cf}+/gu, "").replace(/[\s\p{Cc}]+/gu, " ").trim();
}

/** Only network and write keep their name; read, unknown or missing ask as dangerous (matches Go). */
export function fullTrustRisk(risk: string | undefined): FullTrustRisk {
  return risk === "network" || risk === "write" ? risk : "dangerous";
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function nonEmpty(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "";
}

function timestamp(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function parseSubject(value: unknown): FullTrustSubject | undefined {
  const raw = record(value);
  if (!raw || !nonEmpty(raw.pluginId) || typeof raw.fingerprint !== "string" || typeof raw.eligible !== "boolean") return undefined;
  return {
    pluginId: raw.pluginId,
    name: typeof raw.name === "string" && raw.name.trim() ? raw.name : raw.pluginId,
    version: typeof raw.version === "string" ? raw.version : "",
    capabilities: Array.isArray(raw.capabilities) ? raw.capabilities.filter((item): item is string => typeof item === "string") : [],
    fingerprint: raw.fingerprint,
    eligible: raw.eligible,
    ...(nonEmpty(raw.reason) ? { reason: raw.reason } : {}),
  };
}

export function parseFullTrustStatus(value: unknown): FullTrustStatus | undefined {
  const raw = record(value);
  if (!raw || !nonEmpty(raw.pluginId)) return undefined;
  const subject = parseSubject(raw.subject);
  if (!subject) return undefined;
  return {
    pluginId: raw.pluginId,
    trusted: raw.trusted === true,
    ...(timestamp(raw.grantedAt) && raw.grantedAt > 0 ? { grantedAt: raw.grantedAt } : {}),
    legacyDangerous: raw.legacyDangerous === true,
    ...(nonEmpty(raw.lapsed) ? { lapsed: raw.lapsed } : {}),
    subject,
  };
}

export function parseFullTrustList(value: unknown): FullTrustListItem[] {
  const plugins = record(value)?.plugins;
  if (!Array.isArray(plugins)) return [];
  return plugins.flatMap((item) => {
    const raw = record(item);
    if (!raw || !nonEmpty(raw.pluginId) || !timestamp(raw.grantedAt)) return [];
    return [{
      pluginId: raw.pluginId,
      name: typeof raw.name === "string" && raw.name.trim() ? raw.name : raw.pluginId,
      version: typeof raw.version === "string" ? raw.version : "",
      grantedAt: raw.grantedAt,
    }];
  });
}

function parseAuditEntry(value: unknown): FullTrustAuditEntry | undefined {
  const raw = record(value);
  if (!raw || typeof raw.kind !== "string" || !AUDIT_KINDS.has(raw.kind)) return undefined;
  if (!timestamp(raw.at) || !timestamp(raw.lastAt) || !Number.isSafeInteger(raw.count) || (raw.count as number) < 1) return undefined;
  const entry: FullTrustAuditEntry = { at: raw.at, lastAt: raw.lastAt, count: raw.count as number, kind: raw.kind as FullTrustAuditEntry["kind"] };
  for (const field of AUDIT_TEXT_FIELDS) {
    const text = raw[field];
    if (text === undefined) continue;
    if (typeof text !== "string") return undefined;
    if (text) entry[field] = text;
  }
  return entry;
}

/** Drops malformed entries; keeps at most the newest FULL_TRUST_AUDIT_MAX. */
export function parseFullTrustAudit(value: unknown): FullTrustAuditEntry[] {
  const entries = record(value)?.entries;
  if (!Array.isArray(entries)) return [];
  return entries.flatMap((item) => parseAuditEntry(item) ?? []).slice(-FULL_TRUST_AUDIT_MAX);
}

/** Only an explicit `allowed: true` without a lapse skips the confirmation. */
export function parseAuthorizeReply(value: unknown): { allowed: boolean; lapsed?: string } {
  const raw = record(value);
  const lapsedValue = raw?.lapsed;
  const lapsed = nonEmpty(lapsedValue) ? lapsedValue : undefined;
  return lapsed ? { allowed: false, lapsed } : { allowed: raw?.allowed === true };
}
