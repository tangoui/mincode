/**
 * Worker side of services/regex-probe.ts: runs plugin grammar regexes on
 * adversarial input and answers when done. The main thread terminates this
 * worker when it does not answer within the budget (catastrophic
 * backtracking), so a hang here never blocks the UI.
 */
import { runRegexProbe, type RegexProbeCase } from "@/domain/editor/regex-safety";

self.addEventListener("message", (event: MessageEvent) => {
  const data = event.data as { id?: unknown; cases?: unknown } | undefined;
  if (typeof data?.id !== "number" || !Array.isArray(data.cases)) return;
  const cases = data.cases.filter(
    (item): item is RegexProbeCase =>
      typeof item === "object" && item !== null && typeof item.source === "string" && typeof item.flags === "string",
  );
  const executed = runRegexProbe(cases);
  self.postMessage({ id: data.id, executed });
});
