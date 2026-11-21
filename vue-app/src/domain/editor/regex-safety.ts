/**
 * Static ReDoS heuristics for plugin-contributed regular expressions (Monarch
 * grammars and language configurations). This is the second of three layers:
 * the Go host bounds sizes and shapes, this module rejects the classic
 * catastrophic-backtracking shapes synchronously, and a timed worker probe
 * (services/regex-probe.ts) exercises whatever passes on adversarial input.
 *
 * The analysis parses JavaScript regex syntax into a small AST whose
 * character sets are over-approximated (an ASCII bitset plus a single
 * "any non-ASCII" flag), so overlap checks err on the side of rejecting. It
 * flags, inside any quantified group that repeats (max > 1):
 * - alternatives of the repeated body whose first characters overlap
 *   (`(a|aa)*`);
 * - a repeated element whose first characters overlap what may follow it,
 *   where the follow wraps around to the next iteration (`(a+)+`,
 *   `(\w+\s?)*`, `(.*a)*`, `((ab)*)*`).
 * Backreferences are always rejected. Pure: no DOM, Monaco or Vue.
 */

export interface RegexFlags {
  ignoreCase?: boolean;
  unicode?: boolean;
}

interface CharSet {
  /** Code points 0..127. */
  bits: Uint32Array;
  /** Any code point >= 128. */
  other: boolean;
}

type Node =
  | { t: "set"; set: CharSet }
  | { t: "seq"; items: Node[] }
  | { t: "alt"; alts: Node[] }
  | { t: "rep"; body: Node; min: number; max: number }
  | { t: "empty" }
  | { t: "backref" };

function emptySet(): CharSet {
  return { bits: new Uint32Array(4), other: false };
}

function fullSet(): CharSet {
  return { bits: new Uint32Array([0xffffffff, 0xffffffff, 0xffffffff, 0xffffffff]), other: true };
}

function addCode(set: CharSet, code: number) {
  if (code < 128) set.bits[code >> 5] |= 1 << (code & 31);
  else set.other = true;
}

function addRange(set: CharSet, from: number, to: number) {
  for (let code = from; code <= Math.min(to, 127); code += 1) addCode(set, code);
  if (to >= 128) set.other = true;
}

function union(target: CharSet, source: CharSet): CharSet {
  for (let index = 0; index < 4; index += 1) target.bits[index] |= source.bits[index];
  target.other ||= source.other;
  return target;
}

function overlaps(a: CharSet, b: CharSet): boolean {
  if (a.other && b.other) return true;
  for (let index = 0; index < 4; index += 1) if (a.bits[index] & b.bits[index]) return true;
  return false;
}

function complement(set: CharSet): CharSet {
  const result = emptySet();
  for (let index = 0; index < 4; index += 1) result.bits[index] = ~set.bits[index] >>> 0;
  // Over-approximate: a negated class may still match non-ASCII.
  result.other = true;
  return result;
}

function foldCase(set: CharSet) {
  for (let code = 65; code <= 90; code += 1) {
    const upper = (set.bits[code >> 5] >> (code & 31)) & 1;
    const lower = (set.bits[(code + 32) >> 5] >> ((code + 32) & 31)) & 1;
    if (upper || lower) {
      addCode(set, code);
      addCode(set, code + 32);
    }
  }
}

function classSet(kind: string): CharSet {
  const set = emptySet();
  switch (kind.toLowerCase()) {
    case "d":
      addRange(set, 48, 57);
      break;
    case "w":
      addRange(set, 48, 57);
      addRange(set, 65, 90);
      addRange(set, 97, 122);
      addCode(set, 95);
      break;
    case "s":
      addRange(set, 9, 13);
      addCode(set, 32);
      set.other = true;
      break;
  }
  return kind === kind.toUpperCase() ? complement(set) : set;
}

function dotSet(): CharSet {
  const set = fullSet();
  set.bits[0] &= ~((1 << 10) | (1 << 13));
  return set;
}

class RegexSyntaxError extends Error {}

class Parser {
  private index = 0;
  readonly lookarounds: Node[] = [];
  private readonly source: string;
  private readonly flags: RegexFlags;

  constructor(source: string, flags: RegexFlags) {
    this.source = source;
    this.flags = flags;
  }

