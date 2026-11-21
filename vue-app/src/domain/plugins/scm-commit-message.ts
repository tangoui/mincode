/**
 * Frontend stage of the `scmCommitMessage` contribution point: re-validation
 * of the commit message providers the Go host accepted
 * (backend/plugin/contrib_scm_commit_message.go) and of the
 * `plugin_host_scm_commit_message` response. The host reads the staged
 * changes itself; the WebView only names the provider, project and
 * repository, never sends the draft, and only fills the draft with the
 * answer when the user has not edited it meanwhile. No DOM, Vue or IPC
 * here; nothing in this module calls the plugin.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  PROVIDER_ACTION_ID_PATTERN,
  PROVIDER_ENTRY_ID_PATTERN,
  PROVIDER_PLUGIN_ID_PATTERN,
  boundedLine,
  isRecord,
  localizedTitles,
  responseCount,
} from "@/domain/plugins/provider-points";

export const SCM_COMMIT_MESSAGE_CONTRIBUTION_POINT = "scmCommitMessage";

/** Mirrors contribution-points.json scmCommitMessage maxEntries and the limits the frontend uses. */
export const SCM_COMMIT_MESSAGE_LIMITS = {
  maxEntries: 1,
  titleMaxRunes: 32,
  localesMax: 16,
  messageMaxRunes: 4096,
} as const;

export interface PluginScmProvider {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  activeInWorkspace: boolean;
}

export interface ScmCommitMessageResult {
  message: string;
  truncated: boolean;
  diffTruncated: boolean;
  omittedFiles: number;
}

function validateProvider(raw: unknown): Pick<PluginScmProvider, "id" | "title" | "titleLocalized"> | undefined {
  if (!isRecord(raw)) return undefined;
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!PROVIDER_ENTRY_ID_PATTERN.test(id)) return undefined;
  const title = boundedLine(raw.title, SCM_COMMIT_MESSAGE_LIMITS.titleMaxRunes, true);
  if (!title) return undefined;
  const titleLocalized = localizedTitles(raw.titleLocalized, SCM_COMMIT_MESSAGE_LIMITS.titleMaxRunes, SCM_COMMIT_MESSAGE_LIMITS.localesMax);
  if (titleLocalized === false) return undefined;
  if (typeof raw.action !== "string" || !PROVIDER_ACTION_ID_PATTERN.test(raw.action)) return undefined;
  return { id, title, ...(titleLocalized ? { titleLocalized } : {}) };
}

/**
 * The providers of the process plugins in the contribution snapshot, in
 * ascending plugin id order, at most maxEntries per plugin. Entries without a
 * valid id, title or action are dropped. Inactive plugins are kept with
 * activeInWorkspace false; the commit box offers only active ones.
 */
export function collectScmProviders(contributions: readonly PointContribution[]): PluginScmProvider[] {
  const providers: PluginScmProvider[] = [];
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (contribution.runtime !== "process" || !PROVIDER_PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const seen = new Set<string>();
    for (const raw of contribution.point.entries.slice(0, SCM_COMMIT_MESSAGE_LIMITS.maxEntries)) {
      const entry = validateProvider(raw);
      if (!entry || seen.has(entry.id)) continue;
      seen.add(entry.id);
      providers.push({
        pluginId: contribution.pluginId,
        pluginName: contribution.pluginName,
        ...entry,
        activeInWorkspace: contribution.activeInWorkspace,
      });
    }
  }
  return providers;
}

// eslint-disable-next-line no-control-regex -- matching terminal escape sequences is the point
const ANSI_PATTERN = /\u001b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\u0007\u001b]*(?:\u0007|\u001b\\)|[@-Z\\-_])/g;
// eslint-disable-next-line no-control-regex -- C0 except tab and LF, DEL, C1, separators, bidi and zero-width
const UNSAFE_TEXT_PATTERN = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/g;

/**
 * Re-validates a `plugin_host_scm_commit_message` response like the host's
 * parseScmCommitMessageResult: line ends become LF, ANSI sequences and unsafe
 * characters are removed, surrounding blanks trimmed, the text cut to
 * messageMaxRunes. Undefined when there is no usable message.
 */
export function parseScmCommitMessageResponse(value: unknown): ScmCommitMessageResult | undefined {
  if (!isRecord(value) || typeof value.message !== "string") return undefined;
  const cleaned = value.message.replace(ANSI_PATTERN, "").replace(/\r\n?/g, "\n").replace(UNSAFE_TEXT_PATTERN, "").trim();
  if (!cleaned) return undefined;
  const runes = [...cleaned];
  const cut = runes.length > SCM_COMMIT_MESSAGE_LIMITS.messageMaxRunes;
  return {
    message: cut ? runes.slice(0, SCM_COMMIT_MESSAGE_LIMITS.messageMaxRunes).join("") : cleaned,
    truncated: cut || value.truncated === true,
    diffTruncated: value.diffTruncated === true,
    omittedFiles: responseCount(value.omittedFiles),
  };
}

/** A generated message replaces the draft only when the draft is still what it was when generation started. */
export function shouldApplyGeneratedMessage(draftAtStart: string, draftNow: string): boolean {
  return draftAtStart === draftNow;
}
