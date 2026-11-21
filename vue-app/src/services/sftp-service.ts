import {
  fileTransferEndpoint,
  type FileTransferProtocol,
  type SshConnectionProfile,
  type SshRuntimeCredentials,
} from "@/domain/ssh/types";
import type { SftpConflictStrategy, SftpTransferDirection } from "@/domain/sftp/transfer-queue";
import {
  chooseNativeDirectory,
  invokeBackend,
  listenAppEvent,
} from "@/services/desktop-bridge";

export interface SftpEntry {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
  modified?: number;
  permissions: string;
}
export interface LocalFileEntry {
  name: string;
  path: string;
  isDir: boolean;
  size?: number;
}

export type SftpTransferBackendStatus =
  | "queued"
  | "running"
  | "completed"
  | "failed"
  | "cancelled";

export interface SftpTransferSnapshot {
  jobId: string;
  sessionId: string;
  direction: SftpTransferDirection;
  status: SftpTransferBackendStatus;
  bytesTransferred: number;
  totalBytes: number;
  speedBytesPerSecond: number;
  resumedFrom: number;
  error?: string;
  startedAt?: number;
  finishedAt?: number;
}

export interface SftpTransferStartRequest {
  jobId: string;
  sessionId: string;
  direction: SftpTransferDirection;
  localPath: string;
  remotePath: string;
  resume?: boolean;
  conflictStrategy?: SftpConflictStrategy;
}

interface SftpConnectPayload {
  sessionId: string;
  protocol: FileTransferProtocol;
  host: string;
  port: number;
  username: string;
  authType?: SshConnectionProfile["authType"];
  password?: string;
  ftpPassword?: string;
  tlsServerName?: string;
  keyFile?: string;
  passphrase?: string;
  algorithmPolicy?: SshConnectionProfile["algorithmPolicy"];
  socksProxy?: {
    host: string;
    port: number;
    username?: string;
    password?: string;
  };
  bastion?: SftpConnectPayload;
}

function sshPayload(
  sessionId: string,
  profile: SshConnectionProfile,
  credentials: SshRuntimeCredentials
): SftpConnectPayload {
  return {
    sessionId,
    protocol: "sftp",
    host: profile.host,
    port: profile.port,
    username: profile.username,
    authType: profile.authType,
    password: credentials.password,
    keyFile: profile.keyFile,
    passphrase: credentials.passphrase,
    algorithmPolicy: profile.algorithmPolicy ?? "modern",
    socksProxy: profile.socksProxy
      ? {
          host: profile.socksProxy.host,
          port: profile.socksProxy.port,
          username: profile.socksProxy.username,
          password: credentials.socksPassword,
        }
      : undefined,
  };
}

export async function connectSftp(
  sessionId: string,
  profile: SshConnectionProfile,
  credentials: SshRuntimeCredentials = {}
) {
  const fileTransfer = profile.fileTransfer;
  const endpoint = fileTransferEndpoint(profile);
  const protocol = endpoint.protocol;
  const payload: SftpConnectPayload =
    protocol === "sftp"
      ? sshPayload(sessionId, profile, credentials)
      : {
          sessionId,
          protocol,
          host: endpoint.host,
          port: endpoint.port,
          username: endpoint.username,
          // 始终显式发送独立 FTP 密码字段，避免后端兼容逻辑回退到 SSH password。
          ftpPassword: credentials.ftpPassword ?? "",
          tlsServerName:
            protocol === "ftps-explicit" ? fileTransfer?.tlsServerName : undefined,
          bastion: fileTransfer?.viaCurrentSsh
            ? sshPayload(`${sessionId}-bastion`, profile, credentials)
            : undefined,
        };
  await invokeBackend("sftp_connect", { payload });
  return invokeBackend<string>("sftp_get_home", { sessionId });
}
export function disconnectSftp(sessionId: string) {
  return invokeBackend("sftp_disconnect", { sessionId });
}
export function listSftpDirectory(sessionId: string, path: string) {
  return invokeBackend<SftpEntry[]>("sftp_list_dir", { sessionId, path });
}
export function readSftpTextFile(sessionId: string, path: string) {
  return invokeBackend<string>("sftp_read_text_file", { sessionId, path });
}

const imageMimeTypes = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);
const maxImagePreviewBytes = 4 * 1024 * 1024;

export function isSftpImageFile(name: string) {
  return /\.(?:png|jpe?g|gif|webp)$/i.test(name);
}