  parse(): Node {
    const node = this.alternation();
    if (this.index < this.source.length) throw new RegexSyntaxError("unbalanced parenthesis");
    return node;
  }

  private peek(offset = 0) {
    return this.source[this.index + offset];
  }

  private readCode(): number {
    const code = this.flags.unicode ? this.source.codePointAt(this.index)! : this.source.charCodeAt(this.index);
    this.index += code > 0xffff ? 2 : 1;
    return code;
  }

  private literal(code: number): Node {
    const set = emptySet();
    addCode(set, code);
    if (this.flags.ignoreCase) foldCase(set);
    return { t: "set", set };
  }

  private alternation(): Node {
    const alts = [this.sequence()];
    while (this.peek() === "|") {
      this.index += 1;
      alts.push(this.sequence());
    }
    return alts.length === 1 ? alts[0] : { t: "alt", alts };
  }

  private sequence(): Node {
    const items: Node[] = [];
    while (this.index < this.source.length && this.peek() !== "|" && this.peek() !== ")") {
      items.push(this.quantified(this.atom()));
    }
    return items.length === 1 ? items[0] : { t: "seq", items };
  }

  private quantified(atom: Node): Node {
    let min: number;
    let max: number;
    const char = this.peek();
    if (char === "*") [min, max] = [0, Infinity];
    else if (char === "+") [min, max] = [1, Infinity];
    else if (char === "?") [min, max] = [0, 1];
    else if (char === "{") {
      const match = /^\{(\d+)(,(\d*))?\}/.exec(this.source.slice(this.index));
      if (!match) return atom;
      min = Number(match[1]);
      max = match[2] === undefined ? min : match[3] === "" ? Infinity : Number(match[3]);
      this.index += match[0].length - 1;
    } else return atom;
    this.index += 1;
    if (this.peek() === "?") this.index += 1;
    return { t: "rep", body: atom, min, max };
  }

  private atom(): Node {
    const char = this.peek();
    if (char === "(") {
      this.index += 1;
      let lookaround = false;
      if (this.peek() === "?") {
        const rest = this.source.slice(this.index);
        if (rest.startsWith("?:")) this.index += 2;
        else if (rest.startsWith("?=") || rest.startsWith("?!")) {
          this.index += 2;
          lookaround = true;
        } else if (rest.startsWith("?<=") || rest.startsWith("?<!")) {
          this.index += 3;
          lookaround = true;
        } else if (rest.startsWith("?<")) {
          const end = this.source.indexOf(">", this.index);
          if (end < 0) throw new RegexSyntaxError("unterminated group name");
          this.index = end + 1;
        } else throw new RegexSyntaxError("unsupported group");
      }
      const body = this.alternation();
      if (this.peek() !== ")") throw new RegexSyntaxError("unterminated group");
      this.index += 1;
      if (lookaround) {
        this.lookarounds.push(body);
        return { t: "empty" };
      }
      return body;
    }
    if (char === "[") return this.characterClass();
    if (char === ".") {
      this.index += 1;
      return { t: "set", set: dotSet() };
    }
    if (char === "^" || char === "$") {
      this.index += 1;
      return { t: "empty" };
    }
    if (char === "\\") return this.escape();
    return this.literal(this.readCode());
  }

  /** Parses an escape outside a class; returns a set, an assertion or a backreference. */
  private escape(): Node {
    this.index += 1;
    const char = this.peek();
    if (char === undefined) throw new RegexSyntaxError("trailing backslash");
    if (/[dDwWsS]/.test(char)) {
      this.index += 1;
      return { t: "set", set: classSet(char) };
    }
    if (char === "b" || char === "B") {
      this.index += 1;
      return { t: "empty" };
    }
    if (/[1-9]/.test(char)) {
      this.index += 1;
      return { t: "backref" };
    }
    if (char === "k" && this.peek(1) === "<") return { t: "backref" };
    return this.escapedCharacter(false);
  }

