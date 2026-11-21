export type SshAuthType = "password" | "key" | "agent" | "keyboard-interactive";

export type FileTransferProtocol = "sftp" | "ftp" | "ftps-explicit";

export interface SshFileTransferConfig {
  enabled: boolean;
  protocol: FileTransferProtocol;
  host: string;
  port: number;
  username: string;
  viaCurrentSsh: boolean;
  tlsServerName?: string;
}

export type SshAlgorithmPolicy = "modern" | "legacy";

export interface SshKeepAlivePolicy {
  intervalSeconds: number;
  maxMisses: number;
}

export interface SshReconnectPolicy {
  enabled: boolean;
  maxAttempts: number;
  baseDelayMs: number;
  maxDelayMs: number;
}

export interface SshConfigSource {
  alias: string;
  sourcePath: string;
  rootPath?: string;
  syncedAt: number;
}

export interface SshConfigDiscoveredProfile {
  alias: string;
  host: string;
  port: number;
  username: string;
  identityFile?: string;
  proxyJump?: string;
  serverAliveInterval?: number;
  connectTimeout?: number;
  identitiesOnly?: boolean;
  sourcePath: string;
}

export type SshTerminalStatus =
  | "connecting"
  | "ready"
  | "reconnecting"
  | "credentials"
  | "unavailable"
  | "error";

export type SshAgentStatusState = "ready" | "empty" | "unavailable";

/** Safe-to-display public identity metadata supplied by the local SSH Agent. */
export interface SshAgentIdentity {
  algorithm: string;
  fingerprint: string;
}

/** Availability of the SSH Agent from the current desktop process. */
export interface SshAgentStatus {
  state: SshAgentStatusState;
  identityCount: number;
  identities: SshAgentIdentity[];
}

export interface SshPortForwardRule {
  id: string;
  type: "L" | "R" | "D";
  bindPort: number;
  /** Unused for dynamic SOCKS5 forwarding (type D). */
  targetHost: string;
  /** Unused for dynamic SOCKS5 forwarding (type D). */
  targetPort: number;
  label?: string;
}

export interface SshJumpHostProfile {
  host: string;
  port: number;
  username: string;
  authType: Exclude<SshAuthType, "keyboard-interactive">;
  keyFile?: string;
}

/** 可选 SOCKS5 出站代理；仅作用于第一跳 TCP 拨号，不包含密码。 */
export interface SshSocksProxyProfile {
  host: string;
  port: number;
  username?: string;
}

/** 可安全写入 WebView 本地存储的 SSH 连接资料；不包含密码或私钥口令。 */
export interface SshConnectionProfile {
  id: string;
  name: string;
  host: string;
  port: number;
  username: string;
  authType: SshAuthType;
  keyFile?: string;
  description?: string;
  defaultPath?: string;
  /** Commands executed once after the first successful shell connection, never on reconnect. */
  postConnectCommands?: string[];
  group?: string;
  tags: string[];
  favorite: boolean;
  /** Whether encrypted credentials should be retained across desktop restarts. */
  saveCredentials: boolean;
  portForwards: SshPortForwardRule[];
  jumpHost?: SshJumpHostProfile;
  socksProxy?: SshSocksProxyProfile;
  /** 可安全持久化的文件传输目标；FTP 密码仅存放在加密凭据中。 */
  fileTransfer?: SshFileTransferConfig;
  algorithmPolicy?: SshAlgorithmPolicy;
  keepAlive?: SshKeepAlivePolicy;
  reconnect?: SshReconnectPolicy;
  sshConfigSource?: SshConfigSource;
  createdAt: number;
  updatedAt: number;
}

/** SSH 密码与私钥口令；桌面版可使用加密存储，运行期会话始终只保留内存副本。 */
export interface SshRuntimeCredentials {
  password?: string;
  passphrase?: string;
  jumpPassword?: string;
  jumpPassphrase?: string;
  /** SOCKS5 代理认证密码，仅运行期内存与可选加密存储，不进入 profile 明文。 */
  socksPassword?: string;
  /** FTP/FTPS 目标密码，不得回退复用 SSH 密码。 */
  ftpPassword?: string;
  /** Encrypted at rest; used locally to generate TOTP responses for interactive SSH MFA. */
  totpUri?: string;
}

export interface SshConnectionDraft {
  profile: SshConnectionProfile;
  credentials?: SshRuntimeCredentials;
}

export interface SshMfaPromptItem {
  prompt: string;
  echo: boolean;
}

export interface SshMfaPromptPayload {
  sessionId: string;
  name: string;
  instructions: string;
  prompts: SshMfaPromptItem[];
  /**
   * Transient, in-memory initial values for the MFA dialog. They are never
   * persisted or sent back to the backend until the user confirms a response.
   */
  initialResponses?: string[];
}

export interface ShellOutputEvent {
  sessionId: string;
  data: string;
  /** Total output bytes up to this chunk; only debuggee (`debug-…`) sessions send it. */
  offset?: number;
}

