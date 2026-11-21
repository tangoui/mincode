/**
 * 笔记编辑器的 Markdown 语法高亮（CodeMirror StreamLanguage）。
 *
 * 只做高亮：标题、强调、行内与围栏代码、链接、列表、引用、分隔线；不改动正文。
 * 移植自 official-plugins/knowledge-base/ui-src/markdown-language.mjs。
 */
import type { StreamParser } from "@codemirror/language";

export interface MarkdownStreamState {
  /** 当前所在围栏代码块的开始标记（``` 或 ~~~），不在代码块中为 null。 */
  fence: string | null;
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export const markdownStream: StreamParser<MarkdownStreamState> = {
  name: "markdown",
  startState: () => ({ fence: null }),
  copyState: (state) => ({ fence: state.fence }),
  token(stream, state) {
    if (state.fence) {
      if (stream.sol() && stream.match(new RegExp(`^\\s*${escapeRegExp(state.fence)}`))) {
        state.fence = null;
        stream.skipToEnd();
        return "meta";
      }
      stream.skipToEnd();
      return "monospace";
    }
    if (stream.sol()) {
      const fence = stream.match(/^\s*(`{3,}|~{3,})/);
      if (fence && typeof fence === "object") {
        state.fence = fence[1];
        stream.skipToEnd();
        return "meta";
      }
      if (stream.match(/^#{1,6}\s.*$/)) return "heading";
      if (stream.match(/^\s*(?:[-*_]\s*){3,}$/)) return "contentSeparator";
      if (stream.match(/^\s*>\s?/)) return "quote";
      if (stream.match(/^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?/)) return "list";
    }
    if (stream.match(/^`[^`]+`/)) return "monospace";
    if (stream.match(/^(\*\*|__)(?=\S)(?:.*?\S)\1/)) return "strong";
    if (stream.match(/^(\*|_)(?=\S)(?:.*?\S)\1/)) return "emphasis";
    if (stream.match(/^!?\[[^\]]*\]\([^)]*\)/)) return "link";
    if (stream.match(/^<https?:\/\/[^>]+>/)) return "link";
    stream.next();
    while (!stream.eol() && !/[`*_[!<]/.test(stream.peek() ?? "")) stream.next();
    return null;
  },
};
