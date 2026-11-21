import type { DatabaseRowFieldKind } from "@/domain/database/row-field";

/**
 * 生成值：右键菜单"生成值"使用的确定性注册表。
 * 每个生成器声明适用的字段类型，UI 按当前列类型过滤展示。
 * id 保持字面量联合，保证 i18n 模板 key 的类型安全。
 */
export type DatabaseValueGeneratorId =
  | "uuid"
  | "nowDatetime"
  | "today"
  | "nowTime"
  | "randomInt"
  | "randomFloat"
  | "randomBoolean"
  | "randomString"
  | "email"
  | "username"
  | "chineseName"
  | "phone";

export interface DatabaseValueGenerator {
  id: DatabaseValueGeneratorId;
  kinds: DatabaseRowFieldKind[];
  generate: () => string | number | boolean;
}

function pad(value: number) {
  return String(value).padStart(2, "0");
}

function timestamp() {
  const now = new Date();
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())} ${pad(now.getHours())}:${pad(now.getMinutes())}:${pad(now.getSeconds())}`;
}

function today() {
  return timestamp().slice(0, 10);
}

function randomToken(length: number) {
  const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789";
  let value = "";
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  for (const byte of bytes) value += alphabet[byte % alphabet.length];
  return value;
}

function randomName() {
  const family = ["王", "李", "张", "刘", "陈", "杨", "赵", "黄", "周", "吴"];
  const given = ["伟", "芳", "娜", "敏", "静", "磊", "军", "洋", "勇", "杰", "婷", "雪"];
  const pick = (pool: string[]) => pool[Math.floor(Math.random() * pool.length)];
  return `${pick(family)}${pick(given)}${Math.random() > 0.5 ? pick(given) : ""}`;
}

const TEXTUAL: DatabaseRowFieldKind[] = ["text", "textarea", "json"];

export const DATABASE_VALUE_GENERATORS: DatabaseValueGenerator[] = [
  { id: "uuid", kinds: TEXTUAL, generate: () => crypto.randomUUID() },
  { id: "nowDatetime", kinds: ["datetime", ...TEXTUAL], generate: timestamp },
  { id: "today", kinds: ["date", "datetime", ...TEXTUAL], generate: today },
  { id: "nowTime", kinds: ["time"], generate: () => timestamp().slice(11) },
  { id: "randomInt", kinds: ["number"], generate: () => Math.floor(Math.random() * 100_000) + 1 },
  {
    id: "randomFloat",
    kinds: ["number"],
    generate: () => Math.round((Math.random() * 10_000 + Number.EPSILON) * 100) / 100,
  },
  { id: "randomBoolean", kinds: ["boolean"], generate: () => Math.random() > 0.5 },
  { id: "randomString", kinds: TEXTUAL, generate: () => randomToken(12) },
  { id: "email", kinds: TEXTUAL, generate: () => `user_${randomToken(8)}@example.com` },
  { id: "username", kinds: TEXTUAL, generate: () => `user_${randomToken(6)}` },
  { id: "chineseName", kinds: TEXTUAL, generate: randomName },
  {
    id: "phone",
    kinds: TEXTUAL,
    generate: () => `1${["3", "5", "7", "8", "9"][Math.floor(Math.random() * 5)]}${String(Math.floor(Math.random() * 1_000_000_000)).padStart(9, "0")}`,
  },
];

export function databaseValueGeneratorsForKind(
  kind: DatabaseRowFieldKind,
): DatabaseValueGenerator[] {
  return DATABASE_VALUE_GENERATORS.filter((generator) => generator.kinds.includes(kind));
}
