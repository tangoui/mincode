import type { AiMessageClipboardPayload } from "@/domain/ai/message-clipboard";
import { copyText, invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

type ClipboardImage = AiMessageClipboardPayload["images"][number];
const MAX_NATIVE_CLIPBOARD_IMAGE_BYTES = 8 * 1024 * 1024;

function base64ToBlob(data: string, mimeType: string) {
  const binary = atob(data);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  return new Blob([bytes], { type: mimeType });
}

function arrayBufferToBase64(buffer: ArrayBuffer) {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

async function imageAsPng(image: ClipboardImage) {
  const source = base64ToBlob(image.data, image.mimeType);
  if (image.mimeType === "image/png") return source;
  const bitmap = await createImageBitmap(source);
  try {
    const canvas = document.createElement("canvas");
    canvas.width = bitmap.width;
    canvas.height = bitmap.height;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("Canvas 2D context is unavailable");
    context.drawImage(bitmap, 0, 0);
    return await new Promise<Blob>((resolve, reject) => {
      canvas.toBlob((blob) => {
        if (blob) resolve(blob);
        else reject(new Error("Unable to encode clipboard image"));
      }, "image/png");
    });
  } finally {
    bitmap.close();
  }
}

async function nativeClipboardImage(image: ClipboardImage): Promise<ClipboardImage> {
  if (image.mimeType === "image/png" || typeof createImageBitmap !== "function") return image;
  try {
    const png = await imageAsPng(image);
    if (png.size > MAX_NATIVE_CLIPBOARD_IMAGE_BYTES) return image;
    return {
      ...image,
      mimeType: "image/png",
      data: arrayBufferToBase64(await png.arrayBuffer()),
    };
  } catch {
    // 原生端仍可解码常见图片格式；前端转换能力不可用时保留原图兜底。
    return image;
  }
}

async function copyRichContentInBrowser(payload: AiMessageClipboardPayload) {
  if (!navigator.clipboard?.write || typeof ClipboardItem === "undefined") {
    throw new Error("Rich Clipboard API is unavailable");
  }
  const clipboardData: Record<string, Blob> = {};
  if (payload.text) clipboardData["text/plain"] = new Blob([payload.text], { type: "text/plain" });
  if (payload.html) clipboardData["text/html"] = new Blob([payload.html], { type: "text/html" });
  const firstImage = payload.images[0];
  if (firstImage) clipboardData["image/png"] = await imageAsPng(firstImage);
  await navigator.clipboard.write([new ClipboardItem(clipboardData)]);
}

async function copyRichContentInNative(payload: AiMessageClipboardPayload) {
  const sourceImage = payload.images[0];
  if (!sourceImage) {
    await copyText(payload.text);
    return;
  }
  const image = await nativeClipboardImage(sourceImage);
  await invokeBackend<void>("clipboard_write_rich", {
    text: payload.text,
    html: payload.html,
    imageData: image.data,
    imageMimeType: image.mimeType,
  });
}

export async function copyImage(image: ClipboardImage) {
  if (isDesktopRuntime()) {
    await copyRichContentInNative({ text: "", html: "", images: [image] });
    return;
  }
  await copyRichContentInBrowser({ text: "", html: "", images: [image] });
}

export async function copyRichContent(payload: AiMessageClipboardPayload) {
  if (!payload.images.length) {
    await copyText(payload.text);
    return;
  }
  if (isDesktopRuntime()) {
    await copyRichContentInNative(payload);
    return;
  }
  await copyRichContentInBrowser(payload);
}
