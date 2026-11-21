import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("the commit message scrolls inside an Element Plus scrollbar", async () => {
  const panel = await readFile(new URL("../src/components/projects/ProjectGitPanel.vue", import.meta.url), "utf8");
  const css = await readFile(new URL("../src/styles/workspaces/project/git-panel.css", import.meta.url), "utf8");
  assert.match(panel, /<el-scrollbar class="git-commit-message-scroll" max-height="132px">\s*<el-input\s+v-model="commitMessage"\s+type="textarea"\s+:autosize="\{ minRows: 4 \}"/);
  assert.match(css, /\.git-commit-box--original \.el-textarea__inner \{[^}]*overflow: hidden !important;[^}]*border: 0 !important;/);
  assert.match(css, /\.git-commit-message-scroll:focus-within \{/);
});

test("the commit collapse header overrides Element Plus minimum height", async () => {
  const css = await readFile(new URL("../src/styles/workspaces/project/git-panel.css", import.meta.url), "utf8");
  const header = css.match(/\.project-git-commit-collapse \.el-collapse-item__header \{([^}]*)\}/)?.[1];
  assert.ok(header);
  assert.match(header, /\bheight:\s*30px;/);
  assert.match(header, /\bmin-height:\s*30px;/);
});
