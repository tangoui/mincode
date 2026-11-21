import { MAX_AI_TEXT_ATTACHMENT_BYTES, type AiAttachment } from "./attachments.ts";

export const MAX_AI_CONTEXT_TREE_ENTRIES = 5000;

export interface ProjectContextTreeEntry {
  path: string;
  isDir: boolean;
}

export interface ProjectContextTreeResult {
  entries: ProjectContextTreeEntry[];
  truncated: boolean;
}

export interface ProjectContextFileResult {
  content: string;
  size: number;
  truncated: boolean;
}

export interface ProjectContextAttachmentResult {
  attachment: AiAttachment;
  truncated: boolean;
  originalSize: number;
  attachedSize: number;
  omittedEntries?: number;
}

const textEncoder = new TextEncoder();
const fatalTextDecoder = new TextDecoder("utf-8", { fatal: true });

function createId() {
  return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function normalizeRelativePath(path: string) {
  return path.replace(/\\/g, "/").replace(/^\/+|\/+$/g, "");
}

function normalizeProjectRoot(path: string) {
  const normalized = path.trim().replace(/\\/g, "/");
  if (normalized === "/" || /^[A-Za-z]:\/$/.test(normalized)) return normalized;
  return normalized.replace(/\/+$/g, "");
}

function projectContextName(projectRoot: string, relativePath: string, fallback: string) {
  if (!projectRoot) return relativePath || fallback;
  if (!relativePath) return projectRoot;
  return projectRoot.endsWith("/")
    ? `${projectRoot}${relativePath}`
    : `${projectRoot}/${relativePath}`;
}

function projectRootHeader(projectRoot: string) {
  return projectRoot ? `Project root: ${projectRoot}\n` : "";
}

function byteLength(value: string) {
  return textEncoder.encode(value).byteLength;
}

function decodeUtf8Prefix(bytes: Uint8Array, maxBytes: number) {
  let end = Math.min(bytes.byteLength, Math.max(0, maxBytes));
  while (end > 0) {
    try {
      return fatalTextDecoder.decode(bytes.subarray(0, end));
    } catch {
      end -= 1;
    }
  }
  return "";
}

function decodeUtf8Suffix(bytes: Uint8Array, maxBytes: number) {
  let start = Math.max(0, bytes.byteLength - Math.max(0, maxBytes));
  while (start < bytes.byteLength) {
    try {
      return fatalTextDecoder.decode(bytes.subarray(start));
    } catch {
      start += 1;
    }
  }
  return "";
}

function compactTextToLimit(data: string, marker: string) {
  const bytes = textEncoder.encode(data);
  if (bytes.byteLength <= MAX_AI_TEXT_ATTACHMENT_BYTES) {
    return { data, truncated: false };
  }
  const markerBytes = byteLength(marker);
  const available = Math.max(0, MAX_AI_TEXT_ATTACHMENT_BYTES - markerBytes);
  const headBudget = Math.floor(available / 2);
  const tailBudget = available - headBudget;
  return {
    data: `${decodeUtf8Prefix(bytes, headBudget)}${marker}${decodeUtf8Suffix(bytes, tailBudget)}`,
    truncated: true,
  };
}

function attachment(name: string, data: string): AiAttachment {
  const size = byteLength(data);
  if (size > MAX_AI_TEXT_ATTACHMENT_BYTES) throw new Error("TEXT_ATTACHMENT_TOO_LARGE");
  return {
    id: createId(),
    name,
    mimeType: "text/plain",
    size,
    kind: "text",
    data,
  };
}

export function createProjectFileContext(
  relativePath: string,
  file: ProjectContextFileResult,
  projectRoot = "",
): ProjectContextAttachmentResult {
  const path = normalizeRelativePath(relativePath);
  const root = normalizeProjectRoot(projectRoot);
  const data = `${projectRootHeader(root)}Path: ${path}\n\n${file.content}`;
  const compacted = compactTextToLimit(
    data,
    `\n\n[MinCode: attachment compacted to ${MAX_AI_TEXT_ATTACHMENT_BYTES} bytes]\n\n`,
  );
  const resultAttachment = attachment(projectContextName(root, path, path), compacted.data);
  return {
    attachment: resultAttachment,
    truncated: file.truncated || compacted.truncated,
    originalSize: file.size,
    attachedSize: resultAttachment.size,
  };
}

function directoryScanMarker() {
  return "\n... additional entries omitted after reaching the directory scan limit ...";
}

function directoryCompactionMarker(omittedEntries: number, scanTruncated: boolean) {
  const omitted = omittedEntries > 0 ? `\n... omitted ${omittedEntries} entries to fit the AI attachment limit ...` : "";
  return `${omitted}${scanTruncated ? directoryScanMarker() : ""}`;
}

export function createProjectDirectoryContext(
  relativePath: string,
  tree: ProjectContextTreeResult,
  rootName = ".",
  projectRoot = "",
): ProjectContextAttachmentResult {
  const directory = normalizeRelativePath(relativePath);
  const root = normalizeProjectRoot(projectRoot);
  const label = directory || ".";
  const prefix = directory ? `${directory}/` : "";
  const lines = tree.entries.map((entry) => {
    const normalized = normalizeRelativePath(entry.path);
    const displayPath = prefix && normalized.startsWith(prefix)
      ? normalized.slice(prefix.length)
      : normalized;
    return `${displayPath}${entry.isDir ? "/" : ""}`;
  });
  const header = `${projectRootHeader(root)}Directory: ${label}\n\n`;
  const completeData = `${header}${lines.join("\n")}`;
  const originalSize = byteLength(completeData);
  const completeWithScanMarker = `${completeData}${tree.truncated ? directoryScanMarker() : ""}`;

  if (byteLength(completeWithScanMarker) <= MAX_AI_TEXT_ATTACHMENT_BYTES) {
    const resultAttachment = attachment(
      projectContextName(root, directory, rootName),
      completeWithScanMarker,
    );
    return {
      attachment: resultAttachment,
      truncated: tree.truncated,
      originalSize,
      attachedSize: resultAttachment.size,
      omittedEntries: 0,
    };
  }

  const includedLines: string[] = [];
  let includedBytes = 0;
  const reservedMarkerBytes = byteLength(directoryCompactionMarker(lines.length, tree.truncated));
  const availableForLines = Math.max(0, MAX_AI_TEXT_ATTACHMENT_BYTES - byteLength(header) - reservedMarkerBytes);
  for (const line of lines) {
    const addition = `${includedLines.length > 0 ? "\n" : ""}${line}`;
    const additionBytes = byteLength(addition);
    if (includedBytes + additionBytes > availableForLines) break;
    includedLines.push(line);
    includedBytes += additionBytes;
  }

  let omittedEntries = lines.length - includedLines.length;
  let data = `${header}${includedLines.join("\n")}${directoryCompactionMarker(omittedEntries, tree.truncated)}`;
  while (includedLines.length > 0 && byteLength(data) > MAX_AI_TEXT_ATTACHMENT_BYTES) {
    includedLines.pop();
    omittedEntries += 1;
    data = `${header}${includedLines.join("\n")}${directoryCompactionMarker(omittedEntries, tree.truncated)}`;
  }
  const resultAttachment = attachment(projectContextName(root, directory, rootName), data);
  return {
    attachment: resultAttachment,
    truncated: true,
    originalSize,
    attachedSize: resultAttachment.size,
    omittedEntries,
  };
}

export function createProjectFileAttachment(relativePath: string, content: string): AiAttachment {
  return createProjectFileContext(relativePath, {
    content,
    size: byteLength(content),
    truncated: false,
  }).attachment;
}

export function createProjectDirectoryAttachment(
  relativePath: string,
  entries: ProjectContextTreeEntry[],
  rootName = ".",
): AiAttachment {
  return createProjectDirectoryContext(relativePath, { entries, truncated: false }, rootName).attachment;
}
