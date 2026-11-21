import assert from "node:assert/strict";
import test from "node:test";
import {
  createShellCommandRunner,
  riskRank,
  shellCommandNeedsConfirm,
} from "../src/domain/plugins/shell-commands.ts";

function env({ confirmAnswer = true, onConfirm, trusted = async () => false } = {}) {
  const commands = [
    { pluginId: "p", pluginName: "Sync", id: "sync.retry", title: "Retry sync", titleLocalized: { zh: "重新同步" }, action: "retry", risk: "write", effectiveRisk: "write" },
    { pluginId: "p", pluginName: "Sync", id: "sync.fetch", title: "Fetch", action: "fetch", risk: "read", effectiveRisk: "network" },
    { pluginId: "p", pluginName: "Sync", id: "sync.status", title: "Status", action: "status", risk: "read", effectiveRisk: "read" },
    { pluginId: "p", pluginName: "Sync", id: "sync.wipe", title: "Wipe", titleLocalized: { zh: "清空" }, action: "wipe", risk: "dangerous", effectiveRisk: "dangerous" },
    { pluginId: "p", pluginName: "Sync", id: "sync.legacy", title: "Legacy", action: "legacy", risk: "write" },
    { pluginId: "p", pluginName: "Sync", id: "sync.panel", title: "Panel", target: { type: "view", viewId: "panel" }, risk: "read", effectiveRisk: "dangerous" },
  ];
  const confirms = [];
  const executed = [];
  const runner = createShellCommandRunner({
    commands: () => commands,
    locale: () => ["zh"],
    t: (key, params) => (params ? `${key}|${params.plugin}|${params.command}|${params.origin}` : key),
    async confirm(request) {
      confirms.push(request);
      onConfirm?.(commands);
      return confirmAnswer;
    },
    trusted: (pluginId, origin, command) => trusted(pluginId, origin, command, commands),
    async execute(pluginId, command) {
      executed.push([pluginId, command]);
    },
  });
  return { runner, confirms, executed, commands };
}

function trustEnv({ trustedAnswer = true, confirmAnswer = false, onTrusted } = {}) {
  const trustCalls = [];
  const base = env({
    confirmAnswer,
    async trusted(pluginId, origin, command, commands) {
      trustCalls.push([pluginId, origin, command]);
      onTrusted?.(commands);
      if (trustedAnswer instanceof Error) throw trustedAnswer;
      return trustedAnswer;
    },
  });
  base.commands.push(
    { pluginId: "p.x", pluginName: "X", id: "fix", title: "Fix", action: "fix", risk: "write", effectiveRisk: "write" },
    { pluginId: "p.x", pluginName: "X", id: "wipe", title: "Wipe", action: "wipe", risk: "dangerous", effectiveRisk: "dangerous" },
    { pluginId: "p.x", pluginName: "X", id: "look", title: "Look", action: "look", risk: "read", effectiveRisk: "read" },
  );
  return { ...base, trustCalls };
}

test("shell-commands: risk rank and thresholds", () => {
  assert.deepEqual(["read", "network", "write", "dangerous", undefined, "odd"].map((risk) => riskRank(risk)), [0, 1, 2, 3, 3, 3]);
  assert.equal(shellCommandNeedsConfirm("notification", { effectiveRisk: "read" }), false);
  assert.equal(shellCommandNeedsConfirm("notification", { effectiveRisk: "network" }), true);
  assert.equal(shellCommandNeedsConfirm("tray", { effectiveRisk: "write" }), false);
  assert.equal(shellCommandNeedsConfirm("tray", { effectiveRisk: "dangerous" }), true);
  assert.equal(shellCommandNeedsConfirm("tray", {}), true);
  assert.equal(shellCommandNeedsConfirm("tray", { target: { type: "view", viewId: "v" } }), false);
});

