import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const pagination = await readFile(
  new URL("../src/composables/database/useTablePagination.ts", import.meta.url),
  "utf8"
);
const workspace = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8"
);
const objectBrowser = await readFile(
  new URL("../src/components/database/DatabaseObjectBrowser.vue", import.meta.url),
  "utf8"
);

test("records the page of every table load per tab", () => {
  // store 的 page 是工作区全局单值，被下一张表覆盖后就找不回来了。
  assert.match(pagination, /const tablePagePositions = ref<Record<string, number>>\(\{\}\);/);

  const openTablePage = pagination.match(/async function openTablePage[\s\S]*?\n  }\n/)?.[0];
  assert.ok(openTablePage, "openTablePage implementation must be present");
  // 非游标与游标两条返回路径都要留档，否则向量库切回时仍回到第一页。
  assert.equal(openTablePage.match(/rememberTablePage\(tab\.id, page\)/g)?.length, 2);

  const loadSearchPage = pagination.match(/async function loadTableSearchPage[\s\S]*?\n  }\n/)?.[0];
  assert.ok(loadSearchPage, "loadTableSearchPage implementation must be present");
  assert.match(loadSearchPage, /rememberTablePage\(tab\.id, page\)/);
});

test("restores the remembered page instead of resetting to the first one", () => {
  const activate = workspace.match(/async function activateTableTab[\s\S]*?\n}\n/)?.[0];
  assert.ok(activate, "activateTableTab implementation must be present");

  assert.match(activate, /const restoredPage = tablePagePositions\.value\[tab\.id\] \?\? 1;/);
  assert.match(activate, /openTablePage\(tab, search \? 1 : restoredPage, databaseStore\.pageSize\)/);
  assert.match(activate, /loadTableSearchPage\(tab, search, restoredPage, databaseStore\.pageSize\)/);
  // databaseStore.page 属于上一个激活的标签，拿它还原会串页。
  assert.doesNotMatch(activate, /databaseStore\.page\b/);
});

test("drops the remembered page along with the tab's other state", () => {
  const cleanups = workspace.match(
    /removeTableServerSearch\(tabId\);\s*\n\s*removeTableGridState\(tabId\);\s*\n\s*forgetTablePage\(tabId\);/g
  );
  assert.equal(cleanups?.length, 2, "close-tab and disconnect must both forget the page");
});

test("closing a connection tab also collapses its sidebar branch after disconnect", () => {
  const removeTab = workspace.match(/function removeDatabaseConnectionTab[\s\S]*?\n}\n/)?.[0];
  const disconnect = workspace.match(/async function disconnectConnection[\s\S]*?\n}\n/)?.[0];
  assert.match(removeTab ?? "", /void disconnectConnection\(connection\);/);
  assert.match(disconnect ?? "", /await databaseStore\.disconnect\(connection\.id\);\s*connectionSidebar\.value\?\.setConnectionExpanded\(connection, false\);/);
});

test("keeps the object browser search alive while it is unmounted", () => {
  // 概览面板挂在 v-if="activeObject === 'database'" 下，切到表标签会被卸载。
  assert.match(objectBrowser, /defineModel<string>\("searchText", \{ required: true \}\)/);
  assert.doesNotMatch(objectBrowser, /const searchText = ref\(""\);/);
  assert.match(objectBrowser, /const debouncedSearchText = ref\(searchText\.value\);/);

  assert.match(workspace, /const overviewSearchText = ref\(""\);/);
  assert.match(workspace, /v-model:search-text="overviewSearchText"/);
  // 换库时对象列表整体换掉，旧关键字不该继续过滤。
  const openOverview = workspace.match(/function openDatabaseOverview\([\s\S]*?\n}\n/)?.[0];
  assert.ok(openOverview, "openDatabaseOverview implementation must be present");
  assert.match(openOverview, /overviewSearchText\.value = "";/);
});

test("search/filter pages use the clamped page size for both LIMIT and OFFSET", () => {
  const loadSearchPage = pagination.match(/async function loadTableSearchPage[\s\S]*?\n  }\n/)?.[0];
  assert.ok(loadSearchPage, "loadTableSearchPage implementation must be present");
  // 构造器把 LIMIT 夹到 5000；OFFSET 若按 10000 计算会跳过 5000 行。
  assert.match(loadSearchPage, /const pageSize = tableQueryPageSize\(Math\.max\(10, requestedPageSize\)\);/);
  assert.match(loadSearchPage, /const offset = \(page - 1\) \* pageSize;/);
  assert.match(loadSearchPage, /databaseStore\.pageSize = pageSize;/);
});

test("cached last pages are dropped whenever the tab's row count may change", () => {
  const forget = pagination.match(/function forgetTableLastPages[\s\S]*?\n  }\n/)?.[0];
  assert.ok(forget, "forgetTableLastPages implementation must be present");
  assert.match(forget, /const prefix = `\$\{tabId\}\|`;/);

  for (const name of ["forgetTablePage", "searchAllTableRows", "applyTableFilters", "clearTableServerSearch", "refreshTablePage"]) {
    const body = pagination.match(new RegExp(`function ${name}[\\s\\S]*?\\n  }\\n`))?.[0];
    assert.ok(body, `${name} implementation must be present`);
    assert.match(body, /forgetTableLastPages\(tab(?:\.id|Id)\)/, `${name} must invalidate the last-page cache`);
  }

  const rowMutations = workspace.match(/onApplied: \(\) => forgetActiveTableLastPages\(\),/);
  assert.ok(rowMutations, "submitted grid changes must invalidate the last-page cache");
  const importData = workspace.match(/async function importData[\s\S]*?\n}\n/)?.[0];
  assert.match(importData ?? "", /databaseStore\.importData\([\s\S]*?forgetActiveTableLastPages\(\);/);
});

test("refresh reloads the current table page instead of the no-op selectTable", () => {
  const refreshGrid = workspace.match(/function refreshActiveGrid\(\)[\s\S]*?\n}\n/)?.[0];
  assert.match(refreshGrid ?? "", /refreshSelectedTable\(\);/);
  const refreshSelected = workspace.match(/function refreshSelectedTable\(\)[\s\S]*?\n}\n/)?.[0];
  assert.match(refreshSelected ?? "", /void refreshTablePage\(\);/);
  assert.match(workspace, /@click="refreshSelectedTable"/);
  const refreshPage = pagination.match(/async function refreshTablePage[\s\S]*?\n  }\n/)?.[0];
  assert.match(refreshPage ?? "", /const page = tablePagePositions\.value\[tab\.id\] \?\? 1;/);
});
