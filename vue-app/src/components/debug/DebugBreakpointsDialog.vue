<script setup lang="ts">
/**
 * Every breakpoint of the project (enable, remove, go to) and the exception
 * breakpoints of its debuggers. Breakpoints in debugger-provided sources
 * (saved by debugger address) are listed after the files' as
 * `<name>:<line>` (the address's last segment without its extension, the
 * address as the tooltip), with the adapter's reason when no running
 * session verified them; "go to" opens the source through the debugger. Exception filters come from a session's
 * capabilities; for a debugger without a session in this window the saved
 * filter ids are listed. A debugger that declares `exceptionTypes` also gets
 * its "pause only on these exception types" list (texts from the plugin),
 * saved per project and sent to the debugger's settings when a session of
 * it runs in the project. Like the switches, the list applies without a save
 * button: when the input loses focus, or when the dialog closes; a list that
 * cannot be saved shows why (and, on close, a warning) and is not applied.
 *
 * Method breakpoints are listed for each debugger of the project that
 * declares `functionBreakpoints` (plugin texts for the name input; the host
 * knows no name format). Adding, switching and removing save at once and
 * re-send the type's list to the project's running sessions whose adapter
 * supports them; a name no session verified shows as unverified with the
 * adapter's reason, and turns verified when an adapter reports it later.
 *
 * Field breakpoints (set from the Variables pane) are listed by debugger
 * type: the saved ones, and those of the project's running sessions that
 * could not be saved (marked as this session's only; gone when it ends).
 * Removing one re-sends the field breakpoints of the sessions of that type.
 */
import { computed, reactive, ref, watch } from "vue";
import { Delete, Position } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { addressBreakpointName, breakpointAddress, type BreakpointAt } from "@/domain/debug/address-breakpoints";
import { debugErrorText } from "@/domain/debug/errors";
import { LOGPOINT_UNSUPPORTED_REASON, normalizeLogMessage } from "@/domain/debug/logpoints";
import { toSourcePath } from "@/domain/debug/paths";
import { EXCEPTION_TYPE_LIMITS, normalizeExceptionTypes, parseExceptionTypesText } from "@/domain/debug/exception-types";
import { FUNCTION_BREAKPOINT_LIMITS, normalizeFunctionBreakpointName, type StoredFunctionBreakpoint } from "@/domain/debug/function-breakpoints";
import type { DapExceptionBreakpointsFilter, DataBreakpointAccessType, DebuggerInfo, SourceBreakpoint } from "@/domain/debug/types";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { useVueI18n } from "@/i18n/messages";
import { listDebuggers } from "@/services/debug-service";
import { sameProjectRoot, useDebugConfigsStore } from "@/stores/debug-configs";
import { useDebugSessionsStore, type DebugDataBreakpointEntry, type DebugFunctionBreakpointStatus } from "@/stores/debug-sessions";

interface BreakpointRow {
  id: string;
  /** What the location shows: the project-relative path, or the address's name. */
  label: string;
  /** The location's tooltip: the absolute path, or the address. */
  title: string;
  at: BreakpointAt;
  breakpoint: SourceBreakpoint;
}

interface ExceptionGroup {
  type: string;
  filters: DapExceptionBreakpointsFilter[];
  enabled: string[];
  /** The debugger, when it declares the exception type list. */
  typesDebugger?: DebuggerInfo;
}

interface FunctionGroup {
  type: string;
  /** The debugger that declares the method breakpoint texts. */
  debugger: DebuggerInfo;
  list: StoredFunctionBreakpoint[];
  status: Record<string, DebugFunctionBreakpointStatus>;
}

interface DataGroup {
  type: string;
  entries: DebugDataBreakpointEntry[];
}

const props = defineProps<{
  modelValue: boolean;
  projectRoot: string;
}>();

const emit = defineEmits<{
  "update:modelValue": [visible: boolean];
  "open-location": [path: string, line: number];
  /** Open the source of a debugger address at line. */
  "open-address": [address: string, type: string, line: number];
}>();