  /** Parses the escape after `\` as a single character set (shared with classes). */
  private escapedCharacter(inClass: boolean): Node {
    const char = this.peek()!;
    const simple: Record<string, number> = { n: 10, r: 13, t: 9, v: 11, f: 12, "0": 0 };
    if (inClass && char === "b") {
      this.index += 1;
      return this.literal(8);
    }
    if (char in simple && !(char === "0" && /\d/.test(this.peek(1) ?? ""))) {
      this.index += 1;
      return this.literal(simple[char]);
    }
    if (char === "x" && /^[0-9a-fA-F]{2}$/.test(this.source.slice(this.index + 1, this.index + 3))) {
      const code = parseInt(this.source.slice(this.index + 1, this.index + 3), 16);
      this.index += 3;
      return this.literal(code);
    }
    if (char === "u") {
      const braced = this.flags.unicode ? /^u\{([0-9a-fA-F]+)\}/.exec(this.source.slice(this.index)) : null;
      if (braced) {
        this.index += braced[0].length;
        return this.literal(parseInt(braced[1], 16));
      }
      const plain = /^u([0-9a-fA-F]{4})/.exec(this.source.slice(this.index));
      if (plain) {
        this.index += 5;
        return this.literal(parseInt(plain[1], 16));
      }
    }
    if (char === "c" && /[A-Za-z]/.test(this.peek(1) ?? "")) {
      const code = this.source.charCodeAt(this.index + 1) % 32;
      this.index += 2;
      return this.literal(code);
    }
    if ((char === "p" || char === "P") && this.flags.unicode) {
      const end = this.source.indexOf("}", this.index);
      if (end < 0) throw new RegexSyntaxError("unterminated property escape");
      this.index = end + 1;
      return { t: "set", set: fullSet() };
    }
    return this.literal(this.readCode());
  }

  private characterClass(): Node {
    this.index += 1;
    const negated = this.peek() === "^";
    if (negated) this.index += 1;
    const set = emptySet();
    while (this.index < this.source.length && this.peek() !== "]") {
      const from = this.classAtom();
      if (this.peek() === "-" && this.peek(1) !== "]" && this.peek(1) !== undefined && typeof from === "number") {
        this.index += 1;
        const to = this.classAtom();
        if (typeof to === "number") {
          addRange(set, Math.min(from, to), Math.max(from, to));
          continue;
        }
        addCode(set, from);
        addCode(set, 45);
        union(set, to);
        continue;
      }
      if (typeof from === "number") addCode(set, from);
      else union(set, from);
    }
    if (this.peek() !== "]") throw new RegexSyntaxError("unterminated character class");
    this.index += 1;
    if (this.flags.ignoreCase) foldCase(set);
    return { t: "set", set: negated ? complement(set) : set };
  }

  /** A class member: a single code point, or a set for `\d`-style escapes. */
  private classAtom(): number | CharSet {
    if (this.peek() !== "\\") return this.readCode();
    this.index += 1;
    const char = this.peek();
    if (char === undefined) throw new RegexSyntaxError("trailing backslash");
    if (/[dDwWsS]/.test(char)) {
      this.index += 1;
      return classSet(char);
    }
    const node = this.escapedCharacter(true);
    if (node.t !== "set") return fullSet();
    // escapedCharacter returns a one-code-point set unless it was \p{…}.
    const codes: number[] = [];
    for (let code = 0; code < 128; code += 1) if ((node.set.bits[code >> 5] >> (code & 31)) & 1) codes.push(code);
    return codes.length === 1 && !node.set.other ? codes[0] : node.set;
  }
}

function nullable(node: Node): boolean {
  switch (node.t) {
    case "set":
      return false;
    case "seq":
      return node.items.every(nullable);
    case "alt":
      return node.alts.some(nullable);
    case "rep":
      return node.min === 0 || nullable(node.body);
    default:
      return true;
  }
}

function firstSet(node: Node): CharSet {
  switch (node.t) {
    case "set":
      return node.set;
    case "seq": {
      const result = emptySet();
      for (const item of node.items) {
        union(result, firstSet(item));
        if (!nullable(item)) break;
      }
      return result;
    }
    case "alt":
      return node.alts.reduce((result, alt) => union(result, firstSet(alt)), emptySet());
    case "rep":
      return firstSet(node.body);
    default:
      return emptySet();
  }
}

function hasBackreference(node: Node): boolean {
  switch (node.t) {
    case "backref":
      return true;
    case "seq":
      return node.items.some(hasBackreference);
    case "alt":
      return node.alts.some(hasBackreference);
    case "rep":
      return hasBackreference(node.body);
    default:
      return false;
  }
}

