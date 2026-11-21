export function createSerializedTaskQueue() {
  let tail = Promise.resolve();

  return function enqueue<T>(task: () => Promise<T>): Promise<T> {
    const result = tail.catch(() => undefined).then(task);
    tail = result.then(
      () => undefined,
      () => undefined
    );
    return result;
  };
}

/**
 * 同一时刻只执行一个任务；运行期间的新请求只保留最后一个任务。
 * 所有调用方共享当前运行 Promise，适合“最终状态优先”的自动持久化。
 */
export function createLatestTaskRunner() {
  let pendingTask: (() => Promise<void>) | undefined;
  let running: Promise<void> | undefined;

  return function runLatest(task: () => Promise<void>): Promise<void> {
    pendingTask = task;
    if (!running) {
      running = (async () => {
        let lastError: unknown;
        while (pendingTask) {
          const current = pendingTask;
          pendingTask = undefined;
          try {
            await current();
          } catch (error) {
            lastError = error;
          }
        }
        if (lastError !== undefined) throw lastError;
      })().finally(() => {
        running = undefined;
      });
    }
    return running;
  };
}
