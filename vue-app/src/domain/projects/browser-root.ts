/**
 * 浏览器本地模式的项目根：`browser://<id>/<文件夹名>`。
 *
 * 浏览器拿不到磁盘路径，打开的文件夹以 IndexedDB 中句柄的 id 标识（实现见
 * services/browser-runtime/project-fs.ts）。这类根下的文档路径同样以 `browser://` 开头，
 * 路径工具应把它们当作绝对路径。
 */
export const BROWSER_PROJECT_SCHEME = "browser://";

export function isBrowserProjectPath(path: string) {
  return path.startsWith(BROWSER_PROJECT_SCHEME);
}

/** 浏览器能否打开本地文件夹（File System Access API：Chrome / Edge；Safari、Firefox 不支持）。 */
export function supportsBrowserDirectoryPicker() {
  return typeof window !== "undefined"
    && typeof (window as { showDirectoryPicker?: unknown }).showDirectoryPicker === "function";
}

/** 浏览器项目根的访问状态：已授权、需用户点击重新授权、句柄已丢失（需重新选择文件夹）。 */
export type BrowserProjectAccess = "granted" | "prompt" | "missing";
