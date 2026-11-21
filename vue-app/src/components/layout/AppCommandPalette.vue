<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import type { ScrollbarInstance } from "element-plus";
import {
  ChatDotSquare,
  Collection,
  Connection,
  DataAnalysis,
  Files,
  FolderOpened,
  Link,
  Operation,
  Switch,
} from "@element-plus/icons-vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import {
  groupCommandPaletteItems,
  type CommandPaletteItem,
  type CommandPaletteKind,
} from "@/domain/commands/command-palette";
import { useVueI18n, type MessageKey } from "@/i18n/messages";

const props = withDefaults(defineProps<{
  modelValue: boolean;
  query: string;
  results: CommandPaletteItem[];
  shortcutLabel: string;
  loading?: boolean;
  executingId?: string;
}>(), {
  loading: false,
  executingId: "",
});

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  "update:query": [value: string];
  execute: [item: CommandPaletteItem];
}>();

const { t } = useVueI18n();
const searchField = ref<InstanceType<typeof AppSearchField>>();
const resultList = ref<ScrollbarInstance>();
const activeIndex = ref(0);
let previouslyFocusedElement: HTMLElement | null = null;

const emptyLabel = computed(() => {
  return t("commandPalette.empty");
});
const loadingLabel = computed(() => {
  return t("commandPalette.loading");
});
const resultGroups = computed(() => groupCommandPaletteItems(props.results));
const orderedResults = computed(() =>
  resultGroups.value.flatMap((group) => group.entries.map((entry) => entry.item)),
);
const activeOptionId = computed(() => {
  const item = orderedResults.value[activeIndex.value];
  return item ? optionId(item) : undefined;
});
const modeHint = computed(() => {
  const prefix = props.query.trimStart()[0];
  if (prefix === ">") return t("commandPalette.mode.commands");
  if (prefix === "@") return t("commandPalette.mode.navigation");
  if (prefix === "#") return t("commandPalette.mode.resources");
  return t("commandPalette.mode.all");
});
const statusActive = computed(() =>
  props.loading,
);

const kindIcons: Record<CommandPaletteKind, typeof Operation> = {
  command: Operation,
  workspace: Switch,
  tab: Collection,
  project: FolderOpened,
  file: Files,
  ssh: Connection,
  database: DataAnalysis,
  http: Link,
};

const kindLabelKeys: Record<CommandPaletteKind, MessageKey> = {
  command: "commandPalette.kind.command",
  workspace: "commandPalette.kind.workspace",
  tab: "commandPalette.kind.tab",
  project: "commandPalette.kind.project",
  file: "commandPalette.kind.file",
  ssh: "commandPalette.kind.ssh",
  database: "commandPalette.kind.database",
  http: "commandPalette.kind.http",
};

watch(
  () => props.modelValue,
  (open, wasOpen) => {
    if (open && !wasOpen) capturePreviouslyFocusedElement();
    if (!open) activeIndex.value = 0;
  },
  { immediate: true },
);

watch(
  () => [props.query, props.results.length],
  () => {
    activeIndex.value = 0;
  },
);

onBeforeUnmount(() => {
  restoreFocus();
});

function optionId(item: CommandPaletteItem) {
  return `command-palette-option-${item.id}`;
}

function capturePreviouslyFocusedElement() {
  const activeElement = document.activeElement;
  previouslyFocusedElement = activeElement instanceof HTMLElement &&
    !activeElement.closest(".app-command-palette")
    ? activeElement
    : null;
}

function restoreFocus() {
  const element = previouslyFocusedElement;
  previouslyFocusedElement = null;
  if (element?.isConnected) element.focus({ preventScroll: true });
}

function updateOpen(value: boolean) {
  emit("update:modelValue", value);
}

async function focusSearch() {
  await nextTick();
  searchField.value?.focus();
}

async function revealActiveItem() {
  await nextTick();
  resultList.value?.wrapRef
    ?.querySelector<HTMLElement>(`[data-command-index="${activeIndex.value}"]`)
    ?.scrollIntoView({ block: "nearest" });
}

function moveSelection(offset: number) {
  const total = orderedResults.value.length;
  if (!total) return;
  activeIndex.value = (activeIndex.value + offset + total) % total;
  void revealActiveItem();
}

function executeActive() {
  const item = orderedResults.value[activeIndex.value];
  if (item) emit("execute", item);
}

function handleKeydown(event: KeyboardEvent) {
  if (event.isComposing) return;
  const target = event.target as HTMLElement | null;
  if (event.key === "ArrowDown") {
    event.preventDefault();
    moveSelection(1);
  } else if (event.key === "ArrowUp") {
    event.preventDefault();
    moveSelection(-1);
  } else if (event.key === "Enter") {
    if (target?.closest('[role="option"]')) return;
    event.preventDefault();
    executeActive();
  } else if (event.key === "Escape") {
    event.preventDefault();
    updateOpen(false);
  }
}

