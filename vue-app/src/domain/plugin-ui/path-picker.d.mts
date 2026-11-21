export interface PluginPathPickRequest {
  id: string;
  kind: "file" | "directory";
  multiple: boolean;
  extensions: string[];
}

export const MAX_PICKED_PATHS: number;
export function parsePathPickRequest(value: unknown): PluginPathPickRequest | undefined;
export function filterPickedPaths(paths: unknown[], extensions: string[], max: number): string[];
export function pathPickNeedsConfirmation(activation: { isActive?: unknown } | undefined): boolean;
