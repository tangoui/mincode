<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Connection, Refresh } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import type { SshPortForwardRule, SshPortForwardStatus } from "@/domain/ssh/types";
import { useVueI18n } from "@/i18n/messages";
import {
  listSshPortForwards,
  resumeSshPortForward,
  stopSshPortForward,
} from "@/services/ssh-port-forward-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

const props = withDefaults(
  defineProps<{
    sessionId: string;
    rules: SshPortForwardRule[];
    connectionReady?: boolean;
  }>(),
  {
    connectionReady: false,
  }
);

const emit = defineEmits<{
  configure: [];
}>();

const { t } = useVueI18n();
const popoverOpen = ref(false);
const refreshing = ref(false);
const changingRuleId = ref<string | null>(null);
const statusesByRuleId = ref(new Map<string, SshPortForwardStatus>());
const warnedSessionId = ref("");

const hasRules = computed(() => props.rules.length > 0);
const desktopRuntime = computed(() => isDesktopRuntime());
const canManage = computed(
  () => desktopRuntime.value && props.connectionReady && Boolean(props.sessionId)
);
const runningCount = computed(
  () => props.rules.filter((rule) => statusesByRuleId.value.get(rule.id)?.isRunning).length
);
const failedCount = computed(
  () => props.rules.filter((rule) => Boolean(statusesByRuleId.value.get(rule.id)?.errorCode)).length
);
const toolbarLabel = computed(() => {
  if (!hasRules.value) return t("ssh.portForward.toolbar");
  if (failedCount.value > 0) {
    return t("ssh.portForward.toolbarSummaryWithFailures", {
      running: runningCount.value,
      total: props.rules.length,
      failed: failedCount.value,
    });
  }
  return t("ssh.portForward.toolbarSummary", {
    running: runningCount.value,
    total: props.rules.length,
  });
});

function ruleAddress(rule: SshPortForwardRule) {
  if (rule.type === "D") return `${rule.type} 127.0.0.1:${rule.bindPort}`;
  return `${rule.type} ${rule.bindPort} → ${rule.targetHost}:${rule.targetPort}`;
}

function errorMessage(error: unknown, fallbackKey: "ssh.portForward.statusFailed" | "ssh.portForward.actionFailed") {
  return error instanceof Error && error.message ? error.message : t(fallbackKey);
}

function ruleStatus(rule: SshPortForwardRule) {
  return statusesByRuleId.value.get(rule.id);
}

function isRuleRunning(rule: SshPortForwardRule) {
  return ruleStatus(rule)?.isRunning === true;
}

function hasRuleFailure(rule: SshPortForwardRule) {
  return Boolean(ruleStatus(rule)?.errorCode);
}

function ruleActionLabelKey(rule: SshPortForwardRule) {
  if (isRuleRunning(rule)) return "ssh.portForward.stop" as const;
  return hasRuleFailure(rule)
    ? ("ssh.portForward.retry" as const)
    : ("ssh.portForward.start" as const);
}

function ruleActionAriaLabelKey(rule: SshPortForwardRule) {
  return hasRuleFailure(rule) && !isRuleRunning(rule)
    ? ("ssh.portForward.retryRule" as const)
    : ruleActionLabelKey(rule);
}

function ruleFailureMessage(rule: SshPortForwardRule) {
  const status = ruleStatus(rule);
  if (!status?.errorCode) return "";
  if (status.errorCode === "address_in_use") {
    return t(
      rule.type === "R"
        ? "ssh.portForward.remoteAddressInUse"
        : "ssh.portForward.localAddressInUse",
      { port: rule.bindPort }
    );
  }
  if (status.errorCode === "invalid_rule") {
    return t("ssh.portForward.invalidRule", {
      message: status.errorMessage || t("ssh.portForward.actionFailed"),
    });
  }
  return t("ssh.portForward.startFailedDetail", {
    message: status.errorMessage || t("ssh.portForward.actionFailed"),
  });
}

async function refreshStatuses(options: { notifyFailures?: boolean; showLoadError?: boolean } = {}) {
  if (!canManage.value || !hasRules.value) return;
  refreshing.value = true;
  try {
    const statuses = await listSshPortForwards(props.sessionId);
    statusesByRuleId.value = new Map(statuses.map((status) => [status.ruleId, status]));
    const failures = statuses.filter((status) => !status.isRunning && status.errorCode);
    if (
      options.notifyFailures &&
      failures.length > 0 &&
      warnedSessionId.value !== props.sessionId
    ) {
      warnedSessionId.value = props.sessionId;
      ElMessage.warning(
        t("ssh.portForward.startupWarning", {
          count: failures.length,
        })
      );
    }
  } catch (error) {
    if (options.showLoadError !== false) {
      ElMessage.error(errorMessage(error, "ssh.portForward.statusFailed"));
    }
  } finally {
    refreshing.value = false;
  }
}

function onPopoverShow() {
  void refreshStatuses({ showLoadError: true });
}

