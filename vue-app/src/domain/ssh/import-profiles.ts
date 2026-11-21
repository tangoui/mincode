import {
  createSshProfileId,
  sanitizeSshProfile,
  type FileTransferProtocol,
  type SshAuthType,
  type SshConnectionProfile,
  type SshJumpHostProfile,
  type SshPortForwardRule,
  type SshRuntimeCredentials,
} from "./types.ts";
import { parseSshCredentials } from "./profile-portability.ts";

const SSH_AUTH_TYPES: ReadonlySet<SshAuthType> = new Set([
  "password",
  "key",
  "agent",
  "keyboard-interactive",
]);
const FILE_TRANSFER_PROTOCOLS: ReadonlySet<FileTransferProtocol> = new Set([
  "sftp",
  "ftp",
  "ftps-explicit",
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function nonEmptyString(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function importPostConnectCommands(value: unknown) {
  if (!Array.isArray(value)) return [];
  return value
    .filter((command): command is string => typeof command === "string")
    .map((command) => command.trim())
    .filter(Boolean)
    .slice(0, 32);
}

function importTags(value: unknown) {
  const raw = Array.isArray(value) ? value : typeof value === "string" ? value.split(",") : [];
  return [
    ...new Set(
      raw
        .filter((tag): tag is string => typeof tag === "string" && tag.trim().length > 0)
        .map((tag) => tag.trim())
    ),
  ];
}

function importPort(value: unknown, fallback: number) {
  const numberValue =
    typeof value === "number" ? value : typeof value === "string" ? Number(value.trim()) : NaN;
  return Number.isInteger(numberValue) && numberValue >= 1 && numberValue <= 65_535
    ? numberValue
    : fallback;
}

function importAuthType(value: unknown, keyFile?: string): SshAuthType {
  if (typeof value === "string") {
    const normalized = value.trim().toLowerCase();
    if (SSH_AUTH_TYPES.has(normalized as SshAuthType)) return normalized as SshAuthType;
  }
  return keyFile ? "key" : "agent";
}

function importPortForwards(value: unknown): SshPortForwardRule[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item): SshPortForwardRule[] => {
    if (!isRecord(item) || (item.type !== "L" && item.type !== "R" && item.type !== "D")) return [];
    const dynamic = item.type === "D";
    const targetHost = dynamic ? "" : nonEmptyString(item.targetHost);
    const bindPort = importPort(item.bindPort, 0);
    const targetPort = dynamic ? 0 : importPort(item.targetPort, 0);
    if (!bindPort || (!dynamic && (!targetHost || !targetPort))) return [];
    return [
      {
        id: nonEmptyString(item.id) ?? createSshProfileId("ssh-forward"),
        type: item.type,
        bindPort,
        targetHost: targetHost ?? "",
        targetPort,
        label: nonEmptyString(item.label),
      },
    ];
  });
}

function importFileTransfer(
  value: unknown,
  profile: { host: string; port: number; username: string }
): SshConnectionProfile["fileTransfer"] {
  if (!isRecord(value)) {
    return {
      enabled: true,
      protocol: "sftp",
      host: profile.host,
      port: profile.port,
      username: profile.username,
      viaCurrentSsh: false,
    };
  }
  const protocol =
    typeof value.protocol === "string" &&
    FILE_TRANSFER_PROTOCOLS.has(value.protocol.trim().toLowerCase() as FileTransferProtocol)
      ? (value.protocol.trim().toLowerCase() as FileTransferProtocol)
      : "sftp";
  const host = nonEmptyString(value.host) ?? profile.host;
  const username = nonEmptyString(value.username) ?? profile.username;
  return {
    enabled: value.enabled !== false,
    protocol,
    host,
    port: importPort(value.port, protocol === "sftp" ? profile.port : 21),
    username,
    viaCurrentSsh: protocol !== "sftp" && value.viaCurrentSsh === true,
    tlsServerName:
      protocol === "ftps-explicit" ? nonEmptyString(value.tlsServerName) : undefined,
  };
}

function importJumpHost(value: unknown): SshJumpHostProfile | undefined {
  if (!isRecord(value)) return undefined;
  const host = nonEmptyString(value.host);
  const username = nonEmptyString(value.username);
  if (!host || !username) return undefined;
  const keyFile = nonEmptyString(value.keyFile);
  const authType = importAuthType(value.authType, keyFile);
  return {
    host,
    port: importPort(value.port, 22),
    username,
    authType: authType === "keyboard-interactive" ? "agent" : authType,
    keyFile,
  };
}

function importedRecords(value: unknown): unknown[] {
  if (!isRecord(value) || value.version !== 1 || !Array.isArray(value.profiles)) return [];
  return value.profiles;
}

function importProfile(value: unknown, now: number): SshConnectionProfile | null {
  if (!isRecord(value)) return null;
  const host = nonEmptyString(value.host);
  const username = nonEmptyString(value.username);
  if (!host || !username) return null;

  const keyFile = nonEmptyString(value.keyFile);
  const name = nonEmptyString(value.name) ?? `${username}@${host}`;
  const port = importPort(value.port, 22);
  const createdAt =
    typeof value.createdAt === "number" && Number.isFinite(value.createdAt) ? value.createdAt : now;
  const updatedAt =
    typeof value.updatedAt === "number" && Number.isFinite(value.updatedAt) ? value.updatedAt : now;

  return sanitizeSshProfile({
    id: nonEmptyString(value.id) ?? createSshProfileId(),
    name,
    host,
    port,
    username,
    authType: importAuthType(value.authType, keyFile),
    keyFile,
    description: nonEmptyString(value.description),
    defaultPath: nonEmptyString(value.defaultPath),
    postConnectCommands: importPostConnectCommands(value.postConnectCommands),
    group: nonEmptyString(value.group),
    tags: importTags(value.tags),
    favorite: value.favorite === true,
    saveCredentials: value.saveCredentials !== false,
    portForwards: importPortForwards(value.portForwards),
    jumpHost: importJumpHost(value.jumpHost),
    fileTransfer: importFileTransfer(value.fileTransfer, { host, port, username }),
    createdAt,
    updatedAt,
  });
}

export interface ImportedSshProfile {
  profile: SshConnectionProfile;
  /** 仅当导出文件带着明文口令时存在；导入方需要单独写入加密凭据存储。 */
  credentials?: SshRuntimeCredentials;
}

export function parseSshProfileImportBundle(
  value: unknown,
  now = Date.now()
): ImportedSshProfile[] {
  const seen = new Set<string>();
  return importedRecords(value).flatMap((item) => {
    const profile = importProfile(item, now);
    if (!profile) return [];
    const fingerprint = `${profile.host}:${profile.port}:${profile.username}:${profile.name}`;
    if (seen.has(fingerprint)) return [];
    seen.add(fingerprint);
    const credentials = isRecord(item) ? parseSshCredentials(item.credentials) : undefined;
    return [credentials ? { profile, credentials } : { profile }];
  });
}

export function parseSshProfileImport(value: unknown, now = Date.now()): SshConnectionProfile[] {
  return parseSshProfileImportBundle(value, now).map((entry) => entry.profile);
}
