// 前端调用桌面能力的入口：后端命令、事件、剪贴板、原生对话框。
// 底层是 tango 运行时（desktop-runtime.ts / desktop-events.ts）。
import { measureDevAsync } from "@/services/dev-performance";
import { invokeCommand, isDesktopRuntime, nativeCall } from "@/services/desktop-runtime";
import { emitEvent, onEvent } from "@/services/desktop-events";
import { getBrowserHandler, hasBrowserHandler } from "@/services/browser-runtime/registry";
import type { BackendCommand } from "@/generated/backend-commands";

export type { BackendCommand };

/** 判断当前页面是否由 MinCode 桌面宿主（tango WebView）承载。 */
export { isDesktopRuntime };

/** 浏览器本地模式：页面不在桌面宿主中运行，只有注册了浏览器处理器的命令可用。 */
export function isBrowserLocalMode() {
  return !isDesktopRuntime();
}

/**
 * 功能门禁：该命令当前能否真正执行。桌面端恒为 true；浏览器本地模式下仅当
 * 注册了浏览器处理器（见 services/browser-runtime）时为 true。
 */
export function canInvoke(command: BackendCommand) {
  return isDesktopRuntime() || hasBrowserHandler(command);
}

/**
 * Vue 前端唯一的原生调用入口。
 *
 * 所有桌面能力统一经 tango ipc 转发至 Go 后端（internal/host）。
 */
export interface InvokeBackendOptions {
  /** 中止时取消调用，后端处理器的 ctx 随之取消。 */
  signal?: AbortSignal;
}

export async function invokeBackend<T>(
  command: BackendCommand,
  args: Record<string, unknown> = {},
  options: InvokeBackendOptions = {}
): Promise<T> {
  const { signal } = options;
  signal?.throwIfAborted();
  // 浏览器本地模式：有处理器的命令由浏览器实现，结果原样返回（不经 JSON）；桌面端从不进入此分支。
  // 不套 measureDevAsync：它度量的是桌面 IPC 往返（`ipc:*`），浏览器处理器不经过 IPC，
  // 混入同名记录会污染桌面端的 IPC 耗时统计。
  if (!isDesktopRuntime()) {
    const handler = getBrowserHandler(command);
    if (handler) return (await handler(args, signal)) as T;
  }
  return measureDevAsync(`ipc:${command}`, () => invokeCommand<T>(command, args, signal));
}

/**
 * 提取原生错误的用户可读消息。
 *
 * 运行时拒绝的错误（TangoError）用 String(error) 会带上内部包装前缀；
 * 统一取 message 字段，其他错误回退字符串化。
 */
export function nativeErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  return String(error);
}

/**
 * 复制文本：桌面端走宿主原生剪贴板，浏览器预览再使用 Web Clipboard。
 *
 * 不经由通用后端命令调用 shell 的 pbcopy/clip：打包后的 WebView 可能没有相同的
 * shell/剪贴板会话，导致“复制 DDL”这类操作看似执行成功却没有写入系统剪贴板。
 */
export async function copyText(text: string) {
  if (isDesktopRuntime()) {
    await nativeCall("clipboard.setText", { text });
    return;
  }
  if (!navigator.clipboard?.writeText) throw new Error("Clipboard API is unavailable");
  await navigator.clipboard.writeText(text);
}

/**
 * 读取文本：桌面端走宿主原生剪贴板，浏览器预览仅使用 Web Clipboard。
 */
export async function readText() {
  if (isDesktopRuntime()) return (await nativeCall<string>("clipboard.text")) ?? "";
  if (!navigator.clipboard?.readText) throw new Error("Clipboard API is unavailable");
  return navigator.clipboard.readText();
}

/** 向所有桌面窗口（含本页）广播应用事件。 */
export async function emitAppEvent<T>(eventName: string, payload: T) {
  emitEvent(eventName, payload);
}

/** 订阅 Go 后端或其他窗口发送的应用事件，并兼容字符串化载荷。返回取消订阅函数。 */
export function listenAppEvent<T>(eventName: string, handler: (payload: T) => void) {
  return onEvent(eventName, (payload) => {
    let data: unknown = payload;
    if (typeof data === "string") {
      try {
        data = JSON.parse(data);
      } catch {
        // 非 JSON 事件保持原字符串。
      }
    }
    handler(data as T);
  });
}

function normalizeNativeDialogPath(value: string) {
  if (!value.startsWith("file://")) return value;
  try {
    const path = decodeURIComponent(new URL(value).pathname);
    return /^\/[A-Za-z]:\//.test(path) ? path.slice(1) : path;
  } catch {
    return value;
  }
}

function firstDialogPath(selected: string | string[] | null | undefined) {
  const path = Array.isArray(selected) ? (selected[0] ?? null) : selected || null;
  return path ? normalizeNativeDialogPath(path) : null;
}

