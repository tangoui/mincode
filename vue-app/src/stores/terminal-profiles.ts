/**
 * Terminal profiles (contributes.terminalProfiles) offered on this platform,
 * as re-validated display data from the contribution consumer. The store never
 * holds a launch spec: launching always goes through the host by reference
 * (services/terminal-profile-service.ts).
 */
import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  resolveTerminalProfiles,
  terminalProfilePlatform,
  type TerminalProfileContribution,
} from "@/domain/terminal/profiles";

function currentPlatform() {
  return terminalProfilePlatform(typeof navigator === "undefined" ? "" : navigator.platform || "");
}

export const useTerminalProfilesStore = defineStore("terminal-profiles", () => {
  const profiles = ref<TerminalProfileContribution[]>([]);
  /** True once the consumer applied a snapshot (so a missing profile means it went away). */
  const loaded = ref(false);

  const byKey = computed(() => new Map(profiles.value.map((profile) => [profile.key, profile])));

  function setContributions(contributions: readonly PointContribution[]) {
    profiles.value = resolveTerminalProfiles(contributions, currentPlatform());
    loaded.value = true;
  }

  function clearContributions() {
    profiles.value = [];
  }

  function profileByKey(key: string) {
    return byKey.value.get(key);
  }

  return { profiles, loaded, setContributions, clearContributions, profileByKey };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useTerminalProfilesStore, import.meta.hot));
}
