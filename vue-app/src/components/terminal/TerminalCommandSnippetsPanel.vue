<script setup lang="ts">
/**
 * Terminal drawer listing plugin command snippets (contributes.commandSnippets)
 * for this terminal. The user fills the declared variables, reviews the exact
 * text and inserts or copies it. A snippet is never executed: the insertion
 * plan strips trailing newlines and never adds "\r"; several lines are only
 * inserted when the shell has bracketed paste enabled, after the paste
 * confirmation (without the keep-newline option). Plugin text is rendered as
 * plain text only.
 */
import { computed, defineAsyncComponent, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import {
  commandSnippetDescription,
  commandSnippetInsertionPlan,
  commandSnippetLibraryTitle,
  commandSnippetShell,
  commandSnippetsForTerminal,
  commandSnippetTitle,
  commandSnippetValueIssue,
  expandCommandSnippet,
  filterCommandSnippets,
  type CommandSnippet,
  type CommandSnippetTarget,
} from "@/domain/terminal/command-snippets";
import { terminalProfilePlatform } from "@/domain/terminal/profiles";
import { useVueI18n } from "@/i18n/messages";
import { copyText } from "@/services/desktop-bridge";
import { useTerminalCommandSnippetsStore } from "@/stores/terminal-command-snippets";

const TerminalPasteConfirmDialog = defineAsyncComponent(
  () => import("@/components/terminal/TerminalPasteConfirmDialog.vue")
);

const props = defineProps<{
  modelValue: boolean;
  terminalKind: CommandSnippetTarget;
  ready: boolean;
  /** Reads the shell's current bracketed paste mode (xterm `modes.bracketedPasteMode`). */
  bracketedPasteMode: () => boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  /** Text to paste with `xterm.paste` (single line, or confirmed multi-line inside bracketed paste). */
  insert: [text: string];
}>();

const { t, localeChain } = useVueI18n();
const store = useTerminalCommandSnippetsStore();
const platform = terminalProfilePlatform(typeof navigator === "undefined" ? "" : navigator.platform || "");
const query = ref("");
const selectedKey = ref("");
const values = ref<Record<string, string>>({});
const confirmOpen = ref(false);
const confirmMounted = ref(false);
const confirmText = ref("");

const available = computed(() => commandSnippetsForTerminal(store.snippets, props.terminalKind, platform));
const filtered = computed(() => filterCommandSnippets(available.value, query.value, localeChain.value));
const groups = computed(() => {
  const result: Array<{ key: string; title: string; pluginName: string; snippets: CommandSnippet[] }> = [];
  for (const snippet of filtered.value) {
    const key = `${snippet.pluginId}/${snippet.libraryId}`;
    let group = result.find((item) => item.key === key);
    if (!group) {
      group = { key, title: commandSnippetLibraryTitle(snippet, localeChain.value), pluginName: snippet.pluginName, snippets: [] };
      result.push(group);
    }
    group.snippets.push(snippet);
  }
  return result;
});
const selected = computed(() => available.value.find((snippet) => snippet.key === selectedKey.value));
const shell = computed(() => commandSnippetShell(props.terminalKind, platform));
const expansion = computed(() => (selected.value ? expandCommandSnippet(selected.value, values.value, { shell: shell.value }) : undefined));
const plan = computed(() => (expansion.value ? commandSnippetInsertionPlan(expansion.value.text, props.bracketedPasteMode()) : undefined));
const multiline = computed(() => Boolean(expansion.value?.text.includes("\n")));
const canInsert = computed(() => Boolean(props.ready && plan.value && plan.value.mode !== "blocked" && !expansion.value?.invalid));

watch(selected, (snippet) => {
  values.value = Object.fromEntries((snippet?.variables ?? []).map((variable) => [variable.name, variable.default]));
});
// The library can change or disappear while the drawer is open (plugin disabled, upgraded).
watch(available, (list) => {
  if (selectedKey.value && !list.some((snippet) => snippet.key === selectedKey.value)) selectedKey.value = "";
});
watch(
  () => props.modelValue,
  (open) => {
    if (!open) confirmOpen.value = false;
  }
);

function variableIssue(name: string) {
  const value = values.value[name] ?? "";
  return commandSnippetValueIssue(value) ? t("pluginContrib.commandSnippets.invalidValue", { name }) : "";
}

function insert() {
  if (!expansion.value || expansion.value.invalid || !props.ready) return;
  // Re-read the mode at click time; the shell may have toggled it.
  const current = commandSnippetInsertionPlan(expansion.value.text, props.bracketedPasteMode());
  if (current.mode === "blocked") {
    ElMessage.warning({ message: t("pluginContrib.commandSnippets.multilineBlocked"), grouping: true });
    return;
  }
  if (current.mode === "multiline") {
    confirmText.value = current.text;
    confirmMounted.value = true;
    confirmOpen.value = true;
    return;
  }
  emit("insert", current.text);
  emit("update:modelValue", false);
}

function confirmMultiline(text: string) {
  // The confirm dialog already stripped trailing newlines (allowTrailingNewline=false),
  // and the user may have edited the text down to a single line.
  const current = commandSnippetInsertionPlan(text, props.bracketedPasteMode());
  if (current.mode === "blocked" || !props.ready) {
    ElMessage.warning({ message: t("pluginContrib.commandSnippets.multilineBlocked"), grouping: true });
    return;
  }
  emit("insert", current.text);
  emit("update:modelValue", false);
}

async function copy() {
  if (!expansion.value || expansion.value.invalid) return;
  try {
    await copyText(commandSnippetInsertionPlan(expansion.value.text, false).text);
    ElMessage.success({ message: t("pluginContrib.commandSnippets.copied"), grouping: true });
  } catch {
    ElMessage.error({ message: t("pluginContrib.commandSnippets.copyFailed"), grouping: true });
  }
}
</script>

<template>
  <el-drawer
    :model-value="modelValue"
    :title="t('pluginContrib.commandSnippets.title')"
    direction="rtl"
    size="min(460px, 100vw)"
    append-to-body
    class="terminal-snippets-drawer"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="terminal-snippets" :class="{ 'has-detail': Boolean(selected && expansion) }">
      <AppSearchField
        v-model="query"
        clearable
        :placeholder="t('pluginContrib.commandSnippets.search')"
        :aria-label="t('pluginContrib.commandSnippets.search')"
      />
      <el-empty v-if="groups.length === 0" :description="t('pluginContrib.commandSnippets.empty')" :image-size="64" />
      <div v-else class="terminal-snippets__list" role="listbox" :aria-label="t('pluginContrib.commandSnippets.title')">
        <section v-for="group in groups" :key="group.key" class="terminal-snippets__group">
          <header class="terminal-snippets__group-title">
            <strong dir="auto">{{ group.title }}</strong>
            <span class="terminal-snippets__group-count">{{ group.snippets.length }}</span>
            <el-tag size="small" type="info" disable-transitions>
              {{ t('pluginContrib.commandSnippets.source', { name: group.pluginName }) }}
            </el-tag>
          </header>
          <button
            v-for="snippet in group.snippets"
            :key="snippet.key"
            type="button"
            role="option"
            class="terminal-snippets__item"
            :class="{ 'is-selected': snippet.key === selectedKey }"
            :aria-selected="snippet.key === selectedKey"
            @click="selectedKey = snippet.key"
          >
            <span class="terminal-snippets__item-head">
              <span class="terminal-snippets__item-title" dir="auto">{{ commandSnippetTitle(snippet, localeChain) }}</span>
              <span v-if="snippet.tags.length" class="terminal-snippets__tags">
                <el-tag v-for="tag in snippet.tags" :key="tag" size="small" effect="plain" disable-transitions>{{ tag }}</el-tag>
              </span>
            </span>
            <small v-if="commandSnippetDescription(snippet, localeChain)" dir="auto">
              {{ commandSnippetDescription(snippet, localeChain) }}
            </small>
          </button>
        </section>
      </div>

      <div v-if="selected && expansion" class="terminal-snippets__detail">
        <el-form v-if="selected.variables.length" label-position="top" size="small" @submit.prevent>
          <p class="terminal-snippets__label">{{ t('pluginContrib.commandSnippets.variables') }}</p>
          <el-form-item
            v-for="variable in selected.variables"
            :key="variable.name"
            :label="variable.title || variable.name"
            :error="variableIssue(variable.name)"
          >
            <el-input v-model="values[variable.name]" :aria-label="variable.title || variable.name" spellcheck="false" />
          </el-form-item>
        </el-form>
        <p class="terminal-snippets__label">{{ t('pluginContrib.commandSnippets.preview') }}</p>
        <pre class="terminal-snippets__preview">{{ expansion.text }}</pre>
        <el-alert
          v-if="expansion.unquoted.length"
          type="warning"
          :closable="false"
          show-icon
          :title="t('pluginContrib.commandSnippets.quoteNotice', { names: expansion.unquoted.join(', ') })"
        />
        <el-alert
          v-if="plan?.mode === 'blocked'"
          type="warning"
          :closable="false"
          show-icon
          :title="t('pluginContrib.commandSnippets.multilineBlocked')"
        />
        <el-alert
          v-else-if="multiline"
          type="info"
          :closable="false"
          show-icon
          :title="t('pluginContrib.commandSnippets.multilineConfirm')"
        />
        <p v-if="!ready" class="terminal-snippets__hint">{{ t('pluginContrib.commandSnippets.notReady') }}</p>
        <div class="terminal-snippets__actions">
          <el-button :disabled="Boolean(expansion.invalid)" @click="copy">{{ t('pluginContrib.commandSnippets.copy') }}</el-button>
          <el-button type="primary" :disabled="!canInsert" @click="insert">
            {{ t('pluginContrib.commandSnippets.insert') }}
          </el-button>
        </div>
      </div>
      <p v-else-if="groups.length" class="terminal-snippets__hint terminal-snippets__select-hint">{{ t('pluginContrib.commandSnippets.select') }}</p>
    </div>
    <TerminalPasteConfirmDialog
      v-if="confirmMounted"
      v-model="confirmOpen"
      :text="confirmText"
      :allow-trailing-newline="false"
      @confirm="confirmMultiline"
    />
  </el-drawer>
</template>

<style>
.terminal-snippets-drawer .el-drawer__header {
  margin-bottom: 0;
  padding: 14px 16px 12px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.terminal-snippets-drawer .el-drawer__body {
  display: flex;
  min-height: 0;
  padding: 12px 16px 16px;
  overflow: hidden;
}
</style>

<style scoped>
.terminal-snippets {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 10px;
  min-width: 0;
  min-height: 0;
}

.terminal-snippets > :deep(*) {
  flex-shrink: 0;
}

.terminal-snippets__list {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 10px;
  min-height: 0;
  margin: 0 -6px;
  padding: 0 6px;
  overflow: auto;
}

.terminal-snippets.has-detail .terminal-snippets__list {
  flex-basis: 40%;
  min-height: 140px;
}

.terminal-snippets__group {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.terminal-snippets__group-title {
  position: sticky;
  top: 0;
  z-index: 1;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 2px;
  background: var(--el-bg-color);
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.terminal-snippets__group-title strong {
  min-width: 0;
  overflow: hidden;
  color: var(--el-text-color-regular);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.terminal-snippets__group-count {
  margin-inline-end: auto;
  padding: 0 6px;
  border-radius: 8px;
  background: var(--el-fill-color);
  font-size: 11px;
  line-height: 16px;
}

.terminal-snippets__item {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 3px;
  width: 100%;
  padding: 7px 10px;
  border: 1px solid transparent;
  border-radius: 6px;
  background: transparent;
  color: var(--el-text-color-primary);
  font: inherit;
  text-align: start;
  cursor: pointer;
  transition: background-color 0.15s, border-color 0.15s;
}

.terminal-snippets__item:hover,
.terminal-snippets__item:focus-visible {
  background: var(--el-fill-color-light);
  outline: none;
}

.terminal-snippets__item.is-selected {
  border-color: var(--el-color-primary-light-5);
  background: var(--el-color-primary-light-9);
}

.terminal-snippets__item-head {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.terminal-snippets__item-title {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.terminal-snippets__item small {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.4;
}

.terminal-snippets__tags {
  display: flex;
  flex-shrink: 0;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 4px;
}

.terminal-snippets__detail {
  display: flex;
  flex: 0 1 auto;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
  max-height: 60%;
  padding-top: 12px;
  border-top: 1px solid var(--el-border-color-lighter);
  overflow: auto;
}

.terminal-snippets__select-hint {
  padding: 10px 0 0;
  border-top: 1px solid var(--el-border-color-lighter);
  text-align: center;
}

.terminal-snippets__label,
.terminal-snippets__hint {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.terminal-snippets__preview {
  margin: 0;
  max-height: 200px;
  overflow: auto;
  padding: 8px;
  border-radius: 6px;
  background: var(--el-fill-color-light);
  font-family: "SFMono-Regular", Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  white-space: pre;
}

.terminal-snippets__actions {
  position: sticky;
  bottom: 0;
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 4px;
  background: var(--el-bg-color);
}
</style>
