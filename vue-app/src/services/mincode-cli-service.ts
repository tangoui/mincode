import { invokeBackend } from "@/services/desktop-bridge";

export type MincodeCliState = "installed" | "missing" | "conflict" | "unavailable";

export interface MincodeCliStatus {
  platform: string;
  path: string;
  state: MincodeCliState;
  /** MinCode can add the bundled CLI to PATH itself (false when a system installer manages it). */
  installable: boolean;
  /** macOS / Linux: the command link; Windows: the bin directory added to the user PATH. */
  linkPath: string;
  conflictTarget: string;
  /** Directory of the command link when it is not on PATH yet; empty otherwise. */
  pathHint: string;
}

const states: readonly MincodeCliState[] = ["installed", "missing", "conflict", "unavailable"];

function text(value: unknown) {
  return typeof value === "string" ? value : "";
}

export function normalizeMincodeCliStatus(value: unknown): MincodeCliStatus {
  const raw = value && typeof value === "object" ? (value as Record<string, unknown>) : {};
  const state = text(raw.state) as MincodeCliState;
  if (!states.includes(state)) throw new Error("Invalid mincode CLI status");
  return {
    platform: text(raw.platform),
    path: text(raw.path),
    state,
    installable: raw.installable === true,
    linkPath: text(raw.linkPath),
    conflictTarget: text(raw.conflictTarget),
    pathHint: text(raw.pathHint),
  };
}

export async function getMincodeCliStatus() {
  return normalizeMincodeCliStatus(await invokeBackend<unknown>("plugin_host_cli_status", {}));
}

export async function installMincodeCli(replace = false) {
  return normalizeMincodeCliStatus(await invokeBackend<unknown>("plugin_host_cli_install", { replace }));
}

export async function uninstallMincodeCli() {
  return normalizeMincodeCliStatus(await invokeBackend<unknown>("plugin_host_cli_uninstall", {}));
}
