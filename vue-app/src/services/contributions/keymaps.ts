/**
 * Consumer of the `keymaps` contribution point (declarative plugins, e.g.
 * official-plugins/keymaps). The Go host validates presets structurally at
 * install and load time; `registerKeymapContributions` additionally runs the
 * frontend validator (command catalog, OS-reserved keys). A preset that
 * fails is skipped and reported, never thrown. Registering data never starts
 * a plugin process.
 */
import {
  KEYMAP_CONTRIBUTION_POINT,
  registerKeymapContributions,
  type KeymapPluginIssue,
} from "@/domain/plugins/keymap-contributions";
import { registerContributionConsumer } from "@/services/plugin-contributions";

export type { KeymapPluginIssue } from "@/domain/plugins/keymap-contributions";

let lastIssues: KeymapPluginIssue[] = [];
let lastSyncError: string | undefined;

/**
 * Issues of the last applied snapshot (skipped presets), for diagnostics.
 * Host-stage issues are also written to the plugin's log by the Go manager.
 */
export function keymapPluginIssues(): readonly KeymapPluginIssue[] {
  return lastIssues;
}

/** The error of the last failed refresh (plugin host unavailable), if any. */
export function keymapPluginSyncError(): string | undefined {
  return lastSyncError;
}

/**
 * Registers the keymaps consumer. A failed refresh keeps the previously
 * registered presets; the keybindings store falls back to MinCode for a
 * selection that never registered.
 */
export function registerKeymapContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: KEYMAP_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const result = registerKeymapContributions(contributions);
      lastIssues = result.issues;
      lastSyncError = undefined;
      return result.dispose;
    },
    onError: (message) => {
      lastSyncError = message;
    },
  });
}
