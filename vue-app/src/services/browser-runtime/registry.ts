import type { BackendCommand } from "@/generated/backend-commands";
import type { MessageKey } from "@/i18n/locales/zh";

/**
 * 浏览器本地模式（无桌面运行时）下替代某条后端命令的实现。
 *
 * 参数与返回形状必须与对应 Go 命令一致（以 backend/ 的实现为准）；
 * 返回值由 invokeBackend 原样交给调用方，不经 JSON 序列化。失败必须抛错，不得伪装成功。
 */
export type BrowserHandler = (args: Record<string, unknown>, signal?: AbortSignal) => Promise<unknown>;

const handlers = new Map<BackendCommand, BrowserHandler>();

/**
 * 注册浏览器处理器。只在浏览器本地模式下由 `services/browser-runtime/index.ts`
 * 的功能模块调用；桌面端从不加载这些模块，也从不调用处理器。重复注册以最后一次为准。
 */
export function registerBrowserHandler(command: BackendCommand, handler: BrowserHandler): void {
  handlers.set(command, handler);
}

export function hasBrowserHandler(command: BackendCommand): boolean {
  return handlers.has(command);
}

/** 供 invokeBackend 查找处理器；功能代码应使用 `canInvoke`/`invokeBackend`，不要直接调用处理器。 */
export function getBrowserHandler(command: BackendCommand): BrowserHandler | undefined {
  return handlers.get(command);
}

/** 浏览器本地模式下真正可用的一项功能，及其在浏览器里的限制说明。 */
export interface BrowserLocalFeature {
  id: string;
  /** 功能名称的 i18n 键（`browserLocal.feature.*`）。 */
  titleKey: MessageKey;
  /** 该功能在浏览器里的限制说明的 i18n 键（`browserLocal.feature.*`）。 */
  limitKey: MessageKey;
}

const features: BrowserLocalFeature[] = [];

/**
 * 登记一项浏览器中可用的功能，供外壳「浏览器本地模式」标识弹层列出。
 *
 * 功能模块在导入时与 `registerBrowserHandler` 一起调用，并在 zh.ts / en.ts 同步添加文案。
 * 注册在 main.ts 挂载应用前完成，因此列表无需响应式。`id` 重复视为编码错误并抛出。
 */
export function registerBrowserLocalFeature(feature: BrowserLocalFeature): void {
  if (features.some((existing) => existing.id === feature.id)) {
    throw new Error(`Browser-local feature "${feature.id}" is already registered`);
  }
  features.push({ ...feature });
}

/** 已登记的浏览器可用功能（只读视图，按登记顺序）。 */
export const browserLocalFeatures: readonly Readonly<BrowserLocalFeature>[] = features;

/** 仅供测试：清空已注册的处理器与功能。 */
export function __resetBrowserRuntimeForTests(): void {
  handlers.clear();
  features.length = 0;
}
