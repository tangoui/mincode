<script setup lang="ts">
/**
 * Plugin services (contributes.services) inside a host menu, for what the
 * menu was opened on (`target`, a ServiceSurfaceTarget, or null while no
 * menu is open): the services servicesForSurface offers for it. By default a
 * "Process with plugin" entry expands inline, keeping the menu open (mouse
 * and keyboard); services beyond MENU_VISIBLE_PLUGIN_ITEMS collapse behind a
 * nested "More services" expander, like PluginMenuItems. `flat` lists every
 * service directly (a dropdown of its own, such as the HTTP response
 * button). A service that cannot run now is greyed out with its reason.
 * Renders nothing when no service matches.
 *
 * Nothing is read or serialized while the menu is open: choosing a service
 * builds the request (surfaceRunRequest; a result set is serialized only
 * then) and hands it to runPluginService, which collects params and shows
 * progress and results. A result set's rows are read once per opened target
 * (only to know it is not empty), never on every re-render.
 *
 * The items handle their own @click and carry no `command`: a host
 * `<el-dropdown @command>` sharing the menu receives `undefined` for them and
 * must ignore it.
 */
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { ArrowDown, ArrowRight, MagicStick } from "@element-plus/icons-vue";
import { splitMenuOverflow } from "@/domain/plugins/menus";
import { servicesForSurface, surfaceRunRequest, type ServiceSurfaceGroup, type ServiceSurfaceTarget } from "@/domain/plugins/service-surfaces";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { pluginServices, runPluginService } from "@/services/plugin-services";

const props = defineProps<{
  /** What the open menu was opened on; null while no menu is open. */
  target: () => ServiceSurfaceTarget | null;
  /** List the services directly, without the "Process with plugin" expander. */
  flat?: boolean;
}>();

const emit = defineEmits<{
  /** Emitted after a service was chosen (the dropdown closes itself). */
  run: [];
}>();

const { t, localeChain } = useVueI18n();
const expanded = ref(false);
const moreExpanded = ref(false);

const target = computed(() => props.target());

/**
 * The target servicesForSurface sees: a result set's rows() is read at most
 * once per target object (its emptiness is all the menu needs), so grid
 * updates while the menu is open do not re-read the rows. A click reads the
 * live target again.
 */
const memoRows = new WeakMap<object, unknown[][]>();
function menuTarget(current: ServiceSurfaceTarget): ServiceSurfaceTarget {
  if (current.surface !== "dbResult") return current;
  return {
    ...current,
    rows: () => {
      let rows = memoRows.get(current);
      if (!rows) {
        rows = current.rows();
        memoRows.set(current, rows);
      }
      return rows;
    },
  };
}

const groups = computed(() => (target.value ? servicesForSurface(pluginServices.value, menuTarget(target.value), localeChain.value) : []));
const view = computed(() => splitMenuOverflow(groups.value));

/** What the menu is open on, for collapsing the expanders when it changes. */
function targetKey(current: ServiceSurfaceTarget | null): string {
  if (!current) return "";
  switch (current.surface) {
    case "fileMenu":
      return `fileMenu\u0000${current.files.map((file) => file.absolutePath).join("\u0000")}`;
    case "sftpFile":
      return `sftpFile\u0000${current.sessionId}\u0000${current.remote.path}`;
    case "dbResult":
      return `dbResult\u0000${current.columns.map((column) => column.name).join("\u0000")}`;
    case "httpResponse":
      return `httpResponse\u0000${current.request?.method}\u0000${current.request?.url}\u0000${current.response.status}\u0000${current.response.sizeBytes}`;
    case "terminalSelection":
      return `terminalSelection\u0000${current.kind}\u0000${current.selection}`;
  }
}

// Every menu opening (another target) starts collapsed.
watch(
  () => targetKey(target.value),
  () => {
    expanded.value = false;
    moreExpanded.value = false;
  },
);

function choose(group: ServiceSurfaceGroup) {
  const current = props.target();
  if (group.disabledReason || !current) return;
  const { notice, refused, ...request } = surfaceRunRequest(current, group);
  emit("run");
  if (refused) {
    ElMessage.warning({ message: t(refused.key as MessageKey), grouping: true });
    return;
  }
  if (notice) ElMessage.info({ message: t(notice.key as MessageKey, { count: notice.count }), grouping: true });
  void runPluginService(request);
}

type Expander = "services" | "more";

function flip(expander: Expander) {
  const state = expander === "services" ? expanded : moreExpanded;
  state.value = !state.value;
}

function toggle(event: Event, expander: Expander) {
  // A prevented click keeps the Element Plus dropdown open.
  event.preventDefault();
  flip(expander);
}

