/**
 * Outline panel tree: document symbols (Monaco `languages.DocumentSymbol`, merged from every
 * provider) turned into a stable, filterable tree. Kept free of Monaco imports so it can be
 * tested in node.
 */

export interface OutlineSymbolInput {
  name: string;
  detail?: string;
  kind: number;
  range: { startLineNumber: number; startColumn: number; endLineNumber?: number };
  selectionRange?: { startLineNumber: number; startColumn: number };
  children?: OutlineSymbolInput[];
}

export interface OutlineNode {
  id: string;
  name: string;
  detail: string;
  kind: number;
  kindName: string;
  line: number;
  column: number;
  /** Last line of the symbol's full range, used to find the symbol under the cursor. */
  endLine: number;
  children: OutlineNode[];
}

// Monaco/LSP SymbolKind order (Monaco is LSP's value minus one); names match codicon `symbol-*`.
const SYMBOL_KIND_NAMES = [
  "file",
  "module",
  "namespace",
  "package",
  "class",
  "method",
  "property",
  "field",
  "constructor",
  "enum",
  "interface",
  "function",
  "variable",
  "constant",
  "string",
  "number",
  "boolean",
  "array",
  "object",
  "key",
  "null",
  "enum-member",
  "struct",
  "event",
  "operator",
  "type-parameter",
] as const;

export function symbolKindName(kind: number) {
  return SYMBOL_KIND_NAMES[kind] ?? "misc";
}

const MAX_OUTLINE_DEPTH = 32;

function sortByPosition(a: OutlineNode, b: OutlineNode) {
  return a.line - b.line || a.column - b.column;
}

export function buildOutlineTree(symbols: readonly OutlineSymbolInput[] | null | undefined): OutlineNode[] {
  const build = (items: readonly OutlineSymbolInput[], parentId: string, depth: number): OutlineNode[] => {
    const seen = new Map<string, number>();
    const nodes = items
      .filter((symbol) => symbol && typeof symbol.name === "string" && symbol.range)
      .map((symbol) => {
        const anchor = symbol.selectionRange ?? symbol.range;
        const base = `${parentId}/${symbol.kind}:${symbol.name}`;
        const count = seen.get(base) ?? 0;
        seen.set(base, count + 1);
        const id = count ? `${base}#${count}` : base;
        return {
          id,
          name: symbol.name,
          detail: symbol.detail ?? "",
          kind: symbol.kind,
          kindName: symbolKindName(symbol.kind),
          line: Math.max(1, anchor.startLineNumber),
          column: Math.max(1, anchor.startColumn),
          endLine: Math.max(anchor.startLineNumber, symbol.range.endLineNumber ?? symbol.range.startLineNumber),
          children:
            depth < MAX_OUTLINE_DEPTH && symbol.children?.length ? build(symbol.children, id, depth + 1) : [],
        };
      });
    return nodes.sort(sortByPosition);
  };
  return symbols?.length ? build(symbols, "", 0) : [];
}

/** Keep nodes whose name matches (with their whole subtree) plus the ancestors of matches. */
export function filterOutlineTree(nodes: readonly OutlineNode[], query: string): OutlineNode[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return nodes.slice();
  const filter = (items: readonly OutlineNode[]): OutlineNode[] =>
    items.flatMap((node) => {
      if (node.name.toLowerCase().includes(needle)) return [node];
      const children = filter(node.children);
      return children.length ? [{ ...node, children }] : [];
    });
  return filter(nodes);
}

/** Id of the innermost node whose range contains the line. */
export function outlineNodeAtLine(nodes: readonly OutlineNode[], line: number): string | undefined {
  let found: string | undefined;
  let level = nodes;
  for (;;) {
    let next: OutlineNode | undefined;
    for (const node of level) {
      if (node.line > line) break;
      if (node.endLine >= line) next = node;
    }
    if (!next) return found;
    found = next.id;
    level = next.children;
  }
}

export function countOutlineNodes(nodes: readonly OutlineNode[]): number {
  return nodes.reduce((total, node) => total + 1 + countOutlineNodes(node.children), 0);
}
