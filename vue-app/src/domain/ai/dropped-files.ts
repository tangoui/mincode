import type { BackendCommand } from "@/generated/backend-commands";
import {
  MAX_AI_IMAGE_ATTACHMENT_BYTES,
  MAX_AI_TEXT_ATTACHMENT_BYTES,
  readAiAttachments,
  type AiAttachment,
} from "@/domain/ai/attachments";

export const AI_ASSISTANT_DROP_TARGET_ID = "ai-assistant-drop-target";

interface ExternalFileInfo {
  name: string;
  kind: string;
  mimeType?: string;
  size: number;
}

export type ExternalFileInvoke = <T>(command: BackendCommand, args: Record<string, unknown>) => Promise<T>;

function baseName(path: string) {
  return path.split(/[\\/]/).filter(Boolean).pop() ?? path;
}

function dataUrlBytes(dataUrl: string) {
  const binary = atob(dataUrl.slice(dataUrl.indexOf(",") + 1));
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

async function readDroppedFile(path: string, invoke: ExternalFileInvoke): Promise<File> {
  const info = await invoke<ExternalFileInfo>("local_fs_inspect_external_file", { path });
  if (info.kind === "image") {
    if (info.size > MAX_AI_IMAGE_ATTACHMENT_BYTES) throw new Error("IMAGE_ATTACHMENT_TOO_LARGE");
    const dataUrl = await invoke<string>("local_fs_read_external_data_url", { path });
    return new File([dataUrlBytes(dataUrl)], info.name, { type: info.mimeType || "image/png" });
  }
  if (info.kind === "text") {
    if (info.size > MAX_AI_TEXT_ATTACHMENT_BYTES) throw new Error("TEXT_ATTACHMENT_TOO_LARGE");
    const text = await invoke<string>("local_fs_read_external_text_file", { path });
    const mimeType = info.mimeType?.startsWith("text/") ? info.mimeType : "text/plain";
    return new File([text], info.name, { type: mimeType });
  }
  throw new Error("UNSUPPORTED_ATTACHMENT");
}

/**
 * Reads files dropped from the desktop (native paths) into chat attachments,
 * applying the same type and size rules as files picked in the composer.
 */
export async function readAiAttachmentsFromPaths(paths: readonly string[], invoke: ExternalFileInvoke) {
  const attachments: AiAttachment[] = [];
  const errors: Array<{ name: string; code: string }> = [];
  for (const path of new Set(paths.map((item) => item.trim()).filter(Boolean))) {
    try {
      const result = await readAiAttachments([await readDroppedFile(path, invoke)]);
      attachments.push(...result.attachments);
      errors.push(...result.errors);
    } catch (error) {
      const code = error instanceof Error ? error.message : String(error);
      errors.push({
        name: baseName(path),
        code: /^(?:not_a_file|stat:)/.test(code) ? "UNSUPPORTED_ATTACHMENT" : code,
      });
    }
  }
  return { attachments, errors };
}