/** 终端输出事件按会话命名，与后端 terminal.shellOutputEventName 保持一致。 */
export function shellOutputEventName(sessionId: string) {
  return `shell-output:${sessionId}`;
}

export interface ShellEndedEvent {
  sessionId: string;
  reason: string;
  recoverable: boolean;
}

export interface ShellRecoveredEvent {
  sessionId: string;
}

export interface SshPortForwardStatus {
  ruleId: string;
  isRunning: boolean;
  errorCode?: "address_in_use" | "invalid_rule" | "start_failed";
  errorMessage?: string;
}

export function createSshProfileId(prefix = "ssh-profile") {
  const suffix =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `${prefix}-${suffix}`;
}

export function sanitizeSshProfile(profile: SshConnectionProfile): SshConnectionProfile {
  const now = Date.now();
  return {
    id: profile.id || createSshProfileId(),
    name: profile.name.trim() || `${profile.username.trim()}@${profile.host.trim()}`,
    host: profile.host.trim(),
    port: normalizePort(profile.port, 22),
    username: profile.username.trim(),
    authType: profile.authType,
    keyFile: profile.keyFile?.trim() || undefined,
    description: profile.description?.trim() || undefined,
    defaultPath: profile.defaultPath?.trim() || undefined,
    postConnectCommands: (profile.postConnectCommands ?? [])
      .map((command) => command.trim())
      .filter(Boolean)
      .slice(0, 32),
    group: profile.group?.trim() || undefined,
    tags: [...new Set((profile.tags ?? []).map((tag) => tag.trim()).filter(Boolean))],
    favorite: Boolean(profile.favorite),
    saveCredentials: profile.saveCredentials !== false,
    portForwards: (profile.portForwards ?? []).map((rule) => ({
      id: rule.id || createSshProfileId("ssh-forward"),
      type: rule.type,
      bindPort: normalizePort(rule.bindPort, 0),
      targetHost: rule.targetHost.trim(),
      targetPort: normalizePort(rule.targetPort, 0),
      label: rule.label?.trim() || undefined,
    })),
    jumpHost: profile.jumpHost
      ? {
          host: profile.jumpHost.host.trim(),
          port: normalizePort(profile.jumpHost.port, 22),
          username: profile.jumpHost.username.trim(),
          authType: profile.jumpHost.authType,
          keyFile: profile.jumpHost.keyFile?.trim() || undefined,
        }
      : undefined,
    // 代理地址为空视为未启用，避免半成品配置被静默忽略。
    socksProxy: profile.socksProxy?.host.trim()
      ? {
          host: profile.socksProxy.host.trim(),
          port: normalizePort(profile.socksProxy.port, 1080),
          username: profile.socksProxy.username?.trim() || undefined,
        }
      : undefined,
    fileTransfer: sanitizeFileTransferConfig(profile),
    algorithmPolicy: profile.algorithmPolicy === "legacy" ? "legacy" : "modern",
    keepAlive: {
      intervalSeconds: normalizeRange(profile.keepAlive?.intervalSeconds, 30, 0, 300),
      maxMisses: normalizeRange(profile.keepAlive?.maxMisses, 3, 1, 10),
    },
    reconnect: {
      enabled: profile.reconnect?.enabled !== false,
      maxAttempts: normalizeRange(profile.reconnect?.maxAttempts, 5, 1, 20),
      baseDelayMs: normalizeRange(profile.reconnect?.baseDelayMs, 1000, 250, 60_000),
      maxDelayMs: normalizeRange(profile.reconnect?.maxDelayMs, 15_000, 1000, 300_000),
    },
    sshConfigSource: profile.sshConfigSource
      ? {
          alias: profile.sshConfigSource.alias.trim(),
          sourcePath: profile.sshConfigSource.sourcePath.trim(),
          rootPath: profile.sshConfigSource.rootPath?.trim() || undefined,
          syncedAt: Number.isFinite(profile.sshConfigSource.syncedAt)
            ? profile.sshConfigSource.syncedAt
            : now,
        }
      : undefined,
    createdAt: Number.isFinite(profile.createdAt) ? profile.createdAt : now,
    updatedAt: now,
  };
}

export function sshConfigSourceKey(sourcePath: string, alias: string) {
  return `${sourcePath}\0${alias}`;
}

export function resolveSshConfigProxyJump(
  rawValue: string | undefined,
  profiles: readonly SshConfigDiscoveredProfile[],
  fallbackUsername: string
): SshJumpHostProfile | undefined {
  const firstHop = rawValue?.split(",", 1)[0]?.trim();
  if (!firstHop || firstHop.toLowerCase() === "none") return undefined;

  const referenced = profiles.find(
    (candidate) => candidate.alias.toLowerCase() === firstHop.toLowerCase()
  );
  if (referenced) {
    return {
      host: referenced.host,
      port: normalizePort(referenced.port, 22),
      username: referenced.username || fallbackUsername,
      authType: referenced.identityFile ? "key" : "agent",
      keyFile: referenced.identityFile,
    };
  }

  const parsed = parseProxyJumpDestination(firstHop);
  if (!parsed.host) return undefined;
  return {
    host: parsed.host,
    port: normalizePort(parsed.port, 22),
    username: parsed.username || fallbackUsername,
    authType: "agent",
  };
}

