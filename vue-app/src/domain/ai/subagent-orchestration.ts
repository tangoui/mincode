export const SUBAGENT_MAX_TASKS = 16;
export const SUBAGENT_DEFAULT_TIMEOUT_MS = 20 * 60_000;
export const SUBAGENT_MAX_TIMEOUT_MS = 60 * 60_000;

export type SubagentTaskMode = "read" | "write";
export type SubagentTaskState =
  | "blocked"
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "skipped";

export interface SubagentTaskDefinition {
  id: string;
  title: string;
  prompt: string;
  dependsOn?: string[];
  mode?: SubagentTaskMode;
  /** ID of an existing model option in the desktop assistant. */
  model?: string;
  /** Exclusive logical resources. Keep empty when an isolated worktree is used. */
  resourceKeys?: string[];
  timeoutMs?: number;
  /** Total attempts, including the first execution. */
  maxAttempts?: number;
}

export interface SubagentPlanDefinition {
  title: string;
  tasks: SubagentTaskDefinition[];
  /** Optional positive safe integer limit; defaults to running all ready, resource-compatible tasks. */
  maxConcurrency?: number;
  failFast?: boolean;
}

export interface NormalizedSubagentTask
  extends Omit<SubagentTaskDefinition, "dependsOn" | "mode" | "resourceKeys" | "timeoutMs" | "maxAttempts"> {
  index: number;
  dependsOn: string[];
  mode: SubagentTaskMode;
  resourceKeys: string[];
  timeoutMs: number;
  maxAttempts: number;
  /** Nearest successful write task whose commit is this task's code baseline. */
  inheritedWriteTaskId?: string;
}

export interface NormalizedSubagentPlan {
  title: string;
  tasks: NormalizedSubagentTask[];
  maxConcurrency: number;
  failFast: boolean;
}

export interface SubagentTaskExecutionResult {
  summary: string;
  commitHash?: string;
  branch?: string;
  changedFiles?: string[];
  verification?: string[];
  metadata?: Record<string, unknown>;
}

export interface SubagentTaskSnapshot extends NormalizedSubagentTask {
  state: SubagentTaskState;
  attempts: number;
  queuedAt?: number;
  startedAt?: number;
  finishedAt?: number;
  result?: SubagentTaskExecutionResult;
  error?: string;
}

export interface SubagentPlanSnapshot {
  title: string;
  state: "running" | "succeeded" | "failed" | "cancelled";
  startedAt: number;
  finishedAt?: number;
  tasks: SubagentTaskSnapshot[];
}

export interface SubagentExecutionContext {
  signal: AbortSignal;
  attempt: number;
  completedDependencies: ReadonlyMap<string, SubagentTaskExecutionResult>;
}

export interface SubagentSchedulerOptions {
  execute(
    task: NormalizedSubagentTask,
    context: SubagentExecutionContext,
  ): Promise<SubagentTaskExecutionResult>;
  taskSignal?(task: NormalizedSubagentTask): AbortSignal | undefined;
  now?: () => number;
  onUpdate?: (snapshot: SubagentPlanSnapshot) => void;
}

const taskIdPattern = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/;
const terminalStates = new Set<SubagentTaskState>([
  "succeeded",
  "failed",
  "cancelled",
  "skipped",
]);

function requiredText(value: unknown, field: string, maximum: number) {
  if (typeof value !== "string" || !value.trim()) {
    throw new Error(`${field} is required`);
  }
  const normalized = value.trim();
  if (normalized.length > maximum) throw new Error(`${field} exceeds ${maximum} characters`);
  return normalized;
}

function boundedInteger(value: unknown, fallback: number, minimum: number, maximum: number, field: string) {
  if (value === undefined) return fallback;
  if (!Number.isInteger(value) || Number(value) < minimum || Number(value) > maximum) {
    throw new Error(`${field} must be an integer between ${minimum} and ${maximum}`);
  }
  return Number(value);
}

function uniqueStrings(values: unknown, field: string) {
  if (values === undefined) return [];
  if (!Array.isArray(values) || values.some((value) => typeof value !== "string")) {
    throw new Error(`${field} must be a string array`);
  }
  const normalized = values.map((value) => value.trim()).filter(Boolean);
  if (new Set(normalized).size !== normalized.length) throw new Error(`${field} contains duplicates`);
  return normalized;
}

