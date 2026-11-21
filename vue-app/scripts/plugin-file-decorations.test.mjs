import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import {
  collectDecorationProviders,
  createDecorationQueue,
  DECORATION_COLOR_TOKENS,
  decorationDirectoryOK,
  decorationEligible,
  decorationFailureCounts,
  decorationNameOK,
  loadedDirectoryOf,
  orderByRecency,
  FILE_DECORATION_LIMITS,
  parseFileDecorationsResponse,
  pickDecoration,
} from "../src/domain/plugins/file-decorations.ts";

const contract = JSON.parse(await readFile(new URL("../../plugins/api/contribution-points.json", import.meta.url), "utf8"));

function contribution(pluginId, pluginName, entries, overrides = {}) {
  return {
    pluginId,
    pluginName,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: "f", entries, issues: [] },
    ...overrides,
  };
}

function provider(pluginId, pluginName = pluginId) {
  return { pluginId, pluginName, id: "lint", title: "Lint", activeInWorkspace: true };
}

/** Fake clock and a request double whose answers the test settles by hand. */
function harness(overrides = {}) {
  let clock = 0;
  let nextHandle = 0;
  const timers = new Map();
  const calls = [];
  const options = {
    debounceMs: 150,
    refreshDirectoriesMax: 32,
    cachedDirectoriesMax: 64,
    failuresMax: 3,
    suspendMs: 60_000,
    now: () => clock,
    setTimer(fn, ms) {
      const handle = ++nextHandle;
      timers.set(handle, { at: clock + ms, fn });
      return handle;
    },
    clearTimer(handle) {
      timers.delete(handle);
    },
    request(providerValue, projectRoot, directory, names, signal) {
      return new Promise((resolve) => calls.push({ provider: providerValue, projectRoot, directory, names, signal, resolve }));
    },
    ...overrides,
  };
  const queue = createDecorationQueue(options);
  async function flush() {
    for (let i = 0; i < 5; i += 1) await Promise.resolve();
  }
  async function advance(ms) {
    const target = clock + ms;
    for (;;) {
      const due = [...timers.entries()].filter(([, timer]) => timer.at <= target).sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) break;
      timers.delete(due[0]);
      clock = due[1].at;
      due[1].fn();
      await flush();
    }
    clock = target;
    await flush();
  }
  async function settle(call, value) {
    call.resolve(value);
    await flush();
  }
  return { queue, calls, advance, settle, flush };
}

const decorated = (names) => new Map(names.map((name) => [name, { badge: "E", color: "danger" }]));

test("file-decorations: limits mirror the contract", () => {
  const point = contract.points.fileDecorations;
  for (const [name, value] of Object.entries(FILE_DECORATION_LIMITS)) {
    if (name === "maxEntries") assert.equal(value, point.maxEntries);
    else assert.equal(value, point.limits[name], name);
  }
  assert.deepEqual(Object.keys(DECORATION_COLOR_TOKENS), point.lists.colors);
});

test("file-decorations: colour maps to tokens only", () => {
  assert.deepEqual(DECORATION_COLOR_TOKENS, {
    accent: "var(--app-accent)",
    success: "var(--app-success)",
    warning: "var(--app-warning)",
    danger: "var(--app-danger)",
    info: "var(--app-info)",
    muted: "var(--app-text-muted)",
  });
  const requested = ["ok.ts", "dot.ts", "css.ts", "long.ts", "zwj.ts", "bidi.ts", "space.ts", "empty.ts", "tip.ts", "李.ts"];
  const parsed = parseFileDecorationsResponse(
    {
      decorations: {
        "ok.ts": { badge: " E ", color: "danger", tooltip: "2 errors" },
        "dot.ts": { color: "muted" },
        "css.ts": { badge: "E", color: "#f00" },
        "long.ts": { badge: "ERR" },
        "zwj.ts": { badge: "a‍b" },
        "bidi.ts": { badge: "‮E" },
        "space.ts": { badge: "a b" },
        "empty.ts": { tooltip: "no badge, no colour" },
        "tip.ts": { badge: "W", tooltip: "line\nbreak" },
        "李.ts": { badge: "李" },
        "unrequested.ts": { badge: "X" },
        __proto__: { badge: "P" },
      },
      dropped: 1,
      skipped: 0,
    },
    requested,
  );
  assert.deepEqual([...parsed.entries()], [
    ["ok.ts", { badge: "E", color: "danger", tooltip: "2 errors" }],
    ["dot.ts", { color: "muted" }],
    ["李.ts", { badge: "李" }],
  ]);
  assert.equal(parseFileDecorationsResponse({ decorations: { "ok.ts": { color: "red" } } }, requested).size, 0);
  assert.equal(parseFileDecorationsResponse({ decorations: { "ok.ts": { color: "var(--app-danger)" } } }, requested).size, 0);
  assert.equal(parseFileDecorationsResponse(null, requested).size, 0);
  assert.equal(parseFileDecorationsResponse({ decorations: [] }, requested).size, 0);
});