const { t, localeChain } = useVueI18n();
const configs = useDebugConfigsStore();
const sessions = useDebugSessionsStore();

const state = computed(() => configs.stateFor(props.projectRoot));

/** Installed debuggers, read when the dialog opens; a failed listing shows no lists. */
const debuggers = ref<DebuggerInfo[]>([]);
let listing = 0;

async function loadDebuggers() {
  const request = (listing += 1);
  let found: DebuggerInfo[] = [];
  try {
    found = await listDebuggers();
  } catch {
    // Without the listing there are no exception type lists to show.
  }
  if (request === listing) debuggers.value = found;
}

/** Debugger type → the text being edited (absent: the saved list is shown). */
const typeDrafts = reactive<Record<string, string>>({});
/** Debugger type → why the last save was refused. */
const typeErrors = reactive<Record<string, string>>({});
/** Debugger type → the method name being typed. */
const functionDrafts = reactive<Record<string, string>>({});
/** Debugger type → why the last add was refused. */
const functionErrors = reactive<Record<string, string>>({});

watch(
  () => props.modelValue,
  (visible, wasVisible) => {
    if (!visible) {
      if (wasVisible) saveTypesOnClose();
      return;
    }
    // What could not be saved when the dialog closed is dropped.
    for (const type of Object.keys(typeDrafts)) delete typeDrafts[type];
    for (const type of Object.keys(typeErrors)) delete typeErrors[type];
    for (const type of Object.keys(functionDrafts)) delete functionDrafts[type];
    for (const type of Object.keys(functionErrors)) delete functionErrors[type];
    void loadDebuggers();
  },
  { immediate: true },
);

function typesDebuggerFor(type: string): DebuggerInfo | undefined {
  return debuggers.value.find((item) => item.type === type && item.exceptionTypes);
}

/**
 * Breakpoints in debugger-provided sources, by debugger type then class
 * name; the type is shown only when the project has more than one.
 */
const addressRows = computed<BreakpointRow[]>(() => {
  const byType = Object.entries(state.value.addressBreakpoints).sort(([a], [b]) => a.localeCompare(b));
  const showType = byType.length > 1;
  return byType.flatMap(([type, addresses]) =>
    Object.entries(addresses)
      .map(([address, list]) => ({ address, list, name: addressBreakpointName(address) }))
      .sort((a, b) => a.name.localeCompare(b.name) || a.address.localeCompare(b.address))
      .flatMap(({ address, list, name }) =>
        list.map((breakpoint) => ({
          id: `address:${type}:${address}:${breakpoint.line}`,
          label: showType ? `${name} (${type})` : name,
          title: address,
          at: { address, type },
          breakpoint,
        })),
      ),
  );
});

const rows = computed<BreakpointRow[]>(() => [
  ...Object.entries(state.value.breakpoints)
    .sort(([a], [b]) => a.localeCompare(b))
    .flatMap(([relative, list]) => {
      const path = toSourcePath(props.projectRoot, relative);
      return list.map((breakpoint) => ({ id: `${relative}:${breakpoint.line}`, label: relative, title: path, at: path, breakpoint }));
    }),
  ...addressRows.value,
]);

/**
 * "Not verified" with the adapter's text for an enabled breakpoint of a
 * debugger address that running sessions answered and none verified; null
 * otherwise (file breakpoints show this in the editor gutter).
 */
function statusText(row: BreakpointRow): string | null {
  if (breakpointAddress(row.at) === undefined || !row.breakpoint.enabled) return null;
  const line = row.breakpoint.line;
  if (sessions.verifiedLinesFor(props.projectRoot, row.at)[line] !== false) return null;
  const message = sessions.breakpointDetailsFor(props.projectRoot, row.at)[line]?.message;
  return message ? t("debug.breakpoints.addressUnverifiedReason", { message: debugErrorText(message) }) : t("debug.breakpoints.addressUnverified");
}

