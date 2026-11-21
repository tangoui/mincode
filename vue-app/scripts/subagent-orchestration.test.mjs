import assert from "node:assert/strict";
import test from "node:test";
import {
  normalizeSubagentPlan,
  runSubagentPlan,
  SUBAGENT_MAX_TASKS,
} from "../src/domain/ai/subagent-orchestration.ts";

const sleep = (milliseconds, signal) => new Promise((resolve, reject) => {
  const timeout = setTimeout(resolve, milliseconds);
  signal?.addEventListener("abort", () => {
    clearTimeout(timeout);
    reject(signal.reason);
  }, { once: true });
});

function gate() {
  let release;
  const promise = new Promise((resolve) => { release = resolve; });
  return { promise, release };
}

function task(id, extra = {}) {
  return { id, title: id, prompt: `Complete ${id}`, mode: "read", ...extra };
}

test("validates bounded acyclic plans", () => {
  assert.throws(() => normalizeSubagentPlan({
    title: "cycle",
    tasks: [task("a", { dependsOn: ["b"] }), task("b", { dependsOn: ["a"] })],
  }), /contains a cycle/);
  assert.throws(() => normalizeSubagentPlan({
    title: "unknown",
    tasks: [task("a"), task("b", { dependsOn: ["missing"] })],
  }), /unknown task missing/);
  assert.throws(() => normalizeSubagentPlan({
    title: "too many tasks",
    tasks: Array.from({ length: SUBAGENT_MAX_TASKS + 1 }, (_, index) => task(`task-${index}`)),
  }), /between 1 and 16 tasks/);
  const tasks = Array.from({ length: SUBAGENT_MAX_TASKS }, (_, index) => task(`task-${index}`));
  const plan = normalizeSubagentPlan({ title: "maximum tasks", tasks });
  assert.equal(plan.tasks.length, SUBAGENT_MAX_TASKS);
  assert.equal(plan.maxConcurrency, tasks.length);
});

test("runs a one-task delegation and retains its result", async () => {
  const received = [];
  const result = await runSubagentPlan({ title: "Delegation", tasks: [task("review")] }, {
    execute: async ({ id }) => {
      received.push(id);
      return { summary: "Review complete" };
    },
  });
  assert.deepEqual(received, ["review"]);
  assert.equal(result.state, "succeeded");
  assert.equal(result.tasks[0].result.summary, "Review complete");
});

test("cancels one active child and skips its dependents while independent tasks finish", async () => {
  const cancelled = new AbortController();
  const started = gate();
  const result = runSubagentPlan({
    title: "Cancel a child",
    tasks: [task("a"), task("b"), task("c", { dependsOn: ["a"] })],
  }, {
    taskSignal: (item) => item.id === "a" ? cancelled.signal : undefined,
    execute: async (item, { signal }) => {
      if (item.id === "a") {
        started.release();
        await sleep(300, signal);
      }
      return { summary: item.id };
    },
  });
  await started.promise;
  cancelled.abort();
  assert.deepEqual((await result).tasks.map((item) => [item.id, item.state]), [
    ["a", "cancelled"], ["b", "succeeded"], ["c", "skipped"],
  ]);
});

test("accepts explicit positive safe concurrency limits without a fixed cap", () => {
  for (const maxConcurrency of [1, 2, 5, 17, Number.MAX_SAFE_INTEGER]) {
    const plan = normalizeSubagentPlan({
      title: "explicit concurrency",
      maxConcurrency,
      tasks: [task("a"), task("b")],
    });
    assert.equal(plan.maxConcurrency, maxConcurrency);
  }
});

test("rejects concurrency values that are not positive safe integers", () => {
  for (const maxConcurrency of [
    0, -1, 0.5, 5.5, NaN, Infinity, -Infinity,
    Number.MAX_SAFE_INTEGER + 1, "5", null, true, {}, 5n,
  ]) {
    assert.throws(() => normalizeSubagentPlan({
      title: "invalid concurrency",
      maxConcurrency,
      tasks: [task("a"), task("b")],
    }), /plan.maxConcurrency/);
  }
});

for (const maxConcurrency of [undefined, 5]) {
  test(`${maxConcurrency === undefined ? "default" : "explicit >4"} concurrency runs gated tasks simultaneously`, async () => {
    const executionGate = gate();
    const tasks = Array.from({ length: 8 }, (_, index) => task(`task-${index}`, {
      resourceKeys: [`repo:${index}`],
    }));
    const starts = [];
    let active = 0;
    let peak = 0;
    const promise = runSubagentPlan({
      title: "gated concurrency",
      tasks,
      ...(maxConcurrency === undefined ? {} : { maxConcurrency }),
    }, {
      execute: async (current) => {
        starts.push(current.id);
        active += 1;
        peak = Math.max(peak, active);
        await executionGate.promise;
        active -= 1;
        return { summary: current.id };
      },
    });

    try {
      await Promise.resolve();
      const expected = maxConcurrency ?? tasks.length;
      assert.equal(active, expected);
      assert.deepEqual(starts, tasks.slice(0, expected).map((current) => current.id));
    } finally {
      executionGate.release();
      await promise;
    }
    const result = await promise;
    assert.equal(result.state, "succeeded");
    assert.equal(peak, maxConcurrency ?? tasks.length);
    assert.equal(starts.length, tasks.length);
    assert.ok(result.tasks.every((entry) => entry.state === "succeeded"));
  });
}

