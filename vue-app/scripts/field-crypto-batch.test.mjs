import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const calls = [];
globalThis.__fieldCryptoMocks = { calls, failChunk: false };

const source = (await readFile(new URL("../src/services/field-crypto.ts", import.meta.url), "utf8"))
  .replace(
    'import { invokeBackend } from "@/services/desktop-bridge";',
    `const invokeBackend = async (command, args) => {
       globalThis.__fieldCryptoMocks.calls.push([command, args]);
       if (globalThis.__fieldCryptoMocks.failChunk) throw new Error("bridge down");
       if (command === "encrypt_fields") return args.plains.map((plain) => plain ? "enc:v2:" + plain : plain);
       return args.ciphers.map((cipher) =>
         cipher === "enc:v2:broken" ? { value: "", error: "damaged" } : { value: cipher.replace("enc:v2:", "") }
       );
     };`
  );
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText;
const fieldCrypto = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);

function reset() {
  calls.length = 0;
  globalThis.__fieldCryptoMocks.failChunk = false;
}

test("field decryption in the same tick uses one IPC and isolates damaged values", async () => {
  reset();
  const results = await Promise.allSettled([
    fieldCrypto.decryptField("enc:v2:alpha"),
    fieldCrypto.decryptField("enc:v2:broken"),
    fieldCrypto.decryptField("enc:v2:beta"),
  ]);

  assert.deepEqual(calls, [["decrypt_fields", { ciphers: ["enc:v2:alpha", "enc:v2:broken", "enc:v2:beta"] }]]);
  assert.deepEqual(results[0], { status: "fulfilled", value: "alpha" });
  assert.equal(results[1].status, "rejected");
  assert.match(results[1].reason.message, /damaged/);
  assert.deepEqual(results[2], { status: "fulfilled", value: "beta" });
});

test("field encryption in the same tick uses one IPC", async () => {
  reset();
  const results = await Promise.all([
    fieldCrypto.encryptField("one"),
    fieldCrypto.encryptField("two"),
  ]);
  assert.deepEqual(calls, [["encrypt_fields", { plains: ["one", "two"] }]]);
  assert.deepEqual(results, ["enc:v2:one", "enc:v2:two"]);
});

test("bridge failures reject every queued field", async () => {
  reset();
  globalThis.__fieldCryptoMocks.failChunk = true;
  const results = await Promise.allSettled([
    fieldCrypto.decryptField("enc:v2:a"),
    fieldCrypto.decryptField("enc:v2:b"),
  ]);
  assert.equal(calls.length, 1);
  assert.deepEqual(results.map((result) => result.status), ["rejected", "rejected"]);
});