const exceptionGroups = computed<ExceptionGroup[]>(() => {
  const projectSessions = sessions.sessions.filter((session) => !session.noDebug && sameProjectRoot(session.projectRoot, props.projectRoot));
  const types = new Set([
    ...projectSessions.map((session) => session.type),
    ...Object.keys(state.value.exceptionFilters),
    ...Object.keys(state.value.exceptionTypes),
    // A debugger with a list is shown for the project's run configurations
    // before its first session, so the list can be set up front.
    ...state.value.configs.map((cfg) => cfg.type).filter((type) => typesDebuggerFor(type)),
  ]);
  return [...types].sort().flatMap((type) => {
    const saved = state.value.exceptionFilters[type];
    // The newest session of the type describes its filters.
    const latest = projectSessions.filter((session) => session.type === type).sort((a, b) => b.startedAt - a.startedAt)[0];
    const offered = latest?.capabilities.exceptionBreakpointFilters ?? [];
    const filters = offered.length
      ? offered
      : (saved ?? []).map((filter) => ({ filter, label: t("debug.breakpoints.savedFilter", { filter }) }));
    const typesDebugger = typesDebuggerFor(type);
    if (!filters.length && !typesDebugger) return [];
    const enabled = saved ?? offered.filter((filter) => filter.default).map((filter) => filter.filter);
    return [{ type, filters, enabled, typesDebugger }];
  });
});

/**
 * The project's debuggers that declare method breakpoint texts: those of
 * its run configurations and sessions, and types with saved method breakpoints.
 */
const functionGroups = computed<FunctionGroup[]>(() => {
  const types = new Set([
    ...sessions.sessions.filter((session) => sameProjectRoot(session.projectRoot, props.projectRoot)).map((session) => session.type),
    ...state.value.configs.map((cfg) => cfg.type),
    ...Object.keys(state.value.functionBreakpoints),
  ]);
  return [...types].sort().flatMap((type) => {
    const debugger_ = debuggers.value.find((item) => item.type === type && item.functionBreakpoints);
    if (!debugger_) return [];
    const list = state.value.functionBreakpoints[type] ?? [];
    return [{ type, debugger: debugger_, list, status: sessions.functionBreakpointStatusFor(props.projectRoot, type) }];
  });
});

/** The project's field breakpoints by debugger type (types sorted, entries in the order they were set). */
const dataGroups = computed<DataGroup[]>(() => {
  const groups = new Map<string, DebugDataBreakpointEntry[]>();
  for (const entry of sessions.dataBreakpointEntries(props.projectRoot)) {
    const list = groups.get(entry.type) ?? [];
    list.push(entry);
    groups.set(entry.type, list);
  }
  return [...groups.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([type, entries]) => ({ type, entries }));
});

function dataKey(entry: DebugDataBreakpointEntry): string {
  return `${entry.accessType}:${entry.dataId}`;
}

function accessText(accessType: DataBreakpointAccessType): string {
  if (accessType === "read") return t("debug.variables.watchRead");
  if (accessType === "write") return t("debug.variables.watchWrite");
  return `${t("debug.variables.watchWrite")} · ${t("debug.variables.watchRead")}`;
}

/** "Not verified" (with the adapter's reason) when no running session verified it; null otherwise. */
function dataStatusText(entry: DebugDataBreakpointEntry): string | null {
  if (!entry.status || entry.status.verified) return null;
  return entry.status.message
    ? t("debug.breakpoints.dataUnverifiedReason", { message: entry.status.message })
    : t("debug.breakpoints.dataUnverified");
}

function removeData(entry: DebugDataBreakpointEntry) {
  void sessions.removeDataBreakpoint(props.projectRoot, entry.type, entry);
}

function functionText(debugger_: DebuggerInfo, field: "placeholder" | "description"): string {
  const texts = debugger_.functionBreakpoints;
  return resolvePluginTitle({ title: texts?.[field] ?? "", titleLocalized: texts?.[`${field}Localized`] }, localeChain.value);
}

