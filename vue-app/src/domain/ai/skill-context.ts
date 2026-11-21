import type { StoredAiSkill } from "@/domain/ai/settings-integrations";

export type StoredAiSkillValue = StoredAiSkill | string;

const GENERIC_TERMS = new Set([
  "ai",
  "skill",
  "skills",
  "使用",
  "代码",
  "工具",
  "开发",
  "用户",
  "系统",
  "助手",
  "支持",
]);

function normalizeText(value: string) {
  return value.trim().toLowerCase();
}

function normalizeSkill(skill: StoredAiSkillValue, index: number): StoredAiSkill {
  return typeof skill === "string"
    ? {
        id: `legacy-${index}`,
        name: skill,
        content: skill,
        description: "",
        enabled: true,
      }
    : skill;
}

function enabledSkills(skills: StoredAiSkillValue[]) {
  return skills
    .map(normalizeSkill)
    .filter(
      (skill) =>
        skill?.enabled !== false &&
        typeof skill?.name === "string" &&
        Boolean(skill.name.trim()) &&
        typeof skill.content === "string" &&
        Boolean(skill.content.trim()),
    );
}

function explicitSkill(skills: StoredAiSkill[], prompt: string) {
  const match = /^\s*\/skill\s+([\s\S]+)$/iu.exec(prompt);
  if (!match) return undefined;
  const invocation = normalizeText(match[1]!);
  return [...skills]
    .sort((left, right) => right.name.length - left.name.length)
    .find((skill) => {
      const aliases = [skill.name, skill.id].map(normalizeText).filter(Boolean);
      return aliases.some(
        (alias) => invocation === alias || (invocation.startsWith(alias) && /\s/u.test(invocation.charAt(alias.length))),
      );
    });
}

function metadataTerms(skill: StoredAiSkill) {
  const metadata = normalizeText(`${skill.name} ${skill.description ?? ""}`);
  const terms = new Set<string>();
  for (const match of metadata.matchAll(/[a-z0-9+#.-]{3,}|[\p{Script=Han}]{2,}/gu)) {
    const value = match[0];
    if (/^[\p{Script=Han}]+$/u.test(value)) {
      for (let size = 2; size <= Math.min(4, value.length); size++) {
        for (let index = 0; index <= value.length - size; index++) {
          terms.add(value.slice(index, index + size));
        }
      }
    } else {
      terms.add(value);
    }
  }
  return [...terms].filter((term) => !GENERIC_TERMS.has(term));
}

function skillMatchScore(skill: StoredAiSkill, prompt: string) {
  const normalizedPrompt = normalizeText(prompt);
  const normalizedName = normalizeText(skill.name);
  if (normalizedName && normalizedPrompt.includes(normalizedName)) return 100;

  const matches = metadataTerms(skill).filter((term) => normalizedPrompt.includes(term));
  if (matches.some((term) => term.length >= 4)) return 40;
  if (matches.some((term) => term.length === 3)) return 30;
  return new Set(matches.filter((term) => term.length === 2)).size >= 2 ? 20 : 0;
}

/** Selects only the skills relevant to this turn, avoiding full skill injection on every request. */
export function selectAiSkillsForPrompt(skills: StoredAiSkillValue[], prompt: string) {
  const available = enabledSkills(skills);
  const explicit = explicitSkill(available, prompt);
  if (explicit) return [explicit];
  if (/^\s*\/skill(?:\s|$)/iu.test(prompt)) return [];

  return available
    .map((skill) => ({ skill, score: skillMatchScore(skill, prompt) }))
    .filter(({ score }) => score > 0)
    .sort((left, right) => right.score - left.score)
    .slice(0, 1)
    .map(({ skill }) => skill);
}

/** Context for the user skills selected this turn. */
export function buildSelectedAiSkillsContext(
  skills: StoredAiSkillValue[],
  prompt: string,
) {
  const selected = selectAiSkillsForPrompt(skills, prompt);
  if (!selected.length) return "";
  return `## Enabled skills\nApply the following skill for this request.\n\n${selected
    .map((skill) => `### ${skill.name}\n${skill.content.trim()}`)
    .join("\n\n")}`;
}
