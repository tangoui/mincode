import assert from "node:assert/strict";
import test from "node:test";
import { describePluginInstallError, parsePluginQuotaError } from "../src/domain/plugins/install-error.ts";

test("classifies a missing release and strips error prefixes", () => {
  const info = describePluginInstallError("RuntimeError: market install: this plugin has no published release yet");
  assert.equal(info.kind, "noRelease");
  assert.equal(info.detail, "this plugin has no published release yet");
});

test("captures the platform of a missing package", () => {
  const info = describePluginInstallError("Error: market install: no published package for darwin/arm64");
  assert.equal(info.kind, "noPlatformPackage");
  assert.equal(info.platform, "macOS ARM64");
});

test("classifies network, integrity, compatibility and market data errors", () => {
  assert.equal(describePluginInstallError("market install: release list request failed: dial tcp: timeout").kind, "network");
  assert.equal(describePluginInstallError("market install: package does not match the published size and SHA-256").kind, "integrity");
  assert.equal(describePluginInstallError("market install: plugin cannot be installed here").kind, "incompatible");
  assert.equal(describePluginInstallError("market install: invalid release list").kind, "marketData");
});

test("classifies a paid host plan requirement from the desktop or the cloud", () => {
  assert.equal(describePluginInstallError("market install: this plugin requires a paid MinCode plan (annual or perpetual)").kind, "paidHostRequired");
  assert.equal(describePluginInstallError("该插件需要 MinCode 主程序付费套餐（年度或永久）").kind, "paidHostRequired");
});

test("classifies the backend's enabled-plugin quota refusal", () => {
  assert.equal(describePluginInstallError("plugin limit reached: the free plan allows 6 enabled plugins (6 enabled); disable another plugin or upgrade in Settings → Cloud account").kind, "pluginQuota");
  assert.equal(describePluginInstallError("Error: plugins are not available on the free plan; upgrade in Settings → Cloud account").kind, "pluginQuota");
});

test("extracts the plan and limit from the quota refusal for a localized message", () => {
  assert.deepEqual(
    parsePluginQuotaError("RuntimeError: plugin limit reached: the free plan allows 6 enabled plugins (6 enabled); disable another plugin or upgrade in Settings → Cloud account"),
    { plan: "free", limit: 6 },
  );
  assert.deepEqual(parsePluginQuotaError("plugins are not available on the annual plan; upgrade in Settings → Cloud account"), { plan: "annual", limit: 0 });
  assert.equal(parsePluginQuotaError("something else"), undefined);
});

test("classifies the cloud sign-in refusal and preserves its detail", () => {
  const info = describePluginInstallError("RuntimeError: 请先在设置中登录云账号");
  assert.equal(info.kind, "signInRequired");
  assert.equal(info.detail, "请先在设置中登录云账号");
});

test("keeps unknown messages as-is", () => {
  const info = describePluginInstallError("RuntimeError: cannot read plugin archive");
  assert.equal(info.kind, "unknown");
  assert.equal(info.detail, "cannot read plugin archive");
});
