interface LinkedConversation {
  id: string;
  delegation?: { parentId: string };
}

export function createConversationTree<T extends LinkedConversation>(items: readonly T[]) {
  const byId = new Map(items.map((item) => [item.id, item]));
  const childrenById = new Map<string, T[]>();
  const roots: T[] = [];
  for (const item of items) {
    const parentId = item.delegation?.parentId;
    if (!parentId || parentId === item.id || !byId.has(parentId)) {
      roots.push(item);
      continue;
    }
    const children = childrenById.get(parentId) ?? [];
    children.push(item);
    childrenById.set(parentId, children);
  }

  function anchor(id: string) {
    const first = byId.get(id);
    let current = first;
    const seen = new Set([id]);
    while (current?.delegation) {
      const parent = byId.get(current.delegation.parentId);
      if (!parent) break;
      if (seen.has(parent.id)) return first;
      seen.add(parent.id);
      current = parent;
    }
    return current;
  }

  function rows(collapsed: ReadonlySet<string> = new Set()) {
    const visited = new Set<string>();
    const result: Array<{ item: T; depth: number; children: number }> = [];
    for (const root of [...roots, ...items]) {
      const stack = [{ item: root, depth: 0, visible: true }];
      while (stack.length) {
        const { item, depth, visible } = stack.pop()!;
        if (visited.has(item.id)) continue;
        visited.add(item.id);
        const children = childrenById.get(item.id) ?? [];
        if (visible) result.push({ item, depth, children: children.length });
        const showChildren = visible && !collapsed.has(item.id);
        for (let index = children.length - 1; index >= 0; index -= 1) {
          stack.push({ item: children[index], depth: depth + 1, visible: showChildren });
        }
      }
    }
    return result;
  }

  return { anchor, rows };
}
