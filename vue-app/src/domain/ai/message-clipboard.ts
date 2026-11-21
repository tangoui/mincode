import type { AiAttachment } from "@/domain/ai/attachments";

export interface AiMessageClipboardImage {
  name: string;
  mimeType: string;
  data: string;
}

export interface AiMessageClipboardPayload {
  text: string;
  html: string;
  images: AiMessageClipboardImage[];
}

function escapeHtml(value: string) {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function normalizedImageData(attachment: AiAttachment) {
  const value = attachment.data.trim();
  if (!value.startsWith("data:")) return value.replace(/\s+/g, "");
  const separator = value.indexOf(",");
  return separator >= 0 ? value.slice(separator + 1).replace(/\s+/g, "") : "";
}

function normalizedImageMimeType(value: string) {
  const mimeType = value.trim().toLowerCase();
  return /^image\/[a-z0-9.+-]+$/.test(mimeType) ? mimeType : "image/png";
}

export function createAiMessageClipboardPayload(
  content: string,
  attachments: AiAttachment[] = [],
): AiMessageClipboardPayload {
  const messageText = content.trim();
  const attachmentNames = attachments.map((attachment) => attachment.name.trim()).filter(Boolean);
  const text = messageText || attachmentNames.join("\n");
  const images = attachments
    .filter((attachment) => attachment.kind === "image" && Boolean(attachment.data.trim()))
    .map((attachment) => ({
      name: attachment.name,
      mimeType: normalizedImageMimeType(attachment.mimeType),
      data: normalizedImageData(attachment),
    }))
    .filter((image) => Boolean(image.data));

  const contentHtml = messageText
    ? `<div style="white-space:pre-wrap">${escapeHtml(messageText).replace(/\n/g, "<br>")}</div>`
    : "";
  const imagesHtml = images
    .map((image) => `<div><img src="data:${image.mimeType};base64,${image.data}" alt="${escapeHtml(image.name)}"></div>`)
    .join("");

  return {
    text,
    html: images.length ? `<div>${contentHtml}${imagesHtml}</div>` : "",
    images,
  };
}
