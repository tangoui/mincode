export type PluginInstallErrorKind =
  | "noRelease"
  | "noPlatformPackage"
  | "hostTooOld"
  | "hostTooNew"
  | "incompatible"
  | "network"
  | "integrity"
  | "marketData"
  | "signInRequired"
  | "paidHostRequired"
  | "pluginQuota"
  | "unknown";

export interface PluginInstallErrorInfo {
  kind: PluginInstallErrorKind;
  /** Platform such as `darwin/arm64`, only for `noPlatformPackage`. */
  platform?: string;
  /** MinCode version the plugin needs (`hostTooOld`: at least; `hostTooNew`: below), and the running one. */
  version?: string;
  current?: string;
  /** Original backend message without wrapper prefixes, kept as technical detail. */
  detail: string;
}

const rules: { kind: PluginInstallErrorKind; pattern: RegExp }[] = [
  // `cloudcompanion.DownloadMarketArtifact` refuses when no member session is stored, when the
  // stored session is not a member, or when the cloud rejects the member token. All of them mean
  // "sign in and retry", so the caller can route to Settings → Cloud instead of showing an error.
  { kind: "signInRequired", pattern: /请先在设置中登录云账号|当前会话不是云端会员账号|请先登录云账号|云账号会话已失效/i },
  { kind: "paidHostRequired", pattern: /requires a paid MinCode plan|主程序付费套餐/i },
  { kind: "pluginQuota", pattern: /plugin limit reached|plugins are not available on the \S+ plan/i },
  { kind: "noRelease", pattern: /no published release yet/i },
  { kind: "noPlatformPackage", pattern: /no published package for ([\w-]+\/[\w-]+)/i },
  // `selectRelease` when releases exist for this platform but none supports the running MinCode.
  { kind: "hostTooOld", pattern: /requires MinCode (\S+) or later \(this is (\S+)\)/i },
  { kind: "hostTooNew", pattern: /supports MinCode below (\S+) only \(this is (\S+)\)/i },
  { kind: "incompatible", pattern: /cannot be installed here/i },
  { kind: "integrity", pattern: /does not match the published size and SHA-256/i },
  { kind: "network", pattern: /release list request failed|release list HTTP status|dial tcp|timeout|deadline exceeded|connection refused|no such host/i },
  { kind: "marketData", pattern: /invalid release list|release list (does not match|contains)|package manifest does not match the published release/i },
];

const osNames: Record<string, string> = { darwin: "macOS", windows: "Windows", linux: "Linux" };
const archNames: Record<string, string> = { arm64: "ARM64", amd64: "x64", "386": "x86" };

/** Turns a Go `GOOS/GOARCH` pair into a readable platform label. */
export function formatPluginPlatform(platform: string): string {
  const [os = "", arch = ""] = platform.split("/");
  return `${osNames[os] ?? os} ${archNames[arch] ?? arch}`.trim();
}

export interface PluginQuotaErrorInfo {
  plan: string;
  /** Zero when the plan does not allow plugins at all. */
  limit: number;
}

/** Extracts the plan and limit from the backend's enabled-plugin quota error. */
export function parsePluginQuotaError(raw: string): PluginQuotaErrorInfo | undefined {
  const limited = /plugin limit reached: the (\S+) plan allows (\d+) enabled plugins/i.exec(raw);
  if (limited) return { plan: limited[1], limit: Number(limited[2]) };
  const forbidden = /plugins are not available on the (\S+) plan/i.exec(raw);
  return forbidden ? { plan: forbidden[1], limit: 0 } : undefined;
}

/** Maps a raw install-preview/install error from the backend to a user-facing category. */
export function describePluginInstallError(raw: string): PluginInstallErrorInfo {
  const detail = raw.replace(/^(?:RuntimeError|Error):\s*/, "").replace(/^market install:\s*/, "").trim();
  for (const rule of rules) {
    const match = rule.pattern.exec(detail);
    if (!match) continue;
    if (rule.kind === "noPlatformPackage") return { kind: rule.kind, platform: formatPluginPlatform(match[1]), detail };
    if (rule.kind === "hostTooOld" || rule.kind === "hostTooNew") return { kind: rule.kind, version: match[1], current: match[2], detail };
    return { kind: rule.kind, detail };
  }
  return { kind: "unknown", detail };
}
