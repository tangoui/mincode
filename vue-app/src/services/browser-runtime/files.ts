/**
 * 浏览器本地模式下用户选择的文件。
 *
 * 浏览器拿不到磁盘路径，只能拿到 `File` 对象：选中的文件登记在本标签页的内存表里，
 * 以 `browser-file://<uuid>/<文件名>` 作为「路径」写进请求，发送时再由处理器取回。
 * 刷新页面后内存表清空，旧路径取不到文件，处理器据此提示重新选择。
 *
 * 视图只能通过动态 `import()` 加载本模块（`scripts/browser-runtime.test.mjs` 会检查），
 * 以免把它打进主包。
 */

export interface PickedBrowserFile {
  path: string;
  name: string;
  size: number;
}

export interface PickBrowserFilesOptions {
  multiple?: boolean;
  /** `<input accept>` 的各项，如 `".png"`、`"image/*"`。 */
  accept?: string[];
}

export const BROWSER_FILE_SCHEME = "browser-file://";

const pickedFiles = new Map<string, File>();

/** 登记一个文件并返回它的 `browser-file://` 路径。 */
export function registerBrowserFile(file: File): string {
  const path = `${BROWSER_FILE_SCHEME}${crypto.randomUUID()}/${file.name}`;
  pickedFiles.set(path, file);
  return path;
}

/** 取回本标签页登记过的文件；路径不是 `browser-file://` 或已失效（如刷新后）时返回 undefined。 */
export function browserFile(path: string): File | undefined {
  return pickedFiles.get(path);
}

/** 用隐藏的 `<input type=file>` 让用户选文件；取消时返回空数组。 */
export function pickBrowserFiles(options: PickBrowserFilesOptions = {}): Promise<PickedBrowserFile[]> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.multiple = Boolean(options.multiple);
    if (options.accept?.length) input.accept = options.accept.join(",");
    input.style.display = "none";
    let settled = false;
    const finish = (files: File[]) => {
      if (settled) return;
      settled = true;
      input.remove();
      resolve(files.map((file) => ({ path: registerBrowserFile(file), name: file.name, size: file.size })));
    };
    input.addEventListener("change", () => finish(Array.from(input.files ?? [])), { once: true });
    // Chrome / Edge 113+ 在用户关闭文件对话框时触发 cancel。
    input.addEventListener("cancel", () => finish([]), { once: true });
    document.body.append(input);
    input.click();
  });
}
