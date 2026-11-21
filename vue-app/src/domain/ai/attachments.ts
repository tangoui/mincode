export const MAX_AI_IMAGE_ATTACHMENT_BYTES = 5 * 1024 * 1024;
export const MAX_AI_TEXT_ATTACHMENT_BYTES = 256 * 1024;

export const AI_ATTACHMENT_ACCEPT =
  "image/png,image/jpeg,image/gif,image/webp,.txt,.md,.json,.csv,.tsv,.sql,.log,.xml,.yaml,.yml,.js,.jsx,.ts,.tsx,.css,.scss,.html,.vue,.go,.rs,.py,.php,.java,.sh";

export interface AiAttachment {
  id: string;
  name: string;
  mimeType: string;
  size: number;
  kind: "image" | "text";
  data: string;
  previewUrl?: string;
  /** Set for text inserted from a plugin context provider (contributes.aiContextProviders). */
  origin?: AiAttachmentOrigin;
}

/** Where an attachment came from when it was not a user file. */
export interface AiAttachmentOrigin {
  kind: "plugin-context";
  pluginId: string;
  providerId: string;
}

const ORIGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;

function normalizeAttachmentOrigin(origin: unknown): AiAttachmentOrigin | undefined {
  if (typeof origin !== "object" || origin === null) return undefined;
  const { kind, pluginId, providerId } = origin as Record<string, unknown>;
  if (kind !== "plugin-context" || typeof pluginId !== "string" || typeof providerId !== "string") return undefined;
  if (!ORIGIN_ID_PATTERN.test(pluginId) || !/^[a-z][a-z0-9-]{0,31}$/.test(providerId)) return undefined;
  return { kind, pluginId, providerId };
}

export interface AcpPromptContent {
  type: "image" | "resource";
  name: string;
  mimeType: string;
  data: string;
}

const textExtensions = new Set([
  "txt", "md", "json", "csv", "tsv", "sql", "log", "xml", "yaml", "yml", "js", "jsx",
  "ts", "tsx", "css", "scss", "html", "vue", "go", "rs", "py", "php", "java", "sh",
]);

function extensionOf(name: string) {
  const separator = name.lastIndexOf(".");
  return separator >= 0 ? name.slice(separator + 1).toLowerCase() : "";
}

function isTextFile(file: File) {
  return file.type.startsWith("text/") || file.type === "application/json" ||
    file.type === "application/xml" || file.type === "application/javascript" ||
    textExtensions.has(extensionOf(file.name));
}

