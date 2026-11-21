import type { SshConnectionProfile, SshRuntimeCredentials } from "./types.ts";

export const SSH_PROFILE_EXPORT_VERSION = 1;

/** 导出文件中随 profile 一起落盘的明文凭据字段。 */
export const SSH_CREDENTIAL_FIELDS = [
  "password",
  "passphrase",
  "jumpPassword",
  "jumpPassphrase",
  "socksPassword",
  "ftpPassword",
  "totpUri",
] as const;

export interface SshProfileExportEntry extends SshConnectionProfile {
  credentials?: SshRuntimeCredentials;
}

export interface SshProfileExportPayload {
  version: typeof SSH_PROFILE_EXPORT_VERSION;
  exportedAt: string;
  /** 明确标注该文件是否包含明文口令，导入方与人工审阅都能一眼看出。 */
  includesSecrets: boolean;
  profiles: SshProfileExportEntry[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** 从任意来源读出明文凭据；空字段一律丢弃，避免用空串覆盖本机已保存的口令。 */
export function parseSshCredentials(value: unknown): SshRuntimeCredentials | undefined {
  if (!isRecord(value)) return undefined;
  const credentials: SshRuntimeCredentials = {};
  for (const field of SSH_CREDENTIAL_FIELDS) {
    const secret = value[field];
    // 已加密的存量值只在本机可解，跨机器导入没有意义，直接跳过。
    if (typeof secret !== "string" || !secret || secret.startsWith("enc:v2:")) continue;
    credentials[field] = secret;
  }
  return Object.keys(credentials).length ? credentials : undefined;
}

export function buildSshProfileExport(
  profiles: SshConnectionProfile[],
  credentialsFor: (profileId: string) => SshRuntimeCredentials | undefined,
  options: { includeSecrets?: boolean; exportedAt: string }
): SshProfileExportPayload {
  const includeSecrets = options.includeSecrets === true;
  const entries = profiles.map<SshProfileExportEntry>((profile) => {
    if (!includeSecrets) return { ...profile };
    const credentials = parseSshCredentials(credentialsFor(profile.id));
    return credentials ? { ...profile, credentials } : { ...profile };
  });
  return {
    version: SSH_PROFILE_EXPORT_VERSION,
    exportedAt: options.exportedAt,
    includesSecrets: entries.some((entry) => Boolean(entry.credentials)),
    profiles: entries,
  };
}

/** 导入前判断文件里是否真的带着明文口令，用于提示用户。 */
export function sshProfileImportHasSecrets(value: unknown): boolean {
  if (!isRecord(value) || !Array.isArray(value.profiles)) return false;
  return value.profiles.some((profile) =>
    isRecord(profile) ? Boolean(parseSshCredentials(profile.credentials)) : false
  );
}