test("file-decorations: host nodes are never decorated", () => {
  assert.equal(decorationEligible({ isProjectRoot: true, path: "" }, ""), false);
  assert.equal(decorationEligible({ worktree: {}, path: "x" }, ""), false);
  assert.equal(decorationEligible({ path: "a.ts" }, "a.ts"), false);
  assert.equal(decorationEligible({ path: "" }, ""), false);
  assert.equal(decorationEligible({ path: "a.ts" }, ""), true);
  assert.equal(decorationEligible({ path: "src/a.ts" }, "a.ts"), true);
});

test("file-decorations: collect providers ordered by plugin name, then id", () => {
  const providers = collectDecorationProviders([
    contribution("zeta.plugin", "beta", [{ id: "lint", title: "Lint", action: "decorations.read" }]),
    contribution("b.plugin", "Alpha", [
      { id: "owners", title: "Owners", titleLocalized: { zh: "负责人" }, action: "decorations.read" },
      // Past maxEntries: ignored.
      { id: "second", title: "Second", action: "decorations.read" },
    ]),
    contribution("a.plugin", "alpha", [{ id: "gen", title: "Generated", action: "decorations.read" }], { activeInWorkspace: false }),
    contribution("decl.plugin", "Decl", [{ id: "decl", title: "Decl", action: "x" }], { runtime: "declarative" }),
    contribution("bad.plugin", "Bad", [{ id: "Bad", title: "Bad", action: "x" }]),
    contribution("noaction.plugin", "NoAction", [{ id: "lint", title: "Lint" }]),
  ]);
  assert.deepEqual(providers, [
    { pluginId: "a.plugin", pluginName: "alpha", id: "gen", title: "Generated", activeInWorkspace: false },
    { pluginId: "b.plugin", pluginName: "Alpha", id: "owners", title: "Owners", activeInWorkspace: true },
    { pluginId: "zeta.plugin", pluginName: "beta", id: "lint", title: "Lint", activeInWorkspace: true },
  ]);
});

test("file-decorations: first provider wins", () => {
  assert.equal(pickDecoration([]), undefined);
  assert.equal(pickDecoration([undefined, undefined]), undefined);
  const alpha = { badge: "A", pluginName: "Alpha" };
  const beta = { badge: "B", color: "danger", pluginName: "beta" };
  assert.deepEqual(pickDecoration([undefined, alpha, beta]), alpha);

  // Through the queue: the provider sorted first (Alpha before beta) wins, whatever answered first.
  return (async () => {
    const { queue, calls, advance, settle } = harness();
    const betaProvider = provider("a.beta", "beta");
    const alphaProvider = provider("z.alpha", "Alpha");
    queue.enqueue(betaProvider, "/p", "src", ["a.ts"]);
    queue.enqueue(alphaProvider, "/p", "src", ["a.ts"]);
    await advance(150);
    assert.equal(calls.length, 2);
    await settle(calls.find((call) => call.provider === betaProvider), new Map([["a.ts", { badge: "B", color: "danger" }]]));
    assert.deepEqual(queue.lookup("/p", "src/a.ts"), { badge: "B", color: "danger", pluginName: "beta" });
    await settle(calls.find((call) => call.provider === alphaProvider), new Map([["a.ts", { badge: "A" }]]));
    assert.deepEqual(queue.lookup("/p", "src/a.ts"), { badge: "A", pluginName: "Alpha" });
    assert.equal(queue.lookup("/p", "src/b.ts"), undefined);
    assert.equal(queue.lookup("/other", "src/a.ts"), undefined);
  })();
});