test("concurrent plans keep resource scheduling and cancellation independent", async () => {
  const executionGate = gate();
  const controllers = [new AbortController(), new AbortController()];
  const starts = [[], []];
  const signals = [[], []];
  const updates = [undefined, undefined];
  const tasks = [
    task("a", { resourceKeys: ["repo:shared"] }),
    task("b", { resourceKeys: ["repo:shared"] }),
    task("c", { dependsOn: ["b"], resourceKeys: ["repo:shared"] }),
  ];
  const promises = controllers.map((controller, index) => runSubagentPlan({
    title: `independent plan ${index}`,
    tasks,
  }, {
    onUpdate: (snapshot) => { updates[index] = snapshot; },
    execute: async (current, { signal }) => {
      starts[index].push(current.id);
      signals[index].push(signal);
      await executionGate.promise;
      return { summary: current.id };
    },
  }, controller.signal));

  try {
    await Promise.resolve();
    assert.deepEqual(starts, [["a"], ["a"]]);
    assert.deepEqual(updates.map((snapshot) => snapshot.tasks.map((entry) => entry.state)), [
      ["running", "queued", "blocked"],
      ["running", "queued", "blocked"],
    ]);

    controllers[0].abort();
    const cancelled = await promises[0];
    assert.equal(cancelled.state, "cancelled");
    assert.ok(cancelled.tasks.every((entry) => entry.state === "cancelled"));
    assert.equal(signals[0][0].aborted, true);
    assert.equal(signals[1][0].aborted, false);
    assert.equal(updates[1].state, "running");
    assert.deepEqual(starts[1], ["a"]);

    executionGate.release();
    const succeeded = await promises[1];
    assert.equal(succeeded.state, "succeeded");
    assert.ok(succeeded.tasks.every((entry) => entry.state === "succeeded"));
    assert.deepEqual(starts, [["a"], ["a", "b", "c"]]);
    assert.ok(signals[1].every((signal) => !signal.aborted));
  } finally {
    controllers.forEach((controller) => controller.abort());
    executionGate.release();
    await Promise.all(promises);
  }
});

test("rejects ambiguous write lineages and in-place write retries", () => {
  assert.throws(() => normalizeSubagentPlan({
    title: "merge ambiguity",
    tasks: [
      task("left", { mode: "write" }),
      task("right", { mode: "write" }),
      task("join", { dependsOn: ["left", "right"] }),
    ],
  }), /multiple unmerged write branches/);
  assert.throws(() => normalizeSubagentPlan({
    title: "write retry",
    tasks: [task("write", { mode: "write", maxAttempts: 2 }), task("read")],
  }), /cannot retry in place/);
});

test("runs a DAG at the configured concurrency and unlocks dependents", async () => {
  let active = 0;
  let peak = 0;
  const starts = [];
  const result = await runSubagentPlan({
    title: "parallel",
    maxConcurrency: 2,
    tasks: [
      task("a"), task("b"),
      task("c", { dependsOn: ["a", "b"] }),
      task("d", { dependsOn: ["a"] }),
    ],
  }, {
    execute: async (current, { signal }) => {
      starts.push(current.id);
      active += 1;
      peak = Math.max(peak, active);
      await sleep(12, signal);
      active -= 1;
      return { summary: `done ${current.id}` };
    },
  });
  assert.equal(result.state, "succeeded");
  assert.equal(peak, 2);
  assert.deepEqual(starts.slice(0, 2).sort(), ["a", "b"]);
  assert.ok(starts.indexOf("c") > starts.indexOf("a"));
  assert.ok(starts.indexOf("c") > starts.indexOf("b"));
  assert.ok(result.tasks.every((entry) => entry.state === "succeeded"));
});

test("serializes conflicting resources while using other concurrency slots", async () => {
  const activeResources = new Set();
  let overlap = false;
  let peak = 0;
  let active = 0;
  const result = await runSubagentPlan({
    title: "resources",
    maxConcurrency: 3,
    tasks: [
      task("a", { resourceKeys: ["repo:one"] }),
      task("b", { resourceKeys: ["repo:one"] }),
      task("c", { resourceKeys: ["repo:two"] }),
    ],
  }, {
    execute: async (current, { signal }) => {
      active += 1;
      peak = Math.max(peak, active);
      for (const key of current.resourceKeys) {
        if (activeResources.has(key)) overlap = true;
        activeResources.add(key);
      }
      await sleep(15, signal);
      for (const key of current.resourceKeys) activeResources.delete(key);
      active -= 1;
      return { summary: current.id };
    },
  });
  assert.equal(result.state, "succeeded");
  assert.equal(overlap, false);
  assert.equal(peak, 2);
});

