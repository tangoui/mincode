import type { SshConfigDiscoveredProfile } from "@/domain/ssh/types";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface SshKnownHostEntry {
  id: string;
  hosts: string[];
  marker?: string;
  algorithm: string;
  fingerprint: string;
  line: number;
}

export interface SshKnownHostsResult {
  path: string;
  entries: SshKnownHostEntry[];
}

export type SshConfigProfile = SshConfigDiscoveredProfile;

export interface SshConfigDiscoveryResult {
  path: string;
  profiles: SshConfigProfile[];
  warnings: string[];
  discoveredAt: number;
}

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("SSH security management is available in desktop mode only.");
}

export async function listKnownHosts() {
  requireDesktop();
  return invokeBackend<SshKnownHostsResult>("ssh_known_hosts_list");
}

export async function removeKnownHost(id: string) {
  requireDesktop();
  return invokeBackend<boolean>("ssh_known_hosts_remove", { id });
}

export async function discoverSshConfig(path?: string) {
  requireDesktop();
  return invokeBackend<SshConfigDiscoveryResult>("ssh_config_discover", { path });
}
