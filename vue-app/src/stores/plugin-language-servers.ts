/**
 * Plugin language servers (contributes.languageServers): the registry set by
 * the contribution consumer and the per-project launch state the project LSP
 * runtime reports (needs review, restart limit reached, unavailable). The
 * store never holds a launch spec; starting always goes through
 * services/plugin-language-server-service.ts.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { LanguageServerIssue, PluginLanguageServer } from "@/domain/plugins/language-servers";

export type PluginLanguageServerState =
  | { kind: "review"; changed: boolean }
  | { kind: "limit" }
  | { kind: "unavailable"; message: string };

const MAX_STATES = 256;

function stateKey(projectRoot: string, sessionId: string) {
  return `${projectRoot}\u0000${sessionId}`;
}

export const usePluginLanguageServersStore = defineStore("plugin-language-servers", () => {
  const servers = ref<PluginLanguageServer[]>([]);
  const issues = ref<LanguageServerIssue[]>([]);
  /** Bumped on every contribution snapshot so the runtime re-routes languages. */
  const revision = ref(0);
  const states = ref<Record<string, PluginLanguageServerState>>({});
  /** Bumped when the user confirmed or restarted a server: the project runtime re-syncs. */
  const syncRequests = ref(0);

  function requestSync() {
    syncRequests.value += 1;
  }

  function setContributions(next: PluginLanguageServer[], nextIssues: LanguageServerIssue[] = []) {
    servers.value = next;
    issues.value = nextIssues;
    revision.value += 1;
    const live = new Set(next.map((server) => server.sessionId));
    states.value = Object.fromEntries(
      Object.entries(states.value).filter(([key]) => live.has(key.slice(key.indexOf("\u0000") + 1))),
    );
  }

  function clearContributions() {
    setContributions([]);
  }

  function server(sessionId: string) {
    return servers.value.find((candidate) => candidate.sessionId === sessionId);
  }

  function state(projectRoot: string, sessionId: string): PluginLanguageServerState | undefined {
    return states.value[stateKey(projectRoot, sessionId)];
  }

  function setState(projectRoot: string, sessionId: string, next: PluginLanguageServerState | undefined) {
    const key = stateKey(projectRoot, sessionId);
    const copy = { ...states.value };
    if (next) copy[key] = next;
    else delete copy[key];
    const keys = Object.keys(copy);
    if (keys.length > MAX_STATES) for (const old of keys.slice(0, keys.length - MAX_STATES)) delete copy[old];
    states.value = copy;
  }

  return { servers, issues, revision, states, syncRequests, requestSync, setContributions, clearContributions, server, state, setState };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginLanguageServersStore, import.meta.hot));
}
