<script setup lang="ts">
import { QuestionFilled } from "@element-plus/icons-vue";
import { ElButton, ElInput } from "element-plus";
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from "vue";
import type { AiQuestion, AiQuestionAnswer, AiQuestionOption } from "@/domain/ai/ask-user";
import type { AiQuestionQueueItem } from "@/domain/ai/question-queue";
import { useVueI18n } from "@/i18n/messages";

const props = withDefaults(defineProps<{
  item: AiQuestionQueueItem;
  composerHasDraft: boolean;
  surfaceHidden?: boolean;
  autoReveal?: boolean;
}>(), { autoReveal: true });

const emit = defineEmits<{ answer: [answers: AiQuestionAnswer[]]; skip: [] }>();
const { t } = useVueI18n();
const id = useId();
const root = ref<HTMLElement>();
let previousFocus: HTMLElement | null = null;

const drafts = props.item.questions.map((question) => ({
  question,
  state: reactive({ values: [] as string[], otherSelected: false, text: "", other: "", note: "", noteOpen: false }),
}));
const yesNo = computed(() => [
  { value: "true", label: t("ai.question.yes") },
  { value: "false", label: t("ai.question.no") },
]);
const seconds = computed(() => Math.ceil((props.item.autoResolutionMs ?? 0) / 1000));

const answers = computed(() => drafts.map(({ question, state }): AiQuestionAnswer => {
  const values = question.input === "text" || question.input === "number"
    ? (state.text.trim() ? [state.text] : [])
    : state.values.slice();
  return {
    questionId: question.id,
    values,
    ...(state.otherSelected && state.other.trim() ? { other: state.other } : {}),
    ...(state.noteOpen && state.note.trim() ? { note: state.note } : {}),
  };
}));
const canSubmit = computed(() => drafts.every(({ question, state }, index) => {
  const answer = answers.value[index]!;
  if (state.otherSelected && !answer.other) return false;
  if (!answer.values.length && !answer.other) return !question.required;
  if (question.input !== "number") return true;
  const number = Number(answer.values[0]);
  return question.integer ? Number.isInteger(number) : Number.isFinite(number);
}));

function optionsFor(question: AiQuestion): AiQuestionOption[] {
  return question.input === "boolean" ? yesNo.value : question.options;
}

function updateChoice({ question, state }: typeof drafts[number], value: string, checked: boolean) {
  if (question.multiSelect) {
    state.values = checked ? [...state.values, value] : state.values.filter((selected) => selected !== value);
  } else {
    state.otherSelected = false;
    state.values = [value];
  }
}

function updateOther({ question, state }: typeof drafts[number], checked: boolean) {
  state.otherSelected = checked;
  if (checked && !question.multiSelect) state.values = [];
}

function moveOption(event: KeyboardEvent) {
  const target = event.target;
  if (!(target instanceof HTMLInputElement) || !target.hasAttribute("data-question-choice")) return;
  const direction = { ArrowDown: 1, ArrowRight: 1, ArrowUp: -1, ArrowLeft: -1 }[event.key];
  if (direction === undefined) return;
  const options = [...target.closest("fieldset")?.querySelectorAll<HTMLInputElement>("[data-question-choice]") ?? []];
  if (!options.length) return;
  event.preventDefault();
  const next = options[(options.indexOf(target) + direction + options.length) % options.length];
  next?.focus();
  if (next?.type === "radio" && !next.checked) next.click();
}

function submit() {
  if (canSubmit.value) emit("answer", answers.value);
}

function onKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" || event.isComposing || event.defaultPrevented) return;
  const target = event.target;
  const shortcut = event.metaKey || event.ctrlKey;
  if (!shortcut && target instanceof HTMLButtonElement) return;
  const typing = target instanceof HTMLTextAreaElement
    || (target instanceof HTMLInputElement && !target.hasAttribute("data-question-choice"));
  if (typing && !shortcut) {
    if (target instanceof HTMLInputElement) event.preventDefault();
    return;
  }
  event.preventDefault();
  submit();
}

function reveal() {
  const element = root.value;
  if (!props.autoReveal || props.surfaceHidden || !element?.isConnected) return;
  const active = document.activeElement;
  element.scrollIntoView?.({ block: "nearest" });
  const panel = element.closest(".ai-assistant-drawer__panel");
  if (props.composerHasDraft || !(active instanceof HTMLElement) || !panel?.contains(active) || element.contains(active)) return;
  previousFocus = active;
  const target = element.querySelector<HTMLElement>("input") ?? element.querySelector<HTMLElement>("button");
  target?.focus({ preventScroll: true });
}

