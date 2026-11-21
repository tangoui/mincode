import { watch } from "vue";
import { parseEditorFramework } from "@/domain/editor/frameworks";
import { detectFrameworkRoots } from "@/domain/editor/framework-detection";
import type { PointContribution } from "@/domain/plugins/contributions";
import { setEditorFrameworks, setFrameworkRoots, type FrameworkRegistration } from "@/services/editor-framework-registry";
import { registerMonacoContributionHook } from "@/services/monaco-contributions";
import { attachMonacoFrameworks, frameworkModelPath } from "@/services/monaco-frameworks";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

let detectionError: string | undefined;
export function editorFrameworkDetectionError(): string | undefined { return detectionError; }

async function readMarker(projectRoot: string, filePath: string): Promise<string | undefined> {
  try {
    const info = await invokeBackend<{ isFile: boolean; size: number }>("local_fs_file_info", { projectRoot, filePath });
    if (!info || typeof info.isFile !== "boolean" || !Number.isSafeInteger(info.size)) throw new Error("Invalid marker metadata");
    if (!info.isFile || info.size > 262144) return undefined;
    const text = await invokeBackend<unknown>("local_fs_read_text_file", { projectRoot, filePath });
    if (typeof text !== "string" || text.length > 262144) throw new Error("Invalid marker contents");
    return text;
  } catch (error) {
    if (/no such file|cannot find the file|not found/i.test(String(error))) return undefined;
    throw error;
  }
}

export function applyEditorFrameworks(contributions: PointContribution[]): () => void {
  const registrations: FrameworkRegistration[] = [];
  for (const contribution of contributions) {
    for (const entry of contribution.point.entries) {
      const framework = parseEditorFramework(entry);
      if (!framework) throw new Error(`Invalid editor framework: ${contribution.pluginId}`);
      registrations.push({ pluginId: contribution.pluginId, framework });
    }
  }
  setEditorFrameworks(registrations);
  const roots: string[] = [];
  let disposed = false;
  let generation = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let openPaths: () => string[] = () => [];
  const refresh = () => {
    generation += 1;
    const token = generation;
    // 项目变更后立即撤销旧匹配，异步期间采用拒绝默认值。
    setFrameworkRoots([], roots);
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = undefined;
      if (!isDesktopRuntime()) { detectionError = "Native project detection is unavailable in browser preview"; return; }
      void detectFrameworkRoots(registrations, roots, openPaths(), readMarker).then((result) => {
        if (disposed || token !== generation) return;
        detectionError = undefined;
        setFrameworkRoots(result.roots, result.boundaries);
      }, (error: unknown) => {
        if (!disposed && token === generation) detectionError = error instanceof Error ? error.message : String(error);
      });
    }, 150);
  };
  const stopWatch = watch(() => roots, refresh, { immediate: true });
  const detach = registerMonacoContributionHook((monaco) => {
    openPaths = () => monaco.editor.getModels().filter((model) => !model.isDisposed()).map(frameworkModelPath).filter(Boolean);
    const modelListeners = new Map<string, { dispose(): void }>();
    const markerNames = new Set(["package.json", ...registrations.flatMap(({ framework }) => [...framework.match.all, ...framework.match.any, ...framework.match.exclude].map((condition) => condition.file.split("/").pop()))]);
    const observe = () => {
      for (const model of monaco.editor.getModels()) {
        if (model.isDisposed() || modelListeners.has(model.uri.toString())) continue;
        if (markerNames.has(frameworkModelPath(model).split("/").pop())) {
          modelListeners.set(model.uri.toString(), model.onDidChangeContent(refresh));
        }
      }
      refresh();
    };
    const providers = attachMonacoFrameworks(monaco, registrations);
    const listeners = [monaco.editor.onDidCreateModel(observe), monaco.editor.onWillDisposeModel((model) => {
      modelListeners.get(model.uri.toString())?.dispose();
      modelListeners.delete(model.uri.toString());
      refresh();
    })];
    observe();
    return () => {
      providers();
      listeners.forEach((listener) => listener.dispose());
      modelListeners.forEach((listener) => listener.dispose());
      openPaths = () => [];
    };
  });
  return () => {
    disposed = true;
    generation += 1;
    if (timer) clearTimeout(timer);
    stopWatch();
    detach();
    setEditorFrameworks([]);
    detectionError = undefined;
  };
}
