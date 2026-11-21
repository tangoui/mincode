import test from "node:test";
import assert from "node:assert/strict";
import { parseSmokeConfig } from "../src/domain/e2e/desktop-smoke.ts";

const base = { scenarios: ["terminal"], nonce: "abc123", timeoutMs: 60000 };
const mailClient = {
  packagePath: "/tmp/mail-package",
  outputDir: "/tmp/mail-export",
  attachmentPath: "/tmp/attachment.txt",
  controlUrl: "http://127.0.0.1:12345",
  expectedSubject: "Desktop inbox abc123",
};

test("mail-client desktop smoke config requires an explicit scenario and a loopback fixture", () => {
  assert.equal(parseSmokeConfig({ ...base, mailClient }).mailClient, undefined);
  const requested = { ...base, scenarios: ["mail-client"] };
  assert.throws(() => parseSmokeConfig(requested), /mailClient/);
  const parsed = parseSmokeConfig({ ...requested, mailClient });
  assert.deepEqual(parsed.scenarios, ["mail-client"]);
  assert.deepEqual(parsed.mailClient, mailClient);
  assert.throws(() => parseSmokeConfig({ ...requested, mailClient: { ...mailClient, controlUrl: "https://public.example" } }), /loopback/);
});
