// 当前窗口的控制（作用于发起调用的页面所属窗口，见 internal/host/native.go）。
import { nativeCall } from "@/services/desktop-runtime";

export const Window = {
  Minimise: () => nativeCall<void>("window.minimize"),
  UnMinimise: () => nativeCall<void>("window.unminimize"),
  Show: () => nativeCall<void>("window.show"),
  ToggleMaximise: () => nativeCall<void>("window.toggleMaximize"),
  Fullscreen: () => nativeCall<void>("window.fullscreen"),
  UnFullscreen: () => nativeCall<void>("window.unfullscreen"),
  /** 等同点关闭按钮：主窗口按设置可能隐藏到托盘。 */
  Close: () => nativeCall<void>("window.close"),
  Size: () => nativeCall<{ width: number; height: number }>("window.size"),
  SetSize: (width: number, height: number) => nativeCall<void>("window.setSize", { width, height }),
};
