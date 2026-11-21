<script setup lang="ts">
/**
 * Details of one test of a streaming run (Tests tab): status and duration,
 * a link to its source, the failure message, expected and actual values side
 * by side, the stack trace and the test's output. Stack trace locations are
 * links only when they resolve: a file reference that matches exactly one
 * project file (the run's own test files first, then the project file index)
 * opens it; a debugger address (`scheme://…`) opens through the debugger
 * source path; everything else stays plain text. All text renders as text,
 * never as HTML.
 */
import { computed, shallowRef, watch } from "vue";
import type { TestItem } from "@/domain/plugins/test-providers";
import {
  fileReferenceName,
  fileReferenceSuffix,
  matchProjectFile,
  parseStackTrace,
  type StackSegment,
} from "@/domain/testing/stack-trace";
import { formatTestDuration } from "@/domain/testing/test-run-format";
import type { TestRunState } from "@/domain/testing/test-runs";
import { useVueI18n } from "@/i18n/messages";
import { refreshProjectFileIndex, searchProjectFileIndex } from "@/services/project-index";
import { pathsAreCaseInsensitive } from "@/stores/debug-configs";

const props = defineProps<{
  run: Readonly<TestRunState>;
  nodeId: string;
  tests: readonly TestItem[];
  projectRoot: string;
}>();
const emit = defineEmits<{
  "open-file": [path: string, line: number | undefined];
  "open-address": [address: string];
}>();

/** File names looked up in the project index per trace. */
const INDEX_LOOKUPS_MAX = 16;
const INDEX_RESULTS = 50;

const { t } = useVueI18n();

// A copy per snapshot: the run's node objects are shared and mutated in place.
const node = computed(() => {
  const current = props.run.nodes[props.nodeId];
  return current ? { ...current } : undefined;
});

// The trace text itself (a string compares by value): a new run snapshot
// with the same trace re-parses and re-resolves nothing.
const trace = computed(() => props.run.nodes[props.nodeId]?.trace ?? "");
const traceLines = computed(() => (trace.value ? parseStackTrace(trace.value) : []));
const hasComparison = computed(() => node.value !== undefined && (node.value.expected !== undefined || node.value.actual !== undefined));

/** Reference suffix → project-relative path (null: does not resolve). */
const resolved = shallowRef(new Map<string, string | null>());
let resolveToken = 0;
let indexRefreshed = false;

function fileSegments(lines: StackSegment[][]) {
  return lines.flat().filter((segment): segment is Extract<StackSegment, { kind: "file" }> => segment.kind === "file");
}

function knownPaths(): Set<string> {
  const paths = new Set<string>();
  for (const item of Object.values(props.run.nodes)) if (item.path) paths.add(item.path);
  const visit = (items: readonly TestItem[], depth: number) => {
    if (depth > 256) return;
    for (const item of items) {
      if (item.path) paths.add(item.path);
      if (item.children) visit(item.children, depth + 1);
    }
  };
  visit(props.tests, 0);
  return paths;
}

async function resolveReferences(lines: StackSegment[][]) {
  const token = (resolveToken += 1);
  const caseInsensitive = pathsAreCaseInsensitive();
  const wanted = new Map<string, string>();
  for (const segment of fileSegments(lines)) {
    const suffix = fileReferenceSuffix(segment, props.projectRoot);
    if (suffix && !wanted.has(suffix)) wanted.set(suffix, fileReferenceName(segment));
  }
  const previous = resolved.value;
  // The same references (e.g. another test with the same frames): keep what is resolved.
  if (wanted.size === previous.size && [...wanted.keys()].every((suffix) => previous.has(suffix))) return;
  const next = new Map<string, string | null>();
  const known = knownPaths();
  const unresolved: [string, string][] = [];
  for (const [suffix, name] of wanted) {
    // A suffix resolved before (through the index too) keeps its target: links do not flicker.
    const match = previous.get(suffix) ?? matchProjectFile(suffix, known, caseInsensitive);
    next.set(suffix, match ?? null);
    if (!match) unresolved.push([suffix, name]);
  }
  if (unresolved.length) {
    try {
      if (!indexRefreshed) {
        indexRefreshed = true;
        await refreshProjectFileIndex([props.projectRoot]);
      }
      const names = [...new Set(unresolved.map(([, name]) => name))].slice(0, INDEX_LOOKUPS_MAX);
      const found = new Map<string, string[]>();
      for (const name of names) {
        const entries = await searchProjectFileIndex([props.projectRoot], name, INDEX_RESULTS);
        if (token !== resolveToken) return;
        const same = (value: string) => (caseInsensitive ? value.toLowerCase() === name.toLowerCase() : value === name);
        // Exact name matches sort first (the index compares names without
        // case), then fuzzy ones: a full page whose last entry still has the
        // name in any case may hide further same-name files (ambiguous, never
        // linked); otherwise it holds every same-name file.
        if (entries.length >= INDEX_RESULTS && entries[entries.length - 1]!.name.toLowerCase() === name.toLowerCase()) continue;
        found.set(name, entries.filter((entry) => same(entry.name)).map((entry) => entry.path.replace(/\\/g, "/")));
      }
      for (const [suffix, name] of unresolved) {
        const match = matchProjectFile(suffix, found.get(name) ?? [], caseInsensitive);
        if (match) next.set(suffix, match);
      }
    } catch {
      // The index is a convenience: unresolved references stay plain text.
    }
  }
  if (token === resolveToken) resolved.value = next;
}

