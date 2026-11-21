import { hydrateSettingsSnapshot } from "@/services/settings-persistence";

export const DEFAULT_UI_SCALE = 100;
export const MIN_UI_SCALE = 80;
export const MAX_UI_SCALE = 125;
export const UI_SCALE_STEP = 5;
const LEGACY_DEFAULT_UI_FONT_SIZE = 13;
const MIN_LEGACY_UI_FONT_SIZE = 11;
const MAX_LEGACY_UI_FONT_SIZE = 20;

export type UiFontFamily = "system" | "sans" | "mono";

interface UiAppearanceSettings {
  uiScale?: unknown;
  /** @deprecated Kept only to migrate persisted interface font-size preferences. */
  fontSize?: unknown;
  uiFontFamily?: unknown;
}

const FONT_STACKS: Record<UiFontFamily, string> = {
  system:
    '-apple-system, BlinkMacSystemFont, "SF Pro Text", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Segoe UI", Arial, sans-serif',
  sans:
    'Inter, "Helvetica Neue", Arial, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif',
  mono:
    '"SFMono-Regular", Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace',
};

let resizeFrame: number | undefined;
let viewportListenerInstalled = false;

export function normalizeUiScale(value: unknown): number {
  const parsed = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(parsed)) return DEFAULT_UI_SCALE;
  const snapped = Math.round(parsed / UI_SCALE_STEP) * UI_SCALE_STEP;
  return Math.min(MAX_UI_SCALE, Math.max(MIN_UI_SCALE, snapped));
}

export function legacyFontSizeToUiScale(value: unknown): number {
  const parsed = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(parsed)) return DEFAULT_UI_SCALE;
  const fontSize = Math.min(MAX_LEGACY_UI_FONT_SIZE, Math.max(MIN_LEGACY_UI_FONT_SIZE, Math.round(parsed)));
  return normalizeUiScale((fontSize / LEGACY_DEFAULT_UI_FONT_SIZE) * 100);
}

export function normalizeUiFontFamily(value: unknown): UiFontFamily {
  return value === "sans" || value === "mono" || value === "system" ? value : "system";
}

function updateUnscaledViewport(root: HTMLElement, scale: number) {
  if (typeof window === "undefined") return;
  const factor = scale / 100;
  root.style.setProperty("--app-ui-viewport-width", `${window.innerWidth / factor}px`);
  root.style.setProperty("--app-ui-viewport-height", `${window.innerHeight / factor}px`);
}

function installViewportListener() {
  if (typeof window === "undefined" || viewportListenerInstalled) return;
  viewportListenerInstalled = true;
  window.addEventListener("resize", () => {
    const root = document.documentElement;
    updateUnscaledViewport(root, normalizeUiScale(root.dataset.uiScale));
  });
}

function requestViewportLayout() {
  if (typeof window === "undefined") return;
  if (resizeFrame !== undefined) window.cancelAnimationFrame(resizeFrame);
  resizeFrame = window.requestAnimationFrame(() => {
    resizeFrame = undefined;
    window.dispatchEvent(new Event("resize"));
  });
}

export function applyUiAppearance(settings: UiAppearanceSettings): void {
  const root = document.documentElement;
  const scale = normalizeUiScale(
    settings.uiScale === undefined ? legacyFontSizeToUiScale(settings.fontSize) : settings.uiScale
  );
  const fontFamily = normalizeUiFontFamily(settings.uiFontFamily);

  root.style.setProperty("--app-ui-font-size", `${LEGACY_DEFAULT_UI_FONT_SIZE}px`);
  root.style.setProperty("--app-ui-scale", `${scale}%`);
  root.style.setProperty("--app-ui-overlay-scale", String(100 / scale));
  root.style.setProperty("--app-font-sans", FONT_STACKS[fontFamily]);
  root.dataset.uiScale = String(scale);
  root.dataset.uiFontFamily = fontFamily;
  delete root.dataset.uiFontSize;
  installViewportListener();
  updateUnscaledViewport(root, scale);
  document.body.style.setProperty("zoom", `${scale}%`);
  requestViewportLayout();
}

export async function hydrateUiAppearance(): Promise<void> {
  const stored = await hydrateSettingsSnapshot<UiAppearanceSettings & Record<string, unknown>>();
  applyUiAppearance(stored ?? {});
}
