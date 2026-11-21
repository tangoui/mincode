import {
  planTemplateWrites,
  type FileTemplateKind,
  type FileTemplateVariables,
  type FileTemplateWrite,
} from "@/domain/projects/file-templates";
import { getCustomTemplate } from "@/services/custom-file-template-service";
import type { CustomTemplateSummary } from "@/domain/projects/custom-file-templates";
export type FileTemplate = CustomTemplateSummary;
import { invokeBackend, isDesktopRuntime, nativeErrorMessage } from "@/services/desktop-bridge";

export class FileTemplateSourceError extends Error {}

function hex(buffer: ArrayBuffer) {
  return [...new Uint8Array(buffer)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

/**
 * Fetches and verifies every source of a template; resolves to key → text.
 * Throws FileTemplateSourceError when a source is missing, stale, altered or
 * not valid UTF-8.
 */
export async function loadTemplateSources(template: FileTemplate): Promise<Map<string, string>> {
  if (!isDesktopRuntime()) throw new FileTemplateSourceError("template sources need the desktop app");
  const current = await getCustomTemplate(template.id);
  if(current.template.revision !== template.revision || current.template.files.length !== template.files.length) throw new FileTemplateSourceError("template changed; reload before creating");
  const sources = new Map<string,string>();
  for(let i=0;i<template.files.length;i++) {
    const summary=template.files[i]!, file=current.template.files[i]!;
    const bytes=new TextEncoder().encode(file.content);
    const digest=hex(await crypto.subtle.digest("SHA-256",bytes));
    if(file.path!==summary.path||digest!==summary.source||bytes.length!==summary.size)throw new FileTemplateSourceError("template integrity changed");
    sources.set(digest,file.content);
  }
  return sources;
}

/** Builds the write plan from verified sources (see domain planTemplateWrites). */
export async function prepareTemplateWrites(
  template: FileTemplate,
  variables: FileTemplateVariables,
  parent: string,
): Promise<FileTemplateWrite[]> {
  const sources = await loadTemplateSources(template);
  return planTemplateWrites(template, variables, parent, sources);
}

export interface FileTemplateCreatedEvent {
  kind: FileTemplateKind;
  /** projectRoot used for the writes (the project, or the chosen parent folder). */
  root: string;
  /** Created paths relative to `root`, in write order. */
  created: string[];
  /** Project templates: the new project folder (absolute). */
  projectPath?: string;
  complete: boolean;
}

export interface FileTemplateApplyResult {
  /** Paths (relative to projectRoot) created before the run ended. */
  created: string[];
  failed?: { filePath: string; message: string; alreadyExists: boolean };
}

/** Writes the plan sequentially with create-only writes; stops at the first error and never deletes. */
export async function applyTemplateWrites(projectRoot: string, writes: readonly FileTemplateWrite[]): Promise<FileTemplateApplyResult> {
  const created: string[] = [];
  for (const write of writes) {
    try {
      await invokeBackend("local_fs_write_text_file", {
        projectRoot,
        filePath: write.filePath,
        content: write.content,
        createOnly: true,
      });
      created.push(write.filePath);
  } catch (error) {
      const message = nativeErrorMessage(error);
      return { created, failed: { filePath: write.filePath, message, alreadyExists: message.includes("already_exists") } };
  }
  }
  return { created };
}

/** True when `relativePath` already exists under `projectRoot` (file or folder). */
export async function templateTargetExists(projectRoot: string, relativePath: string): Promise<boolean> {
  try {
    await invokeBackend("local_fs_file_info", { projectRoot, filePath: relativePath });
    return true;
  } catch (error) {
    // A stat failure other than "does not exist" is treated as existing so the
    // caller stops instead of writing into an unknown path.
    return !/no such file|cannot find|not exist/i.test(nativeErrorMessage(error));
  }
}