test("file-decorations: debounce coalesces", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  let changes = 0;
  queue.onChange(() => changes++);
  queue.enqueue(lint, "/p", "", ["a.ts"]);
  await advance(100);
  queue.enqueue(lint, "/p", "", ["a.ts", "b.ts"]);
  await advance(100);
  queue.enqueue(lint, "/p", "", ["a.ts", "b.ts", "c.ts"]);
  await advance(149);
  assert.equal(calls.length, 0);
  await advance(1);
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].names, ["a.ts", "b.ts", "c.ts"]);
  assert.equal(calls[0].directory, "");
  await settle(calls[0], decorated(["c.ts"]));
  assert.equal(changes, 1);
  assert.deepEqual(queue.lookup("/p", "c.ts"), { badge: "E", color: "danger", pluginName: "lint.plugin" });
  await advance(1000);
  assert.equal(calls.length, 1);
});

test("file-decorations: names are capped at 500 per request", async () => {
  const { queue, calls, advance } = harness();
  queue.enqueue(provider("lint.plugin"), "/p", "big", Array.from({ length: 800 }, (_, i) => `f${i}`));
  await advance(150);
  assert.equal(calls[0].names.length, 500);
  assert.equal(calls[0].names[499], "f499");
});

test("file-decorations: refresh cycle requests at most 32 directories", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  for (let i = 0; i < 40; i += 1) queue.enqueue(lint, "/p", `d${i}`, ["a.ts"]);
  await advance(150);
  for (let i = 0; i < 40 && calls[i]; i += 1) await settle(calls[i], new Map());
  await advance(10_000);
  assert.equal(calls.length, 32);
  assert.deepEqual(
    calls.map((call) => call.directory).sort(),
    Array.from({ length: 32 }, (_, i) => `d${i + 8}`).sort(),
  );
});

test("file-decorations: one request in flight per provider", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  const other = provider("other.plugin");
  queue.enqueue(lint, "/p", "a", ["x.ts"]);
  queue.enqueue(lint, "/p", "b", ["y.ts"]);
  queue.enqueue(other, "/p", "a", ["x.ts"]);
  await advance(150);
  // One per provider: lint's "a" and other's "a".
  assert.equal(calls.length, 2);
  assert.deepEqual(calls.map((call) => `${call.provider.pluginId}:${call.directory}`), ["lint.plugin:a", "other.plugin:a"]);
  await advance(5000);
  assert.equal(calls.length, 2);
  await settle(calls[0], new Map());
  assert.equal(calls.length, 3);
  assert.equal(calls[2].directory, "b");
});

test("file-decorations: three failures suspend the provider for 60 s", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  queue.enqueue(lint, "/p", "kept", ["a.ts"]);
  await advance(150);
  await settle(calls[0], decorated(["a.ts"]));
  for (let i = 0; i < 3; i += 1) {
    queue.enqueue(lint, "/p", `f${i}`, ["a.ts"]);
    await advance(150);
    await settle(calls.at(-1), "failed");
  }
  assert.equal(calls.length, 4);
  // Suspended since the third failure: enqueues are dropped, cached decorations stay.
  await advance(59_999 - 150 - 1);
  queue.enqueue(lint, "/p", "later", ["a.ts"]);
  await advance(150);
  assert.equal(calls.length, 4);
  assert.deepEqual(queue.lookup("/p", "kept/a.ts"), { badge: "E", color: "danger", pluginName: "lint.plugin" });
  // 60 000 ms after the third failure the provider is asked again.
  await advance(1);
  queue.enqueue(lint, "/p", "later", ["a.ts"]);
  await advance(150);
  assert.equal(calls.length, 5);
  assert.equal(calls[4].directory, "later");
  // A success resets the count: two more failures do not suspend.
  await settle(calls[4], new Map());
  for (let i = 0; i < 2; i += 1) {
    queue.enqueue(lint, "/p", `g${i}`, ["a.ts"]);
    await advance(150);
    await settle(calls.at(-1), "failed");
  }
  queue.enqueue(lint, "/p", "after", ["a.ts"]);
  await advance(150);
  assert.equal(calls.at(-1).directory, "after");
});

test("file-decorations: failures in one round stop the rest of that round", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  for (let i = 0; i < 10; i += 1) queue.enqueue(lint, "/p", `d${i}`, ["a.ts"]);
  await advance(150);
  for (let i = 0; i < 3; i += 1) await settle(calls[i], "failed");
  await advance(1000);
  assert.equal(calls.length, 3);
});

