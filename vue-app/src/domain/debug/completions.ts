/**
 * Applying a DAP completion item to the text of an expression input.
 */
import type { DapCompletionItem } from "@/domain/debug/types";

/** The identifier that ends at the end of `text` (letters, digits, `_`, `$`). */
const IDENTIFIER_BEFORE = /[\p{L}\p{N}_$]+$/u;

/**
 * The input text after choosing `item` with the cursor at `cursor` (UTF-16
 * code units; the end of the text by default), as VS Code applies DAP
 * completions: an item with a `length` replaces that many code units right
 * before the cursor, any other item the identifier before the cursor. The
 * text after the cursor stays. `start` is not used: its base depends on
 * columnsStartAt1 (MinCode sends true) and adapters disagree — java-debug
 * sends `start: 0` and no length with every item ("list.si" → size()).
 */
export function applyDebugCompletion(input: string, item: DapCompletionItem, cursor = input.length): string {
  const insert = item.text ?? item.label;
  const at = Math.min(Math.max(0, cursor), input.length);
  const before = input.slice(0, at);
  const replaced =
    typeof item.length === "number" ? Math.min(Math.max(0, item.length), at) : (IDENTIFIER_BEFORE.exec(before)?.[0].length ?? 0);
  return before.slice(0, at - replaced) + insert + input.slice(at);
}
