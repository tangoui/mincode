import { registerContributionConsumer } from "@/services/plugin-contributions";
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";

let lastError: string | undefined;
export function editorFrameworkSyncError(): string | undefined {
  return lastError;
}

export function registerEditorFrameworkContributionConsumer(): () => void {
  const apply = lazyContributionApply(
    () => import("@/services/contributions/editor-frameworks-runtime").then((module) => module.applyEditorFrameworks),
    (message) => { lastError = message; },
  );
  return registerContributionConsumer({
    point: "editorFrameworks",
    apply: (contributions) => { lastError = undefined; return apply(contributions); },
    onError: (message) => { lastError = message; },
  });
}
