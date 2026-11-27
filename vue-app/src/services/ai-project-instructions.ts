import { canInvoke, invokeBackend } from "@/services/desktop-bridge";

const instructionPaths = ["AGENTS.md", "CLAUDE.md", ".mincode/rules.md"] as const;
const byteLimit = 32 * 1024;
const truncationNote = "\n\n[MinCode: project instructions truncated at 32 KiB]";
const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true });

interface ProjectFileInfo {
  isFile: boolean;
  size: number;
  modifiedAt?: string;
}

interface CachedFile {
  info: ProjectFileInfo;
  content: string;
}

const files = new Map<string, CachedFile>();
const maxCachedFiles = 48;

async function readInstructions(projectRoot: string, filePath: string) {
  const key = JSON.stringify([projectRoot, filePath]);
  const args = { projectRoot, filePath };
  try {
    const info = await invokeBackend<ProjectFileInfo | null>("local_fs_file_info", { ...args, allowMissing: true });
    const cached = files.get(key);
    files.delete(key);
    if (!info?.isFile || !info.size) return "";
    if (info.modifiedAt && cached?.info.modifiedAt === info.modifiedAt && cached.info.size === info.size) {
      files.set(key, cached);
      return cached.content;
    }
    const content = (await invokeBackend<string>("local_fs_read_text_file", args)).trim();
    if (info.modifiedAt) {
      files.set(key, { info, content });
      if (files.size > maxCachedFiles) {
        const oldest = files.keys().next().value;
        if (oldest !== undefined) files.delete(oldest);
      }
    }
    return content;
  } catch {
    files.delete(key);
    return "";
  }
}

function utf8Prefix(bytes: Uint8Array, maxBytes: number) {
  let end = maxBytes;
  while ((bytes[end] & 0xc0) === 0x80) end--;
  return decoder.decode(bytes.subarray(0, end));
}

export interface ProjectInstructions {
  prompt: string;
  sources: string[];
}

export async function loadProjectInstructions(projectRoot: string): Promise<ProjectInstructions> {
  if (!projectRoot || !canInvoke("local_fs_file_info")) return { prompt: "", sources: [] };
  const contents = await Promise.all(instructionPaths.map((path) => readInstructions(projectRoot, path)));
  const parts = instructionPaths.flatMap((path, index) =>
    contents[index] ? [{ path, section: `\n\n### ${path}\n${contents[index]}` }] : [],
  );
  if (!parts.length) return { prompt: "", sources: [] };

  const heading = "\n\n## Project instructions";
  const prompt = heading + parts.map(({ section }) => section).join("");
  if (encoder.encode(prompt).length <= byteLimit) return { prompt, sources: parts.map(({ path }) => path) };

  const sources: string[] = [];
  let excerpt = heading;
  let remaining = byteLimit - encoder.encode(heading + truncationNote).length;
  for (const { path, section } of parts) {
    if (remaining <= encoder.encode(`\n\n### ${path}\n`).length) break;
    const bytes = encoder.encode(section);
    sources.push(path);
    if (bytes.length > remaining) {
      excerpt += utf8Prefix(bytes, remaining);
      break;
    }
    excerpt += section;
    remaining -= bytes.length;
  }
  return { prompt: excerpt + truncationNote, sources };
}
