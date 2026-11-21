/**
 * Field (DAP data) breakpoints. The adapter names a field with an opaque
 * `dataId` (its `dataBreakpointInfo` answer for a variable of a container);
 * MinCode sends it back verbatim with `setDataBreakpoints`, all of a
 * session's field breakpoints at once. An id the adapter declares
 * `canPersist` is saved per project and debugger type and sent again by the
 * next session (in its start request); any other one lives only in the
 * session it was set in. Id rules mirror the host's start-request check
 * (backend/plugin/debuggers_launch.go debuggerDataBreakpoints):
 * non-blank, at most 1024 characters, no control or bidi characters, at
 * most `dataBreakpointsMax` per start.
 * An adapter may answer a variable it cannot watch without a body. What
 * one adapter answers is measured in its machine smoke test
 * (docs/verification/debugger.md).
 */
import contributionContract from "../plugins/contribution-points.json" with { type: "json" };
import { isUnsafeRune, pairResults } from "./function-breakpoints.ts";
import type { DataBreakpointAccessType, DebugBreakpointListItemResult } from "./types.ts";

/** One field breakpoint: the adapter's id, its description of the field, and when it stops. */
export interface StoredDataBreakpoint {
  dataId: string;
  description: string;
  accessType: DataBreakpointAccessType;
}

/** The adapter's answer for one sent field breakpoint, with what was sent. */
export interface DebugDataBreakpointResult extends StoredDataBreakpoint, DebugBreakpointListItemResult {}

/** A `dataBreakpointInfo` answer; `dataId` null: no field breakpoint can be set there. */
export interface DataBreakpointInfo {
  dataId: string | null;
  description: string;
  accessTypes?: DataBreakpointAccessType[];
  canPersist?: boolean;
}

export const DATA_BREAKPOINT_LIMITS = {
  max: contributionContract.points.debuggers.limits.dataBreakpointsMax,
  /** Go debuggerBreakpointNameRunes (not a contract limit). */
  maxRunes: 1024,
  /** Characters of a description kept (shown in the breakpoints dialog only). */
  maxDescription: 512,
};

const ACCESS_TYPES: readonly DataBreakpointAccessType[] = ["read", "write", "readWrite"];

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isDataBreakpointAccessType(value: unknown): value is DataBreakpointAccessType {
  return typeof value === "string" && (ACCESS_TYPES as readonly string[]).includes(value);
}

/** Whether the host accepts dataId in a start request (it is sent unchanged, never trimmed). */
export function isValidDataId(value: unknown): value is string {
  if (typeof value !== "string" || value.trim() === "") return false;
  let runes = 0;
  for (const char of value) {
    runes += 1;
    if (runes > DATA_BREAKPOINT_LIMITS.maxRunes || isUnsafeRune(char.codePointAt(0)!)) return false;
  }
  return true;
}

/** A one-line description of at most maxDescription characters (the id when the adapter gave none). */
function descriptionText(value: unknown, dataId: string): string {
  const text = typeof value === "string" ? [...value.replace(/\s+/g, " ").trim()].filter((char) => !isUnsafeRune(char.codePointAt(0)!)).join("") : "";
  const chars = [...(text || dataId)];
  return chars.length > DATA_BREAKPOINT_LIMITS.maxDescription ? `${chars.slice(0, DATA_BREAKPOINT_LIMITS.maxDescription - 1).join("")}…` : chars.join("");
}

/** Identity of a field breakpoint: the same field with another access type is another breakpoint. */
export function sameDataBreakpoint(a: Pick<StoredDataBreakpoint, "dataId" | "accessType">, b: Pick<StoredDataBreakpoint, "dataId" | "accessType">): boolean {
  return a.dataId === b.dataId && a.accessType === b.accessType;
}

/**
 * A stored list from untrusted data: null unless it is an array; items with
 * an id the host would refuse or an unknown access type are dropped, so are
 * repeated (dataId, accessType) pairs (the first is kept) and items beyond
 * the host's limit. A missing access type reads as write.
 */