export function normalizeSubagentPlan(
  plan: SubagentPlanDefinition,
  limits: { maxTasks?: number } = {},
): NormalizedSubagentPlan {
  if (!plan || typeof plan !== "object") throw new Error("subagent plan is required");
  const title = requiredText(plan.title, "plan.title", 160);
  const maxTasks = limits.maxTasks ?? SUBAGENT_MAX_TASKS;
  if (!Array.isArray(plan.tasks) || plan.tasks.length < 1 || plan.tasks.length > maxTasks) {
    throw new Error(`plan.tasks must contain between 1 and ${maxTasks} tasks`);
  }

  const ids = new Set<string>();
  const tasks = plan.tasks.map((task, index): NormalizedSubagentTask => {
    const id = requiredText(task.id, `plan.tasks[${index}].id`, 64);
    if (!taskIdPattern.test(id)) throw new Error(`invalid task id: ${id}`);
    if (ids.has(id)) throw new Error(`duplicate task id: ${id}`);
    ids.add(id);
    const mode = task.mode ?? "write";
    if (mode !== "read" && mode !== "write") throw new Error(`invalid task mode for ${id}`);
    return {
      id,
      index,
      title: requiredText(task.title == null || (typeof task.title === "string" && !task.title.trim())
        ? id : task.title, `task ${id} title`, 160),
      prompt: requiredText(task.prompt, `task ${id} prompt`, 12_000),
      dependsOn: uniqueStrings(task.dependsOn, `task ${id} dependsOn`),
      mode,
      ...(task.model === undefined ? {} : { model: requiredText(task.model, `task ${id} model`, 512) }),
      resourceKeys: uniqueStrings(task.resourceKeys, `task ${id} resourceKeys`).sort(),
      timeoutMs: boundedInteger(
        task.timeoutMs,
        SUBAGENT_DEFAULT_TIMEOUT_MS,
        1_000,
        SUBAGENT_MAX_TIMEOUT_MS,
        `task ${id} timeoutMs`,
      ),
      maxAttempts: boundedInteger(task.maxAttempts, 1, 1, 3, `task ${id} maxAttempts`),
    };
  });

  const indegree = new Map(tasks.map((task) => [task.id, task.dependsOn.length]));
  const dependents = new Map<string, string[]>();
  for (const task of tasks) {
    for (const dependency of task.dependsOn) {
      if (dependency === task.id) throw new Error(`task ${task.id} cannot depend on itself`);
      if (!ids.has(dependency)) throw new Error(`task ${task.id} depends on unknown task ${dependency}`);
      const list = dependents.get(dependency) ?? [];
      list.push(task.id);
      dependents.set(dependency, list);
    }
  }
  const queue = tasks.filter((task) => task.dependsOn.length === 0).map((task) => task.id);
  for (let index = 0; index < queue.length; index += 1) {
    for (const dependent of dependents.get(queue[index]) ?? []) {
      const remaining = (indegree.get(dependent) ?? 0) - 1;
      indegree.set(dependent, remaining);
      if (remaining === 0) queue.push(dependent);
    }
  }
  if (queue.length !== tasks.length) {
    const cyclic = tasks.filter((task) => (indegree.get(task.id) ?? 0) > 0).map((task) => task.id);
    throw new Error(`subagent task graph contains a cycle: ${cyclic.join(", ")}`);
  }
  const taskMap = new Map(tasks.map((task) => [task.id, task]));
  const lineageMemo = new Map<string, Set<string>>();
  const writeLineage = (taskId: string): Set<string> => {
    const cached = lineageMemo.get(taskId);
    if (cached) return cached;
    const task = taskMap.get(taskId)!;
    const lineage = new Set<string>();
    for (const dependencyId of task.dependsOn) {
      const dependency = taskMap.get(dependencyId)!;
      if (dependency.mode === "write") lineage.add(dependency.id);
      else for (const ancestor of writeLineage(dependency.id)) lineage.add(ancestor);
    }
    lineageMemo.set(taskId, lineage);
    return lineage;
  };
  for (const task of tasks) {
    if (task.mode === "write" && task.maxAttempts > 1) {
      throw new Error(`write task ${task.id} cannot retry in place; split recovery into another task`);
    }
    const lineage = [...writeLineage(task.id)];
    if (lineage.length > 1) {
      throw new Error(
        `task ${task.id} joins multiple unmerged write branches; integrate them before continuing`,
      );
    }
    task.inheritedWriteTaskId = lineage[0];
  }

  return {
    title,
    tasks,
    maxConcurrency: boundedInteger(
      plan.maxConcurrency,
      tasks.length,
      1,
      Number.MAX_SAFE_INTEGER,
      "plan.maxConcurrency",
    ),
    failFast: plan.failFast === true,
  };
}

