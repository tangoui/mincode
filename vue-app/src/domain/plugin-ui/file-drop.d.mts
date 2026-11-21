export interface PluginFileDrop {
  mode: "contents" | "paths";
  extensions: string[];
  directories: boolean;
  maxEntries: number;
}

export interface PluginDroppedEntry {
  path: string;
  name: string;
  kind: "file" | "directory";
  size: number;
}

export interface PluginDroppedFile {
  name: string;
  size: number;
  lastModified: number;
  data: string;
}

export type PluginDropDecision =
  | { ok: true; entries?: PluginDroppedEntry[]; files?: PluginDroppedFile[] }
  | { ok: false; reason: "none" | "tooMany" | "invalid"; max?: number };

export function parseFileDrop(value: unknown): PluginFileDrop | undefined;
export function acceptDrop(fileDrop: PluginFileDrop | undefined, event: unknown): PluginDropDecision;
