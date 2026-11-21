export interface PluginHostSaveTextResult {
  filename: string;
  content: string;
  savedResult: unknown;
  cancelledResult: unknown;
}

export function parsePluginHostSaveTextResult(value: unknown): PluginHostSaveTextResult;
export function parsePluginHostOpenURLResult(value: unknown): string;
export function isPluginHostDownloadResult(value: unknown): boolean;
