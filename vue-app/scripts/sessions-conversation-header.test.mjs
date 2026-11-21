import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const [view, styles] = await Promise.all([
  readFile(new URL("../src/views/SessionsWorkspaceView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/sessions/workspace.css", import.meta.url), "utf8"),
]);

test("path and meta share one subline instead of stacking two grey rows", () => {
  const header = view.match(
    /<div class="sessions-conversation__identity">(?<body>[\s\S]*?)<div class="sessions-conversation__actions">/,
  )?.groups?.body;

  // 路径原来是独立的 <p>，标题下面就有两行小字，头部被撑成三行。
  assert.ok(header, "conversation header markup moved");
  assert.doesNotMatch(header, /<p v-if="selectedPath"/);
  assert.match(header, /<div class="sessions-conversation__subline">/);
  assert.match(header, /class="sessions-conversation__path"/);

  const subline = styles.match(
    /\.sessions-conversation__subline\s*\{(?<body>[\s\S]*?)\n\}/,
  )?.groups?.body;
  assert.match(subline ?? "", /display: flex;/);
});

test("only the path truncates so model, count and time stay readable", () => {
  const path = styles.match(
    /\.sessions-conversation__path\s*\{(?<body>[\s\S]*?)\n\}/,
  )?.groups?.body;
  const meta = styles.match(
    /\}\n\.sessions-conversation__meta\s*\{(?<body>[\s\S]*?)\n\}/,
  )?.groups?.body;

  assert.match(path ?? "", /flex: 0 1 auto;/);
  assert.match(meta ?? "", /flex: none;/);
  // 两段合成一行后需要一个分隔符，否则路径和模型会粘在一起。
  assert.match(styles, /\.sessions-conversation__path \+ \.sessions-conversation__meta::before/);
});