/**
 * 部分平台把原生选择框的“取消”报告为错误（宿主正常情况下返回 null）。
 * 取消是正常的用户操作，不能把内部错误文本直接显示给用户；其他错误仍需
 * 继续抛出，避免掩盖真正的对话框故障。
 */
export function isNativeDialogCancelled(error: unknown) {
  const text = error instanceof Error
    ? `${error.name}: ${error.message}`
    : typeof error === "string" ? error : String(error ?? "");
  return /(?:cancelled|canceled)\s+by\s+user/i.test(text)
    || /user\s+(?:cancelled|canceled)/i.test(text);
}

/** 打开文件对话框的参数（字段同 internal/host OpenDialogOptions）。 */
interface OpenFileOptions {
  CanChooseDirectories?: boolean;
  CanChooseFiles?: boolean;
  CanCreateDirectories?: boolean;
  AllowsMultipleSelection?: boolean;
  Title?: string;
  Directory?: string;
  Filters?: Array<{ DisplayName: string; Pattern: string }>;
}

async function openFileDialog(options: OpenFileOptions) {
  try {
    const selected = await nativeCall<string[] | null>("dialog.open", options);
    if (!selected?.length) return null;
    return options.AllowsMultipleSelection ? selected : selected[0];
  } catch (error) {
    if (isNativeDialogCancelled(error)) return null;
    throw error;
  }
}

export interface ChooseDirectoryOptions {
  /**
   * 为本地项目选择文件夹：浏览器本地模式下改用 `showDirectoryPicker`，返回 `browser://<id>/<文件夹名>`
   * （见 services/browser-runtime/project-fs.ts）。其他调用方需要真实磁盘路径，不传此项。
   */
  browserProject?: boolean;
}

/** 通过原生对话框选择一个目录；用户取消时返回 null。 */
export async function chooseNativeDirectory(title: string, directory?: string, options: ChooseDirectoryOptions = {}) {
  if (options.browserProject && !isDesktopRuntime()) {
    const { openBrowserDirectory } = await import("@/services/browser-runtime/project-fs");
    return openBrowserDirectory();
  }
  const selected = await openFileDialog({
    CanChooseDirectories: true,
    CanChooseFiles: false,
    CanCreateDirectories: false,
    AllowsMultipleSelection: false,
    Title: title,
    Directory: directory?.trim() || undefined,
  });
  return firstDialogPath(selected);
}

/** 通过原生对话框选择单个文件；用户取消时返回 null。 */
export async function chooseNativeFile(
  title: string,
  filters: Array<{ DisplayName: string; Pattern: string }> = []
) {
  const selected = await openFileDialog({
    CanChooseDirectories: false,
    CanChooseFiles: true,
    CanCreateDirectories: false,
    AllowsMultipleSelection: false,
    Title: title,
    Filters: filters,
  });
  return firstDialogPath(selected);
}

/** 扩展名（如 `.epub`）转成原生对话框的文件过滤器；为空时不过滤。 */
function extensionFilters(title: string, extensions: string[]) {
  if (extensions.length === 0) return [];
  return [{ DisplayName: title, Pattern: extensions.map((extension) => `*${extension}`).join(";") }];
}

/** 通过原生对话框选择一个或多个文件；取消时返回空数组。 */
export async function chooseNativeFiles(title: string, extensions: string[] = []) {
  const selected = await openFileDialog({
    CanChooseDirectories: false,
    CanChooseFiles: true,
    CanCreateDirectories: false,
    AllowsMultipleSelection: true,
    Title: title,
    Filters: extensionFilters(title, extensions),
  });
  const paths = Array.isArray(selected) ? selected : selected ? [selected] : [];
  return paths.filter((path) => typeof path === "string" && path.length > 0).map(normalizeNativeDialogPath);
}

/** 通过原生对话框选择单个文件或目录。 */
export async function chooseNativeFileOrDirectory(title: string) {
  const selected = await openFileDialog({
    CanChooseDirectories: true,
    CanChooseFiles: true,
    CanCreateDirectories: false,
    AllowsMultipleSelection: false,
    Title: title,
  });
  return firstDialogPath(selected);
}

/** 通过原生对话框选择单个文件保存位置。 */
export async function chooseNativeSaveFile(
  title: string,
  filename: string,
  filters: Array<{ DisplayName: string; Pattern: string }> = []
) {
  try {
    const selected = await nativeCall<string | null>("dialog.save", {
      CanCreateDirectories: true,
      Title: title,
      Filename: filename,
      Filters: filters,
    });
    return firstDialogPath(selected);
  } catch (error) {
    if (isNativeDialogCancelled(error)) return null;
    throw error;
  }
}