function handleToggleKeydown(event: KeyboardEvent, expander: Expander) {
  if (event.key !== "Enter" && event.key !== " ") return;
  // Element Plus closes on keyboard activation regardless of preventDefault;
  // stop its handler (merged after this one) and toggle here instead.
  event.preventDefault();
  event.stopImmediatePropagation();
  flip(expander);
}

function reasonText(group: ServiceSurfaceGroup) {
  return group.disabledReason ? t(`plugins.services.disabled.${group.disabledReason}`) : "";
}

function itemLabel(group: ServiceSurfaceGroup) {
  const label = t("plugins.services.menuItem", { title: group.title });
  return group.disabledReason ? t("plugins.services.disabledItem", { title: label, reason: reasonText(group) }) : label;
}

function itemTooltip(group: ServiceSurfaceGroup) {
  return group.disabledReason ? reasonText(group) : group.providers[0]?.description || itemLabel(group);
}
</script>

<template>
  <template v-if="groups.length > 0">
    <template v-if="flat">
      <el-dropdown-item
        v-for="group in groups"
        :key="group.service"
        class="app-plugin-menu-item"
        :disabled="Boolean(group.disabledReason)"
        :title="itemTooltip(group)"
        :aria-label="itemLabel(group)"
        @click="choose(group)"
      >
        <el-icon><MagicStick /></el-icon>
        <span class="app-plugin-menu-item__title">{{ group.title }}</span>
        <span v-if="group.disabledReason" class="app-plugin-menu-item__reason">{{ reasonText(group) }}</span>
      </el-dropdown-item>
    </template>
    <template v-else>
      <el-dropdown-item
        divided
        class="app-plugin-menu-item app-plugin-menu-item--more"
        :aria-expanded="expanded"
        :aria-label="t('plugins.services.menu')"
        @click="toggle($event, 'services')"
        @keydown="handleToggleKeydown($event, 'services')"
      >
        <el-icon><component :is="expanded ? ArrowDown : ArrowRight" /></el-icon>
        <span class="app-plugin-menu-item__title">{{ t("plugins.services.menu") }}</span>
      </el-dropdown-item>
      <template v-if="expanded">
        <el-dropdown-item
          v-for="group in view.visible"
          :key="group.service"
          class="app-plugin-menu-item app-plugin-menu-item--nested"
          :disabled="Boolean(group.disabledReason)"
          :title="itemTooltip(group)"
          :aria-label="itemLabel(group)"
          @click="choose(group)"
        >
          <span class="app-plugin-menu-item__indent" aria-hidden="true" />
          <el-icon><MagicStick /></el-icon>
          <span class="app-plugin-menu-item__title">{{ group.title }}</span>
          <span v-if="group.disabledReason" class="app-plugin-menu-item__reason">{{ reasonText(group) }}</span>
        </el-dropdown-item>
        <template v-if="view.overflow.length > 0">
          <el-dropdown-item
            class="app-plugin-menu-item app-plugin-menu-item--more app-plugin-menu-item--nested"
            :aria-expanded="moreExpanded"
            :aria-label="t('plugins.services.moreCount', { count: view.overflow.length })"
            @click="toggle($event, 'more')"
            @keydown="handleToggleKeydown($event, 'more')"
          >
            <span class="app-plugin-menu-item__indent" aria-hidden="true" />
            <el-icon><component :is="moreExpanded ? ArrowDown : ArrowRight" /></el-icon>
            <span class="app-plugin-menu-item__title">{{ t("plugins.services.moreCount", { count: view.overflow.length }) }}</span>
          </el-dropdown-item>
          <template v-if="moreExpanded">
            <el-dropdown-item
              v-for="group in view.overflow"
              :key="group.service"
              class="app-plugin-menu-item app-plugin-menu-item--nested"
              :disabled="Boolean(group.disabledReason)"
              :title="itemTooltip(group)"
              :aria-label="itemLabel(group)"
              @click="choose(group)"
            >
              <span class="app-plugin-menu-item__indent app-plugin-menu-item__indent--double" aria-hidden="true" />
              <el-icon><MagicStick /></el-icon>
              <span class="app-plugin-menu-item__title">{{ group.title }}</span>
              <span v-if="group.disabledReason" class="app-plugin-menu-item__reason">{{ reasonText(group) }}</span>
            </el-dropdown-item>
          </template>
        </template>
      </template>
    </template>
  </template>
</template>

<style scoped>
.app-plugin-menu-item__title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-plugin-menu-item__reason {
  flex: none;
  margin-left: auto;
  padding-left: 12px;
  font-size: 12px;
  color: var(--app-text-muted);
}

.app-plugin-menu-item__indent {
  flex: none;
  width: 12px;
}

.app-plugin-menu-item__indent--double {
  width: 24px;
}
</style>