async function toggleRule(rule: SshPortForwardRule) {
  if (!canManage.value || changingRuleId.value) return;
  const wasRunning = isRuleRunning(rule);
  changingRuleId.value = rule.id;
  try {
    if (wasRunning) {
      await stopSshPortForward(props.sessionId, rule.id);
      ElMessage.success(t("ssh.portForward.stopped"));
    } else {
      await resumeSshPortForward(props.sessionId, rule);
      ElMessage.success(t("ssh.portForward.started"));
    }
    await refreshStatuses({ showLoadError: true });
  } catch (error) {
    await refreshStatuses({ showLoadError: false });
    ElMessage.error(
      ruleFailureMessage(rule) || errorMessage(error, "ssh.portForward.actionFailed")
    );
  } finally {
    changingRuleId.value = null;
  }
}

watch(
  () => [props.sessionId, props.rules.map((rule) => rule.id).join("|"), props.connectionReady] as const,
  ([sessionId, , connectionReady], previous) => {
    statusesByRuleId.value = new Map();
    if (previous?.[0] !== sessionId) warnedSessionId.value = "";
    if (connectionReady && canManage.value) {
      void refreshStatuses({ notifyFailures: true, showLoadError: false });
    }
  },
  { immediate: true }
);
</script>

<template>
  <el-popover
    v-model:visible="popoverOpen"
    placement="bottom-end"
    :width="340"
    trigger="click"
    popper-class="ssh-port-forward-popper"
    @show="onPopoverShow"
  >
    <template #reference>
      <span class="ssh-port-forward-trigger">
        <el-tooltip :content="toolbarLabel" placement="bottom" :teleported="false">
          <el-button
            text
            :icon="Connection"
            class="ssh-port-forward-toggle"
            :class="{ 'is-active': runningCount > 0, 'has-error': failedCount > 0 }"
            :aria-label="toolbarLabel"
            aria-haspopup="dialog"
            :aria-expanded="popoverOpen"
            @mousedown.stop
          />
        </el-tooltip>
      </span>
    </template>

    <section class="ssh-port-forward-menu" :aria-label="t('ssh.portForward.title')">
      <header class="ssh-port-forward-menu__header">
        <div>
          <strong>{{ t("ssh.portForward.title") }}</strong>
          <span v-if="hasRules">{{ t("ssh.portForward.runningSummary", { running: runningCount, total: rules.length }) }}</span>
        </div>
        <el-tooltip
          :content="t('ssh.portForward.refreshStatus')"
          placement="bottom"
          :teleported="false"
        >
          <el-button
            text
            :icon="Refresh"
            :loading="refreshing"
            :disabled="!canManage || refreshing || !hasRules"
            :aria-label="t('ssh.portForward.refreshStatus')"
            @click="refreshStatuses"
          />
        </el-tooltip>
      </header>

      <div v-if="!hasRules" class="ssh-port-forward-menu__empty">
        <p>{{ t("ssh.portForward.noRules") }}</p>
        <el-button text class="ssh-port-forward-menu__configure" @click="emit('configure')">
          {{ t("ssh.portForward.configure") }}
        </el-button>
      </div>
      <p v-else-if="!desktopRuntime" class="ssh-port-forward-menu__notice">
        {{ t("terminal.onlyDesktop") }}
      </p>
      <p v-else-if="!connectionReady" class="ssh-port-forward-menu__notice">
        {{ t("ssh.portForward.connectionRequired") }}
      </p>
      <div v-else class="ssh-port-forward-menu__list">
        <article v-for="rule in rules" :key="rule.id" class="ssh-port-forward-menu__rule">
          <div class="ssh-port-forward-menu__rule-main">
            <span class="ssh-port-forward-menu__route">{{ ruleAddress(rule) }}</span>
            <span v-if="rule.label" class="ssh-port-forward-menu__label">{{ rule.label }}</span>
          </div>
          <div class="ssh-port-forward-menu__rule-actions">
            <span
              class="ssh-port-forward-menu__status"
              :class="{
                'is-running': isRuleRunning(rule),
                'has-error': hasRuleFailure(rule),
              }"
            >
              {{ t(isRuleRunning(rule) ? "ssh.portForward.running" : hasRuleFailure(rule) ? "ssh.portForward.failed" : "ssh.portForward.stopped") }}
            </span>
            <el-button
              class="ssh-port-forward-menu__action"
              :class="{ 'is-retry': hasRuleFailure(rule) && !isRuleRunning(rule) }"
              :loading="changingRuleId === rule.id"
              :disabled="changingRuleId !== null"
              :aria-label="t(ruleActionAriaLabelKey(rule))"
              :aria-busy="changingRuleId === rule.id"
              @click="toggleRule(rule)"
            >
              {{ t(ruleActionLabelKey(rule)) }}
            </el-button>
          </div>
          <span v-if="ruleFailureMessage(rule)" class="ssh-port-forward-menu__error">
            {{ ruleFailureMessage(rule) }}
          </span>
        </article>
      </div>
    </section>
  </el-popover>
</template>
