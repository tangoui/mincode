// Monaco 浮层必须有实底背景，否则水印和代码会直接透上来。
//
// 三个独立的成因，各锁一条：
//  1. StandaloneThemeService 只把 --vscode-* 变量写在
//     `.monaco-editor, .monaco-diff-editor, .monaco-component` 上 → 宿主必须自带
//     monaco-component 类。
//  2. suggest/hover 等组件样式使用 `.monaco-editor .组件` 后代选择器 → 宿主还必须
//     自带 monaco-editor 类，否则背景、文字和布局规则整套失配。
//  3. editor.main.css 里 `.context-view.monaco-menu-container` 只有 border:none /
//     overflow，背景由调用方补（VS Code 走 workbench 样式）→ 独立集成要自己给。
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const overflowHostSource = await readFile(
  new URL("../src/services/monaco-overflow-host.ts", import.meta.url),
  "utf8",
);
const monacoOverflowCss = await readFile(
  new URL("../src/styles/shared/monaco-overflow.css", import.meta.url),
  "utf8",
);

function ruleBody(css, selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  // 选择器前可能是 `}`、注释结尾或行首；统一按“行内独占选择器”匹配。
  const match = css.match(new RegExp(`(?:^|[}\\n])\\s*${escaped}\\s*\\{([^}]*)\\}`, "s"));
  return match?.[1];
}

test("overflow host carries a Monaco theme scope so --vscode-* variables resolve", () => {
  const className = overflowHostSource.match(/className\s*=\s*"([^"]+)"/)?.[1];
  assert.ok(className, "overflow host must set a className");
  const classes = className.split(/\s+/);
  assert.ok(
    classes.includes("monaco-overflow-widgets-host"),
    "overflow host keeps its own positioning class",
  );
  assert.ok(
    classes.includes("monaco-component"),
    "overflow host needs monaco-component, else --vscode-* variables are undefined and overlays render transparent",
  );
  assert.ok(
    classes.includes("monaco-editor"),
    "overflow host needs monaco-editor so Monaco component selectors style suggest/hover widgets",
  );
});

test("hover overlays declare an opaque background with a non-variable fallback", () => {
  const body = ruleBody(monacoOverflowCss, ".monaco-overflow-widgets-host .monaco-hover");
  assert.ok(body, "hover overlays inside the host must be styled");

  const background = body.match(/background-color:\s*([^;]+)/)?.[1];
  assert.ok(background, "hover overlay must set background-color");
  assert.match(
    background,
    /var\(--vscode-editorHoverWidget-background,\s*[^)]+\)/,
    "hover background must fall back to an app color when the vscode variable is missing",
  );
  assert.doesNotMatch(background, /transparent|none/, "hover background must stay opaque");
});

test("context menu container declares an opaque background", () => {
  const body = ruleBody(monacoOverflowCss, ".context-view.monaco-menu-container");
  assert.ok(
    body,
    "context menu container must be styled — editor.main.css ships no background for it",
  );

  const background = body.match(/background-color:\s*([^;]+)/)?.[1];
  assert.ok(background, "context menu must set background-color");
  assert.doesNotMatch(background, /transparent|none/, "context menu background must stay opaque");
});
