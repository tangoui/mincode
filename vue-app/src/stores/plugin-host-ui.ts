import { acceptHMRUpdate, defineStore } from "pinia";
import { ref } from "vue";
import type { BackupDialogRequest } from "@/domain/backup/requests";
import type { PluginPrompt } from "@/domain/plugins/host-prompts";
import type { PluginDiffRequest } from "@/domain/plugins/host-ui";

/** Diff dialogs opened through host.diff.open; older ones are dropped beyond this. */
const MAX_OPEN_DIFFS = 5;
/** Prompts wait here while another one is on screen. */
const MAX_QUEUED_PROMPTS = 20;

/**
 * Plugin requests that need a host dialog: prompts (host.quickPick /
 * host.inputBox) are shown one at a time in arrival order; diffs
 * (host.diff.open) stack as read-only dialogs; backup dialogs (backup
 * password, restore confirmation; services/backup-host.ts) queue like prompts.
 */
export const usePluginHostUiStore = defineStore("plugin-host-ui", () => {
  const prompts = ref<PluginPrompt[]>([]);
  const diffs = ref<Array<PluginDiffRequest & { key: number }>>([]);
  const backupDialogs = ref<BackupDialogRequest[]>([]);
  let diffSequence = 0;

  function pushPrompt(prompt: PluginPrompt) {
    if (prompts.value.some((item) => item.id === prompt.id) || prompts.value.length >= MAX_QUEUED_PROMPTS) return false;
    prompts.value = [...prompts.value, prompt];
    return true;
  }

  function removePrompt(id: string) {
    prompts.value = prompts.value.filter((item) => item.id !== id);
  }

  function pushDiff(diff: PluginDiffRequest) {
    diffs.value = [...diffs.value, { ...diff, key: ++diffSequence }].slice(-MAX_OPEN_DIFFS);
  }

  function closeDiff(key: number) {
    diffs.value = diffs.value.filter((item) => item.key !== key);
  }

  function pushBackupDialog(request: BackupDialogRequest) {
    if (backupDialogs.value.some((item) => item.id === request.id) || backupDialogs.value.length >= MAX_QUEUED_PROMPTS) return false;
    backupDialogs.value = [...backupDialogs.value, request];
    return true;
  }

  function removeBackupDialog(id: string) {
    backupDialogs.value = backupDialogs.value.filter((item) => item.id !== id);
  }

  return { prompts, diffs, backupDialogs, pushPrompt, removePrompt, pushDiff, closeDiff, pushBackupDialog, removeBackupDialog };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginHostUiStore, import.meta.hot));
}
