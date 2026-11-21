import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import DOMPurify from "dompurify";

// Node has no DOM: record what reaches DOMPurify and the config it gets.
let sanitizeCalls = [];
DOMPurify.sanitize = (html, config) => {
  sanitizeCalls.push({ html, config });
  return html;
};

const {
  WALKTHROUGH_DESCRIPTION_MAX_RUNES,
  WALKTHROUGH_IMAGE_ALT_MAX_RUNES,
  WALKTHROUGH_LOCALES_MAX,
  WALKTHROUGH_MAX_ENTRIES,
  WALKTHROUGH_PROGRESS_MAX_RECORDS,
  WALKTHROUGH_STEPS_MAX,
  WALKTHROUGH_TEXT_MAX_RUNES,
  WALKTHROUGH_TITLE_MAX_RUNES,
  emptyWalkthroughProgress,
  featuredWalkthroughs,
  isWalkthroughComplete,
  markWalkthroughStepDone,
  normalizeWalkthroughProgress,
  parseWalkthroughEntry,
  resolveWalkthroughs,
  setWalkthroughDismissed,
  walkthroughMarkdownKey,
  walkthroughPreviewSummary,
  walkthroughTitle,
} = await import("../src/domain/plugins/walkthroughs.ts");
const { renderPluginMarkdownHtml } = await import("../src/services/plugin-markdown.ts");

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");
const KEY_A = "a".repeat(64);
const KEY_B = "b".repeat(64);
const KEY_C = "c".repeat(64);

function contribution(pluginId, entries, overrides = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "declarative",
    activeInWorkspace: true,
    point: { entries, issues: [] },
    ...overrides,
  };
}

function entry(id, overrides = {}) {
  return {
    id,
    title: `Title ${id}`,
    steps: [
      { id: "one", title: "One", markdown: KEY_A, markdownLocalized: { zh: KEY_B }, image: KEY_C, imageMime: "image/svg+xml", imageAlt: "Diagram" },
      { id: "two", title: "Two", text: "Plain\ntext" },
    ],
    ...overrides,
  };
}

test("常量与贡献点契约同步", async () => {
  for (const path of ["../../plugins/api/contribution-points.json", "../src/domain/plugins/contribution-points.json"]) {
    const contract = JSON.parse(await readSource(path)).points.walkthroughs;
    assert.equal(contract.status, "enabled");
    assert.deepEqual(contract.kinds, ["declarative", "process"]);
    assert.equal(contract.maxEntries, WALKTHROUGH_MAX_ENTRIES);
    assert.equal(contract.limits.stepsMax, WALKTHROUGH_STEPS_MAX);
    assert.equal(contract.limits.titleMaxRunes, WALKTHROUGH_TITLE_MAX_RUNES);
    assert.equal(contract.limits.descriptionMaxRunes, WALKTHROUGH_DESCRIPTION_MAX_RUNES);
    assert.equal(contract.limits.localesMax, WALKTHROUGH_LOCALES_MAX);
    assert.equal(contract.limits.textMaxRunes, WALKTHROUGH_TEXT_MAX_RUNES);
    assert.equal(contract.limits.imageAltMaxRunes, WALKTHROUGH_IMAGE_ALT_MAX_RUNES);
    assert.deepEqual(contract.assets.ext, [".md", ".png", ".svg"]);
  }
});

test("条目再校验：资源键、图片类型与命令只允许 process 插件", () => {
  const plugin = { pluginId: "acme", pluginName: "Acme", runtime: "declarative", activeInWorkspace: true };
  const parsed = parseWalkthroughEntry(entry("intro"), plugin);
  assert.equal(parsed.key, "acme/intro");
  assert.equal(parsed.steps.length, 2);
  assert.equal(parsed.steps[1].text, "Plain\ntext");
  const invalid = [
    entry("intro", { steps: [{ id: "one", title: "x", markdown: "walkthroughs/a.md" }] }),
    entry("intro", { steps: [{ id: "one", title: "x", markdown: KEY_A, text: "both" }] }),
    entry("intro", { steps: [{ id: "one", title: "x", text: "t", image: KEY_A, imageMime: "text/html", imageAlt: "a" }] }),
    entry("intro", { steps: [{ id: "one", title: "x", text: "t", image: KEY_A, imageMime: "image/png" }] }),
    entry("intro", { steps: [{ id: "one", title: "x", text: "t" }, { id: "one", title: "y", text: "u" }] }),
    entry("intro", { steps: [] }),
    entry("Bad Id"),
    entry("intro", { steps: [{ id: "one", title: "x", text: "t", command: "acme.open" }] }),
  ];
  for (const value of invalid) assert.equal(parseWalkthroughEntry(value, plugin), undefined);
  const processStep = entry("intro", { steps: [{ id: "one", title: "x", text: "t", command: "acme.open" }] });
  assert.equal(parseWalkthroughEntry(processStep, { ...plugin, runtime: "process" }).steps[0].command, "acme.open");
  const bidi = parseWalkthroughEntry(entry("intro", { title: "a\u202eb" }), plugin);
  assert.equal(bidi.title, "ab");
});

test("resolve 按插件排序、每插件最多 4 个且只保留一个 featured", () => {
  const entries = ["a", "b", "c", "d", "e"].map((id) => entry(id, { featured: true }));
  const resolved = resolveWalkthroughs([contribution("zeta", [entry("x")]), contribution("acme", entries)]);
  assert.deepEqual(resolved.map((item) => item.key), ["acme/a", "acme/b", "acme/c", "acme/d", "zeta/x"]);
  assert.deepEqual(resolved.filter((item) => item.featured).map((item) => item.key), ["acme/a"]);
});

