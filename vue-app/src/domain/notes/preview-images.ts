/**
 * 笔记预览的图片与外部资源限制，在 renderMarkdownHtml（DOMPurify）之后、v-html 之前执行。
 *
 * 只保留同源笔记附件 `/__notes-assets/<sha256>.<ext>` 的 <img>；其他图片（http/https/data/相对路径等）
 * 不加载，替换为指向原地址的链接，链接文字为 alt（作为纯文本写入），否则为调用方传入的「图片」文案。
 * DOMPurify 默认配置仍允许若干会加载远程资源的写法（srcset、<video>/<audio>/<source>、内联样式 url()、
 * <style>、SVG 的 <image>/<feImage>/<use href>），这里一并去掉，保证预览不发起外部请求，也不让笔记样式影响宿主页面：
 * 整个 <svg> 删除（笔记没有正当的内联 SVG），href / xlink:href 只留在 <a> 上，src 只留在保留的附件 <img> 上。
 * 表单会让主 WebView 提交跳转（DOMPurify 默认保留 form 与 https action），因此拆掉 <form>（保留其内容）
 * 并一律去掉 action / formaction。
 * 同时兜底去掉脚本类元素、on* 事件属性和 javascript:/vbscript:/data: 链接：DOMPurify 在浏览器中已经处理，
 * 这里不依赖它在每个 DOM 实现里都生效（例如组件测试用的 happy-dom）。
 */

export const NOTE_ASSET_IMAGE_SRC = /^\/__notes-assets\/[0-9a-f]{64}\.(?:png|jpg|gif|webp)$/;

const MEDIA_SELECTOR = "img, video, audio";
const REMOVED_SELECTOR = "script, iframe, frame, frameset, object, embed, source, track, style, link, meta, base, svg";
const RESOURCE_ATTRIBUTES = ["srcset", "poster", "background", "style"];
const URL_ATTRIBUTES = ["href", "xlink:href"];
const FORM_ATTRIBUTES = ["action", "formaction", "formmethod", "formtarget"];
const KEPT_IMAGE_ATTRIBUTES = new Set(["src", "alt", "title", "width", "height"]);
// 与浏览器解析 URL 时一致：忽略空白与控制字符后再判断协议。
// eslint-disable-next-line no-control-regex
const IGNORED_URL_CHARACTERS = /[\u0000-\u0020\u007f]/g;
const UNSAFE_URL_SCHEME = /^(?:javascript|vbscript|data):/i;

function isUnsafeUrl(value: string) {
  return UNSAFE_URL_SCHEME.test(value.replace(IGNORED_URL_CHARACTERS, ""));
}

function replaceWithLink(doc: Document, element: Element, label: string) {
  const src = element.getAttribute("src")?.trim() ?? "";
  const text = element.getAttribute("alt")?.trim() || label || src;
  if (!src) {
    element.replaceWith(doc.createTextNode(text));
    return;
  }
  const anchor = doc.createElement("a");
  if (!isUnsafeUrl(src)) anchor.setAttribute("href", src);
  anchor.setAttribute("rel", "noreferrer");
  anchor.className = "note-preview__blocked-image";
  anchor.textContent = text;
  element.replaceWith(anchor);
}

/** @param imageLabel 没有 alt 时链接显示的文字（已本地化的「图片」）。 */
export function restrictPreviewImages(html: string, imageLabel = ""): string {
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, "text/html");
  const body = doc.body;
  for (const element of [...body.querySelectorAll(REMOVED_SELECTOR)]) element.remove();
  for (const form of [...body.querySelectorAll("form")]) form.replaceWith(...form.childNodes);
  const keptImages = new Set<Element>();
  for (const element of [...body.querySelectorAll(MEDIA_SELECTOR)]) {
    const src = element.getAttribute("src") ?? "";
    if (element.tagName === "IMG" && NOTE_ASSET_IMAGE_SRC.test(src)) {
      for (const name of element.getAttributeNames()) {
        if (!KEPT_IMAGE_ATTRIBUTES.has(name)) element.removeAttribute(name);
      }
      keptImages.add(element);
      continue;
    }
    replaceWithLink(doc, element, imageLabel);
  }
  for (const element of [...body.querySelectorAll("*")]) {
    for (const name of element.getAttributeNames()) {
      if (name.toLowerCase().startsWith("on")) element.removeAttribute(name);
    }
    const isLink = element.tagName === "A";
    for (const name of URL_ATTRIBUTES) {
      const value = element.getAttribute(name);
      if (value !== null && (!isLink || isUnsafeUrl(value))) element.removeAttribute(name);
    }
    for (const name of [...RESOURCE_ATTRIBUTES, ...FORM_ATTRIBUTES]) element.removeAttribute(name);
    // 例如 <input type="image"> 也会加载 src。
    if (!keptImages.has(element)) element.removeAttribute("src");
  }
  return body.innerHTML;
}
