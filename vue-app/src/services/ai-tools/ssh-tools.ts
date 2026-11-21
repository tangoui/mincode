import type { AiCapability } from "@/domain/ai/capability-registry";
import type { AiResource } from "@/domain/ai/resource-registry";
import type { SshRuntimeCredentials } from "@/domain/ssh/types";
import type { NativeAiSshProfile } from "../ai-native-tools";
import type { TerminalAiDependencies } from "./terminal-tools";
import { sanitizeAiResult } from "./shared";

export interface SshAiDependencies extends TerminalAiDependencies {
  sshProfile?(profileId: string): NativeAiSshProfile | undefined;
  sshCredentials?(profileId: string): SshRuntimeCredentials | undefined;
}

function text(input: Record<string, unknown>, key: string) {
  return typeof input[key] === "string" ? input[key].trim() : "";
}

function sanitizeSshResult(value: unknown): unknown {
  const sanitized = sanitizeAiResult(value);
  if (sanitized === null || typeof sanitized !== "object") return sanitized;
  if (Array.isArray(sanitized)) return sanitized.map(sanitizeSshResult);
  return Object.fromEntries(
    Object.entries(sanitized).map(([key, item]) =>
      ["path", "sourcePath", "identityFile"].includes(key)
        ? [key, "[REDACTED]"]
        : [key, sanitizeSshResult(item)]
    )
  );
}

function metadataString(resource: AiResource, key: string) {
  const value = resource.metadata?.[key];
  return typeof value === "string" ? value : "";
}

function serverPayload(resource: AiResource, dependencies: SshAiDependencies) {
  const profileId = metadataString(resource, "profileId");
  const saved = dependencies.sshProfile?.(profileId);
  if (!saved || !profileId) throw new Error("SSH profile resource is invalid");
  const credentials = dependencies.sshCredentials?.(profileId);
  return {
    id: saved.id,
    host: saved.host,
    port: saved.port,
    username: saved.username,
    authType: saved.authType,
    password: credentials?.password ?? null,
    keyFile: saved.keyFile ?? null,
    passphrase: credentials?.passphrase ?? null,
    totpUri: credentials?.totpUri ?? null,
    jumpHost: saved.jumpHost
      ? {
          ...saved.jumpHost,
          password: credentials?.jumpPassword ?? null,
          passphrase: credentials?.jumpPassphrase ?? null,
        }
      : null,
    socksProxy: saved.socksProxy
      ? {
          host: saved.socksProxy.host,
          port: saved.socksProxy.port,
          username: saved.socksProxy.username ?? null,
          password: credentials?.socksPassword ?? null,
        }
      : null,
  };
}

/** 服务器管理系列后端读的是平铺参数，和端口转发那批 payload 包装不是一套。 */
function adminArgs(resource: AiResource, extra: Record<string, unknown> = {}) {
  return { sessionId: metadataString(resource, "sessionId"), ...extra };
}

function count(input: Record<string, unknown>, key: string) {
  return typeof input[key] === "number" ? { [key]: input[key] } : {};
}

const targetProperties = {
  resourceId: { type: "string", description: "Stable SSH resource id." },
  name: { type: "string", description: "Unique SSH resource name." },
};

function schema(properties: Record<string, unknown>, required: string[] = []) {
  return {
    type: "object",
    properties: { ...targetProperties, ...properties },
    ...(required.length ? { required } : {}),
    additionalProperties: false,
  };
}

export const sshProfileCapabilityNames = ["ssh.run_command"];
export const sshSessionCapabilityNames = [
  "ssh.reconnect",
  "ssh.list_port_forwards",
  "ssh.start_port_forward",
  "ssh.stop_port_forward",
  "ssh.server_snapshot",
  "ssh.service_logs",
  "ssh.list_processes",
  "ssh.signal_process",
  "ssh.list_services",
  "ssh.service_action",
  "ssh.docker_info",
  "ssh.list_containers",
  "ssh.container_action",
  "ssh.container_logs",
];

