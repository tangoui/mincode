/**
 * Frontend stage of the `keymaps` contribution point: registers the presets
 * the Go host already validated structurally, through the frontend validator
 * (`registerKeymapPreset`: command catalog, OS-reserved keys). A preset that
 * fails is skipped and reported, never thrown. Pure: no IPC, no Vue; the
 * runtime wiring lives in services/contributions/keymaps.ts.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { KeymapPresetError, registerKeymapPreset } from "@/domain/keybindings/preset";

export const KEYMAP_CONTRIBUTION_POINT = "keymaps";

export interface KeymapPluginIssue {
  pluginId: string;
  /** Index into contributes.keymaps, or -1 for issues across presets (extends chains). */
  entry: number;
  preset?: string;
  message: string;
  /** `host`: rejected by the Go manifest validator; `frontend`: rejected when registering. */
  stage: "host" | "frontend";
}

export interface KeymapContributionResult {
  registered: string[];
  issues: KeymapPluginIssue[];
  dispose: () => void;
}

function presetId(value: unknown): string | undefined {
  return typeof value === "object" && value !== null && typeof (value as { id?: unknown }).id === "string"
    ? (value as { id: string }).id
    : undefined;
}

/** Registers every contributed preset; `dispose` unregisters exactly those that registered. */
export function registerKeymapContributions(contributions: readonly PointContribution[]): KeymapContributionResult {
  const disposers: Array<() => void> = [];
  const registered: string[] = [];
  const issues: KeymapPluginIssue[] = [];
  for (const contribution of contributions) {
    for (const issue of contribution.point.issues) {
      issues.push({
        pluginId: contribution.pluginId,
        stage: "host",
        entry: issue.entry,
        message: issue.message,
        ...(issue.id ? { preset: issue.id } : {}),
      });
    }
    contribution.point.entries.forEach((preset, entry) => {
      const id = presetId(preset);
      try {
        disposers.push(registerKeymapPreset(preset, {
          source: "plugin",
          pluginId: contribution.pluginId,
          pluginName: contribution.pluginName,
        }));
        if (id) registered.push(id);
      } catch (error) {
        issues.push({
          pluginId: contribution.pluginId,
          stage: "frontend",
          entry,
          message: error instanceof KeymapPresetError || error instanceof Error ? error.message : String(error),
          ...(id ? { preset: id } : {}),
        });
      }
    });
  }
  return {
    registered,
    issues,
    dispose: () => {
      for (const dispose of disposers.splice(0)) dispose();
    },
  };
}
