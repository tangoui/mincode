import { chooseNativeSaveFile, invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface SaveTextFileOptions {
  /** 原生保存对话框标题 */
  title: string;
  /** 建议的文件名 */
  filename: string;
  content: string;
  /** 原生保存对话框文件类型过滤器 */
  filters?: Array<{ DisplayName: string; Pattern: string }>;
  /** 浏览器预览回退下载时使用的 MIME 类型 */
  browserMimeType?: string;
}

/**
 * 将文本内容保存为文件。
 * 桌面运行时：WKWebView 不支持 `<a download>` 触发下载，
 * 因此走 原生保存对话框 + 后端写盘。
 * 浏览器预览：回退为锚点下载（预览环境无原生能力）。
 * 返回 false 表示用户在对话框中取消。
 */
export async function saveTextFileToDisk(options: SaveTextFileOptions): Promise<boolean> {
  const {
    title,
    filename,
    content,
    filters = [],
    browserMimeType = "text/plain",
  } = options;

  if (isDesktopRuntime()) {
    const path = await chooseNativeSaveFile(title, filename, filters);
    if (!path) return false;
    await invokeBackend<void>("local_fs_write_selected_text_file", { path, content });
    return true;
  }

  const url = URL.createObjectURL(new Blob([content], { type: browserMimeType }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
  return true;
}

export interface SaveBinaryFileOptions {
  /** 原生保存对话框标题 */
  title: string;
  /** 建议的文件名 */
  filename: string;
  /** base64 编码的二进制内容 */
  contentBase64: string;
  /** 原生保存对话框文件类型过滤器 */
  filters?: Array<{ DisplayName: string; Pattern: string }>;
  /** 浏览器预览回退下载时使用的 MIME 类型 */
  browserMimeType?: string;
}

/**
 * 将 base64 编码的二进制内容保存为文件（如表数据导出的 .xlsx）。
 * 桌面运行时走 原生保存对话框 + 后端二进制写盘；
 * 浏览器预览回退为锚点下载。返回 false 表示用户取消。
 */
export async function saveBinaryFileToDisk(options: SaveBinaryFileOptions): Promise<boolean> {
  const {
    title,
    filename,
    contentBase64,
    filters = [],
    browserMimeType = "application/octet-stream",
  } = options;

  if (isDesktopRuntime()) {
    const path = await chooseNativeSaveFile(title, filename, filters);
    if (!path) return false;
    await invokeBackend<void>("local_fs_write_selected_binary_file", { path, content: contentBase64 });
    return true;
  }

  const bytes = Uint8Array.from(atob(contentBase64), (char) => char.charCodeAt(0));
  const url = URL.createObjectURL(new Blob([bytes], { type: browserMimeType }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
  return true;
}