function abortError(message = "Subagent task cancelled") {
  return new DOMException(message, "AbortError");
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/** Stops waiting even when an integration ignores AbortSignal. */
function runTaskWithSignal<T>(signal: AbortSignal, operation: () => Promise<T>) {
  if (signal.aborted) return Promise.reject(signal.reason ?? abortError());
  let onAbort: (() => void) | undefined;
  const aborted = new Promise<never>((_, reject) => {
    onAbort = () => reject(signal.reason ?? abortError());
    signal.addEventListener("abort", onAbort, { once: true });
  });
  return Promise.race([Promise.resolve().then(operation), aborted]).finally(() => {
    if (onAbort) signal.removeEventListener("abort", onAbort);
  });
}

function cloneSnapshot(snapshot: SubagentPlanSnapshot): SubagentPlanSnapshot {
  return {
    ...snapshot,
    tasks: snapshot.tasks.map((task) => ({
      ...task,
      dependsOn: [...task.dependsOn],
      resourceKeys: [...task.resourceKeys],
      result: task.result
        ? {
            ...task.result,
            changedFiles: task.result.changedFiles ? [...task.result.changedFiles] : undefined,
            verification: task.result.verification ? [...task.result.verification] : undefined,
            metadata: task.result.metadata ? { ...task.result.metadata } : undefined,
          }
        : undefined,
    })),
  };
}

export function runSubagentPlan(
  definition: SubagentPlanDefinition,
  options: SubagentSchedulerOptions,
  parentSignal?: AbortSignal,
): Promise<SubagentPlanSnapshot> {
  const plan = normalizeSubagentPlan(definition);
  const now = options.now ?? Date.now;
  const taskById = new Map<string, SubagentTaskSnapshot>();
  const dependents = new Map<string, string[]>();
  const remainingDependencies = new Map<string, number>();
  const ready: string[] = [];
  const activeResources = new Set<string>();
  const controllers = new Map<string, AbortController>();
  const removeTaskAbortListeners: Array<() => void> = [];
  let running = 0;
  let terminal = 0;
  let settled = false;
  let scheduling = false;
  let cancelledByParent = parentSignal?.aborted === true;
  let failFastTriggered = false;

  const snapshot: SubagentPlanSnapshot = {
    title: plan.title,
    state: "running",
    startedAt: now(),
    tasks: plan.tasks.map((task) => {
      const state: SubagentTaskState = task.dependsOn.length ? "blocked" : "queued";
      const entry: SubagentTaskSnapshot = {
        ...task,
        state,
        attempts: 0,
        ...(state === "queued" ? { queuedAt: now() } : {}),
      };
      taskById.set(task.id, entry);
      remainingDependencies.set(task.id, task.dependsOn.length);
      if (state === "queued") ready.push(task.id);
      for (const dependency of task.dependsOn) {
        const list = dependents.get(dependency) ?? [];
        list.push(task.id);
        dependents.set(dependency, list);
      }
      return entry;
    }),
  };

  let resolvePlan!: (value: SubagentPlanSnapshot) => void;
  const done = new Promise<SubagentPlanSnapshot>((resolve) => {
    resolvePlan = resolve;
  });

  function emit() {
    options.onUpdate?.(cloneSnapshot(snapshot));
  }

  function releaseResources(task: SubagentTaskSnapshot) {
    for (const key of task.resourceKeys) activeResources.delete(key);
  }

  function resourcesAvailable(task: SubagentTaskSnapshot) {
    return task.resourceKeys.every((key) => !activeResources.has(key));
  }

  function completeTask(task: SubagentTaskSnapshot) {
    terminal += 1;
    task.finishedAt = now();
    for (const dependentId of dependents.get(task.id) ?? []) {
      const dependent = taskById.get(dependentId);
      if (!dependent || terminalStates.has(dependent.state)) continue;
      const remaining = (remainingDependencies.get(dependentId) ?? 1) - 1;
      remainingDependencies.set(dependentId, remaining);
      if (remaining > 0) continue;
      const failedDependency = dependent.dependsOn
        .map((id) => taskById.get(id))
        .find((dependency) => dependency?.state !== "succeeded");
      if (failedDependency) {
        dependent.state = cancelledByParent ? "cancelled" : "skipped";
        dependent.error = cancelledByParent
          ? "Parent run cancelled"
          : `Dependency ${failedDependency.id} ${failedDependency.state}`;
        completeTask(dependent);
      } else {
        dependent.state = "queued";
        dependent.queuedAt = now();
        ready.push(dependent.id);
      }
    }
  }

  function settleIfDone() {
    if (settled || terminal !== snapshot.tasks.length) return false;
    settled = true;
    parentSignal?.removeEventListener("abort", cancelAll);
    for (const remove of removeTaskAbortListeners) remove();
    snapshot.finishedAt = now();
    if (cancelledByParent || snapshot.tasks.some((task) => task.state === "cancelled")) {
      snapshot.state = "cancelled";
    } else if (snapshot.tasks.some((task) => task.state === "failed" || task.state === "skipped")) {
      snapshot.state = "failed";
    } else {
      snapshot.state = "succeeded";
    }
    emit();
    resolvePlan(cloneSnapshot(snapshot));
    return true;
  }

  function skipPending(reason: string, state: "cancelled" | "skipped") {
    for (const task of snapshot.tasks) {
      if (task.state !== "queued" && task.state !== "blocked") continue;
      task.state = state;
      task.error = reason;
      completeTask(task);
    }
    ready.length = 0;
  }

  function cancelAll() {
    if (settled) return;
    cancelledByParent = true;
    for (const controller of controllers.values()) controller.abort(abortError("Parent run cancelled"));
    skipPending("Parent run cancelled", "cancelled");
    emit();
    settleIfDone();
  }

  async function execute(task: SubagentTaskSnapshot) {
    running += 1;
    task.state = "running";
    task.startedAt ??= now();
    task.attempts += 1;
    for (const key of task.resourceKeys) activeResources.add(key);
    const controller = new AbortController();
    controllers.set(task.id, controller);
    const onParentAbort = () => controller.abort(abortError("Parent run cancelled"));
    parentSignal?.addEventListener("abort", onParentAbort, { once: true });
    const timeout = setTimeout(
      () => controller.abort(abortError(`Task timed out after ${task.timeoutMs}ms`)),
      task.timeoutMs,
    );
    emit();

    try {
      const completedDependencies = new Map<string, SubagentTaskExecutionResult>();
      for (const dependencyId of task.dependsOn) {
        const result = taskById.get(dependencyId)?.result;
        if (result) completedDependencies.set(dependencyId, result);
      }
      const result = await runTaskWithSignal(controller.signal, () =>
        options.execute(task, {
          signal: controller.signal,
          attempt: task.attempts,
          completedDependencies,
        }),
      );
      if (controller.signal.aborted) throw controller.signal.reason ?? abortError();
      task.result = result;
      task.state = "succeeded";
    } catch (error) {
      const parentCancelled = cancelledByParent || parentSignal?.aborted === true;
      if (parentCancelled) {
        task.state = "cancelled";
        task.error = "Parent run cancelled";
      } else if (options.taskSignal?.(task)?.aborted) {
        task.state = "cancelled";
        task.error = "Task cancelled";
      } else if (failFastTriggered) {
        task.state = "skipped";
        task.error = "Sibling task failed";
      } else if (task.attempts < task.maxAttempts) {
        task.state = "queued";
        task.error = errorMessage(error);
        task.queuedAt = now();
        ready.push(task.id);
      } else {
        task.state = "failed";
        task.error = errorMessage(error);
      }
    } finally {
      clearTimeout(timeout);
      parentSignal?.removeEventListener("abort", onParentAbort);
      controllers.delete(task.id);
      releaseResources(task);
      running -= 1;
      if (terminalStates.has(task.state)) completeTask(task);
      if (plan.failFast && task.state === "failed") {
        failFastTriggered = true;
        for (const controller of controllers.values()) controller.abort(abortError("Sibling task failed"));
        skipPending(`Task ${task.id} failed`, "skipped");
      }
      emit();
      schedule();
    }
  }

  function schedule() {
    if (settled || scheduling) return;
    scheduling = true;
    try {
      if (settleIfDone()) return;
      while (!cancelledByParent && running < plan.maxConcurrency && ready.length) {
        const availableIndex = ready.findIndex((id) => {
          const task = taskById.get(id);
          return task?.state === "queued" && resourcesAvailable(task);
        });
        if (availableIndex < 0) break;
        const [taskId] = ready.splice(availableIndex, 1);
        const task = taskById.get(taskId);
        if (task) void execute(task);
      }
      settleIfDone();
    } finally {
      scheduling = false;
    }
  }

  if (parentSignal) parentSignal.addEventListener("abort", cancelAll, { once: true });
  emit();
  scheduling = true;
  for (const task of snapshot.tasks) {
    const signal = options.taskSignal?.(task);
    if (!signal) continue;
    const onAbort = () => {
      if (terminalStates.has(task.state)) return;
      if (task.state === "running") {
        controllers.get(task.id)?.abort(abortError("Task cancelled"));
        return;
      }
      task.state = "cancelled";
      task.error = "Task cancelled";
      completeTask(task);
      emit();
      schedule();
    };
    if (signal.aborted) onAbort();
    else {
      signal.addEventListener("abort", onAbort, { once: true });
      removeTaskAbortListeners.push(() => signal.removeEventListener("abort", onAbort));
    }
  }
  scheduling = false;
  if (cancelledByParent) cancelAll();
  else schedule();
  return done;
}
