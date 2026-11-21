import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const sidebarTemplate = await readFile(
  new URL("../src/components/database/sidebar/DatabaseConnectionSidebar.vue", import.meta.url),
  "utf8"
);
const sidebarStyles = await readFile(
  new URL("../src/styles/workspaces/database/interactions.css", import.meta.url),
  "utf8"
);
const chevronComponent = await readFile(
  new URL("../src/components/ui/AppChevron.vue", import.meta.url),
  "utf8"
);
const databaseTypeIcon = await readFile(
  new URL("../src/components/database/DatabaseTypeIcon.vue", import.meta.url),
  "utf8"
);
const databaseWorkspace = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8"
);
const databaseWorkspaceStyles = await readFile(
  new URL("../src/styles/workspaces/database/redis-workspace.css", import.meta.url),
  "utf8"
);

test("SQL 脚本列表使用独立内容容器，避免 Element Plus 按钮包装层破坏布局", () => {
  assert.match(sidebarTemplate, /class="db-side-list-item__content"/);
  assert.match(
    sidebarStyles,
    /\.db-side-list-item__content,\s*\.db-saved-connection\s*>\s*button:first-child\s*>\s*span\s*\{[^}]*display:\s*flex;[^}]*min-width:\s*0;[^}]*flex:\s*1;[^}]*flex-direction:\s*column;/s
  );
});

test("SQL 脚本按钮包装层保持横向布局并允许内容收缩", () => {
  assert.match(
    sidebarStyles,
    /\.db-side-list-item\s*>\s*\.el-button:first-child\s*>\s*span\s*\{[^}]*display:\s*flex;[^}]*width:\s*100%;[^}]*min-width:\s*0;[^}]*align-items:\s*center;/s
  );
});

test("共享箭头使用中线对齐，不向下偏移", () => {
  assert.match(
    chevronComponent,
    /\.app-chevron\s*\{[^}]*vertical-align:\s*middle;/s
  );
});

// Check the icon's contract independently of attribute order/line wrapping.
// A plugin-driver binding is additive: it must not hide the dialect fallback.
function assertConnectionTypeIcon(template) {
  const icon = template.match(/<DatabaseTypeIcon\b[^>]*\/>/)?.[0] ?? "";
  assert.match(icon, /\s:db-type="connection\.dbType"/);
  assert.match(icon, /\ssize="small"/);
}

test("连接树展示数据库类型图标，避免仅凭自定义名称识别数据源", () => {
  assertConnectionTypeIcon(sidebarTemplate);
});

test("数据库侧栏连接行与 SSH 一样使用 26px 头像：类型图标放在品牌色浅底头像里，连接状态画在头像角标上", async () => {
  const navigatorCss = await readFile(
    new URL("../src/styles/workspaces/database/navigator.css", import.meta.url),
    "utf8"
  );
  const avatar = sidebarTemplate.match(/<ConnectionAvatar[\s\S]*?<\/ConnectionAvatar>/)?.[0] ?? "";
  assert.match(avatar, /class="db-connection-node__avatar"/);
  assert.match(avatar, /:name="connection\.name"/);
  assert.match(avatar, /:seed="connection\.id"/);
  assert.match(avatar, /:size="26"/);
  assert.match(avatar, /:status="connectionAvatarStatus\(connection\)"/);
  assert.match(avatar, /:style="connectionBrandStyle\(connection\)"/);
  assertConnectionTypeIcon(avatar);
  assert.match(sidebarTemplate, /function connectionAvatarStatus\([\s\S]*?"online"[\s\S]*?"pending"[\s\S]*?"offline"/);
  assert.match(sidebarTemplate, /databaseConnectionBrand\(connection\)\.color/);
  assert.match(
    navigatorCss,
    /\.db-connection-node__avatar\.connection-avatar\s*\{[^}]*background:\s*color-mix\(in srgb, var\(--db-connection-brand, var\(--app-accent\)\) 14%, var\(--app-surface\)\)/
  );
  // 旧状态点保留在 DOM 中，但两个侧栏都不再画出来。
  assert.match(sidebarTemplate, /class="db-connection-node__status"/);
  assert.match(
    navigatorCss,
    /\.db-connection-navigator \.db-connection-node__status\s*\{[^}]*position:\s*absolute;[^}]*clip-path:\s*inset\(50%\)/
  );
});

