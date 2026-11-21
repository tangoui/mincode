// Opt-in native performance probe for the AI assistant (-tags e2e builds only). No model is
// called: the probe fills a conversation through the store the way replies arrive (a long
// history with code blocks and tables, then a reply streamed in small chunks) and measures
// the real thread rendering in the AI tab.
import router from "@/router";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";
import { useAiAssistantStore } from "@/stores/ai-assistant";
import { createMeasure, nextFrame, until, type PerfLog } from "@/services/e2e-perf-kit";

function visible<T extends HTMLElement>(selector: string) {
  return [...document.querySelectorAll<T>(selector)].find((element) => element.getBoundingClientRect().width > 0);
}

/** A realistic assistant reply: prose, a list, a table and a fenced code block. */
function reply(index: number) {
  const code = Array.from({ length: 30 }, (_, line) => `  const value${line} = compute(${index}, ${line}); // step ${line}`).join("\n");
  const rows = Array.from({ length: 8 }, (_, row) => `| item ${row} | ${row * index} | ok |`).join("\n");
  return `## Answer ${index}\n\nHere is how the change works, step by step, with the **important** parts highlighted and \`inline code\` where it helps.\n\n- First point about ${index}\n- Second point\n- Third point with a [link](https://example.com/${index})\n\n| name | value | status |\n| --- | --- | --- |\n${rows}\n\n\`\`\`ts\nexport function handler${index}() {\n${code}\n  return value0;\n}\n\`\`\`\n\nThat is all for step ${index}.`;
}

export async function perfAiScenario(config: SmokeConfig, log: PerfLog) {
  const wait = config.timeoutMs;
  const measure = createMeasure(log);
  const assistant = useAiAssistantStore();
  await assistant.hydrate();
  assistant.setPlacement("tab");
  assistant.show();
  const turns = config.perfAi?.turns ?? 100;
  let conversationId = "";
  try {
    await measure("打开 AI 助手（标签页）", async () => {
      await router.push({ name: "ai" });
      await until(() => visible(".is-tab-surface .ai-assistant-body"), "AI tab", wait);
    });
    await measure(`载入 ${turns} 轮长对话（含代码块与表格）`, async () => {
      assistant.createConversation("agentic");
      conversationId = assistant.activeConversationId;
      for (let index = 0; index < turns; index++) {
        assistant.addMessageToConversation(conversationId, { role: "user", content: `Question ${index}: how does part ${index} work?` });
        assistant.addMessageToConversation(conversationId, { role: "assistant", content: reply(index) });
      }
      await until(() => document.querySelectorAll(".ai-message-thread .markdown-code-block, .ai-message-thread pre").length > 3, "rendered replies", wait);
    });
    const scroller = () => visible<HTMLElement>(".ai-message-thread .el-scrollbar__wrap") ?? visible<HTMLElement>(".ai-message-thread");
    await measure("长对话从底滚到顶再回来", async () => {
      const element = await until(scroller, "thread scroller", wait);
      for (let i = 40; i >= 0; i--) { element.scrollTop = ((element.scrollHeight - element.clientHeight) * i) / 40; await nextFrame(); }
      for (let i = 0; i <= 40; i++) { element.scrollTop = ((element.scrollHeight - element.clientHeight) * i) / 40; await nextFrame(); }
    });

    // A streamed reply: the turn is running and the reply grows by small chunks, one per frame.
    const streamed = reply(turns);
    await measure(`流式输出一条回复（${streamed.length} 字，每帧追加 12 字）`, async () => {
      assistant.addMessageToConversation(conversationId, { role: "user", content: "Stream one more long answer" });
      const conversation = assistant.conversationById(conversationId);
      if (!conversation) throw new Error("conversation missing");
      conversation.running = true;
      const messageId = `perf-stream-${config.nonce}`;
      try {
        for (let offset = 0; offset < streamed.length; offset += 12) {
          assistant.appendAssistantMessageForConversation(conversationId, messageId, streamed.slice(offset, offset + 12));
          await nextFrame();
        }
      } finally {
        conversation.running = false;
      }
      await until(() => document.body.textContent?.includes(`That is all for step ${turns}.`), "final streamed text", wait);
    });
    await measure("在输入框连续输入 200 个字符", async () => {
      const textarea = await until(() => visible<HTMLTextAreaElement>(".is-tab-surface .ai-assistant-content textarea"), "composer", wait);
      for (let i = 0; i < 200; i++) {
        textarea.value += i % 30 === 29 ? "\n" : "x";
        textarea.dispatchEvent(new Event("input", { bubbles: true }));
        await nextFrame();
      }
      textarea.value = "";
      textarea.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await measure("切到新对话再切回长对话", async () => {
      for (let i = 0; i < 3; i++) {
        assistant.createConversation("agentic");
        const fresh = assistant.activeConversationId;
        await nextFrame();
        await assistant.switchConversation(conversationId);
        await until(() => document.querySelectorAll(".ai-message-thread pre").length > 3, "long thread back", wait);
        await assistant.deleteConversation(fresh);
      }
    });
    await measure("空闲 5 秒（AI 助手打开）", () => new Promise((resolve) => setTimeout(resolve, 5000)));
  } finally {
    if (conversationId) await assistant.deleteConversation(conversationId).catch(() => undefined);
    await router.push({ name: "dashboard" }).catch(() => undefined);
  }
  await measure("离开 AI 助手后空闲 5 秒", () => new Promise((resolve) => setTimeout(resolve, 5000)));
}
