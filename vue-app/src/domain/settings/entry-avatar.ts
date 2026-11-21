// 设置页条目卡片头像：首字母 + 按名称哈希分配的语义色调，视觉模式与技能市场卡片一致。
const ENTRY_AVATAR_TONES = ["accent", "success", "warning", "danger"] as const;
export type EntryAvatarTone = (typeof ENTRY_AVATAR_TONES)[number];

export function entryAvatarInitial(name: string) {
  return (name.trim()[0] ?? "?").toUpperCase();
}

export function entryAvatarTone(name: string): EntryAvatarTone {
  let hash = 0;
  for (const char of name) hash = (hash * 31 + (char.codePointAt(0) ?? 0)) >>> 0;
  return ENTRY_AVATAR_TONES[hash % ENTRY_AVATAR_TONES.length]!;
}
