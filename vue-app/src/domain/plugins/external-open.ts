import contract from "@/domain/plugins/contribution-points.json" with { type: "json" };
import type { PointContribution } from "@/domain/plugins/contributions";

export interface ExternalCandidate {
  pluginId: string;
  pluginName: string;
  handlerId: string;
  view: string;
  type: string;
  fingerprint: string;
}
const externalTypes: readonly string[] = contract.points.externalOpenHandlers.lists.types;
const safeId = (v: unknown): v is string => typeof v === "string" && /^[a-z0-9][a-zA-Z0-9._-]{0,127}$/.test(v);
const text = (v: unknown, max = 128): v is string => typeof v === "string" && v.length > 0 && v.length <= max;
export function externalCandidate(value: unknown): ExternalCandidate | undefined {
  if (!value || typeof value !== "object") return;
  const v = value as Record<string, unknown>;
  if (!safeId(v.pluginId) || !text(v.pluginName, 256) || !safeId(v.handlerId) || !text(v.handlerId,32) || !safeId(v.view) || !text(v.view,64) || !text(v.type,32) || !externalTypes.includes(v.type) || !text(v.fingerprint)) return;
  return { pluginId: v.pluginId, pluginName: v.pluginName, handlerId: v.handlerId, view: v.view, type: v.type, fingerprint: v.fingerprint };
}
export function validExternalContributions(items: PointContribution[]): string[] {
  return items.flatMap(item => item.point.entries.flatMap(raw => {
    if (!raw || typeof raw !== "object") return [];
    const e = raw as Record<string, unknown>;
    return safeId(e.id) && text(e.id,32) && text(e.type,32) && externalTypes.includes(e.type) && safeId(e.view) && text(e.view,64) ? [`${item.pluginId}/${e.id}`] : [];
  }));
}