export function storedDataBreakpoints(value: unknown): StoredDataBreakpoint[] | null {
  if (!Array.isArray(value)) return null;
  const list: StoredDataBreakpoint[] = [];
  for (const item of value) {
    if (list.length === DATA_BREAKPOINT_LIMITS.max) break;
    if (!isRecord(item) || !isValidDataId(item.dataId)) continue;
    const accessType = item.accessType === undefined ? "write" : item.accessType;
    if (!isDataBreakpointAccessType(accessType)) continue;
    const breakpoint: StoredDataBreakpoint = { dataId: item.dataId, description: descriptionText(item.description, item.dataId), accessType };
    if (list.some((saved) => sameDataBreakpoint(saved, breakpoint))) continue;
    list.push(breakpoint);
  }
  return list;
}

/**
 * A `dataBreakpointInfo` response body: an id the host would refuse reads as
 * null (no breakpoint there), unknown access types are dropped, and
 * `canPersist` is kept only when it is a boolean.
 */
export function parseDataBreakpointInfo(body: unknown): DataBreakpointInfo {
  // Without an id there is nothing to set, so nothing of the answer is shown.
  if (!isRecord(body) || !isValidDataId(body.dataId)) return { dataId: null, description: "" };
  const info: DataBreakpointInfo = { dataId: body.dataId, description: descriptionText(body.description, body.dataId) };
  if (Array.isArray(body.accessTypes)) info.accessTypes = ACCESS_TYPES.filter((type) => (body.accessTypes as unknown[]).includes(type));
  if (typeof body.canPersist === "boolean") info.canPersist = body.canPersist;
  return info;
}

/**
 * The access types a field's menu offers: write by default (when the
 * adapter lists none, or lists it), read only when the adapter lists it.
 */
export function offeredAccessTypes(info: DataBreakpointInfo): DataBreakpointAccessType[] {
  if (info.dataId === null) return [];
  const listed = info.accessTypes;
  const offered: DataBreakpointAccessType[] = [];
  if (listed === undefined || listed.includes("write")) offered.push("write");
  if (listed?.includes("read")) offered.push("read");
  return offered;
}

/** list with breakpoint added at the end, or without it when it was there. */
export function toggledDataBreakpoints(list: readonly StoredDataBreakpoint[], breakpoint: StoredDataBreakpoint): StoredDataBreakpoint[] {
  return list.some((item) => sameDataBreakpoint(item, breakpoint))
    ? list.filter((item) => !sameDataBreakpoint(item, breakpoint))
    : [...list, { ...breakpoint }];
}

/**
 * What a session sends: the saved breakpoints of its type, then its own
 * (not persistable) ones; a pair in both is sent once; at most the host's limit.
 */
export function sessionDataBreakpointList(
  saved: readonly StoredDataBreakpoint[] | undefined,
  sessionOnly: readonly StoredDataBreakpoint[] | undefined,
): StoredDataBreakpoint[] {
  const list: StoredDataBreakpoint[] = [];
  for (const item of [...(saved ?? []), ...(sessionOnly ?? [])]) {
    if (list.length === DATA_BREAKPOINT_LIMITS.max) break;
    if (!list.some((sent) => sameDataBreakpoint(sent, item))) list.push(item);
  }
  return list;
}

/** The results paired with the breakpoints they were sent for; missing results are unverified. */
export function dataBreakpointResults(
  sent: readonly StoredDataBreakpoint[],
  results: readonly DebugBreakpointListItemResult[],
): DebugDataBreakpointResult[] {
  return pairResults<DebugDataBreakpointResult>(sent.map(({ dataId, description, accessType }) => ({ dataId, description, accessType })), results);
}

/** Whether two lists hold the same breakpoints in the same order (what a session was sent vs. what it should have). */
export function sameDataBreakpointLists(a: readonly Pick<StoredDataBreakpoint, "dataId" | "accessType">[], b: readonly Pick<StoredDataBreakpoint, "dataId" | "accessType">[]): boolean {
  return a.length === b.length && a.every((item, index) => sameDataBreakpoint(item, b[index]!));
}