test("shell-commands: only a view target skips the risk decision", async () => {
  for (const target of [null, { type: "url", url: "https://example.com" }, { viewId: "v" }, "view", 0, false]) {
    assert.equal(shellCommandNeedsConfirm("tray", { target }), true, `${JSON.stringify(target)} with no risk confirms`);
    assert.equal(shellCommandNeedsConfirm("tray", { target, effectiveRisk: "dangerous" }), true);
    assert.equal(shellCommandNeedsConfirm("tray", { target, effectiveRisk: "write" }), false, "tray threshold still applies");
    assert.equal(shellCommandNeedsConfirm("notification", { target, effectiveRisk: "network" }), true);
    assert.equal(shellCommandNeedsConfirm("notification", { target, effectiveRisk: "read" }), false);
  }
  const odd = env({ confirmAnswer: false });
  odd.commands.push(
    { pluginId: "p", pluginName: "Sync", id: "sync.null", title: "Null target", target: null, risk: "write", effectiveRisk: "write" },
    { pluginId: "p", pluginName: "Sync", id: "sync.odd", title: "Odd target", target: { type: "shell" }, risk: "read", effectiveRisk: "dangerous" },
  );
  assert.equal(await odd.runner.run("notification", "p", "sync.null"), "cancelled");
  assert.equal(await odd.runner.run("tray", "p", "sync.odd"), "cancelled");
  assert.equal(odd.confirms.length, 2);
  assert.equal(odd.confirms[1].dangerous, true);
  assert.deepEqual(odd.executed, []);
});

test("shell-commands: notification origin confirms non-read with manifest title", async () => {
  const write = env();
  assert.equal(await write.runner.run("notification", "p", "sync.retry"), "ran");
  assert.equal(write.confirms.length, 1);
  const [request] = write.confirms;
  assert.equal(request.title, "pluginContrib.shell.confirmTitle");
  assert.equal(request.dangerous, false);
  assert.equal(request.message, "pluginContrib.shell.confirmWrite|Sync|重新同步|pluginContrib.shell.originNotification");
  assert.match(request.message, /Sync/);
  assert.match(request.message, /重新同步/);
  assert.doesNotMatch(request.message, /OK/, "the plugin-controlled button text never reaches the dialog");
  assert.deepEqual(write.executed, [["p", "sync.retry"]]);

  const network = env();
  await network.runner.run("notification", "p", "sync.fetch");
  assert.equal(network.confirms.length, 1, "network is not read: confirm");
  assert.deepEqual(network.executed, [["p", "sync.fetch"]]);

  const dangerous = env();
  await dangerous.runner.run("notification", "p", "sync.wipe");
  assert.equal(dangerous.confirms[0].dangerous, true);
  assert.equal(dangerous.confirms[0].message, "pluginContrib.shell.confirmDangerous|Sync|清空|pluginContrib.shell.originNotification");

  const read = env();
  assert.equal(await read.runner.run("notification", "p", "sync.status"), "ran");
  assert.deepEqual(read.confirms, [], "read runs directly");
  assert.deepEqual(read.executed, [["p", "sync.status"]]);
});

test("shell-commands: tray origin confirms only dangerous", async () => {
  const write = env();
  await write.runner.run("tray", "p", "sync.retry");
  await write.runner.run("tray", "p", "sync.fetch");
  assert.deepEqual(write.confirms, []);
  assert.deepEqual(write.executed, [["p", "sync.retry"], ["p", "sync.fetch"]]);

  const dangerous = env();
  await dangerous.runner.run("tray", "p", "sync.wipe");
  await dangerous.runner.run("tray", "p", "sync.legacy");
  assert.equal(dangerous.confirms.length, 2, "dangerous and a missing effectiveRisk confirm");
  assert.ok(dangerous.confirms.every((request) => request.dangerous === true));
  assert.equal(dangerous.confirms[0].message, "pluginContrib.shell.confirmDangerous|Sync|清空|pluginContrib.shell.originTray");
  assert.deepEqual(dangerous.executed, [["p", "sync.wipe"], ["p", "sync.legacy"]]);
});

test("shell-commands: view target never confirms", async () => {
  for (const origin of ["notification", "tray"]) {
    const view = env();
    assert.equal(await view.runner.run(origin, "p", "sync.panel"), "ran");
    assert.deepEqual(view.confirms, []);
    assert.deepEqual(view.executed, [["p", "sync.panel"]]);
  }
});

test("shell-commands: cancelled confirmation runs nothing", async () => {
  const cancelled = env({ confirmAnswer: false });
  assert.equal(await cancelled.runner.run("notification", "p", "sync.retry"), "cancelled");
  assert.equal(await cancelled.runner.run("tray", "p", "sync.wipe"), "cancelled");
  assert.equal(cancelled.confirms.length, 2);
  assert.deepEqual(cancelled.executed, []);
});

