import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

import {
  clampTerminalDimensions,
  fitTerminalSize,
  isMeasuredViewport,
} from "../src/services/terminal-viewport.ts";

const hostUrls = {
  local: new URL("../src/components/terminal/LocalTerminal.vue", import.meta.url),
  ssh: new URL("../src/components/terminal/SshTerminal.vue", import.meta.url),
  docker: new URL("../src/components/ssh/SshDockerTerminal.vue", import.meta.url),
};

const [localSource, sshSource, dockerSource] = await Promise.all([
  readFile(hostUrls.local, "utf8"),
  readFile(hostUrls.ssh, "utf8"),
  readFile(hostUrls.docker, "utf8"),
]);

test("a container without width or height is not a measured viewport", () => {
  // 首帧 flex-basis 为 0 的分栏面板、展开动画中的抽屉、v-show 隐藏的窗格。
  assert.equal(isMeasuredViewport({ width: 0, height: 480 }), false);
  assert.equal(isMeasuredViewport({ width: 640, height: 0 }), false);
  assert.equal(isMeasuredViewport({ width: 0, height: 0 }), false);
  assert.equal(isMeasuredViewport({ width: Number.NaN, height: 480 }), false);
  assert.equal(isMeasuredViewport({ width: 640, height: Number.NaN }), false);
  assert.equal(isMeasuredViewport({ width: Infinity, height: 480 }), false);
  assert.equal(isMeasuredViewport({ width: -640, height: 480 }), false);
  assert.equal(isMeasuredViewport(null), false);
  assert.equal(isMeasuredViewport(undefined), false);
});

test("a laid-out container is a measured viewport", () => {
  assert.equal(isMeasuredViewport({ width: 1, height: 1 }), true);
  assert.equal(isMeasuredViewport({ width: 1199, height: 799 }), true);
  assert.equal(isMeasuredViewport({ width: 0.5, height: 0.5 }), true);
});

test("clamping leaves real measurements alone and only floors absent ones", () => {
  assert.deepEqual(clampTerminalDimensions(120, 32), { cols: 120, rows: 32 });
  assert.deepEqual(clampTerminalDimensions(80, 24), { cols: 80, rows: 24 });
  assert.deepEqual(clampTerminalDimensions(9999, 9999), { cols: 512, rows: 256 });
  // 完全没有测量值时退回 xterm 的常规默认，而不是下限。
  assert.deepEqual(clampTerminalDimensions(0, 0), { cols: 120, rows: 32 });
});