test("file-decorations: cache keeps 64 directories", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  for (let i = 0; i < 65; i += 1) {
    queue.enqueue(lint, "/p", `d${i}`, ["a.ts"]);
    await advance(150);
    await settle(calls[i], decorated(["a.ts"]));
  }
  assert.equal(calls.length, 65);
  assert.equal(queue.lookup("/p", "d0/a.ts"), undefined);
  assert.deepEqual(queue.lookup("/p", "d1/a.ts"), { badge: "E", color: "danger", pluginName: "lint.plugin" });
  assert.deepEqual(queue.lookup("/p", "d64/a.ts"), { badge: "E", color: "danger", pluginName: "lint.plugin" });
});

test("file-decorations: collapse cancels", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  queue.enqueue(lint, "/p", "src", ["a.ts"]);
  queue.enqueue(lint, "/p", "src/lib", ["b.ts"]);
  queue.enqueue(lint, "/p", "docs", ["c.md"]);
  await advance(150);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].directory, "src");
  queue.cancelDirectory("/p", "src");
  assert.equal(calls[0].signal.aborted, true);
  await advance(0);
  // The queued subdirectory of the collapsed folder is dropped; "docs" goes next.
  assert.equal(calls.length, 2);
  assert.equal(calls[1].directory, "docs");
  // A late answer of the cancelled request is ignored.
  await settle(calls[0], decorated(["a.ts"]));
  assert.equal(queue.lookup("/p", "src/a.ts"), undefined);
  await settle(calls[1], new Map());
  await advance(1000);
  assert.equal(calls.length, 2);

  // Queued (not yet sent) requests are dropped too.
  queue.enqueue(lint, "/p", "src", ["a.ts"]);
  await advance(100);
  queue.cancelDirectory("/p", "src");
  await advance(1000);
  assert.equal(calls.length, 2);
});

test("file-decorations: clear drops a plugin's state", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  const other = provider("other.plugin");
  queue.enqueue(lint, "/p", "", ["a.ts"]);
  queue.enqueue(other, "/p", "", ["a.ts"]);
  await advance(150);
  await settle(calls[0], decorated(["a.ts"]));
  queue.clear("lint.plugin");
  assert.equal(queue.lookup("/p", "a.ts"), undefined);
  assert.equal(calls[1].signal.aborted, false);
  queue.clear();
  assert.equal(calls[1].signal.aborted, true);
});

test("file-decorations: directory and name rules mirror the host", () => {
  for (const ok of ["", "src", "src/lib", "a b", "李/文档", "v1.2", "-x", "a~b"]) assert.equal(decorationDirectoryOK(ok), true, ok);
  for (const bad of ["C#", "a/C#", "100%", "what?", "a:b", "a\\b", "~home", "$HOME", "/abs", "a/", "a//b", "./a", "a/./b", "..", "../a", "a/..", "a\u0000b", "a‍b", "a‮b", "a­b", "a b", "a．b", "a／b", "x".repeat(1025)]) {
    assert.equal(decorationDirectoryOK(bad), false, JSON.stringify(bad));
  }
  for (const ok of ["a.ts", "C#", "100%.md", "~tmp", ".env.example", "李.ts", "a b"]) assert.equal(decorationNameOK(ok), true, ok);
  for (const bad of ["", ".", "..", "a/b", "a\\b", "a\u0000", "a‍", "a‮", "a ", "x".repeat(256)]) {
    assert.equal(decorationNameOK(bad), false, JSON.stringify(bad));
  }
});

test("file-decorations: only provider failures count", () => {
  for (const code of ["timeout", "unavailable", "busy", "invalid_result", "plugin_error"]) assert.equal(decorationFailureCounts(code), true, code);
  for (const code of ["invalid_request", "forbidden", "not_found", "too_large", "cancelled", "other"]) assert.equal(decorationFailureCounts(code), false, code);
});

test("file-decorations: requests the host would reject are never sent", async () => {
  const { queue, calls, advance } = harness();
  const lint = provider("lint.plugin");
  queue.enqueue(lint, "/p", "empty", []);
  queue.enqueue(lint, "/p", "C#", ["a.cs"]);
  queue.enqueue(lint, "/p", "bad-names", ["a/b", "", ".."]);
  queue.enqueue(lint, "/p", "mixed", ["ok.ts", "a‍b", "ok.ts", "C#"]);
  await advance(150);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].directory, "mixed");
  assert.deepEqual(calls[0].names, ["ok.ts", "C#"]);
});

