import { mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import { h, nextTick, ref } from "vue";
import AiTimelineQuestionRow from "@/components/ai/timeline/AiTimelineQuestionRow.vue";
import { parseAskUserInput } from "@/domain/ai/ask-user";
import type { AiQuestionQueueItem } from "@/domain/ai/question-queue";
import { applySavedLocale } from "@/i18n/messages";

const choice = { header: "Framework", question: "Which framework?", options: [
  { label: "Vue", description: "Reactive components", preview: "Vue example" }, { label: "React" },
] };
const textQuestion = { header: "Name", question: "Project name?" };
const item: AiQuestionQueueItem = {
  id: "question-1", conversationId: "c1", source: "native",
  questions: parseAskUserInput({ questions: [choice] }),
};
const wrappers: VueWrapper[] = [];

function row(overrides: Partial<AiQuestionQueueItem> = {}, attachTo?: HTMLElement, composerHasDraft = false) {
  const wrapper = mount(AiTimelineQuestionRow, { props: { item: { ...item, ...overrides }, composerHasDraft }, attachTo });
  wrappers.push(wrapper);
  return wrapper;
}

afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount();
  document.body.replaceChildren();
  vi.restoreAllMocks();
  await applySavedLocale("zh");
});

it("requires an answer and shows the selected option's preview", async () => {
  const wrapper = row();
  const submit = wrapper.get<HTMLButtonElement>('button[type="submit"]');
  expect(submit.element.disabled).toBe(true);
  expect(wrapper.find("pre").exists()).toBe(false);
  await wrapper.get('input[value="Vue"]').setValue(true);
  expect(wrapper.get("pre").text()).toBe("Vue example");
  expect(submit.element.disabled).toBe(false);
  await wrapper.get("form").trigger("submit");
  expect(wrapper.emitted("answer")).toEqual([[[{ questionId: "question-0", values: ["Vue"] }]]]);
});

it("submits multi-select, other text and an optional note", async () => {
  const wrapper = row({ questions: parseAskUserInput({ questions: [{ ...choice, multiSelect: true }] }) });
  await wrapper.get('input[value="Vue"]').setValue(true);
  await wrapper.get('input[value="React"]').setValue(true);
  await wrapper.get('input[data-question-other-option]').setValue(true);
  expect(wrapper.get<HTMLButtonElement>('button[type="submit"]').element.disabled).toBe(true);
  await wrapper.get('input[data-question-other]').setValue("Svelte");
  await wrapper.get('[data-question-note-toggle]').trigger("click");
  await wrapper.get('textarea[data-question-note]').setValue("existing dependency");
  await wrapper.get("form").trigger("submit");
  expect(wrapper.emitted("answer")).toEqual([[[
    { questionId: "question-0", values: ["Vue", "React"], other: "Svelte", note: "existing dependency" },
  ]]]);
});

it("accepts text and requires Cmd or Ctrl with Enter in a text field", async () => {
  const wrapper = row({ questions: parseAskUserInput({ questions: [textQuestion] }) });
  const input = wrapper.get("input");
  await input.setValue("Example");
  await input.trigger("keydown", { key: "Enter" });
  expect(wrapper.emitted("answer")).toBeUndefined();
  await input.trigger("keydown", { key: "Enter", metaKey: true });
  await input.trigger("keydown", { key: "Enter", ctrlKey: true });
  expect(wrapper.emitted("answer")).toEqual(Array(2).fill([[{ questionId: "question-0", values: ["Example"] }]]));
});

it("hides secret input and displays the Agent message and automatic continuation hint", async () => {
  await applySavedLocale("en");
  const [question] = parseAskUserInput({ questions: [textQuestion] });
  const wrapper = row({
    source: "acp", message: "Codex is still working", autoResolutionMs: 5250,
    questions: [{ ...question!, secret: true, required: false }],
  });
  expect(wrapper.get("input").attributes("type")).toBe("password");
  expect(wrapper.text()).toContain("Codex is still working");
  expect(wrapper.text()).toContain("6 seconds");
  await wrapper.get("[data-question-skip]").trigger("click");
  expect(wrapper.emitted("skip")).toEqual([[]]);
});

it("moves between options by arrow keys and submits from a choice with Enter", async () => {
  const wrapper = row({}, document.body);
  const first = wrapper.get<HTMLInputElement>("input[value=Vue]");
  const second = wrapper.get<HTMLInputElement>("input[value=React]");
  first.element.focus();
  await first.trigger("keydown", { key: "ArrowDown" });
  expect(document.activeElement).toBe(second.element);
  expect(second.element.checked).toBe(true);
  await second.trigger("keydown", { key: "Enter" });
  expect(wrapper.emitted("answer")).toEqual([[[{ questionId: "question-0", values: ["React"] }]]]);
});

it("scrolls into view without taking focus outside the assistant panel", async () => {
  const terminal = document.body.appendChild(document.createElement("textarea"));
  const panel = document.body.appendChild(document.createElement("aside"));
  panel.className = "ai-assistant-drawer__panel";
  terminal.focus();
  const scroll = vi.spyOn(HTMLElement.prototype, "scrollIntoView");
  row({}, panel);
  await nextTick();
  expect(scroll).toHaveBeenCalledWith({ block: "nearest" });
  expect(document.activeElement).toBe(terminal);
});

it("focuses the first option inside the panel and restores focus when the row is removed", async () => {
  const panel = document.body.appendChild(document.createElement("aside"));
  panel.className = "ai-assistant-drawer__panel";
  const composer = panel.appendChild(document.createElement("textarea"));
  composer.focus();
  const visible = ref(true);
  const wrapper = mount({ render: () => visible.value
    ? h(AiTimelineQuestionRow, { item, composerHasDraft: false })
    : null }, { attachTo: panel });
  wrappers.push(wrapper);
  await nextTick();
  expect(document.activeElement).toBe(wrapper.get("input").element);
  visible.value = false;
  await nextTick();
  expect(document.activeElement).toBe(composer);
  row({}, panel, true);
  await nextTick();
  expect(document.activeElement).toBe(composer);
});
