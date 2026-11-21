<script setup lang="ts">
/**
 * Project sidebar "Problems" tab: language-server diagnostics for the whole
 * project, including files that are not open (server push and workspace pull
 * diagnostics). Clicking an entry opens the file at the diagnostic position.
 */
import { computed, ref } from "vue";
import { ArrowRight } from "@element-plus/icons-vue";
import type { ProblemEntry, ProblemFile } from "@/domain/projects/lsp-problems";
import { useVueI18n } from "@/i18n/messages";
import { useProjectProblemsStore } from "@/stores/project-problems";

const props = defineProps<{ projectRoot: string }>();
const emit = defineEmits<{ "open-file": [path: string, line: number, column: number] }>();

const { t } = useVueI18n();
const problems = useProjectProblemsStore();
const filter = ref("");
const collapsed = ref(new Set<string>());

const summary = computed(() => problems.summary(props.projectRoot));
const visibleFiles = computed<ProblemFile[]>(() => {
  const query = filter.value.trim().toLowerCase();
  if (!query) return summary.value.files;
  return summary.value.files.flatMap((file) => {
    if (file.path.toLowerCase().includes(query)) return [file];
    const entries = file.entries.filter((entry) =>
      entry.message.toLowerCase().includes(query) || entry.source?.toLowerCase().includes(query) || entry.code?.toLowerCase().includes(query)
    );
    return entries.length ? [{ ...file, entries }] : [];
  });
});
const total = computed(() => summary.value.errors + summary.value.warnings + summary.value.others);

function severityName(entry: ProblemEntry) {
  return entry.severity === 1 ? "error" : entry.severity === 2 ? "warning" : "info";
}

function severityLabel(entry: ProblemEntry) {
  return t(entry.severity === 1 ? "project.problems.error" : entry.severity === 2 ? "project.problems.warning" : "project.problems.info");
}

function toggle(file: ProblemFile) {
  const next = new Set(collapsed.value);
  if (next.has(file.uri)) next.delete(file.uri);
  else next.add(file.uri);
  collapsed.value = next;
}

function open(file: ProblemFile, entry: ProblemEntry) {
  emit("open-file", file.path, entry.line + 1, entry.character + 1);
}

function fileName(path: string) {
  return path.split("/").pop() ?? path;
}

function fileDirectory(path: string) {
  const index = path.lastIndexOf("/");
  return index > 0 ? path.slice(0, index) : "";
}
</script>

<template>
  <section class="project-problems-panel" :aria-label="t('project.problems.tab')">
    <header class="project-problems-panel__toolbar">
      <el-input
        v-model="filter"
        size="small"
        clearable
        :placeholder="t('project.problems.filter')"
        :aria-label="t('project.problems.filter')"
        :disabled="!total"
      />
      <span
        class="project-problems-panel__counts"
        :title="t('project.problems.counts', { errors: summary.errors, warnings: summary.warnings, others: summary.others })"
      >
        <span class="codicon codicon-error is-error" aria-hidden="true" />{{ summary.errors }}
        <span class="codicon codicon-warning is-warning" aria-hidden="true" />{{ summary.warnings }}
      </span>
    </header>

    <el-scrollbar class="project-problems-panel__body">
      <p v-if="!total" class="project-sidebar-empty project-sidebar-empty--compact">{{ t("project.problems.empty") }}</p>
      <p v-else-if="!visibleFiles.length" class="project-sidebar-empty project-sidebar-empty--compact">
        {{ t("project.problems.noMatch") }}
      </p>
      <ul v-else class="project-problems-panel__files">
        <li v-for="file in visibleFiles" :key="file.uri">
          <button
            type="button"
            class="project-problems-panel__file"
            :aria-expanded="!collapsed.has(file.uri)"
            :title="file.path"
            @click="toggle(file)"
          >
            <el-icon class="project-problems-panel__chevron" :class="{ 'is-open': !collapsed.has(file.uri) }">
              <ArrowRight />
            </el-icon>
            <span class="project-problems-panel__file-name" dir="auto">{{ fileName(file.path) }}</span>
            <small class="project-problems-panel__file-dir" dir="auto">{{ fileDirectory(file.path) }}</small>
            <span class="project-problems-panel__badge">{{ file.entries.length }}</span>
          </button>
          <ul v-if="!collapsed.has(file.uri)" class="project-problems-panel__entries">
            <li v-for="(entry, index) in file.entries" :key="`${entry.serverId}:${entry.line}:${entry.character}:${index}`">
              <button
                type="button"
                class="project-problems-panel__entry"
                :title="entry.message"
                @click="open(file, entry)"
              >
                <span
                  class="codicon"
                  :class="[`codicon-${severityName(entry)}`, `is-${severityName(entry)}`]"
                  role="img"
                  :aria-label="severityLabel(entry)"
                />
                <span class="project-problems-panel__message" dir="auto">{{ entry.message }}</span>
                <small class="project-problems-panel__meta">
                  {{ [entry.source, entry.code].filter(Boolean).join(" ") }}
                  {{ t("project.problems.position", { line: entry.line + 1, column: entry.character + 1 }) }}
                </small>
              </button>
            </li>
          </ul>
        </li>
      </ul>
      <p v-if="summary.truncated" class="project-sidebar-empty project-sidebar-empty--compact">
        {{ t("project.problems.truncated") }}
      </p>
    </el-scrollbar>
  </section>
</template>

<style scoped>
.project-problems-panel {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.project-problems-panel__toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--app-border);
}

.project-problems-panel__counts {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: 3px;
  color: var(--app-text-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.project-problems-panel__body {
  min-height: 0;
  flex: 1;
}

.project-problems-panel__files,
.project-problems-panel__entries {
  margin: 0;
  padding: 0;
  list-style: none;
}

.project-problems-panel__file,
.project-problems-panel__entry {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 6px;
  border: 0;
  background: transparent;
  color: var(--app-text);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
  text-align: left;
}

.project-problems-panel__file {
  height: 24px;
  padding: 0 8px 0 4px;
}

.project-problems-panel__entry {
  min-height: 22px;
  padding: 2px 8px 2px 26px;
}

.project-problems-panel__file:hover,
.project-problems-panel__entry:hover {
  background: var(--app-hover);
}

.project-problems-panel__file:focus-visible,
.project-problems-panel__entry:focus-visible {
  outline: 1px solid var(--app-accent);
  outline-offset: -1px;
}

.project-problems-panel__chevron {
  flex: none;
  color: var(--app-text-muted);
  transition: transform 0.12s ease;
}

.project-problems-panel__chevron.is-open {
  transform: rotate(90deg);
}

.project-problems-panel__file-name,
.project-problems-panel__message {
  overflow: hidden;
  min-width: 0;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-problems-panel__message {
  flex: 1;
}

.project-problems-panel__file-dir,
.project-problems-panel__meta {
  overflow: hidden;
  min-width: 0;
  color: var(--app-text-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-problems-panel__file-dir {
  flex: 1;
}

.project-problems-panel__meta {
  flex: none;
  max-width: 45%;
}

.project-problems-panel__badge {
  flex: none;
  min-width: 18px;
  padding: 0 5px;
  border-radius: 9px;
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 16px;
  text-align: center;
}

.codicon.is-error {
  color: var(--app-danger);
}

.codicon.is-warning {
  color: var(--app-warning);
}

.codicon.is-info {
  color: var(--app-info);
}
</style>
