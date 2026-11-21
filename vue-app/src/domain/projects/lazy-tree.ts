// A lazily loaded tree model for the project explorer, independent of any UI. Node names
// and semantics follow Element Plus' el-tree Node (childNodes, loaded, expand(callback,
// expandParent), insertChild, remove) so callers written against el-tree keep working,
// while rendering is left to a virtualized list that draws only the visible rows.

export interface LazyTreeOptions<T> {
  key: (data: T) => string;
  isLeaf: (data: T) => boolean;
  /** Called for the synthetic level-0 root on start, then once per directory until it is marked unloaded. */
  load: (node: LazyTreeNode<T>, resolve: (children: T[]) => void) => void;
  defaultExpandedKeys?: readonly string[];
}

/** What ProjectVirtualTree exposes through a template ref. */
export interface LazyTreeApi<T> {
  root: LazyTreeNode<T>;
  getNode(key: string): LazyTreeNode<T> | undefined;
  remove(target: LazyTreeNode<T> | T): void;
  setCurrentKey(key: string | null): void;
  getCurrentKey(): string | null;
  scrollToNode(key: string, align?: "center" | "nearest"): void;
}

/** Same call shape as Element Plus' LoadFunction, typed for the explorer's node data. */
export type LoadFunction<T = unknown> = LazyTreeOptions<T>["load"];

export class LazyTreeNode<T> {
  childNodes: LazyTreeNode<T>[] = [];
  expanded = false;
  loaded = false;
  loading = false;
  isLeaf: boolean;
  /** Callbacks waiting for an in-flight load. */
  private pending: (() => void)[] = [];

  readonly tree: LazyTree<T>;
  data: T;
  readonly level: number;
  parent: LazyTreeNode<T> | null;

  constructor(tree: LazyTree<T>, data: T, level: number, parent: LazyTreeNode<T> | null) {
    this.tree = tree;
    this.data = data;
    this.level = level;
    this.parent = parent;
    this.isLeaf = level > 0 && tree.options.isLeaf(data);
  }

  get key(): string {
    return this.level === 0 ? "" : this.tree.options.key(this.data);
  }

  expand(callback?: () => void, expandParent = false) {
    if (expandParent) {
      for (let ancestor = this.parent; ancestor && ancestor.level > 0; ancestor = ancestor.parent) ancestor.expanded = true;
    }
    const done = () => {
      this.expanded = true;
      callback?.();
    };
    if (this.isLeaf) return done();
    this.loadData(done);
  }

  collapse() {
    this.expanded = false;
  }

  /** Loads children unless already loaded; the callback runs once they are present. */
  loadData(callback?: () => void) {
    if (this.loaded) return callback?.();
    if (callback) this.pending.push(callback);
    if (this.loading) return;
    this.loading = true;
    let settled = false;
    this.tree.options.load(this, (children) => {
      if (settled) return;
      settled = true;
      for (const child of this.childNodes) this.tree.unindex(child);
      this.childNodes = [];
      for (const data of children) this.insertChild({ data });
      this.loaded = true;
      this.loading = false;
      const waiting = this.pending;
      this.pending = [];
      for (const run of waiting) run();
    });
  }

  insertChild(child: { data: T }, index?: number): LazyTreeNode<T> {
    const node = new LazyTreeNode(this.tree, child.data, this.level + 1, this);
    const at = index === undefined || index < 0 || index >= this.childNodes.length ? this.childNodes.length : index;
    this.childNodes.splice(at, 0, node);
    this.tree.index(node);
    // Read the node back through this.childNodes: when the tree is wrapped in a reactive
    // proxy (the explorer view), that yields the observable node. Mutating the raw instance
    // (expand → load → expanded) would leave the rendered rows stale.
    const stored = this.childNodes[at];
    if (this.tree.defaultExpanded.has(stored.key)) stored.expand();
    return stored;
  }

  remove() {
    if (!this.parent) return;
    const siblings = this.parent.childNodes;
    const at = siblings.indexOf(this);
    if (at >= 0) siblings.splice(at, 1);
    this.tree.unindex(this);
    this.parent = null;
  }
}

export class LazyTree<T> {
  readonly root: LazyTreeNode<T>;
  defaultExpanded: Set<string>;
  private readonly byKey = new Map<string, LazyTreeNode<T>>();

  readonly options: LazyTreeOptions<T>;

  constructor(options: LazyTreeOptions<T>) {
    this.options = options;
    this.defaultExpanded = new Set(options.defaultExpandedKeys ?? []);
    this.root = new LazyTreeNode<T>(this, undefined as T, 0, null);
  }