// 这条是本次修复的核心：FitAddon 在 0 宽容器上返回它自己的下限 2 列，clamp 会把它
// 抬成 20 —— 一个看起来完全合法、实则编造的宽度。20 列的 PTY 会把 CLI 输出硬换行
// 写进 scrollback，之后再多的 SIGWINCH 也回流不了。所以 20 必须永远够不到。
test("the clamp floor is a last resort, never a stand-in for a failed measurement", () => {
  assert.deepEqual(clampTerminalDimensions(2, 5), { cols: 20, rows: 5 });
  for (const [source, name] of [
    [localSource, "LocalTerminal"],
    [sshSource, "SshTerminal"],
    [dockerSource, "SshDockerTerminal"],
  ]) {
    assert.doesNotMatch(
      source,
      /Math\.max\(20,/,
      `${name} 不应再自带一份 clamp 副本`,
    );
  }
});

// clientWidth 含 padding。.terminal-pane-host 的 padding: 6px 7px 让一个 0 宽面板
// 报出 clientWidth === 14 —— 大于 0，却仍然一个字符都放不下。FitAddon 读的是
// getComputedStyle().width（始终是 content 宽度），守卫必须用同一个基准。
test("the layout guard measures the content box, not the padding-inclusive one", async () => {
  const source = await readFile(
    new URL("../src/services/terminal-viewport.ts", import.meta.url),
    "utf8",
  );
  assert.match(source, /getComputedStyle\(host\)/);
  assert.doesNotMatch(source, /host\.client(Width|Height)/);
});

test("every xterm host refuses to fit an unmeasured container", () => {
  for (const [source, name] of [
    [localSource, "LocalTerminal"],
    [sshSource, "SshTerminal"],
    [dockerSource, "SshDockerTerminal"],
  ]) {
    assert.match(
      source,
      /if \(!hasTerminalLayout\(terminalHost\.value\)\) return(?: null)?;[\s\S]{0,400}?fitTerminalSize\(xterm, fitAddon\)/,
      `${name} 的 fit 之前缺少布局守卫`,
    );
    // FitAddon.fit() shrinks the screen to whatever a sliver of a container holds (down
    // to 2 columns) while the PTY gets the clamped 20: the shell then redraws for a
    // width the screen does not have and leaves fragments in the scrollback.
    assert.doesNotMatch(source, /fitAddon\.fit\(\)/, `${name} 不能直接调用 fitAddon.fit()`);
  }
  // onResize 是退化尺寸漏向后端的另一条路径。
  assert.match(
    dockerSource,
    /onResize\(\(\{ cols, rows \}\) => \{[\s\S]{0,200}?if \(!hasTerminalLayout\(terminalHost\.value\)\) return;/,
    "SshDockerTerminal 的 onResize 回调缺少布局守卫",
  );
});

test("local and SSH panes sync PTY size through one deduplicated, frame-coalesced path", () => {
  for (const [source, name] of [
    [localSource, "LocalTerminal"],
    [sshSource, "SshTerminal"],
  ]) {
    assert.doesNotMatch(source, /xterm\.onResize\(/, `${name} 不应再从 onResize 重复发送尺寸`);
    assert.equal(source.match(/"resize_local_shell"/g)?.length, 1, `${name} 只能有一处尺寸发送`);
    assert.match(source, /new ResizeObserver\(\(\) => resizeSync\.schedule\(\)\)/);
    assert.match(source, /resizeSync\.reset\(\);\s*backendReady = true;/, `${name} 连接后必须重新发送初始尺寸`);
  }
});

test("every xterm host waits for a real layout before creating the PTY", () => {
  // 顺序是关键：await 布局 → fit → 启动会话。任何一处颠倒都会让后端拿到假尺寸。
  assert.match(
    localSource,
    /await waitForTerminalLayout\(terminalHost\.value\);[\s\S]{0,200}?fitAndResize\(\);[\s\S]{0,400}?"start_local_shell"/,
  );
  assert.match(
    sshSource,
    /await waitForTerminalLayout\(terminalHost\.value\);[\s\S]{0,200}?fitAndResize\(\);[\s\S]{0,400}?"start_ssh_shell"/,
  );
  assert.match(
    dockerSource,
    /await waitForTerminalLayout\(terminalHost\.value\);[\s\S]{0,200}?fitTerminal\(\);[\s\S]{0,200}?await startStream\(\)/,
  );
});

test("the local shell is sized before its initial command is typed", () => {
  // resume 命令一旦在 20 列下执行，输出的硬换行就永久留在 scrollback 里。
  const waitIndex = localSource.indexOf("await waitForTerminalLayout");
  const commandIndex = localSource.indexOf("props.initialCommand");
  assert.ok(waitIndex > 0 && commandIndex > 0);
  assert.ok(
    waitIndex < commandIndex,
    "必须先等到真实布局，再写入 initialCommand",
  );
});

function fakeTerminal(cols = 80, rows = 24) {
  const resizes = [];
  const refreshes = [];
  return {
    cols,
    rows,
    resizes,
    refreshes,
    refresh(start, end) {
      refreshes.push([start, end]);
    },
    resize(nextCols, nextRows) {
      resizes.push([nextCols, nextRows]);
      this.cols = nextCols;
      this.rows = nextRows;
    },
  };
}
const proposing = (dimensions) => ({ proposeDimensions: () => dimensions });

test("a sliver of a container leaves the screen and the PTY alone", () => {
  // FitAddon proposes 2 columns for a container narrower than two cells.
  for (const proposed of [{ cols: 2, rows: 30 }, { cols: 19, rows: 30 }, { cols: 120, rows: 4 }, undefined, { cols: Number.NaN, rows: 30 }]) {
    const terminal = fakeTerminal(160, 30);
    assert.equal(fitTerminalSize(terminal, proposing(proposed)), null, JSON.stringify(proposed));
    assert.deepEqual(terminal.resizes, []);
    assert.deepEqual(terminal.refreshes, []);
    assert.equal(terminal.cols, 160);
  }
});

test("the screen and the PTY always get the same size", () => {
  const terminal = fakeTerminal(80, 24);
  assert.deepEqual(fitTerminalSize(terminal, proposing({ cols: 120, rows: 40 })), { cols: 120, rows: 40 });
  assert.deepEqual(terminal.resizes, [[120, 40]]);
  // Above the PTY maximum the screen is clamped too, instead of staying wider than the shell.
  assert.deepEqual(fitTerminalSize(terminal, proposing({ cols: 900, rows: 300 })), { cols: 512, rows: 256 });
  assert.deepEqual([terminal.cols, terminal.rows], [512, 256]);
  // An unchanged size does not resize again.
  const before = terminal.resizes.length;
  assert.deepEqual(fitTerminalSize(terminal, proposing({ cols: 512, rows: 256 })), { cols: 512, rows: 256 });
  assert.equal(terminal.resizes.length, before);
  assert.deepEqual(terminal.refreshes, [[0, 255]]);
});

test("showing an unchanged grid repaints stale glyphs without resizing the shell", () => {
  const terminal = fakeTerminal(80, 24);
  assert.deepEqual(fitTerminalSize(terminal, proposing({ cols: 80, rows: 24 })), { cols: 80, rows: 24 });
  assert.deepEqual(terminal.resizes, []);
  assert.deepEqual(terminal.refreshes, [[0, 23]]);
});
