/**
 * Plugin test providers (contributes.testProviders): the re-validated
 * providers of process plugins active in this workspace. Discovered tests
 * and results live in the Tests panel and are never persisted. The set of
 * providers whose non-read run the user confirmed lasts for the app session.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PluginTestProvider, TestProviderIssue } from "@/domain/plugins/test-providers";

export const usePluginTestProvidersStore = defineStore("plugin-test-providers", () => {
  const providers = ref<PluginTestProvider[]>([]);
  const issues = ref<TestProviderIssue[]>([]);
  const loaded = ref(false);
  /** Bumped on every snapshot so the open panel drops stale trees. */
  const revision = ref(0);
  const confirmedRuns = ref(new Set<string>());

  function setContributions(next: PluginTestProvider[], nextIssues: TestProviderIssue[]) {
    providers.value = next;
    issues.value = nextIssues;
    loaded.value = true;
    revision.value += 1;
  }

  function clearContributions() {
    providers.value = [];
    issues.value = [];
    revision.value += 1;
  }

  function isRunConfirmed(key: string) {
    return confirmedRuns.value.has(key);
  }

  function confirmRun(key: string) {
    confirmedRuns.value = new Set(confirmedRuns.value).add(key);
  }

  return { providers, issues, loaded, revision, setContributions, clearContributions, isRunConfirmed, confirmRun };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginTestProvidersStore, import.meta.hot));
}
