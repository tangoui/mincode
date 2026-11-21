export type PluginInstallErrorKind =
  | "hostTooOld"
  | "hostTooNew"
  | "incompatible"
  | "unknown";

export interface PluginInstallErrorInfo {
  kind: PluginInstallErrorKind;
  /** MinCode version the plugin needs (`hostTooOld`: at least; `hostTooNew`: below), and the running one. */
  version?: string;
  current?: string;
  /** Original backend message without wrapper prefixes, kept as technical detail. */
  detail: string;
}

const rules: { kind: PluginInstallErrorKind; pattern: RegExp }[] = [
  { kind: "hostTooOld", pattern: /requires MinCode (\S+) or later \(this is (\S+)\)/i },
  { kind: "hostTooNew", pattern: /supports MinCode below (\S+) only \(this is (\S+)\)/i },
  { kind: "incompatible", pattern: /cannot be installed here/i },
];

/** Maps a raw install-preview/install error from the backend to a user-facing category. */
export function describePluginInstallError(raw: string): PluginInstallErrorInfo {
  const detail = raw.replace(/^(?:RuntimeError|Error):\s*/, "").trim();
  for (const rule of rules) {
    const match = rule.pattern.exec(detail);
    if (!match) continue;
    if (rule.kind === "hostTooOld" || rule.kind === "hostTooNew") return { kind: rule.kind, version: match[1], current: match[2], detail };
    return { kind: rule.kind, detail };
  }
  return { kind: "unknown", detail };
}
