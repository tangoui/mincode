import assert from "node:assert/strict";
import test from "node:test";
import { createPredictionGate } from "../src/domain/terminal/predictive-echo/gate.ts";

const record = (gate, ms, times = 16) => {
  for (let i = 0; i < times; i += 1) gate.recordLatency(ms);
};

/** always 模式且本行已确认，只剩猜错/暂停影响 allowance。 */
const confirmedAlways = () => {
  const gate = createPredictionGate("always");
  gate.markEchoConfirmed();
  return gate;
};

test("auto mode waits for samples", () => {
  const gate = createPredictionGate("auto");
  assert.equal(gate.allowance(0, 0), 0);
  gate.markEchoConfirmed();
  assert.equal(gate.allowance(0, 0), 0, "no latency samples yet, so auto does not predict");
  assert.equal(gate.medianLatency(), null);
});

test("auto hysteresis", () => {
  const gate = createPredictionGate("auto");
  record(gate, 40);
  gate.markEchoConfirmed();
  assert.equal(gate.medianLatency(), 40);
  assert.equal(gate.allowance(0, 0), Infinity);
  record(gate, 25);
  assert.equal(gate.medianLatency(), 25);
  assert.equal(gate.allowance(0, 0), Infinity, "between 20 and 30ms stays on");
  record(gate, 15);
  assert.equal(gate.medianLatency(), 15);
  assert.equal(gate.allowance(0, 0), 0);
  record(gate, 25);
  assert.equal(gate.allowance(0, 0), 0, "between 20 and 30ms stays off");
});

test("median uses only the latest 16 samples", () => {
  const gate = createPredictionGate("auto");
  record(gate, 1000, 10);
  record(gate, 40, 16);
  assert.equal(gate.medianLatency(), 40);
  const even = createPredictionGate("auto");
  even.recordLatency(10);
  even.recordLatency(30);
  assert.equal(even.medianLatency(), 20);
});

test("always ignores latency", () => {
  const gate = createPredictionGate("always");
  gate.markEchoConfirmed();
  assert.equal(gate.allowance(0, 0), Infinity, "no samples needed");
  record(gate, 1);
  assert.equal(gate.allowance(0, 0), Infinity, "fast echo still predicts");
});

test("off never predicts", () => {
  const gate = createPredictionGate("off");
  record(gate, 200);
  gate.markEchoConfirmed();
  assert.equal(gate.allowance(0, 0), 0);
  gate.setMode("always");
  assert.equal(gate.allowance(0, 0), Infinity);
  gate.setMode("off");
  assert.equal(gate.allowance(0, 0), 0);
});

test("first char rule", () => {
  const gate = createPredictionGate("always");
  assert.equal(gate.lineConfirmed(), false);
  assert.equal(gate.allowance(0, 0), 0);
  gate.markEchoConfirmed();
  assert.equal(gate.lineConfirmed(), true);
  assert.equal(gate.allowance(0, 0), Infinity);
  gate.clearLine();
  assert.equal(gate.lineConfirmed(), false);
  assert.equal(gate.allowance(0, 0), 0);
});

test("first char rule applies in tentative mode too", () => {
  const gate = createPredictionGate("always");
  gate.enterTentative();
  assert.equal(gate.allowance(0, 0), 0);
  gate.recordMiss(0);
  assert.equal(gate.allowance(0, 0), 0);
});

test("tentative after miss", () => {
  const gate = confirmedAlways();
  gate.recordMiss(100);
  assert.equal(gate.allowance(100, 0), 1);
  assert.equal(gate.allowance(100, 1), 0);
  gate.markEchoConfirmed();
  assert.equal(gate.allowance(100, 0), Infinity);
  assert.equal(gate.allowance(100, 3), Infinity);
});

test("enterTentative starts with one char at a time", () => {
  const gate = confirmedAlways();
  gate.enterTentative();
  assert.equal(gate.allowance(0, 0), 1);
  assert.equal(gate.allowance(0, 1), 0);
  gate.markEchoConfirmed();
  assert.equal(gate.allowance(0, 0), Infinity);
});

