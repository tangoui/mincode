/**
 * Frontend checks for plugin Monarch grammars (the `languages` contribution
 * point). The Go host already validated the grammar shape (states, rules,
 * actions, guards, include graph, sizes; see
 * backend/plugin/contrib_languages.go); this module mirrors how
 * Monaco's Monarch compiler expands `@attribute` references, compiles every
 * rule and guard regex exactly as Monaco would and runs the static ReDoS
 * heuristics on it. Pure: no Monaco, DOM or Vue.
 */
import { regexSafetyIssue, type RegexProbeCase } from "./regex-safety.ts";

/** Upper bound for a regex after `@attribute` expansion (matches the contract limit). */
export const MONARCH_REGEX_EXPANDED_MAX_LENGTH = 8192;
/** Monaco expands nested attribute references at most this many times. */
const MONARCH_EXPANSION_PASSES = 5;

export type MonarchGrammar = Record<string, unknown> & { tokenizer: Record<string, unknown[]> };

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

/** Mirrors Monaco's compileRegExp attribute expansion (`@name` → `(?:value)`, `@@` → `@`). */
export function expandMonarchRegex(grammar: Record<string, unknown>, source: string): string {
  let text = source.replace(/@@/g, "\u0001");
  for (let pass = 0; pass < MONARCH_EXPANSION_PASSES; pass += 1) {
    let expanded = false;
    text = text.replace(/@(\w+)/g, (_match, name: string) => {
      expanded = true;
      const value = Object.prototype.hasOwnProperty.call(grammar, name) ? grammar[name] : undefined;
      if (typeof value !== "string") {
        throw new Error(value === undefined ? `unknown attribute @${name}` : `attribute @${name} must be a string`);
      }
      return value ? `(?:${value})` : "";
    });
    if (text.length > MONARCH_REGEX_EXPANDED_MAX_LENGTH) {
      throw new Error(`regex exceeds ${MONARCH_REGEX_EXPANDED_MAX_LENGTH} characters after attribute expansion`);
    }
    if (!expanded) break;
  }
  // eslint-disable-next-line no-control-regex -- mirrors Monaco's @@ placeholder
  return text.replace(/\u0001/g, "@");
}

/** Where a regex appears in a grammar, for issue messages. */
export interface MonarchRegex extends RegexProbeCase {
  where: string;
}

function guardPattern(key: string): string | undefined {
  const scrutinee = /^\$(([sS]?)(\d\d?)|#)(.*)$/.exec(key);
  const operatorPattern = scrutinee ? scrutinee[4] : key;
  if (!operatorPattern || /^\w*$/.test(operatorPattern)) return undefined;
  const match = /^(@|!@|~|!~|==|!=)(.*)$/.exec(operatorPattern);
  const operator = match ? match[1] : "~";
  const pattern = match ? match[2] : operatorPattern;
  if (operator !== "~" && operator !== "!~") return undefined;
  if (/^(\w|\|)*$/.test(pattern)) return undefined;
  return pattern;
}

function collectActionRegexes(
  grammar: Record<string, unknown>,
  action: unknown,
  where: string,
  flags: string,
  out: MonarchRegex[],
  depth = 0,
) {
  if (depth > 16) return;
  if (Array.isArray(action)) {
    action.forEach((item, index) => collectActionRegexes(grammar, item, `${where}[${index}]`, flags, out, depth + 1));
    return;
  }
  const cases = objectValue(objectValue(action)?.cases);
  if (!cases) return;
  for (const [key, value] of Object.entries(cases)) {
    const pattern = guardPattern(key);
    if (pattern !== undefined) {
      out.push({ source: expandMonarchRegex(grammar, `^${pattern}$`), flags, where: `${where} case "${key}"` });
    }
    collectActionRegexes(grammar, value, `${where} case "${key}"`, flags, out, depth + 1);
  }
}

/**
 * Every regex Monaco would compile for `grammar` (rule regexes wrapped in
 * `^(?:…)`, `~` guards in `^…$`), with attributes expanded. Throws on an
 * expansion Monaco would reject.
 */
export function collectMonarchRegexes(grammar: MonarchGrammar): MonarchRegex[] {
  const flags = `${grammar.ignoreCase === true ? "i" : ""}${grammar.unicode === true ? "u" : ""}`;
  const out: MonarchRegex[] = [];
  for (const [state, rules] of Object.entries(grammar.tokenizer)) {
    if (!Array.isArray(rules)) continue;
    rules.forEach((rule, index) => {
      const where = `tokenizer.${state}[${index}]`;
      const record = objectValue(rule);
      const regex = Array.isArray(rule) ? rule[0] : record?.regex;
      const action = Array.isArray(rule) ? rule[1] : record?.action;
      if (typeof regex === "string") {
        const body = regex.startsWith("^") ? regex.slice(1) : regex;
        out.push({ source: expandMonarchRegex(grammar, `^(?:${body})`), flags, where });
      }
      collectActionRegexes(grammar, action, where, flags, out);
    });
  }
  return out;
}

/** Regex-valued fields of a language configuration (strings; converted to RegExp for Monaco). */
export function collectConfigurationRegexes(configuration: Record<string, unknown>): MonarchRegex[] {
  const out: MonarchRegex[] = [];
  const add = (source: unknown, where: string) => {
    if (typeof source === "string") out.push({ source, flags: "", where });
  };
  add(configuration.wordPattern, "wordPattern");
  const indentation = objectValue(configuration.indentationRules);
  for (const key of ["increaseIndentPattern", "decreaseIndentPattern", "indentNextLinePattern", "unIndentedLinePattern"]) {
    add(indentation?.[key], `indentationRules.${key}`);
  }
  const markers = objectValue(objectValue(configuration.folding)?.markers);
  add(markers?.start, "folding.markers.start");
  add(markers?.end, "folding.markers.end");
  return out;
}

/** Runs the synchronous regex checks; returns the first problem, if any. */
export function regexListIssue(regexes: readonly MonarchRegex[]): string | undefined {
  for (const regex of regexes) {
    const issue = regexSafetyIssue(regex.source, { ignoreCase: regex.flags.includes("i"), unicode: regex.flags.includes("u") });
    if (issue) return `${regex.where}: ${issue}`;
  }
  return undefined;
}

/** Stable 32-bit FNV-1a hash (hex) of a string, for caching probe results. */
export function fnv1a(text: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < text.length; index += 1) {
    hash ^= text.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(16).padStart(8, "0");
}
