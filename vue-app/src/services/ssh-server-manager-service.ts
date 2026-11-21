import { invokeBackend } from "@/services/desktop-bridge";

export interface SshServerDisk {
  mount: string;
  totalBytes: number;
  usedBytes: number;
  availBytes: number;
}

export interface SshServerDiskIO {
  device: string;
  readBytes: number;
  writeBytes: number;
}

export interface SshServerNetIO {
  interface: string;
  rxBytes: number;
  txBytes: number;
}

export interface SshServerSnapshot {
  hostname: string;
  operatingSystem: string;
  kernel: string;
  architecture: string;
  uptimeSeconds: number;
  load1: number;
  load5: number;
  load15: number;
  memoryTotalBytes: number;
  memoryUsedBytes: number;
  diskTotalBytes: number;
  diskUsedBytes: number;
  disks: SshServerDisk[];
  diskIO: SshServerDiskIO[];
  diskIOTimestampMs: number;
  cpuCores: number;
  cpuTotal: number;
  cpuIdle: number;
  netIO: SshServerNetIO[];
}

export interface SshServerProcess {
  pid: number;
  ppid: number;
  user: string;
  cpu: number;
  memory: number;
  state: string;
  command: string;
}

export interface SshSystemdService {
  unit: string;
  load: string;
  active: string;
  sub: string;
  description: string;
}

export interface SshDockerInfo {
  available: boolean;
  clientVersion?: string;
  serverVersion?: string;
  error?: string;
}

export interface SshDockerContainer {
  id: string;
  names: string;
  image: string;
  state: string;
  status: string;
  ports: string;
  created: string;
}

export function loadSshServerSnapshot(sessionId: string) {
  return invokeBackend<SshServerSnapshot>("ssh_admin_snapshot", { sessionId });
}

export function loadSshServerLogs(sessionId: string, unit: string, lines = 200) {
  return invokeBackend<string>("ssh_admin_logs", { sessionId, unit, lines });
}

export function loadSshServerProcesses(sessionId: string, limit = 150) {
  return invokeBackend<SshServerProcess[]>("ssh_admin_processes", { sessionId, limit });
}

export function signalSshServerProcess(sessionId: string, pid: number, signal: string) {
  return invokeBackend("ssh_admin_process_signal", { sessionId, pid, signal });
}

export function loadSshSystemdServices(sessionId: string) {
  return invokeBackend<SshSystemdService[]>("ssh_admin_systemd_list", { sessionId });
}

export function runSshSystemdAction(sessionId: string, unit: string, action: string) {
  return invokeBackend("ssh_admin_systemd_action", { sessionId, unit, action });
}

export function loadSshDockerInfo(sessionId: string) {
  return invokeBackend<SshDockerInfo>("ssh_admin_docker_info", { sessionId });
}

export function loadSshDockerContainers(sessionId: string) {
  return invokeBackend<SshDockerContainer[]>("ssh_admin_docker_containers", { sessionId });
}

export function runSshDockerAction(sessionId: string, containerId: string, action: string) {
  return invokeBackend("ssh_admin_docker_action", { sessionId, containerId, action });
}

export function loadSshDockerLogs(sessionId: string, containerId: string, lines = 200) {
  return invokeBackend<string>("ssh_admin_docker_logs", { sessionId, containerId, lines });
}
