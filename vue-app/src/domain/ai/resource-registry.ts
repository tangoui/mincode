export type AiResourceState = "available" | "busy" | "unavailable";

export interface AiResource {
  id: string;
  kind: string;
  name: string;
  state: AiResourceState;
  active?: boolean;
  capabilities: string[];
  metadata?: Record<string, unknown>;
}

export interface AiResourceTarget {
  resourceId?: string;
  name?: string;
  capability?: string;
}

export type AiResourceResolution =
  | { status: "resolved"; resource: AiResource }
  | { status: "ambiguous"; candidates: AiResource[] }
  | { status: "not_found"; candidates: AiResource[] };

export interface AiResourceRegistry {
  replace(source: string, resources: AiResource[]): void;
  setActive(resourceId: string | undefined): void;
  list(capability?: string): AiResource[];
  resolve(target: AiResourceTarget): AiResourceResolution;
}

function normalizeName(value: string) {
  return value.trim().replace(/\s+/g, "").toLocaleLowerCase();
}

export function createResourceRegistry(): AiResourceRegistry {
  const resourcesBySource = new Map<string, AiResource[]>();
  let activeResourceId: string | undefined;

  function list(capability?: string) {
    const seen = new Set<string>();
    const resources: AiResource[] = [];
    for (const sourceResources of resourcesBySource.values()) {
      for (const resource of sourceResources) {
        if (seen.has(resource.id) || resource.state === "unavailable") continue;
        if (capability && !resource.capabilities.includes(capability)) continue;
        seen.add(resource.id);
        resources.push(resource);
      }
    }
    return resources;
  }

  return {
    replace(source, resources) {
      resourcesBySource.set(source, resources.slice());
    },
    setActive(resourceId) {
      activeResourceId = resourceId?.trim() || undefined;
    },
    list,
    resolve(target) {
      const candidates = list(target.capability);
      const explicitId = target.resourceId?.trim();
      if (explicitId) {
        const resource = candidates.find((item) => item.id === explicitId);
        return resource
          ? { status: "resolved", resource }
          : { status: "not_found", candidates: [] };
      }

      const targetName = target.name ? normalizeName(target.name) : "";
      if (targetName) {
        const exact = candidates.filter((item) => normalizeName(item.name) === targetName);
        if (exact.length === 1) return { status: "resolved", resource: exact[0] };
        if (exact.length > 1) return { status: "ambiguous", candidates: exact };

        const partial = candidates.filter((item) => normalizeName(item.name).includes(targetName));
        if (partial.length === 1) return { status: "resolved", resource: partial[0] };
        if (partial.length > 1) return { status: "ambiguous", candidates: partial };
        return { status: "not_found", candidates: [] };
      }

      const snapshotActive = candidates.filter((item) => item.active);
      if (snapshotActive.length === 1) return { status: "resolved", resource: snapshotActive[0] };
      if (snapshotActive.length > 1) return { status: "ambiguous", candidates: snapshotActive };
      const active = candidates.find((item) => item.id === activeResourceId);
      if (active) return { status: "resolved", resource: active };
      if (candidates.length === 1) return { status: "resolved", resource: candidates[0] };
      return candidates.length > 1
        ? { status: "ambiguous", candidates }
        : { status: "not_found", candidates: [] };
    },
  };
}

export const aiResourceRegistry = createResourceRegistry();