watch(traceLines, (lines) => void resolveReferences(lines), { immediate: true });

function fileTarget(segment: Extract<StackSegment, { kind: "file" }>): string | undefined {
  const suffix = fileReferenceSuffix(segment, props.projectRoot);
  return suffix ? (resolved.value.get(suffix) ?? undefined) : undefined;
}

const source = computed(() => (node.value?.path ? { path: node.value.path, line: node.value.line } : undefined));
</script>

<template>
  <section v-if="node" class="project-test-run-details" :aria-label="t('pluginContrib.testProviders.details')">
    <header class="project-test-run-details__header">
      <span class="project-test-run-details__name" dir="auto">{{ node.label }}</span>
      <span class="project-test-run-details__status" :class="`is-${node.status}`">{{ t(`pluginContrib.testProviders.status.${node.status}`) }}</span>
      <span v-if="node.durationMs !== undefined" class="project-test-run-details__muted">{{ formatTestDuration(t, node.durationMs) }}</span>
      <el-link
        v-if="source"
        class="project-test-run-details__source"
        type="primary"
        underline="never"
        href="#"
        data-action="go-to-source"
        @click.prevent="emit('open-file', source.path, source.line)"
      >
        {{ t("pluginContrib.testProviders.goToSource") }}
      </el-link>
    </header>

    <pre v-if="node.message" class="project-test-run-details__message" dir="auto" data-test="message">{{ node.message }}</pre>

    <div v-if="hasComparison" class="project-test-run-details__comparison" data-test="comparison">
      <div>
        <div class="project-test-run-details__label">{{ t("pluginContrib.testProviders.expected") }}</div>
        <pre dir="auto" data-test="expected">{{ node.expected ?? "" }}</pre>
      </div>
      <div>
        <div class="project-test-run-details__label">{{ t("pluginContrib.testProviders.actual") }}</div>
        <pre dir="auto" data-test="actual">{{ node.actual ?? "" }}</pre>
      </div>
    </div>

    <template v-if="traceLines.length">
      <div class="project-test-run-details__label">{{ t("pluginContrib.testProviders.trace") }}</div>
      <pre class="project-test-run-details__trace" data-test="trace"><template v-for="(line, lineIndex) in traceLines" :key="lineIndex"><template
        v-for="(segment, index) in line"
        :key="index"
      ><el-link
        v-if="segment.kind === 'file' && fileTarget(segment)"
        type="primary"
        underline="hover"
        href="#"
        class="project-test-run-details__link"
        data-link="file"
        :aria-label="t('pluginContrib.testProviders.openLocation', { location: segment.text })"
        @click.prevent="emit('open-file', fileTarget(segment)!, segment.line)"
      >{{ segment.text }}</el-link><el-link
        v-else-if="segment.kind === 'address'"
        type="primary"
        underline="hover"
        href="#"
        class="project-test-run-details__link"
        data-link="address"
        :aria-label="t('pluginContrib.testProviders.openLocation', { location: segment.text })"
        @click.prevent="emit('open-address', segment.address)"
      >{{ segment.text }}</el-link><template v-else>{{ segment.text }}</template></template>{{ "\n" }}</template></pre>
    </template>

    <template v-if="node.output">
      <div class="project-test-run-details__label">{{ t("pluginContrib.testProviders.output") }}</div>
      <pre class="project-test-run-details__output" dir="auto" data-test="output">{{ node.output }}</pre>
    </template>
  </section>
</template>

<style scoped>
.project-test-run-details {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 6px 8px 8px;
  font-size: 12px;
}

.project-test-run-details__header {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.project-test-run-details__name {
  overflow: hidden;
  min-width: 0;
  color: var(--app-text);
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-test-run-details__status {
  flex: none;
  color: var(--app-text-muted);
}

.project-test-run-details__status.is-passed {
  color: var(--el-color-success);
}

.project-test-run-details__status.is-failed,
.project-test-run-details__status.is-errored {
  color: var(--el-color-danger);
}

.project-test-run-details__muted {
  flex: none;
  color: var(--app-text-muted);
}

.project-test-run-details__source {
  flex: none;
  margin-left: auto;
  font-size: 12px;
}

.project-test-run-details__label {
  color: var(--app-text-muted);
  font-size: 11px;
}

.project-test-run-details pre {
  margin: 0;
  padding: 4px 6px;
  border: 1px solid var(--app-border);
  border-radius: 4px;
  background: var(--app-surface);
  color: var(--app-text);
  font-family: var(--app-font-mono, monospace);
  font-size: 11px;
  line-height: 1.45;
  white-space: pre-wrap;
  word-break: break-word;
}

.project-test-run-details__message {
  color: var(--el-color-danger);
}

.project-test-run-details__comparison {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 6px;
}

.project-test-run-details__link {
  font-family: inherit;
  font-size: inherit;
  vertical-align: baseline;
}
</style>