test("shell-commands: changed command after confirm does not run", async () => {
  for (const change of [
    (commands) => { commands[0] = { ...commands[0], effectiveRisk: "dangerous" }; },
    (commands) => { commands[0] = { ...commands[0], titleLocalized: { zh: "删除全部" } }; },
    (commands) => { commands[0] = { ...commands[0], action: "other" }; },
    (commands) => { commands.splice(0, 1); },
  ]) {
    const changed = env({ onConfirm: change });
    await assert.rejects(changed.runner.run("notification", "p", "sync.retry"), (error) => error.reason === "changed");
    assert.deepEqual(changed.executed, []);
  }
});

test("shell-commands: missing command throws unavailable", async () => {
  const missing = env();
  await assert.rejects(missing.runner.run("notification", "p", "sync.gone"), (error) => error.reason === "unavailable");
  await assert.rejects(missing.runner.run("tray", "other", "sync.retry"), (error) => error.reason === "unavailable", "another plugin's command is not this plugin's");
  assert.deepEqual(missing.confirms, []);
  assert.deepEqual(missing.executed, []);
});

test("shell-commands: trusted command skips the dialog", async () => {
  const trusted = trustEnv();
  assert.equal(await trusted.runner.run("notification", "p.x", "fix"), "ran");
  assert.deepEqual(trusted.confirms, [], "confirm is not shown");
  assert.deepEqual(trusted.executed, [["p.x", "fix"]]);
  assert.equal(trusted.trustCalls.length, 1);
  assert.equal(trusted.trustCalls[0][0], "p.x");
  assert.equal(trusted.trustCalls[0][1], "notification");
  assert.equal(trusted.trustCalls[0][2].id, "fix");
  assert.equal(trusted.trustCalls[0][2].effectiveRisk, "write");

  const tray = trustEnv();
  assert.equal(await tray.runner.run("tray", "p.x", "wipe"), "ran");
  assert.deepEqual(tray.confirms, []);
  assert.deepEqual(tray.executed, [["p.x", "wipe"]]);
  assert.deepEqual(tray.trustCalls.map(([pluginId, origin, command]) => [pluginId, origin, command.id]), [["p.x", "tray", "wipe"]]);
});

test("shell-commands: untrusted or failing trust check still confirms", async () => {
  for (const trustedAnswer of [false, new Error("authorize failed"), null, "yes"]) {
    for (const [origin, command] of [["notification", "fix"], ["tray", "wipe"]]) {
      const untrusted = trustEnv({ trustedAnswer });
      assert.equal(await untrusted.runner.run(origin, "p.x", command), "cancelled");
      assert.equal(untrusted.trustCalls.length, 1);
      assert.equal(untrusted.confirms.length, 1, `${String(trustedAnswer)} on ${origin} falls back to the dialog`);
      assert.deepEqual(untrusted.executed, []);
    }
  }
});

test("shell-commands: trusted command still re-checked", async () => {
  for (const change of [
    (commands) => { const i = commands.findIndex((c) => c.pluginId === "p.x" && c.id === "fix"); commands[i] = { ...commands[i], effectiveRisk: "dangerous" }; },
    (commands) => { const i = commands.findIndex((c) => c.pluginId === "p.x" && c.id === "fix"); commands[i] = { ...commands[i], action: "other" }; },
    (commands) => { commands.splice(commands.findIndex((c) => c.pluginId === "p.x" && c.id === "fix"), 1); },
  ]) {
    const changed = trustEnv({ onTrusted: change });
    await assert.rejects(
      changed.runner.run("notification", "p.x", "fix"),
      (error) => error.name === "ShellCommandError" && error.reason === "changed",
    );
    assert.deepEqual(changed.confirms, []);
    assert.deepEqual(changed.executed, []);
  }
});

test("shell-commands: read never asks trust", async () => {
  const read = trustEnv();
  assert.equal(await read.runner.run("notification", "p.x", "look"), "ran");
  assert.equal(await read.runner.run("tray", "p.x", "look"), "ran");
  assert.deepEqual(read.trustCalls, []);
  assert.deepEqual(read.confirms, []);
});

test("shell-commands: tray write never asks trust", async () => {
  const tray = trustEnv();
  assert.equal(await tray.runner.run("tray", "p.x", "fix"), "ran");
  assert.deepEqual(tray.trustCalls, []);
  assert.deepEqual(tray.confirms, []);
  assert.deepEqual(tray.executed, [["p.x", "fix"]]);
});

test("shell-commands: missing command never asks trust", async () => {
  const missing = trustEnv();
  await assert.rejects(missing.runner.run("notification", "p.x", "gone"), (error) => error.reason === "unavailable");
  assert.deepEqual(missing.trustCalls, []);
});
