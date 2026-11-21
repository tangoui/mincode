import assert from "node:assert/strict";
import test from "node:test";

import { describePluginInstallError } from "../src/domain/plugins/install-error.ts";
import {
  openCloudAccount,
  openPluginSettings,
  requestOpenSettings,
  settingsNavigationRequest,
} from "../src/services/settings-navigation.ts";

// Exact messages from `cloudcompanion.DownloadMarketArtifact`, wrapped the way
// `market.installPreviewHandler` and the host surfaces them to the renderer.
const NO_SESSION = "请先在设置中登录云账号，再从插件市场安装";
const NON_MEMBER = "当前会话不是云端会员账号，请重新登录";
const SESSION_EXPIRED = "云账号会话已失效，请重新登录";
const PAID_HOST = "该插件需要 MinCode 主程序付费套餐（年度或永久），请先在 设置 → 云账号 购买或激活";

test("市场安装缺少会员会话时归类为需要登录", () => {
  for (const raw of [
    NO_SESSION,
    `market install: ${NO_SESSION}`,
    `RuntimeError: market install: ${NO_SESSION}`,
    `market install: ${NON_MEMBER}`,
    `market install: ${SESSION_EXPIRED}`,
  ]) {
    const info = describePluginInstallError(raw);
    assert.equal(info.kind, "signInRequired", raw);
  }
});

test("需要登录的分类保留后端原始文案作为技术详情", () => {
  assert.equal(describePluginInstallError(`market install: ${NO_SESSION}`).detail, NO_SESSION);
  assert.equal(describePluginInstallError(`Error: market install: ${NON_MEMBER}`).detail, NON_MEMBER);
  assert.equal(describePluginInstallError(`market install: ${NO_SESSION}`).platform, undefined);
});

test("需要付费套餐的拒绝不会被误判为需要登录", () => {
  // The paid-host refusal also mentions 设置 → 云账号, so the sign-in rule must stay specific.
  const info = describePluginInstallError(`market install: ${PAID_HOST}`);
  assert.equal(info.kind, "paidHostRequired");
  assert.equal(info.detail, PAID_HOST);
});

test("其它市场安装错误分类保持不变", () => {
  assert.equal(describePluginInstallError("market install: invalid plugin ID").kind, "unknown");
  assert.equal(describePluginInstallError("market install: no published release yet").kind, "noRelease");
  assert.equal(describePluginInstallError("market install: release list HTTP status 503").kind, "network");
  assert.equal(
    describePluginInstallError("market install: plugin limit reached: the free plan allows 3 enabled plugins").kind,
    "pluginQuota",
  );
  assert.equal(
    describePluginInstallError("market install: invalid release list response").kind,
    "marketData",
  );
});

test("未登录跳转请求指向设置页的云账号分类", () => {
  const before = settingsNavigationRequest.value;
  requestOpenSettings("cloud");
  const after = settingsNavigationRequest.value;
  assert.equal(after.category, "cloud");
  assert.equal(after.pluginId, undefined);
  assert.equal(after.seq, before.seq + 1);
});

test("openCloudAccount 与 openPluginSettings 各自携带明确分类", () => {
  openPluginSettings("demo-plugin");
  assert.equal(settingsNavigationRequest.value.category, "plugins");
  assert.equal(settingsNavigationRequest.value.pluginId, "demo-plugin");

  const pluginsSeq = settingsNavigationRequest.value.seq;
  openCloudAccount();
  assert.equal(settingsNavigationRequest.value.category, "cloud");
  assert.equal(settingsNavigationRequest.value.pluginId, undefined);
  // Every request must advance the sequence, otherwise App.vue skips a repeated open.
  assert.equal(settingsNavigationRequest.value.seq, pluginsSeq + 1);
});
