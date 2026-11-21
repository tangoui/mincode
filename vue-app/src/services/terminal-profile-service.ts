/**
 * Plugin terminal profiles (contributes.terminalProfiles): the IPC boundary.
 *
 * The WebView never sends a command, args or env for a plugin profile. It
 * sends only `(pluginId, profileId)` plus the `${workspaceFolder}` value; the
 * Go host resolves the launch spec from its own manifest snapshot, checks the
 * plugin is enabled, effective in the workspace and granted `shell`, and only
 * starts the process when the user confirmed the exact spec fingerprint
 * (backend/plugin/terminal_profiles_launch.go). The confirmation is
 * required in every approval mode; AI tools cannot launch plugin profiles.
 */
import { h } from "vue";
import { ElMessageBox } from "element-plus";
import TerminalProfileConfirmContent from "@/components/terminal/TerminalProfileConfirmContent.vue";
import {
  parseTerminalProfileLaunch,
  terminalProfileErrorCode,
  terminalProfileTitle,
  type TerminalProfileLaunch,
} from "@/domain/terminal/profiles";
import type { TerminalProfileRef } from "@/domain/terminal/types";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

/** Window event asking the tab bar to open a profile in a new terminal tab (detail: `{ key }`). */
export const TERMINAL_PROFILE_OPEN_EVENT = "app:terminal-profile-open";

export interface TerminalProfileStartOptions {
  sessionId: string;
  fingerprint: string;
  cols?: number;
  rows?: number;
  attachIfExists?: boolean;
}

type ProfileTarget = Pick<TerminalProfileRef, "pluginId" | "profileId" | "workspaceFolder">;

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("unavailable: terminal profiles need the MinCode desktop app");
}

function parseLaunch(raw: unknown, operation: string): TerminalProfileLaunch {
  const launch = parseTerminalProfileLaunch(raw);
  if (!launch) throw new Error(`unavailable: ${operation} returned an invalid launch spec`);
  return launch;
}

/** Resolves the launch spec on the host (nothing is started). */
export async function prepareTerminalProfile(target: ProfileTarget): Promise<TerminalProfileLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_terminal_profile_prepare", {
    payload: { pluginId: target.pluginId, profileId: target.profileId, workspaceFolder: target.workspaceFolder ?? "" },
  });
  return parseLaunch(raw, "prepare");
}

/** Stores the user's confirmation of exactly `fingerprint` (the host re-resolves and compares). */
export async function trustTerminalProfile(target: ProfileTarget, fingerprint: string): Promise<TerminalProfileLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_terminal_profile_trust", {
    payload: {
      pluginId: target.pluginId,
      profileId: target.profileId,
      fingerprint,
      workspaceFolder: target.workspaceFolder ?? "",
    },
  });
  return parseLaunch(raw, "trust");
}

/** Starts (or re-attaches to) the PTY session of a confirmed profile. */
export async function startTerminalProfileSession(target: ProfileTarget, options: TerminalProfileStartOptions) {
  requireDesktop();
  return invokeBackend<string>("plugin_host_terminal_profile_start", {
    payload: {
      pluginId: target.pluginId,
      profileId: target.profileId,
      fingerprint: options.fingerprint,
      sessionId: options.sessionId,
      workspaceFolder: target.workspaceFolder ?? "",
      cols: options.cols,
      rows: options.rows,
      attachIfExists: options.attachIfExists ?? false,
    },
  });
}

/**
 * Shows the exact host-resolved command line and asks the user to run it.
 * Cancel keeps the initial focus. Resolves to the trusted launch, or
 * undefined when the user cancelled.
 */
async function confirmLaunch(target: ProfileTarget, launch: TerminalProfileLaunch) {
  const { t, localeChain } = useVueI18n();
  try {
    await ElMessageBox.confirm(
      h(TerminalProfileConfirmContent, { launch, changed: launch.previouslyTrusted }),
      t("pluginContrib.terminalProfiles.confirmTitle", { title: terminalProfileTitle(launch, localeChain.value) }),
      {
        type: "warning",
        confirmButtonText: t("pluginContrib.terminalProfiles.confirmRun"),
        cancelButtonText: t("common.cancel"),
        distinguishCancelAndClose: true,
        autofocus: false,
        customClass: "app-terminal-profile-confirm",
      },
    );
  } catch {
    return undefined;
  }
  return trustTerminalProfile(target, launch.fingerprint);
}

/**
 * Prepares a profile and, when the host has no matching confirmation, asks
 * the user. Resolves to a trusted launch or undefined (cancelled). Host
 * errors (`forbidden`, `unavailable`, …) are rethrown.
 */
export async function ensureTerminalProfileConfirmed(target: ProfileTarget): Promise<TerminalProfileLaunch | undefined> {
  const launch = await prepareTerminalProfile(target);
  if (launch.trusted) return launch;
  return confirmLaunch(target, launch);
}

/** Asks the tab bar to open `key` (`<pluginId>.<profileId>`) in a new terminal tab. */
export function requestTerminalProfileTab(key: string) {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent(TERMINAL_PROFILE_OPEN_EVENT, { detail: { key } }));
}

/** Localized message for a host error; falls back to the raw message. */
export function terminalProfileErrorMessage(error: unknown): string {
  const { t } = useVueI18n();
  switch (terminalProfileErrorCode(error)) {
    case "unavailable":
    case "not_found":
      return t("pluginContrib.terminalProfiles.unavailable");
    case "changed":
    case "untrusted":
      return t("pluginContrib.terminalProfiles.changed");
    case "forbidden":
      return t("pluginContrib.terminalProfiles.forbidden");
    default:
      return error instanceof Error ? error.message : String(error);
  }
}
