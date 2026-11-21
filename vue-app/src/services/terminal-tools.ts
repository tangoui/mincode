import { shallowRef } from "vue";
import {
  createTerminalRecordingStore,
  type SavedTerminalRecording,
  type TerminalRecordingFrame,
} from "@/domain/terminal/recording-store";
import {
  applyPersistedBatch,
  loadPersistedValue,
  savePersistedValue,
} from "@/services/persistent-store";

export type { SavedTerminalRecording, TerminalRecordingFrame };

const TERMINAL_BOOKMARKS_STORAGE_KEY = "app_terminal_bookmarks_v1";

export interface TerminalCommandBookmark {
  id: string;
  title: string;
  command: string;
  createdAt: number;
}

function createId(prefix: string) {
  return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

function isBookmark(value: unknown): value is TerminalCommandBookmark {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate.id === "string" &&
    typeof candidate.title === "string" &&
    typeof candidate.command === "string" &&
    typeof candidate.createdAt === "number"
  );
}

export async function loadTerminalBookmarks() {
  const value = await loadPersistedValue<unknown>(TERMINAL_BOOKMARKS_STORAGE_KEY);
  return Array.isArray(value) ? value.filter(isBookmark).slice(0, 100) : [];
}

export async function saveTerminalBookmarks(bookmarks: TerminalCommandBookmark[]) {
  await savePersistedValue(TERMINAL_BOOKMARKS_STORAGE_KEY, bookmarks.slice(0, 100));
}

/** 所有终端窗格共享的录制列表；仅在回放面板打开或保存新录制时才从存储加载。 */
export const terminalRecordings = shallowRef<readonly SavedTerminalRecording[]>([]);

const terminalRecordingStore = createTerminalRecordingStore(
  {
    load: (key) => loadPersistedValue<unknown>(key),
    apply: ({ sets, deleteKeys }) => applyPersistedBatch({ sets, deleteKeys }),
  },
  (recordings) => {
    terminalRecordings.value = recordings;
  },
);

export function ensureTerminalRecordingsLoaded() {
  return terminalRecordingStore.ensureLoaded();
}

export function addTerminalRecording(recording: SavedTerminalRecording) {
  return terminalRecordingStore.add(recording);
}

export function deleteTerminalRecording(id: string) {
  return terminalRecordingStore.remove(id);
}

export function renameTerminalRecording(id: string, name: string) {
  return terminalRecordingStore.rename(id, name);
}

export function createTerminalBookmark(title: string, command: string): TerminalCommandBookmark {
  return {
    id: createId("terminal-command"),
    title: title.trim(),
    command: command.trim(),
    createdAt: Date.now(),
  };
}

export function createTerminalRecording(
  frames: TerminalRecordingFrame[],
  name?: string,
  now = Date.now()
): SavedTerminalRecording {
  const label = name?.trim();
  return {
    id: createId("terminal-recording"),
    // A recording is named after the terminal it came from; only sessions that
    // cannot describe themselves fall back to the timestamp.
    name: label || new Date(now).toLocaleString(),
    frames,
    createdAt: now,
  };
}
