import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";
import { createPinia, setActivePinia } from "pinia";

// Since 9ec99569 the license store no longer verifies standalone keys: Pro follows the
// cloud member entitlement held by the quota store, activation throws
// LegacyLicenseRetiredError, and remove() only deletes a record left by the retired flow.
// The quota store (and the desktop bridge it pulls in) is replaced by a controllable stub.
const serviceStub = `
  const service = () => globalThis.__licenseTestService;
  export const activateLicense = (...args) => service().activateLicense(...args);
  export const licenseEntitlementFromVerification = (result) => result;
  export const loadLicensePricing = () => service().loadLicensePricing();
  export const loadLicenseRecord = () => service().loadLicenseRecord();
  export const removeLicenseRecord = () => service().removeLicenseRecord();
  export const saveLicenseRecord = (record) => service().saveLicenseRecord(record);
  export const verifyLicense = (key) => service().verifyLicense(key);
`;
const quotaStoreStub = `
  import { ref } from ${JSON.stringify(import.meta.resolve("vue"))};
  import { defineStore } from ${JSON.stringify(import.meta.resolve("pinia"))};
  export const useQuotaStore = defineStore("quota", () => {
    const effective = ref(globalThis.__licenseTestQuota.initial ?? null);
    async function refresh() {
      globalThis.__licenseTestQuota.refreshes += 1;
      const next = await globalThis.__licenseTestQuota.next?.();
      if (next !== undefined) effective.value = next;
    }
    const admit = () => ({ allowed: true });
    return { effective, refresh, admit };
  });
`;
const srcRoot = fileURLToPath(new URL("../src/", import.meta.url));
const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/license-service") {
      return { url: dataModule(serviceStub), shortCircuit: true };
    }
    if (specifier === "@/stores/quota") {
      return { url: dataModule(quotaStoreStub), shortCircuit: true };
    }
    if (specifier === "@/services/cloud-companion-service" || specifier === "@/services/desktop-bridge") {
      // Only types are imported from these at runtime-free positions; fail loudly if that changes.
      return { url: dataModule(`throw new Error("unexpected runtime import of ${specifier}");`), shortCircuit: true };
    }
    if (specifier.startsWith("@/domain/")) {
      const relativePath = specifier.slice("@/".length);
      return { url: pathToFileURL(`${srcRoot}/${relativePath}.ts`).href, shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});
const { LegacyLicenseRetiredError, licenseRecordFromEntitlement, useLicenseStore } = await import(
  "../src/stores/license.ts"
);

const NOW = 1_800_000_000_000;
const seconds = (ms) => Math.floor(ms / 1000);

function entitlement(overrides = {}) {
  return {
    source: "verified",
    plan: "annual",
    accountId: 7,
    expiresAt: seconds(NOW + 60_000),
    checkedAt: seconds(NOW - 1_000),
    limits: { max_devices: 3 },
    ...overrides,
  };
}

function setup(initial = null) {
  const pinia = createPinia();
  setActivePinia(pinia);
  const calls = { removes: 0, legacy: [] };
  globalThis.__licenseTestQuota = { initial, refreshes: 0, next: undefined };
  const legacy = (name) => (...args) => {
    calls.legacy.push({ name, args });
    return Promise.reject(new Error(`retired license-service.${name} must not be called`));
  };
  globalThis.__licenseTestService = {
    activateLicense: legacy("activateLicense"),
    loadLicensePricing: legacy("loadLicensePricing"),
    loadLicenseRecord: legacy("loadLicenseRecord"),
    saveLicenseRecord: legacy("saveLicenseRecord"),
    verifyLicense: legacy("verifyLicense"),
    removeLicenseRecord: async () => { calls.removes += 1; },
  };
  const store = useLicenseStore();
  return { pinia, store, calls, quota: globalThis.__licenseTestQuota };
}

function teardown(pinia) {
  // Runs the store's onScopeDispose so no expiration timer outlives the test.
  pinia._e.stop();
}

test("entitlement mapping only grants Pro for a verified/offline, unexpired paid plan", () => {
  assert.equal(licenseRecordFromEntitlement(null, NOW).status, "free");
  assert.equal(licenseRecordFromEntitlement(entitlement({ source: "free" }), NOW).status, "free");
  assert.equal(licenseRecordFromEntitlement(entitlement({ source: "none" }), NOW).status, "free");
  assert.equal(licenseRecordFromEntitlement(entitlement({ plan: "free" }), NOW).status, "free");
  // An annual plan without an expiry is not trusted as Pro.
  assert.equal(licenseRecordFromEntitlement(entitlement({ expiresAt: null }), NOW).status, "free");

  const annual = licenseRecordFromEntitlement(entitlement(), NOW);
  assert.equal(annual.plan, "personal_pro");
  assert.equal(annual.status, "active");
  assert.equal(annual.licenseType, "annual");
  assert.equal(annual.lifetime, false);
  assert.equal(annual.expiresAt, seconds(NOW + 60_000) * 1000);
  assert.equal(annual.lastVerifiedAt, seconds(NOW - 1_000) * 1000);
  assert.equal(annual.maxDevices, 3);
  assert.equal(annual.unlimitedDevices, false);
  assert.equal(annual.licenseKey, undefined, "cloud entitlement never carries a standalone key");

  assert.equal(licenseRecordFromEntitlement(entitlement({ source: "offline" }), NOW).status, "active");
  assert.equal(
    licenseRecordFromEntitlement(entitlement({ expiresAt: seconds(NOW - 1_000) }), NOW).status,
    "expired",
  );

  const perpetual = licenseRecordFromEntitlement(
    entitlement({ plan: "perpetual", expiresAt: null, limits: { max_devices: 0 } }),
    NOW,
  );
  assert.equal(perpetual.status, "active");
  assert.equal(perpetual.lifetime, true);
  assert.equal(perpetual.licenseType, "lifetime");
  assert.equal(perpetual.unlimitedDevices, true);
});

