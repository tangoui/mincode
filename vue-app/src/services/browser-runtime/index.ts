/**
 * 浏览器本地模式的处理器入口：只在 `!isDesktopRuntime()` 时由 main.ts 动态导入，
 * 桌面端不会加载本模块及其功能处理器。
 *
 * 每个功能模块（如 `./http`）在导入时调用 `registerBrowserHandler` 注册自己的命令，
 * 并调用 `registerBrowserLocalFeature` 登记标识弹层中的功能说明（均来自 `./registry`）。
 * 新增功能只需在此加一行 import。
 *
 * 功能处理器模块只能从本文件导入（`scripts/browser-runtime.test.mjs` 会检查）：
 * 其他位置静态导入会把它们打进主包，而主包入口距预算上限仅约 2.6 KiB。
 * `./registry` 体积很小，可以在任何位置导入。
 */
import "./http";
import "./ai";
import "./ai-store";
import "./agent-runs";
