/**
 * Clickable locations in a test failure's stack trace (Tests panel details).
 * Language-neutral: each line is split into plain text, file references
 * (`some/path/Name.ext:12`, also a bare `Name.ext:12`, optionally after a
 * qualified frame name such as `a.b.Name.method(Name.ext:12)`) and debugger
 * addresses (`scheme://…`, the host's address rule in
 * domain/debug/address-breakpoints.ts). Resolving a reference to a project
 * file is done by the caller (`fileReferenceSuffix` + `matchProjectFile`);
 * anything that does not resolve stays plain text. Never produces HTML.
 */
import { isValidDebuggerAddress } from "@/domain/debug/address-breakpoints";

export type StackSegment =
  | { kind: "text"; text: string }
  | { kind: "file"; text: string; file: string; line: number; qualifier?: string }
  | { kind: "address"; text: string; address: string };

export const STACK_TRACE_LIMITS = {
  /** Lines parsed into segments (the rest stay one plain text line each). */
  linesMax: 2000,
  /** Characters of one line that are scanned. */
  lineCharsMax: 2000,
} as const;

const ADDRESS = /[a-zA-Z][a-zA-Z0-9+.-]+:\/\/[^\s()<>"'`]+/g;
const FILE_REF = /((?:[A-Za-z]:[\\/])?(?:[^\s():"'`<>|*?]*[\\/])?[A-Za-z0-9_$][A-Za-z0-9_$.+-]*\.[A-Za-z][A-Za-z0-9]{0,9}):(\d{1,7})(?::\d{1,7})?/g;
const QUALIFIED_FRAME = /([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*){2,})\($/;

interface Match {
  start: number;
  end: number;
  segment: StackSegment;
}

function matchesOf(line: string): Match[] {
  const out: Match[] = [];
  for (const match of line.matchAll(ADDRESS)) {
    const raw = match[0].replace(/[.,;:]+$/, "");
    if (!isValidDebuggerAddress(raw)) continue;
    out.push({ start: match.index, end: match.index + raw.length, segment: { kind: "address", text: raw, address: raw } });
  }
  for (const match of line.matchAll(FILE_REF)) {
    const start = match.index;
    const end = start + match[0].length;
    if (out.some((item) => start < item.end && end > item.start)) continue;
    const lineNumber = Number(match[2]);
    if (!Number.isSafeInteger(lineNumber) || lineNumber < 1) continue;
    const frame = QUALIFIED_FRAME.exec(line.slice(0, start));
    out.push({
      start,
      end,
      segment: { kind: "file", text: match[0], file: match[1]!, line: lineNumber, ...(frame ? { qualifier: frame[1] } : {}) },
    });
  }
  return out.sort((a, b) => a.start - b.start);
}

/** Splits one line into text, file and address segments. */
export function parseStackTraceLine(line: string): StackSegment[] {
  if (line.length > STACK_TRACE_LIMITS.lineCharsMax) return [{ kind: "text", text: line }];
  const segments: StackSegment[] = [];
  let cursor = 0;
  for (const match of matchesOf(line)) {
    if (match.start > cursor) segments.push({ kind: "text", text: line.slice(cursor, match.start) });
    segments.push(match.segment);
    cursor = match.end;
  }
  if (cursor < line.length) segments.push({ kind: "text", text: line.slice(cursor) });
  return segments;
}

/** The trace's lines as segments (lines past linesMax are plain text). */
export function parseStackTrace(text: string): StackSegment[][] {
  return text.split(/\r?\n/).map((line, index) =>
    index < STACK_TRACE_LIMITS.linesMax ? parseStackTraceLine(line) : [{ kind: "text" as const, text: line }],
  );
}

/**
 * The project-relative path suffix a file reference stands for, or undefined
 * when it cannot be a project file: an absolute path outside projectRoot, or
 * one with `..`. A bare name after a qualified frame name gets the
 * qualifier's leading parts as directories (`a.b.Name.method` + `Name.ext` →
 * `a/b/Name.ext`).
 */
export function fileReferenceSuffix(segment: Extract<StackSegment, { kind: "file" }>, projectRoot: string): string | undefined {
  const file = segment.file.replace(/\\/g, "/");
  const root = projectRoot.replace(/\\/g, "/").replace(/\/+$/, "");
  let relative: string;
  if (file.startsWith("/") || /^[A-Za-z]:\//.test(file)) {
    if (!root || !file.startsWith(`${root}/`)) return undefined;
    relative = file.slice(root.length + 1);
  } else {
    relative = file.replace(/^(\.\/)+/, "");
  }
  const parts = relative.split("/");
  if (!relative || parts.some((part) => part === "" || part === "." || part === "..")) return undefined;
  if (parts.length === 1 && segment.qualifier) {
    const dirs = segment.qualifier.split(".").slice(0, -2);
    if (dirs.length) return `${dirs.join("/")}/${relative}`;
  }
  return relative;
}

/**
 * The one candidate project path (project-relative, `/`-separated) that is
 * suffix or ends with `/suffix`; undefined when none or several match.
 */
export function matchProjectFile(suffix: string, candidates: Iterable<string>, caseInsensitive = false): string | undefined {
  const norm = (value: string) => (caseInsensitive ? value.toLowerCase() : value);
  const wanted = norm(suffix);
  let found: string | undefined;
  for (const candidate of candidates) {
    const path = norm(candidate);
    if (path !== wanted && !path.endsWith(`/${wanted}`)) continue;
    if (found !== undefined && found !== candidate) return undefined;
    found = candidate;
  }
  return found;
}

/** The file name of a reference (for an index search). */
export function fileReferenceName(segment: Extract<StackSegment, { kind: "file" }>): string {
  return segment.file.replace(/\\/g, "/").split("/").pop() ?? segment.file;
}
