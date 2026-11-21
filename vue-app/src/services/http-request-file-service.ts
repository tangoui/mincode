import { chooseNativeFile, isDesktopRuntime } from "./desktop-bridge";

export interface SelectedHttpRequestFile {
  path: string;
  name: string;
}

export async function chooseHttpRequestFile(
  title: string
): Promise<SelectedHttpRequestFile | undefined> {
  if (!isDesktopRuntime()) return undefined;
  const path = await chooseNativeFile(title);
  if (!path) return undefined;
  return {
    path,
    name: path.split(/[\\/]/).pop() || path,
  };
}

/**
 * 选择 HTTP 请求要上传的文件（参数文件、form-data 文件、binary 请求体）。
 *
 * 桌面端走原生对话框，与 `chooseHttpRequestFile` 相同；浏览器本地模式用浏览器文件选择，
 * 返回 `browser-file://` 路径，只在当前标签页有效，由浏览器的 `http_send_request` 处理器读取。
 * 调用方应先用 `canInvoke("http_send_request")` 门禁。
 */
export async function chooseHttpRequestUploadFile(
  title: string
): Promise<SelectedHttpRequestFile | undefined> {
  if (isDesktopRuntime()) return chooseHttpRequestFile(title);
  // 动态导入：浏览器文件表不进主包，桌面端从不加载。
  const { pickBrowserFiles } = await import("@/services/browser-runtime/files");
  const [file] = await pickBrowserFiles({ multiple: false });
  return file ? { path: file.path, name: file.name } : undefined;
}
