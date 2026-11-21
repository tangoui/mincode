import { computed, ref } from "vue";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import {
  isBrowserProjectPath,
  supportsBrowserDirectoryPicker,
  type BrowserProjectAccess,
} from "@/domain/projects/browser-root";
import { projectBasename } from "@/domain/projects/types";

interface BrowserAccessStore {
  readonly projectRoot: string;
  /** 记住的项目；横幅只针对仍在列表中的根。 */
  readonly projects: readonly { readonly root: string }[];
  readonly browserRootAccess: Readonly<Record<string, BrowserProjectAccess>>;
  refreshBrowserRootAccess(root: string): Promise<BrowserProjectAccess>;
  requestBrowserRootAccess(root: string): Promise<boolean>;
}

/** project-fs 处理器在没有权限（`project.browser.permission`）或句柄丢失（`project.browser.missing`）时抛出的错误。 */
export function isBrowserProjectAccessError(error: unknown) {
  const message = error instanceof Error ? error.message : "";
  return message === "project.browser.permission" || message === "project.browser.missing";
}

/**
 * 浏览器本地模式下 `browser://` 项目根的访问状态与「重新授权」横幅。
 *
 * 刷新后浏览器把文件夹权限重置为 `prompt`：打开、切换到某个项目根，或读取它的目录 / 文件因权限或句柄
 * 丢失而失败时都要重新查询，让横幅出现，不能把读取失败显示成空文件夹。桌面端所有方法都是空操作。
 */
export function useBrowserProjectAccess(project: BrowserAccessStore) {
  const isBrowserLocalRoot = (root: string) => !isDesktopRuntime() && isBrowserProjectPath(root);
  /** Safari、Firefox 没有 showDirectoryPicker：打开文件夹按钮改为说明需要 Chrome / Edge。 */
  const pickerUnsupported = !isDesktopRuntime() && !supportsBrowserDirectoryPicker();
  /** 最近一次查询为未授权 / 已丢失、但不是当前项目的浏览器根（例如在树里展开了另一个项目）。 */
  const attentionRoot = ref("");

  /** 查询访问状态；非浏览器根恒为 true。未授权时记下该根，供横幅显示。 */
  async function check(root: string) {
    if (!isBrowserLocalRoot(root)) return true;
    const granted = (await project.refreshBrowserRootAccess(root)) === "granted";
    if (!granted) attentionRoot.value = root;
    else if (attentionRoot.value === root) attentionRoot.value = "";
    return granted;
  }

  /** 读取失败时调用：属于浏览器权限 / 句柄问题则重新查询（横幅随之出现）并返回 true。 */
  function noteFailure(root: string, error: unknown) {
    if (!isBrowserLocalRoot(root) || !isBrowserProjectAccessError(error)) return false;
    void check(root);
    return true;
  }

  function needsAttention(root: string) {
    const access = root && isBrowserLocalRoot(root) ? project.browserRootAccess[root] : undefined;
    return access === "prompt" || access === "missing";
  }

  /** 最近失败的其他浏览器项目；已从列表移除的根不再提示（否则「重新授权」会把它重新加回来）。 */
  const rememberedAttentionRoot = () => {
    const root = attentionRoot.value;
    return root && project.projects.some((entry) => entry.root === root) ? root : "";
  };

  /** 横幅针对的根：当前项目优先，其次是最近失败、且仍在列表中的其他浏览器项目。 */
  const banner = computed(() => {
    const root = [project.projectRoot, rememberedAttentionRoot()].find(needsAttention);
    if (!root) return null;
    return { root, name: projectBasename(root), access: project.browserRootAccess[root] as "prompt" | "missing" };
  });

  /** 请求权限（必须在用户点击中调用）。 */
  async function reauthorize(root: string) {
    const granted = await project.requestBrowserRootAccess(root);
    if (granted && attentionRoot.value === root) attentionRoot.value = "";
    return granted;
  }

  /** 浏览器模式下的磁盘路径（手输或桌面端 worktree 绑定）：没有句柄，无法打开。 */
  const isNativePathInBrowser = (root: string) => !isDesktopRuntime() && !isBrowserProjectPath(root);

  return { pickerUnsupported, isBrowserLocalRoot, isNativePathInBrowser, check, noteFailure, banner, reauthorize };
}
