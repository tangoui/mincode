import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const tabBarSource = await readFile(
  new URL("../src/components/layout/AppTabBar.vue", import.meta.url),
  "utf8"
);
const foundationSource = await readFile(
  new URL("../src/styles/foundation/foundation.css", import.meta.url),
  "utf8"
);

test("top workspace tab context menu copies the displayed label through the desktop bridge", () => {
  assert.match(
    tabBarSource,
    /import \{ copyText, invokeBackend, isDesktopRuntime \} from "@\/services\/desktop-bridge";/
  );
  assert.match(
    tabBarSource,
    /async function copyTabName[\s\S]*?await copyText\(tabTitle\(tab\)\);/
  );
  assert.match(tabBarSource, /<el-dropdown-item command="copy"/);
  assert.match(tabBarSource, /value === "copy"\) void copyTabName\(tabId\)/);
});

test("opening one tab context menu closes the other tab context menus", () => {
  assert.match(tabBarSource, /:ref="\(menu: unknown\) => setTabMenuRef\(tab\.id, menu\)"\s+trigger="contextmenu"/);
  assert.match(
    tabBarSource,
    /function handleTabMenuVisible[\s\S]*?for \(const \[id, menu\] of tabMenuRefs\) if \(id !== tabId\) menu\.handleClose\(\);/
  );
});

test("top workspace tabs use Element Plus card tabs with closable panes", () => {
  assert.match(tabBarSource, /import AppWorkspaceTabs from "@\/components\/ui\/AppWorkspaceTabs\.vue";/);
  assert.match(tabBarSource, /<AppWorkspaceTabs\s+class="app-top-tabs__workspace-tabs"/);
  assert.match(tabBarSource, /<el-tab-pane\s+v-for="tab in visibleTabs"/);
  assert.match(
    tabBarSource,
    /:closable="isWorkspaceTabClosable\(tab\) && renamingTabId !== tab\.id"/
  );
  assert.match(tabBarSource, /@tab-remove="removeWorkspaceTab"/);
  assert.doesNotMatch(tabBarSource, /app-workspace-tab-shell|app-workspace-tab__close/);
});

test("clicking a top tab re-selects through tab-click so the active route is restored", () => {
  assert.match(tabBarSource, /@tab-click="handleWorkspaceTabClick"/);
  assert.match(
    tabBarSource,
    /function handleWorkspaceTabClick[\s\S]*?selectTab\(String\(pane\.paneName\)\)/
  );
});

test("inline rename keystrokes never reach Element Plus tab shortcuts", () => {
  assert.match(
    tabBarSource,
    /class="app-workspace-tab__rename"[\s\S]*?@keydown\.stop[\s\S]*?:data-tab-rename-input="tab\.id"/
  );
});

test("top tab close icon is always visible with a fixed width and no transitions", () => {
  assert.match(
    foundationSource,
    /\.app-top-tabs__workspace-tabs\.el-tabs\s*\{[^}]*--app-workspace-tabs-close-size:\s*16px;/s
  );
  assert.match(
    foundationSource,
    /\.app-top-tabs__workspace-tabs\.el-tabs--card > \.el-tabs__header \.el-tabs__item \.is-icon-close\s*\{[^}]*transition:\s*none;/s
  );
});

test("top tabs keep fixed padding and opt out of the desktop window drag region", () => {
  const itemRule = foundationSource.match(
    /\.app-top-tabs \.app-top-tabs__workspace-tabs\.el-tabs--card\.el-tabs--top > \.el-tabs__header \.el-tabs__item\s*\{([^}]*)\}/
  );
  assert.ok(itemRule);
  assert.match(itemRule[1], /padding:\s*0 6px 0 0;/);
  assert.match(itemRule[1], /transition:\s*none;/);
  assert.match(itemRule[1], /--tango-drag:\s*no-drag;/);
});

test("active top tabs keep their accent background against late workspace styles", () => {
  assert.match(
    foundationSource,
    /\.el-tabs__item\.is-active\s*\{[^}]*background:\s*var\(--app-accent-soft\);/s
  );
});

test("top workspace tabs stay equal-width and use spare space before overflowing", () => {
  assert.match(tabBarSource, /const TOP_TAB_WIDTH_PX = 120;/);
  assert.match(tabBarSource, /const TOP_TAB_MIN_WIDTH_PX = 88;/);
  assert.match(tabBarSource, /:style="\{ width: `\$\{workspaceTabLabelWidth\(tab\)\}px` \}"/);
  assert.match(
    tabBarSource,
    /function workspaceTabLabelWidth[\s\S]*?Math\.floor\(workspaceTabDisplayWidth\(tab\)\) - TOP_TAB_ITEM_CHROME_PX - closeWidth/
  );
  assert.match(foundationSource, /\.app-workspace-tab\s*\{[^}]*width:\s*100%;/s);
  assert.match(
    tabBarSource,
    /function workspaceTabWidth[\s\S]*?if \(!tab\.pinned\) return TOP_TAB_WIDTH_PX;/
  );
  assert.match(
    tabBarSource,
    /function workspaceTabFitWidth[\s\S]*?if \(!tab\.pinned\) return TOP_TAB_MIN_WIDTH_PX;/
  );
  assert.match(
    tabBarSource,
    /function workspaceTabDisplayWidth[\s\S]*?availableForRegularTabs \/ regularTabs\.length/
  );
});
