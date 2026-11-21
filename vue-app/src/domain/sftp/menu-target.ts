/**
 * Targets of the remote-row menus in the SFTP views: the sftp/file/context
 * plugin menu and the sftpFile service surface ("Process with plugin").
 * The connection is the actual transfer endpoint (fileTransferEndpoint),
 * captured by the view when its session connects (sftpMenuConnection), so a
 * profile edited later never pairs a row with another endpoint. The
 * whitelisted passContext is built from it by domain/plugins/menu-targets.ts,
 * so credentials, key paths and proxy or jump-host details never get here.
 */
import type { SftpFileMenuTarget } from "@/domain/plugins/menu-targets";
import type { SftpFileServiceTarget } from "@/domain/plugins/service-surfaces";
import { fileTransferEndpoint, type SshConnectionProfile } from "@/domain/ssh/types";

export interface SftpMenuEntry {
  name: string;
  path: string;
  isDir: boolean;
  size?: number;
  /** ls-style mode string from sftp_list (Go formatPermissions): "l…" symbolic link, "d…" directory, "-…" other. */
  permissions?: string;
}

export type SftpMenuConnection = SftpFileMenuTarget["connection"];

/** The endpoint a session is opened with; record it when connecting. */
export function sftpMenuConnection(profile: SshConnectionProfile): SftpMenuConnection {
  const { protocol, host, port, username } = fileTransferEndpoint(profile);
  return { id: profile.id, name: profile.name, protocol, host, port, username };
}

export function sftpFileMenuTarget(connection: SftpMenuConnection, entry: SftpMenuEntry): SftpFileMenuTarget {
  return {
    connection: { ...connection },
    path: entry.path,
    name: entry.name,
    kind: entry.isDir ? "directory" : "file",
    ...(entry.isDir || typeof entry.size !== "number" ? {} : { size: entry.size }),
  };
}

/**
 * The sftpFile service target of a remote row, read through the open session
 * `sessionId`; null for directories, symbolic links (the host hands over
 * regular files only) and paths with NUL, CR or LF (the host refuses those
 * remote paths, so the entry would only fail).
 */
export function sftpFileServiceTarget(sessionId: string, connection: SftpMenuConnection, entry: SftpMenuEntry): SftpFileServiceTarget | null {
  if (!sessionId || entry.isDir || entry.permissions?.startsWith("l") || /[\0\r\n]/.test(entry.path)) return null;
  return {
    surface: "sftpFile",
    sessionId,
    file: { name: entry.name, size: entry.size ?? 0 },
    remote: sftpFileMenuTarget(connection, entry),
  };
}
