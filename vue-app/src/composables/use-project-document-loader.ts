import type { ProjectEditorDocument } from "@/domain/projects/types";
import type { useLocalProjectStore } from "@/stores/local-project";
import { nativeErrorMessage } from "@/services/desktop-bridge";

type ProjectStore = Pick<ReturnType<typeof useLocalProjectStore>,
  "activeWorkspaceId" | "documents" | "openDocument" | "setDocumentLoading" | "setDocumentLoaded" | "setDocumentError"
>;

// 打开文件时，新标签的渲染和建编辑器（setDocumentLoaded）各是一段较长的同步工作。
// 读文件的回复常常在下一帧之前就到了（桌面 IPC 很快），两段会连成一个长任务、中间不出帧，
// 所以内容要等下一帧画完再应用：rAF 回调在绘制之前执行，再用 setTimeout 推到绘制之后。
// 读文件照常立即发出，总耗时基本不变。窗口被遮挡时 rAF 不触发，按超时继续。
const PAINT_WAIT_TIMEOUT_MS = 50;

function afterNextPaint(): Promise<void> {
  if (typeof requestAnimationFrame !== "function") return Promise.resolve();
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, PAINT_WAIT_TIMEOUT_MS);
    requestAnimationFrame(() => setTimeout(() => {
      clearTimeout(timer);
      resolve();
    }, 0));
  });
}

/** Share concurrent reads, without letting an old read reactivate a tab or cross workspaces. */
export function useProjectDocumentLoader(
  project: ProjectStore,
  read: (path: string, root: string) => Promise<string>,
) {
  const pending = new WeakMap<ProjectEditorDocument, Promise<void>>();

  function load(path: string, root: string): Promise<void> {
    const document = project.documents.find((item) => item.path === path) ?? project.openDocument(path);
    if (document.loaded) return Promise.resolve();
    const existing = pending.get(document);
    if (existing) return existing;
    const workspaceId = project.activeWorkspaceId;
    const isCurrent = () => project.activeWorkspaceId === workspaceId &&
      project.documents.find((item) => item.path === path) === document && !document.editor && !document.loaded;
    project.setDocumentLoading(path, true);
    const task = (async () => {
      try {
        const [content] = await Promise.all([read(path, root), afterNextPaint()]);
        if (isCurrent()) project.setDocumentLoaded(path, content);
      } catch (error) {
        if (isCurrent()) project.setDocumentError(path, "project.readFailed", nativeErrorMessage(error));
      } finally {
        pending.delete(document);
      }
    })();
    pending.set(document, task);
    return task;
  }

  return { load };
}