function editFunction(type: string, text: string) {
  functionDrafts[type] = text;
  delete functionErrors[type];
}

function saveFunctions(group: FunctionGroup, list: StoredFunctionBreakpoint[]) {
  configs.setFunctionBreakpoints(props.projectRoot, group.type, list);
  void sessions.syncFunctionBreakpoints(props.projectRoot, group.type);
}

/** Adds the typed name (trimmed, enabled); a blank name is ignored, a refused one says why. */
function addFunction(group: FunctionGroup) {
  const draft = functionDrafts[group.type] ?? "";
  if (!draft.trim()) return;
  const result = normalizeFunctionBreakpointName(draft);
  if (!result.ok) {
    functionErrors[group.type] =
      result.reason === "tooLong"
        ? t("debug.breakpoints.functionNameTooLong", { max: FUNCTION_BREAKPOINT_LIMITS.maxRunes })
        : t("debug.breakpoints.functionNameInvalid");
    return;
  }
  if (group.list.some((item) => item.name === result.name)) {
    functionErrors[group.type] = t("debug.breakpoints.functionDuplicate");
    return;
  }
  if (group.list.length >= FUNCTION_BREAKPOINT_LIMITS.max) {
    functionErrors[group.type] = t("debug.breakpoints.functionsTooMany", { max: FUNCTION_BREAKPOINT_LIMITS.max });
    return;
  }
  delete functionDrafts[group.type];
  saveFunctions(group, [...group.list, { name: result.name, enabled: true }]);
}

function setFunctionEnabled(group: FunctionGroup, name: string, enabled: boolean) {
  saveFunctions(
    group,
    group.list.map((item) => (item.name === name ? { ...item, enabled } : item)),
  );
}

function removeFunction(group: FunctionGroup, name: string) {
  saveFunctions(
    group,
    group.list.filter((item) => item.name !== name),
  );
}

/** "Not verified" (with the adapter's reason) for an enabled name no running session verified; null otherwise. */
function functionStatusText(group: FunctionGroup, item: StoredFunctionBreakpoint): string | null {
  const status = item.enabled ? group.status[item.name] : undefined;
  if (!status || status.verified) return null;
  return status.message
    ? t("debug.breakpoints.functionUnverifiedReason", { message: status.message })
    : t("debug.breakpoints.functionUnverified");
}

function sync(at: BreakpointAt) {
  void sessions.syncBreakpointsForFile(props.projectRoot, at);
}

function setEnabled(row: BreakpointRow, enabled: boolean) {
  if (configs.setBreakpoint(props.projectRoot, row.at, { ...row.breakpoint, enabled })) sync(row.at);
}

function remove(row: BreakpointRow) {
  if (configs.removeBreakpoint(props.projectRoot, row.at, row.breakpoint.line)) sync(row.at);
}

function goTo(row: BreakpointRow) {
  if (typeof row.at === "string") emit("open-location", row.at.replace(/\\/g, "/"), row.breakpoint.line);
  else emit("open-address", row.at.address, row.at.type, row.breakpoint.line);
  emit("update:modelValue", false);
}

function setFilter(group: ExceptionGroup, filter: string, enabled: boolean) {
  const next = enabled ? [...new Set([...group.enabled, filter])] : group.enabled.filter((item) => item !== filter);
  configs.setExceptionFilters(props.projectRoot, group.type, next);
  void sessions.applyExceptionFilters(props.projectRoot, group.type, next);
}

function savedTypesText(type: string): string {
  return (state.value.exceptionTypes[type] ?? []).join("\n");
}

function typesText(type: string): string {
  return typeDrafts[type] ?? savedTypesText(type);
}

function editTypes(type: string, text: string) {
  typeDrafts[type] = text;
  delete typeErrors[type];
}

function typesChanged(type: string): boolean {
  return typeDrafts[type] !== undefined && typeDrafts[type] !== savedTypesText(type);
}

