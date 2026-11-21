/**
 * Pure model of the plugin contribution snapshot returned by the Go host
 * (`plugin_host_contributions`, backend/plugin/contributions.go).
 * Entries are untrusted: every point consumer validates its own entries
 * before registering them. Nothing here touches IPC or Vue.
 */

export interface PluginContributionIssue {
  point: string;
  /** Index into contributes.<point>, or -1 for issues found across entries. */
  entry: number;
  key?: string;
  id?: string;
  message: string;
}

export interface PluginContributionPoint {
  /** Changes whenever the decoded entries change; lets consumers skip no-op refreshes. */
  fingerprint: string;
  entries: unknown[];
  issues: PluginContributionIssue[];
  /** Optional runtime state a point attaches (for example dynamic status bar text). */
  runtime?: unknown;
}

export interface PluginContributions {
  pluginId: string;
  pluginName: string;
  version: string;
  source: string;
  runtime: string;
  state: string;
  activeInWorkspace: boolean;
  points: Record<string, PluginContributionPoint>;
}

export interface PluginContributionsSnapshot {
  revision: number;
  plugins: PluginContributions[];
}

export interface PluginContributionsChanged {
  revision: number;
  pluginId?: string;
  points?: string[];
}

/** One plugin's data for one point, as handed to that point's consumer. */
export interface PointContribution {
  pluginId: string;
  pluginName: string;
  version: string;
  source: string;
  runtime: string;
  activeInWorkspace: boolean;
  point: PluginContributionPoint;
}

const MAX_PLUGINS = 1024;
const MAX_POINT_KEY_LENGTH = 64;

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function text(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function revisionOf(value: unknown): number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : 0;
}

function parseIssue(value: unknown, point: string): PluginContributionIssue | undefined {
  const record = objectValue(value);
  const message = text(record?.message).trim();
  if (!record || !message) return undefined;
  const key = text(record.key).trim();
  const id = text(record.id).trim();
  return {
    point: text(record.point).trim() || point,
    entry: Number.isInteger(record.entry) ? Number(record.entry) : -1,
    message,
    ...(key ? { key } : {}),
    ...(id ? { id } : {}),
  };
}

function parsePoint(value: unknown, point: string): PluginContributionPoint | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const issues = (Array.isArray(record.issues) ? record.issues : []).flatMap((issue) => parseIssue(issue, point) ?? []);
  return {
    fingerprint: text(record.fingerprint),
    entries: Array.isArray(record.entries) ? [...record.entries] : [],
    issues,
    ...(record.runtime !== undefined ? { runtime: record.runtime } : {}),
  };
}

/** Validates a `plugin_host_contributions` result; malformed plugins/points are dropped. */
export function parseContributionsSnapshot(raw: unknown): PluginContributionsSnapshot {
  const record = objectValue(raw);
  const plugins = (Array.isArray(record?.plugins) ? record.plugins : []).slice(0, MAX_PLUGINS).flatMap((value) => {
    const item = objectValue(value);
    const pluginId = text(item?.pluginId).trim();
    const pointsRecord = objectValue(item?.points);
    if (!item || !pluginId || !pointsRecord) return [];
    const points: Record<string, PluginContributionPoint> = {};
    for (const [key, pointValue] of Object.entries(pointsRecord)) {
      if (!key || key.length > MAX_POINT_KEY_LENGTH) continue;
      const point = parsePoint(pointValue, key);
      if (point) points[key] = point;
    }
    return [{
      pluginId,
      pluginName: text(item.pluginName).trim() || pluginId,
      version: text(item.version),
      source: text(item.source),
      runtime: text(item.runtime),
      state: text(item.state),
      activeInWorkspace: item.activeInWorkspace === true,
      points,
    }];
  });
  return { revision: revisionOf(record?.revision), plugins };
}

/** Validates a `plugin:contributions-changed` event payload. */
export function parseContributionsChanged(raw: unknown): PluginContributionsChanged {
  const record = objectValue(raw);
  const pluginId = text(record?.pluginId).trim();
  const points = Array.isArray(record?.points) ? record.points.filter((point): point is string => typeof point === "string") : [];
  return {
    revision: revisionOf(record?.revision),
    ...(pluginId ? { pluginId } : {}),
    ...(points.length ? { points } : {}),
  };
}

/** The contributions of one point across plugins, ordered by plugin id (the host order). */
export function pointContributions(snapshot: PluginContributionsSnapshot, point: string): PointContribution[] {
  return snapshot.plugins.flatMap((plugin) => {
    const data = plugin.points[point];
    return data
      ? [{
          pluginId: plugin.pluginId,
          pluginName: plugin.pluginName,
          version: plugin.version,
          source: plugin.source,
          runtime: plugin.runtime,
          activeInWorkspace: plugin.activeInWorkspace,
          point: data,
        }]
      : [];
  });
}

/**
 * A stable key for one point's data: it changes when any plugin's entries
 * (fingerprint), identity, workspace activity or runtime overlay change.
 * Consumers re-apply only when it differs from the last applied key.
 */
export function pointContributionsKey(contributions: readonly PointContribution[]): string {
  return JSON.stringify(
    contributions.map((item) => [item.pluginId, item.version, item.activeInWorkspace, item.point.fingerprint, item.point.issues.length, item.point.runtime ?? null]),
  );
}