/** Returns why `node` can backtrack catastrophically, given what may follow it. */
function ambiguity(node: Node, follow: CharSet, inLoop: boolean): string | undefined {
  switch (node.t) {
    case "seq":
      for (let index = 0; index < node.items.length; index += 1) {
        const rest = node.items.slice(index + 1);
        const next = firstSet({ t: "seq", items: rest });
        if (rest.every(nullable)) union(next, follow);
        const found = ambiguity(node.items[index], next, inLoop);
        if (found) return found;
      }
      return undefined;
    case "alt":
      for (const alt of node.alts) {
        const found = ambiguity(alt, follow, inLoop);
        if (found) return found;
      }
      return undefined;
    case "rep": {
      if (node.max <= 1) return ambiguity(node.body, follow, inLoop);
      const bodyFirst = firstSet(node.body);
      if (inLoop && overlaps(bodyFirst, follow)) {
        return "nested quantifier: a repeated part can also match what follows it";
      }
      if (node.body.t === "alt") {
        const firsts = node.body.alts.map(firstSet);
        for (let a = 0; a < firsts.length; a += 1) {
          for (let b = a + 1; b < firsts.length; b += 1) {
            if (overlaps(firsts[a], firsts[b])) return "repeated alternatives overlap";
          }
        }
      }
      return ambiguity(node.body, union(union(emptySet(), bodyFirst), follow), true);
    }
    default:
      return undefined;
  }
}

/**
 * Checks one regex source. Returns undefined when it compiles and passes the
 * heuristics, else a short reason. Syntax the parser does not model is left
 * to the worker probe (returns undefined once `new RegExp` accepts it).
 */
export function regexSafetyIssue(source: string, flags: RegexFlags = {}): string | undefined {
  try {
    new RegExp(source, `${flags.ignoreCase ? "i" : ""}${flags.unicode ? "u" : ""}`);
  } catch (error) {
    return `invalid regular expression: ${error instanceof Error ? error.message : String(error)}`;
  }
  let root: Node;
  let parser: Parser;
  try {
    parser = new Parser(source, flags);
    root = parser.parse();
  } catch {
    return undefined;
  }
  const trees = [root, ...parser.lookarounds];
  if (trees.some(hasBackreference)) return "backreferences are not supported";
  for (const tree of trees) {
    const found = ambiguity(tree, emptySet(), false);
    if (found) return `possible catastrophic backtracking (${found})`;
  }
  return undefined;
}

/** One regex to exercise in the worker probe. */
export interface RegexProbeCase {
  source: string;
  flags: string;
}

const PROBE_REPEAT = 64;
const PROBE_BASE_CHARS = ["a", "0", " ", "_", ".", '"', "'", "\t"];
const PROBE_MAX_CHARS = 24;

/** Adversarial inputs for one regex: runs of candidate characters followed by a non-matching tail. */
export function regexProbeInputs(source: string): string[] {
  const chars = new Set(PROBE_BASE_CHARS);
  for (const char of source.replace(/\\[dDwWsSbBnrtvf]/g, "")) {
    if (chars.size >= PROBE_MAX_CHARS) break;
    if (/[\x21-\x7e]/.test(char) && !/[\\()[\]{}|*+?^$]/.test(char)) chars.add(char);
  }
  const list = [...chars];
  const inputs = list.map((char) => `${char.repeat(PROBE_REPEAT)}\u0001`);
  for (let index = 0; index + 1 < list.length; index += 1) {
    inputs.push(`${(list[index] + list[index + 1]).repeat(PROBE_REPEAT / 2)}\u0001`);
  }
  return inputs;
}

/** Runs every case on its adversarial inputs (the worker calls this; a hang is the signal). */
export function runRegexProbe(cases: readonly RegexProbeCase[]): number {
  let executed = 0;
  for (const probe of cases) {
    let regex: RegExp;
    try {
      regex = new RegExp(probe.source, probe.flags);
    } catch {
      continue;
    }
    for (const input of regexProbeInputs(probe.source)) {
      regex.lastIndex = 0;
      regex.exec(input);
      executed += 1;
    }
  }
  return executed;
}