export function sshCapabilities(dependencies: SshAiDependencies): AiCapability[] {
  return [
    {
      module: "ssh", discovery: true, target: "none", mode: "read", risk: "read",
      tool: { name: "ssh.list_connections", description: "List saved SSH profiles without credentials.", inputSchema: { type: "object", properties: {}, additionalProperties: false } },
      execute: () => dependencies.listResources("ssh.run_command"),
    },
    {
      module: "ssh", target: "none", mode: "read", risk: "read",
      tool: { name: "ssh.list_known_hosts", description: "List the local OpenSSH known_hosts entries with their host patterns, key algorithm and fingerprint.", inputSchema: { type: "object", properties: {}, additionalProperties: false } },
      execute: async () => sanitizeSshResult(await dependencies.invoke("ssh_known_hosts_list", {})),
    },
    {
      module: "ssh", target: "none", mode: "write", risk: "dangerous",
      tool: { name: "ssh.remove_known_host", description: "Delete one entry from the local OpenSSH known_hosts file. Removing a host key disables its man-in-the-middle protection until the key is trusted again.", inputSchema: { type: "object", properties: { id: { type: "string", description: "Entry id from ssh.list_known_hosts." } }, required: ["id"], additionalProperties: false } },
      approvalPreview: ({ input }) => `Remove known_hosts entry ${text(input, "id")}`,
      execute: ({ input }) => dependencies.invoke("ssh_known_hosts_remove", { id: text(input, "id") }),
    },
    {
      module: "ssh", target: "none", mode: "read", risk: "read",
      tool: { name: "ssh.discover_config", description: "Read the default local OpenSSH config and return the host profiles it declares. Config location and identity-file paths stay local.", inputSchema: { type: "object", properties: {}, additionalProperties: false } },
      execute: async () => sanitizeSshResult(await dependencies.invoke("ssh_config_discover", {})),
    },
    {
      module: "ssh", mode: "write", risk: "network",
      tool: { name: "ssh.reconnect", description: "Recover a live SSH shell that is in its backend reconnect grace period.", inputSchema: schema({}) },
      execute: ({ resource }) => dependencies.invoke("shell_ssh_try_recover", { payload: { sessionId: metadataString(resource, "sessionId") } }),
    },
    {
      module: "ssh", mode: "write", risk: "dangerous",
      tool: { name: "ssh.run_command", description: "Run one command through a saved SSH profile using internally resolved credentials.", inputSchema: schema({ command: { type: "string" } }, ["command"]) },
      approvalPreview: ({ input }) => `${text(input, "resourceId") || text(input, "name") || "SSH"}: ${text(input, "command")}`,
      execute: ({ input, resource }) => dependencies.invoke("ssh_batch_exec", { payload: { servers: [serverPayload(resource, dependencies)], command: text(input, "command") } }),
    },
    {
      module: "ssh", mode: "read", risk: "read",
      tool: { name: "ssh.list_port_forwards", description: "List running port forwards for a live SSH shell.", inputSchema: schema({}) },
      execute: ({ resource }) => dependencies.invoke("list_port_forwards", { payload: { sessionId: metadataString(resource, "sessionId") } }),
    },
    {
      module: "ssh", mode: "write", risk: "dangerous",
      tool: { name: "ssh.start_port_forward", description: "Start a port forward on a live SSH shell.", inputSchema: schema({ ruleId: { type: "string" }, type: { type: "string", enum: ["L", "R"] }, bindPort: { type: "integer", minimum: 1, maximum: 65535 }, targetHost: { type: "string" }, targetPort: { type: "integer", minimum: 1, maximum: 65535 }, label: { type: "string" } }, ["ruleId", "type", "bindPort", "targetHost", "targetPort"]) },
      approvalPreview: ({ input }) =>
        `${text(input, "type")} ${String(input.bindPort)} -> ${text(input, "targetHost")}:${String(input.targetPort)}`,
      execute: ({ input, resource }) => dependencies.invoke("resume_port_forward", { payload: { sessionId: metadataString(resource, "sessionId"), rule: { id: text(input, "ruleId"), type: input.type, bindPort: input.bindPort, targetHost: text(input, "targetHost"), targetPort: input.targetPort, ...(text(input, "label") ? { label: text(input, "label") } : {}) } } }),
    },
    {
      module: "ssh", mode: "write", risk: "write",
      tool: { name: "ssh.stop_port_forward", description: "Stop a running SSH port forward.", inputSchema: schema({ ruleId: { type: "string" } }, ["ruleId"]) },
      execute: ({ input, resource }) => dependencies.invoke("stop_port_forward", { payload: { sessionId: metadataString(resource, "sessionId"), ruleId: text(input, "ruleId") } }),
    },
    {
      module: "ssh", mode: "read", risk: "read",
      tool: { name: "ssh.server_snapshot", description: "Read CPU, memory, disk, load and uptime of the Linux server behind a live SSH shell.", inputSchema: schema({}) },
      execute: async ({ resource }) => sanitizeSshResult(await dependencies.invoke("ssh_admin_snapshot", adminArgs(resource))),
    },
    {
      module: "ssh", mode: "read", risk: "read",
      tool: { name: "ssh.service_logs", description: "Read journalctl output on the Linux server, optionally scoped to one systemd unit.", inputSchema: schema({ unit: { type: "string", description: "Systemd unit name ending in .service; omit for the whole journal." }, lines: { type: "integer", minimum: 1, maximum: 2000 } }) },
      execute: async ({ input, resource }) =>
        sanitizeSshResult(await dependencies.invoke("ssh_admin_logs", adminArgs(resource, { unit: text(input, "unit"), ...count(input, "lines") }))),
    },
    {
      module: "ssh", mode: "read", risk: "read",
      tool: { name: "ssh.list_processes", description: "List the busiest processes on the Linux server, sorted by CPU usage.", inputSchema: schema({ limit: { type: "integer", minimum: 1, maximum: 500 } }) },
      execute: ({ input, resource }) => dependencies.invoke("ssh_admin_processes", adminArgs(resource, count(input, "limit"))),
    },
    {
      module: "ssh", mode: "write", risk: "dangerous",
      tool: { name: "ssh.signal_process", description: "Send a termination signal to one process on the Linux server.", inputSchema: schema({ pid: { type: "integer", minimum: 2 }, signal: { type: "string", enum: ["TERM", "KILL", "HUP", "INT"] } }, ["pid", "signal"]) },
      approvalPreview: ({ input }) => `kill -${text(input, "signal")} ${String(input.pid)}`,
      execute: ({ input, resource }) =>
        dependencies.invoke("ssh_admin_process_signal", adminArgs(resource, { pid: input.pid, signal: text(input, "signal") })),
    },
    {
      module: "ssh", mode: "read", risk: "read",
      tool: { name: "ssh.list_services", description: "List systemd units on the Linux server with their load, active and sub states.", inputSchema: schema({}) },
      execute: ({ resource }) => dependencies.invoke("ssh_admin_systemd_list", adminArgs(resource)),
    },
    {
      module: "ssh", mode: "write", risk: "dangerous",
      tool: { name: "ssh.service_action", description: "Start, stop, restart or reload a systemd unit on the Linux server.", inputSchema: schema({ unit: { type: "string", description: "Systemd unit name ending in .service." }, action: { type: "string", enum: ["start", "stop", "restart", "reload"] } }, ["unit", "action"]) },
      approvalPreview: ({ input }) => `systemctl ${text(input, "action")} ${text(input, "unit")}`,
      execute: ({ input, resource }) =>
        dependencies.invoke("ssh_admin_systemd_action", adminArgs(resource, { unit: text(input, "unit"), action: text(input, "action") })),
    },
    {
      module: "ssh", mode: "read", risk: "read",
      tool: { name: "ssh.docker_info", description: "Read the Docker daemon version and container counts on the Linux server.", inputSchema: schema({}) },
      execute: ({ resource }) => dependencies.invoke("ssh_admin_docker_info", adminArgs(resource)),
    },
    {
      module: "ssh", mode: "read", risk: "read",
      tool: { name: "ssh.list_containers", description: "List Docker containers on the Linux server, including stopped ones.", inputSchema: schema({}) },
      execute: ({ resource }) => dependencies.invoke("ssh_admin_docker_containers", adminArgs(resource)),
    },
    {
      module: "ssh", mode: "write", risk: "dangerous",
      tool: { name: "ssh.container_action", description: "Start, stop, restart, pause, unpause or remove a Docker container on the Linux server.", inputSchema: schema({ containerId: { type: "string" }, action: { type: "string", enum: ["start", "stop", "restart", "pause", "unpause", "remove"] } }, ["containerId", "action"]) },
      approvalPreview: ({ input }) => `docker ${text(input, "action")} ${text(input, "containerId")}`,
      execute: ({ input, resource }) =>
        dependencies.invoke("ssh_admin_docker_action", adminArgs(resource, { containerId: text(input, "containerId"), action: text(input, "action") })),
    },
    {
      module: "ssh", mode: "read", risk: "read",
      tool: { name: "ssh.container_logs", description: "Read recent stdout and stderr of a Docker container on the Linux server.", inputSchema: schema({ containerId: { type: "string" }, lines: { type: "integer", minimum: 1, maximum: 2000 } }, ["containerId"]) },
      execute: async ({ input, resource }) =>
        sanitizeSshResult(await dependencies.invoke("ssh_admin_docker_logs", adminArgs(resource, { containerId: text(input, "containerId"), ...count(input, "lines")}))),
    },
  ];
}
