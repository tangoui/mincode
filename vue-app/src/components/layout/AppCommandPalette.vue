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
  type CommandPaletteGroup,
  type CommandPaletteItem,
  type CommandPaletteKind,
} from "@/domain/commands/command-palette";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { setEmbeddedBrowserOverlayBlocked } from "@/services/browser-overlay-events";

type SearchScope = "all" | "files" | "content";
type ProjectSearchError = "no-project" | "query-too-short" | "search-failed" | null;

interface ProjectOption {
  root: string;
  name: string;
}

const props = withDefaults(defineProps<{
  modelValue: boolean;
  query: string;
  results: CommandPaletteItem[];
  shortcutLabel: string;
  scope?: SearchScope;
  projectName?: string;
  projectRoot?: string;
  projectTarget?: string;
  projectOptions?: ProjectOption[];
  projectScopeAvailable?: boolean;
  projectSearchError?: ProjectSearchError;
  truncated?: boolean;
  searchEngine?: string;
  searchDurationMs?: number;
  loading?: boolean;
  executingId?: string;
}>(), {
  scope: "all",
  projectName: "",
  projectRoot: "",
  projectTarget: "",
  projectOptions: () => [],
  projectScopeAvailable: false,
  projectSearchError: null,
  truncated: false,
  searchEngine: "",
  searchDurationMs: undefined,
  loading: false,
  executingId: "",
});

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  "update:query": [value: string];
  "update:scope": [value: SearchScope];
  "update:projectTarget": [value: string];
  execute: [item: CommandPaletteItem];
}>();

const { t } = useVueI18n();
const searchField = ref<InstanceType<typeof AppSearchField>>();
const resultList = ref<ScrollbarInstance>();
const activeIndex = ref(0);
const overlaySource = "command-palette";
let previouslyFocusedElement: HTMLElement | null = null;

const hasProjectScope = computed(() => props.projectScopeAvailable);
const isProjectScope = computed(() => props.scope !== "all");
const selectedProject = computed(() =>
  props.projectOptions.find((option) => option.root === props.projectTarget),
);
/** More than one project and no explicit target means the whole workspace. */
const searchesWholeWorkspace = computed(
  () => !selectedProject.value && props.projectOptions.length > 1,
);
const showProjectSelector = computed(() => props.projectOptions.length > 1);
const projectLabel = computed(() => {
  if (selectedProject.value) return selectedProject.value.name;
  if (searchesWholeWorkspace.value) return t("commandPalette.allProjects");
  return (
    props.projectOptions[0]?.name ||
    props.projectName ||
    props.projectRoot ||
    t("commandPalette.projectUnnamed")
  );
});
const projectContextPath = computed(() => {
  if (selectedProject.value) return selectedProject.value.root;
  if (props.projectOptions.length === 1) return props.projectOptions[0]?.root ?? "";
  return "";
});
const scopeOptions = computed(() => [
  { value: "all" as const, label: t("commandPalette.scope.all"), disabled: false },
  { value: "files" as const, label: t("commandPalette.scope.files"), disabled: !hasProjectScope.value },
  { value: "content" as const, label: t("commandPalette.scope.content"), disabled: !hasProjectScope.value },
]);
const searchPlaceholder = computed(() => {
  if (props.scope === "files") return t("commandPalette.placeholder.files", { project: projectLabel.value });
  if (props.scope === "content") return t("commandPalette.placeholder.content", { project: projectLabel.value });
  return t("commandPalette.placeholder");
});
const searchLabel = computed(() => {
  if (props.scope === "files") return t("commandPalette.searchLabel.files");
  if (props.scope === "content") return t("commandPalette.searchLabel.content");
  return t("commandPalette.searchLabel");
});
const emptyLabel = computed(() => {
  if (props.scope === "files") return t("commandPalette.empty.files");
  if (props.scope === "content") return t("commandPalette.empty.content");
  return t("commandPalette.empty");
});
const loadingLabel = computed(() => {
  if (props.scope === "files") return t("commandPalette.loading.files");
  if (props.scope === "content") return t("commandPalette.loading.content");
  return t("commandPalette.loading");
});
const searchErrorLabel = computed(() => {
  if (!props.projectSearchError) return "";
  return t(`commandPalette.searchError.${props.projectSearchError}` as MessageKey);
});
// The "all" scope mixes commands, workspaces and saved resources, so it splits
// into one section per module. Project scopes return only files and stay flat,
// unless the search spans several workspace projects, in which case results
// carry a project label and split per project.
const showGroupHeaders = computed(
  () =>
    props.results.length > 0 &&
    (props.scope === "all" || props.results.some((item) => item.group)),
);
const resultGroups = computed<CommandPaletteGroup[]>(() =>
  showGroupHeaders.value
    ? groupCommandPaletteItems(props.results)
    : [{
        key: "results",
        kind: "file" as const,
        entries: props.results.map((item, index) => ({ item, index })),
      }],
);
const orderedResults = computed(() =>
  resultGroups.value.flatMap((group) => group.entries.map((entry) => entry.item)),
);
const activeOptionId = computed(() => {
  const item = orderedResults.value[activeIndex.value];
  return item ? optionId(item) : undefined;
});
// Telemetry for the project scopes only; "all" filters in memory, so there is no
// search to time. A zero duration means nothing has run yet.
const searchMetaLabel = computed(() => {
  if (!isProjectScope.value) return "";
  const parts: string[] = [];
  if (props.searchEngine) parts.push(props.searchEngine);
  if (props.searchDurationMs) {
    parts.push(t("commandPalette.searchDuration", { duration: props.searchDurationMs }));
  }
  return parts.join(" · ");
});

