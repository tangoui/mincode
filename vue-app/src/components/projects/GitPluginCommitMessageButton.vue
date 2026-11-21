<script setup lang="ts">
/**
 * "插件生成" next to "AI 生成" in the Git commit box (contributes.scmCommitMessage).
 * Mounted lazily by ProjectGitPanel only when an active provider exists. One
 * provider is a plain button; several open a menu ("{title} · {plugin}").
 * Only the user's click runs a provider; clicking again while it runs
 * cancels the host request. The answer is handed to the parent as draft text
 * (finish); the parent decides whether it may replace the draft and nothing
 * here ever commits. Mutually exclusive with AI generation: `busy` disables
 * the button while the AI runs.
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { Connection } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import type { PluginScmProvider } from "@/domain/plugins/scm-commit-message";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { useVueI18n } from "@/i18n/messages";
import type { ContributionError } from "@/services/plugin-contribution-requests";
import { providerErrorText } from "@/services/plugin-provider-invoke";
import { generatePluginCommitMessage } from "@/services/plugin-scm-commit-message";

const props = defineProps<{
  providers: PluginScmProvider[];
  projectRoot: string;
  repoPath: string;
  hasStaged: boolean;
  /** The draft when the user clicks; sent back with the result, never to the host. */
  draft: string;
  /** AI generation is running for this repository. */
  busy: boolean;
  /** This button's generation is running for this repository. */
  generating: boolean;
}>();

const emit = defineEmits<{
  start: [];
  finish: [result: { message?: string; draftAtStart: string }];
  cancel: [];
}>();

const { t, localeChain } = useVueI18n();
// The in-flight request; not reactive except for the running flag.
let inflight: { controller: AbortController; repoPath: string } | undefined;
const running = ref(false);
const active = computed(() => props.generating || running.value);
// The dropdown trigger and the cancel button replace each other; focus follows the swap.
const triggerButton = ref<{ ref?: HTMLButtonElement } | null>(null);
const actionButton = ref<{ ref?: HTMLButtonElement } | null>(null);

function providerKey(provider: PluginScmProvider) {
  return `${provider.pluginId}/${provider.id}`;
}

function providerTitle(provider: PluginScmProvider) {
  return resolvePluginTitle(provider, localeChain.value);
}

function pluginName(provider: PluginScmProvider) {
  return provider.pluginName || provider.pluginId;
}

const buttonTitle = computed(() => {
  if (active.value) return t("common.cancel");
  const [only] = props.providers;
  return props.providers.length === 1 && only
    ? t("git.pluginGenerateTitle", { plugin: pluginName(only), title: providerTitle(only) })
    : t("git.pluginGenerate");
});

/** After a swap, moves focus to the new button unless the user already moved it elsewhere. */
function moveFocus(target: typeof triggerButton, previous: typeof triggerButton) {
  const old = previous.value?.ref;
  const hadFocus = !document.activeElement || document.activeElement === document.body || document.activeElement === old;
  void nextTick(() => {
    const focused = document.activeElement;
    if (hadFocus || !focused || focused === document.body) target.value?.ref?.focus();
  });
}

const ERROR_KEYS = { forbidden: "git.pluginGenerateForbidden", timeout: "git.pluginGenerateTimeout" } as const;

function errorReason(error: ContributionError) {
  return error.code === "forbidden" || error.code === "timeout" ? t(ERROR_KEYS[error.code]) : providerErrorText(error);
}

function abortInflight() {
  inflight?.controller.abort();
  inflight = undefined;
  running.value = false;
}

function cancel() {
  abortInflight();
  emit("cancel");
  if (props.providers.length > 1) moveFocus(triggerButton, actionButton);
}

async function run(provider: PluginScmProvider) {
  if (props.busy) return;
  if (active.value) {
    cancel();
    return;
  }
  if (!props.hasStaged) {
    ElMessage.warning(t("git.pluginGenerateRequiresStaged"));
    return;
  }
  const request = { controller: new AbortController(), repoPath: props.repoPath };
  inflight = request;
  running.value = true;
  const draftAtStart = props.draft;
  emit("start");
  if (props.providers.length > 1) moveFocus(actionButton, triggerButton);
  const result = await generatePluginCommitMessage(provider, props.projectRoot, request.repoPath, request.controller.signal);
  // Cancelled, unmounted or the repository changed meanwhile: the parent already reset the draft state.
  if (inflight !== request) return;
  inflight = undefined;
  running.value = false;
  if (props.providers.length > 1) moveFocus(triggerButton, actionButton);
  if (!result.ok) {
    if (result.error.code !== "cancelled") ElMessage.error(t("git.pluginGenerateFailed", { reason: errorReason(result.error) }));
    emit("finish", { draftAtStart });
    return;
  }
  const { message, omittedFiles, diffTruncated } = result.response;
  if (omittedFiles > 0) ElMessage.info(t("git.pluginGenerateOmitted", { count: omittedFiles }));
  if (diffTruncated) ElMessage.info(t("git.pluginGenerateTruncated"));
  emit("finish", { message, draftAtStart });
}

function onClick() {
  const [only] = props.providers;
  if (active.value) cancel();
  else if (only) void run(only);
}

function onCommand(key: unknown) {
  const provider = props.providers.find((item) => providerKey(item) === key);
  if (provider) void run(provider);
}

// The parent cancelled (another repository, page left): drop the request without a second cancel event.
watch(
  () => props.generating,
  (now) => {
    if (!now && inflight?.repoPath === props.repoPath) abortInflight();
  },
);
watch(
  () => props.repoPath,
  () => {
    if (inflight) cancel();
  },
);
// Unmounted mid-run (the provider was disabled, uninstalled or left the workspace): the parent must reset its state.
onBeforeUnmount(() => {
  if (inflight) cancel();
});
</script>

<template>
  <el-dropdown
    v-if="providers.length > 1 && !active"
    class="git-plugin-generate"
    trigger="click"
    :disabled="busy"
    @command="onCommand"
  >
    <el-button
      ref="triggerButton"
      text
      native-type="button"
      data-testid="git-plugin-generate"
      aria-haspopup="menu"
      :disabled="busy"
      :title="buttonTitle"
    >
      <el-icon><Connection /></el-icon>
      {{ t("git.pluginGenerate") }}
    </el-button>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item v-for="provider in providers" :key="providerKey(provider)" :command="providerKey(provider)">
          {{ providerTitle(provider) }} · {{ pluginName(provider) }}
        </el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
  <el-button
    v-else
    ref="actionButton"
    text
    native-type="button"
    data-testid="git-plugin-generate"
    :disabled="busy"
    :title="buttonTitle"
    :aria-busy="active"
    @click="onClick"
  >
    <el-icon :class="{ 'is-generating': active }"><Connection /></el-icon>
    {{ active ? t("common.cancel") : t("git.pluginGenerate") }}
  </el-button>
</template>