test("backoff", () => {
  const gate = confirmedAlways();
  gate.recordMiss(0);
  gate.recordMiss(1000);
  gate.recordMiss(2000);
  assert.equal(gate.allowance(2000, 0), 0);
  assert.equal(gate.allowance(6999, 0), 0);
  assert.equal(gate.allowance(7001, 0), 1, "after the pause the gate is tentative");
  assert.equal(gate.allowance(7001, 1), 0);

  gate.recordMiss(8000);
  gate.recordMiss(9000);
  gate.recordMiss(10000);
  assert.equal(gate.allowance(19999, 0), 0, "second pause doubles to 10000ms");
  assert.equal(gate.allowance(20001, 0), 1);

  let t = 21000;
  const pauses = [];
  for (let round = 0; round < 4; round += 1) {
    gate.recordMiss(t);
    gate.recordMiss(t + 100);
    gate.recordMiss(t + 200);
    const start = t + 200;
    let end = start;
    while (gate.allowance(end, 0) === 0) end += 1;
    pauses.push(end - start);
    t = end + 1000;
  }
  assert.deepEqual(pauses, [20000, 40000, 60000, 60000], "doubles up to the 60000ms cap");
});

test("misses spread over more than 10 seconds do not pause", () => {
  const gate = confirmedAlways();
  gate.recordMiss(0);
  gate.recordMiss(6000);
  gate.recordMiss(12000);
  assert.equal(gate.allowance(12000, 0), 1);
});

test("backoff doubling resets after 60 seconds without misses", () => {
  const gate = confirmedAlways();
  gate.recordMiss(0);
  gate.recordMiss(100);
  gate.recordMiss(200);
  // 暂停到 5200，此后 60 秒无猜错
  const later = 5200 + 60000;
  gate.recordMiss(later);
  gate.recordMiss(later + 100);
  gate.recordMiss(later + 200);
  assert.equal(gate.allowance(later + 200 + 4999, 0), 0);
  assert.equal(gate.allowance(later + 200 + 5001, 0), 1, "pause is back to 5000ms");
});

test("timeout", () => {
  const gate = createPredictionGate("auto");
  assert.equal(gate.timeoutMs(), 1000);
  record(gate, 100);
  assert.equal(gate.timeoutMs(), 1000);
  record(gate, 500);
  assert.equal(gate.timeoutMs(), 1500);
});

test("first echo of a line does not end tentative mode", () => {
  const gate = confirmedAlways();
  gate.clearLine();
  gate.enterTentative();
  gate.markEchoConfirmed();
  assert.equal(gate.allowance(0, 0), 1, "the first echo was never a prediction");
  assert.equal(gate.allowance(0, 1), 0);
  gate.markEchoConfirmed();
  assert.equal(gate.allowance(0, 0), Infinity);
});

test("echo during a pause does not skip tentative mode after it", () => {
  const gate = confirmedAlways();
  gate.recordMiss(0);
  gate.recordMiss(100);
  gate.recordMiss(200);
  gate.markEchoConfirmed();
  assert.equal(gate.allowance(3000, 0), 0);
  assert.equal(gate.allowance(5201, 0), 1);
  assert.equal(gate.allowance(5201, 1), 0);
  gate.markEchoConfirmed();
  assert.equal(gate.allowance(5201, 0), Infinity, "a confirmation after the pause restores continuous mode");
});

test("misses during a pause neither extend it nor count toward the next one", () => {
  const gate = confirmedAlways();
  gate.recordMiss(0);
  gate.recordMiss(100);
  gate.recordMiss(200);
  gate.recordMiss(1000);
  gate.recordMiss(2000);
  gate.recordMiss(3000);
  assert.equal(gate.allowance(5199, 0), 0);
  assert.equal(gate.allowance(5201, 0), 1, "pause still ends at 5200");
  gate.recordMiss(5300);
  gate.recordMiss(5400);
  assert.equal(gate.allowance(5400, 0), 1, "only the two misses after the pause count");
  gate.recordMiss(5500);
  assert.equal(gate.allowance(5500, 0), 0, "third miss after the pause starts the next one");
  assert.equal(gate.allowance(5500 + 9999, 0), 0, "and it is doubled to 10000ms");
  assert.equal(gate.allowance(5500 + 10001, 0), 1);
});
