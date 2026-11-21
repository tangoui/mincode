import { computed, onActivated, onBeforeUnmount, onDeactivated, watch, type Ref } from "vue";
import { settingsSnapshotVersion } from "@/services/settings-persistence";
import { listLspServers, registerProjectLspHost, type LspServerInfo } from "@/services/lsp-service";
import type { ProjectLanguageServerController } from "@/composables/project-language-server-runtime";
import type { LspProjectSettings } from "@/domain/projects/lsp";

// Monaco-free facade: the LSP runtime (and with it Monaco) is only imported once an
// editor needs it, so the Project route can render without the editor chunk.
// Lifecycle hooks must still be registered synchronously during setup.
export function useProjectLanguageServers(options: {
  projectRoot: Ref<string>;
  enabledServers: Ref<string[]>;
  /** Whole project LSP settings: plugin language overrides are read from here. */
  lspSettings?: Ref<LspProjectSettings>;
  /** Enables a server in the project's LSP settings (what the Language Servers dialog does). */
  enableServer?: (serverId: string) => void;
}) {
  const { projectRoot, enabledServers } = options;
  const pluginLanguageOverrides = computed(() => options.lspSettings?.value.pluginLanguageOverrides ?? {});
  let controller: ProjectLanguageServerController | null = null;
  let loading: Promise<ProjectLanguageServerController | null> | null = null;
  let active = true;
  let disposed = false;

  function ensureLoaded(): Promise<ProjectLanguageServerController | null> {
    loading ??= import("@/composables/project-language-server-runtime").then(
      (runtime) => {
        if (disposed) return null;
        controller = runtime.createProjectLanguageServerController({ projectRoot, enabledServers, pluginLanguageOverrides, active });
        return controller;
      },
      () => {
        // Chunk load failed (e.g. offline dev server); allow a later editor mount to retry.
        loading = null;
        return null;
      }
    );
    return loading;
  }

  watch(settingsSnapshotVersion, () => controller?.settingsChanged());

  // Lets other features (Java debugging) drive this project's built-in servers;
  // the runtime chunk loads only when they do.
  const unregisterHost = registerProjectLspHost({
    root: () => projectRoot.value,
    active: () => active && !disposed,
    builtinServerAvailability: async (serverId, language) =>
      (await ensureLoaded())?.builtinServerAvailability(serverId, language) ?? "unavailable",
    enableBuiltinServer: (serverId) => {
      if (!options.enableServer) return false;
      if (!enabledServers.value.includes(serverId)) options.enableServer(serverId);
      return enabledServers.value.includes(serverId);
    },
    restartBuiltinServer: async (serverId) =>
      (await ensureLoaded())?.restartBuiltinServer(projectRoot.value, serverId) ?? false,
  });

  watch(
    [projectRoot, enabledServers, pluginLanguageOverrides],
    ([root, enabled, overrides]) =>
      controller?.sync(root as string, enabled as string[], overrides as Record<string, string>),
    { deep: true }
  );

  onActivated(() => {
    active = true;
    controller?.activate();
  });

  onDeactivated(() => {
    active = false;
    controller?.deactivate();
  });

  // Before the runtime loads no language server session can exist, so there is nothing to notify.
  async function notifyFileSaved(path: string) {
    await controller?.notifyFileSaved(path);
  }

  /** Apply LSP formatting to the file at the given path (for format-on-save). */
  async function formatDocument(path: string): Promise<void> {
    await (await ensureLoaded())?.formatDocument(path);
  }

  async function fetchServerList(): Promise<LspServerInfo[]> {
    return listLspServers();
  }

  onBeforeUnmount(async () => {
    unregisterHost();
    disposed = true;
    active = false;
    await controller?.dispose();
    controller = null;
  });

  return { ensureLoaded, notifyFileSaved, formatDocument, fetchServerList };
}
