/**
 * Helpers over a test provider's discovered tree (domain/plugins/test-providers.ts):
 * the subtrees a run of some ids covers, and the tests of one file with their
 * lines (the editor's gutter run buttons). Pure: no Vue or IPC.
 */
import type { TestItem } from "@/domain/plugins/test-providers";

/** A discovered test (or suite) shown on a line of a file. */
export interface TestFileEntry {
  id: string;
  label: string;
  line: number;
}

const DEPTH_MAX = 256;

/** Project-relative path → its tests with lines (in tree order), from a discovered tree. */
export function testsByFile(items: readonly TestItem[]): Map<string, TestFileEntry[]> {
  const out = new Map<string, TestFileEntry[]>();
  const visit = (level: readonly TestItem[], depth: number) => {
    if (depth > DEPTH_MAX) return;
    for (const item of level) {
      if (item.path && item.line) {
        const list = out.get(item.path) ?? [];
        if (!out.has(item.path)) out.set(item.path, list);
        list.push({ id: item.id, label: item.label, line: item.line });
      }
      if (item.children?.length) visit(item.children, depth + 1);
    }
  };
  visit(items, 0);
  return out;
}

/**
 * The discovered subtrees of ids, in tree order (a node inside an already
 * picked subtree is not repeated); every item when ids is empty.
 */
export function pickTestItems(items: readonly TestItem[], ids: readonly string[]): TestItem[] {
  if (!ids.length) return [...items];
  const wanted = new Set(ids);
  const out: TestItem[] = [];
  const visit = (level: readonly TestItem[], depth: number) => {
    if (depth > DEPTH_MAX) return;
    for (const item of level) {
      if (wanted.has(item.id)) out.push(item);
      else if (item.children?.length) visit(item.children, depth + 1);
    }
  };
  visit(items, 0);
  return out;
}

/** The discovered item with id, or undefined. */
export function findTestItem(items: readonly TestItem[], id: string): TestItem | undefined {
  return pickTestItems(items, [id])[0];
}