const modeHint = computed(() => {
  const prefix = props.query.trimStart()[0];
  if (prefix === ">") return t("commandPalette.mode.commands");
  if (prefix === "@") return t("commandPalette.mode.navigation");
  if (prefix === "#") return t("commandPalette.mode.resources");
  return t("commandPalette.mode.all");
});
const statusActive = computed(() =>
  props.loading || props.truncated || Boolean(searchMetaLabel.value),
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
    setEmbeddedBrowserOverlayBlocked(overlaySource, open);
    if (open && !wasOpen) capturePreviouslyFocusedElement();
    if (!open) activeIndex.value = 0;
  },
  { immediate: true },
);

watch(
  () => [props.query, props.results.length, props.scope],
  () => {
    activeIndex.value = 0;
  },
);

onBeforeUnmount(() => {
  setEmbeddedBrowserOverlayBlocked(overlaySource, false);
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

function selectScope(scope: SearchScope) {
  if (scope !== "all" && !hasProjectScope.value) return;
  emit("update:scope", scope);
}

function selectProjectTarget(root: string) {
  if (props.projectTarget === root) return;
  emit("update:projectTarget", root);
}

function handleScopeKeydown(event: KeyboardEvent, scope: SearchScope) {
  event.stopPropagation();
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  event.preventDefault();
  const availableScopes = scopeOptions.value.filter((option) => !option.disabled);
  const currentIndex = availableScopes.findIndex((option) => option.value === scope);
  const offset = event.key === "ArrowRight" ? 1 : -1;
  const next = availableScopes[(currentIndex + offset + availableScopes.length) % availableScopes.length];
  if (!next) return;
  selectScope(next.value);
  void nextTick(() => {
    document.querySelector<HTMLButtonElement>(`[data-search-scope="${next.value}"]`)?.focus();
  });
}

function handleKeydown(event: KeyboardEvent) {
  if (event.isComposing) return;
  const target = event.target as HTMLElement | null;
  if (target?.closest(".app-command-palette__scope-selector")) return;
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

function isContentResult(item: CommandPaletteItem) {
  return props.scope === "content" && Boolean(item.search);
}

function resultPath(item: CommandPaletteItem) {
  return item.search?.scope?.path || item.subtitle || "";
}

function resultLocation(item: CommandPaletteItem) {
  const { line, column } = item.search ?? {};
  if (!line) return "";
  return column ? `${line}:${column}` : String(line);
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
      <div
        class="app-command-palette__scope-selector"
        role="group"
        :aria-label="t('commandPalette.scopeLabel')"
      >
        <button
          v-for="option in scopeOptions"
          :key="option.value"
          type="button"
          :data-search-scope="option.value"
          :class="{ 'is-selected': props.scope === option.value }"
          :aria-pressed="props.scope === option.value"
          :disabled="option.disabled"
          @click="selectScope(option.value)"
          @keydown="handleScopeKeydown($event, option.value)"
        >
          {{ option.label }}
        </button>
      </div>

      <div
        v-if="isProjectScope && showProjectSelector"
        class="app-command-palette__project-picker"
        role="group"
        :aria-label="t('commandPalette.projectTargetLabel')"
      >
        <button
          type="button"
          :class="{ 'is-selected': searchesWholeWorkspace }"
          :aria-pressed="searchesWholeWorkspace"
          @click="selectProjectTarget('')"
          @keydown.stop
        >
          {{ t("commandPalette.allProjects") }}
        </button>
        <button
          v-for="option in props.projectOptions"
          :key="option.root"
          type="button"
          :title="option.root"
          :class="{ 'is-selected': props.projectTarget === option.root }"
          :aria-pressed="props.projectTarget === option.root"
          @click="selectProjectTarget(option.root)"
          @keydown.stop
        >
          {{ option.name }}
        </button>
      </div>

      <div v-else-if="isProjectScope" class="app-command-palette__project-context">
        <span>{{ t("commandPalette.projectScope", { project: projectLabel }) }}</span>
        <small v-if="projectContextPath">{{ projectContextPath }}</small>
      </div>

      <AppSearchField
        ref="searchField"
        :model-value="props.query"
        :placeholder="searchPlaceholder"
        :aria-label="searchLabel"
        :clear-label="t('commandPalette.clearSearch')"
        clearable
        @update:model-value="emit('update:query', $event)"
      />

      <div v-if="props.scope === 'all'" class="app-command-palette__modebar">
        <span v-show="!statusActive">{{ modeHint }}</span>
        <div
          v-show="statusActive"
          class="app-command-palette__search-status is-inline"
          role="status"
          aria-live="polite"
        >
          <span v-if="props.loading" class="app-command-palette__spinner" aria-hidden="true" />
          <span v-if="props.loading">{{ loadingLabel }}</span>
          <span v-if="props.truncated">{{ t("commandPalette.truncated") }}</span>
          <span v-if="searchMetaLabel" class="app-command-palette__search-meta">{{ searchMetaLabel }}</span>
        </div>
        <div class="app-command-palette__modes" :aria-label="t('commandPalette.searchModesLabel')">
          <kbd>&gt;</kbd>{{ t("commandPalette.mode.commandsShort") }}
          <kbd>@</kbd>{{ t("commandPalette.mode.navigationShort") }}
          <kbd>#</kbd>{{ t("commandPalette.mode.resourcesShort") }}
        </div>
      </div>

      <!-- Without the mode bar this row is the only separator between the search
           field and the results, so it always renders and the list never shifts. -->
      <div
        v-else
        class="app-command-palette__search-status"
        role="status"
        aria-live="polite"
      >
        <span v-if="props.loading" class="app-command-palette__spinner" aria-hidden="true" />
        <span v-if="props.loading">{{ loadingLabel }}</span>
        <span v-if="props.truncated">{{ t("commandPalette.truncated") }}</span>
        <span v-if="searchMetaLabel" class="app-command-palette__search-meta">{{ searchMetaLabel }}</span>
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
            v-if="showGroupHeaders"
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
              'is-search-result': isContentResult(entry.item),
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
              <template v-if="isContentResult(entry.item)">
                <span class="app-command-palette__result-heading">
                  <strong>{{ entry.item.title }}</strong>
                  <small>{{ resultPath(entry.item) }}<template v-if="resultLocation(entry.item)">:{{ resultLocation(entry.item) }}</template></small>
                </span>
                <code v-if="entry.item.search?.detail" class="app-command-palette__snippet">{{ entry.item.search.detail }}</code>
              </template>
              <template v-else>
                <strong>{{ entry.item.title }}</strong>
                <small v-if="entry.item.subtitle">{{ entry.item.subtitle }}</small>
              </template>
            </span>
            <span
              v-if="!isContentResult(entry.item) && !showGroupHeaders"
              class="app-command-palette__kind"
            >{{ t(kindLabelKeys[entry.item.kind]) }}</span>
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
          :class="{ 'is-error': props.projectSearchError === 'search-failed' }"
        >
          <el-empty :image-size="48" :description="searchErrorLabel || emptyLabel" />
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
