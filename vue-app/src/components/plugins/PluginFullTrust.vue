<script setup lang="ts">
import { computed, defineAsyncComponent, h, onMounted, ref, useId, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Unlock } from "@element-plus/icons-vue";
import { fullTrustDisplayText as displayText, type FullTrustStatus } from "@/domain/plugins/full-trust";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { getFullTrust, getFullTrustAudit, setFullTrust } from "@/services/plugin-full-trust";
import { useAppLockStore } from "@/stores/app-lock";
import "@/styles/workspaces/plugins/plugin-full-trust.css";

// Full trust switch of one plugin (spec §七). Go is the only judge: turning it
// on shows the single warning and sends the fingerprint Go returned just
// before it, so a plugin that changed meanwhile is refused (`changed:`).
// Turning it off never asks. Name, ID and version come from the host plugin
// list (props), never from anything the plugin says at runtime.
const PluginFullTrustAudit = defineAsyncComponent(() => import("@/components/plugins/PluginFullTrustAudit.vue"));

// The explicit undefined default keeps an omitted `trusted` undefined (Vue casts absent booleans to false).
const props = withDefaults(defineProps<{
  pluginId: string;
  pluginName: string;
  pluginVersion: string;
  /** Trust state already known from the plugin list: Go is then asked only when the panel opens. */
  trusted?: boolean;
}>(), { trusted: undefined });
const emit = defineEmits<{ changed: [trusted: boolean] }>();
const { t } = useVueI18n();
const appLock = useAppLockStore();
const status = ref<FullTrustStatus>();
const saving = ref(false);
const statusFailed = ref(false);
const auditCount = ref<number>();
const auditVisible = ref(false);
const reasonId = `plugin-full-trust-reason-${useId()}`;
const switchRef = ref<{ $el?: Element }>();

const NOT_ELIGIBLE = new Set(["not_installed", "builtin", "development", "declarative", "no_operations", "disabled", "package_unreadable"]);

const trusted = computed(() => (status.value ? status.value.trusted === true : props.trusted === true));
const blockedReason = computed(() => {
  const subject = status.value?.subject;
  if (subject && !subject.eligible) return notEligibleText(subject.reason ?? "");
  if (!trusted.value && appLock.locked) return t("plugins.fullTrust.locked");
  return "";
});
// ElSwitch has no aria-describedby prop and puts fall-through attributes on its
// wrapper, not on the role="switch" input, so the reason is linked here.
watch([switchRef, blockedReason], ([control, reason]) => {
  const input = control?.$el?.querySelector("input");
  if (!input) return;
  if (reason) input.setAttribute("aria-describedby", reasonId);
  else input.removeAttribute("aria-describedby");
}, { flush: "post" });
// Revoking is always allowed; only turning on can be blocked.
const switchDisabled = computed(() => saving.value || (!trusted.value && blockedReason.value !== ""));

function notEligibleText(reason: string) {
  return NOT_ELIGIBLE.has(reason) ? t(`plugins.fullTrust.notEligible.${reason}` as MessageKey) : reason;
}

onMounted(async () => {
  // A page of plugin cards would otherwise query Go once per card on every page.
  if (props.trusted !== undefined) return;
  try {
    status.value = await getFullTrust(props.pluginId);
  } catch {
    status.value = undefined;
  }
});

/** First open of a card whose trust state came from the plugin list: read the full status now. */
async function ensureStatus() {
  if (status.value) return;
  statusFailed.value = false;
  try {
    status.value = await getFullTrust(props.pluginId);
  } catch {
    statusFailed.value = true;
  }
}

/** On every open: Go may have ended trust since (package change, lapse on read). */
async function onShow() {
  try {
    status.value = await getFullTrust(props.pluginId);
  } catch {
    // Keep the last known state; turning on re-reads it anyway.
  }
  await loadAuditCount();
}

