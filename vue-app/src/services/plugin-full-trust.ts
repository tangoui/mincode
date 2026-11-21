// Frontend side of plugin full trust (backend/plugin/full_trust_commands.go).
// Go is the only judge: callers ask authorizeWithoutPrompt right before a
// confirmation the design lists as skippable (page actions, CodeLens,
// notification and tray commands, test runs) and show their normal prompt
// whenever it answers false. Never use it for host.terminal.open,
// host.project.open, grants, provider pickers, external links or
// plugin-requested confirms. Imported only by lazily loaded modules so it
// stays out of the entry bundle (scripts/plugin-full-trust.test.mjs).
import { ElMessage } from "element-plus";
import {
  fullTrustRisk,
  parseAuthorizeReply,
  parseFullTrustAudit,
  parseFullTrustList,
  parseFullTrustStatus,
  type FullTrustAuditEntry,
  type FullTrustListItem,
  type FullTrustStatus,
  type FullTrustSurface,
} from "@/domain/plugins/full-trust";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { listPlugins } from "@/services/plugin-service";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

export const FULL_TRUST_LAPSE_REASONS = [
  "version_changed",
  "permissions_changed",
  "package_changed",
  "disabled",
  "uninstalled",
  "upgraded",
  "cleared",
  "not_eligible",
] as const;

const lapseNoticeShown = new Set<string>();

/**
 * True only when Go says this confirmation may be skipped (Go audits the
 * skip). Never throws: outside the desktop runtime, on any error or on an
 * unexpected reply the answer is false and the caller confirms as usual.
 */
export async function authorizeWithoutPrompt(
  pluginId: string,
  surface: FullTrustSurface,
  risk: string | undefined,
  subject: string,
): Promise<boolean> {
  if (!isDesktopRuntime()) return false;
  try {
    const reply = parseAuthorizeReply(
      await invokeBackend<unknown>("plugin_full_trust_authorize", { pluginId, surface, risk: fullTrustRisk(risk), subject }),
    );
    if (reply.lapsed) void noticeLapsed(pluginId, reply.lapsed);
    return reply.allowed;
  } catch {
    return false;
  }
}

/** Tells the user once per plugin why a trusted plugin asks again. */
async function noticeLapsed(pluginId: string, reason: string) {
  if (lapseNoticeShown.has(pluginId)) return;
  lapseNoticeShown.add(pluginId);
  const { t } = useVueI18n();
  let name = pluginId;
  try {
    name = (await listPlugins()).find((plugin) => plugin.id === pluginId)?.name.trim() || pluginId;
  } catch {
    // Keep the plugin ID.
  }
  ElMessage.warning(t("plugins.fullTrust.lapsedNotice", { name, reason: fullTrustReasonText(reason) }));
}

export function fullTrustReasonText(reason: string): string {
  const { t } = useVueI18n();
  return (FULL_TRUST_LAPSE_REASONS as readonly string[]).includes(reason)
    ? t(`plugins.fullTrust.reason.${reason}` as MessageKey)
    : reason;
}

async function statusOf(value: Promise<unknown>): Promise<FullTrustStatus> {
  const status = parseFullTrustStatus(await value);
  if (!status) throw new Error("plugin_full_trust: unexpected reply");
  return status;
}

/**
 * Record state and eligibility. Only `full` (asked right before the warning)
 * makes Go hash the plugin's package for the fingerprint to trust; without it
 * `subject.fingerprint` is empty unless a stored record was re-checked.
 */
export function getFullTrust(pluginId: string, options: { full?: boolean } = {}): Promise<FullTrustStatus> {
  return statusOf(invokeBackend<unknown>("plugin_full_trust_get", { pluginId, ...(options.full ? { full: true } : {}) }));
}

export async function setFullTrust(pluginId: string, trusted: boolean, fingerprint?: string): Promise<FullTrustStatus> {
  const status = await statusOf(invokeBackend<unknown>("plugin_full_trust_set", { pluginId, trusted, ...(fingerprint ? { fingerprint } : {}) }));
  // Trusted again: a later lapse deserves its own notice.
  if (status.trusted) lapseNoticeShown.delete(pluginId);
  return status;
}

export async function listFullTrust(): Promise<FullTrustListItem[]> {
  return parseFullTrustList(await invokeBackend<unknown>("plugin_full_trust_list", {}));
}

export async function getFullTrustAudit(pluginId: string): Promise<FullTrustAuditEntry[]> {
  return parseFullTrustAudit(await invokeBackend<unknown>("plugin_full_trust_audit", { pluginId }));
}

export async function clearFullTrustAudit(pluginId: string): Promise<void> {
  await invokeBackend<unknown>("plugin_full_trust_audit_clear", { pluginId });
}

export function listenFullTrustChanged(
  handler: (event: { pluginId: string; trusted: boolean; reason: string }) => void,
): () => void {
  if (!isDesktopRuntime()) return () => undefined;
  return listenAppEvent<unknown>("plugin:full-trust-changed", (payload) => {
    const raw = payload && typeof payload === "object" ? payload as Record<string, unknown> : undefined;
    if (!raw || typeof raw.pluginId !== "string" || !raw.pluginId) return;
    const trusted = raw.trusted === true;
    if (trusted) lapseNoticeShown.delete(raw.pluginId);
    handler({ pluginId: raw.pluginId, trusted, reason: typeof raw.reason === "string" ? raw.reason : "" });
  });
}
