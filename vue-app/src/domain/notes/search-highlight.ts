export interface HighlightSegment {
  text: string;
  match: boolean;
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * 把文本按搜索词（去首尾空白、不区分大小写、按字面匹配）切成片段，供模板逐段渲染为文本与 <mark>。
 * 片段始终作为文本节点输出，不经 v-html，正文中的 HTML 按字面显示。
 */
export function highlightSegments(text: string, query: string): HighlightSegment[] {
  const needle = query.trim();
  if (!needle || !text) return [{ text, match: false }];
  const segments: HighlightSegment[] = [];
  let last = 0;
  for (const found of text.matchAll(new RegExp(escapeRegExp(needle), "giu"))) {
    const start = found.index ?? 0;
    if (start > last) segments.push({ text: text.slice(last, start), match: false });
    segments.push({ text: found[0], match: true });
    last = start + found[0].length;
  }
  if (last < text.length) segments.push({ text: text.slice(last), match: false });
  return segments.length ? segments : [{ text, match: false }];
}
