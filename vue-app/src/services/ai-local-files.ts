import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { currentKeyboardPlatform } from "@/services/keyboard-platform";

interface LocalPathInfo {
  exists: boolean;
  isFile: boolean;
  isDir: boolean;
  size: number;
  modifiedAt: string | null;
  mime: string;
  path?: string;
}

export interface AiLocalFile {
  path: string;
  canonicalPath: string;
  isFile: boolean;
  size: number;
  modifiedAt: string | null;
  mime: string;
}

export function aiFileRevealLabel() {
  const platform = currentKeyboardPlatform();
  if (platform === "mac") return "ai.files.revealFinder";
  if (platform === "win") return "ai.files.revealExplorer";
  return "ai.files.reveal";
}

export async function inspectAiLocalPath(path: string, signal: AbortSignal): Promise<AiLocalFile | undefined> {
  if (!isDesktopRuntime()) return;
  const info = await invokeBackend<LocalPathInfo>("local_path_info", { path }, { signal });
  if (info.exists && (info.isFile || info.isDir)) {
    return {
      path,
      canonicalPath: info.path ?? path,
      isFile: info.isFile,
      size: info.size,
      modifiedAt: info.modifiedAt,
      mime: info.mime,
    };
  }
}

export async function readAiImageThumbnail(file: AiLocalFile, signal: AbortSignal): Promise<string | undefined> {
  const maximum = 20 * 1024 * 1024;
  if (!isDesktopRuntime() || !file.isFile || !file.mime.startsWith("image/") || file.size > maximum) return;
  const data = await invokeBackend<string>("local_image_thumbnail", { path: file.path }, { signal });
  if (data.startsWith("data:image/png;base64,")) return data;
}

export async function openAiLocalPath(file: AiLocalFile, reveal = false) {
  if (!isDesktopRuntime()) return;
  await invokeBackend("local_path_open", { path: file.path, reveal });
}
