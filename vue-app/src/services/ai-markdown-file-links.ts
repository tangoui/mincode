import { aiProsePathCandidates, looksLikeInlinePath, resolveAiLocalPath } from "@/domain/ai/local-paths";
import { classifyMarkdownLinkHref } from "@/domain/markdown-links";
import { invokeBackend } from "@/services/desktop-bridge";
import { inspectAiLocalPath, type AiLocalFile } from "@/services/ai-local-files";

const MAX_LOOKUPS_PER_REPLY = 128;

export async function linkAiMarkdownFiles(
  html: string,
  directory: string,
  menuLabel: string,
  signal: AbortSignal,
) {
  const template = document.createElement("template");
  template.innerHTML = html;
  const root = template.content;
  // These attributes are assigned only after a successful native lookup.
  for (const element of root.querySelectorAll("[data-ai-file-link], [data-ai-file-menu]")) {
    element.removeAttribute("data-ai-file-link");
    element.removeAttribute("data-ai-file-menu");
  }

  const links: AiLocalFile[] = [];
  const checked = new Map<string, AiLocalFile | undefined>();
  let home: string | undefined;

  async function lookup(value: string, fromHref = false) {
    signal.throwIfAborted();
    if (home === undefined && /^~(?:[\\/]|%2f|%5c)/i.test(value)) {
      home = await invokeBackend<string>("get_user_home_dir", {}, { signal }).catch(() => "");
    }
    const path = resolveAiLocalPath(value, directory, home ?? "", fromHref);
    if (!path) return;
    if (checked.has(path)) return checked.get(path);
    if (checked.size >= MAX_LOOKUPS_PER_REPLY) return;
    const file = await inspectAiLocalPath(path, signal).catch(() => undefined);
    checked.set(path, file);
    return file;
  }

  function decorate(anchor: HTMLAnchorElement, file: AiLocalFile) {
    const index = String(links.push(file) - 1);
    anchor.href = "#";
    anchor.title = file.path;
    anchor.setAttribute("data-ai-file-link", index);
    const button = document.createElement("button");
    button.type = "button";
    button.className = "ai-markdown-file-menu-button";
    button.setAttribute("data-ai-file-menu", index);
    button.setAttribute("aria-label", menuLabel);
    button.setAttribute("aria-haspopup", "menu");
    button.setAttribute("aria-expanded", "false");
    button.textContent = "⋯";
    anchor.after(button);
  }

  const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      if (node instanceof Element && node.matches("pre, button, svg")) return NodeFilter.FILTER_REJECT;
      if (node.parentElement?.closest("a, code")) return NodeFilter.FILTER_REJECT;
      return NodeFilter.FILTER_ACCEPT;
    },
  });
  // Capture traversal order before updating the DOM so inserted elements never
  // become candidates and links retain their order in the original reply.
  const nodes: Node[] = [];
  for (let node = walker.nextNode(); node; node = walker.nextNode()) nodes.push(node);
  for (const node of nodes) {
    if (node instanceof HTMLImageElement) {
      // A local file path cannot load inside the WebView. Local images are listed only in the
      // reply's file cards; a missing file keeps its alt text so the reference is not lost.
      const target = classifyMarkdownLinkHref(node.getAttribute("src"));
      if (target.kind !== "local") continue;
      const file = await lookup(target.path, true);
      if (!file) {
        node.replaceWith(node.getAttribute("alt")?.trim() ?? "");
        continue;
      }
      links.push(file);
      const parent = node.parentElement;
      node.remove();
      if (parent?.tagName === "P" && !parent.textContent?.trim() && !parent.querySelector("img, a, code")) parent.remove();
    } else if (node instanceof HTMLAnchorElement) {
      const target = classifyMarkdownLinkHref(node.getAttribute("href"));
      if (target.kind !== "local") continue;
      const file = await lookup(target.path, true);
      if (file) decorate(node, file);
      else node.replaceWith(...node.childNodes);
    } else if (node instanceof HTMLElement && node.tagName === "CODE") {
      const text = node.textContent?.trim() ?? "";
      if (!text || !looksLikeInlinePath(text) || checked.size >= MAX_LOOKUPS_PER_REPLY) continue;
      const file = await lookup(text);
      if (!file) continue;
      const anchor = document.createElement("a");
      node.replaceWith(anchor);
      anchor.append(node);
      decorate(anchor, file);
    } else if (node instanceof Text && checked.size < MAX_LOOKUPS_PER_REPLY) {
      const text = node.data;
      const replacement = document.createDocumentFragment();
      let offset = 0;
      for (const spans of aiProsePathCandidates(text)) {
        if (spans[0].start < offset) continue;
        if (checked.size >= MAX_LOOKUPS_PER_REPLY) break;
        for (const span of spans) {
          const file = await lookup(span.text);
          if (!file) continue;
          replacement.append(text.slice(offset, span.start));
          const anchor = document.createElement("a");
          anchor.textContent = span.text;
          replacement.append(anchor);
          decorate(anchor, file);
          offset = span.end;
          break;
        }
      }
      if (offset) {
        replacement.append(text.slice(offset));
        node.replaceWith(replacement);
      }
    }
  }
  signal.throwIfAborted();
  return { html: template.innerHTML, links };
}
