import type { SaveOutcome } from "@/domain/projects/save-policy";

/** Runtime-owned file queues outlive a KeepAlive view and serialize all writers to one target. */
export function createProjectSaveCoordinator() {
  const pending = new Map<string, Promise<SaveOutcome>>();
  const queued = new Map<string, { run: () => Promise<SaveOutcome>; task: Promise<SaveOutcome> }>();
  function save(target: string, run: () => Promise<SaveOutcome>, coalesce?: string): Promise<SaveOutcome> {
    if (coalesce) {
      const existing = queued.get(coalesce);
      if (existing) { existing.run = run; return existing.task; }
    }
    const previous = pending.get(target) ?? Promise.resolve<SaveOutcome>("skipped");
    const entry = { run, task: Promise.resolve<SaveOutcome>("skipped") };
    const task = previous.catch(() => "failed" as const).then(() => {
      if (coalesce) queued.delete(coalesce);
      return entry.run();
    });
    entry.task = task;
    if (coalesce) queued.set(coalesce, entry);
    pending.set(target, task);
    void task.finally(() => { if (pending.get(target) === task) pending.delete(target); }).catch(() => undefined);
    return task;
  }
  async function wait(target: string) {
    while (pending.has(target)) await pending.get(target)?.catch(() => undefined);
  }
  return { save, wait };
}

export const projectSaveCoordinator = createProjectSaveCoordinator();
