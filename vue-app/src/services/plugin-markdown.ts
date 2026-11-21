/**
 * Sanitized markdown for plugin-provided text (walkthrough steps). Stricter
 * than services/markdown-render.ts: raw HTML and images are rendered as escaped
 * text, headings shift to h3-h6, only a small tag set survives and links keep
 * nothing but an http(s) href. Links never navigate the WebView: the
 * rendering component blocks link clicks and opens http(s) links in the
 * system browser (PluginWalkthroughDialog.vue). Images of a walkthrough come only
 * from declared step assets (blob URLs rendered by the dialog, not by markdown).
 */
import DOMPurify from "dompurify";
import { Marked, type Tokens } from "marked";

const ALLOWED_TAGS = [
  "p",
  "br",
  "strong",
  "em",
  "del",
  "code",
  "pre",
  "ul",
  "ol",
  "li",
  "blockquote",
  "h3",
  "h4",
  "h5",
  "h6",
  "hr",
  "table",
  "thead",
  "tbody",
  "tr",
  "th",
  "td",
  "a",
];
export const PLUGIN_MARKDOWN_HTTP_URL = /^https?:\/\//i;
const MAX_MARKDOWN_CHARS = 32_768;

function escapeHtml(value: string) {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}

const pluginMarked = new Marked({
  async: false,
  gfm: true,
  breaks: true,
  renderer: {
    html(token: Tokens.HTML | Tokens.Tag) {
      return escapeHtml(token.text);
    },
    image(token: Tokens.Image) {
      return escapeHtml(token.text);
    },
    heading(token: Tokens.Heading) {
      const level = Math.min(6, token.depth + 2);
      return `<h${level}>${this.parser.parseInline(token.tokens)}</h${level}>\n`;
    },
  },
});

/** Renders untrusted plugin markdown to sanitized HTML (safe for v-html). */
export function renderPluginMarkdownHtml(markdown: string): string {
  const source = markdown.length > MAX_MARKDOWN_CHARS ? markdown.slice(0, MAX_MARKDOWN_CHARS) : markdown;
  const html = pluginMarked.parse(source) as string;
  return DOMPurify.sanitize(html, {
    ALLOWED_TAGS,
    ALLOWED_ATTR: ["href"],
    ALLOWED_URI_REGEXP: PLUGIN_MARKDOWN_HTTP_URL,
    ALLOW_DATA_ATTR: false,
  }) as unknown as string;
}

/** Decodes canonical UTF-8 markdown bytes from a contribution asset. */
export function decodePluginMarkdown(bytes: Uint8Array): string {
  return new TextDecoder("utf-8", { fatal: false }).decode(bytes);
}
