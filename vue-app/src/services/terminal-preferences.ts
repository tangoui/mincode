import type { IDisposable, ITerminalAddon, Terminal } from "@xterm/xterm";
import type { PredictiveEchoMode } from "@/domain/terminal/predictive-echo/gate";
import { readSettingsSnapshot } from "@/services/settings-persistence";

export const DEFAULT_TERMINAL_SCROLLBACK = 5000;

export interface TerminalPreferences {
  scrollback: number;
  webgl: boolean;
  /** SSH 终端预测回显：自动（按回显延迟开关）/ 始终 / 关闭 */
  predictiveEcho: PredictiveEchoMode;
}

const PREDICTIVE_ECHO_MODES: readonly PredictiveEchoMode[] = ["auto", "always", "off"];

/**
 * 读取设置面板中的终端偏好。设置快照尚未水合时退化为 localStorage 快照，
 * 保证终端首屏创建也能拿到用户配置；数值非法时回落到默认值。
 */
export function readTerminalPreferences(): TerminalPreferences {
  const snapshot = readSettingsSnapshot<Record<string, unknown>>();
  const scrollback = Number(snapshot.scrollback);
  return {
    scrollback:
      Number.isFinite(scrollback) && scrollback >= 1000 && scrollback <= 100000
        ? Math.round(scrollback)
        : DEFAULT_TERMINAL_SCROLLBACK,
    webgl: snapshot.webgl === undefined ? true : snapshot.webgl === true,
    predictiveEcho: PREDICTIVE_ECHO_MODES.includes(snapshot.predictiveEcho as PredictiveEchoMode)
      ? (snapshot.predictiveEcho as PredictiveEchoMode)
      : "auto",
  };
}

type WebglModule = typeof import("@xterm/addon-webgl");
let webglModule: WebglModule | undefined;
let webglLoad: Promise<WebglModule> | undefined;

function loadWebglModule(): Promise<WebglModule> {
  webglLoad ??= import("@xterm/addon-webgl").then(
    (loaded) => (webglModule = loaded),
    (error: unknown) => {
      webglLoad = undefined;
      throw error;
    },
  );
  return webglLoad;
}

/**
 * 预先加载 WebGL 插件模块（设置关闭时不加载）。加载完成后，新终端可在 open() 之前
 * 同步装上 WebGL 渲染器，xterm 就不会先建一套 DOM 渲染器、fit 一次，再换成 WebGL 重排。
 */
export function preloadTerminalWebgl(): Promise<void> {
  if (!readTerminalPreferences().webgl) return Promise.resolve();
  return loadWebglModule().then(() => undefined, () => undefined);
}

/**
 * 按设置启用 WebGL 渲染。addon 体积较大，仅在开关打开时动态加载；
 * 上下文丢失或初始化失败时静默回退到 DOM 渲染。
 * 模块已预加载时同步装上，可在 xterm.open() 之前调用（渲染器随 open 创建）；否则异步加载，
 * 到时终端须已 open。`onSettled` 在装上（总是异步）及上下文丢失回退后调用（已释放则不调用）：
 * WebGL 按整设备像素量字符宽度，同一容器能放下的列数与 DOM 渲染不同，调用方据此重新 fit。
 */
export function attachTerminalWebglAddon(terminal: Terminal, onSettled?: () => void): IDisposable | undefined {
  if (!readTerminalPreferences().webgl) return undefined;

  let addon: (ITerminalAddon & IDisposable) | undefined;
  let canvases: HTMLCanvasElement[] = [];
  // Loaded before open: the renderer's canvases appear only at open, so collect them at release.
  let collectCanvasesOnRelease = false;
  let disposed = false;
  const releaseRenderer = () => {
    if (collectCanvasesOnRelease) canvases = terminalCanvases(terminal);
    collectCanvasesOnRelease = false;
    const current = addon;
    addon = undefined;
    current?.dispose();
    loseWebglContexts(canvases);
    canvases = [];
  };
  const handle = {
    dispose() {
      if (disposed) return;
      disposed = true;
      releaseRenderer();
    },
  };

  const install = ({ WebglAddon }: WebglModule) => {
    try {
      const created = new WebglAddon();
      created.onContextLoss(() => {
        if (disposed || addon !== created) return;
        releaseRenderer();
        // The fallback uses different glyph metrics. Notify the pane even when
        // its container has not resized, so it can refit and repaint the buffer.
        onSettled?.();
      });
      const opened = Boolean(terminal.element);
      const before = new Set(terminalCanvases(terminal));
      // 终端已销毁时 loadAddon 会抛错，统一在下方回退处理。
      terminal.loadAddon(created);
      addon = created;
      // 记下渲染器新建的画布，释放时据此丢弃它们的 GL 上下文。
      if (opened) canvases = terminalCanvases(terminal).filter((canvas) => !before.has(canvas));
      else collectCanvasesOnRelease = true;
    } catch {
      // WebGL 不可用或终端已销毁时保持默认 DOM 渲染。
    }
  };

  if (webglModule) {
    install(webglModule);
    queueMicrotask(() => {
      if (!disposed) onSettled?.();
    });
    return handle;
  }

  void loadWebglModule()
    .then((loaded) => {
      if (disposed) return;
      install(loaded);
      onSettled?.();
    })
    .catch(() => {
      // 加载失败保持默认 DOM 渲染。
      if (!disposed) onSettled?.();
    });

  return handle;
}

function terminalCanvases(terminal: Terminal) {
  return terminal.element ? [...terminal.element.querySelectorAll("canvas")] : [];
}

/**
 * 主动丢弃 WebGL 渲染器的 GL 上下文，显存立即释放。插件 dispose 只移除画布，
 * 上下文要等垃圾回收才释放，空闲的应用可能很久都不回收。只处理渲染器自己创建的画布，
 * 它们已有 webgl2 上下文，getContext 返回现有上下文而不会新建。
 */
export function loseWebglContexts(canvases: readonly HTMLCanvasElement[]) {
  for (const canvas of canvases) {
    try {
      const context = canvas.getContext("webgl2");
      context?.getExtension("WEBGL_lose_context")?.loseContext();
    } catch {
      // 画布已失效时无需处理。
    }
  }
}