</script>

<template>
  <el-dialog
    class="app-command-palette"
    :model-value="props.modelValue"
    width="min(720px, calc(100vw - 32px))"
    append-to-body
    align-center
    :show-close="false"
    :close-on-click-modal="true"
    :close-on-press-escape="true"
    @update:model-value="updateOpen"
    @opened="focusSearch"
    @closed="restoreFocus"
  >
    <template #header>
      <div class="app-command-palette__header">
        <div>
          <el-icon aria-hidden="true"><ChatDotSquare /></el-icon>
          <strong>{{ t("commandPalette.title") }}</strong>
        </div>
        <kbd v-if="props.shortcutLabel">{{ props.shortcutLabel }}</kbd>
      </div>
    </template>

    <div class="app-command-palette__body" @keydown="handleKeydown">
      <AppSearchField
        ref="searchField"
        :model-value="props.query"
        :placeholder="t('commandPalette.placeholder')"
        :aria-label="t('commandPalette.searchLabel')"
        :clear-label="t('commandPalette.clearSearch')"
        clearable
        @update:model-value="emit('update:query', $event)"
      />

      <div class="app-command-palette__modebar">
        <span v-show="!statusActive">{{ modeHint }}</span>
        <div
          v-show="statusActive"
          class="app-command-palette__search-status is-inline"
          role="status"
          aria-live="polite"
        >
          <span v-if="props.loading" class="app-command-palette__spinner" aria-hidden="true" />
          <span v-if="props.loading">{{ loadingLabel }}</span>
        </div>
        <div class="app-command-palette__modes" :aria-label="t('commandPalette.searchModesLabel')">
          <kbd>&gt;</kbd>{{ t("commandPalette.mode.commandsShort") }}
          <kbd>@</kbd>{{ t("commandPalette.mode.navigationShort") }}
          <kbd>#</kbd>{{ t("commandPalette.mode.resourcesShort") }}
        </div>
      </div>

      <el-scrollbar
        ref="resultList"
        class="app-command-palette__results"
        role="listbox"
        :aria-label="t('commandPalette.resultsLabel')"
        :aria-busy="props.loading"
        :aria-activedescendant="activeOptionId"
      >
        <template v-for="group in resultGroups" :key="group.key">
          <div
            class="app-command-palette__group"
            aria-hidden="true"
          >
            <span>{{ group.label ?? t(kindLabelKeys[group.kind]) }}</span>
            <small>{{ group.entries.length }}</small>
          </div>

          <button
            v-for="entry in group.entries"
            :id="optionId(entry.item)"
            :key="entry.item.id"
            type="button"
            class="app-command-palette__item"
            :class="{
              'is-active': activeIndex === entry.index,
            }"
            role="option"
            :aria-selected="activeIndex === entry.index"
            :aria-label="`${entry.item.group ?? t(kindLabelKeys[entry.item.kind])}: ${entry.item.title}`"
            :data-command-index="entry.index"
            :disabled="Boolean(props.executingId)"
            @mouseenter="activeIndex = entry.index"
            @focus="activeIndex = entry.index"
            @click="emit('execute', entry.item)"
          >
            <span class="app-command-palette__icon" aria-hidden="true">
              <el-icon><component :is="kindIcons[entry.item.kind]" /></el-icon>
            </span>
            <span class="app-command-palette__copy">
                <strong>{{ entry.item.title }}</strong>
                <small v-if="entry.item.subtitle">{{ entry.item.subtitle }}</small>
            </span>
            <span v-if="props.executingId === entry.item.id" class="app-command-palette__spinner" aria-hidden="true" />
            <kbd
              v-else-if="entry.item.keybinding"
              class="app-command-palette__keybinding"
              :aria-label="t('commandPalette.keybindingLabel', { key: entry.item.keybinding })"
            >{{ entry.item.keybinding }}</kbd>
            <kbd v-else-if="activeIndex === entry.index">↵</kbd>
          </button>
        </template>

        <div
          v-if="!props.loading && props.results.length === 0"
          class="app-command-palette__empty"
        >
          <el-empty :image-size="48" :description="emptyLabel" />
        </div>
        <div v-else-if="props.loading && props.results.length === 0" class="app-command-palette__loading">
          <span class="app-command-palette__spinner" aria-hidden="true" />
          <span>{{ loadingLabel }}</span>
        </div>
      </el-scrollbar>
    </div>

    <template #footer>
      <div class="app-command-palette__footer">
        <span><kbd>↑</kbd><kbd>↓</kbd>{{ t("commandPalette.navigateHint") }}</span>
        <span><kbd>↵</kbd>{{ t("commandPalette.openHint") }}</span>
        <span><kbd>Esc</kbd>{{ t("commandPalette.closeHint") }}</span>
      </div>
    </template>
  </el-dialog>
</template>
