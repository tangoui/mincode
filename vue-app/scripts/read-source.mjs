import { readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";

// 源码契约只比较内容，不让 Git checkout 的换行格式改变提取结果。
export async function readSource(path, encoding = "utf8") {
  return (await readFile(path, encoding)).replaceAll("\r\n", "\n");
}

export function readSourceSync(path, encoding = "utf8") {
  return readFileSync(path, encoding).replaceAll("\r\n", "\n");
}