async function loadAuditCount() {
  try {
    const entries = await getFullTrustAudit(props.pluginId);
    auditCount.value = entries.filter((entry) => entry.kind === "skipped").reduce((sum, entry) => sum + entry.count, 0);
  } catch {
    auditCount.value = undefined;
  }
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/** Maps Go's error prefixes (full_trust_commands.go) to the user's next step. */
function reportSetError(error: unknown) {
  const message = errorText(error);
  const notEligible = /\bnot_eligible:(\w*)/.exec(message);
  if (/\bchanged:/.test(message)) ElMessage.warning(t("plugins.fullTrust.changed"));
  else if (/\blocked:/.test(message)) ElMessage.warning(t("plugins.fullTrust.locked"));
  else if (notEligible) ElMessage.warning(notEligibleText(notEligible[1] ?? ""));
  else ElMessage.error(t("plugins.fullTrust.setFailed", { error: message }));
}


type WarningValues = { name: string; id: string; version: string };

/**
 * The warning paragraphs. The template is split before anything is filled in
 * and every placeholder becomes its own text node, so a plugin name can neither
 * add paragraphs nor be parsed as markup; each value is a <bdi>, so its
 * direction cannot reorder the surrounding warning.
 */
function warningBody(values: WarningValues) {
  const paragraphs = t("plugins.fullTrust.confirmBody").split("\n\n").map((paragraph) =>
    h("p", paragraph.split(/(\{(?:name|id|version)\})/).filter(Boolean).map((part) => {
      const key = /^\{(name|id|version)\}$/.exec(part)?.[1] as keyof WarningValues | undefined;
      return key ? h("bdi", { class: "plugin-full-trust__value" }, values[key]) : part;
    })));
  return h("div", { class: "plugin-full-trust__warning" }, paragraphs);
}

/** The plugin Go would fingerprint is not the one this card shows: the list is stale. */
function subjectDiffers(subject: FullTrustStatus["subject"]) {
  return subject.pluginId !== props.pluginId
    || subject.version !== props.pluginVersion
    || subject.name.trim() !== props.pluginName.trim();
}

async function enable() {
  // Review what Go would trust right now; the warning names the host's copy of the plugin.
  // Only here does Go hash the package for the fingerprint (mount and show stay cheap).
  const current = await getFullTrust(props.pluginId, { full: true });
  status.value = current;
  if (!current.subject.eligible) return;
  if (!current.subject.fingerprint) {
    ElMessage.warning(t("plugins.fullTrust.changed"));
    return;
  }
  if (subjectDiffers(current.subject)) {
    ElMessage.warning(t("plugins.fullTrust.changed"));
    return;
  }
  // Show exactly what Go will fingerprint (its manifest copy), never runtime text.
  const values = { name: displayText(current.subject.name), id: displayText(current.subject.pluginId), version: displayText(current.subject.version) };
  try {
    await ElMessageBox.confirm(warningBody(values), t("plugins.fullTrust.confirmTitle", { name: values.name }), {
      type: "warning",
      confirmButtonText: t("plugins.fullTrust.confirmButton"),
      cancelButtonText: t("common.cancel"),
      confirmButtonClass: "el-button--danger",
      customClass: "plugin-full-trust__dialog",
      distinguishCancelAndClose: true,
      autofocus: false,
      closeOnClickModal: false,
    });
  } catch {
    return;
  }
  status.value = await setFullTrust(props.pluginId, true, current.subject.fingerprint);
}

async function toggle(value: boolean) {
  if (saving.value) return;
  if (value && blockedReason.value) return;
  saving.value = true;
  try {
    if (value) await enable();
    else status.value = await setFullTrust(props.pluginId, false);
  } catch (error) {
    reportSetError(error);
  } finally {
    saving.value = false;
  }
  if (status.value && status.value.trusted === value) emit("changed", value);
}

async function revokeLegacy() {
  if (saving.value) return;
  saving.value = true;
  try {
    status.value = await setFullTrust(props.pluginId, false);
  } catch (error) {
    reportSetError(error);
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <!-- persistent=false: the panel renders only while open (ElPopover keeps it in the document by default). -->
  <el-popover
    v-if="status || props.trusted !== undefined"
    trigger="click"
    :persistent="false"
    placement="top-end"
    :width="340"
    popper-class="plugin-full-trust-popper"
    role="dialog"
    @show="onShow"
  >
    <template #reference>
      <el-button
        text
        :icon="Unlock"
        class="plugin-full-trust__button"
        :class="{ 'is-trusted': trusted }"
        @click="ensureStatus"
      >
        {{ t("plugins.fullTrust.button") }}
      </el-button>
    </template>
    <p v-if="!status && statusFailed" class="plugin-full-trust__panel plugin-full-trust__reason" role="alert">{{ t("plugins.fullTrust.statusFailed") }}</p>
    <div v-else-if="!status" v-loading="true" class="plugin-full-trust__panel is-loading" />
    <div v-else class="plugin-full-trust__panel">
      <div class="plugin-full-trust__row">
        <div class="plugin-full-trust__text">
          <strong>{{ t("plugins.fullTrust.label") }}</strong>
          <small>{{ t("plugins.fullTrust.hint") }}</small>
        </div>
        <el-switch
          size="small"
          :model-value="trusted"
          :loading="saving"
          :disabled="switchDisabled"
          :aria-label="t('plugins.fullTrust.label')"
          ref="switchRef"
          @change="toggle($event === true)"
        />
      </div>
      <p v-if="blockedReason" :id="reasonId" class="plugin-full-trust__reason" role="note">{{ blockedReason }}</p>
      <div v-if="status.legacyDangerous" class="plugin-full-trust__legacy">
        <span>{{ t("plugins.fullTrust.legacyLabel") }}</span>
        <el-button link type="danger" size="small" :disabled="saving" @click="revokeLegacy">
          {{ t("plugins.fullTrust.legacyRevoke") }}
        </el-button>
      </div>
      <el-button link type="primary" size="small" class="plugin-full-trust__audit-link" @click="auditVisible = true">
        {{ t("plugins.fullTrust.auditOpen", { count: auditCount ?? "…" }) }}
      </el-button>
    </div>
  </el-popover>
  <PluginFullTrustAudit
    v-if="auditVisible"
    v-model="auditVisible"
    :plugin-id="pluginId"
    :plugin-name="displayText(pluginName)"
  />
</template>
