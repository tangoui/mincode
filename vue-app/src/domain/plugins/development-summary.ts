import { DECLARATIVE_CONTRIBUTION_POINTS } from "./install-preview.ts";

/** Valid entry count and skipped-entry reasons for one contribution point of a dev session. */
export interface DevelopmentContributionSummary {
  count: number;
  issues: string[];
}

export interface DevelopmentContributionSource {
  keymapCount: number;
  keymapIssues: string[];
  contributions: Record<string, DevelopmentContributionSummary>;
}

export type DevelopmentStatusFilter = "all" | "running" | "notRunning";

interface DevelopmentCardSource {
  pluginName: string;
  pluginId: string;
  path: string;
  project?: { version: string };
  session?: object;
  runnerStatus?: string;
}

export function isDevelopmentCardActive(card: Pick<DevelopmentCardSource, "session" | "runnerStatus">) {
  return !!card.session || ["starting", "running", "stopping"].includes(card.runnerStatus ?? "");
}

export function filterDevelopmentCards<T extends DevelopmentCardSource>(cards: readonly T[], status: DevelopmentStatusFilter, search: string): T[] {
  const query = search.trim().toLocaleLowerCase();
  return cards.filter((card) => {
    const active = isDevelopmentCardActive(card);
    if (status === "running" && !active || status === "notRunning" && active) return false;
    return !query || [card.pluginName, card.pluginId, card.path, card.project?.version]
      .some((value) => value?.toLocaleLowerCase().includes(query));
  }).sort((a, b) => Number(isDevelopmentCardActive(b)) - Number(isDevelopmentCardActive(a)));
}

const MAX_POINTS = 32;
const MAX_ISSUES = 16;
const POINT_KEY = /^[a-zA-Z][a-zA-Z0-9]{0,63}$/;

function issueList(value: unknown, limit = MAX_ISSUES) {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string").slice(0, limit) : [];
}

/** Normalizes the host's `contributions` map (point key → { count, issues }) from a dev session summary. */
export function normalizeDevelopmentContributions(value: unknown): Record<string, DevelopmentContributionSummary> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const result: Record<string, DevelopmentContributionSummary> = {};
  for (const [key, raw] of Object.entries(value as Record<string, unknown>).slice(0, MAX_POINTS)) {
    if (!POINT_KEY.test(key) || !raw || typeof raw !== "object" || Array.isArray(raw)) continue;
    const entry = raw as Record<string, unknown>;
    const count = Number(entry.count);
    result[key] = { count: Number.isSafeInteger(count) && count >= 0 ? count : 0, issues: issueList(entry.issues) };
  }
  return result;
}

function orderedPoints(contributions: Record<string, DevelopmentContributionSummary>) {
  const known: readonly string[] = DECLARATIVE_CONTRIBUTION_POINTS;
  const others = Object.keys(contributions).filter((key) => !known.includes(key)).sort();
  return [...known.filter((key) => key in contributions), ...others];
}

/**
 * What a declarative (data-only) package contributes, e.g. `[{ point: "themes", count: 5 }]`.
 * Hosts that predate the per-point summary only report keymaps.
 */
export function declarativeContributionCounts(manifest: DevelopmentContributionSource) {
  if (!Object.keys(manifest.contributions).length) {
    return manifest.keymapCount > 0 ? [{ point: "keymaps", count: manifest.keymapCount }] : [];
  }
  return orderedPoints(manifest.contributions)
    .map((point) => ({ point, count: manifest.contributions[point].count }))
    .filter((item) => item.count > 0);
}

/** Skipped-entry reasons across every contribution point, capped for display. */
export function declarativeContributionIssues(manifest: DevelopmentContributionSource) {
  if (!Object.keys(manifest.contributions).length) return manifest.keymapIssues.slice(0, MAX_ISSUES);
  return orderedPoints(manifest.contributions)
    .flatMap((point) => manifest.contributions[point].issues)
    .slice(0, MAX_ISSUES);
}
