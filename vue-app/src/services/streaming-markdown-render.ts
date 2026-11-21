import DOMPurify from "dompurify";
import { marked, type Token, type TokensList } from "marked";

type ReferenceLinks = TokensList["links"];

// Same options as renderMarkdownHtml so a streamed message renders exactly like the finished one.
const MARKED_OPTIONS = { async: false, gfm: true, breaks: true } as const;

interface CachedBlock {
  key: string;
  html: string;
}

export interface StreamingMarkdownRenderer {
  /** Sanitized HTML for the whole content; blocks that stopped changing are reused. */
  render(content: string): string;
}

function linksSignature(links: ReferenceLinks) {
  return JSON.stringify(Object.entries(links).sort(([left], [right]) => left.localeCompare(right)));
}

function renderBlock(token: Token, links: ReferenceLinks) {
  const html = marked.parser(Object.assign([token], { links }), MARKED_OPTIONS);
  return html ? DOMPurify.sanitize(html) : "";
}

/**
 * Renders markdown that grows while a reply streams. The text is lexed as a whole each time, so
 * block boundaries always follow marked's own rules (lists, tables, fences, HTML blocks); every
 * block but the last is complete and its HTML is kept. A block is re-rendered when its source or
 * the reference-link definitions change, because a definition may arrive after its first use.
 * The cache belongs to one message and never enters the shared markdown LRU.
 */
export function createStreamingMarkdownRenderer(): StreamingMarkdownRenderer {
  let blocks: CachedBlock[] = [];
  return {
    render(content) {
      const tokens = marked.lexer(content, MARKED_OPTIONS);
      const signature = linksSignature(tokens.links);
      const next: CachedBlock[] = [];
      tokens.forEach((token, index) => {
        const key = `${signature}\u0000${token.raw}`;
        const isLast = index === tokens.length - 1;
        const cached = blocks[index];
        const html = !isLast && cached?.key === key ? cached.html : renderBlock(token, tokens.links);
        next.push({ key, html });
      });
      blocks = next;
      return next.map((block) => block.html).join("");
    },
  };
}