test("file-decorations: rejected requests do not suspend the provider", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  for (let i = 0; i < 5; i += 1) {
    queue.enqueue(lint, "/p", `link${i}`, ["a.ts"]);
    await advance(150);
    await settle(calls.at(-1), "rejected");
  }
  // Two real failures after five rejections: still below the gate.
  for (let i = 0; i < 2; i += 1) {
    queue.enqueue(lint, "/p", `f${i}`, ["a.ts"]);
    await advance(150);
    await settle(calls.at(-1), "failed");
  }
  queue.enqueue(lint, "/p", "next", ["a.ts"]);
  await advance(150);
  assert.equal(calls.length, 8);
  assert.equal(calls.at(-1).directory, "next");
});

test("file-decorations: cancelAll stops requests and keeps the cache", async () => {
  const { queue, calls, advance, settle } = harness();
  const lint = provider("lint.plugin");
  queue.enqueue(lint, "/p", "kept", ["a.ts"]);
  await advance(150);
  await settle(calls[0], decorated(["a.ts"]));
  queue.enqueue(lint, "/p", "a", ["a.ts"]);
  queue.enqueue(lint, "/p", "b", ["a.ts"]);
  await advance(150);
  queue.enqueue(lint, "/p", "c", ["a.ts"]);
  queue.cancelAll();
  assert.equal(calls[1].signal.aborted, true);
  await advance(1000);
  assert.equal(calls.length, 2);
  assert.deepEqual(queue.lookup("/p", "kept/a.ts"), { badge: "E", color: "danger", pluginName: "lint.plugin" });
});

test("file-decorations: a re-expanded loaded folder is asked again", () => {
  const data = (path, extra = {}) => ({ projectRoot: "/p", path, name: path.split("/").pop(), isDir: true, ...extra });
  const node = {
    loaded: true,
    data: data("src"),
    childNodes: [{ data: data("src/lib") }, { data: { ...data("src/a.ts"), isDir: false } }],
  };
  assert.deepEqual(loadedDirectoryOf(node), { projectRoot: "/p", directory: "src", names: ["lib", "a.ts"] });
  // First expansion: Element Plus has not loaded the children yet; loadTree reports them.
  assert.equal(loadedDirectoryOf({ ...node, loaded: false }), undefined);
  assert.equal(loadedDirectoryOf({ ...node, data: { ...data("src/a.ts"), isDir: false } }), undefined);
  // The project root is the "" directory; worktree children are projects, not names.
  assert.deepEqual(
    loadedDirectoryOf({
      loaded: true,
      data: data("", { isProjectRoot: true, name: "app" }),
      childNodes: [{ data: data("", { isProjectRoot: true, worktree: {}, name: "wt" }) }, { data: data("README.md") }],
    }),
    { projectRoot: "/p", directory: "", names: ["README.md"] },
  );
  // Through the queue, the same folder is requested once per expansion.
  return (async () => {
    const { queue, calls, advance, settle } = harness();
    const lint = provider("lint.plugin");
    const entry = loadedDirectoryOf(node);
    queue.enqueue(lint, entry.projectRoot, entry.directory, entry.names);
    await advance(150);
    await settle(calls[0], new Map());
    queue.cancelDirectory("/p", "src");
    queue.enqueue(lint, entry.projectRoot, entry.directory, entry.names);
    await advance(150);
    assert.equal(calls.length, 2);
  })();
});

test("file-decorations: refresh keeps the most recently expanded folders", () => {
  const entries = ["a", "b", "c", "d"].map((directory) => ({ projectRoot: "/p", directory, names: ["x"] }));
  // Tree order a b c d, expanded in the order c a d (b unknown: oldest).
  const recent = ["/p\u0000c", "/p\u0000a", "/p\u0000d"];
  assert.deepEqual(orderByRecency(entries, recent).map((entry) => entry.directory), ["b", "c", "a", "d"]);
});

test("file-decorations: colour classes use the token map only", async () => {
  const css = await readFile(new URL("../src/styles/workspaces/project/plugin-file-decorations.css", import.meta.url), "utf8");
  for (const [color, token] of Object.entries(DECORATION_COLOR_TOKENS)) {
    const rule = new RegExp(`\\.project-tree-node__decoration\\.is-${color}\\s*\\{\\s*color:\\s*${token.replace(/[()]/g, "\\$&")};\\s*\\}`);
    assert.match(css, rule, color);
  }
  assert.doesNotMatch(css, /#[0-9a-f]{3,8}\b|rgb|hsl/i);
});
