export interface OrderedConnectionGroup<T> {
  key: string;
  label: string;
  items: T[];
}

/**
 * Restores a compact, stable order for named connection groups.
 * The empty key is reserved for "Ungrouped" and never participates in ordering.
 */
export function normalizeConnectionGroupOrder(value: unknown): string[] {
  if (!Array.isArray(value)) return [];

  const seen = new Set<string>();
  const order: string[] = [];
  for (const item of value) {
    if (typeof item !== "string") continue;
    const group = item.trim();
    if (!group || seen.has(group)) continue;
    seen.add(group);
    order.push(group);
  }
  return order;
}

/**
 * Applies the saved order to the visible groups. New groups have a predictable
 * alphabetical fallback, while the ungrouped bucket remains at the end.
 */
export function orderConnectionGroups<T, Group extends OrderedConnectionGroup<T>>(
  groups: Group[],
  groupOrder: unknown
): Group[] {
  const positions = new Map(normalizeConnectionGroupOrder(groupOrder).map((key, index) => [key, index]));

  return [...groups].sort((left, right) => {
    if (!left.key) return right.key ? 1 : 0;
    if (!right.key) return -1;

    const leftPosition = positions.get(left.key);
    const rightPosition = positions.get(right.key);
    if (leftPosition !== undefined || rightPosition !== undefined) {
      if (leftPosition === undefined) return 1;
      if (rightPosition === undefined) return -1;
      return leftPosition - rightPosition;
    }

    return left.label.localeCompare(right.label, undefined, {
      numeric: true,
      sensitivity: "base",
    });
  });
}