function createId() {
  return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function arrayBufferToBase64(buffer: ArrayBuffer) {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

function detectSupportedImageMime(binary: string) {
  const byte = (index: number) => binary.charCodeAt(index);
  if (binary.length >= 8 &&
    byte(0) === 0x89 && byte(1) === 0x50 && byte(2) === 0x4e && byte(3) === 0x47 &&
    byte(4) === 0x0d && byte(5) === 0x0a && byte(6) === 0x1a && byte(7) === 0x0a) {
    return "image/png";
  }
  if (binary.length >= 3 && byte(0) === 0xff && byte(1) === 0xd8 && byte(2) === 0xff) {
    return "image/jpeg";
  }
  if (binary.startsWith("GIF87a") || binary.startsWith("GIF89a")) return "image/gif";
  if (binary.length >= 12 && binary.startsWith("RIFF") && binary.slice(8, 12) === "WEBP") {
    return "image/webp";
  }
  return "";
}

function normalizeBase64Image(value: string) {
  const compact = value.replace(/\s+/g, "");
  if (!compact || !/^[A-Za-z0-9+/]*={0,2}$/.test(compact) || compact.length % 4 === 1) return undefined;
  const data = compact.padEnd(compact.length + ((4 - (compact.length % 4)) % 4), "=");
  try {
    const mimeType = detectSupportedImageMime(atob(data));
    return mimeType ? { data, mimeType } : undefined;
  } catch {
    return undefined;
  }
}

export function compactAiAttachment(attachment: AiAttachment): AiAttachment {
  const origin = attachment.kind === "text" ? normalizeAttachmentOrigin(attachment.origin) : undefined;
  return {
    id: attachment.id,
    name: attachment.name,
    mimeType: attachment.mimeType,
    size: attachment.size,
    kind: attachment.kind,
    data: attachment.data,
    ...(origin ? { origin } : {}),
  };
}

export function normalizeAiAttachment(attachment: AiAttachment): AiAttachment | undefined {
  if (!attachment || (attachment.kind !== "image" && attachment.kind !== "text")) return undefined;
  if (attachment.kind === "text") return compactAiAttachment(attachment);

  let mimeType = typeof attachment.mimeType === "string" ? attachment.mimeType.trim().toLowerCase() : "";
  let data = typeof attachment.data === "string" ? attachment.data.trim() : "";
  if (data.toLowerCase().startsWith("data:")) {
    const match = data.match(/^data:([^;,]+);base64,([\s\S]*)$/i);
    if (!match) return undefined;
    mimeType = (match[1] ?? "").trim().toLowerCase();
    data = match[2] ?? "";
  }

  const normalized = normalizeBase64Image(data);
  if (!mimeType.startsWith("image/") || !normalized) return undefined;
  return compactAiAttachment({
    ...attachment,
    mimeType: normalized.mimeType,
    data: normalized.data,
  });
}

export function sanitizeAiAttachments(attachments?: AiAttachment[]) {
  if (!attachments?.length) return [];
  return attachments
    .map(normalizeAiAttachment)
    .filter((attachment): attachment is AiAttachment => Boolean(attachment));
}

export async function createAiAttachment(file: File): Promise<AiAttachment> {
  if (file.type.startsWith("image/")) {
    if (file.size === 0) throw new Error("EMPTY_IMAGE_ATTACHMENT");
    if (file.size > MAX_AI_IMAGE_ATTACHMENT_BYTES) throw new Error("IMAGE_ATTACHMENT_TOO_LARGE");
    const normalized = normalizeBase64Image(arrayBufferToBase64(await file.arrayBuffer()));
    if (!normalized) throw new Error("EMPTY_IMAGE_ATTACHMENT");
    return {
      id: createId(), name: file.name, mimeType: normalized.mimeType, size: file.size, kind: "image",
      data: normalized.data,
    };
  }
  if (!isTextFile(file)) throw new Error("UNSUPPORTED_ATTACHMENT");
  if (file.size > MAX_AI_TEXT_ATTACHMENT_BYTES) throw new Error("TEXT_ATTACHMENT_TOO_LARGE");
  return {
    id: createId(), name: file.name, mimeType: file.type || "text/plain", size: file.size,
    kind: "text", data: await file.text(),
  };
}

export function getAttachmentImageSrc(attachment: AiAttachment): string {
  if (attachment.kind !== "image" || !attachment.data) return "";
  if (attachment.previewUrl) return attachment.previewUrl;
  if (attachment.data.startsWith("data:")) return attachment.data;
  return `data:${attachment.mimeType || "image/png"};base64,${attachment.data}`;
}

export async function readAiAttachments(files: FileList | File[]) {
  const attachments: AiAttachment[] = [];
  const errors: Array<{ name: string; code: string }> = [];
  for (const file of Array.from(files)) {
    try {
      attachments.push(await createAiAttachment(file));
    } catch (error) {
      errors.push({ name: file.name, code: error instanceof Error ? error.message : String(error) });
    }
  }
  return { attachments, errors };
}

function hasPlainTextClipboardData(data: DataTransfer) {
  if (data.getData("text/plain") !== "") return true;

  // 部分 WebView 会在 paste 事件中声明 text/plain，但 getData 暂时返回空字符串。
  if (Array.from(data.types ?? []).some((type) => type.toLowerCase() === "text/plain")) return true;
  return Array.from(data.items ?? []).some((item) =>
    item.kind === "string" && item.type.toLowerCase() === "text/plain"
  );
}

export async function readAiAttachmentsFromClipboard(data: DataTransfer) {
  // 复制富文本时，剪贴板可能同时携带浏览器生成的文件项；有纯文本时应优先正常粘贴文字。
  if (hasPlainTextClipboardData(data)) return { attachments: [], errors: [] };

  const itemFiles = Array.from(data.items ?? [])
    .filter((item) => item.kind === "file")
    .map((item) => item.getAsFile())
    .filter((file): file is File => Boolean(file));
  const clipboardFiles = Array.from(data.files ?? []);
  const files = [...itemFiles];
  for (const file of clipboardFiles) {
    if (!files.some((item) => item === file)) files.push(file);
  }
  return readAiAttachments(files);
}

export function toAcpPromptContent(attachment: AiAttachment): AcpPromptContent {
  return {
    type: attachment.kind === "image" ? "image" : "resource",
    name: attachment.name,
    mimeType: attachment.mimeType,
    data: attachment.data,
  };
}