function typesPlaceholder(debugger_: DebuggerInfo): string {
  const texts = debugger_.exceptionTypes;
  return resolvePluginTitle({ title: texts?.placeholder ?? "", titleLocalized: texts?.placeholderLocalized }, localeChain.value);
}

function typesDescription(debugger_: DebuggerInfo): string {
  const texts = debugger_.exceptionTypes;
  return resolvePluginTitle({ title: texts?.description ?? "", titleLocalized: texts?.descriptionLocalized }, localeChain.value);
}

/**
 * Saves a changed list (trimmed, duplicates dropped) and syncs it to the
 * project's running sessions of the debugger. An item the host would refuse
 * is reported next to the input, nothing is saved and the reason is
 * returned; an unchanged list is left alone.
 */
function saveTypes(group: ExceptionGroup): string | null {
  const debugger_ = group.typesDebugger;
  if (!debugger_ || !typesChanged(group.type)) return null;
  const result = normalizeExceptionTypes(parseExceptionTypesText(typesText(group.type)));
  if (!result.ok) {
    const error =
      result.reason === "tooLong"
        ? t("debug.breakpoints.exceptionTypeTooLong", { max: EXCEPTION_TYPE_LIMITS.maxRunes })
        : result.reason === "tooMany"
          ? t("debug.breakpoints.exceptionTypesTooMany", { max: EXCEPTION_TYPE_LIMITS.max })
          : t("debug.breakpoints.exceptionTypeInvalid", { value: result.value });
    typeErrors[group.type] = error;
    return error;
  }
  delete typeErrors[group.type];
  delete typeDrafts[group.type];
  configs.setExceptionTypes(props.projectRoot, group.type, result.types);
  void sessions.syncExceptionTypes(props.projectRoot, debugger_);
  return null;
}

/** Closing the dialog (with the input still focused, e.g. by Esc) saves what was typed too. */
function saveTypesOnClose() {
  for (const group of exceptionGroups.value) {
    const error = saveTypes(group);
    if (error !== null) ElMessage.warning({ message: t("debug.breakpoints.exceptionTypesNotSaved", { error }), grouping: true });
  }
}

