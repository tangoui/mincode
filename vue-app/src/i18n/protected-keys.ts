/**
 * Host message keys that a language pack (contributes.localizations) may
 * never translate. They cover approval and grant dialogs, capability and
 * install/upgrade disclosures, destructive confirmations, secrets, SSH
 * host fingerprints, paste confirmation, shared `common.*` buttons and
 * the language setting row itself (so the user can always switch back). A
 * protected key always renders in the user's base language (zh or en).
 *
 * Pure module: `domain/i18n/locale-pack.ts` drops protected keys when it
 * builds an overlay and `i18n/messages.ts` filters them again when an
 * overlay is installed; `translate` itself never consults this list.
 * vue-app/scripts/localizations-contributions.test.mjs asserts that every
 * prefix and key below still matches the catalogue and that a sentinel list
 * stays protected. Extend this file whenever new security copy is added.
 */

/** Every key starting with one of these is protected. */
export const PROTECTED_MESSAGE_PREFIXES: readonly string[] = [
  "common.",
  "settings.language",
  "settings.ai.dangerous",
  "settings.ai.clearAuthorizations",
  "settings.ai.revokeAuthorization",
  "ai.approvalMode.",
  // Plugin database driver credential release (Phase E).
  "pluginContrib.databaseDrivers.grant",
  "pluginContrib.databaseDrivers.release",
  "pluginContrib.databaseDrivers.trust",
  "ai.agentic.approval",
  "ai.agentic.approve",
  "ai.agentic.inlineApproval",
  "ai.agentic.allow",
  "ai.agentic.reject",
  "ai.agentic.risk",
  "ai.agentic.sessionApproved",
  "ai.live.approval",
  "ai.external.approval",
  "plugins.permissions",
  "plugins.capabilit",
  "plugins.noCapabilities",
  "plugins.enableCapability",
  "plugins.enableLocalConfirm",
  "plugins.localTrustWarning",
  "plugins.aiGrants.",
  // Full trust: the warning, badge, preview line and lapse notice (what is and is not bypassed).
  "plugins.fullTrust.",
  "plugins.development.approve",
  "plugins.development.trustWarning",
  "plugins.install",
  "plugins.preview",
  "plugins.declarativePreviewNotice",
  "plugins.confirmInstall",
  "plugins.actionConfirm",
  // Confirmation of a dangerous service call the host makes.
  "plugins.services.confirm",
  // An unconfirmed cancellation must never read as "cancelled" or "no side effects".
  "plugins.services.unconfirmed",
  "plugins.trustedConfirm",
  "plugins.pathPickerConfirm",
  "plugins.clipboardSensitive",
  "plugins.clipboardReplace",
  // ai.config: the host confirmation before plugin-proposed AI settings are written.
  "plugins.aiConfig.",
  // terminalProfiles launch confirmations (keys added with that point).
  "pluginContrib.terminalProfiles.",
  // Process launch confirmations shared by tasks / languageServers / linters.
  "pluginContrib.processLaunch.",
  // Plugin ACP agents: the environment / connector disclosures of their launch confirmation.
  "ai.pluginAgent.",
  "pluginContrib.customEditors.defaultConfirm",
  // Notification buttons and the tray: confirmations, origin names and failures of plugin shell commands.
  "pluginContrib.shell.",
  // CodeLens command confirmation (the lens title is the plugin's; the dialog wording is the host's).
  "pluginContrib.languageFeatures.codeLensConfirm",
  // Notification action buttons' accessible names: which plugin a button comes from.
  "notifications.pluginAction",
  "pluginContrib.customEditors.overwrite",
  "pluginContrib.configuration.clearSecret",
  "pluginContrib.configuration.secret",
  "pluginContrib.configuration.setSecret",
  "pluginContrib.configuration.replaceSecret",
  "terminal.paste",
  "ssh.knownHosts",
  "ssh.credentials",
  "ssh.saveCredentials",
  "ssh.password",
  "project.confirm.",
  "database.schemaSync.risk",
  "database.schemaSync.confirm",
  "database.confirm",
  "database.destructive",
  "commandPalette.pluginParams.confirm",
  "sessions.terminalActivity.waiting_approval",
  // Embedded browser page dialogs: which page is asking must not be re-labelled.
  "browser.pageMessage.dialog",
];

/** Exact protected keys that no prefix covers. */
export const PROTECTED_MESSAGE_KEYS: readonly string[] = [
  "pluginContrib.customEditors.remember",
  // httpCodeGenerators: tells the user what the rendered code leaves out.
  "pluginContrib.httpCodeGenerators.redactionNotice",
  // settingsPages: the header outside the frame names the plugin and that it cannot read MinCode settings.
  "pluginContrib.settingsPages.header",
];

/**
 * Structural patterns: destructive/approval confirmations, dangerous or
 * destructive actions, secrets, passwords and fingerprints anywhere in the
 * catalogue, forced git operations, and every contribution-point install
 * preview line (`pluginContrib.<point>.preview*`).
 */
export const PROTECTED_MESSAGE_PATTERNS: readonly RegExp[] = [
  /[Cc]onfirm/,
  /[Dd]anger/,
  /[Dd]estructive/,
  /[Ss]ecret/,
  /[Pp]assword|[Pp]asscode/,
  /[Ff]ingerprint/,
  /^git\..*[Ff]orce/,
  /^pluginContrib\.[^.]+\.preview/,
];

const protectedKeySet = new Set(PROTECTED_MESSAGE_KEYS);

/** True when a language pack must never translate `key`. */
export function isProtectedMessageKey(key: string): boolean {
  if (protectedKeySet.has(key)) return true;
  for (const prefix of PROTECTED_MESSAGE_PREFIXES) {
    if (key.startsWith(prefix)) return true;
  }
  for (const pattern of PROTECTED_MESSAGE_PATTERNS) {
    if (pattern.test(key)) return true;
  }
  return false;
}
