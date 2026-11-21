export interface AiLoopTurnCompletion {
  completed: boolean;
  requiresContinuation: boolean;
  summary: string;
}

const CHINESE_CONTINUATION_INTENT =
  /(?:我(?:接下来|下一步|随后|现在|再|会|将|要|准备|继续|先)|接下来我(?:会|将|要|准备|继续)|下一步我(?:会|将|要|准备|继续)|让我(?:继续|再|先|来))[^。！？\n]{0,100}(?:检查|排查|查看|核对|分析|搜索|查找|读取|修改|修复|实现|测试|验证|构建|运行|执行|处理|调查|确认|定位|配置|部署|继续)/i;
const ENGLISH_CONTINUATION_INTENT =
  /(?:\bi(?:['’]ll|\s+will|\s+am going to|\s+plan to|\s+need to|\s+shall)|\bnext,?\s+i(?:['’]ll|\s+will)|\blet me)\b[^.!?\n]{0,120}\b(?:check|inspect|investigate|analyze|search|read|change|modify|fix|implement|test|verify|build|run|execute|configure|deploy|continue)\b/i;

// “请告诉我需求，我会先分析”这类以用户输入为前提的承诺不是待执行的工作。
const CHINESE_CONDITIONAL_PREFIX =
  /(?:请(?!稍)|如果|如需|若是|要是|只要|一旦|等你|告诉我|贴出|发来|你(?:说明|提供|确认))/;
const ENGLISH_CONDITIONAL_PREFIX =
  /(?:\b(?:if|once|when|after|as soon as|whenever)\b[^.!?\n]{0,80}\byou\b|^\s*(?:(?:please|just)\s+(?:tell|share|send|provide|describe|paste|let me know)|tell me|let me know)\b)/i;

function promiseIn(sentence: string, intent: RegExp, conditionalPrefix: RegExp) {
  const index = sentence.search(intent);
  return index >= 0 && !conditionalPrefix.test(sentence.slice(0, index));
}

function isUnconditionalPromise(sentence: string) {
  return promiseIn(sentence, CHINESE_CONTINUATION_INTENT, CHINESE_CONDITIONAL_PREFIX)
    || promiseIn(sentence, ENGLISH_CONTINUATION_INTENT, ENGLISH_CONDITIONAL_PREFIX);
}

function continuationIntent(content: string) {
  // 只检查回复尾部，避免把前文“接下来会检查”的过程说明误认为尚未完成。
  const tail = content
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/^\s*>\s?/gm, "")
    .trim()
    .slice(-360);
  return tail.split(/(?<=[。！？!?\n])|(?<=\.)\s+/).some(isUnconditionalPromise);
}

/** 没有继续调用业务工具、已有可见回复且未承诺后续动作时，本轮可以直接交付。 */
export function aiLoopTurnCompletion(
  content: string,
  businessToolCallCount: number,
): AiLoopTurnCompletion {
  const summary = content.trim();
  const requiresContinuation = businessToolCallCount === 0
    && Boolean(summary)
    && continuationIntent(summary);
  return {
    completed: businessToolCallCount === 0 && Boolean(summary) && !requiresContinuation,
    requiresContinuation,
    summary,
  };
}
