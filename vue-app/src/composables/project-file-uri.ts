import * as monaco from "@/services/monaco-runtime.js";

function normalizePath(path: string) {
  return path.replace(/\\/g, "/");
}

function absoluteProjectPath(projectRoot: string, relativePath: string) {
  const root = normalizePath(projectRoot).replace(/\/+$/, "");
  const relative = normalizePath(relativePath).replace(/^\/+/, "");
  return relative ? `${root}/${relative}` : root;
}

export function projectFileUri(projectRoot: string, path: string) {
  const normalizedPath = normalizePath(path);
  const absolutePath =
    normalizedPath.startsWith("/") || /^[A-Za-z]:\//.test(normalizedPath)
      ? normalizedPath
      : absoluteProjectPath(projectRoot, normalizedPath);
  return monaco.Uri.file(absolutePath);
}

/** Monaco model URI of an editor document: a file URI inside a project, else `app:`. */
export function projectEditorModelUri(path: string, projectRoot?: string) {
  if (projectRoot) return projectFileUri(projectRoot, path);
  const normalized = normalizePath(path).replace(/^\/+/, "");
  return monaco.Uri.from({ scheme: "app", path: `/${normalized}` });
}
