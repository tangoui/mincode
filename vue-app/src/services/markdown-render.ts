import DOMPurify from "dompurify";
import { marked } from "marked";

const MARKDOWN_HTML_CACHE_LIMIT = 128;
// Very large documents are rendered but not memoized so the cache stays small.
const MARKDOWN_HTML_CACHE_MAX_CONTENT_LENGTH = 200_000;

const markdownHtmlCache = new Map<string, string>();

function rememberMarkdownHtml(content: string, html: string) {
  if (content.length > MARKDOWN_HTML_CACHE_MAX_CONTENT_LENGTH) return;
  markdownHtmlCache.delete(content);
  markdownHtmlCache.set(content, html);
  while (markdownHtmlCache.size > MARKDOWN_HTML_CACHE_LIMIT) {
    const oldest = markdownHtmlCache.keys().next().value;
    if (oldest === undefined) break;
    markdownHtmlCache.delete(oldest);
  }
}

/** Returns memoized sanitized HTML and refreshes its LRU position. */
export function cachedMarkdownHtml(content: string): string | undefined {
  const html = markdownHtmlCache.get(content);
  if (html === undefined) return undefined;
  markdownHtmlCache.delete(content);
  markdownHtmlCache.set(content, html);
  return html;
}

/**
 * Parses GFM markdown and sanitizes the result. `memoize: false` is meant for
 * transient content (e.g. a stream still growing) that would only evict
 * reusable entries from the LRU.
 */
export function renderMarkdownHtml(content: string, options: { memoize?: boolean } = {}): string {
  const cached = cachedMarkdownHtml(content);
  if (cached !== undefined) return cached;
  const html = DOMPurify.sanitize(
    marked.parse(content, {
      async: false,
      gfm: true,
      breaks: true,
    }),
  );
  if (options.memoize !== false) rememberMarkdownHtml(content, html);
  return html;
}

export function clearMarkdownHtmlCache() {
  markdownHtmlCache.clear();
}
