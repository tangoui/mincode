/**
 * Classification of links rendered inside markdown previews (AI messages,
 * sessions, docs...). The main desktop webview navigates in place when an
 * anchor is followed, so every navigable link is intercepted first.
 */
export type MarkdownLinkClassification =
  | { kind: "http"; url: string }
  | { kind: "local"; path: string }
  | { kind: "anchor" }
  | { kind: "blocked" };

const WINDOWS_ABSOLUTE_PATH = /^[A-Za-z]:[\\/]/;
const URI_SCHEME = /^[A-Za-z][A-Za-z\d+.-]*:/;

export function classifyMarkdownLinkHref(
  href: string | null | undefined
): MarkdownLinkClassification {
  const trimmed = (href ?? "").trim();
  if (!trimmed) return { kind: "blocked" };
  if (trimmed.startsWith("#")) return { kind: "anchor" };

  const scheme = trimmed.slice(0, 8).toLowerCase();
  if (scheme.startsWith("http:") || scheme.startsWith("https:")) {
    try {
      const parsed = new URL(trimmed);
      if (!parsed.hostname) return { kind: "blocked" };
      return { kind: "http", url: trimmed };
    } catch {
      return { kind: "blocked" };
    }
  }

  if (scheme.startsWith("file:") || WINDOWS_ABSOLUTE_PATH.test(trimmed)) {
    return { kind: "local", path: trimmed };
  }
  if (URI_SCHEME.test(trimmed)) return { kind: "blocked" };

  return { kind: "local", path: trimmed };
}
