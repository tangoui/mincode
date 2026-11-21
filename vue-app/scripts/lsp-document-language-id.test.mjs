import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { lspDocumentLanguageId } from "../src/domain/projects/lsp.ts";

test("Terraform files open with the language IDs terraform-ls serves", () => {
  assert.equal(lspDocumentLanguageId("hcl", "/p/main.tf"), "terraform");
  assert.equal(lspDocumentLanguageId("hcl", "/p/MAIN.TF"), "terraform");
  assert.equal(lspDocumentLanguageId("hcl", "/p/prod.tfvars"), "terraform-vars");
  assert.equal(lspDocumentLanguageId("hcl", "/p/packer.pkr.hcl"), "hcl");
});

test("other documents keep the editor language ID", () => {
  for (const [language, path] of [["go", "/p/a.go"], ["sql", "/p/a.sql"], ["shell", "/p/a.sh"], ["typescript", "/p/a.tf.ts"]]) {
    assert.equal(lspDocumentLanguageId(language, path), language);
  }
});

test("didOpen sends the mapped language ID", async () => {
  const runtime = await readFile(new URL("../src/composables/project-language-server-runtime.ts", import.meta.url), "utf8");
  const didOpen = runtime.slice(runtime.indexOf("function didOpen("), runtime.indexOf("function didOpen(") + 400);
  assert.match(didOpen, /languageId: lspDocumentLanguageId\(model\.getLanguageId\(\), model\.uri\.path\)/);
});
