// Element Plus has its own built-in texts (MessageBox OK / Cancel, pagination,
// empty tables, date pickers). Without a locale from the app they are always
// English, even in the Chinese interface.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { elementPlusLocale } from "../src/services/element-plus-locale.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");

test("the Element Plus language follows the interface language", async () => {
  const zh = await elementPlusLocale("zh");
  const en = await elementPlusLocale("en");
  assert.equal(zh.name, "zh-cn");
  assert.equal(zh.el.messagebox.confirm, "确定");
  assert.equal(en.name, "en");
  assert.equal(en.el.messagebox.confirm, "OK");
});

test("app startup gives Element Plus the current language", async () => {
  const main = await readSource("../src/main.ts");
  assert.match(main, /installElementPlusLocale\(/, "main.ts installs the Element Plus locale");
  const mounts = main.split("\n").filter((line) => line.includes(".mount("));
  assert.equal(mounts.length, 1);
  for (const mount of mounts) assert.match(mount, /installElementPlusLocale\(createApp\(/, `${mount.trim()} gets the locale`);
});