  /** Loads the top level (el-tree calls load for its synthetic root on mount). */
  start() {
    this.root.loadData();
  }

  getNode(key: string): LazyTreeNode<T> | undefined {
    return this.byKey.get(key);
  }

  remove(target: LazyTreeNode<T> | T) {
    const node = target instanceof LazyTreeNode ? target : this.byKey.get(this.options.key(target));
    node?.remove();
  }

  /** Expands keys that exist now; later-created nodes pick them up on creation. */
  setDefaultExpandedKeys(keys: readonly string[]) {
    this.defaultExpanded = new Set(keys);
    for (const key of keys) this.byKey.get(key)?.expand(undefined, true);
  }

  /** @internal */
  index(node: LazyTreeNode<T>) {
    this.byKey.set(node.key, node);
  }

  /** @internal Drops a node and its loaded descendants from the key index. */
  unindex(node: LazyTreeNode<T>) {
    if (this.byKey.get(node.key) === node) this.byKey.delete(node.key);
    for (const child of node.childNodes) this.unindex(child);
  }
}

/** Expanded nodes in display order (depth-first), excluding the synthetic root. */
export function visibleRows<T>(root: LazyTreeNode<T>): LazyTreeNode<T>[] {
  const rows: LazyTreeNode<T>[] = [];
  const stack = [...root.childNodes].reverse();
  while (stack.length) {
    const node = stack.pop()!;
    rows.push(node);
    if (node.expanded) for (let i = node.childNodes.length - 1; i >= 0; i -= 1) stack.push(node.childNodes[i]);
  }
  return rows;
}

export interface WindowInput {
  count: number;
  rowHeight: number;
  scrollTop: number;
  viewportHeight: number;
  overscan: number;
}

/** Half-open index range [start, end) of rows to render, with overscan on both sides. */
export function visibleWindow({ count, rowHeight, scrollTop, viewportHeight, overscan }: WindowInput) {
  if (count <= 0 || rowHeight <= 0) return { start: 0, end: 0 };
  // The content can shrink under a scrolled viewport (collapse); clamp to the real scroll range.
  const top = Math.max(0, Math.min(scrollTop, count * rowHeight - viewportHeight));
  const start = Math.max(0, Math.floor(top / rowHeight) - overscan);
  const end = Math.min(count, Math.ceil((top + Math.max(0, viewportHeight)) / rowHeight) + overscan);
  return { start, end };
}

export interface StickyRow<T> {
  node: LazyTreeNode<T>;
  /** Offset from the top of the viewport; negative while being pushed out by the next sibling. */
  top: number;
}

/**
 * Expanded ancestors that scrolled above their slot (level N pins at slot N), the way CSS
 * sticky behaves for nested folders: each stays until the end of its subtree pushes it up.
 */
export function stickyRows<T>(rows: readonly LazyTreeNode<T>[], { rowHeight, scrollTop, maxLevels }: { rowHeight: number; scrollTop: number; maxLevels: number }): StickyRow<T>[] {
  const pinned: StickyRow<T>[] = [];
  if (!rows.length || rowHeight <= 0) return pinned;
  const indexOf = new Map<LazyTreeNode<T>, number>();
  const position = (node: LazyTreeNode<T>) => {
    let index = indexOf.get(node);
    if (index === undefined) {
      index = rows.indexOf(node);
      indexOf.set(node, index);
    }
    return index;
  };
  for (let level = 1; level <= maxLevels; level += 1) {
    const slotTop = (level - 1) * rowHeight;
    const under = rows[Math.floor((scrollTop + slotTop) / rowHeight)];
    if (!under) break;
    let candidate: LazyTreeNode<T> | null = under;
    while (candidate && candidate.level > level) candidate = candidate.parent;
    if (!candidate || candidate.level !== level || !candidate.expanded || !candidate.childNodes.length) break;
    const start = position(candidate);
    if (start < 0 || start * rowHeight >= scrollTop + slotTop) break;
    // The subtree ends after its last visible descendant.
    let last: LazyTreeNode<T> = candidate;
    while (last.expanded && last.childNodes.length) last = last.childNodes[last.childNodes.length - 1];
    const subtreeBottom = (position(last) + 1) * rowHeight - scrollTop;
    pinned.push({ node: candidate, top: Math.min(slotTop, subtreeBottom - rowHeight) });
  }
  return pinned;
}
