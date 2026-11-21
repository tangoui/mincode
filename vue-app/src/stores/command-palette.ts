import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  normalizeCommandPaletteShortcut,
  type CommandPaletteShortcutId,
} from "@/domain/commands/command-shortcuts";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { useKeybindingsStore } from "@/stores/keybindings";

const STORAGE_KEY = "app_command_palette_v1";
const MAX_USAGE_ENTRIES = 200;

export interface CommandPaletteUsage {
  count: number;
  lastUsedAt: number;
}

interface PersistedCommandPaletteState {
  version: 1;
  shortcut: CommandPaletteShortcutId;
  usage: Record<string, CommandPaletteUsage>;
}

function normalizeUsage(value: unknown): Record<string, CommandPaletteUsage> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const entries = Object.entries(value as Record<string, unknown>)
    .flatMap(([id, raw]) => {
      if (!id.trim() || !raw || typeof raw !== "object" || Array.isArray(raw)) return [];
      const item = raw as Partial<CommandPaletteUsage>;
      if (
        typeof item.count !== "number" ||
        !Number.isFinite(item.count) ||
        item.count < 1 ||
        typeof item.lastUsedAt !== "number" ||
        !Number.isFinite(item.lastUsedAt) ||
        item.lastUsedAt < 1
      ) {
        return [];
      }
      return [
        [
          id,
          {
            count: Math.min(Math.floor(item.count), 1_000_000),
            lastUsedAt: Math.floor(item.lastUsedAt),
          },
        ] as const,
      ];
    })
    .sort((left, right) => right[1].lastUsedAt - left[1].lastUsedAt)
    .slice(0, MAX_USAGE_ENTRIES);
  return Object.fromEntries(entries);
}

export const useCommandPaletteStore = defineStore("command-palette", () => {
  // Compatibility shim: the palette shortcut now lives in the keybindings store
  // (app_keybindings_v1 overrides). The legacy field is still written so an
  // older build keeps the user's choice; it is only read by the one-time migration.
  const keybindings = useKeybindingsStore();
  const shortcut = computed<CommandPaletteShortcutId>(() => keybindings.paletteShortcut);
  const usage = ref<Record<string, CommandPaletteUsage>>({});
  const hydrated = ref(false);
  let hydratePromise: Promise<void> | undefined;

  const recentCommandIds = computed(() =>
    Object.entries(usage.value)
      .sort((left, right) => right[1].lastUsedAt - left[1].lastUsedAt)
      .map(([id]) => id)
  );

  function snapshot(): PersistedCommandPaletteState {
    return {
      version: 1,
      shortcut: shortcut.value,
      usage: usage.value,
    };
  }

  async function persist() {
    await savePersistedValue(STORAGE_KEY, snapshot());
  }

  async function hydrate(force = false) {
    if (hydrated.value && !force) return;
    if (hydratePromise && !force) return hydratePromise;
    hydratePromise = (async () => {
      const stored = await loadPersistedValue<Partial<PersistedCommandPaletteState>>(STORAGE_KEY);
      usage.value = normalizeUsage(stored?.usage);
      hydrated.value = true;
    })();
    try {
      await hydratePromise;
    } finally {
      hydratePromise = undefined;
    }
  }

  function setShortcut(value: unknown) {
    keybindings.setPaletteShortcut(normalizeCommandPaletteShortcut(value));
    if (hydrated.value) void persist();
  }

  function recordUsage(commandId: string, usedAt = Date.now()) {
    const id = commandId.trim();
    if (!id || !Number.isFinite(usedAt) || usedAt < 1) return;
    const current = usage.value[id];
    const next = {
      ...usage.value,
      [id]: {
        count: Math.min((current?.count ?? 0) + 1, 1_000_000),
        lastUsedAt: Math.floor(usedAt),
      },
    };
    usage.value = normalizeUsage(next);
    if (hydrated.value) void persist();
  }

  return {
    shortcut,
    usage,
    hydrated,
    recentCommandIds,
    hydrate,
    setShortcut,
    recordUsage,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useCommandPaletteStore, import.meta.hot));
}
