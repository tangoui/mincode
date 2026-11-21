import { watch } from "vue";
import type { ProjectEditorViewState } from "@/domain/projects/types";
import { publishPluginHostEvent, type PluginHostSelection } from "@/services/plugin-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { useLocalProjectStore } from "@/stores/local-project";

const ACTIVE_FILE_DEBOUNCE_MS = 150;
/** Cursor moves are coalesced; only the last selection in each window is sent. */
const SELECTION_THROTTLE_MS = 250;

/**
 * Feeds the plugin host editor activity (scope is owned by plugin-workspace-scope) so process
 * plugins can use host.workspace.* and receive host events
 * (backend/plugin/workspace_host.go). The backend drops paths
 * outside the root and never forwards VCS internals or credential files.
 * Returns a disposer; outside the desktop runtime it does nothing.
 */
export function installPluginHostEventBridge(): () => void {
  if (!isDesktopRuntime()) return () => undefined;
  const localProject = useLocalProjectStore();
  let activeTimer: ReturnType<typeof setTimeout> | undefined;

  const stopActive = watch(
    () => localProject.activePath,
    (path) => {
      clearTimeout(activeTimer);
      activeTimer = setTimeout(() => {
        void publishPluginHostEvent("editor.activeFileChanged", path ?? null).catch(() => undefined);
      }, ACTIVE_FILE_DEBOUNCE_MS);
    }
  );
  let selectionTimer: ReturnType<typeof setTimeout> | undefined;
  let pendingSelection: { path: string; selection: PluginHostSelection } | undefined;
  let lastSelection = "";
  const flushSelection = () => {
    selectionTimer = undefined;
    const pending = pendingSelection;
    pendingSelection = undefined;
    if (!pending) return;
    const key = `${pending.path}:${Object.values(pending.selection).join(",")}`;
    if (key === lastSelection) return;
    lastSelection = key;
    void publishPluginHostEvent("editor.selectionChanged", pending.path, pending.selection).catch(() => undefined);
  };
  const stopSaved = localProject.$onAction(({ name, args, after }) => {
    if (name === "setEditorViewState") {
      const [path, state] = args as [string, ProjectEditorViewState];
      if (path !== localProject.activePath || !state) return;
      pendingSelection = { path, selection: {
        startLine: state.selectionStartLineNumber, startColumn: state.selectionStartColumn,
        endLine: state.positionLineNumber, endColumn: state.positionColumn,
      } };
      selectionTimer ??= setTimeout(flushSelection, SELECTION_THROTTLE_MS);
      return;
    }
    if (name !== "markDocumentSaved") return;
    const [path] = args as [string];
    after(() => {
      void publishPluginHostEvent("editor.fileSaved", path).catch(() => undefined);
    });
  });

  return () => {
    clearTimeout(activeTimer);
    clearTimeout(selectionTimer);
    stopActive();
    stopSaved();
  };
}
