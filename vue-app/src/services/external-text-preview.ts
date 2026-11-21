import { invokeBackend } from "@/services/desktop-bridge";

export interface ExternalTextChunk {
  text: string;
  startOffset: number;
  nextOffset: number;
  totalBytes: number;
  eof: boolean;
  version: string;
}

/** A bounded read, never the ordinary full-document read/save path. Offsets are bytes. */
export async function readExternalTextChunk(path: string, offset: number, version: string | undefined, signal: AbortSignal): Promise<ExternalTextChunk> {
  const raw = await invokeBackend<unknown>("local_fs_read_external_text_chunk", {
    path, offset, ...(version ? { expectedVersion: version } : {}),
  }, { signal });
  if (!raw || typeof raw !== "object") throw new Error("invalid_chunk");
  const chunk = raw as ExternalTextChunk;
  if (typeof chunk.text !== "string" || typeof chunk.version !== "string" || !chunk.version ||
      !Number.isSafeInteger(chunk.startOffset) || !Number.isSafeInteger(chunk.nextOffset) || !Number.isSafeInteger(chunk.totalBytes) ||
      chunk.startOffset !== offset || offset < 0 || chunk.nextOffset < offset || chunk.nextOffset > chunk.totalBytes ||
      typeof chunk.eof !== "boolean" || chunk.eof !== (chunk.nextOffset === chunk.totalBytes) ||
      (!chunk.eof && chunk.nextOffset === offset) || chunk.nextOffset - offset > 512 * 1024 ||
      new TextEncoder().encode(chunk.text).length !== chunk.nextOffset - offset || (version && version !== chunk.version)) {
    throw new Error("invalid_chunk");
  }
  return chunk;
}
