import { nearestFrameworkRoot, type EditorFramework, type FrameworkRoot } from "@/domain/editor/frameworks";

export interface FrameworkRegistration {
  pluginId: string;
  framework: EditorFramework;
}

let registrations: readonly FrameworkRegistration[] = [];
let roots: readonly FrameworkRoot[] = [];
let boundaries: readonly string[] = [];
const listeners = new Set<() => void>();

export function setEditorFrameworks(next: readonly FrameworkRegistration[]): void {
  registrations = next;
  roots = [];
  for (const listener of listeners) listener();
}

export function setFrameworkRoots(next: readonly FrameworkRoot[], projectBoundaries: readonly string[]): void {
  roots = next;
  boundaries = projectBoundaries;
  for (const listener of listeners) listener();
}

export function frameworkRegistryChanged(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function frameworksForPath(path: string): FrameworkRoot[] {
  return registrations.flatMap(({ pluginId, framework }) => {
    const selected = nearestFrameworkRoot(path, roots.filter((item) => item.pluginId === pluginId && item.framework.id === framework.id), boundaries);
    return selected ? [selected] : [];
  });
}

export function frameworkPluginAllowed(pluginId: string, path: string): boolean {
  if (!registrations.some((item) => item.pluginId === pluginId)) return true;
  return frameworksForPath(path).some((item) => item.pluginId === pluginId);
}