export function mergeSshConfigProfile(
  discovered: SshConfigDiscoveredProfile,
  allDiscovered: readonly SshConfigDiscoveredProfile[],
  existing: SshConnectionProfile | undefined,
  rootPath: string,
  syncedAt = Date.now()
) {
  const base: SshConnectionProfile = existing ?? {
    id: createSshProfileId(),
    name: discovered.alias,
    host: discovered.host,
    port: discovered.port || 22,
    username: discovered.username,
    authType: discovered.identityFile ? "key" : "agent",
    tags: [],
    favorite: false,
    saveCredentials: false,
    portForwards: [],
    createdAt: syncedAt,
    updatedAt: syncedAt,
  };
  return sanitizeSshProfile({
    ...base,
    name: existing?.name || discovered.alias,
    host: discovered.host,
    port: discovered.port || 22,
    username: discovered.username,
    keyFile: discovered.identityFile || existing?.keyFile,
    authType: discovered.identityFile ? "key" : (existing?.authType ?? "agent"),
    jumpHost: resolveSshConfigProxyJump(
      discovered.proxyJump,
      allDiscovered,
      discovered.username
    ),
    keepAlive: {
      intervalSeconds:
        discovered.serverAliveInterval ?? existing?.keepAlive?.intervalSeconds ?? 30,
      maxMisses: existing?.keepAlive?.maxMisses ?? 3,
    },
    sshConfigSource: {
      alias: discovered.alias,
      sourcePath: discovered.sourcePath,
      rootPath,
      syncedAt,
    },
    updatedAt: syncedAt,
  });
}

export function findOrphanedSshConfigProfiles(
  existingProfiles: readonly SshConnectionProfile[],
  discoveredProfiles: readonly SshConfigDiscoveredProfile[],
  rootPath: string
) {
  const activeKeys = new Set(
    discoveredProfiles.map((profile) => sshConfigSourceKey(profile.sourcePath, profile.alias))
  );
  return existingProfiles.filter((profile) => {
    const source = profile.sshConfigSource;
    if (!source || source.rootPath !== rootPath) return false;
    return !activeKeys.has(sshConfigSourceKey(source.sourcePath, source.alias));
  });
}

function parseProxyJumpDestination(value: string) {
  let destination = value.trim();
  let username = "";
  const at = destination.lastIndexOf("@");
  if (at >= 0) {
    username = destination.slice(0, at);
    destination = destination.slice(at + 1);
  }
  if (destination.startsWith("[")) {
    const end = destination.indexOf("]");
    if (end > 0) {
      const port = destination.slice(end + 1).replace(/^:/, "");
      return { host: destination.slice(1, end), port: Number(port) || 22, username };
    }
  }
  const colon = destination.lastIndexOf(":");
  if (colon > 0 && destination.indexOf(":") === colon) {
    return {
      host: destination.slice(0, colon),
      port: Number(destination.slice(colon + 1)) || 22,
      username,
    };
  }
  return { host: destination, port: 22, username };
}

export function normalizeRange(value: number | undefined, fallback: number, min: number, max: number) {
  if (!Number.isFinite(value)) return fallback;
  return Math.min(max, Math.max(min, Math.round(Number(value))));
}

/**
 * The endpoint file transfer actually connects to: the SSH host for SFTP, the
 * configured FTP/FTPS host (falling back to the SSH host and user) otherwise.
 * Mirrors services/sftp-service.ts connectSftp.
 */
export function fileTransferEndpoint(profile: SshConnectionProfile): {
  protocol: FileTransferProtocol;
  host: string;
  port: number;
  username: string;
} {
  const transfer = profile.fileTransfer;
  const protocol = transfer?.protocol ?? "sftp";
  if (protocol === "sftp") return { protocol, host: profile.host, port: profile.port, username: profile.username };
  return {
    protocol,
    host: transfer?.host || profile.host,
    port: transfer?.port ?? 21,
    username: transfer?.username || profile.username,
  };
}

function sanitizeFileTransferConfig(profile: SshConnectionProfile): SshFileTransferConfig {
  const config = profile.fileTransfer;
  const protocol: FileTransferProtocol =
    config?.protocol === "ftp" || config?.protocol === "ftps-explicit"
      ? config.protocol
      : "sftp";
  const defaultPort = protocol === "sftp" ? profile.port : 21;
  return {
    enabled: config?.enabled !== false,
    protocol,
    host: config?.host.trim() || profile.host.trim(),
    port: normalizePort(config?.port ?? defaultPort, defaultPort),
    username: config?.username.trim() || profile.username.trim(),
    viaCurrentSsh: protocol !== "sftp" && config?.viaCurrentSsh === true,
    tlsServerName:
      protocol === "ftps-explicit" ? config?.tlsServerName?.trim() || undefined : undefined,
  };
}

function normalizePort(value: number, fallback: number) {
  return Number.isInteger(value) && value >= 1 && value <= 65535 ? value : fallback;
}
