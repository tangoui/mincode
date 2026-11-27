import type { Router } from "vue-router";
import { aiCapabilityRegistry } from "@/domain/ai/capability-registry";
import type { NativeAiTerminalPane } from "@/services/ai-native-tools";
import { aiOperationManager } from "@/domain/ai/operation-manager";
import { createResourceRegistry } from "@/domain/ai/resource-registry";
import { isTerminalLeafPane, type TerminalPaneNode } from "@/domain/terminal/types";
import { useVueI18n } from "@/i18n/messages";
import type { AgentControlExecutorDeps } from "@/services/agent-control-executor";
import { createAgentControlPaneOperation } from "@/services/agent-control-panes";
import { invokeAgentControl } from "@/services/agent-control-service";
import { syncNativeAiResources } from "@/services/ai-native-tools";
import { invokeBackend, listenAppEvent } from "@/services/desktop-bridge";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useSftpSessionsStore } from "@/stores/sftp-sessions";
import { useSshConnectionsStore } from "@/stores/ssh-connections";
import { useTerminalWorkspaceStore } from "@/stores/terminal-workspace";

export function createAppAgentControlDeps(router: Router): AgentControlExecutorDeps {
  const terminals = useTerminalWorkspaceStore();
  const database = useDatabaseWorkspaceStore();
  const ssh = useSshConnectionsStore();
  const sftp = useSftpSessionsStore();
  const { t } = useVueI18n();

  async function terminalPanes(): Promise<NativeAiTerminalPane[]> {
    function leafPanes(node: TerminalPaneNode): NativeAiTerminalPane[] {
      if (!isTerminalLeafPane(node)) return node.children.flatMap(leafPanes);
      return node.type === "local"
        ? [{ id: node.id, type: node.type, name: node.id }]
        : [{ id: node.id, type: node.type, name: node.profile.name, profileId: node.profile.id }];
    }
    const visible = new Map(terminals.tabs.flatMap(({ root }) => leafPanes(root)).map((pane) => [pane.id, pane]));
    for (const id of terminals.localPaneIds()) if (!visible.has(id)) visible.set(id, { id, type: "local", name: id });
    return Promise.all([...visible.values()].map(async (pane) => ({
      ...pane,
      connected: await invokeBackend<boolean>("shell_session_alive", { payload: { sessionId: pane.id } }).catch(() => false),
    })));
  }

  return {
    capabilities: aiCapabilityRegistry,
    operations: aiOperationManager,
    listen: listenAppEvent,
    invoke: invokeAgentControl,
    paneOperation: createAgentControlPaneOperation(router),
    message: (key) => t(`settings.agentControl.${key}`),
    resolveProjectRoot: (origin) => origin.kind === "pane" ? origin.cwd ?? "" : "",
    async resources(projectRoot, name) {
      const resources = createResourceRegistry();
      const capability = aiCapabilityRegistry.get(name);
      const module = capability?.module;
      if (module === "database" || module === "redis") await database.initialize();
      if (module === "ssh" || module === "sftp") await ssh.hydrate();
      const panes = module === "terminal" || module === "ssh" ? await terminalPanes() : [];
      syncNativeAiResources(resources, {
        nativeRuntime: true,
        projectRoot,
        activeTerminalId: terminals.activePaneId,
        terminalPanes: panes,
        sshProfiles: ssh.profiles,
        sftpSessions: sftp.sessions.map((session) => ({ ...session, connected: true })),
        database: {
          connections: database.connections,
          sessions: database.sessionIds,
          schemas: database.schemas,
          activeConnectionId: database.activeConnectionId,
        },
      });
      return resources;
    },
  };
}
