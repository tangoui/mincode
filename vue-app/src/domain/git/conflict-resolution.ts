export type GitConflictChoice = "ours" | "theirs" | "both";

export interface GitConflictBlock {
  start: number;
  end: number;
  ours: string;
  base: string;
  theirs: string;
}

const lineEnding = (value: string) => (value.includes("\r\n") ? "\r\n" : "\n");

export function parseGitConflictBlocks(raw: string): GitConflictBlock[] {
  const eol = lineEnding(raw);
  const lines = raw.split(/\r?\n/);
  const blocks: GitConflictBlock[] = [];

  for (let index = 0; index < lines.length; index += 1) {
    if (!lines[index]?.startsWith("<<<<<<<")) continue;
    const start = index;
    let separator = -1;
    let end = -1;
    let baseMarker = -1;
    for (index += 1; index < lines.length; index += 1) {
      const line = lines[index] ?? "";
      if (line.startsWith("|||||||") && separator < 0) baseMarker = index;
      else if (line.startsWith("=======") && separator < 0) separator = index;
      else if (line.startsWith(">>>>>>>") && separator >= 0) {
        end = index;
        break;
      }
    }
    if (separator < 0 || end < 0) continue;
    const oursEnd = baseMarker >= 0 ? baseMarker : separator;
    blocks.push({
      start,
      end,
      ours: lines.slice(start + 1, oursEnd).join(eol),
      base: baseMarker >= 0 ? lines.slice(baseMarker + 1, separator).join(eol) : "",
      theirs: lines.slice(separator + 1, end).join(eol),
    });
  }
  return blocks;
}

export function applyGitConflictChoice(
  raw: string,
  blockIndex: number,
  choice: GitConflictChoice,
): string {
  const blocks = parseGitConflictBlocks(raw);
  const block = blocks[blockIndex];
  if (!block) return raw;
  const eol = lineEnding(raw);
  const lines = raw.split(/\r?\n/);
  const selected = choice === "ours"
    ? block.ours
    : choice === "theirs"
      ? block.theirs
      : [block.ours, block.theirs].filter(Boolean).join(eol);
  const replacement = selected ? selected.split(eol) : [];
  lines.splice(block.start, block.end - block.start + 1, ...replacement);
  return lines.join(eol);
}

export function hasGitConflictMarkers(value: string) {
  return parseGitConflictBlocks(value).length > 0;
}