test("按语言链选择 markdown 资源", () => {
  const step = entry("intro").steps[0];
  assert.equal(walkthroughMarkdownKey(step, ["zh-CN", "en"]), KEY_B);
  assert.equal(walkthroughMarkdownKey(step, ["en"]), KEY_A);
  assert.equal(walkthroughTitle({ title: "Get started", titleLocalized: { zh: "入门" } }, ["zh"]), "入门");
});

test("进度：完成步骤、隐藏与 featured 选择", () => {
  const [intro] = resolveWalkthroughs([contribution("acme", [entry("intro", { featured: true })])]);
  let progress = emptyWalkthroughProgress();
  assert.deepEqual(featuredWalkthroughs([intro], progress).map((item) => item.key), ["acme/intro"]);
  progress = markWalkthroughStepDone(progress, intro.key, "one");
  assert.equal(markWalkthroughStepDone(progress, intro.key, "one"), progress);
  assert.equal(isWalkthroughComplete(intro, progress), false);
  progress = markWalkthroughStepDone(progress, intro.key, "two");
  assert.equal(isWalkthroughComplete(intro, progress), true);
  assert.deepEqual(featuredWalkthroughs([intro], progress), []);
  const dismissed = setWalkthroughDismissed(emptyWalkthroughProgress(), intro.key, true);
  assert.deepEqual(featuredWalkthroughs([intro], dismissed), []);
  assert.deepEqual(featuredWalkthroughs([{ ...intro, activeInWorkspace: false }], emptyWalkthroughProgress()), []);
  const many = [1, 2, 3, 4].map((index) => ({ ...intro, key: `p${index}/intro`, pluginId: `p${index}` }));
  assert.equal(featuredWalkthroughs(many, emptyWalkthroughProgress()).length, 3);
});

test("进度归一化丢弃非法数据并限制记录数", () => {
  assert.deepEqual(normalizeWalkthroughProgress({ version: 2 }), emptyWalkthroughProgress());
  const completed = Object.fromEntries(Array.from({ length: 300 }, (_, index) => [`p${index}/intro`, ["one", "Bad Id", 3]]));
  const normalized = normalizeWalkthroughProgress({ version: 1, completed: { ...completed, "bad key": ["one"] }, dismissed: ["x", "p1/intro"] });
  assert.equal(Object.keys(normalized.completed).length + normalized.dismissed.length, WALKTHROUGH_PROGRESS_MAX_RECORDS);
  assert.deepEqual(normalized.completed["p299/intro"], ["one"]);
  assert.deepEqual(normalized.dismissed, ["p1/intro"]);
});

test("插件 markdown：原始 HTML 与图片转义，标题降级，净化配置只允许 http(s) 链接", () => {
  sanitizeCalls = [];
  const html = renderPluginMarkdownHtml("# Title\n\n<script>alert(1)</script>\n\n![x](https://evil/img.png) <b>bold</b> [docs](https://example.com)");
  assert.equal(sanitizeCalls.length, 1);
  assert.match(html, /<h3>Title<\/h3>/);
  assert.doesNotMatch(html, /<script|<img|<b>/);
  assert.match(html, /&lt;script&gt;/);
  assert.match(html, /<a href="https:\/\/example.com">docs<\/a>/);
  const { config } = sanitizeCalls[0];
  assert.deepEqual(config.ALLOWED_ATTR, ["href"]);
  assert.ok(!config.ALLOWED_TAGS.includes("img"));
  assert.ok(config.ALLOWED_URI_REGEXP.test("https://example.com"));
  assert.ok(!config.ALLOWED_URI_REGEXP.test("javascript:alert(1)"));
  assert.ok(!config.ALLOWED_URI_REGEXP.test("file:///etc/passwd"));
});

test("安装预览摘要", () => {
  const summary = walkthroughPreviewSummary({ walkthroughs: [entry("intro", { steps: [{ id: "a", text: "x", command: "acme.open" }] })] });
  assert.deepEqual(summary, { count: 1, titles: ["Title intro"], commandSteps: 1 });
  assert.equal(walkthroughPreviewSummary({}), undefined);
});

test("指南使用可键盘访问的侧栏和独立滚动正文，不挤压横向步骤标题", async () => {
  const dialog = await readSource("../src/components/plugins/PluginWalkthroughDialog.vue");
  assert.doesNotMatch(dialog, /<el-steps\b/);
  assert.match(dialog, /<el-progress\b/);
  assert.match(dialog, /<el-button[\s\S]*?:aria-current="index === stepIndex \? 'step' : undefined"/);
  assert.match(dialog, /class="app-walkthrough__navigation"/);
  assert.match(dialog, /<el-scrollbar[^>]*class="app-walkthrough__reading"/);
});

test("接线：对话框只经净化渲染 markdown，命令经 executePluginCommand，抽屉挂载列表", async () => {
  const dialog = await readSource("../src/components/plugins/PluginWalkthroughDialog.vue");
  const vHtml = dialog.match(/v-html="([^"]+)"/g) ?? [];
  assert.deepEqual(vHtml, ['v-html="markdownHtml"']);
  assert.match(dialog, /markdownHtml\.value = markdown \? renderPluginMarkdownHtml\(/);
  assert.match(dialog, /executePluginCommand\(current\.pluginId, command\)/);
  assert.match(dialog, /contributionAssetObjectURL\(/);
  assert.doesNotMatch(dialog, /onMounted\([^)]*open\.value = true/);
  const manager = await readSource("../src/views/PluginManagerWorkspaceView.vue");
  assert.match(manager, /<PluginWalkthroughList v-if="detail\.plugin" :plugin-id="detail\.plugin\.id" \/>/);
  const index = await readSource("../src/services/contributions/index.ts");
  assert.match(index, /registerWalkthroughContributionConsumer,/);
});