test("数据库侧栏工具栏：标题 + 全部操作默认平铺，导入连接与导出配置仅在侧栏过窄时收进「更多」", () => {
  const toolbar = sidebarTemplate.match(/<div class="db-explorer__toolbar">[\s\S]*?\n      <\/div>/)?.[0] ?? "";
  assert.match(toolbar, /<strong class="db-explorer__title">\{\{ t\("database\.sidebarTitle"\) \}\}<\/strong>/);
  const more = toolbar.match(/<el-dropdown class="db-explorer__more"[\s\S]*?<\/el-dropdown>/)?.[0] ?? "";
  const inline = toolbar.replace(more, "");
  assert.match(inline, /@click="emit\('import-config'\)"/);
  assert.match(inline, /class="db-square-action db-explorer__secondary"[^>]*@click="emit\('import-connections'\)"/);
  assert.match(inline, /class="db-square-action db-explorer__secondary"[^>]*@click="emit\('export-config'\)"/);
  assert.match(inline, /:disabled="!databaseStore\.desktopRuntime \|\| !activeConnection"[^>]*@click="emit\('refresh-schema'\)"/);
  assert.match(inline, /class="db-square-action is-primary"[^>]*@click="emit\('new-connection'\)"/);
  assert.match(more, /emit\('import-connections'\)/);
  assert.match(more, /emit\('export-config'\)/);
  assert.doesNotMatch(toolbar, /db-explorer__toolbar-separator/);
});

test("数据库侧栏连接行第二行显示「类型 · 地址」，底部显示连接数统计", () => {
  const meta = sidebarTemplate.match(/<span class="db-connection-node__meta"[\s\S]*?<\/em\s*>/)?.[0] ?? "";
  assert.match(meta, /<em\b[\s\S]*?'is-driver-unavailable': driverUnavailable\(connection\)[\s\S]*?connectionTypeLabel\(connection\)[\s\S]*?connectionEndpoint\(connection, t\("database\.overviewManagedService"\)\)[\s\S]*?<\/em\s*>/);
  assert.doesNotMatch(sidebarTemplate, /connection\.tags/);
  assert.match(sidebarTemplate, /import \{ connectionEndpoint \} from "@\/domain\/database\/endpoint"/);
  const footers = sidebarTemplate.match(/<footer class="db-explorer__footer">/g) ?? [];
  assert.equal(footers.length, 1);
  const footer = sidebarTemplate.match(/<footer class="db-explorer__footer">[\s\S]*?<\/footer>/)?.[0] ?? "";
  assert.match(footer, /<span class="db-explorer__footer-summary">/);
  assert.match(footer, /database\.sidebarFooterSummary/);
  assert.doesNotMatch(footer, /<el-button/);
});

test("概览与侧栏共用 connectionEndpoint", async () => {
  const overview = await readFile(
    new URL("../src/components/database/DatabaseConnectionOverview.vue", import.meta.url),
    "utf8"
  );
  assert.match(overview, /connectionEndpoint\(connection, t\("database\.overviewManagedService"\)\)/);
  assert.doesNotMatch(overview, /connection\.filePath\) return/);
});

test("侧栏底部只定义一次，摘要文字可省略号截断", async () => {
  const [navigatorCss, workspaceCss] = await Promise.all([
    readFile(new URL("../src/styles/workspaces/database/navigator.css", import.meta.url), "utf8"),
    readFile(new URL("../src/styles/workspaces/database/workspace.css", import.meta.url), "utf8"),
  ]);
  assert.doesNotMatch(workspaceCss, /\.db-explorer__footer/);
  assert.equal((navigatorCss.match(/(?:^|\n)\.db-explorer__footer\s*\{/g) ?? []).length, 1);
  assert.match(
    navigatorCss,
    /\.db-explorer__footer-summary\s*\{[^}]*display:\s*block;[^}]*overflow:\s*hidden;[^}]*text-overflow:\s*ellipsis;[^}]*white-space:\s*nowrap;/s
  );
});

test("数据库类型图标使用轻量图形，不渲染字母色块和阴影", () => {
  assert.match(databaseTypeIcon, /<svg[^>]*viewBox="0 0 24 24"/);
  assert.doesNotMatch(databaseTypeIcon, /brand\.mark/);
  assert.doesNotMatch(databaseTypeIcon, /box-shadow:/);
  assert.doesNotMatch(databaseTypeIcon, /background:/);
});

test("数据库侧栏启用分栏边界刷新，展开后恢复保存宽度", () => {
  const splitPane = databaseWorkspace.match(/<AppSplitPane[\s\S]*?>/)?.[0] ?? "";
  assert.match(splitPane, /:collapsed="explorerCollapsed"/);
  assert.match(splitPane, /:collapsed-size="0"/);
  assert.match(splitPane, /refresh-panel-on-collapse/);
});

test("SQL 结果分割面板提供纵向 flex 高度链路供 AutoResizer 测量", () => {
  assert.match(
    databaseWorkspaceStyles,
    /\.db-query-split\s*>\s*\.el-splitter\s*>\s*\.el-splitter-panel\s*\{[^}]*display:\s*flex;[^}]*min-height:\s*0;[^}]*flex-direction:\s*column;[^}]*overflow:\s*hidden;/s
  );
});
