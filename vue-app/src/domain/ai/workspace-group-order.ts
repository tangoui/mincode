export type WorkspaceGroupDropPosition = "before" | "after";

export interface WorkspaceGroupDropTarget {
  key: string;
  position: WorkspaceGroupDropPosition;
}

export interface WorkspaceGroupDropCandidate {
  key: string;
  top: number;
  bottom: number;
}

export function resolveWorkspaceGroupDropTarget(
  candidates: readonly WorkspaceGroupDropCandidate[],
  sourceKey: string,
  pointerY: number,
): WorkspaceGroupDropTarget | undefined {
  if (!sourceKey || !Number.isFinite(pointerY)) return undefined;
  const source = candidates.find((candidate) => candidate.key === sourceKey);
  if (source && pointerY >= source.top && pointerY <= source.bottom) return undefined;
  let nearest: { target: WorkspaceGroupDropTarget; distance: number } | undefined;
  for (const candidate of candidates) {
    if (
      !candidate.key
      || candidate.key === sourceKey
      || !Number.isFinite(candidate.top)
      || !Number.isFinite(candidate.bottom)
      || candidate.bottom < candidate.top
    ) continue;
    const midpoint = candidate.top + (candidate.bottom - candidate.top) / 2;
    const distance = Math.abs(pointerY - midpoint);
    if (nearest && nearest.distance <= distance) continue;
    nearest = {
      target: {
        key: candidate.key,
        position: pointerY < midpoint ? "before" : "after",
      },
      distance,
    };
  }
  return nearest?.target;
}

export function normalizeWorkspaceGroupOrder(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const result: string[] = [];
  for (const item of value) {
    if (typeof item !== "string") continue;
    const key = item.trim();
    if (!key || seen.has(key)) continue;
    seen.add(key);
    result.push(key);
  }
  return result;
}

export function appendWorkspaceGroupKeys(order: readonly string[], keys: readonly string[]) {
  const result = normalizeWorkspaceGroupOrder(order);
  const seen = new Set(result);
  for (const key of normalizeWorkspaceGroupOrder(keys)) {
    if (seen.has(key)) continue;
    seen.add(key);
    result.push(key);
  }
  return result;
}

export function applyVisibleWorkspaceGroupOrder(
  currentOrder: readonly string[],
  visibleOrder: readonly string[],
) {
  const visible = normalizeWorkspaceGroupOrder(visibleOrder);
  const visibleKeys = new Set(visible);
  const orderedKeys = appendWorkspaceGroupKeys(currentOrder, visible);
  let visibleIndex = 0;
  return orderedKeys.map((key) =>
    visibleKeys.has(key) ? visible[visibleIndex++] : key
  );
}

export function moveWorkspaceGroupKey(
  keys: readonly string[],
  sourceKey: string,
  targetKey: string,
  position: WorkspaceGroupDropPosition,
) {
  const result = normalizeWorkspaceGroupOrder(keys);
  if (!sourceKey || !targetKey || sourceKey === targetKey || !result.includes(sourceKey)) {
    return result;
  }
  const withoutSource = result.filter((key) => key !== sourceKey);
  const targetIndex = withoutSource.indexOf(targetKey);
  if (targetIndex < 0) return result;
  withoutSource.splice(targetIndex + (position === "after" ? 1 : 0), 0, sourceKey);
  return withoutSource;
}