test("retries bounded transient failures and skips failed dependents", async () => {
  const attempts = new Map();
  const result = await runSubagentPlan({
    title: "retry",
    tasks: [
      task("retry", { maxAttempts: 2 }),
      task("fail"),
      task("after-fail", { dependsOn: ["fail"] }),
      task("after-retry", { dependsOn: ["retry"] }),
    ],
  }, {
    execute: async (current) => {
      const count = (attempts.get(current.id) ?? 0) + 1;
      attempts.set(current.id, count);
      if (current.id === "retry" && count === 1) throw new Error("transient");
      if (current.id === "fail") throw new Error("permanent");
      return { summary: current.id };
    },
  });
  assert.equal(result.state, "failed");
  assert.equal(attempts.get("retry"), 2);
  assert.equal(result.tasks.find((entry) => entry.id === "retry")?.state, "succeeded");
  assert.equal(result.tasks.find((entry) => entry.id === "after-retry")?.state, "succeeded");
  assert.equal(result.tasks.find((entry) => entry.id === "after-fail")?.state, "skipped");
});

test("cancels running and blocked tasks through the parent signal", async () => {
  const controller = new AbortController();
  const promise = runSubagentPlan({
    title: "cancel",
    maxConcurrency: 2,
    tasks: [task("a"), task("b"), task("c", { dependsOn: ["a"] })],
  }, {
    execute: (_current, { signal }) => sleep(5_000, signal).then(() => ({ summary: "late" })),
  }, controller.signal);
  await sleep(15);
  controller.abort();
  const result = await promise;
  assert.equal(result.state, "cancelled");
  assert.ok(result.tasks.every((entry) => entry.state === "cancelled"));
});

test("fail-fast aborts siblings without retrying them", async () => {
  const attempts = new Map();
  const result = await runSubagentPlan({
    title: "fail fast",
    failFast: true,
    maxConcurrency: 2,
    tasks: [task("fail"), task("slow", { maxAttempts: 3 }), task("queued")],
  }, {
    execute: async (current, { signal }) => {
      attempts.set(current.id, (attempts.get(current.id) ?? 0) + 1);
      if (current.id === "fail") throw new Error("boom");
      await sleep(5_000, signal);
      return { summary: current.id };
    },
  });
  assert.equal(result.state, "failed");
  assert.equal(attempts.get("slow"), 1);
  assert.equal(result.tasks.find((entry) => entry.id === "slow")?.state, "skipped");
  assert.equal(result.tasks.find((entry) => entry.id === "queued")?.state, "skipped");
});

test("timeout releases a task that ignores AbortSignal", async () => {
  const started = Date.now();
  const result = await runSubagentPlan({
    title: "non-cooperative timeout",
    tasks: [task("stalled", { timeoutMs: 1_000 }), task("next")],
    maxConcurrency: 1,
  }, {
    execute: (current) => current.id === "stalled"
      ? new Promise(() => {})
      : Promise.resolve({ summary: current.id }),
  });

  assert.ok(Date.now() - started < 2_000);
  assert.equal(result.state, "failed");
  assert.equal(result.tasks.find((entry) => entry.id === "stalled")?.state, "failed");
  assert.match(result.tasks.find((entry) => entry.id === "stalled")?.error ?? "", /timed out/);
  assert.equal(result.tasks.find((entry) => entry.id === "next")?.state, "succeeded");
});

test("parent cancellation releases a task that ignores AbortSignal", async () => {
  const controller = new AbortController();
  const promise = runSubagentPlan({
    title: "non-cooperative cancellation",
    tasks: [task("stalled"), task("queued")],
    maxConcurrency: 1,
  }, {
    execute: () => new Promise(() => {}),
  }, controller.signal);

  await sleep(10);
  controller.abort();
  const result = await Promise.race([
    promise,
    sleep(500).then(() => { throw new Error("cancellation did not settle"); }),
  ]);
  assert.equal(result.state, "cancelled");
  assert.ok(result.tasks.every((entry) => entry.state === "cancelled"));
});

test("missing display titles fall back to validated task IDs", () => {
  for (const title of [undefined, null, "", "  "]) {
    const plan = normalizeSubagentPlan({ title: "test", tasks: [
      { id: "runtime", title, prompt: "inspect lifecycle" },
      { id: "android", title: " Android ", prompt: "inspect devices" },
    ] });
    assert.equal(plan.tasks[0].title, "runtime");
    assert.equal(plan.tasks[1].title, "Android");
  }
  for (const invalid of [42, {}, "x".repeat(161)]) {
    assert.throws(() => normalizeSubagentPlan({ title: "test", tasks: [
      { id: "runtime", title: invalid, prompt: "inspect lifecycle" },
      { id: "android", title: "Android", prompt: "inspect devices" },
    ] }));
  }
});