function conditionText(row: BreakpointRow): string {
  const breakpoint = row.breakpoint;
  const logMessage = normalizeLogMessage(breakpoint.logMessage);
  const reason = logMessage === undefined ? undefined : sessions.breakpointDetailsFor(props.projectRoot, row.at)[breakpoint.line]?.message;
  const parts = [
    breakpoint.condition,
    breakpoint.hitCondition ? t("debug.breakpoints.hitCondition", { value: breakpoint.hitCondition }) : "",
    logMessage !== undefined ? t("debug.breakpoints.logMessage", { value: logMessage }) : "",
    reason === LOGPOINT_UNSUPPORTED_REASON ? t("debug.editor.logpointsUnsupported") : "",
  ];
  return parts.filter(Boolean).join(" · ");
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="t('debug.breakpoints.title')"
    width="640px"
    append-to-body
    class="debug-breakpoints-dialog"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-table
      v-if="rows.length"
      :data="rows"
      row-key="id"
      size="small"
      max-height="320"
      class="debug-breakpoints-dialog__table"
    >
      <el-table-column :label="t('debug.breakpoints.enabled')" width="64">
        <template #default="{ row }">
          <el-switch
            size="small"
            :model-value="(row as BreakpointRow).breakpoint.enabled"
            :aria-label="t('debug.breakpoints.enabled')"
            @update:model-value="setEnabled(row as BreakpointRow, Boolean($event))"
          />
        </template>
      </el-table-column>
      <el-table-column :label="t('debug.breakpoints.location')" min-width="220">
        <template #default="{ row }">
          <el-button
            link
            native-type="button"
            class="debug-breakpoints-dialog__location"
            :title="(row as BreakpointRow).title"
            @click="goTo(row as BreakpointRow)"
          >
            {{ (row as BreakpointRow).label }}:{{ (row as BreakpointRow).breakpoint.line }}
          </el-button>
          <el-text
            v-if="statusText(row as BreakpointRow)"
            type="warning"
            size="small"
            truncated
            class="debug-breakpoints-dialog__location-status"
            :title="statusText(row as BreakpointRow) ?? undefined"
          >
            {{ statusText(row as BreakpointRow) }}
          </el-text>
        </template>
      </el-table-column>
      <el-table-column :label="t('debug.breakpoints.condition')" min-width="160" show-overflow-tooltip>
        <template #default="{ row }">{{ conditionText(row as BreakpointRow) }}</template>
      </el-table-column>
      <el-table-column width="76" align="right">
        <template #default="{ row }">
          <el-button
            text
            size="small"
            native-type="button"
            :aria-label="t('debug.breakpoints.goTo')"
            :title="t('debug.breakpoints.goTo')"
            @click="goTo(row as BreakpointRow)"
          >
            <el-icon><Position /></el-icon>
          </el-button>
          <el-button
            text
            size="small"
            native-type="button"
            :aria-label="t('debug.breakpoints.remove')"
            :title="t('debug.breakpoints.remove')"
            @click="remove(row as BreakpointRow)"
          >
            <el-icon><Delete /></el-icon>
          </el-button>
        </template>
      </el-table-column>
    </el-table>
    <el-empty v-else :image-size="56" :description="t('debug.breakpoints.empty')" />

    <section v-if="functionGroups.length" class="debug-breakpoints-dialog__functions">
      <h4>{{ t("debug.breakpoints.functions") }}</h4>
      <el-alert
        class="debug-breakpoints-dialog__function-slow"
        type="warning"
        :title="t('debug.breakpoints.functionSlow')"
        :closable="false"
        show-icon
      />
      <div
        v-for="group in functionGroups"
        :key="group.type"
        class="debug-breakpoints-dialog__function-group"
        :data-debugger-type="group.type"
      >
        <el-form class="debug-breakpoints-dialog__function-form" size="small" @submit.prevent="addFunction(group)">
          <span class="debug-breakpoints-dialog__type">{{ group.type }}</span>
          <el-form-item :error="functionErrors[group.type] ?? ''">
            <el-input
              :model-value="functionDrafts[group.type] ?? ''"
              :placeholder="functionText(group.debugger, 'placeholder')"
              :aria-label="t('debug.breakpoints.functionName')"
              spellcheck="false"
              class="debug-breakpoints-dialog__function-input"
              @update:model-value="editFunction(group.type, String($event))"
            />
          </el-form-item>
          <el-button
            class="debug-breakpoints-dialog__function-add"
            native-type="submit"
            :disabled="!(functionDrafts[group.type] ?? '').trim()"
          >
            {{ t("debug.breakpoints.functionAdd") }}
          </el-button>
        </el-form>
        <p v-if="functionText(group.debugger, 'description')" class="debug-breakpoints-dialog__hint debug-breakpoints-dialog__function-hint">
          {{ functionText(group.debugger, "description") }}
        </p>
        <ul v-if="group.list.length" class="debug-breakpoints-dialog__function-list">
          <li
            v-for="item in group.list"
            :key="item.name"
            class="debug-breakpoints-dialog__function"
            :data-function-name="item.name"
          >
            <el-switch
              size="small"
              :model-value="item.enabled"
              :aria-label="t('debug.breakpoints.enabled')"
              @update:model-value="setFunctionEnabled(group, item.name, Boolean($event))"
            />
            <span class="debug-breakpoints-dialog__function-name" :title="item.name">{{ item.name }}</span>
            <el-text
              v-if="functionStatusText(group, item)"
              type="warning"
              size="small"
              truncated
              class="debug-breakpoints-dialog__function-status"
              :title="functionStatusText(group, item) ?? undefined"
            >
              {{ functionStatusText(group, item) }}
            </el-text>
            <el-button
              text
              size="small"
              native-type="button"
              :aria-label="t('debug.breakpoints.remove')"
              :title="t('debug.breakpoints.remove')"
              @click="removeFunction(group, item.name)"
            >
              <el-icon><Delete /></el-icon>
            </el-button>
          </li>
        </ul>
      </div>
    </section>

    <section v-if="dataGroups.length" class="debug-breakpoints-dialog__data">
      <h4>{{ t("debug.breakpoints.dataTitle") }}</h4>
      <div v-for="group in dataGroups" :key="group.type" class="debug-breakpoints-dialog__data-group" :data-debugger-type="group.type">
        <span class="debug-breakpoints-dialog__type">{{ group.type }}</span>
        <ul class="debug-breakpoints-dialog__function-list debug-breakpoints-dialog__data-list">
          <li
            v-for="entry in group.entries"
            :key="dataKey(entry)"
            class="debug-breakpoints-dialog__function debug-breakpoints-dialog__data-item"
            :data-data-id="entry.dataId"
          >
            <span class="debug-breakpoints-dialog__function-name" :title="entry.dataId">{{ entry.description }}</span>
            <span class="debug-breakpoints-dialog__data-access">{{ accessText(entry.accessType) }}</span>
            <el-tag v-if="entry.sessionOnly" size="small" type="info" disable-transitions>{{ t("debug.breakpoints.dataSessionOnly") }}</el-tag>
            <el-text
              v-if="dataStatusText(entry)"
              type="warning"
              size="small"
              truncated
              class="debug-breakpoints-dialog__function-status"
              :title="dataStatusText(entry) ?? undefined"
            >
              {{ dataStatusText(entry) }}
            </el-text>
            <el-button
              text
              size="small"
              native-type="button"
              :aria-label="t('debug.breakpoints.remove')"
              :title="t('debug.breakpoints.remove')"
              @click="removeData(entry)"
            >
              <el-icon><Delete /></el-icon>
            </el-button>
          </li>
        </ul>
      </div>
    </section>

    <section class="debug-breakpoints-dialog__exceptions">
      <h4>{{ t("debug.breakpoints.exceptions") }}</h4>
      <p v-if="!exceptionGroups.length" class="debug-breakpoints-dialog__hint">{{ t("debug.breakpoints.noExceptionFilters") }}</p>
      <div v-for="group in exceptionGroups" :key="group.type" class="debug-breakpoints-dialog__debugger" :data-debugger-type="group.type">
        <div class="debug-breakpoints-dialog__group">
          <span class="debug-breakpoints-dialog__type">{{ group.type }}</span>
          <el-checkbox
            v-for="filter in group.filters"
            :key="filter.filter"
            :model-value="group.enabled.includes(filter.filter)"
            :title="filter.description"
            @update:model-value="setFilter(group, filter.filter, Boolean($event))"
          >
            {{ filter.label }}
          </el-checkbox>
        </div>
        <el-form
          v-if="group.typesDebugger"
          class="debug-breakpoints-dialog__types"
          label-position="top"
          size="small"
          @submit.prevent
        >
          <el-form-item :label="t('debug.breakpoints.exceptionTypes')" :error="typeErrors[group.type] ?? ''">
            <el-input
              :model-value="typesText(group.type)"
              type="textarea"
              :autosize="{ minRows: 2, maxRows: 6 }"
              :placeholder="typesPlaceholder(group.typesDebugger)"
              :aria-label="t('debug.breakpoints.exceptionTypes')"
              spellcheck="false"
              class="debug-breakpoints-dialog__types-input"
              @update:model-value="editTypes(group.type, String($event))"
              @blur="saveTypes(group)"
            />
          </el-form-item>
          <p v-if="typesDescription(group.typesDebugger)" class="debug-breakpoints-dialog__hint">{{ typesDescription(group.typesDebugger) }}</p>
          <p class="debug-breakpoints-dialog__hint">{{ t("debug.breakpoints.exceptionTypesHint") }}</p>
        </el-form>
      </div>
    </section>
  </el-dialog>
</template>
