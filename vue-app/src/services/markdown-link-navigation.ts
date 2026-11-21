import router from "@/router";
import { resolveLocalMarkdownTarget, isSystemPackagePath } from "@/domain/markdown-local-paths";
import { isImageDocument } from "@/domain/projects/types";
import { requestAiWorkspaceAction } from "@/services/ai-workspace-actions";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { useBrowserWorkspaceStore } from "@/stores/browser-workspace";
import { useLocalProjectStore } from "@/stores/local-project";
import { useWorkbenchStore } from "@/stores/workbench";

interface LocalFsFileInfo {
  isFile: boolean;
  size: number;
}

const MAX_PROJECT_TEXT_FILE_BYTES = 5 * 1024 * 1024;
const MAX_PROJECT_IMAGE_FILE_BYTES = 20 * 1024 * 1024;

/**
 * Opens a web link clicked inside rendered markdown without hijacking the main
 * desktop webview: queues a tab in the built-in browser workspace and switches
 * to it. Browser preview mode falls back to a regular new tab.
 */
export async function openWorkspaceLink(url: string): Promise<void> {
  if (!isDesktopRuntime()) {
    window.open(url, "_blank", "noopener,noreferrer");
    return;
  }
  const browserWorkspace = useBrowserWorkspaceStore();
  const workbench = useWorkbenchStore();
  browserWorkspace.requestUserOpenTab(url);
  workbench.openView("browser");
  await router.push({ name: "browser" }).catch(() => undefined);
}

/**
 * 可预览的文本和图片在本地项目中打开；目录、应用包、二进制或超限文件
 * 在 Finder/Explorer 中选中定位。后端仍负责最终的规范路径边界检查。
 */
export async function openLocalMarkdownPath(path: string): Promise<void> {
  if (!isDesktopRuntime()) throw new Error("Local file links require the desktop runtime");

  const project = useLocalProjectStore();
  const target = resolveLocalMarkdownTarget(path, project.projects, project.projectRoot);
  if (!target || !target.relativePath) throw new Error("The link is outside the saved Local Projects");

  const fileInfo = await invokeBackend<LocalFsFileInfo>("local_fs_file_info", {
    projectRoot: target.projectRoot,
    filePath: target.relativePath,
  });
  const image = fileInfo.isFile && isImageDocument(target.relativePath);
  const binary = fileInfo.isFile && !image
    ? await invokeBackend<boolean>("local_fs_peek_binary_for_preview", {
        projectRoot: target.projectRoot,
        filePath: target.relativePath,
      })
    : false;
  const tooLarge = image
    ? fileInfo.size > MAX_PROJECT_IMAGE_FILE_BYTES
    : fileInfo.size > MAX_PROJECT_TEXT_FILE_BYTES;

  if (!fileInfo.isFile || binary || tooLarge || isSystemPackagePath(target.relativePath)) {
    await invokeBackend<void>("local_fs_show_in_file_manager", {
      path: target.absolutePath,
      select: isSystemPackagePath(target.relativePath),
    });
    return;
  }

  const workbench = useWorkbenchStore();
  workbench.openView("projects");
  await router.push({ name: "projects" });
  await requestAiWorkspaceAction("projects", {
    action: "open-file",
    path: target.relativePath,
    projectRoot: target.projectRoot,
  });
}
