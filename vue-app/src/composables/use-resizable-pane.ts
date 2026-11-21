import { ref, type Ref } from "vue";
import {
  loadPersistedValue,
  registerDesktopBrowserSnapshotKeys,
  savePersistedValue,
} from "@/services/persistent-store";

type PaneLimit = number | ((containerSize: number) => number);

interface UseResizablePaneOptions {
  sizeKey: string;
  collapsedKey: string;
  defaultSize: number;
  minSize: number;
  maxSize: PaneLimit;
  containerRef?: Ref<HTMLElement | null>;
}

function readStoredNumber(key: string, fallback: number) {
  if (typeof window === "undefined") return fallback;
  const value = Number(localStorage.getItem(key));
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function readStoredBoolean(key: string) {
  if (typeof window === "undefined") return false;
  const value = localStorage.getItem(key);
  return value === "true" || value === "1";
}

export function useResizablePane(options: UseResizablePaneOptions) {
  // The initial size is read synchronously from localStorage below, so desktop saves keep that copy.
  registerDesktopBrowserSnapshotKeys(options.sizeKey, options.collapsedKey);
  const size = ref(readStoredNumber(options.sizeKey, options.defaultSize));
  const collapsed = ref(readStoredBoolean(options.collapsedKey));

  function containerSize() {
    return options.containerRef?.value?.getBoundingClientRect().width ?? window.innerWidth;
  }

  function maximumSize() {
    return typeof options.maxSize === "function"
      ? options.maxSize(containerSize())
      : options.maxSize;
  }

  function clamp(value: number) {
    return Math.round(Math.max(options.minSize, Math.min(maximumSize(), value)));
  }

  function persist() {
    return Promise.all([
      savePersistedValue(options.collapsedKey, collapsed.value),
      savePersistedValue(options.sizeKey, Math.round(size.value)),
    ]);
  }

  async function hydrate() {
    const [storedCollapsed, storedSize] = await Promise.all([
      loadPersistedValue<unknown>(options.collapsedKey),
      loadPersistedValue<unknown>(options.sizeKey),
    ]);
    if (typeof storedCollapsed === "boolean") collapsed.value = storedCollapsed;
    if (typeof storedSize === "number" && Number.isFinite(storedSize)) {
      size.value = clamp(storedSize);
    } else {
      size.value = clamp(size.value);
    }
    await persist();
  }

  return {
    size,
    collapsed,
    hydrate,
    persist,
  };
}