export async function readSftpImagePreview(sessionId: string, path: string): Promise<string> {
  const result = await invokeBackend<unknown>("sftp_read_image_preview", { sessionId, path });
  if (!result || typeof result !== "object" || !("mimeType" in result) || !("data" in result)) {
    throw new Error("Invalid SFTP image preview response");
  }
  const { mimeType, data } = result;
  if (typeof mimeType !== "string" || !imageMimeTypes.has(mimeType) ||
      typeof data !== "string" || !data || data.length > Math.ceil(maxImagePreviewBytes / 3) * 4 ||
      !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(data)) {
    throw new Error("Invalid or oversized SFTP image preview");
  }
  const padding = data.endsWith("==") ? 2 : data.endsWith("=") ? 1 : 0;
  if (data.length / 4 * 3 - padding > maxImagePreviewBytes) {
    throw new Error("Oversized SFTP image preview");
  }
  return `data:${mimeType};base64,${data}`;
}
export function writeSftpTextFile(sessionId: string, path: string, content: string) {
  return invokeBackend("sftp_write_text_file", { sessionId, path, content });
}
export function createSftpDirectory(sessionId: string, path: string) {
  return invokeBackend("sftp_mkdirs", { sessionId, path });
}
export function deleteSftpEntry(sessionId: string, path: string, isDir: boolean) {
  return invokeBackend("sftp_delete", { sessionId, path, isDir });
}
export function renameSftpEntry(sessionId: string, oldPath: string, newPath: string) {
  return invokeBackend("sftp_rename", { sessionId, oldPath, newPath });
}
export function downloadSftpFile(sessionId: string, remotePath: string, localPath?: string) {
  return invokeBackend<string>("sftp_download", { sessionId, remotePath, localPath });
}
export function uploadSftpFile(sessionId: string, localPath: string, remotePath: string) {
  return invokeBackend("sftp_upload_file", { sessionId, localPath, remotePath });
}
export function chooseLocalSftpDirectory(title: string) {
  return chooseNativeDirectory(title);
}
export function listLocalFiles(projectRoot: string, dirPath: string) {
  return invokeBackend<LocalFileEntry[]>("local_fs_list_dir", { projectRoot, dirPath });
}
export function deleteLocalFileEntry(projectRoot: string, path: string) {
  return invokeBackend("local_fs_delete", { projectRoot, path });
}
export function renameLocalFileEntry(projectRoot: string, oldPath: string, newName: string) {
  return invokeBackend("local_fs_rename", { projectRoot, oldPath, newName });
}
export function moveLocalFileEntry(projectRoot: string, srcPath: string, destDir: string) {
  return invokeBackend("local_fs_move_to", { projectRoot, srcPath, destDir });
}

export function resolveSftpInitialPath(home: string, configuredPath?: string) {
  const value = configuredPath?.trim();
  if (!value || value === "~") return home;
  if (value.startsWith("~/")) return `${home.replace(/\/$/, "")}/${value.slice(2)}`;
  if (value.startsWith("/")) return value;
  return `${home.replace(/\/$/, "")}/${value}`;
}

export function cancelSftpTransfer(jobId: string, sessionId: string) {
  return invokeBackend("sftp_transfer_cancel", { jobId, sessionId });
}

export function listSftpTransfers(sessionId: string) {
  return invokeBackend<SftpTransferSnapshot[]>("sftp_transfer_list", { sessionId });
}

export async function runSftpTransfer(
  request: SftpTransferStartRequest,
  onProgress: (snapshot: SftpTransferSnapshot) => void
) {
  let settled = false;
  let resolveEnded!: (snapshot: SftpTransferSnapshot) => void;
  const ended = new Promise<SftpTransferSnapshot>((resolve) => {
    resolveEnded = resolve;
  });
  const stopProgress = listenAppEvent<SftpTransferSnapshot>(
    "sftp-transfer-progress",
    (snapshot) => {
      if (snapshot.jobId === request.jobId) onProgress(snapshot);
    }
  );
  const stopEnded = listenAppEvent<SftpTransferSnapshot>("sftp-transfer-ended", (snapshot) => {
    if (snapshot.jobId !== request.jobId || settled) return;
    settled = true;
    onProgress(snapshot);
    resolveEnded(snapshot);
  });
  try {
    const initial = await invokeBackend<SftpTransferSnapshot>("sftp_transfer_start", {
      ...request,
      resume: request.resume ?? true,
    });
    onProgress(initial);
    return await ended;
  } finally {
    settled = true;
    stopProgress();
    stopEnded();
  }
}