onMounted(() => { void nextTick(reveal); });
watch(() => props.surfaceHidden, (hidden) => { if (!hidden) reveal(); }, { flush: "post" });
onBeforeUnmount(() => {
  if (!root.value?.contains(document.activeElement)) return;
  if (previousFocus?.isConnected) previousFocus.focus({ preventScroll: true });
  else root.value.closest(".ai-assistant-drawer__panel")
    ?.querySelector<HTMLElement>(".ai-assistant-composer__input textarea")?.focus({ preventScroll: true });
});
</script>

<template>
  <section
    ref="root" class="ai-timeline-question is-attention" role="region"
    :aria-labelledby="`${id}-title`" :data-question-id="item.id"
  >
    <div class="ai-timeline__row">
      <QuestionFilled class="ai-timeline__icon" aria-hidden="true" />
      <strong :id="`${id}-title`" class="ai-timeline__title" aria-live="polite">{{ item.message || t("ai.question.required") }}</strong>
    </div>
    <form class="ai-timeline__contents" @submit.prevent="submit" @keydown="onKeydown">
      <p v-if="seconds > 0" class="ai-timeline-question__hint">{{ t("ai.question.autoContinue", { seconds }) }}</p>
      <fieldset v-for="(draft, index) in drafts" :key="draft.question.id" class="ai-timeline-question__field" @keydown="moveOption">
        <legend :id="`${id}-field-${index}`">
          <span v-if="draft.question.header" class="ai-timeline-question__header">{{ draft.question.header }}</span>
          <span v-if="draft.question.question !== draft.question.header">{{ draft.question.question }}</span>
        </legend>
        <template v-if="draft.question.input === 'choice' || draft.question.input === 'boolean'">
          <label
            v-for="option in optionsFor(draft.question)" :key="option.value"
            class="ai-timeline-question__option"
          >
            <input
              :type="draft.question.multiSelect ? 'checkbox' : 'radio'" :name="`${id}-option-${index}`" :value="option.value"
              :checked="draft.state.values.includes(option.value)" data-question-choice
              @change="updateChoice(draft, option.value, ($event.target as HTMLInputElement).checked)"
            />
            <span>
              {{ option.label }}
              <small v-if="option.description">{{ option.description }}</small>
            </span>
          </label>
          <label v-if="draft.question.allowOther" class="ai-timeline-question__option">
            <input
              :type="draft.question.multiSelect ? 'checkbox' : 'radio'" :name="`${id}-option-${index}`"
              :checked="draft.state.otherSelected" data-question-choice data-question-other-option
              @change="updateOther(draft, ($event.target as HTMLInputElement).checked)"
            />
            <span>
              {{ draft.question.otherOption?.label || t("ai.question.other") }}
              <small v-if="draft.question.otherOption?.description">{{ draft.question.otherOption.description }}</small>
            </span>
          </label>
          <ElInput
            v-if="draft.state.otherSelected" v-model="draft.state.other"
            :type="draft.question.secret ? 'password' : 'text'" size="small" data-question-other
            :aria-label="t('ai.question.other')" :placeholder="t('ai.question.otherPlaceholder')"
          />
          <template v-if="!draft.question.multiSelect && !draft.question.secret">
            <template v-for="option in draft.question.options" :key="option.value">
              <pre v-if="option.preview && draft.state.values.includes(option.value)" class="ai-timeline-question__preview">{{ option.preview }}</pre>
            </template>
          </template>
        </template>
        <ElInput
          v-else v-model="draft.state.text" size="small"
          :type="draft.question.secret ? 'password' : draft.question.input === 'number' ? 'number' : 'text'"
          :step="draft.question.integer ? '1' : 'any'" :aria-labelledby="`${id}-field-${index}`"
        />
        <template v-if="draft.question.allowNote !== false && !draft.question.secret && !(draft.state.otherSelected && draft.question.otherOption)">
          <ElButton
            text size="small" data-question-note-toggle
            :aria-expanded="draft.state.noteOpen" @click="draft.state.noteOpen = !draft.state.noteOpen"
          >{{ t(draft.state.noteOpen ? "ai.question.hideNote" : "ai.question.addNote") }}</ElButton>
          <ElInput
            v-if="draft.state.noteOpen" v-model="draft.state.note"
            type="textarea" :rows="2" size="small" data-question-note :aria-label="t('ai.question.note')"
          />
        </template>
      </fieldset>
      <div class="ai-timeline__approval-actions">
        <ElButton type="primary" native-type="submit" size="small" :disabled="!canSubmit">{{ t("ai.question.submit") }}</ElButton>
        <ElButton size="small" data-question-skip @click="emit('skip')">{{ t("ai.question.skip") }}</ElButton>
      </div>
    </form>
  </section>
</template>
