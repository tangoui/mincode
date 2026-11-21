import assert from "node:assert/strict";
import test from "node:test";

const { createStartupIdleBridgeCoordinator } = await import("../src/services/startup-idle-bridges.ts");

function scheduler() {
  const tasks = [];
  return {
    tasks,
    schedule(task) {
      tasks.push(task);
      return () => {
        const index = tasks.indexOf(task);
        if (index >= 0) tasks.splice(index, 1);
      };
    },
  };
}

test("idle bridges do not load before the scheduler runs", async () => {
  const queued = scheduler();
  let loads = 0;
  createStartupIdleBridgeCoordinator([{ name: "plugin", load: async () => { loads += 1; } }], queued.schedule);
  assert.equal(loads, 0);
  assert.equal(queued.tasks.length, 1);
});

test("dispose cancels queued bridges and disposes an installed bridge", async () => {
  const queued = scheduler();
  let disposed = 0;
  const stop = createStartupIdleBridgeCoordinator([{ name: "plugin", load: async () => () => { disposed += 1; } }], queued.schedule);
  queued.tasks.splice(0).forEach((task) => task());
  await new Promise((resolve) => setTimeout(resolve, 0));
  stop();
  assert.equal(disposed, 1);
  assert.equal(queued.tasks.length, 0);
});

test("one bridge failure does not prevent other idle bridges", async () => {
  const queued = scheduler();
  let loaded = 0;
  const errors = [];
  createStartupIdleBridgeCoordinator([
    { name: "broken", load: async () => { throw new Error("broken"); } },
    { name: "healthy", load: async () => { loaded += 1; } },
  ], queued.schedule, (name, error) => errors.push(`${name}:${error.message}`));
  queued.tasks.splice(0).forEach((task) => task());
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(loaded, 1);
  assert.deepEqual(errors, ["broken:broken"]);
});
