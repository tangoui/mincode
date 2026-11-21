<script setup lang="ts">
/**
 * One plugin walkthrough, step by step. Plugin text is untrusted: step
 * markdown renders only through renderPluginMarkdownHtml (raw HTML and images
 * escaped, http(s) links opened in the system browser), plain-text steps
 * render as text, and images come only from the step's declared asset as a
 * blob URL. A step command runs through executePluginCommand, which keeps the
 * command's risk confirmation. The dialog opens only on a user action.
 */
import { computed, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { Check, CircleCheckFilled, VideoPlay } from "@element-plus/icons-vue";
import {
  WALKTHROUGHS_CONTRIBUTION_POINT,
  completedWalkthroughSteps,
  walkthroughDescription,
  walkthroughMarkdownKey,
  walkthroughTitle,
} from "@/domain/plugins/walkthroughs";
import { useVueI18n } from "@/i18n/messages";
import { contributionAssetBytes, contributionAssetObjectURL } from "@/services/contribution-assets";
import { executePluginCommand } from "@/services/plugin-commands";
import { PLUGIN_MARKDOWN_HTTP_URL, decodePluginMarkdown, renderPluginMarkdownHtml } from "@/services/plugin-markdown";
import { openSystemBrowser } from "@/services/web-automation-service";
import { usePluginWalkthroughsStore } from "@/stores/plugin-walkthroughs";

const props = defineProps<{ walkthroughKey: string }>();
const open = defineModel<boolean>({ required: true });

const { t, localeChain } = useVueI18n();
const store = usePluginWalkthroughsStore();

const stepIndex = ref(0);
const markdownHtml = ref("");
const imageUrl = ref("");
const contentState = ref<"idle" | "loading" | "failed">("idle");
const runningCommand = ref(false);
let loadGeneration = 0;

const walkthrough = computed(() => store.walkthroughByKey(props.walkthroughKey));
const step = computed(() => walkthrough.value?.steps[stepIndex.value]);
const completed = computed(() => (walkthrough.value ? completedWalkthroughSteps(walkthrough.value, store.progress) : new Set<string>()));
const title = computed(() => (walkthrough.value ? walkthroughTitle(walkthrough.value, localeChain.value) : t("pluginContrib.walkthroughs.section")));
const description = computed(() => (walkthrough.value ? walkthroughDescription(walkthrough.value, localeChain.value) : ""));
const isLast = computed(() => (walkthrough.value ? stepIndex.value >= walkthrough.value.steps.length - 1 : true));
const stepDone = computed(() => Boolean(step.value && completed.value.has(step.value.id)));

async function loadStep() {
  const generation = ++loadGeneration;
  markdownHtml.value = "";
  imageUrl.value = "";
  const current = walkthrough.value;
  const currentStep = step.value;
  if (!current || !currentStep) return;
  const markdownKey = walkthroughMarkdownKey(currentStep, localeChain.value);
  if (!markdownKey && !currentStep.image) {
    contentState.value = "idle";
    return;
  }
  contentState.value = "loading";
  try {
    const [markdown, image] = await Promise.all([
      markdownKey ? contributionAssetBytes(current.pluginId, WALKTHROUGHS_CONTRIBUTION_POINT, markdownKey) : Promise.resolve(undefined),
      currentStep.image
        ? contributionAssetObjectURL(current.pluginId, WALKTHROUGHS_CONTRIBUTION_POINT, currentStep.image)
        : Promise.resolve(undefined),
    ]);
    if (generation !== loadGeneration) return;
    if (markdownKey && (!markdown || markdown.mime !== "text/markdown")) throw new Error("markdown unavailable");
    if (currentStep.image && !image) throw new Error("image unavailable");
    markdownHtml.value = markdown ? renderPluginMarkdownHtml(decodePluginMarkdown(markdown.bytes)) : "";
    imageUrl.value = image ?? "";
    contentState.value = "idle";
  } catch {
    if (generation === loadGeneration) contentState.value = "failed";
  }
}

watch(
  () => [open.value, props.walkthroughKey] as const,
  ([isOpen]) => {
    if (!isOpen) return;
    void store.hydrate();
    const current = walkthrough.value;
    const firstOpen = current?.steps.findIndex((item) => !completed.value.has(item.id)) ?? 0;
    stepIndex.value = Math.max(0, firstOpen);
    void loadStep();
  },
  { immediate: true },
);

// A new snapshot (plugin update, disable) replaces the walkthrough object and
// may release its cached assets: reload the current step.
watch(walkthrough, (next, previous) => {
  if (!open.value || next === previous) return;
  if (next && stepIndex.value >= next.steps.length) stepIndex.value = 0;
  void loadStep();
});
watch(stepIndex, () => void loadStep());
watch(localeChain, () => {
  if (open.value) void loadStep();
});

/** Link clicks never navigate the WebView; http(s) links open in the system browser. */
function handlePluginMarkdownClick(event: MouseEvent) {
  const link = event.target instanceof Element ? event.target.closest("a") : null;
  if (!link) return;
  event.preventDefault();
  const href = link.getAttribute("href") ?? "";
  if (PLUGIN_MARKDOWN_HTTP_URL.test(href)) void openSystemBrowser(href).catch(() => undefined);
}

async function runCommand() {
  const current = walkthrough.value;
  const command = step.value?.command;
  if (!current || !command || current.runtime !== "process") return;
  runningCommand.value = true;
  try {
    await executePluginCommand(current.pluginId, command);
  } catch {
    ElMessage.warning(t("pluginContrib.walkthroughs.commandUnavailable"));
  } finally {
    runningCommand.value = false;
  }
}

async function markDone() {
  if (!walkthrough.value || !step.value) return;
  await store.markStepDone(walkthrough.value.key, step.value.id);
}

async function next() {
  await markDone();
  if (isLast.value) open.value = false;
  else stepIndex.value += 1;
}

async function dismiss() {
  if (!walkthrough.value) return;
  await store.setDismissed(walkthrough.value.key, true);
  open.value = false;
}
</script>

<template>
  <el-dialog v-model="open" :title="title" width="min(900px, calc(100vw - 32px))" top="6vh" append-to-body class="app-walkthrough-dialog">
    <div v-if="!walkthrough" class="app-walkthrough__unavailable" role="alert">
      {{ t("pluginContrib.walkthroughs.unavailable") }}
    </div>
    <div v-else class="app-walkthrough">
      <p v-if="description" class="app-walkthrough__description">{{ description }}</p>
      <div class="app-walkthrough__progress">
        <span>{{ t('pluginContrib.walkthroughs.progress', { done: completed.size, total: walkthrough.steps.length }) }}</span>
        <el-progress :percentage="Math.round(completed.size / walkthrough.steps.length * 100)" :show-text="false" :stroke-width="4" />
      </div>
      <div class="app-walkthrough__layout">
        <el-scrollbar class="app-walkthrough__sidebar">
          <nav class="app-walkthrough__navigation" :aria-label="t('pluginContrib.walkthroughs.section')">
            <el-button
              v-for="(item, index) in walkthrough.steps"
              :key="item.id"
              text
              native-type="button"
              class="app-walkthrough__nav-item"
              :class="{ 'is-current': index === stepIndex, 'is-done': completed.has(item.id) }"
              :aria-current="index === stepIndex ? 'step' : undefined"
              :aria-label="walkthroughTitle(item, localeChain) + (completed.has(item.id) ? ' · ' + t('pluginContrib.walkthroughs.stepDone') : '')"
              @click="stepIndex = index"
            >
              <span class="app-walkthrough__number" aria-hidden="true"><el-icon v-if="completed.has(item.id)"><Check /></el-icon><template v-else>{{ index + 1 }}</template></span>
              <span class="app-walkthrough__nav-title">{{ walkthroughTitle(item, localeChain) }}</span>
            </el-button>
          </nav>
        </el-scrollbar>
        <el-scrollbar :key="step?.id" class="app-walkthrough__reading">
      <section v-if="step" class="app-walkthrough__step" :aria-label="t('pluginContrib.walkthroughs.stepOf', { current: stepIndex + 1, total: walkthrough.steps.length })">
        <header class="app-walkthrough__step-header">
          <small>{{ t("pluginContrib.walkthroughs.stepOf", { current: stepIndex + 1, total: walkthrough.steps.length }) }}</small>
          <h3>{{ walkthroughTitle(step, localeChain) }}</h3>
          <span v-if="stepDone" class="app-walkthrough__done"><el-icon><CircleCheckFilled /></el-icon>{{ t("pluginContrib.walkthroughs.stepDone") }}</span>
        </header>
        <div v-if="contentState === 'loading'" class="app-walkthrough__status" role="status">{{ t("pluginContrib.walkthroughs.loading") }}</div>
        <div v-else-if="contentState === 'failed'" class="app-walkthrough__status is-error" role="alert">
          {{ t("pluginContrib.walkthroughs.contentUnavailable") }}
        </div>
        <template v-else>
          <p v-if="step.text" class="app-walkthrough__text">{{ step.text }}</p>
          <!-- eslint-disable-next-line vue/no-v-html -- sanitized by renderPluginMarkdownHtml (no raw HTML, no images, http(s) links only) -->
          <div v-else-if="markdownHtml" class="app-walkthrough__markdown" v-html="markdownHtml" @click="handlePluginMarkdownClick" />
          <img v-if="imageUrl" class="app-walkthrough__image" :src="imageUrl" :alt="step.imageAlt" />
        </template>
        <el-button
          v-if="step.command && walkthrough.runtime === 'process'"
          type="primary"
          plain
          :icon="VideoPlay"
          :loading="runningCommand"
          native-type="button"
          @click="runCommand"
        >
          {{ t("pluginContrib.walkthroughs.runCommand") }}
        </el-button>
      </section>
        </el-scrollbar>
      </div>
    </div>
    <template #footer>
      <div class="app-walkthrough__footer">
        <el-button v-if="walkthrough?.featured" text native-type="button" @click="dismiss">{{ t("pluginContrib.walkthroughs.dismiss") }}</el-button>
        <span class="app-walkthrough__spacer" />
        <template v-if="walkthrough && step">
          <el-button native-type="button" :disabled="stepIndex === 0" @click="stepIndex -= 1">{{ t("pluginContrib.walkthroughs.previous") }}</el-button>
          <el-button v-if="!stepDone" native-type="button" :icon="Check" @click="markDone">{{ t("pluginContrib.walkthroughs.markDone") }}</el-button>
          <el-button type="primary" native-type="button" @click="next">
            {{ isLast ? t("pluginContrib.walkthroughs.finish") : t("pluginContrib.walkthroughs.next") }}
          </el-button>
        </template>
        <el-button v-else native-type="button" @click="open = false">{{ t("common.close") }}</el-button>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped>
.app-walkthrough {
  display: grid;
  gap: 14px;
  min-width: 0;
}
.app-walkthrough__description {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.app-walkthrough__progress {
  display: grid;
  grid-template-columns: auto minmax(80px, 160px);
  align-items: center;
  gap: 12px;
  color: var(--app-text-muted);
  font-size: 12px;
}
.app-walkthrough__layout {
  display: grid;
  grid-template-columns: 216px minmax(0, 1fr);
  height: min(440px, 58vh);
  min-height: 240px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  overflow: hidden;
  background: var(--app-surface);
}
.app-walkthrough__sidebar {
  min-width: 0;
  background: var(--app-sidebar);
  border-right: 1px solid var(--app-border);
}
.app-walkthrough__navigation {
  display: grid;
  gap: 4px;
  padding: 10px;
}
.app-walkthrough__nav-item {
  width: 100%;
  height: auto;
  min-height: 36px;
  margin: 0;
  padding: 8px;
  justify-content: flex-start;
  color: var(--app-text-regular);
}
/* Element Plus 按钮的默认内容容器不适合多行目录，限定在此处覆盖。 */
.app-walkthrough__nav-item :deep(> span) {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  min-width: 0;
}
.app-walkthrough__nav-item:hover {
  background: var(--app-hover);
}
.app-walkthrough__nav-item.is-current {
  background: var(--app-accent-soft);
  color: var(--app-accent);
  font-weight: 600;
}
.app-walkthrough__nav-item:focus-visible {
  outline: 2px solid var(--app-accent);
  outline-offset: -2px;
}
.app-walkthrough__number {
  display: inline-flex;
  justify-content: center;
  align-items: center;
  flex-shrink: 0;
  width: 22px;
  height: 22px;
  border: 1px solid var(--app-border);
  border-radius: 50%;
  font-size: 11px;
}
.app-walkthrough__nav-item.is-done .app-walkthrough__number {
  color: var(--el-color-success);
  border-color: var(--el-color-success);
}
.app-walkthrough__nav-title {
  min-width: 0;
  white-space: normal;
  overflow-wrap: anywhere;
  text-align: left;
  line-height: 1.5;
}
.app-walkthrough__reading {
  min-width: 0;
}
@media (max-width: 640px) {
  .app-walkthrough__layout {
    grid-template-columns: 160px minmax(0, 1fr);
  }
  .app-walkthrough__navigation {
    padding: 6px;
  }
}
.app-walkthrough__step {
  display: grid;
  gap: 10px;
  justify-items: start;
  min-width: 0;
  padding: 24px;
}
.app-walkthrough__step > * {
  max-width: 100%;
}
.app-walkthrough__step-header {
  display: grid;
  gap: 8px;
  padding-bottom: 12px;
  width: 100%;
  border-bottom: 1px solid var(--app-border);
}
.app-walkthrough__step-header small {
  color: var(--app-text-muted);
  font-size: 11px;
}
.app-walkthrough__step-header h3 {
  margin: 0;
  color: var(--app-text);
  font-size: 20px;
  font-weight: 600;
  line-height: 1.4;
}
.app-walkthrough__done {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--el-color-success);
  font-size: 12px;
}
.app-walkthrough__text {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  color: var(--app-text);
  font-size: 13px;
  line-height: 1.6;
}
.app-walkthrough__markdown {
  width: 100%;
  overflow-wrap: anywhere;
  color: var(--app-text);
  font-size: 13px;
  line-height: 1.6;
}
.app-walkthrough__markdown :deep(pre) {
  overflow: auto;
  padding: 8px 10px;
  border-radius: 4px;
  background: var(--app-surface-muted, var(--el-fill-color-light));
}
.app-walkthrough__markdown :deep(a) {
  color: var(--app-accent);
  cursor: pointer;
}
.app-walkthrough__markdown :deep(:first-child) {
  margin-top: 0;
}
.app-walkthrough__image {
  max-width: 100%;
  max-height: 320px;
  object-fit: contain;
  border: 1px solid var(--app-border);
  border-radius: 4px;
}
.app-walkthrough__status {
  color: var(--app-text-muted);
  font-size: 12px;
}
.app-walkthrough__status.is-error,
.app-walkthrough__unavailable {
  color: var(--el-color-danger);
  font-size: 13px;
}
.app-walkthrough__footer {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding-top: 12px;
  border-top: 1px solid var(--app-border);
}
.app-walkthrough__spacer {
  flex: 1;
}
</style>
