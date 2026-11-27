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
/**
 * Turns a `file:` URL into a plain local path. DOMPurify drops `file:` URLs from
 * `src`, so local images in AI replies must reach the file linker as paths.
 */
export function fileUrlToLocalPath(href: string): string | undefined {
  if (!/^file:/i.test(href)) return undefined;
  try {
    const url = new URL(href);
    if (url.host && url.host !== "localhost") return undefined;
    const path = decodeURIComponent(url.pathname);
    return /^\/[A-Za-z]:\//.test(path) ? path.slice(1) : path;
  } catch {
    return undefined;
  }
}

export function renderMarkdownHtml(content: string, options: { memoize?: boolean; aiFileLinks?: boolean } = {}): string {
  const windowsLinks = options.aiFileLinks && /\b[A-Za-z]:[\\/]/.test(content);
  const fileImages = options.aiFileLinks && /!\[[^\]]*\]\(\s*<?file:/i.test(content);
  const rewrite = windowsLinks || fileImages;
  const cached = rewrite ? undefined : cachedMarkdownHtml(content);
  if (cached !== undefined) return cached;
  const html = DOMPurify.sanitize(
    marked.parse(content, {
      async: false,
      gfm: true,
      breaks: true,
      ...(rewrite && {
        walkTokens(token) {
          if (windowsLinks && token.type === "link" && /^[A-Za-z]:[\\/]/.test(token.href)) {
            token.href = "file:///" + token.href.replace(/\\/g, "/").replace(/#/g, "%23").replace(/\?/g, "%3F");
          }
          if (fileImages && token.type === "image") {
            const path = fileUrlToLocalPath(token.href);
            if (path) token.href = path;
          }
        },
      }),
    }),
  );
  if (!rewrite && options.memoize !== false) rememberMarkdownHtml(content, html);
  return html;
}

export function clearMarkdownHtmlCache() {
  markdownHtmlCache.clear();
}
