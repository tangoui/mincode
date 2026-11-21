import {
  restoreHttpCollections,
  type HttpEnvironment,
  type HttpKeyValueRow,
  type HttpParameterType,
  type HttpRequestCollection,
  type SavedHttpRequest,
} from "./request-collection.ts";

export const HTTP_RESOURCES_VERSION = 2 as const;
const MAX_HTTP_ENVIRONMENTS = 256;
const MAX_HTTP_ENVIRONMENT_ROWS = 2_000;

export type { HttpEnvironment } from "./request-collection.ts";

export interface HttpResourcesSnapshot {
  version: typeof HTTP_RESOURCES_VERSION;
  environments: HttpEnvironment[];
  collections: HttpRequestCollection[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function restoreEnvironmentRows(value: unknown): HttpKeyValueRow[] {
  if (!Array.isArray(value)) return [];
  return value.slice(0, MAX_HTTP_ENVIRONMENT_ROWS).flatMap((candidate): HttpKeyValueRow[] => {
    if (!isRecord(candidate) || typeof candidate.key !== "string" || typeof candidate.value !== "string") {
      return [];
    }
    const valueType =
      typeof candidate.valueType === "string" &&
      ["string", "integer", "number", "boolean", "array", "object", "file"].includes(
        candidate.valueType
      )
        ? (candidate.valueType as HttpParameterType)
        : "string";
    return [
      {
        id:
          typeof candidate.id === "string" && candidate.id.trim()
            ? candidate.id
            : crypto.randomUUID(),
        key: candidate.key,
        value: candidate.value,
        enabled: candidate.enabled !== false,
        valueType,
        description: typeof candidate.description === "string" ? candidate.description : "",
        fileName: typeof candidate.fileName === "string" ? candidate.fileName : undefined,
      },
    ];
  });
}

export function restoreHttpEnvironments(value: unknown): HttpEnvironment[] {
  if (!Array.isArray(value)) return [];
  const ids = new Set<string>();
  return value.slice(0, MAX_HTTP_ENVIRONMENTS).flatMap((candidate): HttpEnvironment[] => {
    if (!isRecord(candidate) || typeof candidate.name !== "string" || !candidate.name.trim()) {
      return [];
    }
    const id = typeof candidate.id === "string" && candidate.id.trim()
      ? candidate.id.trim()
      : crypto.randomUUID();
    if (ids.has(id)) return [];
    ids.add(id);
    return [{ id, name: candidate.name.trim(), vars: restoreEnvironmentRows(candidate.vars) }];
  });
}

export function restoreHttpResources(value: unknown): HttpResourcesSnapshot {
  if (!isRecord(value)) {
    return { version: HTTP_RESOURCES_VERSION, environments: [], collections: [] };
  }
  const legacyEnvironments = restoreHttpEnvironments(value.environments);
  return {
    version: HTTP_RESOURCES_VERSION,
    // 保留旧字段只用于兼容环境包和旧快照；运行时环境归属顶级集合。
    environments: [],
    collections: restoreHttpCollections(value.collections, legacyEnvironments),
  };
}

function cloneRequest(request: SavedHttpRequest): SavedHttpRequest {
  return structuredClone(request);
}

function cloneCollection(collection: HttpRequestCollection): HttpRequestCollection {
  return {
    ...collection,
    requests: collection.requests.map(cloneRequest),
    children: collection.children?.map(cloneCollection) ?? [],
    environments: collection.environments?.map((environment) => structuredClone(environment)),
  };
}

function mergeRequests(
  current: SavedHttpRequest[],
  incoming: readonly SavedHttpRequest[]
): SavedHttpRequest[] {
  const byId = new Map(current.map((request) => [request.id, request]));
  for (const request of incoming) {
    const previous = byId.get(request.id);
    if (!previous || (request.updatedAt ?? 0) > (previous.updatedAt ?? 0)) {
      byId.set(request.id, cloneRequest(request));
    }
  }
  return [...byId.values()];
}

function mergeCollectionList(
  current: HttpRequestCollection[],
  incoming: readonly HttpRequestCollection[]
): HttpRequestCollection[] {
  const byId = new Map(current.map((collection) => [collection.id, collection]));
  for (const collection of incoming) {
    const previous = byId.get(collection.id);
    if (!previous) {
      const cloned = cloneCollection(collection);
      current.push(cloned);
      byId.set(cloned.id, cloned);
      continue;
    }
    previous.requests = mergeRequests(previous.requests, collection.requests);
    previous.children = mergeCollectionList(
      previous.children ?? [],
      collection.children ?? []
    );
    if (!previous.environments?.length && collection.environments?.length) {
      previous.environments = structuredClone(collection.environments);
      previous.activeEnvironmentId = collection.activeEnvironmentId;
    }
  }
  return current;
}

function mergeEnvironmentList(
  current: HttpEnvironment[],
  incoming: readonly HttpEnvironment[]
): HttpEnvironment[] {
  const ids = new Set(current.map((environment) => environment.id));
  for (const environment of incoming) {
    if (ids.has(environment.id)) continue;
    current.push(structuredClone(environment));
    ids.add(environment.id);
  }
  return current;
}

/**
 * Combines durable HTTP resources from every legacy workspace snapshot. IDs are
 * stable, so unrelated workspace resources are retained and duplicate requests
 * keep the most recently updated copy.
 */
export function mergeLegacyHttpResourceStates(states: readonly unknown[]): HttpResourcesSnapshot {
  const merged: HttpResourcesSnapshot = {
    version: HTTP_RESOURCES_VERSION,
    environments: [],
    collections: [],
  };
  for (const state of states) {
    const restored = restoreHttpResources(state);
    mergeEnvironmentList(merged.environments, restored.environments);
    mergeCollectionList(merged.collections, restored.collections);
  }
  return merged;
}
