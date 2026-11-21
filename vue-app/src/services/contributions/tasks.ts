/**
 * Consumer of the `tasks` contribution point (process plugins with the
 * `shell` capability). Tasks are per project, so the consumer only records
 * whether any active plugin contributes tasks and registers two palette
 * commands that reveal the project Tasks tab (optionally detecting). The
 * runtime (tasks-runtime.ts) loads only when a plugin contributes tasks; the
 * per-task palette commands are registered by the Tasks panel for the open
 * project. Registering data never starts anything: every run goes through the
 * host by reference and needs the user's confirmation for that project
 * (services/plugin-task-service.ts).
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function tasksSyncError(): string | undefined {
  return lastSyncError;
}

export function registerTaskContributionConsumer(): () => void {
  let runtime: Promise<typeof import("@/services/contributions/tasks-runtime")> | undefined;
  const load = () => (runtime ??= import("@/services/contributions/tasks-runtime"));
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "tasks",
    apply: lazyContributionApply(async () => {
      const module = await load();
      lastSyncError = undefined;
      return module.applyTaskContributions;
    }, onError),
    onError,
  });
}