test("the store derives authorization from the cloud quota only after hydration", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: NOW });
  const { pinia, store, calls, quota } = setup(entitlement({ plan: "perpetual", expiresAt: null }));
  try {
    assert.equal(store.record.status, "active");
    assert.equal(store.isAuthorized, false, "not authorized before hydrate");
    await store.hydrate();
    assert.equal(store.isAuthorized, true);
    assert.equal(store.hasFeature("anything"), true);
    assert.equal(quota.refreshes, 0, "hydrate must not add a startup entitlement request");
    assert.deepEqual(calls.legacy, [], "hydrate must not read or verify a legacy license record");

    quota.next = async () => ({ source: "free", plan: "free" });
    assert.equal(await store.refreshVerification(), false);
    assert.equal(quota.refreshes, 1);
    assert.equal(store.record.status, "free");
    assert.equal(store.hasFeature("anything"), false);
  } finally {
    teardown(pinia);
  }
});

test("standalone activation is retired and never touches the license service", async () => {
  const { pinia, store, calls } = setup();
  try {
    await store.hydrate();
    await assert.rejects(store.activate("user@example.test", "KEY"), LegacyLicenseRetiredError);
    assert.deepEqual(calls.legacy, []);
    assert.equal(store.record.status, "free");
    assert.equal(store.isAuthorized, false);

    await store.refreshPricing();
    assert.deepEqual(store.pricing, { plans: [] });
    assert.deepEqual(calls.legacy, [], "the standalone pricing catalog is retired");
  } finally {
    teardown(pinia);
  }
});

test("removing the legacy record neither revokes nor restores the cloud entitlement", async () => {
  const { pinia, store, calls, quota } = setup();
  try {
    await store.hydrate();
    let release;
    const gate = new Promise((resolve) => { release = resolve; });
    globalThis.__licenseTestService.removeLicenseRecord = async () => {
      calls.removes += 1;
      await gate;
    };
    const removal = store.remove();
    assert.equal(store.busy, true);
    release();
    await removal;
    assert.equal(store.busy, false);
    assert.equal(calls.removes, 1);
    assert.equal(store.record.status, "free", "a retired record cannot restore Pro");

    quota.next = async () => entitlement({ plan: "perpetual", expiresAt: null });
    await store.refreshVerification();
    await store.remove();
    assert.equal(calls.removes, 2);
    assert.equal(store.isAuthorized, true, "removal does not affect the account entitlement");
    assert.deepEqual(calls.legacy, []);
  } finally {
    teardown(pinia);
  }
});

test("removal clears busy even when deleting the legacy record fails", async () => {
  const { pinia, store } = setup();
  try {
    globalThis.__licenseTestService.removeLicenseRecord = async () => { throw new Error("disk"); };
    await assert.rejects(store.remove(), /disk/);
    assert.equal(store.busy, false);
  } finally {
    teardown(pinia);
  }
});

test("an annual grant turns off exactly when it expires without a refresh", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: NOW });
  const { pinia, store, quota } = setup(entitlement({ expiresAt: seconds(NOW + 60_000) }));
  try {
    await store.hydrate();
    assert.equal(store.isAuthorized, true);
    t.mock.timers.tick(59_000);
    assert.equal(store.isAuthorized, true);
    t.mock.timers.tick(1_100);
    assert.equal(store.record.status, "expired");
    assert.equal(store.isAuthorized, false);
    assert.equal(quota.refreshes, 0, "expiry is evaluated locally");
  } finally {
    teardown(pinia);
  }
});

test("a quota change reschedules expiration and disposal clears the timer", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: NOW });
  const { pinia, store, quota } = setup();
  await store.hydrate();
  assert.equal(store.isAuthorized, false);

  quota.next = async () => entitlement({ expiresAt: seconds(NOW + 10_000) });
  await store.refreshVerification();
  assert.equal(store.isAuthorized, true);
  t.mock.timers.tick(10_100);
  assert.equal(store.isAuthorized, false, "the rescheduled timer fires at the new expiry");

  quota.next = async () => entitlement({ expiresAt: seconds(NOW + 60_000) });
  await store.refreshVerification();
  assert.equal(store.isAuthorized, true);
  teardown(pinia);
  // After disposal no timer may still flip the (now detached) state.
  t.mock.timers.tick(120_000);
  assert.equal(store.record.status, "active");
});
