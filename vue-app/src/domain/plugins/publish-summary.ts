export const pluginPublishStatuses = ["preparing", "pending_confirmation", "awaiting_login", "running", "succeeded", "failed", "rejected", "expired", "canceled"] as const;
export type PluginPublishStatus = typeof pluginPublishStatuses[number];
export const pluginPublishSteps = ["lookup", "draft", "cover", "metadata", "upload", "submit", "done"] as const;
export const pluginPublishFields = ["name", "description", "icon", "cover_url", "homepage", "readme", "category", "tags"] as const;
export type PluginPublishField = typeof pluginPublishFields[number];
export type PluginPublishValue = string | string[] | null;
export interface PluginPublishChange { field: PluginPublishField; old: PluginPublishValue; new: PluginPublishValue }
export interface PluginPublishPackage { os: string; arch: string; file: string; size: number; sha256: string; state: string; percent: number }
export interface PluginPublishTarget { os: string; arch: string }
export interface PluginPublishResult { releaseId: number; reviewStatus: string; developerUrl: string; uploaded: PluginPublishTarget[]; skipped: PluginPublishTarget[] }
export interface PluginPublishRecord {
  id: string; status: PluginPublishStatus; step: typeof pluginPublishSteps[number]; pluginId: string; pluginName: string; version: string;
  releaseDir: string; account: string; newPlugin: boolean; existingDraft: boolean; submit: boolean; releaseNotes: string;
  packages: PluginPublishPackage[]; metadataChanges: PluginPublishChange[]; expiresAt: string;
  result?: PluginPublishResult; errorCode?: string; errorMessage?: string;
}
function object(value: unknown): Record<string, unknown> | null { return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null; }
function text(value: unknown): string { return typeof value === "string" ? value : ""; }
function member<T extends string>(values: readonly T[], value: unknown): value is T { return typeof value === "string" && values.includes(value as T); }
function validTarget(os: unknown, arch: unknown) { return `${text(os)}/${text(arch)}` === "darwin/arm64" || `${text(os)}/${text(arch)}` === "darwin/amd64" || `${text(os)}/${text(arch)}` === "windows/amd64" || `${text(os)}/${text(arch)}` === "linux/amd64"; }
function metadataValue(value: unknown): PluginPublishValue | undefined { return typeof value === "string" || value === null ? value : Array.isArray(value) && value.every(v => typeof v === "string") ? value.slice() : undefined; }
function targets(value: unknown): PluginPublishTarget[] { return Array.isArray(value) ? value.flatMap(v => { const item = object(v); return item && validTarget(item.os, item.arch) ? [{ os: text(item.os), arch: text(item.arch) }] : []; }) : []; }
function publicUrl(value: unknown) { try { const url = new URL(text(value)); return url.protocol === "https:" && !url.username && !url.password ? url.href : ""; } catch { return ""; } }
/** Fixed public schema only: never pass through native objects, unknown fields, tokens or approval flags. */
export function normalizePluginPublishRecord(value: unknown): PluginPublishRecord | null {
  const v = object(value);
  if (!v || !text(v.id) || !member(pluginPublishStatuses, v.status) || !member(pluginPublishSteps, v.step) || !Array.isArray(v.packages) || (v.packages.length < 1 && ["pending_confirmation", "awaiting_login", "running", "succeeded"].includes(v.status)) || v.packages.length > 4 || !Array.isArray(v.metadataChanges)) return null;
  const packages: PluginPublishPackage[] = [];
  for (const raw of v.packages) {
    const p = object(raw);
    if (!p || !validTarget(p.os, p.arch) || typeof p.size !== "number" || !Number.isSafeInteger(p.size) || p.size < 1 || p.size > 64 * 1024 * 1024 || typeof p.sha256 !== "string" || !/^[a-f0-9]{64}$/i.test(p.sha256) || typeof p.percent !== "number" || !Number.isFinite(p.percent) || !member(["pending", "uploading", "uploaded", "skipped"], p.state)) return null;
    if (packages.some(item => item.os === p.os && item.arch === p.arch)) return null;
    packages.push({ os: text(p.os), arch: text(p.arch), file: text(p.file), size: p.size, sha256: p.sha256, state: p.state, percent: Math.max(0, Math.min(100, Math.round(p.percent))) });
  }
  const metadataChanges: PluginPublishChange[] = [];
  for (const raw of v.metadataChanges) {
    const change = object(raw);
    if (!change || !member(pluginPublishFields, change.field)) return null;
    const old = metadataValue(change.old), next = metadataValue(change.new);
    if (old === undefined || next === undefined) return null;
    metadataChanges.push({ field: change.field, old, new: next });
  }
  const result: PluginPublishRecord = {
    id: text(v.id), status: v.status, step: v.step, pluginId: text(v.pluginId), pluginName: text(v.pluginName), version: text(v.version), releaseDir: text(v.releaseDir), account: text(v.account),
    newPlugin: v.newPlugin === true, existingDraft: v.existingDraft === true, submit: v.submit === true, releaseNotes: text(v.releaseNotes), expiresAt: text(v.expiresAt), packages, metadataChanges,
  };
  if (typeof v.errorCode === "string") result.errorCode = v.errorCode;
  if (typeof v.errorMessage === "string") result.errorMessage = v.errorMessage;
  const r = object(v.result);
  if (r && typeof r.releaseId === "number" && Number.isSafeInteger(r.releaseId) && r.releaseId > 0) result.result = { releaseId: r.releaseId, reviewStatus: text(r.reviewStatus), developerUrl: publicUrl(r.developerUrl), uploaded: targets(r.uploaded), skipped: targets(r.skipped) };
  return result;
}
export function releaseNotesExcerpt(value: string, limit = 280): string { const chars = Array.from(value); return chars.length > limit ? chars.slice(0, limit).join("") + "…" : value; }
export function metadataChangeRows(changes: PluginPublishChange[]) { return changes.map(change => {
  const display = (value: PluginPublishValue) => { const text = Array.isArray(value) ? value.join(", ") : value ?? ""; return change.field === "readme" ? releaseNotesExcerpt(text, 200) : text; };
  return { field: change.field, old: display(change.old), new: display(change.new) };
}); }
export function isPluginPublishTerminal(status: PluginPublishStatus) { return status !== "preparing" && status !== "pending_confirmation" && status !== "awaiting_login" && status !== "running"; }
